package nps

import (
	"context"
	"testing"

	"github.com/hackclub/fillout-ysws-nps/airtable"
)

// programFake serves YSWS Programs and NPS records for program/tag tests.
type programFake struct {
	programs map[string]string // record ID -> program name
	nps      []airtable.Record
}

func (f *programFake) ListRecords(_ context.Context, table string, _ *airtable.ListOptions) ([]airtable.Record, error) {
	if table == YSWSProgramsTable {
		var recs []airtable.Record
		for id, name := range f.programs {
			recs = append(recs, airtable.Record{ID: id, Fields: map[string]any{"Name": name}})
		}
		return recs, nil
	}
	return f.nps, nil
}

func (f *programFake) CreateRecords(_ context.Context, _ string, _ []map[string]any, _ bool) ([]airtable.Record, error) {
	return nil, nil
}

func TestListProgramNames_SortedDeduped(t *testing.T) {
	f := &programFake{programs: map[string]string{"r1": "Zeta", "r2": "alpha", "r3": "Zeta"}}
	names, err := ListProgramNames(context.Background(), f)
	if err != nil {
		t.Fatalf("ListProgramNames: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "Zeta" {
		t.Errorf("names = %v, want [alpha Zeta]", names)
	}
}

func TestCanonicalProgram(t *testing.T) {
	names := []string{"Flavortown", "Parthenon"}
	if got, ok := CanonicalProgram(names, "flavortown"); !ok || got != "Flavortown" {
		t.Errorf("CanonicalProgram(flavortown) = %q,%v", got, ok)
	}
	if _, ok := CanonicalProgram(names, "nope"); ok {
		t.Error("CanonicalProgram(nope) = ok, want false")
	}
	if _, ok := CanonicalProgram(names, ""); ok {
		t.Error("CanonicalProgram(empty) = ok, want false")
	}
}

func TestSampleTagsByProgram(t *testing.T) {
	f := &programFake{
		programs: map[string]string{"recA": "Flavortown", "recB": "Parthenon"},
		nps: []airtable.Record{
			{Fields: map[string]any{"Tags (Comma Separated)": "a,b", "YSWS": []any{"recA"}}},
			{Fields: map[string]any{"Tags (Comma Separated)": "b,c", "YSWS": []any{"recA"}}},
			{Fields: map[string]any{"Tags (Comma Separated)": "x", "YSWS": []any{"recB"}}},
			{Fields: map[string]any{"Tags (Comma Separated)": "orphan", "YSWS": []any{"recUnknown"}}},
			{Fields: map[string]any{"Tags (Comma Separated)": "", "YSWS": []any{"recA"}}},
		},
	}
	got, err := SampleTagsByProgram(context.Background(), f, "NPS", 6, 5)
	if err != nil {
		t.Fatalf("SampleTagsByProgram: %v", err)
	}
	byProg := map[string][]string{}
	for _, pt := range got {
		byProg[pt.Program] = pt.Tags
	}
	if len(got) != 2 {
		t.Fatalf("programs = %d, want 2 (orphan link excluded)", len(got))
	}
	// Flavortown tags a,b,c (deduped, sorted); Parthenon x.
	if a := byProg["Flavortown"]; len(a) != 3 || a[0] != "a" || a[1] != "b" || a[2] != "c" {
		t.Errorf("Flavortown tags = %v, want [a b c]", a)
	}
	if p := byProg["Parthenon"]; len(p) != 1 || p[0] != "x" {
		t.Errorf("Parthenon tags = %v, want [x]", p)
	}
}
