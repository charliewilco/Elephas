# Elephas

Elephas is a self-hostable memory store for AI applications. It stores structured facts as entities, memories, and relationships, exposes them over HTTP, and includes an optional ingest pipeline that can extract candidate memories from raw text using an OpenAI-compatible endpoint.

This repository ships both:

- A Go library centered on the `elephas.Service`
- A standalone HTTP service at `cmd/elephas`

## What It Does

Elephas models durable memory as a graph:

- `entities` are subjects such as people, organizations, places, or agents
- `memories` are atomic facts attached to an entity
- `relationships` connect entities with typed edges such as `works_at`
- `ingest` accepts raw text, calls a configured extractor, and resolves candidate facts against existing data

Current API capabilities include:

- CRUD for memories, entities, and relationships
- Memory search
- Entity context retrieval
- Graph path lookup
- Health, readiness, and stats endpoints
- Optional request authentication with a bearer token
- Optional Redis caching for entity context

## Architecture

The codebase is organized around a small service layer and swappable storage backends:

- `cmd/elephas` - HTTP server bootstrap
- `internal/httpapi` - REST router and request handling
- `internal/config` - environment-based configuration
- `internal/store/postgres` - Postgres backend
- `internal/store/sqlite` - SQLite backend
- `internal/store/age` - Apache AGE backend
- `internal/extractor/openai` - OpenAI-compatible extraction client
- `internal/cache/redis` - Redis-backed context cache

## Requirements

- Go `1.26`
- One of:
  - SQLite for local/dev usage
  - Postgres for the default production backend
  - Postgres with Apache AGE for graph queries via AGE
- Optional Redis for context caching
- Optional OpenAI-compatible chat completion endpoint for ingest

## Quick Start

### 1. Configure local development

Elephas reads environment variables directly and also loads a local `.env` file if present.

For the fastest local setup, use SQLite:

```bash
cat > .env <<'EOF'
ELEPHAS_DB_BACKEND=sqlite
ELEPHAS_DB_DSN=file:elephas.db
ELEPHAS_HTTP_PORT=8080
EOF
```

### 2. Run the server

With `just`:

```bash
just run
```

Or with Go directly:

```bash
go run ./cmd/elephas
```

The server will start on `http://localhost:8080` by default.

### 3. Check health

```bash
curl http://localhost:8080/v1/health
curl http://localhost:8080/v1/ready
curl http://localhost:8080/v1/stats
```

## Common Configuration

### Database

- `ELEPHAS_DB_BACKEND` - `postgres`, `sqlite`, or `age`
- `ELEPHAS_DB_DSN` - required for `postgres` and `age`; defaults to `file:elephas.db` for `sqlite`
- `ELEPHAS_DB_MAX_CONNS`
- `ELEPHAS_DB_IDLE_CONNS`
- `ELEPHAS_DB_CONN_TIMEOUT_MS`

### HTTP server

- `ELEPHAS_HTTP_PORT`
- `ELEPHAS_HTTP_READ_TIMEOUT_MS`
- `ELEPHAS_HTTP_WRITE_TIMEOUT_MS`
- `ELEPHAS_API_KEY` - optional bearer token required on every request when set

### Extractor

- `ELEPHAS_EXTRACTOR_ENDPOINT` - OpenAI-compatible chat completions endpoint
- `ELEPHAS_EXTRACTOR_API_KEY`
- `ELEPHAS_EXTRACTOR_MODEL`
- `ELEPHAS_EXTRACTOR_TIMEOUT_MS`
- `ELEPHAS_EXTRACTOR_MAX_CANDIDATES`

### Search and conflict resolution

- `ELEPHAS_SEARCH_DEFAULT_LIMIT`
- `ELEPHAS_SEARCH_MAX_LIMIT`
- `ELEPHAS_RESOLVE_THRESHOLD`

### Cache

- `ELEPHAS_CACHE_DSN` - optional Redis DSN
- `ELEPHAS_CACHE_TTL_SECONDS`

## Development Commands

The repository includes a `Justfile`:

```bash
just fmt
just fmt-check
just tidy
just build
just test
just vet
just lint
just check
just ci
just run
```

Equivalent core Go commands:

```bash
go build ./...
go test ./...
go vet ./...
gofmt -w .
```

`just lint` expects `staticcheck` at `$(go env GOPATH)/bin/staticcheck`.

## API Overview

Base path: `/v1`

### Admin

- `GET /health`
- `GET /ready`
- `GET /stats`

### Memories

- `POST /memories`
- `GET /memories/{id}`
- `PATCH /memories/{id}`
- `DELETE /memories/{id}`
- `GET /memories`
- `POST /memories/search`

### Entities

- `POST /entities`
- `GET /entities/{id}`
- `PATCH /entities/{id}`
- `DELETE /entities/{id}`
- `GET /entities`
- `GET /entities/{id}/context`

### Relationships

- `POST /relationships`
- `GET /relationships/{id}`
- `DELETE /relationships/{id}`
- `GET /relationships`

### Ingest and graph

- `POST /ingest`
- `GET /ingest/{id}`
- `POST /graph/path`

If `ELEPHAS_API_KEY` is set, include:

```bash
Authorization: Bearer <token>
```

## Example Requests

Create an entity:

```bash
curl -X POST http://localhost:8080/v1/entities \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Charlie",
    "type": "person",
    "external_id": "user-123",
    "metadata": {
      "team": "core"
    }
  }'
```

Create a memory:

```bash
curl -X POST http://localhost:8080/v1/memories \
  -H 'Content-Type: application/json' \
  -d '{
    "entity_id": "REPLACE_WITH_ENTITY_ID",
    "content": "Prefers dark mode across all applications.",
    "category": "preference",
    "confidence": 0.9
  }'
```

Search memories:

```bash
curl -X POST http://localhost:8080/v1/memories/search \
  -H 'Content-Type: application/json' \
  -d '{
    "q": "dark mode",
    "include_entity_context": true,
    "limit": 10
  }'
```

Run ingest as a dry run:

```bash
curl -X POST http://localhost:8080/v1/ingest \
  -H 'Content-Type: application/json' \
  -d '{
    "raw_text": "Charlie works at Weave and prefers dark mode.",
    "subject_external_id": "user-123",
    "dry_run": true
  }'
```

Find a path between two entities:

```bash
curl -X POST http://localhost:8080/v1/graph/path \
  -H 'Content-Type: application/json' \
  -d '{
    "from_entity_id": "REPLACE_WITH_SOURCE_ENTITY_ID",
    "to_entity_id": "REPLACE_WITH_TARGET_ENTITY_ID",
    "max_depth": 3
  }'
```

## Notes

- Ingest requires a configured extractor. Without `ELEPHAS_EXTRACTOR_ENDPOINT` and `ELEPHAS_EXTRACTOR_API_KEY`, `POST /v1/ingest` will return an extractor-unavailable error.
- Migrations run automatically on startup.
- SQLite is the easiest backend for local development; Postgres or AGE are better fits for deployed environments.

## Status

The repository includes a draft product spec in [`SPEC.md`](./SPEC.md). The implementation already covers the core service, HTTP API, ingest flow, migrations, and multiple storage backends, but the spec is broader than a minimal getting-started guide, so the README focuses on what is implemented in this codebase today.
