package nps

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/fillout"
)

func raw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func ans(id, name string, value any) fillout.QuestionAnswer {
	return fillout.QuestionAnswer{ID: id, Name: name, Value: raw(value)}
}

func TestBuildFields_MapsConcreteColumns(t *testing.T) {
	submitted := time.Date(2026, 6, 15, 7, 59, 2, 0, time.UTC)
	sub := fillout.Submission{
		SubmissionID:   "sub-abc",
		SubmissionTime: submitted,
		Questions: []fillout.QuestionAnswer{
			ans("q_score", "How likely to recommend?", 9),
			ans("q_email", "Your email", "kid@example.com"),
			ans("q_well", "What are we doing well?", "Great mentors"),
			ans("q_improve", "How can we improve?", "More events"),
			ans("q_hours", "Hours spent", "25.6"),
			ans("q_extra", "Planning to Come?", true),
			ans("q_secret", "Captcha", "xyz"),
		},
	}
	m := Mapping{Entries: []MappingEntry{
		{QuestionID: "q_score", Target: "score"},
		{QuestionID: "q_email", Target: "email"},
		{QuestionID: "q_well", Target: "doing_well"},
		{QuestionID: "q_improve", Target: "improve"},
		{QuestionID: "q_hours", Target: "hours"},
		{QuestionID: "q_extra", Target: TargetCustomFields},
		{QuestionID: "q_secret", Target: TargetIgnore},
	}}

	fields := BuildFields(sub, m, RecordOptions{YSWSProgram: "recYSWS1"})

	if got := fields["On a scale from 1-10, how likely are you to recommend this YSWS to a friend?"]; got != float64(9) {
		t.Errorf("score = %v (%T), want 9", got, got)
	}
	if got := fields["Email (optional, for prize)"]; got != "kid@example.com" {
		t.Errorf("email = %v", got)
	}
	if got := fields["What are we doing well?"]; got != "Great mentors" {
		t.Errorf("doing_well = %v", got)
	}
	if got := fields["How can we improve?"]; got != "More events" {
		t.Errorf("improve = %v", got)
	}
	if got := fields["How many hours do you estimate you spent on your project?"]; got != 25.6 {
		t.Errorf("hours = %v (%T), want 25.6", got, got)
	}

	// YSWS link is a record-ID array.
	link, ok := fields[FieldYSWS].([]string)
	if !ok || len(link) != 1 || link[0] != "recYSWS1" {
		t.Errorf("YSWS = %v, want [recYSWS1]", fields[FieldYSWS])
	}

	// Override Created At mirrors the submission time in RFC3339 UTC.
	if got := fields[FieldOverrideCreatedAt]; got != "2026-06-15T07:59:02Z" {
		t.Errorf("override created at = %v", got)
	}

	custom, _ := fields[FieldCustomFields].(string)
	if !strings.Contains(custom, "**Planning to Come?**\nYes") {
		t.Errorf("custom fields missing catch-all entry:\n%s", custom)
	}
	if strings.Contains(custom, "Captcha") {
		t.Errorf("ignored question leaked into custom fields:\n%s", custom)
	}
	if strings.Contains(custom, "Great mentors") {
		t.Errorf("concrete-column answer leaked into custom fields:\n%s", custom)
	}
	if !strings.Contains(custom, StampLine("sub-abc")) {
		t.Errorf("custom fields must contain the dedup stamp:\n%s", custom)
	}
}

func TestBuildFields_UnmappedQuestionsGoToCustomFields(t *testing.T) {
	sub := fillout.Submission{
		SubmissionID: "s1",
		Questions: []fillout.QuestionAnswer{
			ans("q1", "Anything else?", "Love it"),
		},
	}
	// Empty mapping: nothing is consumed, so the answer is preserved.
	fields := BuildFields(sub, Mapping{}, RecordOptions{})

	custom, _ := fields[FieldCustomFields].(string)
	if !strings.Contains(custom, "**Anything else?**\nLove it") {
		t.Errorf("unmapped answer not preserved:\n%s", custom)
	}
	if _, ok := fields[FieldYSWS]; ok {
		t.Error("YSWS set despite empty record id")
	}
}

