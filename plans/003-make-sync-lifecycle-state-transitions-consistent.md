# Plan 003: Make sync lifecycle state transitions consistent

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- app.go app_test.go`
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

The stop/remove handlers currently cancel the in-memory poller before the durable database change succeeds. If the database update/delete fails, the process can stop polling while the row still says `active`, so a restart behaves differently from the running process. The remove handler also captures created Airtable record IDs before stopping the poller, leaving a race where a poller could create another row between capture and deletion. The durable state transition should happen first, then the in-memory manager should follow that committed state.

## Current state

Relevant files:

- `app.go` — owns `handleStop`, `handleResume`, `handleRemoveConfirm`, and `handleRemove`.
- `app_test.go` — has DB-backed handler tests for remove behavior.

Excerpts:

```go
// app.go:464-478
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	if !ok {
		http.Error(w, "invalid job id", http.StatusBadRequest)
		return
	}
	s.manager.StopJob(id)
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusStopped); err != nil {
		s.serverError(w, "stopping job", err)
		return
	}
	if user, ok := auth.UserFromContext(r.Context()); ok {
		s.audit(r, db.AuditEntry{ActorEmail: user.Email, Action: db.ActionSyncStopped, SyncJobID: &id, Details: "Stopped sync."})
	}
	http.Redirect(w, r, "/?flash="+url.QueryEscape("Sync stopped"), http.StatusFound)
}
```

```go
// app.go:568-579
// Capture the record IDs while the ledger still exists (DeleteJob cascades it).
var recordIDs []string
if deleteRecords {
	recordIDs, err = s.store.CreatedRecordIDs(r.Context(), id)
	if err != nil {
		s.serverError(w, "loading created records", err)
		return
	}
}

