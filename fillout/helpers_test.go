package fillout

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recordedRequest captures the parts of an incoming request a test cares about.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

// newTestClient spins up an httptest server backed by handler and returns a
// Client pointed at it. The server is closed via t.Cleanup. The rate limiter is
// disabled, and 429 retries are off, so tests don't pay limiter or backoff
// latency unless they opt in (e.g. via WithMaxRetries).
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClient("test-key", WithBaseURL(srv.URL), WithRateLimit(0, 0), WithMaxRetries(0))
}

// jsonHandler responds with status and the given JSON body, recording the
// request into rec.
func jsonHandler(t *testing.T, rec *recordedRequest, status int, body string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if rec != nil {
			buf, _ := io.ReadAll(r.Body)
			*rec = recordedRequest{
				Method: r.Method,
				Path:   r.URL.Path,
				Query:  r.URL.RawQuery,
				Header: r.Header.Clone(),
				Body:   string(buf),
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}
