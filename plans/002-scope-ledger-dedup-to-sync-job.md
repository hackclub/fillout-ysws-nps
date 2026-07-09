# Plan 002: Scope submission ledger deduplication to each sync job

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- internal/db/migrations internal/db/ledger.go internal/db/db_test.go internal/nps/sync.go internal/nps/sync_test.go`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: `plans/001-establish-complete-verification-gates.md`
- **Category**: bug
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

The app permits one sync per Fillout form, Airtable base, and Airtable table. That means the same Fillout form can be synced to different Airtable destinations. The ledger currently treats `(fillout_form_id, fillout_submission_id)` as globally unique, so a submission processed by one destination can suppress the same submission for another destination. The fix is to keep deduplication scoped to the sync job while leaving the Airtable-side stamped-record check in place to prevent duplicates in the same destination table.

## Current state

Relevant files:

- `internal/db/migrations/0001_init.sql` — creates `sync_jobs` and `synced_submissions`.
- `internal/db/ledger.go` — inserts and queries ledger rows.
- `internal/nps/sync.go` — poller calls the ledger before Airtable dedup.
- `internal/db/db_test.go` — DB integration tests for ledger behavior.
- `internal/nps/sync_test.go` — fake store tests for poller dedup behavior.

Excerpts:

```sql
-- internal/db/migrations/0001_init.sql:19
UNIQUE (fillout_form_id, airtable_base_id, airtable_table)

