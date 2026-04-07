package age

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/store/sqlstore"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type Store struct {
	*sqlstore.Store
	db     *sql.DB
	runner sqlstore.Runner
	logger *slog.Logger
}

func Open(ctx context.Context, cfg config.DatabaseConfig, searchCfg config.SearchConfig) (elephas.Store, *sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, nil, err
	}

	db.SetMaxOpenConns(cfg.MaxConns)
	db.SetMaxIdleConns(cfg.IdleConns)
	db.SetConnMaxIdleTime(5 * time.Minute)

	pingCtx := ctx
	cancel := func() {}
	if cfg.ConnTimeout > 0 {
		pingCtx, cancel = context.WithTimeout(ctx, cfg.ConnTimeout)
	}
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	return New(
		db,
		db,
		true,
		sqlstore.WithSearchLimits(searchCfg.DefaultLimit, searchCfg.MaxLimit),
	), db, nil
}

func New(db *sql.DB, runner sqlstore.Runner, closeDB bool, opts ...sqlstore.Option) *Store {
	return &Store{
		Store:  sqlstore.NewWithRunner(db, runner, "age", closeDB, opts...),
		db:     db,
		runner: runner,
		logger: slog.Default(),
	}
}

func (s *Store) RunInTx(ctx context.Context, fn func(context.Context, elephas.Store) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return elephas.WrapError(elephas.ErrorCodeStore, "begin transaction", err, nil)
	}

	defaultLimit, maxLimit := s.SearchLimits()
	child := New(s.db, tx, false, sqlstore.WithSearchLimits(defaultLimit, maxLimit))
	if err := fn(ctx, child); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return elephas.WrapError(elephas.ErrorCodeStore, "commit transaction", err, nil)
	}

	return nil
}

func (s *Store) Reconcile(ctx context.Context) error {
	storeLogger := s.logger.With("component", "store")
	if err := s.deleteAllGraph(ctx); err != nil {
		return err
	}

	storeLogger.Info("reconciling age graph projection", "request_id", elephas.RequestIDFromContext(ctx))

	entities, err := s.listAllEntities(ctx)
	if err != nil {
		return err
	}
	for _, entity := range entities {
		if err := s.syncEntity(ctx, entity); err != nil {
			return err
		}
	}

	memories, err := s.listAllMemories(ctx)
	if err != nil {
		return err
	}
	for _, memory := range memories {
		if err := s.syncMemory(ctx, memory); err != nil {
			return err
		}
	}

	relationships, err := s.listAllRelationships(ctx)
	if err != nil {
		return err
	}
	for _, relationship := range relationships {
		if err := s.syncRelationship(ctx, relationship); err != nil {
			return err
		}
	}

	storeLogger.Info("age graph reconciliation completed",
		"request_id", elephas.RequestIDFromContext(ctx),
		"entities", len(entities),
		"memories", len(memories),
		"relationships", len(relationships),
	)
	return nil
}

func (s *Store) CreateEntity(ctx context.Context, params elephas.CreateEntityParams) (elephas.Entity, error) {
	if s.inTransaction() {
		entity, err := s.Store.CreateEntity(ctx, params)
		if err != nil {
			return elephas.Entity{}, err
		}
		if err := s.syncEntity(ctx, entity); err != nil {
			return elephas.Entity{}, err
		}
		return entity, nil
	}

	var entity elephas.Entity
	err := s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		created, err := tx.(*Store).CreateEntity(ctx, params)
		if err == nil {
			entity = created
		}
		return err
	})
	return entity, err
}

func (s *Store) UpdateEntity(ctx context.Context, id uuid.UUID, patch elephas.EntityPatch) (elephas.Entity, error) {
	if s.inTransaction() {
		entity, err := s.Store.UpdateEntity(ctx, id, patch)
		if err != nil {
			return elephas.Entity{}, err
		}
		if err := s.syncEntity(ctx, entity); err != nil {
			return elephas.Entity{}, err
		}
		return entity, nil
	}

	var entity elephas.Entity
	err := s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		updated, err := tx.(*Store).UpdateEntity(ctx, id, patch)
		if err == nil {
			entity = updated
		}
		return err
	})
	return entity, err
}

