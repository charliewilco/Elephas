package elephas

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

func (s *Service) CreateMemory(ctx context.Context, params CreateMemoryParams) (Memory, error) {
	if err := validateCreateMemory(params); err != nil {
		return Memory{}, err
	}

	memory, err := s.store.CreateMemory(ctx, params)
	if err != nil {
		return Memory{}, err
	}

	s.invalidateEntityContext(ctx, memory.EntityID)
	return memory, nil
}

func (s *Service) GetMemory(ctx context.Context, id uuid.UUID) (Memory, error) {
	return s.store.GetMemory(ctx, id)
}

func (s *Service) UpdateMemory(ctx context.Context, id uuid.UUID, patch MemoryPatch) (Memory, error) {
	if err := validateMemoryPatch(patch); err != nil {
		return Memory{}, err
	}

	current, err := s.store.GetMemory(ctx, id)
	if err != nil {
		return Memory{}, err
	}

	updated, err := s.store.UpdateMemory(ctx, id, patch)
	if err != nil {
		return Memory{}, err
	}

	s.invalidateEntityContext(ctx, current.EntityID)
	return updated, nil
}

func (s *Service) DeleteMemory(ctx context.Context, id uuid.UUID) error {
	current, err := s.store.GetMemory(ctx, id)
	if err != nil {
		return err
	}

	if err := s.store.DeleteMemory(ctx, id); err != nil {
		return err
	}

	s.invalidateEntityContext(ctx, current.EntityID)
	return nil
}

func (s *Service) ListMemories(ctx context.Context, filter MemoryFilter) (Page[Memory], error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	return s.store.ListMemories(ctx, filter)
}

func (s *Service) SearchMemories(ctx context.Context, query SearchQuery) ([]MemorySearchResult, error) {
	if strings.TrimSpace(query.Query) == "" {
		return nil, NewError(ErrorCodeInvalidRequest, "query is required", nil)
	}
	return s.store.SearchMemories(ctx, query)
}

func (s *Service) CreateEntity(ctx context.Context, params CreateEntityParams) (Entity, error) {
	if err := validateCreateEntity(params); err != nil {
		return Entity{}, err
	}
	return s.store.CreateEntity(ctx, params)
}

func (s *Service) GetEntity(ctx context.Context, id uuid.UUID) (Entity, error) {
	return s.store.GetEntity(ctx, id)
}

func (s *Service) UpdateEntity(ctx context.Context, id uuid.UUID, patch EntityPatch) (Entity, error) {
	if err := validateEntityPatch(patch); err != nil {
		return Entity{}, err
	}

	entity, err := s.store.UpdateEntity(ctx, id, patch)
	if err != nil {
		return Entity{}, err
	}

	s.invalidateEntityContext(ctx, id)
	return entity, nil
}

func (s *Service) DeleteEntity(ctx context.Context, id uuid.UUID) error {
	if err := s.store.DeleteEntity(ctx, id); err != nil {
		return err
	}

	s.invalidateEntityContext(ctx, id)
	return nil
}

func (s *Service) ListEntities(ctx context.Context, filter EntityFilter) (Page[Entity], error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	return s.store.ListEntities(ctx, filter)
}

func (s *Service) CreateRelationship(ctx context.Context, params CreateRelationshipParams) (Relationship, error) {
	if err := validateCreateRelationship(params); err != nil {
		return Relationship{}, err
	}

	relationship, err := s.store.CreateRelationship(ctx, params)
	if err != nil {
		return Relationship{}, err
	}

	s.invalidateEntityContext(ctx, params.FromEntityID, params.ToEntityID)
	return relationship, nil
}

func (s *Service) GetRelationship(ctx context.Context, id uuid.UUID) (Relationship, error) {
	return s.store.GetRelationship(ctx, id)
}

func (s *Service) DeleteRelationship(ctx context.Context, id uuid.UUID) error {
	relationship, err := s.store.GetRelationship(ctx, id)
	if err != nil {
		return err
	}

	if err := s.store.DeleteRelationship(ctx, id); err != nil {
		return err
	}

	s.invalidateEntityContext(ctx, relationship.FromEntityID, relationship.ToEntityID)
	return nil
}

func (s *Service) ListRelationships(ctx context.Context, filter RelationshipFilter) (Page[Relationship], error) {
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	return s.store.ListRelationships(ctx, filter)
}

func (s *Service) GetEntityContext(ctx context.Context, entityID uuid.UUID, depth int) (EntityContext, error) {
	if depth < 0 {
		return EntityContext{}, NewError(ErrorCodeInvalidRequest, "depth must be >= 0", nil)
	}
	if depth > 3 {
		return EntityContext{}, NewError(ErrorCodeInvalidRequest, "depth must be <= 3", nil)
	}

	if s.cache != nil {
		cached, ok, err := s.cache.GetEntityContext(ctx, entityID, depth)
		if err != nil {
			s.loggerFor(ctx, "cache").Warn("entity context cache read failed", "entity_id", entityID.String(), "error", err)
		}
		if ok {
			return cached, nil
		}
	}

	result, err := s.store.GetEntityContext(ctx, entityID, depth)
	if err != nil {
		return EntityContext{}, err
	}

	if s.cache != nil {
		if err := s.cache.SetEntityContext(ctx, entityID, depth, result); err != nil {
			s.loggerFor(ctx, "cache").Warn("entity context cache write failed", "entity_id", entityID.String(), "error", err)
		}
	}

	return result, nil
}

