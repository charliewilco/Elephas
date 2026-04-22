package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/charliewilco/elephas"
	"github.com/google/uuid"
)

func TestCacheStoresAndInvalidatesEntityContext(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)

	cache, err := New(ctx, "redis://"+server.Addr(), time.Minute)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer cache.Close()

	entityID := uuid.New()
	now := time.Now().UTC()
	contextValue := elephas.EntityContext{
		Entity: elephas.Entity{
			ID:        entityID,
			Name:      "Cached",
			Type:      elephas.EntityTypePerson,
			CreatedAt: now,
			UpdatedAt: now,
			Metadata:  map[string]any{"source": "test"},
		},
		Memories: []elephas.Memory{
			{
				ID:         uuid.New(),
				EntityID:   entityID,
				Content:    "note",
				Category:   elephas.MemoryCategoryFact,
				Confidence: 0.6,
				CreatedAt:  now,
				UpdatedAt:  now,
				Metadata:   map[string]any{"source": "test"},
			},
		},
	}

	if err := cache.SetEntityContext(ctx, entityID, 2, contextValue); err != nil {
		t.Fatalf("set entity context: %v", err)
	}

	got, ok, err := cache.GetEntityContext(ctx, entityID, 2)
	if err != nil {
		t.Fatalf("get entity context: %v", err)
	}
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if got.Entity.ID != entityID || got.Entity.Name != "Cached" {
		t.Fatalf("unexpected cached entity: %+v", got.Entity)
	}
	if len(got.Memories) != 1 || got.Memories[0].Content != "note" {
		t.Fatalf("expected cached memories to round-trip, got %+v", got.Memories)
	}

	if err := cache.DeleteEntityContext(ctx, entityID); err != nil {
		t.Fatalf("delete entity context: %v", err)
	}

	_, ok, err = cache.GetEntityContext(ctx, entityID, 2)
	if err != nil {
		t.Fatalf("get entity context after delete: %v", err)
	}
	if ok {
		t.Fatalf("expected cache miss after delete")
	}
}
