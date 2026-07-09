# Plan 001: Establish complete verification gates

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- Makefile .github/workflows .github/workflows/ci.yml`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P1
- **Effort**: S
- **Risk**: LOW
- **Depends on**: none
- **Category**: tests / dx
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

This repository is split into three Go modules: the root app, `fillout`, and `airtable`. The current `make test` only runs the root module, so changes can pass the advertised local gate while breaking a submodule. There is also no checked-in CI workflow, and the Postgres-backed tests intentionally skip when `DATABASE_URL` is unset. A complete verification gate should run root and submodule tests, vet all modules, and run DB integration tests in CI against a disposable Postgres service.

## Current state

Relevant files:

- `Makefile` — developer entrypoint for Docker and local tests.
- `go.mod` — root Go module; imports local submodules through `replace`.
- `fillout/go.mod` — separate Fillout client module with its own tests.
- `airtable/go.mod` — separate Airtable client module with its own tests.
- `internal/db/db_test.go` — DB integration tests skip unless `DATABASE_URL` is set.
- `.github/workflows/ci.yml` — does not exist yet.

Excerpts:

```makefile
# Makefile:1-3
.PHONY: help up down logs ps restart test psql

# Makefile:22-23
test: ## Run the Go test suite on the host
	go test ./...
```

```go
// go.mod:1-7
module github.com/hackclub/fillout-ysws-nps

go 1.26.4

replace github.com/hackclub/fillout-ysws-nps/airtable => ./airtable
replace github.com/hackclub/fillout-ysws-nps/fillout => ./fillout
```

```go
// fillout/go.mod:1-5
module github.com/hackclub/fillout-ysws-nps/fillout

go 1.26.4

require golang.org/x/time v0.15.0
```

```go
// airtable/go.mod:1-3
module github.com/hackclub/fillout-ysws-nps/airtable

go 1.26
```

```go
// internal/db/db_test.go:12-21
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
```

Repo conventions to match:

- Make targets have `##` help text and are listed in `.PHONY`.
- Commit messages in recent history are short imperative sentences, e.g. `Show last poll times in the viewer's local timezone` and `Harden web security controls`.
- The public repo must not commit `.env` or real secrets; use only placeholder env values in CI.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Root tests | `go test ./...` | exit 0; root module packages pass |
| Fillout tests | `(cd fillout && go test ./...)` | exit 0 |
| Airtable tests | `(cd airtable && go test ./...)` | exit 0 |
| Root vet | `go vet ./...` | exit 0, no output |
| Fillout vet | `(cd fillout && go vet ./...)` | exit 0, no output |
| Airtable vet | `(cd airtable && go vet ./...)` | exit 0, no output |
| New local gate | `make test` | exit 0; runs all three module test suites |
| New vet gate | `make vet` | exit 0; vets all three modules |

## Scope

**In scope** (the only files you should modify):

- `Makefile`
- `.github/workflows/ci.yml` (create)

**Out of scope** (do NOT touch):

- Application source code.
- Test source code.
- `go.mod` / `go.sum` files, unless a verification command explicitly reports they are stale.
- Real environment files or credentials.
- README updates; those are handled by `plans/008-expand-public-readme.md`.

## Git workflow

- Branch: `advisor/001-establish-complete-verification-gates`
- Commit message style: short imperative sentence, e.g. `Run all Go module tests in CI`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Expand Makefile targets to cover every module

Update `Makefile` so:

1. `.PHONY` includes `vet` (and any helper targets you add).
2. `test` runs these exact commands in this order:
   - `go test ./...`
   - `cd fillout && go test ./...`
   - `cd airtable && go test ./...`
3. Add a `vet` target with help text that runs:
   - `go vet ./...`
   - `cd fillout && go vet ./...`
   - `cd airtable && go vet ./...`

Keep the same simple Makefile style; do not introduce scripts or a new task runner.

**Verify**: `make test` → exits 0 and shows test output for the root module plus the `fillout` and `airtable` modules.

**Verify**: `make vet` → exits 0 with no vet diagnostics.

### Step 2: Add CI with a disposable Postgres service

Create `.github/workflows/ci.yml` with one workflow that runs on `push` and `pull_request`. It should:

1. Check out the repository.
2. Set up Go `1.26.4` to match `go.mod`.
3. Start a Postgres service using the same major version as local dev (`postgres:17-alpine`).
4. Set `DATABASE_URL` for the root test step to the service, for example `postgres://app:app@localhost:5432/app?sslmode=disable`.
5. Run `make vet`.
6. Run `make test`.

Use placeholder CI database credentials only (`app`/`app` is fine). Do not reference real API keys. The existing external API integration tests skip unless their API-key env vars are set, so CI should not set them.

**Verify**: `git diff -- .github/workflows/ci.yml Makefile` → shows only the Makefile target changes and the new workflow.

**Verify**: `make vet && make test` → both exit 0 locally.

### Step 3: Confirm DB tests no longer silently disappear in CI

Run the root module tests once with no `DATABASE_URL`, and once with a disposable DB if one is available locally.

Minimum local verification:

- `env -u DATABASE_URL go test ./internal/db` → exits 0 with tests skipped because no DB is configured.

If Docker is available and the operator allows starting containers, optionally verify the same CI shape locally:

```bash
docker run --rm --name fillout-nps-ci-postgres \
  -e POSTGRES_USER=app -e POSTGRES_PASSWORD=app -e POSTGRES_DB=app \
  -p 55432:5432 -d postgres:17-alpine
# wait until pg_isready succeeds, then:
DATABASE_URL='postgres://app:app@localhost:55432/app?sslmode=disable' go test ./internal/db
```

Do not leave containers running.

**Verify**: `env -u DATABASE_URL go test ./internal/db` → exit 0; output may include `skipping db integration test`.

**Verify**: if you ran the Docker optional check, `DATABASE_URL='postgres://app:app@localhost:55432/app?sslmode=disable' go test ./internal/db` → exit 0 with DB tests executed, not skipped.

## Test plan

No application tests need to be added. This plan changes verification infrastructure. The new checks themselves are verified by running:

- `make vet`
- `make test`
- `env -u DATABASE_URL go test ./internal/db`
- Optional Docker-backed `go test ./internal/db` with `DATABASE_URL` set.

## Done criteria

All must hold:

- [ ] `Makefile` has a `test` target that runs root, `fillout`, and `airtable` tests.
- [ ] `Makefile` has a `vet` target that vets root, `fillout`, and `airtable`.
- [ ] `.github/workflows/ci.yml` exists and runs `make vet` and `make test` with a Postgres service and `DATABASE_URL` set for root tests.
- [ ] `make vet` exits 0 locally.
- [ ] `make test` exits 0 locally.
- [ ] No secrets or real `.env` values are committed.
- [ ] No files outside the in-scope list are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- The Makefile or module layout no longer matches the excerpts above.
- GitHub Actions cannot set up Go 1.26.4 in the runner environment; do not silently downgrade Go.
- `make test` fails in a submodule for reasons unrelated to your Makefile changes.
- Adding CI requires secrets or real external API keys.

## Maintenance notes

When adding any future Go submodule, update both `make test` and `make vet` in the same PR. Reviewers should ensure CI still runs DB-backed root tests rather than relying only on skip-on-missing-`DATABASE_URL` behavior.
