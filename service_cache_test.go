package elephas_test

import (
	"context"
	"errors"
	"testing"

	"github.com/charliewilco/elephas"
	"github.com/google/uuid"
)

func TestEntityContextCacheFallsBackToStore(t *testing.T) {
	entityID := uuid.New()
	expected := elephas.EntityContext{
		Entity: elephas.Entity{
			ID:   entityID,
			Name: "Charlie",
			Type: elephas.EntityTypePerson,
		},
	}

	store := stubStore{
		entityContext: expected,
	}
	cache := failingCache{}
	service := elephas.NewService(store, elephas.WithContextCache(cache))

	got, err := service.GetEntityContext(context.Background(), entityID, 1)
	if err != nil {
		t.Fatalf("get entity context: %v", err)
	}
	if got.Entity.ID != expected.Entity.ID {
		t.Fatalf("expected store result to be returned")
	}
}

type failingCache struct{}

func (f failingCache) GetEntityContext(context.Context, uuid.UUID, int) (elephas.EntityContext, bool, error) {
	return elephas.EntityContext{}, false, errors.New("cache down")
}

func (f failingCache) SetEntityContext(context.Context, uuid.UUID, int, elephas.EntityContext) error {
	return errors.New("cache down")
}

func (f failingCache) DeleteEntityContext(context.Context, uuid.UUID) error {
	return errors.New("cache down")
}

func (f failingCache) Close() error { return nil }

type stubStore struct {
	entityContext elephas.EntityContext
}

func (s stubStore) Ping(context.Context) error { return nil }
func (s stubStore) Close() error               { return nil }
func (s stubStore) RunInTx(ctx context.Context, fn func(context.Context, elephas.Store) error) error {
	return fn(ctx, s)
}
func (s stubStore) CreateMemory(context.Context, elephas.CreateMemoryParams) (elephas.Memory, error) {
	return elephas.Memory{}, nil
}
func (s stubStore) GetMemory(context.Context, uuid.UUID) (elephas.Memory, error) {
	return elephas.Memory{}, nil
}
func (s stubStore) UpdateMemory(context.Context, uuid.UUID, elephas.MemoryPatch) (elephas.Memory, error) {
	return elephas.Memory{}, nil
}
func (s stubStore) DeleteMemory(context.Context, uuid.UUID) error { return nil }
func (s stubStore) ListMemories(context.Context, elephas.MemoryFilter) (elephas.Page[elephas.Memory], error) {
	return elephas.Page[elephas.Memory]{}, nil
}
func (s stubStore) ListActiveMemoriesByEntityAndCategory(context.Context, uuid.UUID, elephas.MemoryCategory) ([]elephas.Memory, error) {
	return nil, nil
}
func (s stubStore) CreateEntity(context.Context, elephas.CreateEntityParams) (elephas.Entity, error) {
	return elephas.Entity{}, nil
}
func (s stubStore) GetEntity(context.Context, uuid.UUID) (elephas.Entity, error) {
	return elephas.Entity{}, nil
}
func (s stubStore) GetEntityByExternalID(context.Context, string) (elephas.Entity, error) {
	return elephas.Entity{}, nil
}
func (s stubStore) FindEntityByName(context.Context, string) (elephas.Entity, error) {
	return elephas.Entity{}, nil
}
func (s stubStore) UpdateEntity(context.Context, uuid.UUID, elephas.EntityPatch) (elephas.Entity, error) {
	return elephas.Entity{}, nil
}
func (s stubStore) DeleteEntity(context.Context, uuid.UUID) error { return nil }
func (s stubStore) ListEntities(context.Context, elephas.EntityFilter) (elephas.Page[elephas.Entity], error) {
	return elephas.Page[elephas.Entity]{}, nil
}
func (s stubStore) CreateRelationship(context.Context, elephas.CreateRelationshipParams) (elephas.Relationship, error) {
	return elephas.Relationship{}, nil
}
func (s stubStore) GetRelationship(context.Context, uuid.UUID) (elephas.Relationship, error) {
	return elephas.Relationship{}, nil
}
func (s stubStore) DeleteRelationship(context.Context, uuid.UUID) error { return nil }
func (s stubStore) ListRelationships(context.Context, elephas.RelationshipFilter) (elephas.Page[elephas.Relationship], error) {
	return elephas.Page[elephas.Relationship]{}, nil
}
func (s stubStore) RelationshipExists(context.Context, uuid.UUID, uuid.UUID, string) (bool, error) {
	return false, nil
}
func (s stubStore) GetEntityContext(context.Context, uuid.UUID, int) (elephas.EntityContext, error) {
	return s.entityContext, nil
}
func (s stubStore) FindPath(context.Context, uuid.UUID, uuid.UUID, int) ([]elephas.PathNode, error) {
	return nil, nil
}
func (s stubStore) SearchMemories(context.Context, elephas.SearchQuery) ([]elephas.MemorySearchResult, error) {
	return nil, nil
}
func (s stubStore) CreateIngestSource(context.Context, elephas.CreateIngestSourceParams) (elephas.IngestSource, error) {
	return elephas.IngestSource{}, nil
}
func (s stubStore) GetIngestSource(context.Context, uuid.UUID) (elephas.IngestSource, error) {
	return elephas.IngestSource{}, nil
}
func (s stubStore) Stats(context.Context) (elephas.Stats, error) { return elephas.Stats{}, nil }
