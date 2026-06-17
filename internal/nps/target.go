// Package nps maps Fillout NPS form submissions onto the Airtable NPS table:
// AI-assisted field mapping, a pure submission->record transform (rich text and
// a dedup stamp included), duplicate detection, and a background sync poller.
package nps

// Airtable field names on the NPS table that the transform writes directly,
// outside the AI mapping.
const (
	// FieldCustomFields is the rich-text catch-all that holds every unmapped
	// answer plus the dedup stamp.
	FieldCustomFields = "Custom Fields"
	// FieldOverrideCreatedAt records the original Fillout submission time.
	FieldOverrideCreatedAt = "Override Created At"
	// FieldYSWS is the linked-record field to the YSWS Programs table.
	FieldYSWS = "YSWS"
)

// Special mapping targets that are not concrete NPS fields.
const (
	// TargetCustomFields routes an answer into the rich-text catch-all.
	TargetCustomFields = "custom_fields"
	// TargetIgnore drops an answer entirely.
	TargetIgnore = "ignore"
)

// TargetKind classifies how a target value is written to Airtable.
type TargetKind string

const (
	KindNumber TargetKind = "number"
	KindText   TargetKind = "text"
)

// TargetField describes one AI-mappable NPS column.
type TargetField struct {
	// Key is the stable identifier used in mappings and the AI enum.
	Key string
	// AirtableName is the exact field name on the NPS table.
	AirtableName string
	// Kind controls value conversion (number vs. text).
	Kind TargetKind
	// Templatable reports whether mapped answers may be wrapped in a per-question
	// template (used for the free-text feedback columns).
	Templatable bool
	// Description guides the AI when choosing a target for a question.
	Description string
}

// TargetFields is the catalog of NPS columns the AI may map form questions onto.
// The YSWS link, Override Created At, and the Custom Fields catch-all are handled
// by the transform/job rather than the AI, so they are intentionally excluded.
var TargetFields = []TargetField{
	{
		Key:          "score",
		AirtableName: "On a scale from 1-10, how likely are you to recommend this YSWS to a friend?",
		Kind:         KindNumber,
		Description:  "The Net Promoter Score: a 0-10 rating of how likely the respondent is to recommend the program.",
	},
	{
		Key:          "email",
		AirtableName: "Email (optional, for prize)",
		Kind:         KindText,
		Description:  "The respondent's email address.",
	},
	{
		Key:          "doing_well",
		AirtableName: "What are we doing well?",
		Kind:         KindText,
		Templatable:  true,
		Description:  "Free-text positive feedback: what the program is doing well.",
	},
	{
		Key:          "improve",
		AirtableName: "How can we improve?",
		Kind:         KindText,
		Templatable:  true,
		Description:  "Free-text constructive feedback: how the program could improve.",
	},
	{
		Key:          "hours",
		AirtableName: "How many hours do you estimate you spent on your project?",
		Kind:         KindNumber,
		Description:  "Estimated number of hours the respondent spent on their project.",
	},
	{
		Key:          "tags",
		AirtableName: "Tags (Comma Separated)",
		Kind:         KindText,
		Description:  "Optional short comma-separated tags or labels.",
	},
}

// TargetByKey returns the target field with the given key.
func TargetByKey(key string) (TargetField, bool) {
	for _, f := range TargetFields {
		if f.Key == key {
			return f, true
		}
	}
	return TargetField{}, false
}

// IsTemplatable reports whether a target key supports per-question templates.
func IsTemplatable(key string) bool {
	f, ok := TargetByKey(key)
	return ok && f.Templatable
}

// ValidTargets returns every legal mapping target: each concrete field key plus
// the special "custom_fields" and "ignore" targets.
func ValidTargets() []string {
	out := make([]string, 0, len(TargetFields)+2)
	for _, f := range TargetFields {
		out = append(out, f.Key)
	}
	return append(out, TargetCustomFields, TargetIgnore)
}

// TargetOption is a choice for the mapping dropdown: Value is the stable target
// key stored in the mapping, Label is the human-readable NPS column title shown
// to the user.
type TargetOption struct {
	Value string
	Label string
}

// TargetOptions returns the mapping dropdown choices, labelled with the full NPS
// column titles (not the internal keys) so reviewers can see exactly which
// Airtable column each question feeds.
func TargetOptions() []TargetOption {
	opts := make([]TargetOption, 0, len(TargetFields)+2)
	for _, f := range TargetFields {
		opts = append(opts, TargetOption{Value: f.Key, Label: f.AirtableName})
	}
	return append(opts,
		TargetOption{Value: TargetCustomFields, Label: "Custom Fields (catch-all — kept verbatim)"},
		TargetOption{Value: TargetIgnore, Label: "Ignore (don't import)"},
	)
}

func isValidTarget(target string) bool {
	if target == TargetCustomFields || target == TargetIgnore {
		return true
	}
	_, ok := TargetByKey(target)
	return ok
}
