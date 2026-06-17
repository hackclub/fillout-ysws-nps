package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"
)

// testDB connects to the database named by DATABASE_URL, runs migrations, and
// truncates the application tables. It skips the test when DATABASE_URL is unset
// so the unit suite stays runnable without Postgres (mirrors the airtable
// integration-test pattern). Only point DATABASE_URL at a throwaway dev DB.
func testDB(t *testing.T) *DB {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping db integration test")
	}
	ctx := context.Background()
	database, err := Connect(ctx, url)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(database.Close)

	if err := database.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := database.pool.Exec(ctx, `TRUNCATE sync_jobs, synced_submissions, audit_logs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return database
}

func sampleJob() *SyncJob {
	return &SyncJob{
		FilloutFormID:   "form123",
		FilloutFormName: "NPS Form",
		AirtableBaseID:  "appTest",
		AirtableTable:   "NPS",
		Mapping:         json.RawMessage(`{"k":"v"}`),
		CreatedByEmail:  "zach@hackclub.com",
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	database := testDB(t)
	// testDB already migrated once; running again must be a no-op without error.
	if err := database.Migrate(context.Background()); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestCreateAndGetJob(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()

	created, err := database.CreateJob(ctx, sampleJob())
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("CreateJob returned zero ID")
	}
	if created.Status != StatusActive {
		t.Errorf("status = %q, want %q", created.Status, StatusActive)
	}
	if string(created.Mapping) != `{"k": "v"}` && string(created.Mapping) != `{"k":"v"}` {
		t.Errorf("mapping round-trip = %s", created.Mapping)
	}

	got, err := database.GetJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.FilloutFormID != "form123" || got.CreatedByEmail != "zach@hackclub.com" {
		t.Errorf("GetJob returned %+v", got)
	}
}

func TestCreateJob_DuplicateReturnsExisting(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()

	first, err := database.CreateJob(ctx, sampleJob())
	if err != nil {
		t.Fatalf("first CreateJob: %v", err)
	}

	// A second job with a different creator but the same form/base/table must
	// conflict and return the existing job.
	dup := sampleJob()
	dup.CreatedByEmail = "someone-else@hackclub.com"
	existing, err := database.CreateJob(ctx, dup)
	if !errors.Is(err, ErrJobExists) {
		t.Fatalf("expected ErrJobExists, got %v", err)
	}
	if existing.ID != first.ID {
		t.Errorf("existing.ID = %d, want %d", existing.ID, first.ID)
	}
	if existing.CreatedByEmail != "zach@hackclub.com" {
		t.Errorf("existing creator = %q, want original", existing.CreatedByEmail)
	}
}

func TestGetJob_NotFound(t *testing.T) {
	database := testDB(t)
	if _, err := database.GetJob(context.Background(), 999999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSetJobStatus(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()

	job, err := database.CreateJob(ctx, sampleJob())
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := database.SetJobStatus(ctx, job.ID, StatusStopped); err != nil {
		t.Fatalf("SetJobStatus: %v", err)
	}

	got, err := database.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusStopped {
		t.Errorf("status = %q, want %q", got.Status, StatusStopped)
	}
}

func TestSetJobStatus_NotFound(t *testing.T) {
	database := testDB(t)
	if err := database.SetJobStatus(context.Background(), 999999, StatusStopped); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRecordSubmission_Dedup(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()

	job, err := database.CreateJob(ctx, sampleJob())
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	rec := SubmissionRecord{
		SyncJobID:           job.ID,
		FilloutFormID:       job.FilloutFormID,
		FilloutSubmissionID: "sub1",
		AirtableRecordID:    "rec1",
		Outcome:             OutcomeCreated,
	}
	inserted, err := database.RecordSubmission(ctx, rec)
	if err != nil {
		t.Fatalf("RecordSubmission: %v", err)
	}
	if !inserted {
		t.Fatal("first RecordSubmission: inserted = false, want true")
	}

	// Re-recording the same submission must be a no-op.
	inserted, err = database.RecordSubmission(ctx, rec)
	if err != nil {
		t.Fatalf("RecordSubmission (dup): %v", err)
	}
	if inserted {
		t.Fatal("duplicate RecordSubmission: inserted = true, want false")
	}

	synced, err := database.IsSubmissionSynced(ctx, job.FilloutFormID, "sub1")
	if err != nil {
		t.Fatalf("IsSubmissionSynced: %v", err)
	}
	if !synced {
		t.Fatal("IsSubmissionSynced = false, want true")
	}
}

func TestBatchLedger(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	job, err := database.CreateJob(ctx, sampleJob())
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	recs := []SubmissionRecord{
		{SyncJobID: job.ID, FilloutFormID: job.FilloutFormID, FilloutSubmissionID: "s1", Outcome: OutcomeCreated},
		{SyncJobID: job.ID, FilloutFormID: job.FilloutFormID, FilloutSubmissionID: "s2", Outcome: OutcomeSkippedExisting},
	}
	n, err := database.RecordSubmissions(ctx, recs)
	if err != nil {
		t.Fatalf("RecordSubmissions: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted = %d, want 2", n)
	}
	// Re-inserting s1 plus a new s3: only s3 is new.
	n, err = database.RecordSubmissions(ctx, []SubmissionRecord{
		{SyncJobID: job.ID, FilloutFormID: job.FilloutFormID, FilloutSubmissionID: "s1", Outcome: OutcomeCreated},
		{SyncJobID: job.ID, FilloutFormID: job.FilloutFormID, FilloutSubmissionID: "s3", Outcome: OutcomeCreated},
	})
	if err != nil || n != 1 {
		t.Fatalf("re-insert: n=%d err=%v, want 1", n, err)
	}

	existing, err := database.ExistingSubmissionIDs(ctx, job.FilloutFormID, []string{"s1", "s2", "s3", "s4"})
	if err != nil {
		t.Fatalf("ExistingSubmissionIDs: %v", err)
	}
	if !existing["s1"] || !existing["s2"] || !existing["s3"] || existing["s4"] {
		t.Errorf("existing = %v, want s1/s2/s3 true, s4 false", existing)
	}
}

func TestAuditLog(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()
	jobID := int64(42)
	for _, e := range []AuditEntry{
		{ActorEmail: "a@hackclub.com", Action: ActionSyncCreated, SyncJobID: &jobID, FormID: "form1", Details: "created"},
		{ActorEmail: "a@hackclub.com", Action: ActionSyncStopped, SyncJobID: &jobID, Details: "stopped"},
	} {
		if err := database.RecordAudit(ctx, e); err != nil {
			t.Fatalf("RecordAudit: %v", err)
		}
	}

	logs, err := database.ListAuditLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("got %d logs, want 2", len(logs))
	}
	// Newest first.
	if logs[0].Action != ActionSyncStopped || logs[1].Action != ActionSyncCreated {
		t.Errorf("order = %q,%q", logs[0].Action, logs[1].Action)
	}
	if logs[1].SyncJobID == nil || *logs[1].SyncJobID != 42 || logs[1].FormID != "form1" {
		t.Errorf("created entry = %+v", logs[1])
	}
}

func TestStatsAndProgress(t *testing.T) {
	database := testDB(t)
	ctx := context.Background()

	job, err := database.CreateJob(ctx, sampleJob())
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	outcomes := []string{OutcomeCreated, OutcomeCreated, OutcomeSkippedExisting, OutcomeError}
	for i, outcome := range outcomes {
		if _, err := database.RecordSubmission(ctx, SubmissionRecord{
			SyncJobID:           job.ID,
			FilloutFormID:       job.FilloutFormID,
			FilloutSubmissionID: "sub" + string(rune('a'+i)),
			Outcome:             outcome,
		}); err != nil {
			t.Fatalf("RecordSubmission: %v", err)
		}
	}

	stats, err := database.StatsForJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("StatsForJob: %v", err)
	}
	if stats.Created != 2 || stats.SkippedExisting != 1 || stats.Errored != 1 {
		t.Errorf("stats = %+v", stats)
	}
	if stats.Total() != 4 {
		t.Errorf("Total = %d, want 4", stats.Total())
	}

	cursor := time.Now().UTC().Truncate(time.Second)
	if err := database.UpdateJobProgress(ctx, job.ID, &cursor, time.Now(), ""); err != nil {
		t.Fatalf("UpdateJobProgress: %v", err)
	}
	got, err := database.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Cursor == nil || !got.Cursor.Equal(cursor) {
		t.Errorf("cursor = %v, want %v", got.Cursor, cursor)
	}
	if got.LastPolledAt == nil {
		t.Error("LastPolledAt not set")
	}
}