-- internal/db/migrations/0001_init.sql:25-34
CREATE TABLE IF NOT EXISTS synced_submissions (
    id                    BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    sync_job_id           BIGINT      NOT NULL REFERENCES sync_jobs(id) ON DELETE CASCADE,
    fillout_form_id       TEXT        NOT NULL,
    fillout_submission_id TEXT        NOT NULL,
    airtable_record_id    TEXT        NOT NULL DEFAULT '',
    outcome               TEXT        NOT NULL,
    error_text            TEXT        NOT NULL DEFAULT '',
    synced_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (fillout_form_id, fillout_submission_id)
);
```

```go
// internal/db/ledger.go:28-34
func (db *DB) RecordSubmission(ctx context.Context, rec SubmissionRecord) (inserted bool, err error) {
	tag, err := db.pool.Exec(ctx,
		`INSERT INTO synced_submissions
			(sync_job_id, fillout_form_id, fillout_submission_id, airtable_record_id, outcome, error_text)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (fillout_form_id, fillout_submission_id) DO NOTHING`,
```

```go
// internal/db/ledger.go:42-52
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
```

```go
// internal/db/ledger.go:87-92
	tag, err := db.pool.Exec(ctx,
		`INSERT INTO synced_submissions
			(sync_job_id, fillout_form_id, fillout_submission_id, airtable_record_id, outcome, error_text)
		 SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::text[], $5::text[], $6::text[])
		 ON CONFLICT (fillout_form_id, fillout_submission_id) DO NOTHING`,
```

```go
// internal/nps/sync.go:247-255
// Authoritative "already synced" check against our own ledger. Because this
// runs before any Airtable lookup, a submission we've recorded is never
// re-created — so manually deleting its record in Airtable (e.g. a test
// record) does NOT cause it to be re-synced later.
ids := make([]string, len(batch))
for i, s := range batch {
	ids[i] = s.SubmissionID
}
synced, err := m.store.ExistingSubmissionIDs(ctx, job.FilloutFormID, ids)
```

Repo conventions to match:

- DB migrations are embedded from `internal/db/migrations/*.sql` and named with zero-padded increasing numbers.
- DB methods wrap errors with a `db:` prefix, e.g. `fmt.Errorf("db: recording submissions: %w", err)`.
- Integration tests that need Postgres use `testDB(t)` in `internal/db/db_test.go` and skip when `DATABASE_URL` is unset.
- Poller tests use fakes in `internal/nps/sync_test.go`; keep them small and deterministic.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Root tests | `go test ./...` | exit 0 |
| DB tests without DB | `env -u DATABASE_URL go test ./internal/db` | exit 0 with DB tests skipped |
| DB tests with DB | `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db` | exit 0; DB tests execute |
| NPS tests | `go test ./internal/nps` | exit 0 |
| Full local gate | `make test` | exit 0 after Plan 001 is complete |
| Vet | `make vet` | exit 0 after Plan 001 is complete |

## Scope

**In scope** (the only files you should modify):

- `internal/db/migrations/0004_scope_ledger_dedup_by_job.sql` (create)
- `internal/db/ledger.go`
- `internal/db/db_test.go`
- `internal/nps/sync.go`
- `internal/nps/sync_test.go`

**Out of scope** (do NOT touch):

- Airtable-side stamped-record dedup in `internal/nps/dedup.go`; it should continue preventing duplicates already present in the destination table.
- `sync_jobs` uniqueness. The duplicate job policy stays one job per form/base/table.
- UI behavior and route handlers.
- Any data migration that deletes ledger rows. This plan should only change constraints and code behavior.

## Git workflow

- Branch: `advisor/002-scope-ledger-dedup-to-sync-job`
- Commit message style: short imperative sentence, e.g. `Scope synced submission ledger by job`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add a migration that changes the unique constraint

Create `internal/db/migrations/0004_scope_ledger_dedup_by_job.sql`.

The migration should:

1. Drop the old generated unique constraint if it exists:
   `synced_submissions_fillout_form_id_fillout_submission_id_key`.
2. Add a named unique constraint if it does not already exist for `(sync_job_id, fillout_submission_id)`.

Postgres does not support `ADD CONSTRAINT IF NOT EXISTS` in every version. Use an idempotent `DO $$ BEGIN ... END $$;` block that checks `pg_constraint` before adding the new constraint. Keep the old `fillout_form_id` column; it remains useful for display/debugging and existing code.

Target SQL shape:

```sql
ALTER TABLE synced_submissions
  DROP CONSTRAINT IF EXISTS synced_submissions_fillout_form_id_fillout_submission_id_key;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint
    WHERE conname = 'synced_submissions_sync_job_submission_key'
  ) THEN
    ALTER TABLE synced_submissions
      ADD CONSTRAINT synced_submissions_sync_job_submission_key
      UNIQUE (sync_job_id, fillout_submission_id);
  END IF;
END $$;
```

**Verify**: `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db -run TestMigrate_Idempotent` → exits 0.

### Step 2: Change DB ledger APIs to use `sync_job_id`

In `internal/db/ledger.go`:

1. Change comments from "for the form" to "for the sync job" where applicable.
2. Change `RecordSubmission` and `RecordSubmissions` conflict clauses to:
   `ON CONFLICT (sync_job_id, fillout_submission_id) DO NOTHING`.
3. Change `ExistingSubmissionIDs` signature from:

   ```go
   ExistingSubmissionIDs(ctx context.Context, formID string, submissionIDs []string) (map[string]bool, error)
   ```

   to:

   ```go
   ExistingSubmissionIDs(ctx context.Context, syncJobID int64, submissionIDs []string) (map[string]bool, error)
   ```

4. Change its query to:

   ```sql
   SELECT fillout_submission_id FROM synced_submissions
   WHERE sync_job_id = $1 AND fillout_submission_id = ANY($2)
   ```

5. If `IsSubmissionSynced` remains, change it to take `syncJobID int64` instead of `formID string`, and query `sync_job_id = $1`. If no production code uses it, it is still okay to keep it updated for tests and callers.

**Verify**: `go test ./internal/db` → exits 0 when `DATABASE_URL` is unset (tests may skip).

### Step 3: Update the poller store interface and call site

In `internal/nps/sync.go`:

1. Change the `Store` interface method to:

   ```go
   ExistingSubmissionIDs(ctx context.Context, syncJobID int64, submissionIDs []string) (map[string]bool, error)
   ```

2. Change the call in `syncBatch` from `job.FilloutFormID` to `job.ID`:

   ```go
   synced, err := m.store.ExistingSubmissionIDs(ctx, job.ID, ids)
   ```

3. Update the nearby comment so it still explains that the ledger prevents re-syncing a submission for the same job after manual Airtable deletion. Do not claim it suppresses other jobs/targets.

**Verify**: `go test ./internal/nps` → exits 0.

### Step 4: Update fake stores and add regression tests

In `internal/nps/sync_test.go`:

1. Update `fakeStore.ExistingSubmissionIDs` to match the new signature.
2. If the fake ledger is keyed only by submission ID today, change it to avoid hiding the new behavior. A simple option is a helper key such as `fmt.Sprintf("%d/%s", syncJobID, submissionID)` or a nested map.
3. Add a regression test proving two different jobs can process the same Fillout submission independently:
   - job 1 has ID 1, form `form1`, table `NPS`.
   - job 2 has ID 2, same form `form1`, table `Other NPS`.
   - both see submission `s1`.
   - after `syncOnce` for each job, Airtable create count is 2 and the ledger has two created records, one under each job.

In `internal/db/db_test.go`:

1. Extend `TestBatchLedger` or add `TestLedgerDedupScopedToJob`.
2. Create two jobs with the same `FilloutFormID` but different `AirtableTable` (for example `NPS` and `NPS Archive`) so `CreateJob` allows both.
3. Record the same `FilloutSubmissionID` under both jobs.
4. Assert both inserts succeed.
5. Assert `ExistingSubmissionIDs(ctx, job1.ID, []string{"s1"})` is true for job 1 and `ExistingSubmissionIDs(ctx, job2.ID, []string{"s1"})` is true for job 2.
6. Assert reinserting `s1` for job 1 is skipped.

**Verify**: `go test ./internal/nps` → exits 0 and includes the new regression test.

**Verify**: `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db` → exits 0 and runs the new DB regression test.

### Step 5: Run full verification

Run the full suite after Plan 001 has landed.

**Verify**: `make vet && make test` → exits 0.

If Plan 001 has not landed in your branch, run these explicit commands instead:

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

- `internal/db/db_test.go`: DB-backed regression for same Fillout submission under two different `sync_job_id` values.
- `internal/nps/sync_test.go`: poller regression proving two jobs with the same form can create separate destination records.

Use existing tests as patterns:

- Model DB setup after `TestBatchLedger` in `internal/db/db_test.go`.
- Model poller setup after `TestSyncOnce_CreatesNewRecords` and `TestSyncOnce_IdempotentAcrossRuns` in `internal/nps/sync_test.go`.

## Done criteria

All must hold:

- [ ] A new migration changes ledger uniqueness to `(sync_job_id, fillout_submission_id)` idempotently.
- [ ] `RecordSubmission`, `RecordSubmissions`, `ExistingSubmissionIDs`, and `IsSubmissionSynced` (if kept) use `sync_job_id` for dedup.
- [ ] `internal/nps.Store` and `syncBatch` use `job.ID` for ledger checks.
- [ ] Tests cover same submission ID under two different jobs.
- [ ] `DATABASE_URL='<throwaway postgres URL>' go test ./internal/db` exits 0.
- [ ] `go test ./internal/nps` exits 0.
- [ ] `make vet && make test` exits 0 after Plan 001.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- The live ledger schema no longer has the old unique constraint shown above.
- Existing production data contains duplicate `(sync_job_id, fillout_submission_id)` rows before the new constraint is added. That should be impossible under the old broader constraint, so it indicates manual DB drift.
- The fix appears to require changing Airtable stamped-record dedup semantics.
- DB tests cannot be run against a disposable Postgres database after two reasonable attempts.

## Maintenance notes

Future dedup changes must keep the two layers distinct: the local ledger is scoped to a sync job, while `FindStampedRecords` checks the destination Airtable table for already-stamped rows. Reviewers should pay close attention to any query that uses `fillout_form_id` alone for dedup; that is the old bug pattern.
