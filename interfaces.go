package elephas

import (
	"context"

	"github.com/google/uuid"
)

type Store interface {
	Ping(ctx context.Context) error
	Close() error
	RunInTx(ctx context.Context, fn func(context.Context, Store) error) error

	CreateMemory(ctx context.Context, params CreateMemoryParams) (Memory, error)
	GetMemory(ctx context.Context, id uuid.UUID) (Memory, error)
	UpdateMemory(ctx context.Context, id uuid.UUID, patch MemoryPatch) (Memory, error)
	DeleteMemory(ctx context.Context, id uuid.UUID) error
	ListMemories(ctx context.Context, filter MemoryFilter) (Page[Memory], error)
	ListActiveMemoriesByEntityAndCategory(ctx context.Context, entityID uuid.UUID, category MemoryCategory) ([]Memory, error)

	CreateEntity(ctx context.Context, params CreateEntityParams) (Entity, error)
	GetEntity(ctx context.Context, id uuid.UUID) (Entity, error)
	GetEntityByExternalID(ctx context.Context, externalID string) (Entity, error)
	FindEntityByName(ctx context.Context, name string) (Entity, error)
	UpdateEntity(ctx context.Context, id uuid.UUID, patch EntityPatch) (Entity, error)
	DeleteEntity(ctx context.Context, id uuid.UUID) error
	ListEntities(ctx context.Context, filter EntityFilter) (Page[Entity], error)

	CreateRelationship(ctx context.Context, params CreateRelationshipParams) (Relationship, error)
	GetRelationship(ctx context.Context, id uuid.UUID) (Relationship, error)
	DeleteRelationship(ctx context.Context, id uuid.UUID) error
	ListRelationships(ctx context.Context, filter RelationshipFilter) (Page[Relationship], error)
	RelationshipExists(ctx context.Context, fromEntityID, toEntityID uuid.UUID, relationshipType string) (bool, error)

	GetEntityContext(ctx context.Context, entityID uuid.UUID, depth int) (EntityContext, error)
	FindPath(ctx context.Context, fromEntityID, toEntityID uuid.UUID, maxDepth int) ([]PathNode, error)
	SearchMemories(ctx context.Context, query SearchQuery) ([]MemorySearchResult, error)

	CreateIngestSource(ctx context.Context, params CreateIngestSourceParams) (IngestSource, error)
	GetIngestSource(ctx context.Context, id uuid.UUID) (IngestSource, error)

	Stats(ctx context.Context) (Stats, error)
}

type Extractor interface {
	Extract(ctx context.Context, request ExtractRequest) ([]CandidateMemory, error)
}

type ContextCache interface {
	GetEntityContext(ctx context.Context, entityID uuid.UUID, depth int) (EntityContext, bool, error)
	SetEntityContext(ctx context.Context, entityID uuid.UUID, depth int, value EntityContext) error
	DeleteEntityContext(ctx context.Context, entityID uuid.UUID) error
	Close() error
}