func (s *Store) DeleteEntity(ctx context.Context, id uuid.UUID) error {
	if s.inTransaction() {
		if err := s.Store.DeleteEntity(ctx, id); err != nil {
			return err
		}
		return s.deleteEntityNode(ctx, id)
	}

	return s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		return tx.(*Store).DeleteEntity(ctx, id)
	})
}

func (s *Store) CreateMemory(ctx context.Context, params elephas.CreateMemoryParams) (elephas.Memory, error) {
	if s.inTransaction() {
		memory, err := s.Store.CreateMemory(ctx, params)
		if err != nil {
			return elephas.Memory{}, err
		}
		if err := s.syncMemory(ctx, memory); err != nil {
			return elephas.Memory{}, err
		}
		return memory, nil
	}

	var memory elephas.Memory
	err := s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		created, err := tx.(*Store).CreateMemory(ctx, params)
		if err == nil {
			memory = created
		}
		return err
	})
	return memory, err
}

func (s *Store) UpdateMemory(ctx context.Context, id uuid.UUID, patch elephas.MemoryPatch) (elephas.Memory, error) {
	if s.inTransaction() {
		memory, err := s.Store.UpdateMemory(ctx, id, patch)
		if err != nil {
			return elephas.Memory{}, err
		}
		if err := s.syncMemory(ctx, memory); err != nil {
			return elephas.Memory{}, err
		}
		return memory, nil
	}

	var memory elephas.Memory
	err := s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		updated, err := tx.(*Store).UpdateMemory(ctx, id, patch)
		if err == nil {
			memory = updated
		}
		return err
	})
	return memory, err
}

func (s *Store) DeleteMemory(ctx context.Context, id uuid.UUID) error {
	if s.inTransaction() {
		if err := s.Store.DeleteMemory(ctx, id); err != nil {
			return err
		}
		return s.deleteMemoryNode(ctx, id)
	}

	return s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		return tx.(*Store).DeleteMemory(ctx, id)
	})
}

func (s *Store) CreateRelationship(ctx context.Context, params elephas.CreateRelationshipParams) (elephas.Relationship, error) {
	if s.inTransaction() {
		relationship, err := s.Store.CreateRelationship(ctx, params)
		if err != nil {
			return elephas.Relationship{}, err
		}
		if err := s.syncRelationship(ctx, relationship); err != nil {
			return elephas.Relationship{}, err
		}
		return relationship, nil
	}

	var relationship elephas.Relationship
	err := s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		created, err := tx.(*Store).CreateRelationship(ctx, params)
		if err == nil {
			relationship = created
		}
		return err
	})
	return relationship, err
}

func (s *Store) DeleteRelationship(ctx context.Context, id uuid.UUID) error {
	if s.inTransaction() {
		if err := s.Store.DeleteRelationship(ctx, id); err != nil {
			return err
		}
		return s.deleteRelationshipEdge(ctx, id)
	}

	return s.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		return tx.(*Store).DeleteRelationship(ctx, id)
	})
}

