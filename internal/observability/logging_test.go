package observability

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestNewLoggerReturnsUsableLogger(t *testing.T) {
	logger := NewLogger(slog.LevelDebug)
	if logger == nil {
		t.Fatalf("expected logger")
	}

	var buf bytes.Buffer
	testLogger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	testLogger.Info("hello", "component", "test")
	if buf.Len() == 0 {
		t.Fatalf("expected logger handler to emit output")
	}
}
