package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/charliewilco/elephas/internal/config"
)

func TestOpenUsesDefaultDSNAndReturnsStore(t *testing.T) {
	store, db, err := Open(context.Background(), config.DatabaseConfig{
		ConnTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()
	defer db.Close()

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping sqlite db: %v", err)
	}
}
