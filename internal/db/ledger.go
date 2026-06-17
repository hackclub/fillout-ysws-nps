package db

import (
	"context"
	"fmt"
)

// Submission outcome values recorded in the ledger.
const (
	OutcomeCreated         = "created"
	OutcomeSkippedExisting = "skipped_existing"
	OutcomeError           = "error"
)

// SubmissionRecord is a ledger entry for one processed Fillout submission.
type SubmissionRecord struct {
	SyncJobID           int64
	FilloutFormID       string
	FilloutSubmissionID string
	AirtableRecordID    string
	Outcome             string
	ErrorText           string
}

// RecordSubmission inserts a ledger entry. It reports whether a new row was
// inserted; a false return means an entry for this (form, submission) already
// existed and nothing was changed (the fast-path dedup guarantee).
func (db *DB) RecordSubmission(ctx context.Context, rec SubmissionRecord) (inserted bool, err error) {
	tag, err := db.pool.Exec(ctx,
		`INSERT INTO synced_submissions
			(sync_job_id, fillout_form_id, fillout_submission_id, airtable_record_id, outcome, error_text)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (fillout_form_id, fillout_submission_id) DO NOTHING`,
		rec.SyncJobID, rec.FilloutFormID, rec.FilloutSubmissionID,
		rec.AirtableRecordID, rec.Outcome, rec.ErrorText)
	if err != nil {
		return false, fmt.Errorf("db: recording submission: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ExistingSubmissionIDs returns the subset of submissionIDs that already have a
// ledger entry for the form. It is the batched form of IsSubmissionSynced.
func (db *DB) ExistingSubmissionIDs(ctx context.Context, formID string, submissionIDs []string) (map[string]bool, error) {
	out := make(map[string]bool, len(submissionIDs))
	if len(submissionIDs) == 0 {
		return out, nil
	}
	rows, err := db.pool.Query(ctx,
		`SELECT fillout_submission_id FROM synced_submissions
		 WHERE fillout_form_id = $1 AND fillout_submission_id = ANY($2)`,
		formID, submissionIDs)
	if err != nil {
		return nil, fmt.Errorf("db: checking submissions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: scanning submission id: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// RecordSubmissions inserts ledger entries in a single statement, skipping any
// that already exist. It returns the number of rows actually inserted.
func (db *DB) RecordSubmissions(ctx context.Context, recs []SubmissionRecord) (int, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	jobIDs := make([]int64, len(recs))
	formIDs := make([]string, len(recs))
	subIDs := make([]string, len(recs))
	recordIDs := make([]string, len(recs))
	outcomes := make([]string, len(recs))
	errTexts := make([]string, len(recs))
	for i, r := range recs {
		jobIDs[i] = r.SyncJobID
		formIDs[i] = r.FilloutFormID
		subIDs[i] = r.FilloutSubmissionID
		recordIDs[i] = r.AirtableRecordID
		outcomes[i] = r.Outcome
		errTexts[i] = r.ErrorText
	}
	tag, err := db.pool.Exec(ctx,
		`INSERT INTO synced_submissions
			(sync_job_id, fillout_form_id, fillout_submission_id, airtable_record_id, outcome, error_text)
		 SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[])
		 ON CONFLICT (fillout_form_id, fillout_submission_id) DO NOTHING`,
		jobIDs, formIDs, subIDs, recordIDs, outcomes, errTexts)
	if err != nil {
		return 0, fmt.Errorf("db: recording submissions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// IsSubmissionSynced reports whether a ledger entry already exists for the given
// form and submission.
func (db *DB) IsSubmissionSynced(ctx context.Context, formID, submissionID string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM synced_submissions
			WHERE fillout_form_id = $1 AND fillout_submission_id = $2)`,
		formID, submissionID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("db: checking submission: %w", err)
	}
	return exists, nil
}

// JobStats summarizes a job's ledger outcomes.
type JobStats struct {
	Created         int
	SkippedExisting int
	Errored         int
}

// Total returns the total number of processed submissions.
func (s JobStats) Total() int {
	return s.Created + s.SkippedExisting + s.Errored
}

// StatsForJob returns the ledger outcome counts for a job.
func (db *DB) StatsForJob(ctx context.Context, jobID int64) (JobStats, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT outcome, COUNT(*) FROM synced_submissions
		 WHERE sync_job_id = $1 GROUP BY outcome`, jobID)
	if err != nil {
		return JobStats{}, fmt.Errorf("db: job stats: %w", err)
	}
	defer rows.Close()

	var stats JobStats
	for rows.Next() {
		var outcome string
		var count int
		if err := rows.Scan(&outcome, &count); err != nil {
			return JobStats{}, fmt.Errorf("db: scanning job stats: %w", err)
		}
		switch outcome {
		case OutcomeCreated:
			stats.Created = count
		case OutcomeSkippedExisting:
			stats.SkippedExisting = count
		case OutcomeError:
			stats.Errored = count
		}
	}
	return stats, rows.Err()
}
