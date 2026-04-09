# Elephas Memory Store — Specification

Status: Draft v1 (language-agnostic)

Purpose: Define a self-hostable, graph-backed memory store for AI-powered applications that
creates, retrieves, updates, and deletes structured facts extracted from conversational context.

---

## 1. Problem Statement

Elephas is a persistent memory service that accepts raw conversational or documentary text,
extracts discrete facts from it, stores those facts as a typed graph, and surfaces relevant
memories back into AI context on demand.

The service solves four problems common to AI-powered applications:

- It provides a durable, inspectable memory layer that outlives individual conversation sessions.
- It models knowledge as a graph so that relationships between entities are first-class, not
  an afterthought bolted onto a flat row store.
- It supports proper update semantics: memories are refined in place, not deleted and recreated.
- It separates the storage contract from the extraction contract, so the caller can bring any
  LLM or extraction strategy without changing the persistence layer.

Important boundary:

- Elephas is a memory store and graph query service.
- Extraction — the act of turning raw text into structured facts — is the caller's responsibility
  or delegated to Elephas' optional ingest pipeline. Either way, extraction is not bundled into
  the storage engine.
- Elephas does not manage conversation history. It manages the durable facts that survive
  conversations.

---

## 2. Goals and Non-Goals

### 2.1 Goals

- Store memories as typed nodes in a graph with first-class relationship edges.
- Accept raw text via an ingest endpoint and return structured memories through an extraction
  pipeline.
- Support create, read, update, and delete on individual memories with ingest provenance when
  audit recording succeeds.
- Surface relevant memories given a query, using keyword and optional semantic search.
- Provide a `Store` interface so the graph backend is swappable: Postgres (adjacency list) ships
  by default; Apache AGE (openCypher) is a supported alternative.
- Support SQLite as a dev/embedded backend behind the same interface.
- Resolve conflicts at ingest time: new facts that contradict existing memories update the
  canonical memory rather than creating a duplicate.
- Run as a single self-hostable service with one database dependency.
- Be usable as a library or as a standalone HTTP service.

### 2.2 Non-Goals

- Bundled LLM for extraction. Elephas calls a configurable external endpoint.
- Vector store or embedding index in v1. Full-text and graph traversal are sufficient.
  pgvector integration is a post-v1 extension point.
- Multi-tenancy in the open source core.
- Real-time streaming or push notification of memory events.
- Conversation history storage or replay.
- Managed hosting, dashboards, or billing infrastructure.

---

## 3. System Overview

### 3.1 Main Components

1. `API Layer`
   - Exposes HTTP REST endpoints for all memory, entity, and relationship operations.
   - Validates request payloads and maps domain errors to HTTP status codes.
   - Handles authentication when configured.

2. `Ingest Pipeline`
   - Accepts raw text and a subject context.
   - Calls the configured extractor to produce candidate memories.
   - Runs conflict resolution and deduplication against the existing graph.
   - Commits the resulting creates, updates, and no-ops as a single transaction.

3. `Graph Engine`
   - Implements the `Store` interface against a configured backend.
   - Postgres backend: models the graph with an adjacency list using `nodes` and `edges` tables.
   - AGE backend: delegates graph traversal to openCypher queries via the Apache AGE extension.
   - SQLite backend: mirrors the Postgres adjacency list schema for local development.

4. `Extractor`
   - Called by the ingest pipeline.
   - Sends raw text to a configured LLM endpoint and parses the response into candidate memories.
   - Stateless. Implements the `Extractor` interface so it is swappable.

5. `Conflict Resolver`
   - Compares candidate memories from the extractor against existing graph state.
   - Produces a resolution plan: `create`, `update`, `merge`, or `no_op` per candidate.
   - Does not write to storage. Returns the plan to the ingest pipeline for execution.

6. `Search`
   - Accepts a query string and optional filters.
   - Performs full-text search against memory content.
   - Optionally traverses the graph to return related entities and their memories alongside
     direct matches.

7. `Config Layer`
   - Reads configuration from environment variables and an optional local `.env` file.
   - Exposes typed config values consumed by all other components.
   - Validates required values at startup.

### 3.2 Abstraction Levels

Elephas is easiest to port and test when kept in these layers:

1. `Transport Layer` (HTTP handlers)
   - Request parsing, response serialization, authentication middleware.
   - No business logic. Calls domain layer only.

2. `Domain Layer` (memories, entities, relationships, ingest)
   - Business rules for create, update, conflict resolution, and search.
   - Depends on the `Store` and `Extractor` interfaces, not concrete implementations.

3. `Store Layer` (graph backend)
   - Implements `Store` for Postgres, AGE, and SQLite.
   - No business logic. Pure persistence.

4. `Extraction Layer` (LLM integration)
   - Implements `Extractor` for the configured LLM provider.
   - Stateless. Input is raw text, output is a slice of candidate memories.

