# Fillout → YSWS NPS sync

An authenticated YSWS tool for mapping Fillout feedback submissions into the Airtable NPS table. Users connect a Fillout form, review suggested field mappings, and the app keeps polling for new responses while deduplicating records.

## What it does

- Connects a Fillout form by URL or ID.
- Uses OpenAI structured output to suggest mappings from Fillout questions to NPS fields.
- Lets the user review/edit mappings, choose a YSWS program, and add feedback tags.
- Writes deduplicated NPS records to Airtable and keeps polling for new submissions.
- Provides a dashboard for sync jobs and an admin-only audit log.

## Prerequisites

- Go 1.26.4.
- Docker and Docker Compose for the local app/Postgres environment.
- Access to Fillout, Airtable, OpenAI, and Hack Club Auth credentials.
- A local `.env` copied from `.env.example`. Do not commit `.env` or real secrets.

## Local development

1. Copy `.env.example` to `.env` and fill in the required values.
2. If you are using multiple worktrees, choose unique `COMPOSE_PROJECT_NAME`, `APP_PORT`, and `POSTGRES_PORT` values so containers and ports do not collide.
3. Start the dev environment:

   ```bash
   make up
   # or: docker compose up --build
   ```

4. Open `http://localhost:${APP_PORT}`. Postgres is published on `localhost:${POSTGRES_PORT}`.

Useful Make targets:

```bash
make help
make up
make down
make logs
make ps
make restart
make test
make vet
make psql
```

## OAuth callback ports

Hack Club Auth requires the callback URL to be registered exactly. For local development, the registered localhost callback range is `http://localhost:3000/callback` through `http://localhost:3050/callback`.

- Pick `APP_PORT` from 3000–3050 when you need login to work locally.
- Set `HC_AUTH_CALLBACK_BASE_URL=http://localhost:${APP_PORT}`.
- The callback is `<base>/callback`, so the port in `HC_AUTH_CALLBACK_BASE_URL` must match the published app port.

## Testing and verification

Run the standard local gates before committing:

```bash
make vet
make test
```

`make test` runs the root module plus the `fillout` and `airtable` submodules. DB-backed tests run when `DATABASE_URL` points at a throwaway Postgres database; without it, those tests skip so the unit suite remains easy to run.

## Configuration

Required environment variables are documented in `.env.example` and validated on startup:

- `DATABASE_URL` — Postgres connection string.
- `HC_AUTH_CLIENT_ID`, `HC_AUTH_CLIENT_SECRET`, `HC_AUTH_CALLBACK_BASE_URL` — Hack Club Auth app settings.
- `FILLOUT_API_KEY`, `OPENAI_API_KEY`, `AIRTABLE_API_KEY`, `AIRTABLE_BASE_ID` — external service credentials.
- `SESSION_SECRET` — random secret for signed session cookies.
- `ALLOWED_EMAILS` — comma-separated bootstrap login allow-list.

Optional:

- `ADMIN_EMAILS` — comma-separated global sync admins; admins can manage every sync and view the audit log. Defaults to `ALLOWED_EMAILS` when unset.
- `NPS_TABLE` — Airtable table name, default `NPS`.
- `NPS_POLL_INTERVAL` — poll interval, default `30s`.
- `PORT` — HTTP port inside production containers, default `8080`.

## Production deployment

Production uses the root `Dockerfile`, not the development Compose file. The image builds a static Go binary with embedded templates, static assets, and SQL migrations; migrations run on startup. The container expects an external Postgres database.

Build and run locally the same way a Dockerfile-based host will:

```bash
docker build -t fillout-ysws-nps .
docker run --rm -p 8080:8080 --env-file .env fillout-ysws-nps
```

For Coolify or a similar platform:

1. Create the app from this repository using the root `Dockerfile`.
2. Provision a Postgres database and set `DATABASE_URL` for the app.
3. Set the required environment variables from `.env.example`.
4. Expose container port `8080`; `/healthz` is the unauthenticated health check.
5. Set `HC_AUTH_CALLBACK_BASE_URL` to the public app URL and register `<that URL>/callback` with Hack Club Auth.

## Repository layout

- `app.go` — HTTP routes, handlers, templates, static assets, and security headers.
- `internal/nps` — sync manager, mapping, record building, and Airtable dedup logic.
- `internal/db` — Postgres access and embedded migrations.
- `internal/auth` and `hcauth` — session auth and Hack Club Auth client code.
- `internal/config` — environment configuration loading and validation.
- `fillout` — Fillout API client submodule.
- `airtable` — Airtable API client submodule.
- `templates` — server-rendered HTML templates.
- `static` — embedded CSS and JavaScript.
- `Dockerfile` — production image build.
