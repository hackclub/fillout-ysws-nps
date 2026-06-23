package fillout

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewClientRateLimitDefaults(t *testing.T) {
	c := NewClient("key")
	if c.limiter == nil {
		t.Fatal("default limiter is nil")
	}
	// Pace just under Fillout's 5;w=1 window, with no burst, so aligned pollers
	// can never put more than the limit into a single 1-second window.
	if got := float64(c.limiter.Limit()); got != defaultRateLimit {
		t.Errorf("limiter rate = %v, want %v", got, float64(defaultRateLimit))
	}
	if got := c.limiter.Burst(); got != defaultBurst {
		t.Errorf("limiter burst = %d, want %d", got, defaultBurst)
	}
	if defaultBurst != 1 {
		t.Errorf("defaultBurst = %d, want 1 (a larger burst re-introduces the 429 storm)", defaultBurst)
	}
	if c.maxRetries != defaultMaxRetries {
		t.Errorf("maxRetries = %d, want %d", c.maxRetries, defaultMaxRetries)
	}
}

func TestWithMaxRetries(t *testing.T) {
	c := NewClient("key", WithMaxRetries(5))
	if c.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", c.maxRetries)
	}
	// Negative values are clamped to 0 (retries disabled), not left negative.
	c = NewClient("key", WithMaxRetries(-3))
	if c.maxRetries != 0 {
		t.Errorf("maxRetries = %d, want 0 (clamped)", c.maxRetries)
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header map[string]string
		want   time.Duration
	}{
		{"retry-after seconds", map[string]string{"Retry-After": "2"}, 2 * time.Second},
		{"retry-after floored", map[string]string{"Retry-After": "0"}, defaultRetryWait},
		{"ratelimit-reset seconds", map[string]string{"RateLimit-Reset": "3"}, 3 * time.Second},
		{"ratelimit-reset floored", map[string]string{"RateLimit-Reset": "0"}, defaultRetryWait},
		{"retry-after wins", map[string]string{"Retry-After": "5", "RateLimit-Reset": "1"}, 5 * time.Second},
		{"skip unparseable, use reset", map[string]string{"Retry-After": "soon", "RateLimit-Reset": "2"}, 2 * time.Second},
		{"skip negative, use reset", map[string]string{"Retry-After": "-1", "RateLimit-Reset": "4"}, 4 * time.Second},
		{"no headers", nil, defaultRetryWait},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			for k, v := range tt.header {
				h.Set(k, v)
			}
			if got := retryAfter(h); got != tt.want {
				t.Errorf("retryAfter(%v) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

// retryTestClient returns a test client with retries enabled and an instant,
// recording sleep so backoff doesn't slow the test. The returned slice collects
// every wait the client would have slept for.
func retryTestClient(t *testing.T, handler http.HandlerFunc, maxRetries int) (*Client, *[]time.Duration) {
	t.Helper()
	c := newTestClient(t, handler)
	c.maxRetries = maxRetries
	var sleeps []time.Duration
	c.sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		return ctx.Err()
	}
	return c, &sleeps
}

func TestDoRetriesOn429ThenSucceeds(t *testing.T) {
	var hits int32
	c, sleeps := retryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) <= 2 {
			w.Header().Set("RateLimit-Reset", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"message":"slow down"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}, 3)

	var out struct {
		OK bool `json:"ok"`
	}
	if err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, &out); err != nil {
		t.Fatalf("do: %v", err)
	}
	if !out.OK {
		t.Error("response not decoded after retries")
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("server hits = %d, want 3 (2 x 429 + 1 success)", got)
	}
	if len(*sleeps) != 2 {
		t.Fatalf("slept %d times, want 2", len(*sleeps))
	}
	for i, d := range *sleeps {
		if d != defaultRetryWait {
			t.Errorf("sleep[%d] = %v, want %v (reset:0 floored)", i, d, defaultRetryWait)
		}
	}
}

func TestDoRetriesExhaustedReturns429(t *testing.T) {
	var hits int32
	c, sleeps := retryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"slow down"}`))
	}, 2)

	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if !IsRateLimited(err) {
		t.Fatalf("err = %v, want a 429 APIError", err)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Errorf("server hits = %d, want 3 (1 initial + 2 retries)", got)
	}
	if len(*sleeps) != 2 {
		t.Errorf("slept %d times, want 2", len(*sleeps))
	}
}

func TestDoDoesNotRetryNon429(t *testing.T) {
	var hits int32
	c, sleeps := retryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}, 3)

	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if !HasStatus(err, http.StatusInternalServerError) {
		t.Fatalf("err = %v, want a 500 APIError", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (500 is not retried)", got)
	}
	if len(*sleeps) != 0 {
		t.Errorf("slept %d times, want 0", len(*sleeps))
	}
}

func TestDoRetryStopsOnContextCancel(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c.maxRetries = 3
	c.sleep = func(ctx context.Context, d time.Duration) error { return context.Canceled }

	err := c.do(context.Background(), http.MethodGet, "/x", nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (cancel during backoff stops retries)", got)
	}
}

func TestDoResendsBodyOnRetry(t *testing.T) {
	var hits int32
	var bodies []string
	c, _ := retryTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(buf))
		if atomic.AddInt32(&hits, 1) <= 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}, 3)

	body := map[string]string{"hello": "world"}
	if err := c.do(context.Background(), http.MethodPost, "/x", nil, body, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d request bodies, want 2", len(bodies))
	}
	want := `{"hello":"world"}`
	for i, b := range bodies {
		if b != want {
			t.Errorf("body[%d] = %q, want %q (body must be resent on retry)", i, b, want)
		}
	}
}
