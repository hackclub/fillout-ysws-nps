# Plan 005: Batch dashboard job statistics

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- app.go internal/db/ledger.go internal/db/db_test.go app_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S/M
- **Risk**: LOW
- **Depends on**: `plans/001-establish-complete-verification-gates.md`
- **Category**: perf
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

The dashboard currently loads jobs once and then runs one aggregate query per job. That is acceptable for a handful of syncs, but latency and DB load grow linearly as the number of jobs grows. A single batched aggregate query can return stats for all visible jobs and make the dashboard cost stable.

## Current state

Relevant files:

- `app.go` — `handleHome` builds dashboard rows.
- `internal/db/ledger.go` — `StatsForJob` aggregates ledger outcomes for one job.
- `internal/db/db_test.go` — DB integration tests for stats.
- `app_test.go` — app handler/template tests.

Excerpts:

```go
// app.go:212-220
jobs, err := s.store.ListJobs(r.Context())
if err != nil {
	s.serverError(w, "loading jobs", err)
	return
}
rows := make([]jobRow, 0, len(jobs))
for _, j := range jobs {
	stats, err := s.store.StatsForJob(r.Context(), j.ID)
	if err != nil {
```

```go
// internal/db/ledger.go:152-156
// StatsForJob returns the ledger outcome counts for a job.
func (db *DB) StatsForJob(ctx context.Context, jobID int64) (JobStats, error) {
	rows, err := db.pool.Query(ctx,
		`SELECT outcome, COUNT(*) FROM synced_submissions
		 WHERE sync_job_id = $1 GROUP BY outcome`, jobID)
```

```go
// internal/db/jobs.go:127-130
// ListJobs returns all jobs, newest first.
func (db *DB) ListJobs(ctx context.Context) ([]*SyncJob, error) {
	return db.queryJobs(ctx,
		`SELECT `+jobColumns+` FROM sync_jobs ORDER BY created_at DESC`)
}
```

Repo conventions to match:

- DB query helpers live on `*db.DB` and return typed structs/maps, not raw rows.
- Existing stat counters are `JobStats{Created, SkippedExisting, Errored}` and `StatsForJob` should remain available for remove confirmation.
- DB tests use `testDB(t)` and skip when `DATABASE_URL` is unset.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| DB tests without DB | `env -u DATABASE_URL go test ./internal/db` | exit 0 with DB tests skipped |
| DB tests with DB | `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db` | exit 0; DB tests execute |
| App tests | `go test .` | exit 0 |
| Full local gate | `make test` | exit 0 after Plan 001 |
| Vet | `make vet` | exit 0 after Plan 001 |

## Scope

**In scope** (the only files you should modify):

- `internal/db/ledger.go`
- `internal/db/db_test.go`
- `app.go`
- `app_test.go` (only if needed for handler/template assertions)

**Out of scope** (do NOT touch):

- Dashboard visual design.
- Job ordering.
- `StatsForJob` behavior for a single job; keep it for remove confirmation.
- Database schema or indexes unless a measured query plan proves an index is required. Do not add speculative indexes in this plan.

## Git workflow

- Branch: `advisor/005-batch-dashboard-job-stats`
- Commit message style: short imperative sentence, e.g. `Batch dashboard job stats`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add a batched DB stats method

In `internal/db/ledger.go`, add:

```go
func (db *DB) StatsForJobs(ctx context.Context, jobIDs []int64) (map[int64]JobStats, error) { ... }
```

Requirements:

1. Return an empty map when `jobIDs` is empty.
2. Use one query shaped like:

   ```sql
   SELECT sync_job_id, outcome, COUNT(*)
   FROM synced_submissions
   WHERE sync_job_id = ANY($1)
   GROUP BY sync_job_id, outcome
   ```

3. Populate only jobs that have ledger rows. Callers should treat a missing map entry as zero stats.
4. Use the same outcome switch as `StatsForJob`.
5. Wrap errors with `db:` like the existing methods.

Do not remove `StatsForJob`.

**Verify**: `go test ./internal/db` → exits 0 when `DATABASE_URL` is unset (tests may skip).

### Step 2: Add DB tests for the batched method

In `internal/db/db_test.go`, add `TestStatsForJobs` or extend `TestStatsAndProgress`.

Test cases:

1. Empty input returns an empty map and no error.
2. Two jobs with different ledger outcomes return separate `JobStats` values.
3. A job ID with no ledger rows is absent from the map (or returns zero if you choose to prefill; be explicit and test the chosen behavior).
4. Counts match the existing `StatsForJob` for at least one job.

Use `testDB(t)` and `RecordSubmissions` as in existing tests.

**Verify**: `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db -run TestStatsForJobs` → exits 0.

### Step 3: Use the batched method in the dashboard

In `app.go` `handleHome`:

1. After `ListJobs`, collect `jobIDs := make([]int64, 0, len(jobs))`.
2. Call `s.store.StatsForJobs(r.Context(), jobIDs)` once.
3. In the row loop, use `stats := statsByJob[j.ID]` and remove the per-job `StatsForJob` call.
4. Keep the same error handling if the batched call fails.
5. Keep dashboard row fields unchanged.

If Plan 004 has already landed, preserve its `CanManage` row logic while replacing only the stats lookup.

**Verify**: `go test .` → exits 0.

### Step 4: Run full verification

**Verify**: `make vet && make test` → exits 0 after Plan 001.

If Plan 001 has not landed in your branch, run:

```bash
go vet ./...
(cd fillout && go vet ./...)
(cd airtable && go vet ./...)
go test ./...
(cd fillout && go test ./...)
(cd airtable && go test ./...)
```

Expected result: all exit 0.

## Test plan

New tests to write:

- `internal/db/db_test.go`: `StatsForJobs` empty input and multi-job aggregation.

Existing tests to rely on:

- `app_test.go` dashboard/template tests should keep passing, proving the UI receives the same fields.
- `StatsForJob` tests should keep passing because remove confirmation still uses the single-job method.

## Done criteria

All must hold:

- [ ] `StatsForJobs` exists and performs one aggregate query for all requested job IDs.
- [ ] `handleHome` no longer calls `StatsForJob` inside the job loop.
- [ ] DB tests cover empty and multi-job stats.
- [ ] `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db` exits 0.
- [ ] `go test .` exits 0.
- [ ] `make vet && make test` exits 0 after Plan 001.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- `handleHome` has been refactored and no longer has the N+1 query shape shown above.
- The DB driver cannot bind `[]int64` to `ANY($1)` in this codebase without introducing a new dependency. Try pgx's normal slice binding first; do not add dependencies for this.
- A query plan indicates a missing index is required for acceptable performance. Report it as a follow-up rather than adding schema changes here.

## Maintenance notes

If dashboard pagination or filtering is added later, keep stats batching aligned with the visible job IDs only. Reviewers should reject any future dashboard change that reintroduces per-row aggregate queries.
