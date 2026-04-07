package observability

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"testing"
)

func TestNewLoggerEmitsStructuredJSON(t *testing.T) {
	originalStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdout pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = originalStdout
	}()

	logger := NewLogger(slog.LevelDebug)
	if logger == nil {
		t.Fatalf("expected logger")
	}

	logger.Info("hello", "component", "observability", "request_id", "req-123")

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read log output: %v", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		t.Fatalf("expected logger output")
	}

	var payload map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &payload); err != nil {
		t.Fatalf("decode json log: %v\nbody: %s", err, string(data))
	}
	if payload["msg"] != "hello" {
		t.Fatalf("expected log message to be preserved, got %#v", payload["msg"])
	}
	if payload["component"] != "observability" {
		t.Fatalf("expected component field to be preserved, got %#v", payload["component"])
	}
	if payload["request_id"] != "req-123" {
		t.Fatalf("expected request_id field to be preserved, got %#v", payload["request_id"])
	}
}
