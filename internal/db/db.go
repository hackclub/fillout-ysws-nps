// Package db provides Postgres-backed persistence for sync jobs and the
// submission ledger, plus a minimal embedded-migration runner.
package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("db: not found")

// ErrJobExists is returned by CreateJob when a sync job already exists for the
// same form and target. The existing job is returned alongside it.
var ErrJobExists = errors.New("db: a sync job already exists for this form and target")

// DB is a Postgres connection pool with the application's queries.
type DB struct {
	pool *pgxpool.Pool
}

// Connect opens a connection pool to the Postgres database at url and verifies
// connectivity.
func Connect(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("db: connecting: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: pinging: %w", err)
	}
	return &DB{pool: pool}, nil
}

// Close releases the connection pool.
func (db *DB) Close() {
	if db.pool != nil {
		db.pool.Close()
	}
}

// Migrate applies any embedded migrations that have not yet been run, in
// filename order. Each migration runs in its own transaction and is recorded in
// schema_migrations so it is applied at most once.
func (db *DB) Migrate(ctx context.Context) error {
	if _, err := db.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("db: creating schema_migrations: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("db: reading migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		applied, err := db.migrationApplied(ctx, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("db: reading migration %s: %w", name, err)
		}
		if err := db.applyMigration(ctx, name, string(sqlBytes)); err != nil {
			return fmt.Errorf("db: applying migration %s: %w", name, err)
		}
	}
	return nil
}

func (db *DB) migrationApplied(ctx context.Context, version string) (bool, error) {
	var exists bool
	err := db.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, version).
		Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("db: checking migration %s: %w", version, err)
	}
	return exists, nil
}

func (db *DB) applyMigration(ctx context.Context, version, sqlText string) error {
	return pgx.BeginFunc(ctx, db.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, sqlText); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version)
		return err
	})
}
