// Package openai is a tiny, dependency-free client for OpenAI structured-output
// calls. It sends a chat completion request with a JSON-schema response format
// and unmarshals the model's reply into a caller-provided Go value.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"
	defaultModel   = "gpt-4o-mini"
)

// Client talks to the OpenAI chat completions API.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the API base URL (e.g. for a mock server in tests).
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithModel sets the default model used when a Request does not specify one.
func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

// WithHTTPClient supplies a custom *http.Client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.httpClient = h }
}

// NewClient returns a Client authenticated with apiKey. It returns an error if
// apiKey is empty.
func NewClient(apiKey string, opts ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, fmt.Errorf("openai: api key is required")
	}
	c := &Client{
		apiKey:     apiKey,
		baseURL:    defaultBaseURL,
		model:      defaultModel,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// Request describes a single structured-output call.
type Request struct {
	// Model overrides the client's default model when non-empty.
	Model string
	// System is an optional system prompt.
	System string
	// Prompt is the user message. Required.
	Prompt string
	// SchemaName is the name OpenAI associates with the schema. Required.
	// Must match ^[a-zA-Z0-9_-]+$.
	SchemaName string
	// Schema is the JSON Schema describing the desired object. Required.
	// For strict mode it must set "additionalProperties": false and list every
	// property in "required".
	Schema json.RawMessage
}

// APIError is returned when the API responds with a non-2xx status.
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai: api returned status %d: %s", e.StatusCode, e.Body)
}

// RefusalError is returned when the model refuses to answer.
type RefusalError struct {
	Refusal string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("openai: model refused request: %s", e.Refusal)
}

// Structured sends req and unmarshals the model's JSON reply into out, which
// must be a non-nil pointer. The raw JSON content returned by the model is also
// returned so callers can log or re-parse it.
func (c *Client) Structured(ctx context.Context, req Request, out any) (string, error) {
	if err := req.validate(); err != nil {
		return "", err
	}

	model := req.Model
	if model == "" {
		model = c.model
	}

	messages := make([]chatMessage, 0, 2)
	if req.System != "" {
		messages = append(messages, chatMessage{Role: "system", Content: req.System})
	}
	messages = append(messages, chatMessage{Role: "user", Content: req.Prompt})

	body := chatRequest{
		Model:    model,
		Messages: messages,
		ResponseFormat: responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchema{
				Name:   req.SchemaName,
				Strict: true,
				Schema: req.Schema,
			},
		},
	}

	raw, err := c.do(ctx, body)
	if err != nil {
		return "", err
	}

	if out != nil {
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			return raw, fmt.Errorf("openai: decoding structured content: %w", err)
		}
	}
	return raw, nil
}

func (r Request) validate() error {
	if strings.TrimSpace(r.Prompt) == "" {
		return fmt.Errorf("openai: request prompt is required")
	}
	if strings.TrimSpace(r.SchemaName) == "" {
		return fmt.Errorf("openai: request schema name is required")
	}
	if len(r.Schema) == 0 {
		return fmt.Errorf("openai: request schema is required")
	}
	if !json.Valid(r.Schema) {
		return fmt.Errorf("openai: request schema is not valid JSON")
	}
	return nil
}

// do performs the HTTP round-trip and returns the assistant message content.
func (c *Client) do(ctx context.Context, body chatRequest) (string, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("openai: encoding request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("openai: building request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("openai: sending request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("openai: reading response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &APIError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(respBody))}
	}

	var parsed chatResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("openai: decoding response: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("openai: response contained no choices")
	}

	msg := parsed.Choices[0].Message
	if msg.Refusal != "" {
		return "", &RefusalError{Refusal: msg.Refusal}
	}
	if msg.Content == "" {
		return "", fmt.Errorf("openai: response message had empty content")
	}
	return msg.Content, nil
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat responseFormat `json:"response_format"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
			Refusal string `json:"refusal"`
		} `json:"message"`
	} `json:"choices"`
}