func (s *Store) GetEntityContext(ctx context.Context, entityID uuid.UUID, depth int) (elephas.EntityContext, error) {
	entity, err := s.Store.GetEntity(ctx, entityID)
	if err != nil {
		return elephas.EntityContext{}, err
	}

	memories, err := s.Store.ListMemories(ctx, elephas.MemoryFilter{
		EntityID:       &entityID,
		IncludeExpired: true,
		Limit:          500,
	})
	if err != nil {
		return elephas.EntityContext{}, err
	}

	result := elephas.EntityContext{
		Entity:   entity,
		Memories: memories.Data,
	}
	if depth <= 0 {
		return result, nil
	}

	rows, err := s.cypherRows(ctx, fmt.Sprintf(`
MATCH (root:Entity {id: %s})
MATCH p = (root)-[:RELATIONSHIP*1..%d]-(other:Entity)
UNWIND relationships(p) AS rel
RETURN DISTINCT rel.id, rel.type, startNode(rel).id, endNode(rel).id
`, cypherString(entityID.String()), depth), "(relationship_id ag_catalog.agtype, relationship_type ag_catalog.agtype, from_entity_id ag_catalog.agtype, to_entity_id ag_catalog.agtype)", "relationship_id::text, relationship_type::text, from_entity_id::text, to_entity_id::text")
	if err != nil {
		return elephas.EntityContext{}, err
	}
	defer rows.Close()

	seen := map[uuid.UUID]struct{}{}
	for rows.Next() {
		var relationshipIDText, relationshipTypeText, fromIDText, toIDText string
		if err := rows.Scan(&relationshipIDText, &relationshipTypeText, &fromIDText, &toIDText); err != nil {
			return elephas.EntityContext{}, s.wrapError(ctx, "query age entity context", err)
		}

		relationshipID, err := parseAGUUID(relationshipIDText)
		if err != nil {
			return elephas.EntityContext{}, err
		}
		if _, ok := seen[relationshipID]; ok {
			continue
		}
		seen[relationshipID] = struct{}{}

		fromID, err := parseAGUUID(fromIDText)
		if err != nil {
			return elephas.EntityContext{}, err
		}
		toID, err := parseAGUUID(toIDText)
		if err != nil {
			return elephas.EntityContext{}, err
		}

		relationship, err := s.Store.GetRelationship(ctx, relationshipID)
		if err != nil {
			return elephas.EntityContext{}, err
		}

		relatedID := toID
		if toID == entityID {
			relatedID = fromID
		}
		relatedEntity, err := s.Store.GetEntity(ctx, relatedID)
		if err != nil {
			return elephas.EntityContext{}, err
		}

		relationship.Type = parseAGString(relationshipTypeText)
		result.Relationships = append(result.Relationships, elephas.ResolvedRelationship{
			Relationship:  relationship,
			RelatedEntity: relatedEntity,
		})
	}

	return result, rows.Err()
}