func (s *Service) FindPath(ctx context.Context, fromEntityID, toEntityID uuid.UUID, maxDepth int) ([]PathNode, error) {
	if maxDepth <= 0 {
		return nil, NewError(ErrorCodeInvalidRequest, "max_depth must be >= 1", nil)
	}
	if maxDepth > 6 {
		return nil, NewError(ErrorCodeInvalidRequest, "max_depth must be <= 6", nil)
	}
	return s.store.FindPath(ctx, fromEntityID, toEntityID, maxDepth)
}

func (s *Service) GetIngestSource(ctx context.Context, id uuid.UUID) (IngestSource, error) {
	return s.store.GetIngestSource(ctx, id)
}

func (s *Service) Stats(ctx context.Context) (Stats, error) {
	return s.store.Stats(ctx)
}

func (s *Service) invalidateEntityContext(ctx context.Context, entityIDs ...uuid.UUID) {
	if s.cache == nil {
		return
	}

	seen := make(map[uuid.UUID]struct{}, len(entityIDs))
	for _, entityID := range entityIDs {
		if entityID == uuid.Nil {
			continue
		}
		if _, ok := seen[entityID]; ok {
			continue
		}
		seen[entityID] = struct{}{}
		if err := s.cache.DeleteEntityContext(ctx, entityID); err != nil {
			s.loggerFor(ctx, "cache").Warn("entity context cache invalidation failed", "entity_id", entityID.String(), "error", err)
		}
	}
}

func validateCreateMemory(params CreateMemoryParams) error {
	if params.EntityID == uuid.Nil {
		return NewError(ErrorCodeInvalidRequest, "entity_id is required", nil)
	}
	if strings.TrimSpace(params.Content) == "" {
		return NewError(ErrorCodeInvalidRequest, "content is required", nil)
	}
	if err := validateMemoryCategory(params.Category); err != nil {
		return err
	}
	if params.Confidence < 0 || params.Confidence > 1 {
		return NewError(ErrorCodeInvalidRequest, "confidence must be between 0 and 1", nil)
	}
	return nil
}

func validateMemoryPatch(patch MemoryPatch) error {
	if patch.Content != nil && strings.TrimSpace(*patch.Content) == "" {
		return NewError(ErrorCodeInvalidRequest, "content cannot be empty", nil)
	}
	if patch.Confidence != nil && (*patch.Confidence < 0 || *patch.Confidence > 1) {
		return NewError(ErrorCodeInvalidRequest, "confidence must be between 0 and 1", nil)
	}
	return nil
}

func validateCreateEntity(params CreateEntityParams) error {
	if strings.TrimSpace(params.Name) == "" {
		return NewError(ErrorCodeInvalidRequest, "name is required", nil)
	}
	if err := validateEntityType(params.Type); err != nil {
		return err
	}
	return nil
}

func validateEntityPatch(patch EntityPatch) error {
	if patch.Name != nil && strings.TrimSpace(*patch.Name) == "" {
		return NewError(ErrorCodeInvalidRequest, "name cannot be empty", nil)
	}
	if patch.Type != nil {
		if err := validateEntityType(*patch.Type); err != nil {
			return err
		}
	}
	return nil
}

func validateCreateRelationship(params CreateRelationshipParams) error {
	if params.FromEntityID == uuid.Nil || params.ToEntityID == uuid.Nil {
		return NewError(ErrorCodeInvalidRequest, "from_entity_id and to_entity_id are required", nil)
	}
	if strings.TrimSpace(params.Type) == "" {
		return NewError(ErrorCodeInvalidRequest, "type is required", nil)
	}
	if params.Weight != nil && (*params.Weight < 0 || *params.Weight > 1) {
		return NewError(ErrorCodeInvalidRequest, "weight must be between 0 and 1", nil)
	}
	return nil
}

func validateMemoryCategory(category MemoryCategory) error {
	switch category {
	case MemoryCategoryPreference, MemoryCategoryFact, MemoryCategoryRelationship, MemoryCategoryEvent, MemoryCategoryInstruction:
		return nil
	default:
		return NewError(ErrorCodeInvalidRequest, fmt.Sprintf("invalid memory category %q", category), nil)
	}
}

func validateEntityType(entityType EntityType) error {
	switch entityType {
	case EntityTypePerson, EntityTypeOrganization, EntityTypePlace, EntityTypeConcept, EntityTypeObject, EntityTypeAgent:
		return nil
	default:
		return NewError(ErrorCodeInvalidRequest, fmt.Sprintf("invalid entity type %q", entityType), nil)
	}
}