5. `Config Layer`
   - Shared across all other layers.
   - Resolved once at startup.

### 3.3 External Dependencies

- Postgres 14+ (primary storage for Postgres and AGE backends)
- Apache AGE extension (optional, for AGE backend only)
- SQLite (optional, for embedded/dev backend only)
- An LLM HTTP endpoint for extraction (configurable; any OpenAI-compatible endpoint works)
- Redis (optional, for caching hot context retrievals)

---

## 4. Core Domain Model

### 4.1 Entities

#### 4.1.1 Memory

A Memory is a discrete, structured fact about a subject. It is not a raw conversation turn.
Memories are first-class nodes in the graph.

Fields:

- `id` (uuid)
  - Stable identifier assigned at creation. Immutable.
- `entity_id` (uuid)
  - The entity this memory is primarily about. Required.
- `content` (string)
  - The fact expressed in plain language.
  - Example: `"Prefers dark mode across all applications."`
- `category` (enum)
  - One of: `preference`, `fact`, `relationship`, `event`, `instruction`
  - Used for filtering and for conflict resolution heuristics.
- `confidence` (float, 0.0–1.0)
  - Caller-supplied or extractor-supplied confidence score.
  - Default: `1.0`
- `source_id` (uuid, nullable)
  - References the ingest source that produced this memory, if created via ingest.
- `created_at` (timestamp, UTC)
- `updated_at` (timestamp, UTC)
  - Updated whenever `content`, `confidence`, or `metadata` changes.
- `expires_at` (timestamp, UTC, nullable)
  - When set, the memory is treated as expired after this time.
  - Expired memories are excluded from search by default but are not deleted.
- `metadata` (jsonb)
  - Caller-defined key-value pairs. No schema enforced by Elephas.
  - Example: `{"source_app": "elephas-demo", "language": "en"}`

#### 4.1.2 Entity

An Entity is a subject that memories can be about. Entities are nodes in the graph.

Fields:

- `id` (uuid)
- `name` (string)
  - Human-readable identifier for this entity.
  - Example: `"Charlie"`, `"Weave"`, `"Deep Dish Swift"`
- `type` (enum)
  - One of: `person`, `organization`, `place`, `concept`, `object`, `agent`
- `external_id` (string, nullable)
  - Caller-supplied stable ID for this entity (e.g. a user ID from another system).
  - When provided, can be used in place of `entity_id` in API requests.
- `created_at` (timestamp, UTC)
- `updated_at` (timestamp, UTC)
- `metadata` (jsonb)

#### 4.1.3 Relationship

A Relationship is a typed, directed edge between two entities. Relationships are edges in the
graph.

Fields:

- `id` (uuid)
- `from_entity_id` (uuid)
- `to_entity_id` (uuid)
- `type` (string)
  - Caller-defined relationship label.
  - Examples: `"works_at"`, `"knows"`, `"manages"`, `"owns"`, `"reported_by"`
  - Convention: lowercase snake_case.
- `weight` (float, nullable)
  - Optional strength or relevance score for this relationship edge.
- `created_at` (timestamp, UTC)
- `metadata` (jsonb)

#### 4.1.4 Ingest Source

An Ingest Source represents one raw-text submission to the ingest pipeline. It is an audit record.

Fields:

- `id` (uuid)
- `raw_text` (text)
  - The original text submitted.
- `subject_entity_id` (uuid, nullable)
  - The entity the raw text is primarily about, if provided by the caller.
- `extractor_model` (string)
  - The model or extractor identifier used for this ingest run.
- `resolution_plan` (jsonb)
  - The serialized output of the conflict resolver for this ingest run.
  - Stored for audit and replay purposes.
- `created_at` (timestamp, UTC)

#### 4.1.5 Resolution Plan

The output of conflict resolution for a single ingest run. Not persisted as its own row; stored
inside `IngestSource.resolution_plan`.

Structure (per candidate memory in the plan):

- `action` (enum)
  - One of: `create`, `update`, `merge`, `no_op`
- `candidate` (partial Memory)
  - The memory as extracted, before resolution.
- `target_id` (uuid, nullable)
  - The existing memory ID being updated or merged, if applicable.
- `reason` (string)
  - Human-readable explanation of why this action was chosen.
  - Example: `"Content supersedes existing preference memory for this entity and category."`

### 4.2 Graph Model

Elephas models knowledge as a labeled property graph:

- Entities are nodes.
- Relationships are directed edges between nodes.
- Memories are also nodes, connected to their entity by a `HAS_MEMORY` edge.

This means a single entity's full memory context can be retrieved by traversing its `HAS_MEMORY`
edges. Cross-entity facts — e.g. "Charlie works at Weave" — are modeled both as a
`Relationship` edge between the two entity nodes and optionally as a `relationship`-category
`Memory` on the Charlie entity.

### 4.3 Backend Representations

#### 4.3.1 Postgres Adjacency List (Default)

