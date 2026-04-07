package elephas_test

import (
	"context"
	"testing"

	"github.com/charliewilco/elephas"
	"github.com/google/uuid"
)

func TestServiceValidationErrors(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	service := elephas.NewService(store)

	entity, err := service.CreateEntity(context.Background(), elephas.CreateEntityParams{
		Name: "Charlie",
		Type: elephas.EntityTypePerson,
	})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "create memory missing entity",
			run: func() error {
				_, err := service.CreateMemory(context.Background(), elephas.CreateMemoryParams{
					Content:    "hello",
					Category:   elephas.MemoryCategoryFact,
					Confidence: 1,
				})
				return err
			},
		},
		{
			name: "create memory invalid confidence",
			run: func() error {
				_, err := service.CreateMemory(context.Background(), elephas.CreateMemoryParams{
					EntityID:   entity.ID,
					Content:    "hello",
					Category:   elephas.MemoryCategoryFact,
					Confidence: 2,
				})
				return err
			},
		},
		{
			name: "search memories empty query",
			run: func() error {
				_, err := service.SearchMemories(context.Background(), elephas.SearchQuery{})
				return err
			},
		},
		{
			name: "entity context invalid depth",
			run: func() error {
				_, err := service.GetEntityContext(context.Background(), entity.ID, 4)
				return err
			},
		},
		{
			name: "path invalid depth",
			run: func() error {
				_, err := service.FindPath(context.Background(), entity.ID, uuid.New(), 0)
				return err
			},
		},
		{
			name: "create entity missing name",
			run: func() error {
				_, err := service.CreateEntity(context.Background(), elephas.CreateEntityParams{
					Type: elephas.EntityTypePerson,
				})
				return err
			},
		},
		{
			name: "create relationship invalid weight",
			run: func() error {
				weight := 1.2
				_, err := service.CreateRelationship(context.Background(), elephas.CreateRelationshipParams{
					FromEntityID: entity.ID,
					ToEntityID:   uuid.New(),
					Type:         "knows",
					Weight:       &weight,
				})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil {
				t.Fatalf("expected error")
			}

			apiErr, ok := err.(*elephas.Error)
			if !ok {
				t.Fatalf("expected elephas.Error, got %T", err)
			}
			if apiErr.Code != elephas.ErrorCodeInvalidRequest {
				t.Fatalf("expected invalid_request, got %s", apiErr.Code)
			}
		})
	}
}

func TestServiceCacheInvalidationOnMutations(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	cache := &recordingCache{}
	service := elephas.NewService(store, elephas.WithContextCache(cache))
	ctx := context.Background()

	alice, err := service.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Alice", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := service.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Bob", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}

	memory, err := service.CreateMemory(ctx, elephas.CreateMemoryParams{
		EntityID:   alice.ID,
		Content:    "Likes tea",
		Category:   elephas.MemoryCategoryFact,
		Confidence: 0.8,
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}

	content := "Likes coffee"
	if _, err := service.UpdateMemory(ctx, memory.ID, elephas.MemoryPatch{Content: &content}); err != nil {
		t.Fatalf("update memory: %v", err)
	}

	relationship, err := service.CreateRelationship(ctx, elephas.CreateRelationshipParams{
		FromEntityID: alice.ID,
		ToEntityID:   bob.ID,
		Type:         "knows",
	})
	if err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	if err := service.DeleteRelationship(ctx, relationship.ID); err != nil {
		t.Fatalf("delete relationship: %v", err)
	}
	if err := service.DeleteMemory(ctx, memory.ID); err != nil {
		t.Fatalf("delete memory: %v", err)
	}

	if _, err := service.GetEntityContext(ctx, alice.ID, 1); err != nil {
		t.Fatalf("get entity context: %v", err)
	}
	if _, err := service.GetEntityContext(ctx, alice.ID, 1); err != nil {
		t.Fatalf("get cached entity context: %v", err)
	}

	if cache.setCount == 0 {
		t.Fatalf("expected cache writes")
	}
	if cache.deleteCount < 4 {
		t.Fatalf("expected multiple cache invalidations, got %d", cache.deleteCount)
	}
}

type recordingCache struct {
	values      map[string]elephas.EntityContext
	deleteCount int
	setCount    int
}

func (c *recordingCache) GetEntityContext(_ context.Context, entityID uuid.UUID, depth int) (elephas.EntityContext, bool, error) {
	if c.values == nil {
		c.values = map[string]elephas.EntityContext{}
	}
	value, ok := c.values[cacheKey(entityID, depth)]
	return value, ok, nil
}

func (c *recordingCache) SetEntityContext(_ context.Context, entityID uuid.UUID, depth int, value elephas.EntityContext) error {
	if c.values == nil {
		c.values = map[string]elephas.EntityContext{}
	}
	c.setCount++
	c.values[cacheKey(entityID, depth)] = value
	return nil
}

func (c *recordingCache) DeleteEntityContext(_ context.Context, entityID uuid.UUID) error {
	if c.values == nil {
		c.values = map[string]elephas.EntityContext{}
	}
	c.deleteCount++
	for depth := 0; depth <= 3; depth++ {
		delete(c.values, cacheKey(entityID, depth))
	}
	return nil
}

func (c *recordingCache) Close() error { return nil }

func cacheKey(entityID uuid.UUID, depth int) string {
	return entityID.String() + ":" + string(rune('0'+depth))
}
