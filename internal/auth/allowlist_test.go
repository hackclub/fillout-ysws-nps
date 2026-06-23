package auth

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/airtable"
)

// fakeLister is a stub recordLister that records how it was called so tests can
// assert both behavior and, crucially, that the login email is never sent to
// Airtable.
type fakeLister struct {
	mu       sync.Mutex
	calls    int
	records  []airtable.Record
	err      error
	gotTable []string
	gotOpts  []*airtable.ListOptions
}

func (f *fakeLister) ListRecords(_ context.Context, table string, opts *airtable.ListOptions) ([]airtable.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.gotTable = append(f.gotTable, table)
	f.gotOpts = append(f.gotOpts, opts)
	if f.err != nil {
		return nil, f.err
	}
	return f.records, nil
}

func (f *fakeLister) set(records []airtable.Record, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records, f.err = records, err
}

func (f *fakeLister) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// authorRecords builds YSWS Authors records carrying the given Hack Club Auth
// Email cell values.
func authorRecords(emails ...string) []airtable.Record {
	recs := make([]airtable.Record, len(emails))
	for i, e := range emails {
		recs[i] = airtable.Record{
			ID:     "rec" + strconv.Itoa(i),
			Fields: map[string]any{AuthorEmailField: e},
		}
	}
	return recs
}

// fakeClock is a manually advanced clock for exercising cache TTLs.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAllowlist_StaticEmailAllowedWithoutAirtable(t *testing.T) {
	f := &fakeLister{records: authorRecords()}
	l := NewAllowlist([]string{"Zach@Hackclub.com"}, f)

	for _, in := range []string{"zach@hackclub.com", "  ZACH@hackclub.com "} {
		if !l.Allowed(in) {
			t.Errorf("Allowed(%q) = false, want true (static list)", in)
		}
	}
	if got := f.callCount(); got != 0 {
		t.Errorf("static match queried Airtable %d times, want 0", got)
	}
}

func TestAllowlist_AuthorEmailAllowed(t *testing.T) {
	f := &fakeLister{records: authorRecords("Author@Example.com")}
	l := NewAllowlist(nil, f)

	for _, in := range []string{"author@example.com", " AUTHOR@example.com "} {
		if !l.Allowed(in) {
			t.Errorf("Allowed(%q) = false, want true (author table)", in)
		}
	}
	if l.Allowed("stranger@example.com") {
		t.Error("Allowed(stranger) = true, want false")
	}
}

func TestAllowlist_QueriesCorrectTable(t *testing.T) {
	f := &fakeLister{records: authorRecords("a@x.com")}
	l := NewAllowlist(nil, f)
	l.Allowed("a@x.com")

	if len(f.gotTable) == 0 || f.gotTable[0] != AuthorsTable {
		t.Fatalf("queried table %v, want %q", f.gotTable, AuthorsTable)
	}
	if got := f.gotOpts[0].Fields; len(got) != 1 || got[0] != AuthorEmailField {
		t.Errorf("requested fields %v, want [%q]", got, AuthorEmailField)
	}
}

func TestAllowlist_CachesWithinTTL(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f := &fakeLister{records: authorRecords("a@x.com")}
	l := newAllowlist(nil, f, 5*time.Minute, 30*time.Second, clock.now)

	l.Allowed("a@x.com")     // first call fetches
	l.Allowed("a@x.com")     // served from cache
	l.Allowed("other@x.com") // a miss is still served from the same cached set
	if got := f.callCount(); got != 1 {
		t.Fatalf("within TTL fetched %d times, want 1", got)
	}

	clock.advance(5*time.Minute + time.Second)
	l.Allowed("a@x.com") // cache is stale, so refetch
	if got := f.callCount(); got != 2 {
		t.Errorf("after TTL fetched %d times, want 2", got)
	}
}

func TestAllowlist_RefreshPicksUpNewAuthors(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f := &fakeLister{records: authorRecords("old@x.com")}
	l := newAllowlist(nil, f, time.Minute, time.Second, clock.now)

	if l.Allowed("new@x.com") {
		t.Fatal("new author allowed before being added")
	}
	f.set(authorRecords("old@x.com", "new@x.com"), nil)
	if l.Allowed("new@x.com") {
		t.Fatal("new author allowed while prior set is still cached")
	}

	clock.advance(time.Minute + time.Second)
	if !l.Allowed("new@x.com") {
		t.Error("new author not allowed after cache refresh")
	}
}

func TestAllowlist_AirtableErrorKeepsLastGoodSet(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f := &fakeLister{records: authorRecords("a@x.com")}
	l := newAllowlist(nil, f, time.Minute, time.Second, clock.now)

	if !l.Allowed("a@x.com") {
		t.Fatal("author not allowed on first fetch")
	}
	f.set(nil, errors.New("airtable down"))
	clock.advance(time.Minute + time.Second) // force a refresh that will fail

	if !l.Allowed("a@x.com") {
		t.Error("known author locked out after a transient Airtable error")
	}
}