func TestBuildFields_StampAlwaysPresent(t *testing.T) {
	sub := fillout.Submission{SubmissionID: "only-stamp", Questions: nil}
	fields := BuildFields(sub, Mapping{}, RecordOptions{})
	custom, _ := fields[FieldCustomFields].(string)
	if !strings.Contains(custom, StampLine("only-stamp")) {
		t.Errorf("custom = %q, want to contain the stamp", custom)
	}
}

func TestRenderValue(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hello", "hello"},
		{"bool true", true, "Yes"},
		{"bool false", false, "No"},
		{"int number", float64(10), "10"},
		{"float number", 25.6, "25.6"},
		{"string slice", []any{"a", "b", "c"}, "a, b, c"},
		{"nil", nil, ""},
		{"object", map[string]any{"city": "SF", "zip": "94110"}, "city: SF; zip: 94110"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderValue(c.in); got != c.want {
				t.Errorf("renderValue(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestNumericValue(t *testing.T) {
	if n, ok := numericValue(ans("q", "n", 9)); !ok || n != 9 {
		t.Errorf("numeric from number = %v,%v", n, ok)
	}
	if n, ok := numericValue(ans("q", "n", "25.6")); !ok || n != 25.6 {
		t.Errorf("numeric from string = %v,%v", n, ok)
	}
	if _, ok := numericValue(ans("q", "n", "not a number")); ok {
		t.Error("expected non-numeric string to fail")
	}
}

func TestStampLine(t *testing.T) {
	if got := StampLine("abc123"); got != "Fillout Submission: abc123" {
		t.Errorf("StampLine = %q", got)
	}
}

func TestBuildFields_TemplatedFeedbackConcatenates(t *testing.T) {
	sub := fillout.Submission{
		SubmissionID: "s1",
		Questions: []fillout.QuestionAnswer{
			ans("q_enjoy", "What did you enjoy most?", "the mentors"),
			ans("q_well", "What are we doing well?", "fast support"),
		},
	}
	// Both feed "doing_well"; the first is wrapped in a template, the second direct.
	m := Mapping{Entries: []MappingEntry{
		{QuestionID: "q_enjoy", Target: "doing_well", Template: "{{question}}: {{answer}}"},
		{QuestionID: "q_well", Target: "doing_well"},
	}}

	fields := BuildFields(sub, m, RecordOptions{})
	got, _ := fields["What are we doing well?"].(string)
	want := "What did you enjoy most?: the mentors\n\nfast support"
	if got != want {
		t.Errorf("doing_well =\n%q\nwant\n%q", got, want)
	}
}

func TestApplyTemplate(t *testing.T) {
	cases := []struct{ tmpl, q, a, want string }{
		{"", "Q", "A", "A"},                            // direct
		{"{{question}}: {{answer}}", "Q", "A", "Q: A"}, // both placeholders
		{"Liked most", "Q", "A", "Liked most A"},       // no {{answer}} -> appended
	}
	for _, c := range cases {
		if got := applyTemplate(c.tmpl, c.q, c.a); got != c.want {
			t.Errorf("applyTemplate(%q,%q,%q) = %q, want %q", c.tmpl, c.q, c.a, got, c.want)
		}
	}
}

func TestBuildFields_TagsMergeJobAndAnswer(t *testing.T) {
	sub := fillout.Submission{
		SubmissionID: "s1",
		Questions: []fillout.QuestionAnswer{
			ans("q_tag", "Tag", "30-day-new-user"),
		},
	}
	m := Mapping{Entries: []MappingEntry{{QuestionID: "q_tag", Target: "tags"}}}

	fields := BuildFields(sub, m, RecordOptions{Tags: []string{"2026-06-15_newsletter", "30-day-new-user"}})
	got, _ := fields["Tags (Comma Separated)"].(string)
	// Job tags first, answer tag de-duplicated.
	if got != "2026-06-15_newsletter,30-day-new-user" {
		t.Errorf("tags = %q", got)
	}
}

func TestNormalizeTags(t *testing.T) {
	got := NormalizeTags(" 2026-06-15 2nd newsletter , Newsletter, newsletter ,, ")
	want := []string{"2026-06-15_2nd_newsletter", "Newsletter"}
	if len(got) != len(want) {
		t.Fatalf("NormalizeTags = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("tag[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
