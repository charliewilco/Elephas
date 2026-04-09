# Contributing

## Local workflow

Elephas is configured with environment variables plus an optional local `.env` file.
There is no separate repo-specific config file format today.

Recommended local setup:

```bash
cp .env.example .env
just check
```

SQLite is the default and best-supported local backend.

## Standard checks

Run these before sending changes:

```bash
just check
go test -race ./...
```

Useful extra diagnostics:

```bash
go test ./... -coverprofile=coverage.out
go tool cover -func=coverage.out
```

## Backend support tiers

- SQLite: strongest coverage and recommended for local development
- Postgres: supported production backend, but requires DSN-backed tests for higher confidence
- AGE: implemented, but currently lighter automated validation than SQLite and Postgres

## Optional integration test setup

### Postgres

Set `ELEPHAS_TEST_POSTGRES_DSN` to enable the Postgres backend smoke tests.

Example:

```bash
export ELEPHAS_TEST_POSTGRES_DSN='postgres://localhost:5432/elephas?sslmode=disable'
go test ./internal/store/postgres
```

### Apache AGE

Set `ELEPHAS_TEST_AGE_DSN` to enable the AGE backend smoke tests.
The target database must have the AGE extension installed and usable by the test role.

Example:

```bash
export ELEPHAS_TEST_AGE_DSN='postgres://localhost:5432/elephas?sslmode=disable'
go test ./internal/store/age
```

### Redis

If `redis-server` is available on `PATH`, the Redis cache tests will start an ephemeral local
server automatically.

Example:

```bash
go test ./internal/cache/redis
```

## Operational notes

- Migrations run automatically on service startup.
- Readiness depends on both store reachability and migrations being current.
- Redis is a best-effort cache. Cache failures log warnings and fall back to the store.
- The ingest audit record is written after the main ingest transaction commits. Audit failures
  log warnings and do not roll back already-committed memory/entity/relationship writes.
