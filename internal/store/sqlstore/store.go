package sqlstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/charliewilco/elephas"
	"github.com/google/uuid"
)

type Runner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type Store struct {
	db                 *sql.DB
	runner             Runner
	backend            string
	closeDB            bool
	searchDefaultLimit int
	searchMaxLimit     int
}

type predecessor struct {
	entity       uuid.UUID
	relationship elephas.Relationship
}

const (
	defaultSearchDefaultLimit = 20
	defaultSearchMaxLimit     = 100
)

type Option func(*Store)

func WithSearchLimits(defaultLimit, maxLimit int) Option {
	return func(s *Store) {
		s.searchDefaultLimit = defaultLimit
		s.searchMaxLimit = maxLimit
	}
}

func New(db *sql.DB, backend string, opts ...Option) *Store {
	return NewWithRunner(db, db, backend, true, opts...)
}

func NewWithRunner(db *sql.DB, runner Runner, backend string, closeDB bool, opts ...Option) *Store {
	store := &Store{
		db:                 db,
		runner:             runner,
		backend:            backend,
		closeDB:            closeDB,
		searchDefaultLimit: defaultSearchDefaultLimit,
		searchMaxLimit:     defaultSearchMaxLimit,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(store)
		}
	}

	store.searchDefaultLimit, store.searchMaxLimit = normalizeSearchLimits(store.searchDefaultLimit, store.searchMaxLimit)
	return store
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Close() error {
	if !s.closeDB {
		return nil
	}
	return s.db.Close()
}

func (s *Store) RunInTx(ctx context.Context, fn func(context.Context, elephas.Store) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return elephas.WrapError(elephas.ErrorCodeStore, "begin transaction", err, nil)
	}

	child := NewWithRunner(
		s.db,
		tx,
		s.backend,
		false,
		WithSearchLimits(s.searchDefaultLimit, s.searchMaxLimit),
	)

	if err := fn(ctx, child); err != nil {
		_ = tx.Rollback()
		return err
	}

	if err := tx.Commit(); err != nil {
		return elephas.WrapError(elephas.ErrorCodeStore, "commit transaction", err, nil)
	}

	return nil
}

func (s *Store) CreateMemory(ctx context.Context, params elephas.CreateMemoryParams) (elephas.Memory, error) {
	id := uuid.New()
	query := fmt.Sprintf(
		`INSERT INTO memories (id, entity_id, content, category, confidence, source_id, expires_at, metadata)
		 VALUES (%s, %s, %s, %s, %s, %s, %s, %s)`,
		s.bind(1), s.bind(2), s.bind(3), s.bind(4), s.bind(5), s.bind(6), s.bind(7), s.bind(8),
	)

	if _, err := s.runner.ExecContext(
		ctx,
		query,
		id.String(),
		params.EntityID.String(),
		params.Content,
		string(params.Category),
		params.Confidence,
		nullableUUID(params.SourceID),
		nullableTime(params.ExpiresAt),
		mustJSON(params.Metadata),
	); err != nil {
		return elephas.Memory{}, s.wrapExecError("create memory", err)
	}

	return s.GetMemory(ctx, id)
}

func (s *Store) GetMemory(ctx context.Context, id uuid.UUID) (elephas.Memory, error) {
	query := fmt.Sprintf(`SELECT %s FROM memories m WHERE m.id = %s`, s.memorySelect("m"), s.bind(1))
	row := s.runner.QueryRowContext(ctx, query, id.String())
	memory, err := scanMemory(row)
	if err != nil {
		return elephas.Memory{}, s.wrapNotFound("memory", id.String(), err)
	}
	return memory, nil
}

func (s *Store) UpdateMemory(ctx context.Context, id uuid.UUID, patch elephas.MemoryPatch) (elephas.Memory, error) {
	assignments := make([]string, 0, 5)
	args := make([]any, 0, 6)

	if patch.Content != nil {
		assignments = append(assignments, fmt.Sprintf("content = %s", s.bind(len(args)+1)))
		args = append(args, *patch.Content)
	}
	if patch.Confidence != nil {
		assignments = append(assignments, fmt.Sprintf("confidence = %s", s.bind(len(args)+1)))
		args = append(args, *patch.Confidence)
	}
	if patch.ExpiresAt != nil {
		assignments = append(assignments, fmt.Sprintf("expires_at = %s", s.bind(len(args)+1)))
		args = append(args, patch.ExpiresAt.UTC())
	}
	if patch.ClearExpiresAt {
		assignments = append(assignments, "expires_at = NULL")
	}
	if patch.SetMetadata {
		assignments = append(assignments, fmt.Sprintf("metadata = %s", s.bind(len(args)+1)))
		args = append(args, mustJSON(patch.Metadata))
	}
	if patch.SourceID != nil {
		assignments = append(assignments, fmt.Sprintf("source_id = %s", s.bind(len(args)+1)))
		args = append(args, patch.SourceID.String())
	}
	if patch.ClearSourceID {
		assignments = append(assignments, "source_id = NULL")
	}

	if len(assignments) == 0 {
		return s.GetMemory(ctx, id)
	}

	args = append(args, id.String())
	query := fmt.Sprintf("UPDATE memories SET %s WHERE id = %s", strings.Join(assignments, ", "), s.bind(len(args)))
	result, err := s.runner.ExecContext(ctx, query, args...)
	if err != nil {
		return elephas.Memory{}, s.wrapExecError("update memory", err)
	}

	if err := ensureRowsAffected(result, "memory", id.String()); err != nil {
		return elephas.Memory{}, err
	}

	return s.GetMemory(ctx, id)
}