The graph is stored in four tables:

```
entities        (id, name, type, external_id, created_at, updated_at, metadata)
memories        (id, entity_id, content, category, confidence, source_id,
                 created_at, updated_at, expires_at, metadata)
relationships   (id, from_entity_id, to_entity_id, type, weight, created_at, metadata)
ingest_sources  (id, raw_text, subject_entity_id, extractor_model, resolution_plan, created_at)
```

Graph traversal is implemented as recursive CTEs or multi-hop joins within the domain layer.
Full-text search uses `tsvector` columns on `memories.content` with a GIN index.

#### 4.3.2 Apache AGE (openCypher Backend)

When the AGE extension is installed, the graph is stored as AGE vertex and edge types. The same
four logical entities map to:

- `Entity` → AGE vertex label `Entity`
- `Memory` → AGE vertex label `Memory`
- `Relationship` → AGE edge label matching `Relationship.type`
- `HAS_MEMORY` → AGE edge label `HAS_MEMORY` between Entity and Memory vertices

Traversal queries use openCypher via `ag_catalog.cypher()`. The domain layer sends the same
`Store` interface calls; the AGE implementation translates them into openCypher.

#### 4.3.3 SQLite (Embedded Backend)

Mirrors the Postgres adjacency list schema. Uses `fts5` virtual tables for full-text search.
Intended for development, local tooling, and single-user deployments only.

---

## 5. Store Interface

The `Store` interface is the only abstraction between the domain layer and any graph backend.
All backends must implement it completely. No backend-specific types or query languages leak
into the domain layer.

### 5.1 Memory Operations

```
CreateMemory(ctx, memory) → (Memory, error)
GetMemory(ctx, id) → (Memory, error)
UpdateMemory(ctx, id, patch) → (Memory, error)
DeleteMemory(ctx, id) → error
ListMemories(ctx, filter) → ([]Memory, error)
```

`UpdateMemory` accepts a partial patch. Only fields present in the patch are applied. This is
how Elephas guarantees proper update semantics: callers never need to delete and recreate.

`ListMemories` filter fields:

- `entity_id` (uuid, optional)
- `category` (enum, optional)
- `include_expired` (bool, default false)
- `since` (timestamp, optional — return memories updated after this time)
- `limit` (integer, default 50, max 500)
- `offset` (integer, default 0)

### 5.2 Entity Operations

```
CreateEntity(ctx, entity) → (Entity, error)
GetEntity(ctx, id) → (Entity, error)
GetEntityByExternalID(ctx, external_id) → (Entity, error)
UpdateEntity(ctx, id, patch) → (Entity, error)
DeleteEntity(ctx, id) → error    // cascades to memories and relationships
ListEntities(ctx, filter) → ([]Entity, error)
```

### 5.3 Relationship Operations

```
CreateRelationship(ctx, relationship) → (Relationship, error)
GetRelationship(ctx, id) → (Relationship, error)
DeleteRelationship(ctx, id) → error
ListRelationships(ctx, filter) → ([]Relationship, error)
```

`ListRelationships` filter fields:

- `from_entity_id` (uuid, optional)
- `to_entity_id` (uuid, optional)
- `type` (string, optional)

### 5.4 Graph Traversal Operations

```
GetEntityContext(ctx, entity_id, depth) → (EntityContext, error)
FindPath(ctx, from_entity_id, to_entity_id, max_depth) → ([]PathNode, error)
```

`GetEntityContext` returns the entity, all its memories, and all its direct relationships with
their related entities, up to `depth` hops. Depth 1 returns the entity and its immediate
neighbors. Depth 0 returns the entity and its memories only.

`EntityContext`:

- `entity` (Entity)
- `memories` ([]Memory)
- `relationships` ([]ResolvedRelationship)
  - Each `ResolvedRelationship` includes the `Relationship` and the resolved remote entity.

### 5.5 Search Operations

```
SearchMemories(ctx, query) → ([]MemorySearchResult, error)
```

`SearchMemories` query fields:

- `q` (string) — full-text query
- `entity_id` (uuid, optional) — scope to a single entity
- `categories` ([]enum, optional)
- `include_expired` (bool, default false)
- `include_entity_context` (bool, default false)
  - When true, each result includes the owning entity and its direct relationships.
- `limit` (integer, default 20, max 100)

`MemorySearchResult`:

- `memory` (Memory)
- `score` (float) — relevance score from the full-text engine
- `entity` (Entity, nullable) — populated when `include_entity_context` is true
- `relationships` ([]ResolvedRelationship, nullable)

### 5.6 Ingest Source Operations

```
CreateIngestSource(ctx, source) → (IngestSource, error)
GetIngestSource(ctx, id) → (IngestSource, error)
```

---

## 6. Ingest Pipeline

The ingest pipeline is the primary write path for memory creation. It accepts raw text and
produces a consistent, conflict-free set of memories.

