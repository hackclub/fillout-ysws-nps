package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// personSchema is a valid strict JSON schema used across tests.
var personSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"name": {"type": "string"},
		"age": {"type": "integer"}
	},
	"required": ["name", "age"],
	"additionalProperties": false
}`)

type person struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewClient("test-key", WithBaseURL(srv.URL), WithModel("gpt-test"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestNewClientRequiresAPIKey(t *testing.T) {
	if _, err := NewClient("   "); err == nil {
		t.Fatal("expected error for empty api key, got nil")
	}
	if _, err := NewClient("sk-real"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStructuredSendsWellFormedRequest(t *testing.T) {
	var got chatRequest
	var authHeader, contentType, path string

	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		contentType = r.Header.Get("Content-Type")
		path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("server could not decode request: %v", err)
		}
		writeCompletion(w, `{"name":"Ada","age":36}`)
	})

	var out person
	raw, err := c.Structured(context.Background(), Request{
		System:     "You extract people.",
		Prompt:     "Ada Lovelace, 36.",
		SchemaName: "person",
		Schema:     personSchema,
	}, &out)
	if err != nil {
		t.Fatalf("Structured: %v", err)
	}

	if path != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", path)
	}
	if authHeader != "Bearer test-key" {
		t.Errorf("Authorization = %q, want Bearer test-key", authHeader)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", contentType)
	}
	if got.Model != "gpt-test" {
		t.Errorf("model = %q, want gpt-test", got.Model)
	}
	if got.ResponseFormat.Type != "json_schema" {
		t.Errorf("response_format.type = %q, want json_schema", got.ResponseFormat.Type)
	}
	if got.ResponseFormat.JSONSchema.Name != "person" {
		t.Errorf("schema name = %q, want person", got.ResponseFormat.JSONSchema.Name)
	}
	if !got.ResponseFormat.JSONSchema.Strict {
		t.Error("expected strict = true")
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != "system" || got.Messages[1].Role != "user" {
		t.Errorf("messages = %+v, want system then user", got.Messages)
	}
	if got.Messages[1].Content != "Ada Lovelace, 36." {
		t.Errorf("user content = %q", got.Messages[1].Content)
	}

	if out.Name != "Ada" || out.Age != 36 {
		t.Errorf("decoded = %+v, want {Ada 36}", out)
	}
	if raw != `{"name":"Ada","age":36}` {
		t.Errorf("raw = %q", raw)
	}
}

func TestStructuredOmitsSystemWhenEmpty(t *testing.T) {
	var got chatRequest
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		writeCompletion(w, `{"name":"Grace","age":85}`)
	})

	if _, err := c.Structured(context.Background(), Request{
		Prompt:     "Grace Hopper, 85.",
		SchemaName: "person",
		Schema:     personSchema,
	}, &person{}); err != nil {
		t.Fatalf("Structured: %v", err)
	}

	if len(got.Messages) != 1 || got.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want a single user message", got.Messages)
	}
}

func TestStructuredPerRequestModelOverride(t *testing.T) {
	var got chatRequest
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		writeCompletion(w, `{"name":"x","age":1}`)
	})

	if _, err := c.Structured(context.Background(), Request{
		Model:      "gpt-4o",
		Prompt:     "hi",
		SchemaName: "person",
		Schema:     personSchema,
	}, &person{}); err != nil {
		t.Fatalf("Structured: %v", err)
	}
	if got.Model != "gpt-4o" {
		t.Errorf("model = %q, want gpt-4o", got.Model)
	}
}

func TestStructuredValidatesRequest(t *testing.T) {
	c, _ := NewClient("sk-real")
	cases := map[string]Request{
		"missing prompt":      {SchemaName: "person", Schema: personSchema},
		"missing schema name": {Prompt: "hi", Schema: personSchema},
		"missing schema":      {Prompt: "hi", SchemaName: "person"},
		"invalid schema json": {Prompt: "hi", SchemaName: "person", Schema: json.RawMessage(`{`)},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.Structured(context.Background(), req, &person{}); err == nil {
				t.Fatal("expected validation error, got nil")
			}
		})
	}
}

func TestStructuredAPIError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
	})

	_, err := c.Structured(context.Background(), Request{
		Prompt: "hi", SchemaName: "person", Schema: personSchema,
	}, &person{})

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %v", err)
	}
	if apiErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429", apiErr.StatusCode)
	}
	if !strings.Contains(apiErr.Body, "slow down") {
		t.Errorf("body = %q, want it to contain the API message", apiErr.Body)
	}
}

func TestStructuredRefusal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"refusal":"I cannot help with that."}}]}`)
	})

	_, err := c.Structured(context.Background(), Request{
		Prompt: "do something bad", SchemaName: "person", Schema: personSchema,
	}, &person{})

	var refusal *RefusalError
	if !errors.As(err, &refusal) {
		t.Fatalf("expected *RefusalError, got %v", err)
	}
}

func TestStructuredNoChoices(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[]}`)
	})
	if _, err := c.Structured(context.Background(), Request{
		Prompt: "hi", SchemaName: "person", Schema: personSchema,
	}, &person{}); err == nil {
		t.Fatal("expected error for empty choices, got nil")
	}
}

func TestStructuredBadContentJSON(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeCompletion(w, `not json`)
	})
	raw, err := c.Structured(context.Background(), Request{
		Prompt: "hi", SchemaName: "person", Schema: personSchema,
	}, &person{})
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if raw != "not json" {
		t.Errorf("raw = %q, want the undecodable content returned alongside the error", raw)
	}
}

func TestStructuredNilOutSkipsDecode(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeCompletion(w, `{"name":"Ada","age":36}`)
	})
	raw, err := c.Structured(context.Background(), Request{
		Prompt: "hi", SchemaName: "person", Schema: personSchema,
	}, nil)
	if err != nil {
		t.Fatalf("Structured: %v", err)
	}
	if raw != `{"name":"Ada","age":36}` {
		t.Errorf("raw = %q", raw)
	}
}

// writeCompletion writes a minimal chat completion whose assistant content is
// the given string.
func writeCompletion(w http.ResponseWriter, content string) {
	resp := map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"content": content}},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
