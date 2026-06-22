Improtant rules to follow:

- If you're working on a worktree, pull the .env from the parent repo.

- The repo we are working in is public. Always triple check before pushing to make sure we have sufficient code quality, testing quality, and never push any secrets or internal information

- We use test-driven development. Always make sure we have sufficient tests for our code before pushing

## Dev environment (Docker)

The app runs in Docker with Go live-reload (Air) and a Postgres service. Ports
and the Compose project name are all configurable via `.env` so multiple dev
environments can run on one machine at once.

Quick start:

1. Create `.env` (copy `.env.example`, or pull from the parent repo per the rule
   above) and set the variables below.
2. `docker compose up --build` (or `make up`).
3. App: `http://localhost:${APP_PORT}` · Postgres: `localhost:${POSTGRES_PORT}`.

Edits to `.go` / template files trigger an automatic rebuild and restart.

Key `.env` variables:

- `COMPOSE_PROJECT_NAME` — prefixes containers/networks/volumes (isolates envs)
- `APP_PORT` — host port for the app (container always listens on 8080)
- `POSTGRES_PORT` — host port for Postgres (container always listens on 5432)
- `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` — db credentials

**If you're on a worktree, use a custom port.** Every worktree must pick its own
unique `APP_PORT`, `POSTGRES_PORT`, and `COMPOSE_PROJECT_NAME` so concurrent dev
environments don't fight over host ports, container names, networks, or volumes.

### OAuth login (Hack Club Auth) callback URLs

Sign-in (`Log in with Hack Club`) only works if the app's callback URL is
registered with the Hack Club Auth app. To let any worktree log in regardless of
its port, the whole range `http://localhost:3000/callback` …
`http://localhost:3050/callback` (ports **3000–3050**) is registered as an
authorized redirect URL. So:

- Pick your worktree's `APP_PORT` from **3000–3050** if you need working login.
- Set `HC_AUTH_CALLBACK_BASE_URL=http://localhost:${APP_PORT}` to match — the
  callback is `<base>/callback`, and it must equal the port the app is published
  on or Hack Club will reject the redirect.
- A port outside 3000–3050 still serves the UI (incl. the login page), but the
  OAuth redirect will fail until that exact `localhost:<port>/callback` is added
  to the auth app.

Handy `make` targets: `up`, `down`, `logs`, `ps`, `restart`, `test`, `psql`.

## Production deployment (Coolify)

Production uses the root **`Dockerfile`** (not `docker-compose.yml` /
`Dockerfile.dev`, which are dev-only). It's a multi-stage build that produces a
~22 MB Alpine image holding a single static Go binary. HTML templates and SQL
migrations are compiled into the binary via `//go:embed`, so the image carries
no extra files — and migrations run automatically on startup.

The container needs an **external Postgres**; the Dockerfile bundles only the
app. In Coolify, create a Postgres database resource and point the app at it.

Build/run it locally exactly as Coolify will:

```bash
docker build -t fillout-ysws-nps .
docker run --rm -p 8080:8080 --env-file .env fillout-ysws-nps
```

Coolify setup:

1. New Resource → your Git repo → Build Pack: **Dockerfile** (Coolify
   auto-detects the root `Dockerfile`).
2. Add a **PostgreSQL** database resource; copy its connection string into the
   app's `DATABASE_URL`.
3. Set the environment variables below (same ones the app validates at startup;
   missing any aborts boot with a clear message).
4. The app listens on `8080` inside the container — Coolify maps it. Health is
   reported via the Dockerfile `HEALTHCHECK`, which polls `/healthz`
   (unauthenticated, dependency-free).
5. Set `HC_AUTH_CALLBACK_BASE_URL` to the app's public URL (e.g.
   `https://nps.example.com`) and register `<that URL>/callback` with the Hack
   Club Auth app, or login will fail.

Required environment variables (see `.env.example` for descriptions):

- `DATABASE_URL` — Postgres connection string from the Coolify DB resource
- `HC_AUTH_CLIENT_ID`, `HC_AUTH_CLIENT_SECRET`, `HC_AUTH_CALLBACK_BASE_URL`
- `FILLOUT_API_KEY`, `OPENAI_API_KEY`, `AIRTABLE_API_KEY`, `AIRTABLE_BASE_ID`
- `SESSION_SECRET` — `head -c 32 /dev/urandom | base64`
- `ALLOWED_EMAILS` — comma-separated login allow-list

Optional: `NPS_TABLE` (default `NPS`), `NPS_POLL_INTERVAL` (default `30s`),
`PORT` (default `8080`).