### 6.1 Pipeline Stages

```
Raw Text
  → Extract   (LLM call: what facts are present in this text?)
  → Resolve   (compare candidates against existing graph state)
  → Commit    (execute the resolution plan as a transaction)
  → Audit     (persist the ingest source and resolution plan)
```

Each stage is discrete. The pipeline returns the committed memories and the final resolution plan.

### 6.2 Extract Stage

Input:

- `raw_text` (string)
- `subject_entity_id` (uuid, nullable) — hint to the extractor about who the text is about
- `extractor_config` (ExtractorConfig) — model, endpoint, system prompt override

The extractor sends a prompt to the configured LLM requesting that it identify discrete facts
in the raw text. The extractor prompt instructs the model to return a JSON array of candidate
memories, each with `content`, `category`, `confidence`, and optional `related_entity_name`.

The extractor does not write to storage. It returns `[]CandidateMemory`.

`CandidateMemory`:

- `content` (string)
- `category` (enum)
- `confidence` (float)
- `subject_entity_name` (string, nullable) — the entity this candidate is about, by name
- `related_entity_names` ([]string) — names of other entities mentioned in this fact

If the model returns malformed JSON or fails, the extract stage returns an error and the
pipeline aborts without writing anything.

### 6.3 Resolve Stage

Input:

- `[]CandidateMemory`
- `subject_entity_id`
- current graph state (fetched via `Store`)

For each candidate memory, the resolver:

1. Resolves the subject entity. If `subject_entity_name` is present in the candidate and
   differs from `subject_entity_id`, it attempts to match an existing entity by name. If no
   match is found, it queues an entity creation.
2. Loads existing memories for the subject entity with the same category.
3. Compares candidate `content` against existing memory content using normalized string
   similarity.
4. Assigns an action:
   - `create` — no sufficiently similar memory exists.
   - `update` — a similar memory exists but the candidate supersedes it (e.g. new preference
     overrides old preference). The existing memory's `content`, `confidence`, and `updated_at`
     will be updated.
   - `merge` — candidate and existing memory are semantically equivalent with no material
     difference. No write is needed.
   - `no_op` — candidate is redundant or lower confidence than the existing memory.
5. For `related_entity_names`, resolves or queues creation of related entities and queues
   `Relationship` creation if one does not already exist.

The resolver does not write to storage. It returns a `ResolutionPlan`.

### 6.4 Commit Stage

The commit stage executes the `ResolutionPlan` in a single database transaction:

- Creates new entities queued by the resolver.
- Creates new relationships queued by the resolver.
- Executes `create`, `update`, and `merge` actions on memories.
- `no_op` actions produce no writes.

If the transaction fails, the pipeline returns the error. No partial state is committed.

### 6.5 Audit Stage

After a successful commit, the pipeline attempts to write an `IngestSource` record containing:

- The original `raw_text`
- The `extractor_model` used
- The serialized `ResolutionPlan`

This is a best-effort write. If it fails, the pipeline logs a warning but does not roll back
the committed memories. In that case the ingest response may still include committed memories
while `ingest_source_id` remains absent.

### 6.6 Ingest Request and Response

Request:

- `raw_text` (string, required)
- `subject_entity_id` (uuid, optional)
- `subject_external_id` (string, optional — resolved to entity_id if entity exists)
- `extractor_model` (string, optional — overrides config default)
- `dry_run` (bool, default false)
  - When true, runs Extract and Resolve but does not Commit or Audit. Returns the resolution
    plan only.

Response:

- `ingest_source_id` (uuid, nullable — absent on dry_run)
- `memories_created` (integer)
- `memories_updated` (integer)
- `memories_merged` (integer)
- `memories_no_op` (integer)
- `entities_created` (integer)
- `relationships_created` (integer)
- `resolution_plan` (ResolutionPlan)
- `committed_memories` ([]Memory) — the final state of all created or updated memories

---

## 7. API Specification

All endpoints accept and return `application/json`. All timestamps are ISO-8601 UTC.
All IDs are UUIDs unless noted. Errors follow the envelope in Section 7.8.