func (s *Store) DeleteMemory(ctx context.Context, id uuid.UUID) error {
	result, err := s.runner.ExecContext(ctx, fmt.Sprintf("DELETE FROM memories WHERE id = %s", s.bind(1)), id.String())
	if err != nil {
		return s.wrapExecError("delete memory", err)
	}
	return ensureRowsAffected(result, "memory", id.String())
}

func (s *Store) ListMemories(ctx context.Context, filter elephas.MemoryFilter) (elephas.Page[elephas.Memory], error) {
	where, args := s.memoryWhere(filter)
	total, err := s.count(ctx, "memories m", where, args)
	if err != nil {
		return elephas.Page[elephas.Memory]{}, err
	}

	limit := normalizeLimit(filter.Limit, 50, 500)
	offset := normalizeOffset(filter.Offset)
	args = append(args, limit, offset)

	query := fmt.Sprintf(
		`SELECT %s FROM memories m %s ORDER BY m.updated_at DESC LIMIT %s OFFSET %s`,
		s.memorySelect("m"),
		where,
		s.bind(len(args)-1),
		s.bind(len(args)),
	)

	rows, err := s.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return elephas.Page[elephas.Memory]{}, s.wrapExecError("list memories", err)
	}
	defer rows.Close()

	items, err := scanMemories(rows)
	if err != nil {
		return elephas.Page[elephas.Memory]{}, err
	}

	return elephas.Page[elephas.Memory]{
		Data:    items,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(items) < total,
	}, nil
}

func (s *Store) ListActiveMemoriesByEntityAndCategory(ctx context.Context, entityID uuid.UUID, category elephas.MemoryCategory) ([]elephas.Memory, error) {
	query := fmt.Sprintf(
		`SELECT %s FROM memories m
		 WHERE m.entity_id = %s AND m.category = %s AND %s
		 ORDER BY m.updated_at DESC`,
		s.memorySelect("m"),
		s.bind(1),
		s.bind(2),
		s.activePredicate("m.expires_at"),
	)

	rows, err := s.runner.QueryContext(ctx, query, entityID.String(), string(category))
	if err != nil {
		return nil, s.wrapExecError("list active memories", err)
	}
	defer rows.Close()

	return scanMemories(rows)
}

func (s *Store) CreateEntity(ctx context.Context, params elephas.CreateEntityParams) (elephas.Entity, error) {
	id := uuid.New()
	query := fmt.Sprintf(
		`INSERT INTO entities (id, name, type, external_id, metadata)
		 VALUES (%s, %s, %s, %s, %s)`,
		s.bind(1), s.bind(2), s.bind(3), s.bind(4), s.bind(5),
	)

	if _, err := s.runner.ExecContext(ctx, query, id.String(), params.Name, string(params.Type), nullableString(params.ExternalID), mustJSON(params.Metadata)); err != nil {
		return elephas.Entity{}, s.wrapExecError("create entity", err)
	}

	return s.GetEntity(ctx, id)
}

func (s *Store) GetEntity(ctx context.Context, id uuid.UUID) (elephas.Entity, error) {
	query := fmt.Sprintf(`SELECT %s FROM entities e WHERE e.id = %s`, s.entitySelect("e"), s.bind(1))
	row := s.runner.QueryRowContext(ctx, query, id.String())
	entity, err := scanEntity(row)
	if err != nil {
		return elephas.Entity{}, s.wrapNotFound("entity", id.String(), err)
	}
	return entity, nil
}

func (s *Store) GetEntityByExternalID(ctx context.Context, externalID string) (elephas.Entity, error) {
	query := fmt.Sprintf(`SELECT %s FROM entities e WHERE e.external_id = %s`, s.entitySelect("e"), s.bind(1))
	row := s.runner.QueryRowContext(ctx, query, externalID)
	entity, err := scanEntity(row)
	if err != nil {
		return elephas.Entity{}, s.wrapNotFound("entity", externalID, err)
	}
	return entity, nil
}

func (s *Store) FindEntityByName(ctx context.Context, name string) (elephas.Entity, error) {
	query := fmt.Sprintf(`SELECT %s FROM entities e WHERE lower(e.name) = lower(%s) ORDER BY e.created_at ASC LIMIT 1`, s.entitySelect("e"), s.bind(1))
	row := s.runner.QueryRowContext(ctx, query, name)
	entity, err := scanEntity(row)
	if err != nil {
		return elephas.Entity{}, s.wrapNotFound("entity", name, err)
	}
	return entity, nil
}

func (s *Store) UpdateEntity(ctx context.Context, id uuid.UUID, patch elephas.EntityPatch) (elephas.Entity, error) {
	assignments := make([]string, 0, 4)
	args := make([]any, 0, 5)

	if patch.Name != nil {
		assignments = append(assignments, fmt.Sprintf("name = %s", s.bind(len(args)+1)))
		args = append(args, *patch.Name)
	}
	if patch.Type != nil {
		assignments = append(assignments, fmt.Sprintf("type = %s", s.bind(len(args)+1)))
		args = append(args, string(*patch.Type))
	}
	if patch.ExternalID != nil {
		assignments = append(assignments, fmt.Sprintf("external_id = %s", s.bind(len(args)+1)))
		args = append(args, *patch.ExternalID)
	}
	if patch.ClearExternalID {
		assignments = append(assignments, "external_id = NULL")
	}
	if patch.SetMetadata {
		assignments = append(assignments, fmt.Sprintf("metadata = %s", s.bind(len(args)+1)))
		args = append(args, mustJSON(patch.Metadata))
	}

	if len(assignments) == 0 {
		return s.GetEntity(ctx, id)
	}

	args = append(args, id.String())
	query := fmt.Sprintf("UPDATE entities SET %s WHERE id = %s", strings.Join(assignments, ", "), s.bind(len(args)))
	result, err := s.runner.ExecContext(ctx, query, args...)
	if err != nil {
		return elephas.Entity{}, s.wrapExecError("update entity", err)
	}
	if err := ensureRowsAffected(result, "entity", id.String()); err != nil {
		return elephas.Entity{}, err
	}

	return s.GetEntity(ctx, id)
}

