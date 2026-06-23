// Package fillout is a client for the Fillout REST API
// (https://www.fillout.com/help/fillout-rest-api).
//
// It covers the full public surface: listing forms, reading form metadata,
// reading/creating/deleting submissions, and managing webhooks. Create a
// Client with NewClient and call methods on it; every method takes a
// context.Context and returns a typed result or an *APIError on a non-2xx
// response.
package fillout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	// DefaultBaseURL is the US Fillout API base URL.
	DefaultBaseURL = "https://api.fillout.com/v1/api"
	// EUBaseURL is the EU Fillout API base URL.
	EUBaseURL = "https://eu-api.fillout.com/v1/api"

	// defaultRateLimit is the sustained client-side request rate. Fillout's hard
	// limit is 5 requests per 1-second window per API key (the live API
	// advertises it as `ratelimit-policy: 5;w=1`). We pace just below that so
	// brief scheduling jitter — e.g. many sync pollers waking on aligned tickers
	// — can't tip a single 1-second window over the limit.
	defaultRateLimit = 4

	// defaultBurst is the limiter burst size. It is deliberately 1: a larger
	// burst lets aligned callers fire several requests in the same instant, which
	// overflows Fillout's per-second window and triggers 429s even when the
	// sustained rate is safe. Burst 1 forces strict pacing.
	defaultBurst = 1

	// defaultMaxRetries is how many times a request is retried after a 429
	// response before the error is surfaced. Retries honor the server's
	// Retry-After / RateLimit-Reset hint.
	defaultMaxRetries = 3

	// defaultRetryWait is the delay before retrying a 429 when the response
	// carries no usable Retry-After / RateLimit-Reset header, and the floor for
	// any such hint. It matches Fillout's 1-second rate-limit window so a
	// "reset: 0" hint doesn't trigger an immediate re-request within that window.
	defaultRetryWait = time.Second

	defaultUserAgent = "go-fillout/0.1"
)

// Client is a Fillout REST API client. It is safe for concurrent use by
// multiple goroutines.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	userAgent  string
	limiter    *rate.Limiter
	maxRetries int
	// sleep waits for a duration or until the context is cancelled. It is a field
	// so tests can make 429 backoff instant.
	sleep func(ctx context.Context, d time.Duration) error
}

// Option configures a Client. Pass options to NewClient.
type Option func(*Client)

// WithBaseURL overrides the API base URL. Use EUBaseURL for EU accounts, or a
// custom URL when testing against a mock server. A trailing slash is trimmed.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithHTTPClient sets the underlying *http.Client, allowing custom timeouts,
// transports, or proxies.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.httpClient = hc
		}
	}
}

// WithUserAgent sets the User-Agent header sent with each request.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithRateLimit overrides the client-side rate limiter. r is the sustained
// requests-per-second and burst is the maximum burst size. Fillout limits each
// API key to 5 requests per second; set a lower value to be more conservative.
// Pass r <= 0 to disable client-side rate limiting entirely.
func WithRateLimit(r float64, burst int) Option {
	return func(c *Client) {
		if r <= 0 {
			c.limiter = nil
			return
		}
		if burst < 1 {
			burst = 1
		}
		c.limiter = rate.NewLimiter(rate.Limit(r), burst)
	}
}

// NewClient returns a Client authenticated with the given API key. By default
// it talks to the US API, uses http.DefaultClient's transport with a 30s
// timeout, paces requests just under Fillout's 5 req/s window, and retries
// rate-limited (429) responses a few times honoring the server's backoff hint.
func NewClient(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		userAgent:  defaultUserAgent,
		limiter:    rate.NewLimiter(rate.Limit(defaultRateLimit), defaultBurst),
		maxRetries: defaultMaxRetries,
		sleep:      sleepCtx,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithMaxRetries sets how many times the client retries a request after a 429
// (rate-limited) response before returning the error. Each retry waits for the
// duration hinted by the response's Retry-After / RateLimit-Reset header,
// falling back to a 1-second default. Pass 0 to disable retrying.
func WithMaxRetries(n int) Option {
	return func(c *Client) {
		if n < 0 {
			n = 0
		}
		c.maxRetries = n
	}
}

// do performs an HTTP request against the API. body, if non-nil, is JSON
// encoded as the request body. out, if non-nil, receives the JSON-decoded
// response. A non-2xx response yields an *APIError.
//
// A 429 (rate limited) response is retried up to c.maxRetries times, waiting
// between attempts for the duration the server hints via Retry-After /
// RateLimit-Reset (see retryAfter). Each attempt re-acquires a rate-limiter
// token, so retries stay within the client-side pacing budget.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyBytes []byte
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("fillout: encoding request body: %w", err)
		}
		bodyBytes = buf
	}

	for attempt := 0; ; attempt++ {
		if c.limiter != nil {
			if err := c.limiter.Wait(ctx); err != nil {
				return err
			}
		}

		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
		if err != nil {
			return fmt.Errorf("fillout: building request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
		req.Header.Set("Accept", "application/json")
		if c.userAgent != "" {
			req.Header.Set("User-Agent", c.userAgent)
		}
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("fillout: performing request: %w", err)
		}
		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("fillout: reading response body: %w", err)
		}

		// Retry rate-limited responses, honoring the server's backoff hint.
		if resp.StatusCode == http.StatusTooManyRequests && attempt < c.maxRetries {
			if err := c.sleep(ctx, retryAfter(resp.Header)); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return newAPIError(resp.StatusCode, respBody)
		}

		if out != nil && len(respBody) > 0 {
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("fillout: decoding response: %w", err)
			}
		}
		return nil
	}
}

// retryAfter reports how long to wait before retrying a 429, derived from the
// response headers. It prefers the standard Retry-After header (delay in
// seconds) and falls back to the IETF RateLimit-Reset header Fillout sends
// (seconds until the window resets). It never returns less than defaultRetryWait
// — so a "reset: 0" hint doesn't cause an immediate re-request inside the same
// 1-second window — and falls back to that default when no usable header is
// present.
func retryAfter(h http.Header) time.Duration {
	for _, key := range []string{"Retry-After", "RateLimit-Reset"} {
		v := strings.TrimSpace(h.Get(key))
		if v == "" {
			continue
		}
		secs, err := strconv.Atoi(v)
		if err != nil || secs < 0 {
			continue
		}
		if d := time.Duration(secs) * time.Second; d > defaultRetryWait {
			return d
		}
		return defaultRetryWait
	}
	return defaultRetryWait
}

// sleepCtx waits for d, or until ctx is cancelled, whichever comes first. A
// non-positive d returns immediately (with ctx.Err(), so a cancelled context is
// still reported).
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pathEscape escapes a value for safe inclusion as a single URL path segment.
func pathEscape(s string) string {
	return url.PathEscape(s)
}

// newAPIError builds an *APIError, extracting a "message" field from the body
// when the response is JSON.
func newAPIError(status int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Body: body}
	var parsed struct {
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil {
		switch {
		case parsed.Message != "":
			e.Message = parsed.Message
		case parsed.Error != "":
			e.Message = parsed.Error
		}
	}
	return e
}
