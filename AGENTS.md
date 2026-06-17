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

Handy `make` targets: `up`, `down`, `logs`, `ps`, `restart`, `test`, `psql`.
