package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadReadsDotEnvAndResolvesReferences(t *testing.T) {
	tempDir := t.TempDir()
	envFile := filepath.Join(tempDir, ".env")
	content := []byte("ELEPHAS_DB_BACKEND=sqlite\nELEPHAS_DB_DSN=:memory:\nELEPHAS_EXTRACTOR_ENDPOINT=https://example.com/v1/chat/completions\nELEPHAS_EXTRACTOR_API_KEY=$SECRET_KEY\n")
	if err := os.WriteFile(envFile, content, 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Setenv("SECRET_KEY", "secret-value")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DB.Backend != "sqlite" {
		t.Fatalf("expected sqlite backend, got %s", cfg.DB.Backend)
	}
	if cfg.DB.DSN != ":memory:" {
		t.Fatalf("expected sqlite dsn from .env, got %s", cfg.DB.DSN)
	}
	if cfg.Extraction.APIKey != "secret-value" {
		t.Fatalf("expected env reference to resolve")
	}
}

func TestLoadPrefersExistingEnvironmentOverDotEnv(t *testing.T) {
	tempDir := t.TempDir()
	envFile := filepath.Join(tempDir, ".env")
	content := []byte("ELEPHAS_DB_BACKEND=postgres\nELEPHAS_DB_DSN=from-dotenv\nELEPHAS_RESOLVE_THRESHOLD=0.9\n")
	if err := os.WriteFile(envFile, content, 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(previousWD) })
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Setenv("ELEPHAS_DB_BACKEND", "sqlite")
	t.Setenv("ELEPHAS_DB_DSN", ":memory:")
	t.Setenv("ELEPHAS_RESOLVE_THRESHOLD", "0.7")
	t.Setenv("ELEPHAS_EXTRACTOR_ENDPOINT", "")
	t.Setenv("ELEPHAS_EXTRACTOR_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DB.Backend != "sqlite" {
		t.Fatalf("expected environment backend to win, got %s", cfg.DB.Backend)
	}
	if cfg.Resolve.Threshold != 0.7 {
		t.Fatalf("expected environment threshold to win, got %v", cfg.Resolve.Threshold)
	}
}

func TestConfigValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name:    "invalid backend",
			cfg:     Config{DB: DatabaseConfig{Backend: "oracle"}},
			wantErr: "ELEPHAS_DB_BACKEND must be one of postgres, age, sqlite",
		},
		{
			name:    "missing dsn for postgres",
			cfg:     Config{DB: DatabaseConfig{Backend: "postgres"}, Resolve: ResolveConfig{Threshold: 0.85}},
			wantErr: "ELEPHAS_DB_DSN is required for postgres and age backends",
		},
		{
			name: "extractor endpoint without api key",
			cfg: Config{
				DB:         DatabaseConfig{Backend: "sqlite"},
				Extraction: ExtractionConfig{Endpoint: "https://example.com"},
				Resolve:    ResolveConfig{Threshold: 0.85},
			},
			wantErr: "ELEPHAS_EXTRACTOR_API_KEY is required when ELEPHAS_EXTRACTOR_ENDPOINT is set",
		},
		{
			name: "threshold too low",
			cfg: Config{
				DB:      DatabaseConfig{Backend: "sqlite"},
				Resolve: ResolveConfig{Threshold: 0},
			},
			wantErr: "ELEPHAS_RESOLVE_THRESHOLD must be between 0 and 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err == nil || err.Error() != tt.wantErr {
				t.Fatalf("expected %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestLoadDefaultsSQLiteDSNWhenEmpty(t *testing.T) {
	t.Setenv("ELEPHAS_DB_BACKEND", "sqlite")
	t.Setenv("ELEPHAS_DB_DSN", "")
	t.Setenv("ELEPHAS_RESOLVE_THRESHOLD", "0.85")
	t.Setenv("ELEPHAS_EXTRACTOR_ENDPOINT", "")
	t.Setenv("ELEPHAS_EXTRACTOR_API_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.DB.DSN != "file:elephas.db" {
		t.Fatalf("expected default sqlite dsn, got %s", cfg.DB.DSN)
	}
	if cfg.DB.ConnTimeout != 5*time.Second {
		t.Fatalf("expected default timeout, got %v", cfg.DB.ConnTimeout)
	}
}
