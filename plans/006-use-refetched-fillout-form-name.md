# Plan 006: Use the refetched Fillout form name when starting syncs

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- app.go app_test.go templates/app.html`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P2
- **Effort**: S
- **Risk**: LOW
- **Depends on**: `plans/001-establish-complete-verification-gates.md`
- **Category**: bug
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

`handleStart` deliberately re-fetches the Fillout form so the submitted mapping is checked against authoritative questions. But it still trusts a hidden `form_name` field from the browser for the stored sync name, dashboard display, duplicate-sync message, and audit details. Hidden fields are user-controlled. The handler should use the freshly fetched `meta.Name` and avoid accepting a tampered form name.

## Current state

Relevant files:

- `app.go` — `handleStart` re-fetches metadata and creates the `db.SyncJob`.
- `templates/app.html` — preview form includes hidden `form_name`.
- `app_test.go` — tests preview/start behavior with fake Fillout metadata.

Excerpts:

```go
// app.go:379-389
// Re-fetch the form so the stored mapping is authoritative, then apply the
// reviewer's (possibly edited) target choices.
meta, err := s.fillout.GetForm(r.Context(), formID)
if err != nil {
	s.render(w, "page_new_form", newFormView{
		baseView: s.base(r, "New form"),
		Error:    fmt.Sprintf("Couldn't reload form %s: %v", formID, err),
	})
	return
}
formName := strings.TrimSpace(r.FormValue("form_name"))
```

```go
// app.go:419-424
tags := strings.Join(nps.NormalizeTags(tagsInput), ",")
job := &db.SyncJob{
	FilloutFormID:   formID,
	FilloutFormName: formName,
	AirtableBaseID:  s.cfg.AirtableBaseID,
	AirtableTable:   s.cfg.NPSTable,
```

```html
<!-- templates/app.html:504-507 -->
<form method="post" action="/sync/start">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <input type="hidden" name="form_id" value="{{.FormID}}">
  <input type="hidden" name="form_name" value="{{.FormName}}">
```

Repo conventions to match:

- `orDefault(formName, formID)` is used when rendering messages so empty names are tolerated.
- Form IDs are still submitted as hidden fields because the handler must know which form to refetch.
- Tests use `fakeForms` metadata in `app_test.go`.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| App tests | `go test .` | exit 0 |
| Full local gate | `make test` | exit 0 after Plan 001 |
| Vet | `make vet` | exit 0 after Plan 001 |

## Scope

**In scope** (the only files you should modify):

- `app.go`
- `templates/app.html`
- `app_test.go`

**Out of scope** (do NOT touch):

- Mapping form target/template fields.
- Form ID submission.
- Fillout client behavior.
- Database schema.

## Git workflow

- Branch: `advisor/006-use-refetched-fillout-form-name`
- Commit message style: short imperative sentence, e.g. `Use refetched Fillout form name on sync start`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Use `meta.Name` in `handleStart`

In `app.go` `handleStart`, replace:

```go
formName := strings.TrimSpace(r.FormValue("form_name"))
```

with:

```go
formName := strings.TrimSpace(meta.Name)
```

Keep using `formName` everywhere else in the function. This ensures validation re-renders and audit details use the authoritative name.

If `meta.Name` is empty, existing `orDefault(formName, formID)` call sites already handle fallback display.

**Verify**: `go test .` → exits 0.

### Step 2: Remove the hidden `form_name` input

In `templates/app.html`, remove:

```html
<input type="hidden" name="form_name" value="{{.FormName}}">
```

Do not remove the hidden `form_id` input.

**Verify**: `go test .` → exits 0 and template parsing still succeeds.

### Step 3: Add a tamper regression test

In `app_test.go`, add or update a start-sync handler test:

1. Configure `fakeForms.meta.Name` to a known value, for example `Real Fillout Name`.
2. Submit a POST body to `/sync/start` that includes `form_name=Tampered+Name` plus the required fields (`form_id`, `ysws_program`, target fields, etc.).
3. Assert the created job's `FilloutFormName` is `Real Fillout Name`, not `Tampered Name`.
4. If the test uses DB-backed creation, follow the existing `dbBackedServer(t)` pattern and skip when `DATABASE_URL` is unset. If it can use an existing fake store path, keep it as a unit test.

Also update any existing test that expected the hidden `form_name` field to appear in the preview HTML.

**Verify**: `DATABASE_URL='<throwaway postgres URL>' go test . -run 'Test.*Start'` → exits 0 if the new test is DB-backed.

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

New test to write:

- `app_test.go`: hidden/tampered `form_name` is ignored and the stored job uses `fakeForms.meta.Name`.

Existing tests to update:

- Any preview HTML test that looks for `name="form_name"` should be changed to assert the form ID is still present and the visible form title remains correct.

## Done criteria

All must hold:

- [ ] `handleStart` derives `formName` from `meta.Name` after `GetForm`.
- [ ] The preview form no longer submits `form_name`.
- [ ] Regression test proves tampered `form_name` is ignored.
- [ ] `go test .` exits 0.
- [ ] `make vet && make test` exits 0 after Plan 001.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- Fillout metadata no longer includes a reliable `Name` field.
- The start-sync path has been refactored so the handler no longer re-fetches form metadata.
- Fixing tests appears to require weakening CSRF or auth behavior.

## Maintenance notes

Do not trust hidden browser fields for data that can be refetched server-side. Future preview fields should be treated as user input unless the server validates or recomputes them before persistence.