func TestAllowlist_DeniesWhenFirstFetchFails(t *testing.T) {
	f := &fakeLister{err: errors.New("airtable down")}
	l := NewAllowlist(nil, f)

	if l.Allowed("a@x.com") {
		t.Error("allowed login with no successfully fetched data")
	}
}

func TestAllowlist_ErrorBackoffAvoidsHammering(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f := &fakeLister{err: errors.New("airtable down")}
	l := newAllowlist(nil, f, time.Minute, 30*time.Second, clock.now)

	l.Allowed("a@x.com") // fetch fails
	l.Allowed("a@x.com") // within error backoff, so no refetch
	if got := f.callCount(); got != 1 {
		t.Fatalf("retried %d times within backoff, want 1", got)
	}

	clock.advance(31 * time.Second)
	l.Allowed("a@x.com") // backoff elapsed, so retry
	if got := f.callCount(); got != 2 {
		t.Errorf("fetched %d times after backoff, want 2", got)
	}
}

// TestAllowlist_NeverSendsLoginEmailToAirtable is the injection-safety guarantee:
// whatever a caller passes, the Airtable query is a constant formula naming only
// the field, never the user-supplied email. The email is matched in Go.
func TestAllowlist_NeverSendsLoginEmailToAirtable(t *testing.T) {
	f := &fakeLister{records: authorRecords("good@x.com")}
	l := NewAllowlist(nil, f)

	evil := `x") OR FIND("@", LOWER({Name})) OR FIND("`
	if l.Allowed(evil) {
		t.Error("formula-injection email was allowed")
	}

	wantFormula := fmt.Sprintf("{%s} != ''", AuthorEmailField)
	for i, opts := range f.gotOpts {
		if opts.FilterByFormula != wantFormula {
			t.Errorf("query %d formula = %q, want constant %q (user input must never reach the formula)",
				i, opts.FilterByFormula, wantFormula)
		}
	}
}

func TestAllowlist_EmptyEmailDeniedWithoutFetch(t *testing.T) {
	f := &fakeLister{records: authorRecords("a@x.com")}
	l := NewAllowlist(nil, f)

	for _, in := range []string{"", "   ", "\t"} {
		if l.Allowed(in) {
			t.Errorf("Allowed(%q) = true, want false", in)
		}
	}
	if got := f.callCount(); got != 0 {
		t.Errorf("empty email queried Airtable %d times, want 0", got)
	}
}

func TestAllowlist_HandlesMultipleEmailsInOneCell(t *testing.T) {
	// Defensive: the field is a single-email type, but tolerate a pasted list.
	f := &fakeLister{records: authorRecords("a@x.com, b@y.com")}
	l := NewAllowlist(nil, f)

	for _, in := range []string{"a@x.com", "b@y.com"} {
		if !l.Allowed(in) {
			t.Errorf("Allowed(%q) = false, want true", in)
		}
	}
}

// TestAllowlist_ConcurrentAccess exercises Allowed from many goroutines across a
// cache-refresh boundary; run with -race it guards the caching against data
// races, since the check runs on every (concurrent) request.
func TestAllowlist_ConcurrentAccess(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	f := &fakeLister{records: authorRecords("author@x.com")}
	l := newAllowlist([]string{"admin@x.com"}, f, time.Minute, time.Second, clock.now)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				l.Allowed("admin@x.com") // static, no fetch
			case 1:
				l.Allowed("author@x.com") // dynamic
			default:
				clock.advance(2 * time.Minute) // force refreshes to interleave
				l.Allowed("stranger@x.com")
			}
		}(i)
	}
	wg.Wait()

	if !l.Allowed("admin@x.com") || !l.Allowed("author@x.com") {
		t.Error("known emails should remain allowed after concurrent access")
	}
}

func TestAllowlist_IgnoresBlankAndNonStringCells(t *testing.T) {
	f := &fakeLister{records: []airtable.Record{
		{ID: "rec0", Fields: map[string]any{AuthorEmailField: ""}},
		{ID: "rec1", Fields: map[string]any{AuthorEmailField: "  "}},
		{ID: "rec2", Fields: map[string]any{}},                     // field absent
		{ID: "rec3", Fields: map[string]any{AuthorEmailField: 42}}, // unexpected type
		{ID: "rec4", Fields: map[string]any{AuthorEmailField: "ok@x.com"}},
	}}
	l := NewAllowlist(nil, f)

	if !l.Allowed("ok@x.com") {
		t.Error("valid author email not allowed")
	}
	if l.Allowed("") || l.Allowed("42") {
		t.Error("blank/non-string cell produced a spurious allowed email")
	}
}
