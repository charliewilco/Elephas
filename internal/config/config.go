package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DB          DatabaseConfig
	Server      ServerConfig
	Extraction  ExtractionConfig
	Search      SearchConfig
	Cache       CacheConfig
	Resolve     ResolveConfig
	Environment string
}

type DatabaseConfig struct {
	Backend     string
	DSN         string
	MaxConns    int
	IdleConns   int
	ConnTimeout time.Duration
}

type ServerConfig struct {
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	APIKey       string
}

type ExtractionConfig struct {
	Endpoint      string
	APIKey        string
	Model         string
	Timeout       time.Duration
	MaxCandidates int
}

type SearchConfig struct {
	DefaultLimit int
	MaxLimit     int
}

type CacheConfig struct {
	DSN string
	TTL time.Duration
}

type ResolveConfig struct {
	Threshold float64
}

func Load() (Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return Config{}, err
	}

	cfg := Config{
		DB: DatabaseConfig{
			Backend:     getEnv("ELEPHAS_DB_BACKEND", "postgres"),
			DSN:         resolveEnvReference(os.Getenv("ELEPHAS_DB_DSN")),
			MaxConns:    getEnvInt("ELEPHAS_DB_MAX_CONNS", 25),
			IdleConns:   getEnvInt("ELEPHAS_DB_IDLE_CONNS", 5),
			ConnTimeout: time.Duration(getEnvInt("ELEPHAS_DB_CONN_TIMEOUT_MS", 5000)) * time.Millisecond,
		},
		Server: ServerConfig{
			Port:         getEnvInt("ELEPHAS_HTTP_PORT", 8080),
			ReadTimeout:  time.Duration(getEnvInt("ELEPHAS_HTTP_READ_TIMEOUT_MS", 30000)) * time.Millisecond,
			WriteTimeout: time.Duration(getEnvInt("ELEPHAS_HTTP_WRITE_TIMEOUT_MS", 30000)) * time.Millisecond,
			APIKey:       os.Getenv("ELEPHAS_API_KEY"),
		},
		Extraction: ExtractionConfig{
			Endpoint:      os.Getenv("ELEPHAS_EXTRACTOR_ENDPOINT"),
			APIKey:        resolveEnvReference(os.Getenv("ELEPHAS_EXTRACTOR_API_KEY")),
			Model:         getEnv("ELEPHAS_EXTRACTOR_MODEL", "gpt-4o"),
			Timeout:       time.Duration(getEnvInt("ELEPHAS_EXTRACTOR_TIMEOUT_MS", 30000)) * time.Millisecond,
			MaxCandidates: getEnvInt("ELEPHAS_EXTRACTOR_MAX_CANDIDATES", 50),
		},
		Search: SearchConfig{
			DefaultLimit: getEnvInt("ELEPHAS_SEARCH_DEFAULT_LIMIT", 20),
			MaxLimit:     getEnvInt("ELEPHAS_SEARCH_MAX_LIMIT", 100),
		},
		Cache: CacheConfig{
			DSN: os.Getenv("ELEPHAS_CACHE_DSN"),
			TTL: time.Duration(getEnvInt("ELEPHAS_CACHE_TTL_SECONDS", 300)) * time.Second,
		},
		Resolve: ResolveConfig{
			Threshold: getEnvFloat("ELEPHAS_RESOLVE_THRESHOLD", 0.85),
		},
		Environment: getEnv("ELEPHAS_ENV", "development"),
	}

	if cfg.DB.Backend == "sqlite" && cfg.DB.DSN == "" {
		cfg.DB.DSN = "file:elephas.db"
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c Config) Validate() error {
	switch c.DB.Backend {
	case "postgres", "age", "sqlite":
	default:
		return fmt.Errorf("ELEPHAS_DB_BACKEND must be one of postgres, age, sqlite")
	}

	if c.DB.Backend != "sqlite" && c.DB.DSN == "" {
		return errors.New("ELEPHAS_DB_DSN is required for postgres and age backends")
	}

	if c.Extraction.Endpoint != "" && c.Extraction.APIKey == "" {
		return errors.New("ELEPHAS_EXTRACTOR_API_KEY is required when ELEPHAS_EXTRACTOR_ENDPOINT is set")
	}

	if c.Resolve.Threshold <= 0 || c.Resolve.Threshold > 1 {
		return errors.New("ELEPHAS_RESOLVE_THRESHOLD must be between 0 and 1")
	}

	return nil
}

func loadDotEnv(path string) error {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("invalid .env line: %q", line)
		}

		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}

	return scanner.Err()
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getEnvFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func resolveEnvReference(value string) string {
	if len(value) > 1 && strings.HasPrefix(value, "$") {
		return os.Getenv(strings.TrimPrefix(value, "$"))
	}
	return value
}
