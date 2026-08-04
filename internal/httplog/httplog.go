// Package httplog writes one line per HTTP request and lets handlers attach the
// reason a request ended the way it did.
//
// It exists because of a real incident: a user hit a bare "forbidden" at the end
// of the new-form flow and the server recorded nothing at all — no request log,
// no audit row — so the cause had to be reconstructed from gaps in a Postgres
// identity sequence. Any denial should instead leave one greppable line naming
// the user, the request, and why it was refused.
//
// Notes and users are attached through the request context, so a handler deep in
// the stack can explain itself without threading a logger through every call.
// Both are no-ops when the middleware is not installed, which keeps handler unit
// tests free of scaffolding.
package httplog

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

type contextKey struct{}

// recordKey identifies the per-request record in a request's context.
var recordKey contextKey

// record accumulates what the log line for one in-flight request will say. Its
// mutex guards against a handler that reports from a goroutine it spawned.
type record struct {
	mu     sync.Mutex
	status int
	user   string
	reason string
}

func (rec *record) setStatus(code int) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.status = code
}

func (rec *record) setUser(email string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.user = email
}

// addReason appends a reason, so a request that is refused for more than one
// reason reports all of them rather than only the last. Repeats are dropped:
// session state is re-checked several times per request, and a failure there
// would otherwise be reported once per check.
func (rec *record) addReason(reason string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.reason == "" {
		rec.reason = reason
		return
	}
	for _, existing := range strings.Split(rec.reason, "; ") {
		if existing == reason {
			return
		}
	}
	rec.reason += "; " + reason
}

func (rec *record) snapshot() (status int, user, reason string) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.status, rec.user, rec.reason
}

// statusWriter records the response status. A handler that never calls
// WriteHeader implicitly sends 200, which is the record's initial value.
type statusWriter struct {
	http.ResponseWriter
	rec         *record
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.rec.setStatus(code)
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.wroteHeader = true
	return w.ResponseWriter.Write(b)
}

// Middleware logs one line per request. Wrap it around the whole mux, outside
// every other middleware, so nothing escapes unlogged.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &record{status: http.StatusOK}
		sw := &statusWriter{ResponseWriter: w, rec: rec}
		start := time.Now()

		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), recordKey, rec)))

		logRequest(r, rec, time.Since(start))
	})
}

// Note records why a request is ending the way it is: an authorization refusal,
// a rejected login, a request that fell through to a fallback. It is the field
// worth grepping for when something is mysteriously denied.
func Note(r *http.Request, format string, args ...any) {
	if rec, ok := r.Context().Value(recordKey).(*record); ok {
		rec.addReason(fmt.Sprintf(format, args...))
	}
}

// SetUser attaches the authenticated user's email to this request's log line.
func SetUser(r *http.Request, email string) {
	if email == "" {
		return
	}
	if rec, ok := r.Context().Value(recordKey).(*record); ok {
		rec.setUser(email)
	}
}

// logRequest writes the line. The path and every attached string are quoted:
// percent-decoding can put newlines into a path, and an unquoted one would let a
// request forge extra log lines. The query string is never logged — the OAuth
// callback carries a single-use authorization code there.
func logRequest(r *http.Request, rec *record, took time.Duration) {
	status, user, reason := rec.snapshot()
	if !worthLogging(r.URL.Path, status) {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "http %s %q status=%d dur=%s", r.Method, r.URL.Path, status, took.Round(time.Millisecond))
	if user != "" {
		fmt.Fprintf(&b, " user=%q", user)
	}
	if reason != "" {
		fmt.Fprintf(&b, " reason=%q", reason)
	}
	log.Print(b.String())
}

// worthLogging drops the traffic that would bury everything else: the container
// health check runs every few seconds and static assets are fetched on every
// page load. Both are still logged when they fail.
func worthLogging(path string, status int) bool {
	if status >= 400 {
		return true
	}
	return path != "/healthz" && !strings.HasPrefix(path, "/static/")
}