func (s *Store) FindPath(ctx context.Context, fromEntityID, toEntityID uuid.UUID, maxDepth int) ([]elephas.PathNode, error) {
	if fromEntityID == toEntityID {
		entity, err := s.Store.GetEntity(ctx, fromEntityID)
		if err != nil {
			return nil, err
		}
		return []elephas.PathNode{{Entity: entity}}, nil
	}

	rows, err := s.cypherRows(ctx, fmt.Sprintf(`
MATCH p = shortestPath((from:Entity {id: %s})-[:RELATIONSHIP*..%d]->(to:Entity {id: %s}))
UNWIND range(0, length(p)) AS idx
RETURN idx, nodes(p)[idx].id, CASE WHEN idx = 0 THEN null ELSE relationships(p)[idx - 1].id END
`, cypherString(fromEntityID.String()), maxDepth, cypherString(toEntityID.String())), "(idx integer, entity_id ag_catalog.agtype, relationship_id ag_catalog.agtype)", "idx, entity_id::text, relationship_id::text")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type pathRow struct {
		idx            int
		entityID       uuid.UUID
		relationshipID *uuid.UUID
	}
	var pathRows []pathRow
	for rows.Next() {
		var idx int
		var entityIDText string
		var relationshipIDText sql.NullString
		if err := rows.Scan(&idx, &entityIDText, &relationshipIDText); err != nil {
			return nil, s.wrapError(ctx, "query age path", err)
		}

		entityID, err := parseAGUUID(entityIDText)
		if err != nil {
			return nil, err
		}

		var relationshipID *uuid.UUID
		if relationshipIDText.Valid && parseAGString(relationshipIDText.String) != "" && parseAGString(relationshipIDText.String) != "null" {
			parsed, err := parseAGUUID(relationshipIDText.String)
			if err != nil {
				return nil, err
			}
			relationshipID = &parsed
		}

		pathRows = append(pathRows, pathRow{
			idx:            idx,
			entityID:       entityID,
			relationshipID: relationshipID,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pathRows) == 0 {
		return nil, elephas.NewError(elephas.ErrorCodeNotFound, "path not found", map[string]any{
			"from_entity_id": fromEntityID.String(),
			"to_entity_id":   toEntityID.String(),
		})
	}

	path := make([]elephas.PathNode, 0, len(pathRows))
	for _, row := range pathRows {
		entity, err := s.Store.GetEntity(ctx, row.entityID)
		if err != nil {
			return nil, err
		}

		var relationship *elephas.Relationship
		if row.relationshipID != nil {
			value, err := s.Store.GetRelationship(ctx, *row.relationshipID)
			if err != nil {
				return nil, err
			}
			relationship = &value
		}

		path = append(path, elephas.PathNode{
			Entity:       entity,
			Relationship: relationship,
		})
	}

	return path, nil
}

func (s *Store) inTransaction() bool {
	_, ok := s.runner.(*sql.Tx)
	return ok
}

func (s *Store) syncEntity(ctx context.Context, entity elephas.Entity) error {
	return s.cypherExec(ctx, fmt.Sprintf(`
MERGE (e:Entity {id: %s})
SET e.name = %s,
    e.type = %s,
    e.external_id = %s,
    e.metadata_json = %s
RETURN 1
`, cypherString(entity.ID.String()), cypherString(entity.Name), cypherString(string(entity.Type)), cypherNullableString(entity.ExternalID), cypherString(mustJSON(entity.Metadata))))
}

func (s *Store) syncMemory(ctx context.Context, memory elephas.Memory) error {
	entity, err := s.Store.GetEntity(ctx, memory.EntityID)
	if err != nil {
		return err
	}
	if err := s.syncEntity(ctx, entity); err != nil {
		return err
	}

	return s.cypherExec(ctx, fmt.Sprintf(`
MATCH (e:Entity {id: %s})
MERGE (m:Memory {id: %s})
SET m.entity_id = %s,
    m.category = %s,
    m.content = %s,
    m.confidence = %s,
    m.source_id = %s,
    m.expires_at = %s,
    m.metadata_json = %s
MERGE (e)-[:HAS_MEMORY]->(m)
RETURN 1
`, cypherString(memory.EntityID.String()), cypherString(memory.ID.String()), cypherString(memory.EntityID.String()), cypherString(string(memory.Category)), cypherString(memory.Content), cypherFloat(memory.Confidence), cypherNullableUUID(memory.SourceID), cypherNullableTime(memory.ExpiresAt), cypherString(mustJSON(memory.Metadata))))
}

func (s *Store) syncRelationship(ctx context.Context, relationship elephas.Relationship) error {
	fromEntity, err := s.Store.GetEntity(ctx, relationship.FromEntityID)
	if err != nil {
		return err
	}
	if err := s.syncEntity(ctx, fromEntity); err != nil {
		return err
	}

	toEntity, err := s.Store.GetEntity(ctx, relationship.ToEntityID)
	if err != nil {
		return err
	}
	if err := s.syncEntity(ctx, toEntity); err != nil {
		return err
	}

	return s.cypherExec(ctx, fmt.Sprintf(`
MATCH (from:Entity {id: %s}), (to:Entity {id: %s})
MERGE (from)-[r:RELATIONSHIP {id: %s}]->(to)
SET r.type = %s,
    r.weight = %s,
    r.metadata_json = %s
RETURN 1
`, cypherString(relationship.FromEntityID.String()), cypherString(relationship.ToEntityID.String()), cypherString(relationship.ID.String()), cypherString(relationship.Type), cypherNullableFloat(relationship.Weight), cypherString(mustJSON(relationship.Metadata))))
}

func (s *Store) deleteEntityNode(ctx context.Context, id uuid.UUID) error {
	return s.cypherExec(ctx, fmt.Sprintf(`
MATCH (e:Entity {id: %s})
DETACH DELETE e
RETURN 1
`, cypherString(id.String())))
}

func (s *Store) deleteMemoryNode(ctx context.Context, id uuid.UUID) error {
	return s.cypherExec(ctx, fmt.Sprintf(`
MATCH (m:Memory {id: %s})
DETACH DELETE m
RETURN 1
`, cypherString(id.String())))
}

func (s *Store) deleteRelationshipEdge(ctx context.Context, id uuid.UUID) error {
	return s.cypherExec(ctx, fmt.Sprintf(`
MATCH ()-[r:RELATIONSHIP {id: %s}]->()
DELETE r
RETURN 1
`, cypherString(id.String())))
}

func (s *Store) deleteAllGraph(ctx context.Context) error {
	return s.cypherExec(ctx, `
MATCH (n)
DETACH DELETE n
RETURN 1
`)
}

func (s *Store) cypherExec(ctx context.Context, query string) error {
	if err := s.ensureSession(ctx); err != nil {
		return err
	}

	rowsQuery := fmt.Sprintf("SELECT * FROM ag_catalog.cypher('elephas', $$%s$$) AS (result ag_catalog.agtype)", query)
	rows, err := s.runner.QueryContext(ctx, rowsQuery)
	if err != nil {
		return s.wrapError(ctx, "execute age cypher", err)
	}
	defer rows.Close()
	return rows.Err()
}

func (s *Store) cypherRows(ctx context.Context, query, recordDef, selectList string) (*sql.Rows, error) {
	if err := s.ensureSession(ctx); err != nil {
		return nil, err
	}

	sqlQuery := fmt.Sprintf("SELECT %s FROM ag_catalog.cypher('elephas', $$%s$$) AS %s", selectList, query, recordDef)
	rows, err := s.runner.QueryContext(ctx, sqlQuery)
	if err != nil {
		return nil, s.wrapError(ctx, "query age cypher", err)
	}
	return rows, nil
}

func (s *Store) ensureSession(ctx context.Context) error {
	if _, err := s.runner.ExecContext(ctx, "LOAD 'age'"); err != nil && !strings.Contains(err.Error(), "already loaded") {
		return s.wrapError(ctx, "load age", err)
	}
	if _, err := s.runner.ExecContext(ctx, "SET search_path = ag_catalog, public"); err != nil {
		return s.wrapError(ctx, "set age search_path", err)
	}
	return nil
}

func (s *Store) listAllEntities(ctx context.Context) ([]elephas.Entity, error) {
	var all []elephas.Entity
	offset := 0
	for {
		page, err := s.Store.ListEntities(ctx, elephas.EntityFilter{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if !page.HasMore {
			return all, nil
		}
		offset += len(page.Data)
	}
}

func (s *Store) listAllMemories(ctx context.Context) ([]elephas.Memory, error) {
	var all []elephas.Memory
	offset := 0
	for {
		page, err := s.Store.ListMemories(ctx, elephas.MemoryFilter{
			IncludeExpired: true,
			Limit:          500,
			Offset:         offset,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if !page.HasMore {
			return all, nil
		}
		offset += len(page.Data)
	}
}

func (s *Store) listAllRelationships(ctx context.Context) ([]elephas.Relationship, error) {
	var all []elephas.Relationship
	offset := 0
	for {
		page, err := s.Store.ListRelationships(ctx, elephas.RelationshipFilter{Limit: 500, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Data...)
		if !page.HasMore {
			return all, nil
		}
		offset += len(page.Data)
	}
}

func (s *Store) wrapError(ctx context.Context, message string, err error) error {
	storeLogger := s.logger.With("component", "store")
	storeLogger.Warn(message,
		"request_id", elephas.RequestIDFromContext(ctx),
		"error", err,
	)
	return elephas.WrapError(elephas.ErrorCodeStore, message, err, nil)
}

func cypherString(value string) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func cypherNullableString(value *string) string {
	if value == nil {
		return "null"
	}
	return cypherString(*value)
}

func cypherNullableUUID(value *uuid.UUID) string {
	if value == nil {
		return "null"
	}
	return cypherString(value.String())
}

func cypherFloat(value float64) string {
	return fmt.Sprintf("%f", value)
}

func cypherNullableFloat(value *float64) string {
	if value == nil {
		return "null"
	}
	return cypherFloat(*value)
}

func cypherNullableTime(value *time.Time) string {
	if value == nil {
		return "null"
	}
	return cypherString(value.UTC().Format(time.RFC3339Nano))
}

func mustJSON(value any) string {
	if value == nil {
		return "{}"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func parseAGUUID(value string) (uuid.UUID, error) {
	return uuid.Parse(parseAGString(value))
}

func parseAGString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var decoded string
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return strings.Trim(trimmed, "\"")
}
