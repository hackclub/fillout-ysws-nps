package openai_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/openai"
)

// TestStructuredLive hits the real OpenAI API. It is skipped unless
// OPENAI_API_KEY is set and -short is not passed.
func TestStructuredLive(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live API test in -short mode")
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set; skipping live API test")
	}

	client, err := openai.NewClient(apiKey)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"capital": {"type": "string"},
			"population_millions": {"type": "number"}
		},
		"required": ["capital", "population_millions"],
		"additionalProperties": false
	}`)

	var out struct {
		Capital            string  `json:"capital"`
		PopulationMillions float64 `json:"population_millions"`
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	raw, err := client.Structured(ctx, openai.Request{
		System:     "You are a concise geography assistant.",
		Prompt:     "What is the capital of France and its approximate metro population in millions?",
		SchemaName: "country_capital",
		Schema:     schema,
	}, &out)
	if err != nil {
		t.Fatalf("Structured: %v", err)
	}

	t.Logf("raw response: %s", raw)
	if out.Capital == "" {
		t.Errorf("expected a non-empty capital, got %q (raw: %s)", out.Capital, raw)
	}
	if out.PopulationMillions <= 0 {
		t.Errorf("expected a positive population, got %v (raw: %s)", out.PopulationMillions, raw)
	}
}
