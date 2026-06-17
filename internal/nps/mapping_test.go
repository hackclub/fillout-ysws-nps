package nps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/openai"
)

// stubOpenAI returns an OpenAI client whose chat endpoint always replies with
// content (a JSON string that the structured-output decoder will parse).
func stubOpenAI(t *testing.T, content string) *openai.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{
				map[string]any{"message": map[string]any{"content": content}},
			},
		})
	}))
	t.Cleanup(srv.Close)

	client, err := openai.NewClient("test-key", openai.WithBaseURL(srv.URL))
	if err != nil {
		t.Fatalf("openai.NewClient: %v", err)
	}
	return client
}

func TestMapper_Map(t *testing.T) {
	questions := []fillout.QuestionDef{
		{ID: "q_score", Name: "Recommend?", Type: fillout.QuestionOpinionScale},
		{ID: "q_email", Name: "Email", Type: fillout.QuestionEmailInput},
		{ID: "q_extra", Name: "Anything else?", Type: fillout.QuestionLongAnswer},
		{ID: "q_omitted", Name: "Mystery", Type: fillout.QuestionShortAnswer},
	}

	// The model maps two correctly, returns an invalid target for one, and omits
	// the last entirely.
	content := `{"mappings":[
		{"question_id":"q_score","target":"score","reasoning":"clearly the NPS rating","confidence":"high"},
		{"question_id":"q_email","target":"email","reasoning":"email field","confidence":"high"},
		{"question_id":"q_extra","target":"not_a_real_target","reasoning":"oops","confidence":"low"}
	]}`

	mapper := NewMapper(stubOpenAI(t, content))
	mapping, err := mapper.Map(context.Background(), questions)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}

	if len(mapping.Entries) != len(questions) {
		t.Fatalf("got %d entries, want %d (one per question)", len(mapping.Entries), len(questions))
	}

	byID := map[string]MappingEntry{}
	for i, e := range mapping.Entries {
		if e.QuestionID != questions[i].ID {
			t.Errorf("entry %d id = %q, want %q (order preserved)", i, e.QuestionID, questions[i].ID)
		}
		byID[e.QuestionID] = e
	}

	if byID["q_score"].Target != "score" {
		t.Errorf("q_score target = %q", byID["q_score"].Target)
	}
	if byID["q_email"].Target != "email" {
		t.Errorf("q_email target = %q", byID["q_email"].Target)
	}
	// Invalid target falls back to custom_fields.
	if byID["q_extra"].Target != TargetCustomFields {
		t.Errorf("invalid target not defaulted: %q", byID["q_extra"].Target)
	}
	// Omitted question defaults to custom_fields, with name/type filled in.
	if byID["q_omitted"].Target != TargetCustomFields {
		t.Errorf("omitted target = %q, want custom_fields", byID["q_omitted"].Target)
	}
	if byID["q_omitted"].QuestionName != "Mystery" {
		t.Errorf("omitted name not populated: %q", byID["q_omitted"].QuestionName)
	}
}

func TestMapper_SuggestsTemplateForFeedback(t *testing.T) {
	questions := []fillout.QuestionDef{
		{ID: "q_fb", Name: "What did you enjoy most?", Type: fillout.QuestionLongAnswer},
		{ID: "q_email", Name: "Email", Type: fillout.QuestionEmailInput},
	}
	// AI maps the feedback question to doing_well with a template, and (wrongly)
	// returns a template for the email target which must be dropped.
	content := `{"mappings":[
		{"question_id":"q_fb","target":"doing_well","template":"What they enjoyed: {{answer}}","reasoning":"phrasing differs","confidence":"high"},
		{"question_id":"q_email","target":"email","template":"junk","reasoning":"email","confidence":"high"}
	]}`
	mapper := NewMapper(stubOpenAI(t, content))
	mapping, err := mapper.Map(context.Background(), questions)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	byID := map[string]MappingEntry{}
	for _, e := range mapping.Entries {
		byID[e.QuestionID] = e
	}
	if got := byID["q_fb"].Template; got != "What they enjoyed: {{answer}}" {
		t.Errorf("feedback template = %q, want suggested template", got)
	}
	if got := byID["q_email"].Template; got != "" {
		t.Errorf("non-feedback template = %q, want dropped", got)
	}
}

func TestMapper_NoQuestions(t *testing.T) {
	mapper := NewMapper(stubOpenAI(t, `{"mappings":[]}`))
	if _, err := mapper.Map(context.Background(), nil); err == nil {
		t.Fatal("expected error for zero questions")
	}
}

func TestMappingSchemaValid(t *testing.T) {
	schema := mappingSchema()
	if !json.Valid(schema) {
		t.Fatal("mappingSchema is not valid JSON")
	}
	if !strings.Contains(string(schema), `"custom_fields"`) {
		t.Error("schema enum missing custom_fields target")
	}
	if !strings.Contains(string(schema), `"score"`) {
		t.Error("schema enum missing score target")
	}
}

func TestBuildMappingPrompt(t *testing.T) {
	prompt := buildMappingPrompt([]fillout.QuestionDef{
		{ID: "q1", Name: "Recommend?", Type: fillout.QuestionOpinionScale},
	})
	for _, want := range []string{"q1", "Recommend?", "score:", "custom_fields"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
