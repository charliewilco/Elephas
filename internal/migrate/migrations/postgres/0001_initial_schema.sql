CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE IF NOT EXISTS elephas_migrations (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS entities (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('person', 'organization', 'place', 'concept', 'object', 'agent')),
  external_id TEXT UNIQUE,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS entities_name_trgm ON entities USING GIN (name gin_trgm_ops);
CREATE INDEX IF NOT EXISTS entities_type_idx ON entities (type);
CREATE INDEX IF NOT EXISTS entities_external_id_idx ON entities (external_id) WHERE external_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS ingest_sources (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  raw_text TEXT NOT NULL,
  subject_entity_id UUID REFERENCES entities(id) ON DELETE SET NULL,
  extractor_model TEXT NOT NULL,
  resolution_plan JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS ingest_sources_subject_entity_id_idx ON ingest_sources (subject_entity_id);
CREATE INDEX IF NOT EXISTS ingest_sources_created_at_idx ON ingest_sources (created_at);

CREATE TABLE IF NOT EXISTS memories (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  category TEXT NOT NULL CHECK (category IN ('preference', 'fact', 'relationship', 'event', 'instruction')),
  confidence DOUBLE PRECISION NOT NULL DEFAULT 1.0 CHECK (confidence >= 0.0 AND confidence <= 1.0),
  source_id UUID REFERENCES ingest_sources(id) ON DELETE SET NULL,
  expires_at TIMESTAMPTZ,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  search_vec TSVECTOR GENERATED ALWAYS AS (to_tsvector('english', content)) STORED
);

CREATE INDEX IF NOT EXISTS memories_entity_id_idx ON memories (entity_id);
CREATE INDEX IF NOT EXISTS memories_entity_category_idx ON memories (entity_id, category);
CREATE INDEX IF NOT EXISTS memories_category_idx ON memories (category);
CREATE INDEX IF NOT EXISTS memories_source_id_idx ON memories (source_id);
CREATE INDEX IF NOT EXISTS memories_search_vec_idx ON memories USING GIN (search_vec);
CREATE INDEX IF NOT EXISTS memories_expires_at_idx ON memories (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS memories_updated_at_idx ON memories (updated_at);

CREATE TABLE IF NOT EXISTS relationships (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  from_entity_id UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  to_entity_id UUID NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  weight DOUBLE PRECISION,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  UNIQUE (from_entity_id, to_entity_id, type)
);

CREATE INDEX IF NOT EXISTS relationships_from_entity_id_idx ON relationships (from_entity_id);
CREATE INDEX IF NOT EXISTS relationships_to_entity_id_idx ON relationships (to_entity_id);
CREATE INDEX IF NOT EXISTS relationships_type_idx ON relationships (type);

CREATE OR REPLACE FUNCTION elephas_touch_updated_at()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  NEW.updated_at := NOW();
  RETURN NEW;
END;
$$;

DROP TRIGGER IF EXISTS entities_touch_updated_at ON entities;
CREATE TRIGGER entities_touch_updated_at
BEFORE UPDATE ON entities
FOR EACH ROW
EXECUTE FUNCTION elephas_touch_updated_at();

DROP TRIGGER IF EXISTS memories_touch_updated_at ON memories;
CREATE TRIGGER memories_touch_updated_at
BEFORE UPDATE ON memories
FOR EACH ROW
EXECUTE FUNCTION elephas_touch_updated_at();