func (s *Store) DeleteEntity(ctx context.Context, id uuid.UUID) error {
	result, err := s.runner.ExecContext(ctx, fmt.Sprintf("DELETE FROM entities WHERE id = %s", s.bind(1)), id.String())
	if err != nil {
		return s.wrapExecError("delete entity", err)
	}
	return ensureRowsAffected(result, "entity", id.String())
}

func (s *Store) ListEntities(ctx context.Context, filter elephas.EntityFilter) (elephas.Page[elephas.Entity], error) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)

	if filter.Name != "" {
		clauses = append(clauses, fmt.Sprintf("lower(e.name) LIKE lower(%s)", s.bind(len(args)+1)))
		args = append(args, "%"+filter.Name+"%")
	}
	if filter.Type != nil {
		clauses = append(clauses, fmt.Sprintf("e.type = %s", s.bind(len(args)+1)))
		args = append(args, string(*filter.Type))
	}
	if filter.ExternalID != "" {
		clauses = append(clauses, fmt.Sprintf("e.external_id = %s", s.bind(len(args)+1)))
		args = append(args, filter.ExternalID)
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	total, err := s.count(ctx, "entities e", where, args)
	if err != nil {
		return elephas.Page[elephas.Entity]{}, err
	}

	limit := normalizeLimit(filter.Limit, 50, 500)
	offset := normalizeOffset(filter.Offset)
	args = append(args, limit, offset)

	query := fmt.Sprintf(
		`SELECT %s FROM entities e %s ORDER BY e.updated_at DESC LIMIT %s OFFSET %s`,
		s.entitySelect("e"),
		where,
		s.bind(len(args)-1),
		s.bind(len(args)),
	)

	rows, err := s.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return elephas.Page[elephas.Entity]{}, s.wrapExecError("list entities", err)
	}
	defer rows.Close()

	items, err := scanEntities(rows)
	if err != nil {
		return elephas.Page[elephas.Entity]{}, err
	}

	return elephas.Page[elephas.Entity]{
		Data:    items,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(items) < total,
	}, nil
}

func (s *Store) CreateRelationship(ctx context.Context, params elephas.CreateRelationshipParams) (elephas.Relationship, error) {
	id := uuid.New()
	query := fmt.Sprintf(
		`INSERT INTO relationships (id, from_entity_id, to_entity_id, type, weight, metadata)
		 VALUES (%s, %s, %s, %s, %s, %s)`,
		s.bind(1), s.bind(2), s.bind(3), s.bind(4), s.bind(5), s.bind(6),
	)

	if _, err := s.runner.ExecContext(
		ctx,
		query,
		id.String(),
		params.FromEntityID.String(),
		params.ToEntityID.String(),
		params.Type,
		nullableFloat64(params.Weight),
		mustJSON(params.Metadata),
	); err != nil {
		return elephas.Relationship{}, s.wrapExecError("create relationship", err)
	}

	return s.GetRelationship(ctx, id)
}

func (s *Store) GetRelationship(ctx context.Context, id uuid.UUID) (elephas.Relationship, error) {
	query := fmt.Sprintf(`SELECT %s FROM relationships r WHERE r.id = %s`, s.relationshipSelect("r"), s.bind(1))
	row := s.runner.QueryRowContext(ctx, query, id.String())
	relationship, err := scanRelationship(row)
	if err != nil {
		return elephas.Relationship{}, s.wrapNotFound("relationship", id.String(), err)
	}
	return relationship, nil
}

func (s *Store) DeleteRelationship(ctx context.Context, id uuid.UUID) error {
	result, err := s.runner.ExecContext(ctx, fmt.Sprintf("DELETE FROM relationships WHERE id = %s", s.bind(1)), id.String())
	if err != nil {
		return s.wrapExecError("delete relationship", err)
	}
	return ensureRowsAffected(result, "relationship", id.String())
}

