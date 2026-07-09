# Plan 004: Enforce owner-or-admin sync management

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- app.go app_test.go internal/config/config.go internal/config/config_test.go internal/auth/auth.go internal/auth/auth_test.go .env.example templates/app.html`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: M
- **Risk**: MED
- **Depends on**: `plans/001-establish-complete-verification-gates.md`, `plans/003-make-sync-lifecycle-state-transitions-consistent.md`
- **Category**: security
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

Today any authenticated allow-listed user can stop, resume, remove, and optionally delete Airtable records for any sync job. The app already stores who created each sync, so it has enough information to distinguish job owners from other users. This plan makes the policy explicit: job creators can manage their own syncs, and admins can manage every sync and view the audit log. The default admin list should preserve existing bootstrap behavior by treating `ALLOWED_EMAILS` as admins unless `ADMIN_EMAILS` is configured.

## Current state

Relevant files:

- `app.go` — routes and handlers for sync lifecycle actions.
- `templates/app.html` — dashboard action buttons and Audit Log nav link.
- `internal/config/config.go` — parses env config.
- `internal/config/config_test.go` — config parser tests.
- `internal/auth/auth.go` — request-context user helpers.
- `app_test.go` — handler tests for remove and sync setup.
- `.env.example` — documented local env variables.

Excerpts:

```go
// app.go:72-80
mux.Handle("GET /forms/new", s.auth.RequireUser(http.HandlerFunc(s.handleNewForm)))
mux.Handle("POST /forms/preview", s.auth.RequireUser(http.HandlerFunc(s.handlePreview)))
mux.Handle("POST /forms/preview/samples", s.auth.RequireUser(http.HandlerFunc(s.handlePreviewSamples)))
mux.Handle("POST /sync/start", s.auth.RequireUser(http.HandlerFunc(s.handleStart)))
mux.Handle("POST /sync/stop", s.auth.RequireUser(http.HandlerFunc(s.handleStop)))
mux.Handle("POST /sync/resume", s.auth.RequireUser(http.HandlerFunc(s.handleResume)))
mux.Handle("GET /sync/remove", s.auth.RequireUser(http.HandlerFunc(s.handleRemoveConfirm)))
mux.Handle("POST /sync/remove", s.auth.RequireUser(http.HandlerFunc(s.handleRemove)))
mux.Handle("GET /audit", s.auth.RequireUser(http.HandlerFunc(s.handleAudit)))
```

```go
// app.go:420-428
job := &db.SyncJob{
	FilloutFormID:   formID,
	FilloutFormName: formName,
	AirtableBaseID:  s.cfg.AirtableBaseID,
	AirtableTable:   s.cfg.NPSTable,
	Mapping:         mappingJSON,
	YSWSProgram:     canonicalYSWS,
	Tags:            tags,
	CreatedByEmail:  user.Email,
```

```go
// app.go:464-487 (pre-Plan-003 shape shown for auth evidence)
func (s *Server) handleStop(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	...
	s.manager.StopJob(id)
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusStopped); err != nil {
		...
	}
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	id, ok := parseJobID(r.FormValue("job_id"))
	...
	if err := s.store.SetJobStatus(r.Context(), id, db.StatusActive); err != nil {
```

```html
<!-- templates/app.html:255-259 -->
{{if .UserEmail}}
  <a class="navlink" href="/">Dashboard</a>
  <a class="navlink" href="/forms/new">New form</a>
  <a class="navlink" href="/audit">Audit log</a>
  <span class="who">{{.UserEmail}}</span>
```

```html
<!-- templates/app.html:380-392 -->
<form method="post" action="/sync/stop" style="margin:0">
  <input type="hidden" name="csrf_token" value="{{$.CSRF}}">
  <input type="hidden" name="job_id" value="{{.ID}}">
  <button class="btn danger" type="submit">Stop sync</button>
</form>
...
<a class="removelink" href="/sync/remove?job_id={{.ID}}">Remove…</a>
```

Repo conventions to match:

- `internal/config` keeps parsing pure via `loadFrom` and tests supply a map lookup.
- Authorization middleware stores `auth.User` in request context and handlers call `auth.UserFromContext`.
- Templates receive a `baseView` embedded in page-specific view structs.
- Use `http.Error(w, "forbidden", http.StatusForbidden)` or a similarly concise non-secret message for unauthorized state-changing requests.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Config tests | `go test ./internal/config` | exit 0 |
| Auth tests | `go test ./internal/auth` | exit 0 |
| App tests | `go test .` | exit 0 |
| DB-backed app tests | `DATABASE_URL='<throwaway postgres URL>' go test .` | exit 0; DB-backed tests execute |
| Full local gate | `make test` | exit 0 after Plan 001 |
| Vet | `make vet` | exit 0 after Plan 001 |

## Scope

**In scope** (the only files you should modify):

- `internal/config/config.go`
- `internal/config/config_test.go`
- `internal/auth/auth.go`
- `internal/auth/auth_test.go`
- `app.go`
- `app_test.go`
- `templates/app.html`
- `.env.example`

**Out of scope** (do NOT touch):

- Database schema. Use existing `sync_jobs.created_by_email`.
- OAuth allow-list fetching from Airtable.
- The ability for any authenticated allow-listed user to create a new sync.
- UI redesign beyond hiding/disabling unauthorized action controls.
- Public README; Plan 008 updates contributor docs after this policy lands.

## Git workflow

- Branch: `advisor/004-owner-or-admin-sync-management`
- Commit message style: short imperative sentence, e.g. `Limit sync management to owners and admins`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Add admin email configuration

In `internal/config/config.go`:

1. Add `AdminEmails []string` to `Config`.
2. Parse optional `ADMIN_EMAILS` with the existing `parseEmails` helper.
3. If `ADMIN_EMAILS` is empty, default `AdminEmails` to `AllowedEmails` so current bootstrap admins stay admins without requiring a new env var.
4. Do not make `ADMIN_EMAILS` required in `validate`.
5. Add a method:

   ```go
   func (c *Config) IsAdmin(email string) bool { ... }
   ```

   It should normalize case/space the same way `parseEmails` does.

In `.env.example`, add a commented or optional `ADMIN_EMAILS=` entry near `ALLOWED_EMAILS`, explaining that it controls who can manage all syncs and view the audit log, and defaults to `ALLOWED_EMAILS` when unset.

In `internal/config/config_test.go`, add tests for:

- `ADMIN_EMAILS` unset → `AdminEmails` equals normalized `AllowedEmails`.
- `ADMIN_EMAILS` set → `AdminEmails` equals normalized `ADMIN_EMAILS` and does not include unrelated `ALLOWED_EMAILS` values.
- `Config.IsAdmin` is case-insensitive and trims spaces.

**Verify**: `go test ./internal/config` → exits 0.

### Step 2: Add a context helper for tests and internal handlers

In `internal/auth/auth.go`, add an exported helper:

```go
func ContextWithUser(ctx context.Context, user User) context.Context {
    return context.WithValue(ctx, userCtxKey, user)
}
```

This keeps tests from depending on the private context key while preserving the existing `UserFromContext` behavior. Add a small test in `internal/auth/auth_test.go` that `ContextWithUser` followed by `UserFromContext` returns the same user.

**Verify**: `go test ./internal/auth` → exits 0.

### Step 3: Add server-side owner/admin checks

In `app.go`:

1. Add `IsAdmin bool` to `baseView` so navigation can hide admin-only links.
2. Add `CanManage bool` to `jobRow` so each dashboard row knows whether to render Stop/Resume/Remove controls.
3. Update `s.base` and the dashboard base view to set `IsAdmin: s.cfg.IsAdmin(user.Email)`.
4. Add helper methods on `Server`:

   ```go
   func (s *Server) canManageJob(user auth.User, job *db.SyncJob) bool
   func (s *Server) requireCanManageJob(w http.ResponseWriter, r *http.Request, job *db.SyncJob) (auth.User, bool)
   func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (auth.User, bool)
   ```

   Policy:
   - Admin can manage any job.
   - Creator can manage their own job when `strings.EqualFold(strings.TrimSpace(job.CreatedByEmail), strings.TrimSpace(user.Email))`.
   - Jobs with empty `CreatedByEmail` are admin-only.

5. In `handleHome`, set `CanManage` per row.
6. In `handleStop`, `handleResume`, `handleRemoveConfirm`, and `handleRemove`, load the job before the mutation (some handlers already do) and call `requireCanManageJob` before changing DB state or calling `StartJob`/`StopJob`/Airtable.
7. In `handleAudit`, require admin. Since the route is already behind `RequireUser`, a handler-level check is sufficient.

Be careful with Plan 003 ordering: preserve the durable-before-in-memory lifecycle ordering while adding authorization.

**Verify**: `go test .` → exits 0.

### Step 4: Hide unauthorized UI controls

In `templates/app.html`:

1. Render the Audit Log nav link only when `.IsAdmin` is true.
2. Around each job's Stop/Resume form and Remove link, add `{{if .CanManage}} ... {{else}} ... {{end}}`.
3. For unauthorized jobs, show a muted note such as `Managed by {{.CreatedBy}}` instead of action buttons. Do not expose any new sensitive data; `CreatedBy` is already displayed on the row.
4. Do not hide the row itself. Seeing that a sync exists is current behavior; this plan only restricts management actions and audit-log access.

**Verify**: `go test .` → exits 0. Existing template parse tests should still pass.

### Step 5: Add app-level authorization tests

In `app_test.go`, update direct handler tests to attach a user context using the new `auth.ContextWithUser` helper where needed.

Add DB-backed tests for:

1. **Creator can remove own job**
   - Existing remove tests can cover this if the request context user is the job creator.
2. **Admin can remove another user's job**
   - Configure `cfg.AdminEmails` in the test server, create a job by `owner@example.com`, request as `admin@example.com`, expect success.
3. **Non-owner non-admin cannot remove another user's job**
   - Create a job by `owner@example.com`, request as `other@example.com`, expect `403`, job still exists, `fakeAirtable.DeleteRecords` not called.
4. **Non-owner non-admin cannot stop/resume another user's job**
   - At least one of stop or resume must be covered; ideally both.
5. **Non-admin cannot view audit log**
   - Call `handleAudit` or route with a non-admin user and expect `403`.

If constructing the full route with cookies is cumbersome, direct handler calls with `auth.ContextWithUser` are acceptable because the route-level authentication middleware is already tested in `internal/auth`.

**Verify**: `DATABASE_URL='<throwaway postgres URL>' go test . -run 'Test.*(Remove|Stop|Resume|Audit)'` → exits 0.

### Step 6: Run full verification

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

- `internal/config/config_test.go`: admin email parsing/defaulting and `IsAdmin`.
- `internal/auth/auth_test.go`: `ContextWithUser` round trip.
- `app_test.go`: owner/admin/non-owner authorization for lifecycle actions and admin-only audit access.

Use existing tests as patterns:

- Config tests: model after existing `Load`/`parseEmails` tests.
- App DB-backed tests: model after `TestRemoveJobDeletesOnlyItsOwnCreatedRecords` and `TestRemoveJobKeepsRecordsByDefault`.
- Auth tests: model after `UserFromContext`/middleware style tests in `internal/auth/auth_test.go`.

## Done criteria

All must hold:

- [ ] `ADMIN_EMAILS` is optional and defaults to normalized `ALLOWED_EMAILS` when unset.
- [ ] `Config.IsAdmin` exists and is tested.
- [ ] Job creators can manage their own jobs.
- [ ] Admins can manage any job and view `/audit`.
- [ ] Non-owner non-admin users receive `403` for stop/resume/remove and cannot trigger Airtable deletion.
- [ ] Dashboard hides Stop/Resume/Remove controls for jobs the user cannot manage.
- [ ] Audit Log nav link is visible only for admins.
- [ ] `DATABASE_URL='<throwaway postgres URL>' go test .` exits 0.
- [ ] `make vet && make test` exits 0 after Plan 001.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- Product ownership says all allow-listed users are intentionally global admins. In that case, do not implement owner-only restrictions; instead document the global-admin policy explicitly.
- Existing production jobs have empty `created_by_email` and no admin user is configured. Those jobs would become admin-only; confirm an admin migration path before deploying.
- Handler changes conflict with Plan 003 lifecycle ordering.
- Authorization tests require real OAuth or real Airtable credentials.

## Maintenance notes

Future admin-only pages should use the same `Config.IsAdmin` and handler-level admin check. Reviewers should verify that every state-changing sync action loads the job, checks owner/admin authorization, and only then mutates DB state or calls Airtable.
