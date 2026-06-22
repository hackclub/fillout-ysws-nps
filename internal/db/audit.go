package db

import (
	"context"
	"fmt"
	"time"
)

// Audit action identifiers.
const (
	ActionSyncCreated          = "sync.created"
	ActionSyncStopped          = "sync.stopped"
	ActionSyncResumed          = "sync.resumed"
	ActionSyncRemoved          = "sync.removed"
	ActionSyncDuplicateBlocked = "sync.duplicate_blocked"
)

// AuditEntry is a new audit record to append.
type AuditEntry struct {
	ActorEmail string
	Action     string
	SyncJobID  *int64
	FormID     string
	Details    string
}

// AuditLog is a stored audit record.
type AuditLog struct {
	ID         int64
	ActorEmail string
	Action     string
	SyncJobID  *int64
	FormID     string
	Details    string
	CreatedAt  time.Time
}

// RecordAudit appends an audit entry.
func (db *DB) RecordAudit(ctx context.Context, e AuditEntry) error {
	_, err := db.pool.Exec(ctx,
		`INSERT INTO audit_logs (actor_email, action, sync_job_id, form_id, details)
		 VALUES ($1, $2, $3, $4, $5)`,
		e.ActorEmail, e.Action, e.SyncJobID, e.FormID, e.Details)
	if err != nil {
		return fmt.Errorf("db: recording audit: %w", err)
	}
	return nil
}

// ListAuditLogs returns the most recent audit entries, newest first.
func (db *DB) ListAuditLogs(ctx context.Context, limit int) ([]AuditLog, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := db.pool.Query(ctx,
		`SELECT id, actor_email, action, sync_job_id, form_id, details, created_at
		 FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("db: listing audit logs: %w", err)
	}
	defer rows.Close()

	var logs []AuditLog
	for rows.Next() {
		var l AuditLog
		if err := rows.Scan(&l.ID, &l.ActorEmail, &l.Action, &l.SyncJobID, &l.FormID, &l.Details, &l.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: scanning audit log: %w", err)
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}