func (s *Store) ListRelationships(ctx context.Context, filter elephas.RelationshipFilter) (elephas.Page[elephas.Relationship], error) {
	clauses := make([]string, 0, 3)
	args := make([]any, 0, 3)

	if filter.FromEntityID != nil {
		clauses = append(clauses, fmt.Sprintf("r.from_entity_id = %s", s.bind(len(args)+1)))
		args = append(args, filter.FromEntityID.String())
	}
	if filter.ToEntityID != nil {
		clauses = append(clauses, fmt.Sprintf("r.to_entity_id = %s", s.bind(len(args)+1)))
		args = append(args, filter.ToEntityID.String())
	}
	if filter.Type != "" {
		clauses = append(clauses, fmt.Sprintf("r.type = %s", s.bind(len(args)+1)))
		args = append(args, filter.Type)
	}

	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}

	total, err := s.count(ctx, "relationships r", where, args)
	if err != nil {
		return elephas.Page[elephas.Relationship]{}, err
	}

	limit := normalizeLimit(filter.Limit, 50, 500)
	offset := normalizeOffset(filter.Offset)
	args = append(args, limit, offset)

	query := fmt.Sprintf(
		`SELECT %s FROM relationships r %s ORDER BY r.created_at DESC LIMIT %s OFFSET %s`,
		s.relationshipSelect("r"),
		where,
		s.bind(len(args)-1),
		s.bind(len(args)),
	)

	rows, err := s.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return elephas.Page[elephas.Relationship]{}, s.wrapExecError("list relationships", err)
	}
	defer rows.Close()

	items, err := scanRelationships(rows)
	if err != nil {
		return elephas.Page[elephas.Relationship]{}, err
	}

	return elephas.Page[elephas.Relationship]{
		Data:    items,
		Total:   total,
		Limit:   limit,
		Offset:  offset,
		HasMore: offset+len(items) < total,
	}, nil
}

func (s *Store) RelationshipExists(ctx context.Context, fromEntityID, toEntityID uuid.UUID, relationshipType string) (bool, error) {
	query := fmt.Sprintf(
		`SELECT COUNT(1) FROM relationships
		 WHERE from_entity_id = %s AND to_entity_id = %s AND type = %s`,
		s.bind(1), s.bind(2), s.bind(3),
	)
	var count int
	if err := s.runner.QueryRowContext(ctx, query, fromEntityID.String(), toEntityID.String(), relationshipType).Scan(&count); err != nil {
		return false, s.wrapExecError("relationship exists", err)
	}
	return count > 0, nil
}

func (s *Store) GetEntityContext(ctx context.Context, entityID uuid.UUID, depth int) (elephas.EntityContext, error) {
	entity, err := s.GetEntity(ctx, entityID)
	if err != nil {
		return elephas.EntityContext{}, err
	}

	memories, err := s.listAllMemoriesForEntity(ctx, entityID)
	if err != nil {
		return elephas.EntityContext{}, err
	}

	contextResult := elephas.EntityContext{
		Entity:   entity,
		Memories: memories,
	}

	if depth <= 0 {
		return contextResult, nil
	}

	visitedEntities := map[uuid.UUID]struct{}{entityID: {}}
	visitedRelationships := map[uuid.UUID]struct{}{}
	frontier := []uuid.UUID{entityID}

	for hop := 0; hop < depth; hop++ {
		nextFrontier := make([]uuid.UUID, 0)
		for _, currentID := range frontier {
			relationships, err := s.listRelationshipsForEntity(ctx, currentID)
			if err != nil {
				return elephas.EntityContext{}, err
			}

			for _, relationship := range relationships {
				if _, seen := visitedRelationships[relationship.ID]; seen {
					continue
				}
				visitedRelationships[relationship.ID] = struct{}{}

				relatedID := relationship.ToEntityID
				if relationship.ToEntityID == currentID {
					relatedID = relationship.FromEntityID
				}

				relatedEntity, err := s.GetEntity(ctx, relatedID)
				if err != nil {
					return elephas.EntityContext{}, err
				}

				contextResult.Relationships = append(contextResult.Relationships, elephas.ResolvedRelationship{
					Relationship:  relationship,
					RelatedEntity: relatedEntity,
				})

				if _, seen := visitedEntities[relatedID]; !seen {
					visitedEntities[relatedID] = struct{}{}
					nextFrontier = append(nextFrontier, relatedID)
				}
			}
		}
		frontier = nextFrontier
	}

	return contextResult, nil
}

func (s *Store) FindPath(ctx context.Context, fromEntityID, toEntityID uuid.UUID, maxDepth int) ([]elephas.PathNode, error) {
	if maxDepth <= 0 {
		maxDepth = 1
	}
	if maxDepth > 6 {
		maxDepth = 6
	}

	if fromEntityID == toEntityID {
		entity, err := s.GetEntity(ctx, fromEntityID)
		if err != nil {
			return nil, err
		}
		return []elephas.PathNode{{Entity: entity}}, nil
	}

	queue := []uuid.UUID{fromEntityID}
	visited := map[uuid.UUID]struct{}{fromEntityID: {}}
	depths := map[uuid.UUID]int{fromEntityID: 0}
	prev := map[uuid.UUID]predecessor{}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		if depths[current] >= maxDepth {
			continue
		}

		relationships, err := s.listAllOutgoingRelationshipsForEntity(ctx, current)
		if err != nil {
			return nil, err
		}

		for _, relationship := range relationships {
			next := relationship.ToEntityID
			if _, seen := visited[next]; seen {
				continue
			}
			visited[next] = struct{}{}
			depths[next] = depths[current] + 1
			prev[next] = predecessor{entity: current, relationship: relationship}

			if next == toEntityID {
				return s.buildPath(ctx, fromEntityID, toEntityID, prev)
			}

			queue = append(queue, next)
		}
	}

	return nil, elephas.NewError(elephas.ErrorCodeNotFound, "path not found", map[string]any{
		"from_entity_id": fromEntityID.String(),
		"to_entity_id":   toEntityID.String(),
	})
}

