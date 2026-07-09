# Plan 008: Expand the public README for contributors and operators

> **Executor instructions**: Follow this plan step by step. Run every
> verification command and confirm the expected result before moving to the
> next step. If anything in the "STOP conditions" section occurs, stop and
> report — do not improvise. When done, update the status row for this plan
> in `plans/README.md` — unless a reviewer dispatched you and told you they
> maintain the index.
>
> **Drift check (run first)**: `git diff --stat a767b82..HEAD -- README.md AGENTS.md .env.example Makefile`
> If any in-scope file changed since this plan was written, compare the
> "Current state" excerpts against the live code before proceeding; on a
> mismatch, treat it as a STOP condition.

## Status

- **Priority**: P3
- **Effort**: S
- **Risk**: LOW
- **Depends on**: `plans/001-establish-complete-verification-gates.md`, `plans/004-enforce-owner-or-admin-sync-management.md`
- **Category**: docs
- **Planned at**: commit `a767b82`, 2026-07-09

## Why this matters

The public README currently contains only a one-line product description. Setup, test, OAuth callback, and deployment instructions exist in `AGENTS.md`, but human contributors and operators usually start with `README.md`. The README should give enough public, non-secret guidance to run, test, and deploy the app without duplicating sensitive values.

## Current state

Relevant files:

- `README.md` — public project overview; currently one line.
- `AGENTS.md` — agent/developer instructions with Docker, OAuth, and Coolify details.
- `.env.example` — placeholder environment variables.
- `Makefile` — advertised local commands; Plan 001 expands test/vet gates.

Excerpts:

```md
<!-- README.md:1 -->
A small utility for the YSWS team to help them sync feedback from their Fillout forms to the Unified YSWS Projects DB!
```

```md
<!-- AGENTS.md:15-20 -->
Quick start:

1. Create `.env` (copy `.env.example`, or pull from the parent repo per the rule
   above) and set the variables below.
2. `docker compose up --build` (or `make up`).
3. App: `http://localhost:${APP_PORT}` · Postgres: `localhost:${POSTGRES_PORT}`.
```

```md
<!-- AGENTS.md:35-49 -->
### OAuth login (Hack Club Auth) callback URLs
...
- Pick your worktree's `APP_PORT` from **3000–3050** if you need working login.
- Set `HC_AUTH_CALLBACK_BASE_URL=http://localhost:${APP_PORT}` to match — the
  callback is `<base>/callback`, and it must equal the port the app is published
  on or Hack Club will reject the redirect.
```

```md
<!-- AGENTS.md:53-69 -->
## Production deployment (Coolify)
...
docker build -t fillout-ysws-nps .
docker run --rm -p 8080:8080 --env-file .env fillout-ysws-nps
```

```md
<!-- AGENTS.md:86-92 -->
Required environment variables (see `.env.example` for descriptions):

