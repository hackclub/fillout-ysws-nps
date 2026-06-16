package fillout

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestRequestHeaders(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `[]`))
	c.userAgent = "go-fillout-test/9"

	if _, err := c.ListForms(context.Background()); err != nil {
		t.Fatalf("ListForms: %v", err)
	}

	if got := rec.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-key")
	}
	if got := rec.Header.Get("Accept"); got != "application/json" {
		t.Errorf("Accept = %q, want application/json", got)
	}
	if got := rec.Header.Get("User-Agent"); got != "go-fillout-test/9" {
		t.Errorf("User-Agent = %q, want go-fillout-test/9", got)
	}
}

func TestGETHasNoContentTypeOrBody(t *testing.T) {
	var rec recordedRequest
	c := newTestClient(t, jsonHandler(t, &rec, http.StatusOK, `[]`))

	if _, err := c.ListForms(context.Background()); err != nil {
		t.Fatalf("ListForms: %v", err)
	}
	if got := rec.Header.Get("Content-Type"); got != "" {
		t.Errorf("Content-Type on GET = %q, want empty", got)
	}
	if rec.Body != "" {
		t.Errorf("GET body = %q, want empty", rec.Body)
	}
}

func TestAPIErrorParsesMessage(t *testing.T) {
	c := newTestClient(t, jsonHandler(t, nil, http.StatusBadRequest, `{"message":"bad form id"}`))

	_, err := c.GetForm(context.Background(), "nope")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is not *APIError: %T", err)
	}
	if apiErr.StatusCode != http.StatusBadRequest {
		t.Errorf("StatusCode = %d, want 400", apiErr.StatusCode)
	}
	if apiErr.Message != "bad form id" {
		t.Errorf("Message = %q, want %q", apiErr.Message, "bad form id")
	}
}

func TestAPIErrorClassifiers(t *testing.T) {
	tests := []struct {
		status int
		check  func(error) bool
		name   string
	}{
		{http.StatusNotFound, IsNotFound, "IsNotFound"},
		{http.StatusTooManyRequests, IsRateLimited, "IsRateLimited"},
		{http.StatusUnauthorized, IsUnauthorized, "IsUnauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, jsonHandler(t, nil, tt.status, `{}`))
			_, err := c.ListForms(context.Background())
			if err == nil {
				t.Fatal("expected error")
			}
			if !tt.check(err) {
				t.Errorf("%s returned false for status %d", tt.name, tt.status)
			}
			if HasStatus(err, http.StatusTeapot) {
				t.Error("HasStatus matched the wrong status")
			}
		})
	}
}

func TestClassifiersIgnoreNonAPIError(t *testing.T) {
	err := errors.New("plain")
	if IsNotFound(err) || IsRateLimited(err) || IsUnauthorized(err) {
		t.Error("classifier matched a non-APIError")
	}
}

func TestErrorBodyFallback(t *testing.T) {
	c := newTestClient(t, jsonHandler(t, nil, http.StatusInternalServerError, `upstream exploded`))
	_, err := c.ListForms(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("not an APIError: %v", err)
	}
	if string(apiErr.Body) != "upstream exploded" {
		t.Errorf("Body = %q", apiErr.Body)
	}
	if !contains(apiErr.Error(), "upstream exploded") {
		t.Errorf("Error() = %q, want it to include the body", apiErr.Error())
	}
}

func TestRateLimiterBlocks(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`[]`))
	})
	// One request per second, burst of one: the first call passes immediately,
	// the second must wait ~1s, so a short context deadline cancels it.
	WithRateLimit(1, 1)(c)

	if _, err := c.ListForms(context.Background()); err != nil {
		t.Fatalf("first ListForms: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.ListForms(ctx)
	if err == nil {
		t.Fatal("expected the rate limiter to block the second call")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("server hits = %d, want 1 (second call should never reach the server)", got)
	}
}

func TestContextCancellationSkipsRequest(t *testing.T) {
	var hits int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Write([]byte(`[]`))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := c.ListForms(ctx); err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Errorf("server hits = %d, want 0", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
