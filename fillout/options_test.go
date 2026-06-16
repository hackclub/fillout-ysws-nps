package fillout

import (
	"net/http"
	"testing"
	"time"
)

func TestNewClientDefaults(t *testing.T) {
	c := NewClient("key")
	if c.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, DefaultBaseURL)
	}
	if c.apiKey != "key" {
		t.Errorf("apiKey = %q", c.apiKey)
	}
	if c.limiter == nil {
		t.Error("default limiter is nil, want a rate limiter")
	}
	if c.userAgent != defaultUserAgent {
		t.Errorf("userAgent = %q, want %q", c.userAgent, defaultUserAgent)
	}
}

func TestOptions(t *testing.T) {
	hc := &http.Client{Timeout: 5 * time.Second}
	c := NewClient("key",
		WithBaseURL(EUBaseURL+"/"),
		WithHTTPClient(hc),
		WithUserAgent("custom/1"),
		WithRateLimit(0, 0),
	)
	if c.baseURL != EUBaseURL {
		t.Errorf("baseURL = %q, want %q (trailing slash trimmed)", c.baseURL, EUBaseURL)
	}
	if c.httpClient != hc {
		t.Error("WithHTTPClient did not set the client")
	}
	if c.userAgent != "custom/1" {
		t.Errorf("userAgent = %q", c.userAgent)
	}
	if c.limiter != nil {
		t.Error("WithRateLimit(0,0) should disable the limiter")
	}
}

func TestOptionsIgnoreZeroValues(t *testing.T) {
	c := NewClient("key", WithHTTPClient(nil), WithUserAgent(""))
	if c.httpClient == nil {
		t.Error("WithHTTPClient(nil) should leave the default client intact")
	}
	if c.userAgent != defaultUserAgent {
		t.Errorf("WithUserAgent(\"\") should leave the default; got %q", c.userAgent)
	}
}

func TestWithRateLimitClampsBurst(t *testing.T) {
	c := NewClient("key", WithRateLimit(10, 0))
	if c.limiter == nil {
		t.Fatal("limiter is nil")
	}
	if got := c.limiter.Burst(); got != 1 {
		t.Errorf("burst = %d, want 1 (clamped)", got)
	}
}

func TestAPIErrorMessageFormats(t *testing.T) {
	statusOnly := &APIError{StatusCode: http.StatusBadGateway}
	if got := statusOnly.Error(); got != "fillout: 502 Bad Gateway" {
		t.Errorf("status-only Error() = %q", got)
	}
	withMsg := &APIError{StatusCode: http.StatusBadRequest, Message: "nope"}
	if got := withMsg.Error(); got != "fillout: 400 Bad Request: nope" {
		t.Errorf("with-message Error() = %q", got)
	}
}
