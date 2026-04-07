package age

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/migrate"
)

func TestAGEStoreOpensWhenDSNProvided(t *testing.T) {
	dsn := os.Getenv("ELEPHAS_TEST_AGE_DSN")
	if dsn == "" {
		t.Skip("ELEPHAS_TEST_AGE_DSN is not set")
	}

	store, db, err := Open(context.Background(), config.DatabaseConfig{
		DSN:       dsn,
		MaxConns:  5,
		IdleConns: 1,
	}, config.SearchConfig{DefaultLimit: 20, MaxLimit: 100})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	defer db.Close()

	if err := migrate.NewRunner(db, "age").Run(context.Background()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}
}

func TestOpenReturnsContextErrorQuicklyWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := Open(ctx, config.DatabaseConfig{
		DSN:       "postgres://localhost:1/postgres?sslmode=disable",
		MaxConns:  1,
		IdleConns: 1,
	}, config.SearchConfig{DefaultLimit: 20, MaxLimit: 100})
	if err == nil {
		t.Fatalf("expected open to fail with cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}
