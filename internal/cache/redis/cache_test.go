package redis

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/google/uuid"
	redisgo "github.com/redis/go-redis/v9"
)

func TestCacheRoundTripAgainstRealRedis(t *testing.T) {
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server binary not available")
	}

	port, cleanupServer := startRedisServer(t, binary)
	defer cleanupServer()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cache, err := New(ctx, fmt.Sprintf("redis://127.0.0.1:%d/0", port), time.Minute)
	if err != nil {
		t.Fatalf("new cache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	entityID := uuid.New()
	expected := elephas.EntityContext{
		Entity: elephas.Entity{ID: entityID, Name: "Charlie", Type: elephas.EntityTypePerson},
	}

	if err := cache.SetEntityContext(ctx, entityID, 2, expected); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, ok, err := cache.GetEntityContext(ctx, entityID, 2)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !ok {
		t.Fatalf("expected cache hit")
	}
	if got.Entity.ID != expected.Entity.ID {
		t.Fatalf("expected round trip entity id")
	}

	if err := cache.DeleteEntityContext(ctx, entityID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := cache.GetEntityContext(ctx, entityID, 2); err != nil || ok {
		t.Fatalf("expected cache miss after delete, got ok=%v err=%v", ok, err)
	}
}

func TestCacheHandlesInvalidPayloads(t *testing.T) {
	binary, err := exec.LookPath("redis-server")
	if err != nil {
		t.Skip("redis-server binary not available")
	}

	port, cleanupServer := startRedisServer(t, binary)
	defer cleanupServer()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client := redisgo.NewClient(&redisgo.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port)})
	t.Cleanup(func() { _ = client.Close() })

	entityID := uuid.New()
	key := cacheKey(entityID, 1)
	if err := client.Set(ctx, key, "{not-json", time.Minute).Err(); err != nil {
		t.Fatalf("seed invalid payload: %v", err)
	}

	cache := &Cache{client: client, ttl: time.Minute}
	if _, _, err := cache.GetEntityContext(ctx, entityID, 1); err == nil {
		t.Fatalf("expected invalid json to fail")
	}
}

func startRedisServer(t *testing.T, binary string) (int, func()) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	dir := t.TempDir()
	args := []string{
		"--save", "",
		"--appendonly", "no",
		"--bind", "127.0.0.1",
		"--port", fmt.Sprintf("%d", port),
		"--dir", dir,
	}
	cmd := exec.Command(binary, args...)
	logFile, err := os.CreateTemp(dir, "redis-*.log")
	if err != nil {
		t.Fatalf("create log file: %v", err)
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start redis-server: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := redisgo.NewClient(&redisgo.Options{Addr: fmt.Sprintf("127.0.0.1:%d", port)})
	if err := waitForRedis(waitCtx, client); err != nil {
		_ = client.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		t.Fatalf("wait for redis: %v", err)
	}
	_ = client.Close()

	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return port, cleanup
}

func waitForRedis(ctx context.Context, client *redisgo.Client) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := client.Ping(ctx).Err(); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
