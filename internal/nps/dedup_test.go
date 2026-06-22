package nps

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hackclub/fillout-ysws-nps/airtable"
	"github.com/hackclub/fillout-ysws-nps/fillout"
)

// stampReturningAirtable returns, for any dedup query, one record whose Custom
// Fields is produced by the real transform (BuildFields) — i.e. the stamp is the
// italicized last block "_Fillout Submission: <id>_". This exercises the exact
// format FindStampedRecords must parse in production.
type stampReturningAirtable struct {
	recordID  string
	subID     string
	customStr string
}

func (s *stampReturningAirtable) ListRecords(_ context.Context, _ string, opts *airtable.ListOptions) ([]airtable.Record, error) {
	return []airtable.Record{{ID: s.recordID, Fields: map[string]any{FieldCustomFields: s.customStr}}}, nil
}

func (s *stampReturningAirtable) CreateRecords(context.Context, string, []map[string]any, bool) ([]airtable.Record, error) {
	return nil, nil
}

// TestFindStampedRecords_RealStampFormat is a regression test for a dedup bug
// where the stamp parser captured the trailing italic underscore, so the parsed
// ID ("<id>_") never matched the wanted bare ID and cross-instance dedup failed,
// re-creating duplicate Airtable records. The Custom Fields here comes straight
// from BuildFields, so the test breaks if the stored and parsed formats drift.
func TestFindStampedRecords_RealStampFormat(t *testing.T) {
	subID := "a671024b-ba89-431d-92b0-f7ac8a7c0c72"
	sub := fillout.Submission{
		SubmissionID: subID,
		Questions: []fillout.QuestionAnswer{
			{ID: "q1", Name: "Anything else?", Value: json.RawMessage(`"great job"`)},
		},
	}
	fields := BuildFields(sub, Mapping{}, RecordOptions{})
	customStr, _ := fields[FieldCustomFields].(string)

	at := &stampReturningAirtable{recordID: "recABC", subID: subID, customStr: customStr}
	found, err := FindStampedRecords(context.Background(), at, "NPS", []string{subID})
	if err != nil {
		t.Fatalf("FindStampedRecords: %v", err)
	}
	if got := found[subID]; got != "recABC" {
		t.Fatalf("FindStampedRecords did not match the real stamp format: found[%q] = %q, want %q\nCustom Fields was:\n%s",
			subID, got, "recABC", customStr)
	}
}
