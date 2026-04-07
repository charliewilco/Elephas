package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/charliewilco/elephas/internal/config"
)

func TestFatalBootstrapLogsStructuredJSONAndExits(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	exitCode := 0
	fatalBootstrap(logger, "open store", errors.New("boom"), func(code int) {
		exitCode = code
	})

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}

	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &payload); err != nil {
		t.Fatalf("decode log payload: %v", err)
	}

	if payload["msg"] != "open store" {
		t.Fatalf("expected log message to match action, got %#v", payload["msg"])
	}
	if payload["component"] != "bootstrap" {
		t.Fatalf("expected bootstrap component, got %#v", payload["component"])
	}
	if payload["error"] != "boom" {
		t.Fatalf("expected error field to be preserved, got %#v", payload["error"])
	}
}

func TestOpenStorePassesSearchLimitsToBackend(t *testing.T) {
	store, db, err := openStore(context.Background(), config.Config{
		DB: config.DatabaseConfig{
			Backend:     "sqlite",
			DSN:         "file:" + t.Name() + "?mode=memory&cache=shared",
			ConnTimeout: time.Second,
		},
		Search: config.SearchConfig{
			DefaultLimit: 7,
			MaxLimit:     9,
		},
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	defer store.Close()

	searchConfiguredStore, ok := store.(interface{ SearchLimits() (int, int) })
	if !ok {
		t.Fatalf("expected store to expose search limits")
	}

	defaultLimit, maxLimit := searchConfiguredStore.SearchLimits()
	if defaultLimit != 7 || maxLimit != 9 {
		t.Fatalf("expected search limits 7/9, got %d/%d", defaultLimit, maxLimit)
	}
}
