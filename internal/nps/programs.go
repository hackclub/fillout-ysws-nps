package nps

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hackclub/fillout-ysws-nps/airtable"
)

const (
	// YSWSProgramsTable is the Airtable table the NPS "YSWS" field links to.
	YSWSProgramsTable = "YSWS Programs"
	// yswsProgramNameField is its primary field.
	yswsProgramNameField = "Name"
)

// ListProgramNames returns the sorted, de-duplicated YSWS program names, used to
// populate the required program picker on the preview screen.
func ListProgramNames(ctx context.Context, at AirtableAPI) ([]string, error) {
	recs, err := at.ListRecords(ctx, YSWSProgramsTable, &airtable.ListOptions{
		Fields: []string{yswsProgramNameField},
	})
	if err != nil {
		return nil, fmt.Errorf("nps: listing YSWS programs: %w", err)
	}
	seen := make(map[string]bool, len(recs))
	names := make([]string, 0, len(recs))
	for _, r := range recs {
		name, _ := r.Fields[yswsProgramNameField].(string)
		name = strings.TrimSpace(name)
		if name == "" || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return names, nil
}

// ProgramTags is a sample of tags previously used for one program.
type ProgramTags struct {
	Program string
	Tags    []string
}

// SampleTagsByProgram surveys existing NPS records to surface example tags that
// past syncs used, grouped by their linked YSWS program. It returns up to
// maxPrograms programs (first seen), each with up to maxTags distinct tags, to
// show as suggestions on the link page. Best-effort: callers may ignore errors.
func SampleTagsByProgram(ctx context.Context, at AirtableAPI, npsTable string, maxPrograms, maxTags int) ([]ProgramTags, error) {
	progRecs, err := at.ListRecords(ctx, YSWSProgramsTable, &airtable.ListOptions{Fields: []string{yswsProgramNameField}})
	if err != nil {
		return nil, fmt.Errorf("nps: loading programs for tag samples: %w", err)
	}
	idToName := make(map[string]string, len(progRecs))
	for _, r := range progRecs {
		if name, _ := r.Fields[yswsProgramNameField].(string); name != "" {
			idToName[r.ID] = name
		}
	}

	tagsField := "Tags (Comma Separated)"
	if tf, ok := TargetByKey("tags"); ok {
		tagsField = tf.AirtableName
	}
	npsRecs, err := at.ListRecords(ctx, npsTable, &airtable.ListOptions{
		FilterByFormula: fmt.Sprintf("{%s}!=\"\"", tagsField),
		Fields:          []string{tagsField, FieldYSWS},
		MaxRecords:      1000,
	})
	if err != nil {
		return nil, fmt.Errorf("nps: loading tagged records: %w", err)
	}

	var order []string
	byProgram := make(map[string]map[string]bool)
	for _, r := range npsRecs {
		program := idToName[firstLinkID(r.Fields[FieldYSWS])]
		if program == "" {
			continue
		}
		tagsText, _ := r.Fields[tagsField].(string)
		for _, tag := range NormalizeTags(tagsText) {
			set, seen := byProgram[program]
			if !seen {
				if len(order) >= maxPrograms {
					continue
				}
				set = make(map[string]bool)
				byProgram[program] = set
				order = append(order, program)
			}
			if len(set) < maxTags {
				set[tag] = true
			}
		}
	}

	out := make([]ProgramTags, 0, len(order))
	for _, program := range order {
		tags := make([]string, 0, len(byProgram[program]))
		for tag := range byProgram[program] {
			tags = append(tags, tag)
		}
		sort.Strings(tags)
		out = append(out, ProgramTags{Program: program, Tags: tags})
	}
	return out, nil
}

func firstLinkID(v any) string {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return ""
	}
	id, _ := arr[0].(string)
	return id
}

// CanonicalProgram resolves a user-entered program name to its canonical form
// (case-insensitive) from names, reporting whether it is a valid program.
func CanonicalProgram(names []string, input string) (string, bool) {
	in := strings.ToLower(strings.TrimSpace(input))
	if in == "" {
		return "", false
	}
	for _, n := range names {
		if strings.ToLower(n) == in {
			return n, true
		}
	}
	return "", false
}
