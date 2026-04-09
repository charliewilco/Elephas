# Repository Guidelines

## Project Structure & Module Organization
Elephas is a small Go monorepo with two entry points: the reusable library at the repository root and the HTTP server in `cmd/elephas`. Most implementation code lives under `internal/`:

- `internal/httpapi` for routing and handlers
- `internal/config` for environment-driven config
- `internal/store/{sqlite,postgres,age,sqlstore}` for persistence
- `internal/cache/redis` for optional caching
- `internal/extractor/openai` for ingest extraction
- `internal/migrate` for embedded SQL migrations

Tests are colocated with implementation and use `*_test.go` naming, for example `internal/httpapi/router_test.go`.

## Build, Test, and Development Commands
Use `just` for the standard workflow:

- `just run` starts `cmd/elephas` locally
- `just build` compiles all packages with `go build ./...`
- `just test` runs the full test suite with `go test ./...`
- `just vet` runs `go vet ./...`
- `just fmt` rewrites Go files with `gofmt -w .`
- `just check` runs format check, build, test, vet, and lint
- `just ci` adds `go mod tidy` before `just check`

`just lint` expects `staticcheck` at `$(go env GOPATH)/bin/staticcheck`, matching CI.

## Coding Style & Naming Conventions
Follow idiomatic Go and let `gofmt` own formatting; do not hand-align whitespace. Keep packages small and focused, use lowercase package names, and prefer descriptive exported identifiers in `CamelCase`. Tests and helpers should stay near the code they exercise. When adding new entry points or adapters, mirror existing directory names and keep environment variable names in the `ELEPHAS_*` namespace.

## Testing Guidelines
Run `just test` before opening a PR and `just check` before merging. Prefer table-driven tests where behavior branches by backend or request shape. Keep fast unit tests default-friendly with SQLite or in-memory fixtures; backend-specific tests should skip cleanly when required env vars are missing, as in the AGE tests.

## Commit & Pull Request Guidelines
Recent history mixes conventional prefixes and short imperative subjects: `feat: ...`, `fix: ...`, `docs: ...`, `test: ...`, `chore: ...`, plus occasional plain imperative commits. Keep subject lines short, present tense, and scoped to one change. PRs should explain behavior changes, mention config or schema impacts, link the issue when applicable, and include example requests or responses for API-facing changes.

## Security & Configuration Tips
Local development is simplest with SQLite and a `.env` file. Production-oriented changes should call out impact on Postgres/AGE, Redis, or extractor settings such as `ELEPHAS_EXTRACTOR_ENDPOINT` and `ELEPHAS_API_KEY`.
