package nps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/openai"
)

// Mapping records, for every form question, which NPS target it feeds. It is
// persisted as JSONB on the sync job.
type Mapping struct {
	Entries []MappingEntry `json:"entries"`
}

// MappingEntry is the chosen target for a single form question.
type MappingEntry struct {
	QuestionID   string `json:"question_id"`
	QuestionName string `json:"question_name"`
	QuestionType string `json:"question_type,omitempty"`
	// Target is a TargetField key, TargetCustomFields, or TargetIgnore.
	Target string `json:"target"`
	// Template optionally wraps the answer for a templatable feedback target
	// (supports {{question}} and {{answer}}). Empty means map the text directly.
	Template string `json:"template,omitempty"`
	// Reasoning and Confidence come from the AI and are shown in the preview.
	Reasoning  string `json:"reasoning,omitempty"`
	Confidence string `json:"confidence,omitempty"`
}

// Mapper produces a Mapping from a form's questions using an LLM.
type Mapper struct {
	client *openai.Client
}

// NewMapper returns a Mapper backed by the given OpenAI client.
func NewMapper(client *openai.Client) *Mapper {
	return &Mapper{client: client}
}

// aiMapping is the strict-schema shape returned by the model.
type aiMapping struct {
	Mappings []struct {
		QuestionID string `json:"question_id"`
		Target     string `json:"target"`
		Template   string `json:"template"`
		Reasoning  string `json:"reasoning"`
		Confidence string `json:"confidence"`
	} `json:"mappings"`
}

// Map asks the model to assign each question a target, then returns a Mapping
// covering every question (in input order). Questions the model omits or assigns
// an unknown target default to TargetCustomFields so no answer is silently lost.
func (m *Mapper) Map(ctx context.Context, questions []fillout.QuestionDef) (Mapping, error) {
	if len(questions) == 0 {
		return Mapping{}, fmt.Errorf("nps: cannot map a form with no questions")
	}

	var out aiMapping
	if _, err := m.client.Structured(ctx, openai.Request{
		System:     mappingSystemPrompt,
		Prompt:     buildMappingPrompt(questions),
		SchemaName: "nps_field_mapping",
		Schema:     mappingSchema(),
	}, &out); err != nil {
		return Mapping{}, fmt.Errorf("nps: ai mapping: %w", err)
	}

	type choice struct {
		target, template, reasoning, confidence string
	}
	chosen := make(map[string]choice, len(out.Mappings))
	for _, mm := range out.Mappings {
		target := mm.Target
		if !isValidTarget(target) {
			target = TargetCustomFields
		}
		chosen[mm.QuestionID] = choice{target, strings.TrimSpace(mm.Template), mm.Reasoning, mm.Confidence}
	}

	mapping := Mapping{Entries: make([]MappingEntry, 0, len(questions))}
	for _, q := range questions {
		entry := MappingEntry{
			QuestionID:   q.ID,
			QuestionName: q.Name,
			QuestionType: string(q.Type),
			Target:       TargetCustomFields,
		}
		if c, ok := chosen[q.ID]; ok {
			entry.Target = c.target
			entry.Reasoning = c.reasoning
			entry.Confidence = c.confidence
			// Keep an AI-suggested template only for templatable feedback targets.
			if IsTemplatable(entry.Target) {
				entry.Template = c.template
			}
		}
		mapping.Entries = append(mapping.Entries, entry)
	}
	return mapping, nil
}

// BuildMapping constructs a Mapping for the given questions, taking each
// question's target from targetFor (e.g. user-confirmed form values) and its
// optional feedback template from templateFor. Empty or unknown targets fall
// back to TargetCustomFields so no answer is lost; templates are kept only for
// templatable targets. templateFor may be nil.
func BuildMapping(questions []fillout.QuestionDef, targetFor func(questionID string) string, templateFor func(questionID string) string) Mapping {
	mapping := Mapping{Entries: make([]MappingEntry, 0, len(questions))}
	for _, q := range questions {
		target := targetFor(q.ID)
		if !isValidTarget(target) {
			target = TargetCustomFields
		}
		entry := MappingEntry{
			QuestionID:   q.ID,
			QuestionName: q.Name,
			QuestionType: string(q.Type),
			Target:       target,
		}
		if templateFor != nil && IsTemplatable(target) {
			entry.Template = strings.TrimSpace(templateFor(q.ID))
		}
		mapping.Entries = append(mapping.Entries, entry)
	}
	return mapping
}

const mappingSystemPrompt = `You map questions from a Net Promoter Score (NPS) ` +
	`feedback form onto columns of an Airtable table. Assign each question to ` +
	`exactly one target. Use a specific NPS column only when the question's ` +
	`meaning clearly matches it; when unsure, choose "custom_fields" so the ` +
	`answer is preserved verbatim rather than lost. Use "ignore" only for ` +
	`answers with no analytical value (captcha, signature, consent checkbox). ` +
	`Never invent question ids; only use the ids provided.

For questions you map to the feedback columns "doing_well" or "improve", also ` +
	`set "template" when the question's wording is not exactly the column's ` +
	`wording, so the stored feedback reads clearly. The template must BOLD the ` +
	`label using Airtable rich-text markdown (double asterisks) and, by default, ` +
	`use the question's EXACT wording via the {{question}} placeholder: ` +
	`"**{{question}}** {{answer}}" (no trailing colon after the bold label). ` +
	`Only replace {{question}} with a short ` +
	`paraphrased label in a genuinely special case where the exact wording reads ` +
	`poorly as a label. Leave "template" empty ("") when the answer should be ` +
	`copied directly (e.g. the question already matches the column), and always ` +
	`leave it empty for every non-feedback target.`

func buildMappingPrompt(questions []fillout.QuestionDef) string {
	var b strings.Builder
	b.WriteString("Target columns:\n")
	for _, f := range TargetFields {
		fmt.Fprintf(&b, "- %s: %s\n", f.Key, f.Description)
	}
	b.WriteString("- custom_fields: Any answer that does not clearly fit a column above. Preserved verbatim in a catch-all rich-text field. Prefer this over dropping data.\n")
	b.WriteString("- ignore: Only for answers with no analytical value.\n\n")
	b.WriteString("Map each of these form questions to exactly one target:\n")
	for i, q := range questions {
		fmt.Fprintf(&b, "%d. id=%s | name=%q | type=%s\n", i+1, q.ID, q.Name, q.Type)
	}
	return b.String()
}

// mappingSchema builds the strict JSON schema for the mapping response, with the
// target enum derived from the live target catalog.
func mappingSchema() json.RawMessage {
	type prop struct {
		Type string   `json:"type"`
		Enum []string `json:"enum,omitempty"`
	}
	item := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question_id": prop{Type: "string"},
			"target":      prop{Type: "string", Enum: ValidTargets()},
			"template":    prop{Type: "string"},
			"reasoning":   prop{Type: "string"},
			"confidence":  prop{Type: "string", Enum: []string{"high", "medium", "low"}},
		},
		"required":             []string{"question_id", "target", "template", "reasoning", "confidence"},
		"additionalProperties": false,
	}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mappings": map[string]any{
				"type":  "array",
				"items": item,
			},
		},
		"required":             []string{"mappings"},
		"additionalProperties": false,
	}
	raw, _ := json.Marshal(schema)
	return raw
}
