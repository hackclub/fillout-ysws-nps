// Command structured-demo makes a single OpenAI structured-output call and
// prints the resulting object. It reads OPENAI_API_KEY from the environment,
// falling back to a .env file in the working directory.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/hackclub/fillout-ysws-nps/internal/dotenv"
	"github.com/hackclub/fillout-ysws-nps/openai"
)

func main() {
	_ = dotenv.Load(".env")

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("OPENAI_API_KEY is not set (export it or add it to .env)")
	}

	client, err := openai.NewClient(apiKey)
	if err != nil {
		log.Fatalf("creating client: %v", err)
	}

	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"field": {"type": "string"},
			"born_year": {"type": "integer"},
			"key_contributions": {
				"type": "array",
				"items": {"type": "string"}
			}
		},
		"required": ["name", "field", "born_year", "key_contributions"],
		"additionalProperties": false
	}`)

	var out struct {
		Name             string   `json:"name"`
		Field            string   `json:"field"`
		BornYear         int      `json:"born_year"`
		KeyContributions []string `json:"key_contributions"`
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	raw, err := client.Structured(ctx, openai.Request{
		System:     "You are a precise biographer. Answer with verified facts only.",
		Prompt:     "Give a short structured profile of the scientist Ada Lovelace.",
		SchemaName: "scientist_profile",
		Schema:     schema,
	}, &out)
	if err != nil {
		log.Fatalf("structured call failed: %v", err)
	}

	fmt.Println("Raw JSON returned by the model:")
	fmt.Println(raw)
	fmt.Println()
	fmt.Printf("Decoded Go struct:\n%+v\n", out)
}