### 7.1 Memories

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/memories` | Create a memory |
| `GET` | `/v1/memories/:id` | Get a memory |
| `PATCH` | `/v1/memories/:id` | Update a memory |
| `DELETE` | `/v1/memories/:id` | Delete a memory |
| `GET` | `/v1/memories` | List memories |
| `POST` | `/v1/memories/search` | Search memories |

`POST /v1/memories` body:

```json
{
  "entity_id": "uuid",
  "content": "string",
  "category": "preference | fact | relationship | event | instruction",
  "confidence": 0.0,
  "expires_at": "timestamp | null",
  "metadata": {}
}
```

`PATCH /v1/memories/:id` body (all fields optional):

```json
{
  "content": "string",
  "confidence": 0.0,
  "expires_at": "timestamp | null",
  "metadata": {}
}
```

`GET /v1/memories` query parameters:

- `entity_id` (uuid)
- `category` (string)
- `include_expired` (bool, default false)
- `since` (ISO-8601 timestamp)
- `limit` (integer, default 50)
- `offset` (integer, default 0)

`POST /v1/memories/search` body:

```json
{
  "q": "string",
  "entity_id": "uuid | null",
  "categories": ["preference"],
  "include_expired": false,
  "include_entity_context": false,
  "limit": 20
}
```

### 7.2 Entities

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/entities` | Create an entity |
| `GET` | `/v1/entities/:id` | Get entity |
| `PATCH` | `/v1/entities/:id` | Update entity |
| `DELETE` | `/v1/entities/:id` | Delete entity (cascades) |
| `GET` | `/v1/entities` | List entities |
| `GET` | `/v1/entities/:id/context` | Get entity + memories + relationships |

`GET /v1/entities/:id/context` query parameters:

- `depth` (integer, default 1, max 3)

Response shape for entity context:

```json
{
  "entity": {},
  "memories": [],
  "relationships": [
    {
      "relationship": {},
      "related_entity": {}
    }
  ]
}
```

### 7.3 Relationships

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/relationships` | Create a relationship |
| `GET` | `/v1/relationships/:id` | Get a relationship |
| `DELETE` | `/v1/relationships/:id` | Delete a relationship |
| `GET` | `/v1/relationships` | List relationships |

`GET /v1/relationships` query parameters:

- `from_entity_id` (uuid)
- `to_entity_id` (uuid)
- `type` (string)

### 7.4 Ingest

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/ingest` | Ingest raw text |
| `GET` | `/v1/ingest/:id` | Get ingest source record |

`POST /v1/ingest` body:

```json
{
  "raw_text": "string",
  "subject_entity_id": "uuid | null",
  "subject_external_id": "string | null",
  "extractor_model": "string | null",
  "dry_run": false
}
```

### 7.5 Graph

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/v1/graph/path` | Find path between two entities |

`POST /v1/graph/path` body:

```json
{
  "from_entity_id": "uuid",
  "to_entity_id": "uuid",
  "max_depth": 3
}
```

Response:

```json
{
  "path": [
    {
      "entity": {},
      "relationship": {}
    }
  ],
  "found": true
}
```

### 7.6 Admin

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/v1/health` | Liveness check |
| `GET` | `/v1/ready` | Readiness check (validates DB connection) |
| `GET` | `/v1/stats` | Runtime statistics |

`GET /v1/stats` response:

```json
{
  "entity_count": 0,
  "memory_count": 0,
  "relationship_count": 0,
  "ingest_source_count": 0,
  "backend": "postgres | age | sqlite"
}
```

### 7.7 Pagination

All list endpoints return a pagination envelope:

```json
{
  "data": [],
  "total": 0,
  "limit": 50,
  "offset": 0,
  "has_more": false
}
```

### 7.8 Error Envelope

All errors return a consistent envelope:

```json
{
  "error": {
    "code": "string",
    "message": "string",
    "details": {}
  }
}
```

Error codes map to HTTP status codes as follows:

| Code | HTTP Status |
|------|-------------|
| `not_found` | 404 |
| `invalid_request` | 400 |
| `conflict` | 409 |
| `extraction_failed` | 422 |
| `store_error` | 500 |
| `extractor_unavailable` | 503 |

---

## 8. Configuration

### 8.1 Source and Precedence

Configuration is read from environment variables and an optional local `.env` file.
Environment variables take precedence over `.env` file values. No separate Elephas config
file format is defined in v1.

### 8.2 Config Fields

#### Database

- `ELEPHAS_DB_BACKEND` (string)
  - One of: `postgres`, `age`, `sqlite`
  - Default: `postgres`
- `ELEPHAS_DB_DSN` (string)
  - Postgres or SQLite DSN.
  - Required for `postgres` and `age` backends.
  - For `sqlite`: may be a file path or `:memory:`.
  - May be a `$VAR_NAME` reference.
- `ELEPHAS_DB_MAX_CONNS` (integer)
  - Default: `25`
- `ELEPHAS_DB_IDLE_CONNS` (integer)
  - Default: `5`
- `ELEPHAS_DB_CONN_TIMEOUT_MS` (integer)
  - Default: `5000`

#### Server

- `ELEPHAS_HTTP_PORT` (integer)
  - Default: `8080`
- `ELEPHAS_HTTP_READ_TIMEOUT_MS` (integer)
  - Default: `30000`
- `ELEPHAS_HTTP_WRITE_TIMEOUT_MS` (integer)
  - Default: `30000`
- `ELEPHAS_API_KEY` (string, optional)
  - When set, all API requests must include `Authorization: Bearer <key>`.
  - When absent, authentication is disabled.

#### Extraction

