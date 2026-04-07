package sqlite

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/charliewilco/elephas/internal/config"
)

func TestOpenUsesDefaultDSNAndReturnsStore(t *testing.T) {
	_ = os.Remove("elephas.db")
	t.Cleanup(func() { _ = os.Remove("elephas.db") })

	store, db, err := Open(context.Background(), config.DatabaseConfig{
		ConnTimeout: time.Second,
	}, config.SearchConfig{DefaultLimit: 20, MaxLimit: 100})
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()
	defer db.Close()

	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("ping sqlite db: %v", err)
	}
}