func (s *Store) SearchMemories(ctx context.Context, query elephas.SearchQuery) ([]elephas.MemorySearchResult, error) {
	if strings.TrimSpace(query.Query) == "" {
		return nil, elephas.NewError(elephas.ErrorCodeInvalidRequest, "search query cannot be empty", nil)
	}

	limit := normalizeLimit(query.Limit, s.searchDefaultLimit, s.searchMaxLimit)
	sqlQuery, args := s.searchQuery(query, limit)
	rows, err := s.runner.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, s.wrapExecError("search memories", err)
	}
	defer rows.Close()

	results := make([]elephas.MemorySearchResult, 0)
	for rows.Next() {
		memory, score, err := scanMemoryWithScore(rows)
		if err != nil {
			return nil, err
		}

		result := elephas.MemorySearchResult{
			Memory: memory,
			Score:  score,
		}

		if query.IncludeEntityContext {
			entityContext, err := s.GetEntityContext(ctx, memory.EntityID, 1)
			if err != nil {
				return nil, err
			}
			result.Entity = &entityContext.Entity
			result.Relationships = entityContext.Relationships
		}

		results = append(results, result)
	}

	return results, rows.Err()
}

func (s *Store) CreateIngestSource(ctx context.Context, params elephas.CreateIngestSourceParams) (elephas.IngestSource, error) {
	id := uuid.New()
	query := fmt.Sprintf(
		`INSERT INTO ingest_sources (id, raw_text, subject_entity_id, extractor_model, resolution_plan)
		 VALUES (%s, %s, %s, %s, %s)`,
		s.bind(1), s.bind(2), s.bind(3), s.bind(4), s.bind(5),
	)

	if _, err := s.runner.ExecContext(ctx, query, id.String(), params.RawText, nullableUUID(params.SubjectEntityID), params.ExtractorModel, mustJSON(params.ResolutionPlan)); err != nil {
		return elephas.IngestSource{}, s.wrapExecError("create ingest source", err)
	}

	return s.GetIngestSource(ctx, id)
}

func (s *Store) GetIngestSource(ctx context.Context, id uuid.UUID) (elephas.IngestSource, error) {
	query := fmt.Sprintf(`SELECT %s FROM ingest_sources i WHERE i.id = %s`, s.ingestSourceSelect("i"), s.bind(1))
	row := s.runner.QueryRowContext(ctx, query, id.String())
	source, err := scanIngestSource(row)
	if err != nil {
		return elephas.IngestSource{}, s.wrapNotFound("ingest source", id.String(), err)
	}
	return source, nil
}

func (s *Store) Stats(ctx context.Context) (elephas.Stats, error) {
	entityCount, err := s.simpleCount(ctx, "entities")
	if err != nil {
		return elephas.Stats{}, err
	}
	memoryCount, err := s.simpleCount(ctx, "memories")
	if err != nil {
		return elephas.Stats{}, err
	}
	relationshipCount, err := s.simpleCount(ctx, "relationships")
	if err != nil {
		return elephas.Stats{}, err
	}
	ingestCount, err := s.simpleCount(ctx, "ingest_sources")
	if err != nil {
		return elephas.Stats{}, err
	}

	return elephas.Stats{
		EntityCount:       entityCount,
		MemoryCount:       memoryCount,
		RelationshipCount: relationshipCount,
		IngestSourceCount: ingestCount,
		Backend:           s.backend,
	}, nil
}

func (s *Store) buildPath(ctx context.Context, fromEntityID, toEntityID uuid.UUID, prev map[uuid.UUID]predecessor) ([]elephas.PathNode, error) {
	chain := []struct {
		entityID     uuid.UUID
		relationship *elephas.Relationship
	}{{entityID: toEntityID}}

	current := toEntityID
	for current != fromEntityID {
		step, ok := prev[current]
		if !ok {
			return nil, elephas.NewError(elephas.ErrorCodeNotFound, "path not found", nil)
		}
		relationship := step.relationship
		chain = append(chain, struct {
			entityID     uuid.UUID
			relationship *elephas.Relationship
		}{
			entityID:     step.entity,
			relationship: &relationship,
		})
		current = step.entity
	}

	path := make([]elephas.PathNode, 0, len(chain))
	for i := len(chain) - 1; i >= 0; i-- {
		entity, err := s.GetEntity(ctx, chain[i].entityID)
		if err != nil {
			return nil, err
		}

		var relationship *elephas.Relationship
		if i < len(chain)-1 {
			relationship = chain[i].relationship
		}

		path = append(path, elephas.PathNode{
			Entity:       entity,
			Relationship: relationship,
		})
	}

	return path, nil
}

func (s *Store) listRelationshipsForEntity(ctx context.Context, entityID uuid.UUID) ([]elephas.Relationship, error) {
	query := fmt.Sprintf(
		`SELECT %s FROM relationships r
		 WHERE r.from_entity_id = %s OR r.to_entity_id = %s
		 ORDER BY r.created_at DESC`,
		s.relationshipSelect("r"),
		s.bind(1),
		s.bind(2),
	)

	rows, err := s.runner.QueryContext(ctx, query, entityID.String(), entityID.String())
	if err != nil {
		return nil, s.wrapExecError("list relationships for entity", err)
	}
	defer rows.Close()

	return scanRelationships(rows)
}

func (s *Store) listAllOutgoingRelationshipsForEntity(ctx context.Context, entityID uuid.UUID) ([]elephas.Relationship, error) {
	const pageSize = 500

	relationships := make([]elephas.Relationship, 0)
	for offset := 0; ; offset += pageSize {
		page, err := s.ListRelationships(ctx, elephas.RelationshipFilter{
			FromEntityID: &entityID,
			Limit:        pageSize,
			Offset:       offset,
		})
		if err != nil {
			return nil, err
		}
		relationships = append(relationships, page.Data...)
		if len(page.Data) < pageSize {
			break
		}
	}
	return relationships, nil
}