- `ELEPHAS_EXTRACTOR_ENDPOINT` (string)
  - OpenAI-compatible chat completions endpoint.
  - Example: `https://api.openai.com/v1/chat/completions`
  - Required when using the ingest pipeline.
- `ELEPHAS_EXTRACTOR_API_KEY` (string)
  - Bearer token for the extractor endpoint.
  - May be a `$VAR_NAME` reference.
- `ELEPHAS_EXTRACTOR_MODEL` (string)
  - Default: `gpt-4o`
- `ELEPHAS_EXTRACTOR_TIMEOUT_MS` (integer)
  - Default: `30000`
- `ELEPHAS_EXTRACTOR_MAX_CANDIDATES` (integer)
  - Maximum number of candidate memories the extractor may return per ingest call.
  - Default: `50`

#### Search

- `ELEPHAS_SEARCH_DEFAULT_LIMIT` (integer)
  - Default: `20`
- `ELEPHAS_SEARCH_MAX_LIMIT` (integer)
  - Default: `100`

#### Cache (optional)

- `ELEPHAS_CACHE_DSN` (string, optional)
  - Redis DSN. When absent, caching is disabled.
- `ELEPHAS_CACHE_TTL_SECONDS` (integer)
  - Default: `300`

### 8.3 Startup Validation

At startup, Elephas validates:

- `ELEPHAS_DB_BACKEND` is a known value.
- `ELEPHAS_DB_DSN` is present for non-SQLite backends.
- Database connection is reachable and migrations are current.
- If `ELEPHAS_EXTRACTOR_ENDPOINT` is set, `ELEPHAS_EXTRACTOR_API_KEY` must also be set.
- If `ELEPHAS_CACHE_DSN` is set, the Redis connection is reachable.

If any required value is missing or any connection check fails, startup fails with a
descriptive error and a nonzero exit code.

---

## 9. Database Schema (Postgres / AGE Backends)

### 9.1 Migrations

Elephas uses forward-only numbered migration files. Migration state is tracked in a
`elephas_migrations` table. No migration framework is required; the service applies pending
migrations at startup.

Migration naming convention: `{four-digit-sequence}_{description}.sql`
Example: `0001_initial_schema.sql`

### 9.2 Schema (Postgres Adjacency List)

