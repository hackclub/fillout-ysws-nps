package nps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hackclub/fillout-ysws-nps/airtable"
	"github.com/hackclub/fillout-ysws-nps/fillout"
	"github.com/hackclub/fillout-ysws-nps/internal/db"
)

// --- fakes ---

type fakeStore struct {
	mu       sync.Mutex
	ledger   map[string]db.SubmissionRecord // keyed by submissionID
	progress *time.Time
	lastErr  string
}

func newFakeStore() *fakeStore {
	return &fakeStore{ledger: map[string]db.SubmissionRecord{}}
}

func (f *fakeStore) ListActiveJobs(context.Context) ([]*db.SyncJob, error) { return nil, nil }

func (f *fakeStore) ExistingSubmissionIDs(_ context.Context, _ string, ids []string) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]bool)
	for _, id := range ids {
		if _, ok := f.ledger[id]; ok {
			out[id] = true
		}
	}
	return out, nil
}

func (f *fakeStore) RecordSubmissions(_ context.Context, recs []db.SubmissionRecord) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, rec := range recs {
		if _, ok := f.ledger[rec.FilloutSubmissionID]; ok {
			continue
		}
		f.ledger[rec.FilloutSubmissionID] = rec
		n++
	}
	return n, nil
}

func (f *fakeStore) UpdateJobProgress(_ context.Context, _ int64, cursor *time.Time, _ time.Time, lastErr string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.progress = cursor
	f.lastErr = lastErr
	return nil
}

func (f *fakeStore) outcomeCounts() (created, skipped int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rec := range f.ledger {
		switch rec.Outcome {
		case db.OutcomeCreated:
			created++
		case db.OutcomeSkippedExisting:
			skipped++
		}
	}
	return
}

type fakeFillout struct {
	subs []fillout.Submission
	err  error // yielded after the submissions, if set
}

func (f *fakeFillout) AllSubmissions(_ context.Context, _ string, params *fillout.GetSubmissionsParams) iter.Seq2[fillout.Submission, error] {
	return func(yield func(fillout.Submission, error) bool) {
		for _, s := range f.subs {
			if !params.AfterDate.IsZero() && !s.SubmissionTime.After(params.AfterDate) {
				continue
			}
			if !yield(s, nil) {
				return
			}
		}
		if f.err != nil {
			yield(fillout.Submission{}, f.err)
		}
	}
}

type fakeAirtable struct {
	mu         sync.Mutex
	stamped    map[string]string // submissionID -> existing recordID
	created    []map[string]any
	createErr  error
	nextRecNum int
}

func newFakeAirtable() *fakeAirtable {
	return &fakeAirtable{stamped: map[string]string{}}
}

func (f *fakeAirtable) ListRecords(_ context.Context, _ string, opts *airtable.ListOptions) ([]airtable.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Match dedup queries: the OR(FIND...) formula embeds each stamp line. Return
	// records carrying the stamp in Custom Fields so the parser can map them back.
	var out []airtable.Record
	for subID, recID := range f.stamped {
		if opts != nil && strings.Contains(opts.FilterByFormula, StampLine(subID)) {
			out = append(out, airtable.Record{ID: recID, Fields: map[string]any{FieldCustomFields: StampLine(subID)}})
		}
	}
	return out, nil
}

func (f *fakeAirtable) CreateRecords(_ context.Context, _ string, fields []map[string]any, _ bool) ([]airtable.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return nil, f.createErr
	}
	out := make([]airtable.Record, 0, len(fields))
	for _, fl := range fields {
		f.nextRecNum++
		f.created = append(f.created, fl)
		out = append(out, airtable.Record{ID: recID(f.nextRecNum)})
	}
	return out, nil
}

func recID(n int) string { return "rec" + string(rune('0'+n)) }

// --- helpers ---

func testManager(store Store, fc FilloutAPI, ac AirtableAPI) *Manager {
	return NewManager(store, fc, ac, time.Minute, log.New(io.Discard, "", 0))
}