func (s *Store) listAllMemoriesForEntity(ctx context.Context, entityID uuid.UUID) ([]elephas.Memory, error) {
	const pageSize = 500

	memories := make([]elephas.Memory, 0)
	for offset := 0; ; offset += pageSize {
		page, err := s.ListMemories(ctx, elephas.MemoryFilter{
			EntityID:       &entityID,
			IncludeExpired: true,
			Limit:          pageSize,
			Offset:         offset,
		})
		if err != nil {
			return nil, err
		}
		memories = append(memories, page.Data...)
		if len(page.Data) < pageSize {
			break
		}
	}
	return memories, nil
}

func (s *Store) memoryWhere(filter elephas.MemoryFilter) (string, []any) {
	clauses := make([]string, 0, 4)
	args := make([]any, 0, 4)

	if filter.EntityID != nil {
		clauses = append(clauses, fmt.Sprintf("m.entity_id = %s", s.bind(len(args)+1)))
		args = append(args, filter.EntityID.String())
	}
	if filter.Category != nil {
		clauses = append(clauses, fmt.Sprintf("m.category = %s", s.bind(len(args)+1)))
		args = append(args, string(*filter.Category))
	}
	if !filter.IncludeExpired {
		clauses = append(clauses, s.activePredicate("m.expires_at"))
	}
	if filter.Since != nil {
		clauses = append(clauses, fmt.Sprintf("m.updated_at >= %s", s.bind(len(args)+1)))
		args = append(args, filter.Since.UTC())
	}

	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

func (s *Store) searchQuery(query elephas.SearchQuery, limit int) (string, []any) {
	args := make([]any, 0, 6)
	clauses := make([]string, 0, 4)

	if s.backend == "sqlite" {
		args = append(args, sqliteFTSQuery(query.Query))
		clauses = append(clauses, fmt.Sprintf("memories_fts MATCH %s", s.bind(len(args))))
	} else {
		args = append(args, query.Query)
		clauses = append(clauses, fmt.Sprintf("m.search_vec @@ websearch_to_tsquery('english', %s)", s.bind(len(args))))
	}

	if query.EntityID != nil {
		args = append(args, query.EntityID.String())
		clauses = append(clauses, fmt.Sprintf("m.entity_id = %s", s.bind(len(args))))
	}
	if len(query.Categories) > 0 {
		parts := make([]string, 0, len(query.Categories))
		for _, category := range query.Categories {
			args = append(args, string(category))
			parts = append(parts, s.bind(len(args)))
		}
		clauses = append(clauses, fmt.Sprintf("m.category IN (%s)", strings.Join(parts, ", ")))
	}
	if !query.IncludeExpired {
		clauses = append(clauses, s.activePredicate("m.expires_at"))
	}

	args = append(args, limit)
	where := "WHERE " + strings.Join(clauses, " AND ")

	if s.backend == "sqlite" {
		return fmt.Sprintf(
			`SELECT %s, -bm25(memories_fts) AS score
			 FROM memories_fts
			 JOIN memories m ON m.id = memories_fts.memory_id
			 %s
			 ORDER BY score DESC
			 LIMIT %s`,
			s.memorySelect("m"),
			where,
			s.bind(len(args)),
		), args
	}

	return fmt.Sprintf(
		`SELECT %s, ts_rank_cd(m.search_vec, websearch_to_tsquery('english', %s)) AS score
		 FROM memories m
		 %s
		 ORDER BY score DESC, m.updated_at DESC
		 LIMIT %s`,
		s.memorySelect("m"),
		s.bind(1),
		where,
		s.bind(len(args)),
	), args
}

func sqliteFTSQuery(query string) string {
	terms := splitSearchTerms(query)
	if len(terms) == 0 {
		return `""`
	}

	parts := make([]string, 0, len(terms))
	for _, term := range terms {
		if term == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " AND ")
}

func splitSearchTerms(query string) []string {
	terms := make([]string, 0, 4)
	var current strings.Builder
	inQuotes := false

	flush := func() {
		if current.Len() == 0 {
			return
		}
		terms = append(terms, strings.TrimSpace(current.String()))
		current.Reset()
	}

	for _, r := range query {
		switch {
		case r == '"':
			if inQuotes {
				inQuotes = false
				flush()
				continue
			}
			flush()
			inQuotes = true
		case unicode.IsSpace(r) && !inQuotes:
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()

	return terms
}

func (s *Store) simpleCount(ctx context.Context, table string) (int64, error) {
	var count int64
	query := fmt.Sprintf("SELECT COUNT(1) FROM %s", table)
	if err := s.runner.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, s.wrapExecError("count "+table, err)
	}
	return count, nil
}

func (s *Store) count(ctx context.Context, table, where string, args []any) (int, error) {
	var count int
	query := fmt.Sprintf("SELECT COUNT(1) FROM %s %s", table, where)
	if err := s.runner.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, s.wrapExecError("count "+table, err)
	}
	return count, nil
}

func (s *Store) bind(n int) string {
	if s.backend == "sqlite" {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}

func (s *Store) activePredicate(column string) string {
	if s.backend == "sqlite" {
		return fmt.Sprintf("(%s IS NULL OR %s > strftime('%%Y-%%m-%%dT%%H:%%M:%%fZ', 'now'))", column, column)
	}
	return fmt.Sprintf("(%s IS NULL OR %s > NOW())", column, column)
}

func (s *Store) memorySelect(alias string) string {
	return strings.Join([]string{
		s.idExpr(alias, "id"),
		s.idExpr(alias, "entity_id"),
		alias + ".content",
		alias + ".category",
		alias + ".confidence",
		s.idExpr(alias, "source_id"),
		s.timeExpr(alias, "created_at"),
		s.timeExpr(alias, "updated_at"),
		s.timeExpr(alias, "expires_at"),
		s.jsonExpr(alias, "metadata"),
	}, ", ")
}

func (s *Store) entitySelect(alias string) string {
	return strings.Join([]string{
		s.idExpr(alias, "id"),
		alias + ".name",
		alias + ".type",
		alias + ".external_id",
		s.timeExpr(alias, "created_at"),
		s.timeExpr(alias, "updated_at"),
		s.jsonExpr(alias, "metadata"),
	}, ", ")
}

func (s *Store) relationshipSelect(alias string) string {
	return strings.Join([]string{
		s.idExpr(alias, "id"),
		s.idExpr(alias, "from_entity_id"),
		s.idExpr(alias, "to_entity_id"),
		alias + ".type",
		alias + ".weight",
		s.timeExpr(alias, "created_at"),
		s.jsonExpr(alias, "metadata"),
	}, ", ")
}

func (s *Store) ingestSourceSelect(alias string) string {
	return strings.Join([]string{
		s.idExpr(alias, "id"),
		alias + ".raw_text",
		s.idExpr(alias, "subject_entity_id"),
		alias + ".extractor_model",
		s.jsonExpr(alias, "resolution_plan"),
		s.timeExpr(alias, "created_at"),
	}, ", ")
}

func (s *Store) idExpr(alias, column string) string {
	if s.backend == "sqlite" {
		return alias + "." + column
	}
	return fmt.Sprintf("%s.%s::text", alias, column)
}

func (s *Store) timeExpr(alias, column string) string {
	if s.backend == "sqlite" {
		return alias + "." + column
	}
	return fmt.Sprintf("CASE WHEN %s.%s IS NULL THEN NULL ELSE to_char(%s.%s AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.US\"Z\"') END", alias, column, alias, column)
}

func (s *Store) jsonExpr(alias, column string) string {
	if s.backend == "sqlite" {
		return alias + "." + column
	}
	return fmt.Sprintf("%s.%s::text", alias, column)
}

func (s *Store) wrapExecError(action string, err error) error {
	return elephas.WrapError(elephas.ErrorCodeStore, action, err, nil)
}

func (s *Store) wrapNotFound(kind, identifier string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return elephas.NewError(elephas.ErrorCodeNotFound, fmt.Sprintf("%s not found", kind), map[string]any{
			"id": identifier,
		})
	}
	return s.wrapExecError("get "+kind, err)
}

func normalizeLimit(limit, fallback, max int) int {
	if limit <= 0 {
		limit = fallback
	}
	if limit > max {
		limit = max
	}
	return limit
}

func normalizeSearchLimits(defaultLimit, maxLimit int) (int, int) {
	if defaultLimit <= 0 {
		defaultLimit = defaultSearchDefaultLimit
	}
	if maxLimit <= 0 {
		maxLimit = defaultSearchMaxLimit
	}
	if defaultLimit > maxLimit {
		defaultLimit = maxLimit
	}
	return defaultLimit, maxLimit
}

func (s *Store) SearchLimits() (int, int) {
	return s.searchDefaultLimit, s.searchMaxLimit
}

func normalizeOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func nullableUUID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableFloat64(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
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

func ensureRowsAffected(result sql.Result, kind, identifier string) error {
	rowsAffected, err := result.RowsAffected()
	if err == nil && rowsAffected == 0 {
		return elephas.NewError(elephas.ErrorCodeNotFound, fmt.Sprintf("%s not found", kind), map[string]any{
			"id": identifier,
		})
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMemory(row rowScanner) (elephas.Memory, error) {
	var (
		id, entityID         string
		content, category    string
		confidence           float64
		sourceID, createdAt  sql.NullString
		updatedAt, expiresAt sql.NullString
		metadata             string
	)

	if err := row.Scan(&id, &entityID, &content, &category, &confidence, &sourceID, &createdAt, &updatedAt, &expiresAt, &metadata); err != nil {
		return elephas.Memory{}, err
	}

	parsedID, err := uuid.Parse(id)
	if err != nil {
		return elephas.Memory{}, err
	}
	parsedEntityID, err := uuid.Parse(entityID)
	if err != nil {
		return elephas.Memory{}, err
	}

	result := elephas.Memory{
		ID:         parsedID,
		EntityID:   parsedEntityID,
		Content:    content,
		Category:   elephas.MemoryCategory(category),
		Confidence: confidence,
		CreatedAt:  parseTimestamp(createdAt.String),
		UpdatedAt:  parseTimestamp(updatedAt.String),
		ExpiresAt:  parseNullableTimestamp(expiresAt),
		Metadata:   parseJSONMap(metadata),
	}
	if sourceID.Valid && sourceID.String != "" {
		parsedSourceID, err := uuid.Parse(sourceID.String)
		if err != nil {
			return elephas.Memory{}, err
		}
		result.SourceID = &parsedSourceID
	}

	return result, nil
}

func scanMemories(rows *sql.Rows) ([]elephas.Memory, error) {
	items := make([]elephas.Memory, 0)
	for rows.Next() {
		item, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanMemoryWithScore(row rowScanner) (elephas.Memory, float64, error) {
	var (
		id, entityID         string
		content, category    string
		confidence           float64
		sourceID, createdAt  sql.NullString
		updatedAt, expiresAt sql.NullString
		metadata             string
		score                float64
	)

	if err := row.Scan(&id, &entityID, &content, &category, &confidence, &sourceID, &createdAt, &updatedAt, &expiresAt, &metadata, &score); err != nil {
		return elephas.Memory{}, 0, err
	}

	memory, err := scanMemory(scanMemoryRow{
		values: []any{id, entityID, content, category, confidence, sourceID, createdAt, updatedAt, expiresAt, metadata},
	})
	if err != nil {
		return elephas.Memory{}, 0, err
	}

	return memory, score, nil
}

func scanEntity(row rowScanner) (elephas.Entity, error) {
	var (
		id, name, entityType string
		externalID           sql.NullString
		createdAt, updatedAt sql.NullString
		metadata             string
	)

	if err := row.Scan(&id, &name, &entityType, &externalID, &createdAt, &updatedAt, &metadata); err != nil {
		return elephas.Entity{}, err
	}

	parsedID, err := uuid.Parse(id)
	if err != nil {
		return elephas.Entity{}, err
	}

	entity := elephas.Entity{
		ID:        parsedID,
		Name:      name,
		Type:      elephas.EntityType(entityType),
		CreatedAt: parseTimestamp(createdAt.String),
		UpdatedAt: parseTimestamp(updatedAt.String),
		Metadata:  parseJSONMap(metadata),
	}
	if externalID.Valid && externalID.String != "" {
		value := externalID.String
		entity.ExternalID = &value
	}

	return entity, nil
}

func scanEntities(rows *sql.Rows) ([]elephas.Entity, error) {
	items := make([]elephas.Entity, 0)
	for rows.Next() {
		item, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanRelationship(row rowScanner) (elephas.Relationship, error) {
	var (
		id, fromID, toID string
		relType          string
		weight           sql.NullFloat64
		createdAt        sql.NullString
		metadata         string
	)

	if err := row.Scan(&id, &fromID, &toID, &relType, &weight, &createdAt, &metadata); err != nil {
		return elephas.Relationship{}, err
	}

	parsedID, err := uuid.Parse(id)
	if err != nil {
		return elephas.Relationship{}, err
	}
	parsedFromID, err := uuid.Parse(fromID)
	if err != nil {
		return elephas.Relationship{}, err
	}
	parsedToID, err := uuid.Parse(toID)
	if err != nil {
		return elephas.Relationship{}, err
	}

	relationship := elephas.Relationship{
		ID:           parsedID,
		FromEntityID: parsedFromID,
		ToEntityID:   parsedToID,
		Type:         relType,
		CreatedAt:    parseTimestamp(createdAt.String),
		Metadata:     parseJSONMap(metadata),
	}
	if weight.Valid {
		value := weight.Float64
		relationship.Weight = &value
	}

	return relationship, nil
}

func scanRelationships(rows *sql.Rows) ([]elephas.Relationship, error) {
	items := make([]elephas.Relationship, 0)
	for rows.Next() {
		item, err := scanRelationship(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanIngestSource(row rowScanner) (elephas.IngestSource, error) {
	var (
		id              string
		rawText         string
		subjectEntityID sql.NullString
		extractorModel  string
		resolutionPlan  string
		createdAt       sql.NullString
	)

	if err := row.Scan(&id, &rawText, &subjectEntityID, &extractorModel, &resolutionPlan, &createdAt); err != nil {
		return elephas.IngestSource{}, err
	}

	parsedID, err := uuid.Parse(id)
	if err != nil {
		return elephas.IngestSource{}, err
	}

	source := elephas.IngestSource{
		ID:             parsedID,
		RawText:        rawText,
		ExtractorModel: extractorModel,
		CreatedAt:      parseTimestamp(createdAt.String),
		ResolutionPlan: parseResolutionPlan(resolutionPlan),
	}
	if subjectEntityID.Valid && subjectEntityID.String != "" {
		parsedSubject, err := uuid.Parse(subjectEntityID.String)
		if err != nil {
			return elephas.IngestSource{}, err
		}
		source.SubjectEntityID = &parsedSubject
	}

	return source, nil
}

func parseTimestamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	layouts := []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999-07:00", "2006-01-02 15:04:05.999999Z07:00"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

func parseNullableTimestamp(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	parsed := parseTimestamp(value.String)
	return &parsed
}

func parseJSONMap(value string) map[string]any {
	if value == "" {
		return map[string]any{}
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return map[string]any{}
	}
	if parsed == nil {
		return map[string]any{}
	}
	return parsed
}

func parseResolutionPlan(value string) elephas.ResolutionPlan {
	var plan elephas.ResolutionPlan
	if err := json.Unmarshal([]byte(value), &plan); err != nil {
		return elephas.ResolutionPlan{}
	}
	return plan
}

type scanMemoryRow struct {
	values []any
}

func (s scanMemoryRow) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return fmt.Errorf("scan destination mismatch: %d != %d", len(dest), len(s.values))
	}

	for i := range dest {
		switch pointer := dest[i].(type) {
		case *string:
			*pointer = s.values[i].(string)
		case *float64:
			*pointer = s.values[i].(float64)
		case *sql.NullString:
			*pointer = s.values[i].(sql.NullString)
		default:
			return fmt.Errorf("unsupported scan destination %T", dest[i])
		}
	}
	return nil
}
