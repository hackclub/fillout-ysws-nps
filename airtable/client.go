// Package airtable is a small, dependency-free client for the Airtable REST
// API, scoped to a single base, with full CRUD support over records.
//
// Create a client with New and call ListRecords, GetRecord, CreateRecords,
// UpdateRecords, ReplaceRecords, or DeleteRecords. Batch operations are
// automatically split into Airtable's maximum of 10 records per request.
package airtable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.airtable.com/v0"
	defaultTimeout = 30 * time.Second

	// maxBatchSize is the maximum number of records Airtable accepts in a
	// single create, update, or delete request.
	maxBatchSize = 10
)

// Client is an Airtable API client scoped to a single base.
//
// A Client is safe for concurrent use by multiple goroutines.
type Client struct {
	apiKey     string
	baseID     string
	baseURL    string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client used for requests. A nil client is
// ignored, leaving the default in place.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) {
		if h != nil {
			c.httpClient = h
		}
	}
}

// WithBaseURL overrides the Airtable API base URL (default
// "https://api.airtable.com/v0"). It is primarily useful for testing against a
// stub server.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		if u != "" {
			c.baseURL = strings.TrimRight(u, "/")
		}
	}
}

// New returns a Client authenticated with the given personal access token (or
// legacy API key) and scoped to baseID. Both arguments are required.
func New(apiKey, baseID string, opts ...Option) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("airtable: apiKey is required")
	}
	if baseID == "" {
		return nil, errors.New("airtable: baseID is required")
	}
	c := &Client{
		apiKey:     apiKey,
		baseID:     baseID,
		baseURL:    defaultBaseURL,
		httpClient: &http.Client{Timeout: defaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// tablePath returns the request path for a table, escaping the table name so
// that either a table ID (e.g. "tbl...") or a display name (e.g. "Approved
// Projects") may be used.
func (c *Client) tablePath(table string) string {
	return "/" + c.baseID + "/" + url.PathEscape(table)
}

// recordPath returns the request path for a single record.
func (c *Client) recordPath(table, recordID string) string {
	return c.tablePath(table) + "/" + url.PathEscape(recordID)
}

// do executes an HTTP request against the API and decodes a successful JSON
// response into out (which may be nil). A non-2xx response is returned as an
// *APIError.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("airtable: encode request body: %w", err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return fmt.Errorf("airtable: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("airtable: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("airtable: read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseAPIError(resp.StatusCode, data)
	}

	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("airtable: decode response body: %w", err)
		}
	}
	return nil
}

// chunk splits items into consecutive slices of at most size elements.
func chunk[T any](items []T, size int) [][]T {
	if size <= 0 {
		size = 1
	}
	var out [][]T
	for i := 0; i < len(items); i += size {
		end := i + size
		if end > len(items) {
			end = len(items)
		}
		out = append(out, items[i:end])
	}
	return out
}
