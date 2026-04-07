package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

type Cache struct {
	client *redis.Client
	ttl    time.Duration
}

func New(ctx context.Context, dsn string, ttl time.Duration) (*Cache, error) {
	options, err := redis.ParseURL(dsn)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}

	return &Cache{
		client: client,
		ttl:    ttl,
	}, nil
}

func (c *Cache) GetEntityContext(ctx context.Context, entityID uuid.UUID, depth int) (elephas.EntityContext, bool, error) {
	value, err := c.client.Get(ctx, cacheKey(entityID, depth)).Result()
	if errors.Is(err, redis.Nil) {
		return elephas.EntityContext{}, false, nil
	}
	if err != nil {
		return elephas.EntityContext{}, false, err
	}

	var contextValue elephas.EntityContext
	if err := json.Unmarshal([]byte(value), &contextValue); err != nil {
		return elephas.EntityContext{}, false, err
	}

	return contextValue, true, nil
}

func (c *Cache) SetEntityContext(ctx context.Context, entityID uuid.UUID, depth int, value elephas.EntityContext) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}

	return c.client.Set(ctx, cacheKey(entityID, depth), data, c.ttl).Err()
}

func (c *Cache) DeleteEntityContext(ctx context.Context, entityID uuid.UUID) error {
	keys := []string{
		cacheKey(entityID, 0),
		cacheKey(entityID, 1),
		cacheKey(entityID, 2),
		cacheKey(entityID, 3),
	}
	return c.client.Del(ctx, keys...).Err()
}

func (c *Cache) Close() error {
	return c.client.Close()
}

func cacheKey(entityID uuid.UUID, depth int) string {
	return fmt.Sprintf("elephas:entity_context:%s:%d", entityID.String(), depth)
}
