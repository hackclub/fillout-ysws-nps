package auth

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hackclub/fillout-ysws-nps/airtable"
)

const (
	// AuthorsTable is the Airtable table whose Hack Club Auth Email field grants
	// login access in addition to the static ALLOWED_EMAILS list.
	AuthorsTable = "YSWS Authors"
	// AuthorEmailField is the email field on AuthorsTable that grants access.
	AuthorEmailField = "Hack Club Auth Email"

	// authorRefreshTTL is how long a fetched set of author emails is reused
	// before the next check refreshes it from Airtable. The login check runs on
	// every request, so this keeps Airtable traffic bounded; the trade-off is
	// that adding or removing an author takes up to this long to take effect.
	authorRefreshTTL = 5 * time.Minute
	// authorErrorTTL is the (shorter) reuse window after a failed fetch, so a
	// transient Airtable outage is retried soon without hammering the API.
	authorErrorTTL = 30 * time.Second
	// authorFetchTimeout caps a single Airtable fetch.
	authorFetchTimeout = 10 * time.Second
)

// recordLister is the slice of the Airtable client the allow-list needs.
// *airtable.Client satisfies it; tests supply a fake.
type recordLister interface {
	ListRecords(ctx context.Context, table string, opts *airtable.ListOptions) ([]airtable.Record, error)
}

// Allowlist decides who may log in. An email is allowed if it is in the static
// list (from ALLOWED_EMAILS) or appears in the Hack Club Auth Email field of the
// YSWS Authors table. The static list is an always-available bootstrap so admins
// can still log in if Airtable is unreachable.
//
// Author emails are fetched from Airtable and cached with a TTL because the check
// runs on every authenticated request. The login email is never sent to Airtable
// (it is matched in Go against the fetched set), so there is no filterByFormula
// injection surface.
type Allowlist struct {
	static map[string]bool

	at     recordLister
	ttl    time.Duration
	errTTL time.Duration
	now    func() time.Time

	mu         sync.Mutex
	cache      map[string]bool
	validUntil time.Time
	// lastErr is the error from the most recent refresh attempt, kept so a
	// refusal can say whether the author list is trustworthy or simply failed to
	// load. An Airtable outage denies every author, and that must not look the
	// same in the log as an address that was never on the list.
	lastErr error
	// loggedCount is the author-email count reported by the last log line, so a
	// steady-state refresh every few minutes stays silent.
	loggedCount int
	logged      bool
}

// NewAllowlist builds an Allowlist from the static ALLOWED_EMAILS list and an
// Airtable client used to read the YSWS Authors table.
func NewAllowlist(static []string, at recordLister) *Allowlist {
	return newAllowlist(static, at, authorRefreshTTL, authorErrorTTL, time.Now)
}

// newAllowlist is the test seam: it takes explicit TTLs and a clock.
func newAllowlist(static []string, at recordLister, ttl, errTTL time.Duration, now func() time.Time) *Allowlist {
	set := make(map[string]bool, len(static))
	for _, e := range static {
		if n := normalizeEmail(e); n != "" {
			set[n] = true
		}
	}
	return &Allowlist{
		static: set,
		at:     at,
		ttl:    ttl,
		errTTL: errTTL,
		now:    now,
	}
}

// Allowed reports whether email may log in. It is safe for concurrent use.
func (l *Allowlist) Allowed(email string) bool {
	norm := normalizeEmail(email)
	if norm == "" {
		return false
	}
	if l.static[norm] {
		return true
	}
	return l.authorEmails()[norm]
}

// authorEmails returns the cached set of YSWS Authors emails, refreshing from
// Airtable when the cache is empty or stale. On a refresh failure it keeps
// serving the last good set so a transient outage doesn't lock out known
// authors; if no fetch has ever succeeded it returns an empty set (deny).
func (l *Allowlist) authorEmails() map[string]bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cache != nil && l.now().Before(l.validUntil) {
		return l.cache
	}

	ctx, cancel := context.WithTimeout(context.Background(), authorFetchTimeout)
	defer cancel()
	set, err := l.fetch(ctx)
	l.lastErr = err
	if err != nil {
		if l.cache == nil {
			l.cache = map[string]bool{}
		}
		l.validUntil = l.now().Add(l.errTTL)
		// Always log a failure: while it lasts, every author-only login is
		// refused, and that used to happen with no trace at all.
		log.Printf("auth: refreshing the %s allow-list failed: %v — serving %d cached author email(s); author logins are denied while this count is 0",
			AuthorsTable, err, len(l.cache))
		l.logged, l.loggedCount = false, -1
		return l.cache
	}
	l.cache = set
	l.validUntil = l.now().Add(l.ttl)
	// A healthy refresh happens every few minutes forever, so report only the
	// first one and any change in size — enough to see the list is alive and to
	// timestamp when someone was added or removed.
	if !l.logged || l.loggedCount != len(set) {
		log.Printf("auth: loaded %d author email(s) from %s", len(set), AuthorsTable)
		l.logged, l.loggedCount = true, len(set)
	}
	return l.cache
}

// Allowed and Explain together satisfy the Allower interface the Authenticator
// depends on.
var _ Allower = (*Allowlist)(nil)

// Explain describes in one line why email is not allowed to log in. It names
// both sources and how many entries each holds, so "the address is not on the
// list" is distinguishable from "the list failed to load" — the two have the
// same symptom for the user and completely different fixes.
func (l *Allowlist) Explain(email string) string {
	if normalizeEmail(email) == "" {
		return "the identity provider returned no email address"
	}

	l.mu.Lock()
	authors, err := len(l.cache), l.lastErr
	l.mu.Unlock()

	reason := fmt.Sprintf("not in ALLOWED_EMAILS (%d entries) and not among the %d %s email(s) loaded from Airtable",
		len(l.static), authors, AuthorsTable)
	if err != nil {
		reason += fmt.Sprintf("; the last allow-list refresh failed (%v), so that list may be incomplete", err)
	}
	return reason
}

// fetch reads the non-empty Hack Club Auth Email values from the YSWS Authors
// table into a normalized set. The filterByFormula names only the constant
// field - never the login email - so it carries no injection risk.
func (l *Allowlist) fetch(ctx context.Context) (map[string]bool, error) {
	recs, err := l.at.ListRecords(ctx, AuthorsTable, &airtable.ListOptions{
		Fields:          []string{AuthorEmailField},
		FilterByFormula: fmt.Sprintf("{%s} != ''", AuthorEmailField),
	})
	if err != nil {
		return nil, fmt.Errorf("auth: listing %q: %w", AuthorsTable, err)
	}
	set := make(map[string]bool, len(recs))
	for _, r := range recs {
		for _, e := range emailsFromCell(r.Fields[AuthorEmailField]) {
			if n := normalizeEmail(e); n != "" {
				set[n] = true
			}
		}
	}
	return set, nil
}

// emailsFromCell extracts email addresses from an Airtable cell value. The field
// is a single-email type, but this defensively tolerates a pasted list and the
// array shape Airtable uses for multi-value fields.
func emailsFromCell(v any) []string {
	switch t := v.(type) {
	case string:
		return splitEmails(t)
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, splitEmails(s)...)
			}
		}
		return out
	default:
		return nil
	}
}

// splitEmails splits a cell on commas, semicolons, and whitespace. Real email
// addresses contain none of these unquoted, so this only ever separates a
// pasted list into individual addresses.
func splitEmails(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || unicode.IsSpace(r)
	})
}

func normalizeEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