func mappingJSON(t *testing.T, entries ...MappingEntry) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(Mapping{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sub(id string, secs int, score int) fillout.Submission {
	return fillout.Submission{
		SubmissionID:   id,
		SubmissionTime: time.Date(2026, 6, 15, 0, 0, secs, 0, time.UTC),
		Questions:      []fillout.QuestionAnswer{ans("q_score", "Recommend?", score)},
	}
}

// --- tests ---

func TestSyncOnce_CreatesNewRecords(t *testing.T) {
	store := newFakeStore()
	fc := &fakeFillout{subs: []fillout.Submission{sub("s1", 1, 9), sub("s2", 2, 7)}}
	at := newFakeAirtable()
	m := testManager(store, fc, at)

	job := &db.SyncJob{ID: 1, FilloutFormID: "form1", AirtableTable: "NPS"}
	mapping, _ := parseMapping(mappingJSON(t, MappingEntry{QuestionID: "q_score", Target: "score"}))

	cursor := m.syncOnce(context.Background(), job, mapping, nil)

	created, skipped := store.outcomeCounts()
	if created != 2 || skipped != 0 {
		t.Errorf("created=%d skipped=%d, want 2/0", created, skipped)
	}
	if len(at.created) != 2 {
		t.Errorf("airtable created %d records, want 2", len(at.created))
	}
	if cursor == nil || !cursor.Equal(time.Date(2026, 6, 15, 0, 0, 2, 0, time.UTC)) {
		t.Errorf("cursor = %v, want max submission time", cursor)
	}
}

func TestSyncOnce_SkipsLedgerDuplicates(t *testing.T) {
	store := newFakeStore()
	// Pretend s1 was already synced previously.
	store.ledger["s1"] = db.SubmissionRecord{FilloutSubmissionID: "s1", Outcome: db.OutcomeCreated}

	fc := &fakeFillout{subs: []fillout.Submission{sub("s1", 1, 9), sub("s2", 2, 7)}}
	at := newFakeAirtable()
	m := testManager(store, fc, at)

	job := &db.SyncJob{ID: 1, FilloutFormID: "form1", AirtableTable: "NPS"}
	mapping, _ := parseMapping(mappingJSON(t, MappingEntry{QuestionID: "q_score", Target: "score"}))

	m.syncOnce(context.Background(), job, mapping, nil)

	if len(at.created) != 1 {
		t.Fatalf("airtable created %d, want 1 (s1 already in ledger)", len(at.created))
	}
}

func TestSyncOnce_LedgerPreventsResyncOfDeletedRecord(t *testing.T) {
	store := newFakeStore()
	// s1 was synced before (in our ledger) but its Airtable record was manually
	// deleted — the fake Airtable has no stamped records and would happily create.
	store.ledger["s1"] = db.SubmissionRecord{FilloutSubmissionID: "s1", Outcome: db.OutcomeCreated}

	fc := &fakeFillout{subs: []fillout.Submission{sub("s1", 1, 9)}}
	at := newFakeAirtable()
	m := testManager(store, fc, at)

	job := &db.SyncJob{ID: 1, FilloutFormID: "form1", AirtableTable: "NPS"}
	mapping, _ := parseMapping(mappingJSON(t, MappingEntry{QuestionID: "q_score", Target: "score"}))

	m.syncOnce(context.Background(), job, mapping, nil)

	if len(at.created) != 0 {
		t.Errorf("airtable created %d, want 0 (ledgered record must not be re-synced after manual deletion)", len(at.created))
	}
}

func TestSyncOnce_SkipsAirtableStamped(t *testing.T) {
	store := newFakeStore()
	fc := &fakeFillout{subs: []fillout.Submission{sub("s1", 1, 9)}}
	at := newFakeAirtable()
	at.stamped["s1"] = "recExisting" // another setup already wrote this submission
	m := testManager(store, fc, at)

	job := &db.SyncJob{ID: 1, FilloutFormID: "form1", AirtableTable: "NPS"}
	mapping, _ := parseMapping(mappingJSON(t, MappingEntry{QuestionID: "q_score", Target: "score"}))

	m.syncOnce(context.Background(), job, mapping, nil)

	created, skipped := store.outcomeCounts()
	if created != 0 || skipped != 1 {
		t.Errorf("created=%d skipped=%d, want 0/1", created, skipped)
	}
	if len(at.created) != 0 {
		t.Errorf("airtable created %d, want 0 (already stamped)", len(at.created))
	}
	if rec := store.ledger["s1"]; rec.AirtableRecordID != "recExisting" {
		t.Errorf("skipped ledger record id = %q, want recExisting", rec.AirtableRecordID)
	}
}

func TestSyncOnce_IdempotentAcrossRuns(t *testing.T) {
	store := newFakeStore()
	fc := &fakeFillout{subs: []fillout.Submission{sub("s1", 1, 9), sub("s2", 2, 7)}}
	at := newFakeAirtable()
	m := testManager(store, fc, at)

	job := &db.SyncJob{ID: 1, FilloutFormID: "form1", AirtableTable: "NPS"}
	mapping, _ := parseMapping(mappingJSON(t, MappingEntry{QuestionID: "q_score", Target: "score"}))

	cursor := m.syncOnce(context.Background(), job, mapping, nil)
	// Second pass re-fetches (cursor-1s overlap) but must create nothing new.
	m.syncOnce(context.Background(), job, mapping, cursor)

	if len(at.created) != 2 {
		t.Errorf("airtable created %d across two passes, want 2", len(at.created))
	}
}

func TestSyncOnce_StopsAndRecordsErrorOnCreateFailure(t *testing.T) {
	store := newFakeStore()
	fc := &fakeFillout{subs: []fillout.Submission{sub("s1", 1, 9)}}
	at := newFakeAirtable()
	at.createErr = errors.New("airtable boom")
	m := testManager(store, fc, at)

	job := &db.SyncJob{ID: 1, FilloutFormID: "form1", AirtableTable: "NPS"}
	mapping, _ := parseMapping(mappingJSON(t, MappingEntry{QuestionID: "q_score", Target: "score"}))

	cursor := m.syncOnce(context.Background(), job, mapping, nil)

	if store.lastErr == "" {
		t.Error("expected lastErr to be recorded on create failure")
	}
	// Cursor must not advance past the failed submission.
	if cursor != nil {
		t.Errorf("cursor = %v, want nil (no submission fully processed)", cursor)
	}
	if _, ok := store.ledger["s1"]; ok {
		t.Error("failed submission must not be recorded in the ledger (so it retries)")
	}
}

func TestDedupFormula(t *testing.T) {
	got := dedupFormula("abc123")
	want := `FIND("Fillout Submission: abc123", {Custom Fields})`
	if got != want {
		t.Errorf("dedupFormula = %q, want %q", got, want)
	}
}
