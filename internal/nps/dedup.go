package nps

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hackclub/fillout-ysws-nps/airtable"
)

// dedupQueryChunk is how many submission stamps are checked per Airtable query.
const dedupQueryChunk = 25

// stampIDPattern extracts the submission ID from a Custom Fields stamp line. The
// stamp is stored italicized ("_Fillout Submission: <id>_"), so the capture can
// include the wrapper's trailing underscore; callers strip it to recover the
// bare submission ID.
var stampIDPattern = regexp.MustCompile(regexp.QuoteMeta(stampPrefix) + `\s*(\S+)`)

// AirtableAPI is the subset of the Airtable client the sync needs. The real
// *airtable.Client satisfies it; tests supply a fake.
type AirtableAPI interface {
	ListRecords(ctx context.Context, table string, opts *airtable.ListOptions) ([]airtable.Record, error)
	CreateRecords(ctx context.Context, table string, fields []map[string]any, typecast bool) ([]airtable.Record, error)
}

// dedupFormula builds an Airtable filterByFormula that matches records already
// stamped with this submission's dedup marker in the Custom Fields field.
func dedupFormula(submissionID string) string {
	return fmt.Sprintf("FIND(%q, {%s})", StampLine(submissionID), FieldCustomFields)
}

// FindStampedRecords looks up, in batched Airtable queries, which of the given
// submissions already have a stamped record, returning submissionID -> recordID
// for those found. This is the durable, Airtable-side dedup check: it detects
// rows synced by us or by another instance even when our local ledger has none.
func FindStampedRecords(ctx context.Context, at AirtableAPI, table string, submissionIDs []string) (map[string]string, error) {
	found := make(map[string]string, len(submissionIDs))
	for start := 0; start < len(submissionIDs); start += dedupQueryChunk {
		end := start + dedupQueryChunk
		if end > len(submissionIDs) {
			end = len(submissionIDs)
		}
		chunk := submissionIDs[start:end]
		want := make(map[string]bool, len(chunk))
		terms := make([]string, len(chunk))
		for i, id := range chunk {
			want[id] = true
			terms[i] = fmt.Sprintf("FIND(%q, {%s})", StampLine(id), FieldCustomFields)
		}
		records, err := at.ListRecords(ctx, table, &airtable.ListOptions{
			FilterByFormula: "OR(" + strings.Join(terms, ",") + ")",
			Fields:          []string{FieldCustomFields},
		})
		if err != nil {
			return nil, fmt.Errorf("nps: checking for existing records: %w", err)
		}
		for _, rec := range records {
			text, _ := rec.Fields[FieldCustomFields].(string)
			m := stampIDPattern.FindStringSubmatch(text)
			if m == nil {
				continue
			}
			// Strip the italic wrapper's underscores from the captured token to
			// recover the bare submission ID (see stampIDPattern).
			if id := strings.Trim(m[1], "_"); want[id] {
				found[id] = rec.ID
			}
		}
	}
	return found, nil
}
