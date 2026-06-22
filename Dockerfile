# syntax=docker/dockerfile:1
#
# Production image for Coolify (or any Docker host). Single self-contained
# binary — no Docker Compose. The app needs an external Postgres reachable via
# DATABASE_URL; provision that as a separate Coolify database resource.
#
# Build/run locally to mirror Coolify:
#   docker build -t fillout-ysws-nps .
#   docker run --rm -p 8080:8080 --env-file .env fillout-ysws-nps

# ── Build stage ────────────────────────────────────────────────────────────
# Pinned to the Go toolchain version in go.mod for reproducible builds.
FROM golang:1.26.4-alpine AS build

WORKDIR /src

# Warm the module cache first so dependency downloads are cached across
# source-only rebuilds. The root module uses local `replace` directives for the
# fillout and airtable sub-modules, so their go.mod/go.sum files must be present
# for `go mod download` to resolve the full module graph. (airtable has no
# external deps, hence no go.sum.)
COPY go.mod go.sum ./
COPY fillout/go.mod fillout/go.sum ./fillout/
COPY airtable/go.mod ./airtable/
RUN go mod download

# Copy the source and build a fully static binary. HTML templates and SQL
# migrations are compiled in via //go:embed, so the runtime image needs nothing
# beyond the binary and CA certificates.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/app .

# ── Runtime stage ──────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates: outbound HTTPS to Airtable, Fillout, OpenAI, Hack Club Auth.
# tzdata: correct timezone handling for timestamps.
# wget (busybox, already in alpine): backs the HEALTHCHECK below.
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -u 10001 app

COPY --from=build /out/app /usr/local/bin/app

USER app
WORKDIR /home/app

# The server binds 0.0.0.0:$PORT (default 8080). Coolify maps this port.
ENV PORT=8080
EXPOSE 8080

# Coolify surfaces container health from this directive. /healthz is
# unauthenticated and has no dependencies, so it reflects process liveness.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT:-8080}/healthz" >/dev/null 2>&1 || exit 1

ENTRYPOINT ["/usr/local/bin/app"]
