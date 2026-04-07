PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS elephas_migrations (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE TABLE IF NOT EXISTS entities (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('person', 'organization', 'place', 'concept', 'object', 'agent')),
  external_id TEXT UNIQUE,
  metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS entities_name_idx ON entities (name);
CREATE INDEX IF NOT EXISTS entities_type_idx ON entities (type);
CREATE INDEX IF NOT EXISTS entities_external_id_idx ON entities (external_id) WHERE external_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS ingest_sources (
  id TEXT PRIMARY KEY,
  raw_text TEXT NOT NULL,
  subject_entity_id TEXT REFERENCES entities(id) ON DELETE SET NULL,
  extractor_model TEXT NOT NULL,
  resolution_plan TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(resolution_plan)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS ingest_sources_subject_entity_id_idx ON ingest_sources (subject_entity_id);
CREATE INDEX IF NOT EXISTS ingest_sources_created_at_idx ON ingest_sources (created_at);

CREATE TABLE IF NOT EXISTS memories (
  id TEXT PRIMARY KEY,
  entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  content TEXT NOT NULL,
  category TEXT NOT NULL CHECK (category IN ('preference', 'fact', 'relationship', 'event', 'instruction')),
  confidence REAL NOT NULL DEFAULT 1.0 CHECK (confidence >= 0.0 AND confidence <= 1.0),
  source_id TEXT REFERENCES ingest_sources(id) ON DELETE SET NULL,
  expires_at TEXT,
  metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);

CREATE INDEX IF NOT EXISTS memories_entity_id_idx ON memories (entity_id);
CREATE INDEX IF NOT EXISTS memories_entity_category_idx ON memories (entity_id, category);
CREATE INDEX IF NOT EXISTS memories_category_idx ON memories (category);
CREATE INDEX IF NOT EXISTS memories_source_id_idx ON memories (source_id);
CREATE INDEX IF NOT EXISTS memories_expires_at_idx ON memories (expires_at) WHERE expires_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS memories_updated_at_idx ON memories (updated_at);

CREATE TABLE IF NOT EXISTS relationships (
  id TEXT PRIMARY KEY,
  from_entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  to_entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
  type TEXT NOT NULL,
  weight REAL,
  metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  UNIQUE (from_entity_id, to_entity_id, type)
);

CREATE INDEX IF NOT EXISTS relationships_from_entity_id_idx ON relationships (from_entity_id);
CREATE INDEX IF NOT EXISTS relationships_to_entity_id_idx ON relationships (to_entity_id);
CREATE INDEX IF NOT EXISTS relationships_type_idx ON relationships (type);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
  memory_id UNINDEXED,
  content,
  tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TRIGGER IF NOT EXISTS memories_fts_ai
AFTER INSERT ON memories
BEGIN
  INSERT INTO memories_fts (memory_id, content)
  VALUES (NEW.id, NEW.content);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_au
AFTER UPDATE OF content ON memories
BEGIN
  DELETE FROM memories_fts WHERE memory_id = OLD.id;
  INSERT INTO memories_fts (memory_id, content)
  VALUES (NEW.id, NEW.content);
END;

CREATE TRIGGER IF NOT EXISTS memories_fts_ad
AFTER DELETE ON memories
BEGIN
  DELETE FROM memories_fts WHERE memory_id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS entities_touch_updated_at
AFTER UPDATE OF name, type, external_id, metadata ON entities
BEGIN
  UPDATE entities
  SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  WHERE id = OLD.id;
END;

CREATE TRIGGER IF NOT EXISTS memories_touch_updated_at
AFTER UPDATE OF entity_id, content, category, confidence, source_id, expires_at, metadata ON memories
BEGIN
  UPDATE memories
  SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
  WHERE id = OLD.id;
END;