s.manager.StopJob(id)
if err := s.store.DeleteJob(r.Context(), id); err != nil {
	s.serverError(w, "removing job", err)
	return
}
```

```go
// app_test.go:439-466 (shape of existing DB-backed remove test)
job, err := database.CreateJob(ctx, &db.SyncJob{
	FilloutFormID: formID, FilloutFormName: "Removable", AirtableBaseID: "appTest",
	AirtableTable: "NPS", CreatedByEmail: "z@hackclub.com", Status: db.StatusActive,
})
...
form := strings.NewReader("job_id=" + strconv.FormatInt(job.ID, 10) + "&delete_records=1")
req := httptest.NewRequest(http.MethodPost, "/sync/remove", form)
req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
rec := httptest.NewRecorder()
s.handleRemove(rec, req)
```

Repo conventions to match:

- Handlers parse IDs with `parseJobID` and use `s.serverError(w, "action", err)` for unexpected failures.
- Audit writes are best effort through `s.audit` and should not block the user.
- Existing remove tests use `dbBackedServer(t)` and a fake Airtable, skipping when `DATABASE_URL` is unset.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| App tests | `go test ./...` | exit 0 |
| DB-backed app tests | `DATABASE_URL='<throwaway postgres URL>' go test .` | exit 0; DB-backed tests execute |
| Full local gate | `make test` | exit 0 after Plan 001 |
| Vet | `make vet` | exit 0 after Plan 001 |

## Scope

**In scope** (the only files you should modify):

- `app.go`
- `app_test.go`

**Out of scope** (do NOT touch):

- `internal/nps/Manager` internals.
- Database schema.
- Airtable delete semantics.
- Authorization policy; Plan 004 handles who is allowed to perform lifecycle actions.

## Git workflow

- Branch: `advisor/003-consistent-sync-lifecycle-transitions`
- Commit message style: short imperative sentence, e.g. `Persist sync lifecycle state before stopping pollers`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Change stop to persist before cancelling the poller

In `app.go`, update `handleStop` so it calls `s.store.SetJobStatus(..., db.StatusStopped)` before `s.manager.StopJob(id)`. If `SetJobStatus` fails, return the server error and do not call `StopJob`.

Preferred shape:

```go
if err := s.store.SetJobStatus(r.Context(), id, db.StatusStopped); err != nil {
    s.serverError(w, "stopping job", err)
    return
}
s.manager.StopJob(id)
```

If you need the form ID for audit, load the job before changing status. Keep that load small and do not introduce a transaction unless necessary.

**Verify**: `go test ./...` → exits 0.

### Step 2: Change remove to stop after durable stop, then capture record IDs

In `app.go`, update `handleRemove` to avoid both races:

1. Load the job as it does today.
2. If the job exists, first persist a stopped status with `s.store.SetJobStatus(r.Context(), id, db.StatusStopped)`.
   - If this fails, return a server error and do not stop the in-memory poller.
   - If the job is already gone, keep the existing "already removed" behavior.
3. Call `s.manager.StopJob(id)` only after the stopped status succeeds.
4. After the poller is stopped, capture `CreatedRecordIDs` if `delete_records` is set.
5. Delete the job with `s.store.DeleteJob`.
6. Keep the current best-effort Airtable deletion and flash behavior.

This order intentionally means that if `DeleteJob` fails after the stopped status succeeds, the durable row remains `stopped` rather than `active`; that is safer than an active row with no in-memory poller.

Do not call `CreatedRecordIDs` before stopping the poller; that is the race to fix.

**Verify**: `go test ./...` → exits 0.

### Step 3: Add regression tests for ordering-sensitive behavior

Add tests in `app_test.go`.

Minimum tests:

1. **Stop does not cancel the poller when the DB update fails**
   - Use `dbBackedServer(t)`.
   - Create a job.
   - Start its poller with `s.manager.StartJob(job)`.
   - Delete the job directly with `database.DeleteJob(ctx, job.ID)` so `SetJobStatus` will fail with not found.
   - Call `s.handleStop` with that job ID.
   - Assert the response is `500`.
   - Assert `s.manager.IsRunning(job.ID)` is still true. Then call `s.manager.StopJob(job.ID)` in cleanup so the test does not leak a goroutine.

2. **Remove captures records after stopping the poller**
   - If direct ordering is hard to observe with the concrete `*db.DB`, at least adjust the existing remove tests to assert the job status is set/stopped before deletion if you introduce a helper.
   - Prefer adding a small unexported helper in `app.go` only if necessary, but keep it local to handlers and avoid broad interfaces.

Keep existing tests `TestRemoveJobDeletesOnlyItsOwnCreatedRecords` and `TestRemoveJobKeepsRecordsByDefault` passing.

**Verify**: `DATABASE_URL='<throwaway postgres URL>' go test . -run 'Test(Stop|Remove)'` → exits 0.

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

- `app_test.go`: stop ordering regression where DB failure must not stop a running poller.
- `app_test.go`: preserve existing remove behavior after the new ordering.

Use existing `dbBackedServer(t)` tests as the pattern; they already build a real DB-backed server and fake Airtable.

## Done criteria

All must hold:

- [ ] `handleStop` updates DB status before calling `StopJob`.
- [ ] `handleStop` does not stop the poller when `SetJobStatus` fails.
- [ ] `handleRemove` persists a stopped status before stopping the poller.
- [ ] `handleRemove` captures `CreatedRecordIDs` after stopping the poller, not before.
- [ ] Existing remove-with-delete and remove-keep-records tests still pass.
- [ ] New regression test covers DB failure ordering for stop.
- [ ] `DATABASE_URL='<throwaway postgres URL>' go test .` exits 0.
- [ ] `make vet && make test` exits 0 after Plan 001.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- The handler code no longer matches the excerpts and already uses a transaction or helper with different semantics.
- Testing the failure path requires changing `internal/nps/Manager` internals.
- A race-free remove implementation appears to require a DB transaction spanning external Airtable calls. Do not hold DB transactions while calling Airtable; report back instead.
- A verification command fails twice after a reasonable fix attempt.

## Maintenance notes

Keep durable DB state as the source of truth and make in-memory pollers follow it. Reviewers should scrutinize any future handler that calls `StartJob` or `StopJob` before the corresponding database state is committed.
