package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Job status values.
const (
	StatusActive  = "active"
	StatusStopped = "stopped"
)

// SyncJob is a configured Fillout-form -> Airtable-table sync.
type SyncJob struct {
	ID              int64
	FilloutFormID   string
	FilloutFormName string
	AirtableBaseID  string
	AirtableTable   string
	// Mapping is the opaque field-mapping configuration, stored as JSONB. The
	// db package does not interpret it.
	Mapping        json.RawMessage
	YSWSProgram    string
	Tags           string
	Status         string
	Cursor         *time.Time
	CreatedByEmail string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	LastPolledAt   *time.Time
	LastError      string
}

const jobColumns = `id, fillout_form_id, fillout_form_name, airtable_base_id,
	airtable_table, mapping, ysws_program, tags, status, cursor,
	created_by_email, created_at, updated_at, last_polled_at, last_error`

func scanJob(row pgx.Row) (*SyncJob, error) {
	var j SyncJob
	var mapping []byte
	err := row.Scan(
		&j.ID, &j.FilloutFormID, &j.FilloutFormName, &j.AirtableBaseID,
		&j.AirtableTable, &mapping, &j.YSWSProgram, &j.Tags, &j.Status,
		&j.Cursor, &j.CreatedByEmail, &j.CreatedAt, &j.UpdatedAt,
		&j.LastPolledAt, &j.LastError,
	)
	if err != nil {
		return nil, err
	}
	j.Mapping = json.RawMessage(mapping)
	return &j, nil
}

// CreateJob inserts a new sync job. If a job already exists for the same form
// and target (the unique constraint), it returns the existing job together with
// ErrJobExists so callers can surface "already being synced" gracefully.
func (db *DB) CreateJob(ctx context.Context, j *SyncJob) (*SyncJob, error) {
	if len(j.Mapping) == 0 {
		j.Mapping = json.RawMessage("{}")
	}
	status := j.Status
	if status == "" {
		status = StatusActive
	}

	const q = `
		INSERT INTO sync_jobs
			(fillout_form_id, fillout_form_name, airtable_base_id, airtable_table,
			 mapping, ysws_program, tags, status, cursor, created_by_email)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9, $10)
		RETURNING ` + jobColumns

	created, err := scanJob(db.pool.QueryRow(ctx, q,
		j.FilloutFormID, j.FilloutFormName, j.AirtableBaseID, j.AirtableTable,
		string(j.Mapping), j.YSWSProgram, j.Tags, status, j.Cursor,
		j.CreatedByEmail,
	))
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			existing, getErr := db.GetJobByTarget(ctx, j.FilloutFormID, j.AirtableBaseID, j.AirtableTable)
			if getErr != nil {
				return nil, fmt.Errorf("db: fetching conflicting job: %w", getErr)
			}
			return existing, ErrJobExists
		}
		return nil, fmt.Errorf("db: creating job: %w", err)
	}
	return created, nil
}

// GetJob returns a job by ID, or ErrNotFound.
func (db *DB) GetJob(ctx context.Context, id int64) (*SyncJob, error) {
	job, err := scanJob(db.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM sync_jobs WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: getting job: %w", err)
	}
	return job, nil
}

// GetJobByTarget returns the job for a form/base/table tuple, or ErrNotFound.
func (db *DB) GetJobByTarget(ctx context.Context, formID, baseID, table string) (*SyncJob, error) {
	job, err := scanJob(db.pool.QueryRow(ctx,
		`SELECT `+jobColumns+` FROM sync_jobs
		 WHERE fillout_form_id = $1 AND airtable_base_id = $2 AND airtable_table = $3`,
		formID, baseID, table))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: getting job by target: %w", err)
	}
	return job, nil
}

// ListJobs returns all jobs, newest first.
func (db *DB) ListJobs(ctx context.Context) ([]*SyncJob, error) {
	return db.queryJobs(ctx,
		`SELECT `+jobColumns+` FROM sync_jobs ORDER BY created_at DESC`)
}

// ListActiveJobs returns all jobs with status 'active'.
func (db *DB) ListActiveJobs(ctx context.Context) ([]*SyncJob, error) {
	return db.queryJobs(ctx,
		`SELECT `+jobColumns+` FROM sync_jobs WHERE status = $1 ORDER BY created_at DESC`,
		StatusActive)
}

func (db *DB) queryJobs(ctx context.Context, q string, args ...any) ([]*SyncJob, error) {
	rows, err := db.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: listing jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*SyncJob
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("db: scanning job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// UpdateJobProgress advances a job's cursor and stamps the last poll time and
// error (empty string clears any prior error).
func (db *DB) UpdateJobProgress(ctx context.Context, id int64, cursor *time.Time, polledAt time.Time, lastErr string) error {
	_, err := db.pool.Exec(ctx,
		`UPDATE sync_jobs
		 SET cursor = $2, last_polled_at = $3, last_error = $4, updated_at = now()
		 WHERE id = $1`,
		id, cursor, polledAt, lastErr)
	if err != nil {
		return fmt.Errorf("db: updating job progress: %w", err)
	}
	return nil
}

// DeleteJob removes a sync job. Its synced_submissions ledger rows are removed
// too via ON DELETE CASCADE. Returns ErrNotFound if no job matched.
func (db *DB) DeleteJob(ctx context.Context, id int64) error {
	tag, err := db.pool.Exec(ctx, `DELETE FROM sync_jobs WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db: deleting job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetJobStatus changes a job's status.
func (db *DB) SetJobStatus(ctx context.Context, id int64, status string) error {
	tag, err := db.pool.Exec(ctx,
		`UPDATE sync_jobs SET status = $2, updated_at = now() WHERE id = $1`,
		id, status)
	if err != nil {
		return fmt.Errorf("db: setting job status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