- `DATABASE_URL` — Postgres connection string from the Coolify DB resource
- `HC_AUTH_CLIENT_ID`, `HC_AUTH_CLIENT_SECRET`, `HC_AUTH_CALLBACK_BASE_URL`
- `FILLOUT_API_KEY`, `OPENAI_API_KEY`, `AIRTABLE_API_KEY`, `AIRTABLE_BASE_ID`
- `SESSION_SECRET` — `head -c 32 /dev/urandom | base64`
- `ALLOWED_EMAILS` — comma-separated login allow-list
```

Repo conventions to match:

- The repo is public. Do not put real secrets, internal URLs, or private operational details in README.
- Keep `AGENTS.md` as the detailed agent guidance; README should summarize and link to files where useful.
- After Plan 001, `make test` and `make vet` are the canonical local verification commands.
- After Plan 004, `ADMIN_EMAILS` is optional and controls global sync admins/audit access.

## Commands you will need

| Purpose | Command | Expected on success |
|---|---|---|
| Inspect docs | `rg -n "TODO|FIXME|sk-|secret" README.md` | no accidental placeholder secrets beyond generic wording |
| Root tests | `go test ./...` | exit 0 (docs-only change should not affect tests) |
| Full local gate | `make test` | exit 0 after Plan 001 |

## Scope

**In scope** (the only file you should modify):

- `README.md`

**Out of scope** (do NOT touch):

- `AGENTS.md`.
- `.env.example` (Plan 004 updates it for `ADMIN_EMAILS`).
- Application source code.
- Deployment configuration.
- Any real secret values, private domains, or internal database URLs.

## Git workflow

- Branch: `advisor/008-expand-public-readme`
- Commit message style: short imperative sentence, e.g. `Document setup and deployment in README`
- Do NOT push or open a PR unless the operator instructed it.

## Steps

### Step 1: Rewrite README with a useful public structure

Replace the one-line README with a structured document. Include these sections:

1. **Title and summary**
   - Name: `Fillout → YSWS NPS sync` or similar.
   - One paragraph describing the app: authenticated YSWS tool that maps Fillout submissions to the Airtable NPS table and keeps polling for new responses.

2. **What it does**
   - Connect a Fillout form.
   - Uses OpenAI structured output to suggest field mappings.
   - Lets a user review/edit mappings, choose a YSWS program, and tag feedback.
   - Writes deduplicated NPS records to Airtable and keeps polling.
   - Provides a dashboard and audit log.

3. **Prerequisites**
   - Go 1.26.4.
   - Docker/Docker Compose for local Postgres/dev server.
   - Access to Fillout, Airtable, OpenAI, and Hack Club Auth credentials.
   - Emphasize: do not commit `.env`; use `.env.example`.

4. **Local development**
   - Copy `.env.example` to `.env`.
   - For worktrees, use unique `COMPOSE_PROJECT_NAME`, `APP_PORT`, and `POSTGRES_PORT`.
   - Run `make up` or `docker compose up --build`.
   - App URL and Postgres URL shape.

5. **OAuth callback ports**
   - Summarize the 3000–3050 localhost callback range from `AGENTS.md`.
   - State `HC_AUTH_CALLBACK_BASE_URL` must match the app's host port.

6. **Testing and verification**
   - After Plan 001, document `make test` and `make vet`.
   - Mention DB-backed tests run when `DATABASE_URL` points at a throwaway Postgres.
   - Mention submodule tests are included by `make test`.

7. **Configuration**
   - List required env vars from `AGENTS.md` and `.env.example`.
   - Include `ADMIN_EMAILS` only if Plan 004 has landed; explain it is optional and defaults to `ALLOWED_EMAILS`.
   - Keep descriptions high-level; no values beyond placeholders.

8. **Production deployment**
   - Summarize Coolify/root `Dockerfile` flow.
   - Mention external Postgres, `DATABASE_URL`, port 8080, `/healthz`, and registered public callback URL.
   - Include the local production-like build/run commands from `AGENTS.md`.

9. **Repository layout**
   - Brief list: `app.go`, `internal/nps`, `internal/db`, `fillout`, `airtable`, `hcauth`, `templates`, `Dockerfile`.

Keep the README concise; do not paste all of `AGENTS.md` verbatim.

**Verify**: `rg -n "sk-|SESSION_SECRET=.*[A-Za-z0-9+/]{10,}|DATABASE_URL=postgres://[^<]" README.md` → no matches that look like real secrets or real connection strings. Generic variable names and placeholder examples are fine.

### Step 2: Align commands with the current Makefile

Check the current `Makefile` after Plan 001. Ensure the README's commands match exactly:

- `make up`
- `make down`
- `make logs`
- `make test`
- `make vet` (if Plan 001 landed)
- `make psql`

Do not document targets that do not exist.

**Verify**: `make help` → exits 0 and displays the targets README mentions.

### Step 3: Run a docs-safe verification pass

Run tests even though this is a docs-only change, to preserve repo convention.

**Verify**: `go test ./...` → exits 0.

If Plan 001 has landed:

**Verify**: `make test` → exits 0.

## Test plan

No new automated tests are required for a README-only change. Verification is:

- Secret-pattern scan over `README.md`.
- `make help` to ensure documented targets exist.
- `go test ./...` or `make test` to confirm no accidental source changes broke the app.

## Done criteria

All must hold:

- [ ] README has setup, test, configuration, OAuth callback, deployment, and layout sections.
- [ ] README mentions `ADMIN_EMAILS` only if Plan 004 has landed.
- [ ] README does not include real secret values, internal-only URLs, or private database strings.
- [ ] README commands match existing Makefile targets.
- [ ] `rg` secret-pattern check over README has no concerning matches.
- [ ] `go test ./...` exits 0.
- [ ] No files outside `README.md` are modified.
- [ ] `plans/README.md` status row updated.

## STOP conditions

Stop and report back if:

- `AGENTS.md`, `.env.example`, and the Makefile disagree about required env vars or commands after prior plans land.
- You find real credentials in any doc while preparing the README. Do not copy them; report only the file/line and credential type.
- Plan 004 has not landed and you cannot tell whether `ADMIN_EMAILS` exists. Omit `ADMIN_EMAILS` from README for now and note the dependency in `plans/README.md`.

## Maintenance notes

Keep detailed agent/worktree rules in `AGENTS.md`; README should stay public-facing and concise. Whenever required env vars or Make targets change, update `.env.example`, `AGENTS.md`, and `README.md` together.
