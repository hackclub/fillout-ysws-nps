package httplog

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureLog redirects the standard logger into a buffer for the duration of a
// test and returns everything written to it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	flags, prefix, out := log.Flags(), log.Prefix(), log.Writer()
	log.SetFlags(0)
	log.SetPrefix("")
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetFlags(flags)
		log.SetPrefix(prefix)
		log.SetOutput(out)
	})
	return &buf
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	Middleware(h).ServeHTTP(rec, req)
	return rec
}

func TestLogsMethodPathAndStatus(t *testing.T) {
	buf := captureLog(t)

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}), httptest.NewRequest(http.MethodPost, "/sync/start", nil))

	got := buf.String()
	for _, want := range []string{"POST", `"/sync/start"`, "status=418", "dur="} {
		if !strings.Contains(got, want) {
			t.Errorf("log line %q missing %q", got, want)
		}
	}
}

func TestLogsImplicitOK(t *testing.T) {
	buf := captureLog(t)

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}), httptest.NewRequest(http.MethodGet, "/", nil))

	if got := buf.String(); !strings.Contains(got, "status=200") {
		t.Errorf("log line %q does not record the implicit 200", got)
	}
}

// A denial must name the user and the reason: that is the whole point of this
// package. A 403 with no explanation is what made the "forbidden" incident
// impossible to diagnose from the logs.
func TestLogsUserAndReason(t *testing.T) {
	buf := captureLog(t)

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetUser(r, "clay@example.com")
		Note(r, "cannot manage sync job %d (owner %s)", 20, "zach@example.com")
		http.Error(w, "forbidden", http.StatusForbidden)
	}), httptest.NewRequest(http.MethodPost, "/sync/start", nil))

	got := buf.String()
	for _, want := range []string{"status=403", `user="clay@example.com"`, `reason="cannot manage sync job 20 (owner zach@example.com)"`} {
		if !strings.Contains(got, want) {
			t.Errorf("log line %q missing %q", got, want)
		}
	}
}

// The OAuth callback carries a single-use authorization code in its query
// string. Access logs are shipped off-box, so the query must never be logged.
func TestNeverLogsQueryString(t *testing.T) {
	buf := captureLog(t)

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		httptest.NewRequest(http.MethodGet, "/callback?code=super-secret-code&state=abc123", nil))

	got := buf.String()
	if strings.Contains(got, "super-secret-code") || strings.Contains(got, "abc123") {
		t.Errorf("log line leaks the OAuth query string: %q", got)
	}
	if !strings.Contains(got, `"/callback"`) {
		t.Errorf("log line %q does not record the path", got)
	}
}

// A request path is attacker-controlled and percent-decoding can put newlines in
// it, so it must be quoted rather than interpolated raw — otherwise a request
// can forge additional log lines.
func TestQuotesPathToPreventLogInjection(t *testing.T) {
	buf := captureLog(t)

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		httptest.NewRequest(http.MethodGet, "/evil%0Astatus=200%20user=admin", nil))

	got := buf.String()
	if strings.Count(strings.TrimRight(got, "\n"), "\n") != 0 {
		t.Errorf("log output spans multiple lines, path was not escaped: %q", got)
	}
	if !strings.Contains(got, `\n`) {
		t.Errorf("log line %q does not escape the newline in the path", got)
	}
}

// The container health check polls /healthz every few seconds; logging each one
// would bury everything that matters. Failures still get logged.
func TestSkipsHealthyNoiseButLogsFailures(t *testing.T) {
	buf := captureLog(t)
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})

	serve(ok, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	serve(ok, httptest.NewRequest(http.MethodGet, "/static/app.css", nil))
	if got := buf.String(); got != "" {
		t.Errorf("healthy noise was logged: %q", got)
	}

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := buf.String(); !strings.Contains(got, "status=500") {
		t.Errorf("failing health check was not logged: %q", got)
	}
}

// Handlers are unit-tested directly, without the middleware wrapped around
// them. Note and SetUser must be safe no-ops there.
func TestNoteAndSetUserWithoutMiddleware(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	SetUser(req, "nobody@example.com")
	Note(req, "no middleware here")
}

func TestPreservesResponseBodyAndStatus(t *testing.T) {
	captureLog(t)

	rec := serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("body"))
	}), httptest.NewRequest(http.MethodGet, "/", nil))

	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusCreated)
	}
	if got := rec.Body.String(); got != "body" {
		t.Errorf("body = %q, want %q", got, "body")
	}
}

// Session state is re-checked several times while serving one request, so the
// same refusal arrives more than once. It should be reported once — but a
// genuinely different second reason must still be kept.
func TestReasonsAreDedupedButNotCollapsed(t *testing.T) {
	buf := captureLog(t)

	serve(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Note(r, "session for %q expired", "user@example.com")
		Note(r, "session for %q expired", "user@example.com")
		Note(r, "and the allow-list was empty")
		http.Error(w, "nope", http.StatusForbidden)
	}), httptest.NewRequest(http.MethodGet, "/", nil))

	got := buf.String()
	if n := strings.Count(got, "expired"); n != 1 {
		t.Errorf("duplicate reason logged %d times, want 1: %q", n, got)
	}
	if !strings.Contains(got, "and the allow-list was empty") {
		t.Errorf("second, distinct reason was dropped: %q", got)
	}
}
