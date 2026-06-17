package nps

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hackclub/fillout-ysws-nps/fillout"
)

// stampPrefix begins the dedup marker appended to the Custom Fields rich-text
// field. Duplicate detection searches for the full StampLine in Airtable.
const stampPrefix = "Fillout Submission:"

// StampLine returns the dedup marker for a Fillout submission ID.
func StampLine(submissionID string) string {
	return stampPrefix + " " + submissionID
}

// RecordOptions carries per-job context applied to every record a sync writes.
type RecordOptions struct {
	// YSWSProgram is the program name (or record ID) linked in the YSWS field.
	YSWSProgram string
	// Tags are job-level source tags merged into the Tags column.
	Tags []string
}

// BuildFields converts a Fillout submission into a map of Airtable NPS field
// values according to the mapping. Number/email columns take the first mapped
// answer that yields a value; templatable feedback columns concatenate every
// mapped answer (each optionally wrapped in its template); the Tags column
// merges job tags with any tag answers; everything else is preserved in the
// Custom Fields rich-text catch-all, which always ends with the dedup stamp.
// The function is pure.
func BuildFields(sub fillout.Submission, m Mapping, opts RecordOptions) map[string]any {
	targetByQID := make(map[string]string, len(m.Entries))
	for _, e := range m.Entries {
		if isValidTarget(e.Target) {
			targetByQID[e.QuestionID] = e.Target
		}
	}

	answersByQID := make(map[string]fillout.QuestionAnswer, len(sub.Questions))
	for _, a := range sub.Questions {
		answersByQID[a.ID] = a
	}

	fields := make(map[string]any)

	for _, tf := range TargetFields {
		switch {
		case tf.Key == "tags":
			// Handled below by merging job tags with answer tags.
		case tf.Kind == KindNumber:
			for _, e := range m.Entries {
				if e.Target != tf.Key {
					continue
				}
				if a, ok := answersByQID[e.QuestionID]; ok {
					if n, ok := numericValue(a); ok {
						fields[tf.AirtableName] = n
						break
					}
				}
			}
		case tf.Templatable:
			// Concatenate every mapped answer, each wrapped in its template.
			var pieces []string
			for _, e := range m.Entries {
				if e.Target != tf.Key {
					continue
				}
				a, ok := answersByQID[e.QuestionID]
				if !ok {
					continue
				}
				text := renderAnswer(a)
				if strings.TrimSpace(text) == "" {
					continue
				}
				name := a.Name
				if name == "" {
					name = e.QuestionName
				}
				pieces = append(pieces, applyTemplate(e.Template, name, text))
			}
			if len(pieces) > 0 {
				fields[tf.AirtableName] = strings.Join(pieces, "\n\n")
			}
		default:
			// Plain text (e.g. email): first mapped answer with a value.
			for _, e := range m.Entries {
				if e.Target != tf.Key {
					continue
				}
				if a, ok := answersByQID[e.QuestionID]; ok {
					if text := renderAnswer(a); strings.TrimSpace(text) != "" {
						fields[tf.AirtableName] = text
						break
					}
				}
			}
		}
	}

	// Tags: job source tags first, then any tag answers, de-duplicated.
	tags := append([]string(nil), opts.Tags...)
	for _, e := range m.Entries {
		if e.Target != "tags" {
			continue
		}
		if a, ok := answersByQID[e.QuestionID]; ok {
			tags = append(tags, NormalizeTags(renderAnswer(a))...)
		}
	}
	if merged := dedupeTags(tags); len(merged) > 0 {
		if tf, ok := TargetByKey("tags"); ok {
			fields[tf.AirtableName] = strings.Join(merged, ",")
		}
	}

	// Custom Fields catch-all, in submission order: every answer not consumed by
	// a concrete column and not explicitly ignored.
	var blocks []string
	for _, a := range sub.Questions {
		if target, mapped := targetByQID[a.ID]; mapped && target != TargetCustomFields {
			continue
		}
		text := renderAnswer(a)
		if strings.TrimSpace(text) == "" {
			continue
		}
		label := a.Name
		if label == "" {
			label = a.ID
		}
		blocks = append(blocks, "**"+label+"**\n"+text)
	}
	// The dedup stamp stays a findable substring but is italicized as a subtle
	// footer so the rich-text field reads cleanly.
	blocks = append(blocks, "_"+StampLine(sub.SubmissionID)+"_")
	fields[FieldCustomFields] = strings.Join(blocks, "\n\n")

	if !sub.SubmissionTime.IsZero() {
		fields[FieldOverrideCreatedAt] = sub.SubmissionTime.UTC().Format(time.RFC3339)
	}
	if opts.YSWSProgram != "" {
		fields[FieldYSWS] = []string{opts.YSWSProgram}
	}

	return fields
}

// applyTemplate formats one feedback answer. An empty template returns the answer
// unchanged. Otherwise {{answer}} and {{question}} are substituted; if the
// template omits {{answer}}, the answer is appended so it is never lost.
func applyTemplate(tmpl, question, answer string) string {
	if strings.TrimSpace(tmpl) == "" {
		return answer
	}
	out := strings.NewReplacer("{{answer}}", answer, "{{question}}", question).Replace(tmpl)
	if !strings.Contains(tmpl, "{{answer}}") {
		out = strings.TrimSpace(out + " " + answer)
	}
	return out
}

// dedupeTags removes case-insensitive duplicates, preserving first-seen order.
func dedupeTags(tags []string) []string {
	seen := make(map[string]bool, len(tags))
	var out []string
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" || seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		out = append(out, t)
	}
	return out
}

// renderAnswer converts a submission answer's polymorphic JSON value into
// human-readable text suitable for a rich-text or plain-text Airtable field.
func renderAnswer(a fillout.QuestionAnswer) string {
	if len(a.Value) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(a.Value, &v); err != nil {
		return strings.TrimSpace(string(a.Value))
	}
	return renderValue(v)
}

func renderValue(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "Yes"
		}
		return "No"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := renderValue(item); strings.TrimSpace(s) != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			if s := renderValue(t[k]); strings.TrimSpace(s) != "" {
				parts = append(parts, k+": "+s)
			}
		}
		return strings.Join(parts, "; ")
	default:
		return fmt.Sprintf("%v", t)
	}
}

// numericValue extracts a float from an answer whose value is a JSON number or a
// numeric string.
func numericValue(a fillout.QuestionAnswer) (float64, bool) {
	var v any
	if err := json.Unmarshal(a.Value, &v); err != nil {
		return 0, false
	}
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		if s := strings.TrimSpace(t); s != "" {
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				return n, true
			}
		}
	}
	return 0, false
}
