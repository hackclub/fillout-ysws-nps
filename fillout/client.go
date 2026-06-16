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
	"strings"
	"time"

	"golang.org/x/time/rate"
)

const (
	// DefaultBaseURL is the US Fillout API base URL.
	DefaultBaseURL = "https://api.fillout.com/v1/api"
	// EUBaseURL is the EU Fillout API base URL.
	EUBaseURL = "https://eu-api.fillout.com/v1/api"

	// defaultRateLimit matches Fillout's documented limit of 5 requests per
	// second per API key.
	defaultRateLimit = 5

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
// timeout, and rate-limits requests to Fillout's documented 5 req/s.
func NewClient(apiKey string, opts ...Option) *Client {
	c := &Client{
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		userAgent:  defaultUserAgent,
		limiter:    rate.NewLimiter(rate.Limit(defaultRateLimit), defaultRateLimit),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// do performs an HTTP request against the API. body, if non-nil, is JSON
// encoded as the request body. out, if non-nil, receives the JSON-decoded
// response. A non-2xx response yields an *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var reqBody io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("fillout: encoding request body: %w", err)
		}
		reqBody = bytes.NewReader(buf)
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
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("fillout: performing request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("fillout: reading response body: %w", err)
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