```sql
CREATE EXTENSION IF NOT EXISTS "pgcrypto";
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

CREATE TABLE entities (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name        TEXT NOT NULL,
  type        TEXT NOT NULL CHECK (type IN ('person','organization','place','concept','object','agent')),
  external_id TEXT UNIQUE,
  metadata    JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX entities_name_trgm ON entities USING GIN (name gin_trgm_ops);
CREATE INDEX entities_type ON entities (type);
CREATE INDEX entities_external_id ON entities (external_id) WHERE external_id IS NOT NULL;

CREATE TABLE memories (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id   UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  content     TEXT NOT NULL,
  category    TEXT NOT NULL CHECK (category IN ('preference','fact','relationship','event','instruction')),
  confidence  FLOAT NOT NULL DEFAULT 1.0 CHECK (confidence >= 0.0 AND confidence <= 1.0),
  source_id   UUID,
  expires_at  TIMESTAMPTZ,
  metadata    JSONB NOT NULL DEFAULT '{}',
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  search_vec  TSVECTOR GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);

CREATE INDEX memories_entity_id ON memories (entity_id);
CREATE INDEX memories_category ON memories (category);
CREATE INDEX memories_search_vec ON memories USING GIN (search_vec);
CREATE INDEX memories_expires_at ON memories (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX memories_updated_at ON memories (updated_at);

CREATE TABLE relationships (
  id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  from_entity_id   UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  to_entity_id     UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  type             TEXT NOT NULL,
  weight           FLOAT,
  metadata         JSONB NOT NULL DEFAULT '{}',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (from_entity_id, to_entity_id, type)
);

CREATE INDEX relationships_from ON relationships (from_entity_id);
CREATE INDEX relationships_to ON relationships (to_entity_id);
CREATE INDEX relationships_type ON relationships (type);

CREATE TABLE ingest_sources (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  raw_text            TEXT NOT NULL,
  subject_entity_id   UUID REFERENCES entities(id) ON DELETE SET NULL,
  extractor_model     TEXT NOT NULL,
  resolution_plan     JSONB NOT NULL DEFAULT '{}',
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE elephas_migrations (
  id          SERIAL PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  applied_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### 9.3 Cascade Behavior

- Deleting an entity cascades to all its memories and all relationships where it is
  `from_entity_id` or `to_entity_id`.
- Deleting an ingest source does not cascade to the memories it created. Memories are
  permanent records; their provenance is audit data.
- Deleting a memory does not affect its ingest source record.

---

## 10. Extractor Interface

The `Extractor` interface decouples the LLM integration from the ingest pipeline. Any
caller-supplied implementation that satisfies this interface works.

### 10.1 Interface

```
Extract(ctx, request ExtractRequest) → ([]CandidateMemory, error)
```

`ExtractRequest`:

- `raw_text` (string)
- `subject_entity_name` (string, nullable)
- `model` (string, nullable — uses config default when absent)
- `system_prompt_override` (string, nullable)

`CandidateMemory`:

- `content` (string)
- `category` (enum)
- `confidence` (float, 0.0–1.0)
- `subject_entity_name` (string, nullable)
- `related_entity_names` ([]string)

### 10.2 Default Extractor Behavior

The default extractor implementation:

1. Constructs a system prompt instructing the model to extract discrete facts as JSON.
2. Sends the raw text as a user message.
3. Expects the model to return a JSON array of candidates with no preamble.
4. Validates the response against the `CandidateMemory` schema.
5. Returns an error if the model returns malformed output or the HTTP call fails.

The system prompt instructs the model to:

- Return only a JSON array. No markdown, no preamble.
- Emit one memory per discrete fact.
- Prefer specific, atomic facts over compound statements.
- Assign confidence based on how directly the text asserts the fact.

### 10.3 Extraction Failure Semantics

If the extractor returns an error:

- The ingest pipeline aborts and returns `extraction_failed`.
- No storage writes occur.
- The ingest source record is not written.

If the extractor returns zero candidates, the ingest pipeline returns a successful response
with all counts at zero. This is not an error.

---

## 11. Conflict Resolution

### 11.1 Similarity Threshold

The resolver uses string similarity to compare candidate memory content against existing
memories of the same entity and category. The default similarity threshold for triggering
an `update` or `merge` action is `0.85`. This is configurable via `ELEPHAS_RESOLVE_THRESHOLD`
(float, 0.0–1.0).

### 11.2 Action Selection Rules

For each candidate memory:

1. Load all non-expired memories for the subject entity with the same category.
2. Compute similarity between the candidate `content` and each existing memory's `content`.
3. If the maximum similarity is below threshold:
   - Action: `create`
4. If the maximum similarity is at or above threshold:
   - If the candidate `confidence` > existing memory `confidence`:
     - Action: `update` (candidate supersedes)
   - If the candidate `confidence` <= existing memory `confidence` and content is materially
     the same:
     - Action: `merge` (no write, already known)
   - If the candidate `confidence` <= existing memory `confidence` and content is different
     but not confidently better:
     - Action: `no_op`

### 11.3 Category-Specific Rules

`preference` memories are treated as singletons per entity per preference topic. If an
incoming preference fact is similar to an existing preference fact, the new one always wins
regardless of confidence. This prevents stale preferences from persisting after a user
explicitly changes them.

`instruction` memories follow the same singleton rule as `preference`.

`event` and `fact` memories are additive. New events and facts `create` unless they are
near-identical duplicates (merge threshold).

`relationship` memories defer to the explicit `Relationship` graph edge. If a relationship
memory is extracted and the corresponding `Relationship` edge already exists, the action
is `merge`.

### 11.4 Conflict Resolution and the Update Guarantee

Elephas guarantees that a resolved `update` action calls `Store.UpdateMemory` with the
candidate content and new confidence, preserving the original memory's `id`, `created_at`,
and full audit trail. Conflict resolution never deletes a memory and creates a new one as a
substitute. This is a first-class correctness invariant.

---

## 12. Search

### 12.1 Full-Text Search (Default)

`SearchMemories` queries the `search_vec` tsvector column using `websearch_to_tsquery` on
the Postgres backend. Results are ranked by `ts_rank_cd`.

### 12.2 Entity-Scoped Search

When `entity_id` is provided, the query is filtered to memories belonging to that entity
before ranking. This is the standard pattern for surfacing relevant context for a known
subject.

### 12.3 Graph-Augmented Results

When `include_entity_context` is true, each result is augmented with the owning entity and
its first-degree relationships. This allows a caller to retrieve not just the matching memory
but the broader graph context around the entity it belongs to.

### 12.4 Expired Memory Handling

Expired memories are excluded from all search results by default. They remain in the store
and can be retrieved directly by ID or via list with `include_expired: true`.

---

## 13. Observability

### 13.1 Structured Logging

Elephas emits structured JSON logs to stdout. All log entries include:

- `level` (string): `debug`, `info`, `warn`, `error`
- `ts` (ISO-8601)
- `msg` (string)
- `component` (string): one of `api`, `ingest`, `store`, `extractor`, `resolver`

Request logs include:

- `method`, `path`, `status`, `duration_ms`, `request_id`

Ingest logs include:

- `ingest_source_id`, `subject_entity_id`, `extractor_model`, `candidates`, `created`,
  `updated`, `merged`, `no_op`, `duration_ms`

Error logs include:

- `error` (string) — the error message
- `stack` (string, optional) — stack trace when available

### 13.2 Health and Readiness

`GET /v1/health` — always returns `200 OK` if the process is alive.

`GET /v1/ready` — returns `200 OK` only if the database connection pool is healthy and
migrations are current. Returns `503 Service Unavailable` otherwise.

### 13.3 Request IDs

Every request is assigned a `X-Request-ID` header value. If the caller supplies one,
Elephas uses it. If not, Elephas generates a UUID. The request ID is included in all
log entries for that request.

---

## 14. Failure Model

### 14.1 Failure Classes

1. `Startup Failures`
   - Missing required config
   - Database unreachable
   - Migrations pending or failed

2. `Store Failures`
   - Query error
   - Connection pool exhausted
   - Constraint violation

3. `Extractor Failures`
   - HTTP transport error
   - Non-2xx response from extractor endpoint
   - Malformed JSON response
   - Candidate count exceeds `ELEPHAS_EXTRACTOR_MAX_CANDIDATES`

4. `Ingest Pipeline Failures`
   - Extract stage error (see extractor failures)
   - Resolve stage error (store read failure during context load)
   - Commit stage error (transaction failure)

5. `Request Validation Failures`
   - Missing required fields
   - Invalid enum values
   - UUID parse errors
   - Confidence out of range

### 14.2 Ingest Atomicity

The commit stage of the ingest pipeline executes inside a single database transaction.
If any write in the transaction fails, all writes roll back. The caller receives a
`store_error` response and may retry the ingest request in full.

### 14.3 Partial Availability

If the cache backend (Redis) is unavailable after startup, Elephas falls through to the store
for every request and logs a warning. It does not return errors to callers due to cache
unavailability.

If the extractor endpoint is unavailable, only ingest requests fail. All read and direct
write operations on memories, entities, and relationships are unaffected.

If the best-effort ingest audit write fails after a successful commit, the committed memories
remain durable and visible to callers, but the corresponding `IngestSource` record may be
missing.

---

## 15. Non-Goals Restated (v1)

- No vector embeddings or semantic similarity search. Full-text search and graph traversal
  are the v1 search primitives. pgvector is an explicit post-v1 extension point.
- No multi-tenancy in the open source core. All data shares a single namespace.
- No streaming or webhook events on memory changes.
- No bundled LLM model. Elephas calls external endpoints only.
- No managed tier, billing, or hosting infrastructure in this specification.

---

## 16. Extension Points (Post-v1)

The following are intentional gaps left for post-v1 work:

- `pgvector backend` — add a `search_embedding` column to `memories`, populate via background
  job, and add a `SearchMemoriesSemantic` method to the `Store` interface.
- `Multi-tenancy` — add a `tenant_id` column to all tables and enforce row-level security.
- `Webhooks` — emit `memory.created`, `memory.updated`, `memory.deleted`, and `ingest.completed`
  events to a configured HTTP endpoint.
- `Pluggable extractors` — add a second built-in extractor that calls the Anthropic Messages API
  rather than an OpenAI-compatible endpoint.
- `Memory expiry worker` — background process that deletes or archives expired memories on a
  configurable schedule, rather than filtering them at query time.
- `Bulk ingest` — accept an array of raw text submissions in a single request and process them
  as a batch.

---

## 17. Implementation Checklist

### 17.1 Required for v1 Conformance

- `Store` interface defined and implemented for Postgres (adjacency list) backend
- `Store` interface implemented for SQLite backend
- Database migrations with forward-only numbered files
- `entities`, `memories`, `relationships`, `ingest_sources`, `elephas_migrations` tables
- Full-text search via tsvector/GIN on `memories.content`
- `PATCH` on memories applies partial updates, never deletes and recreates
- Cascade delete: entity deletion cascades to memories and relationships
- Ingest pipeline: Extract → Resolve → Commit → Audit, all in sequence
- Commit stage executes in a single database transaction
- `Extractor` interface defined and implemented for OpenAI-compatible endpoints
- Conflict resolver with `create`, `update`, `merge`, `no_op` actions
- `preference` and `instruction` category singleton rules enforced
- Expired memory filtering on all list and search endpoints
- Entity context endpoint with configurable depth
- Graph path-finding endpoint with max depth cap
- Structured JSON logging with component and request context
- `/v1/health` and `/v1/ready` endpoints
- Startup validation with nonzero exit on missing required config or failed DB check
- All API error responses use the standard error envelope
- `X-Request-ID` propagation on all requests

### 17.2 Recommended (Not Required for v1)

- Apache AGE backend implementing the same `Store` interface
- Redis cache layer behind the store for hot entity context reads
- `dry_run` support on ingest endpoint
- `ELEPHAS_EXTRACTOR_MAX_CANDIDATES` enforcement with error on overflow
- `GET /v1/stats` endpoint
- `POST /v1/graph/path` endpoint
- Per-request `extractor_model` override on ingest
- `subject_external_id` resolution on ingest (look up entity by external ID)
