package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/charliewilco/elephas"
	rediscache "github.com/charliewilco/elephas/internal/cache/redis"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/extractor/openai"
	"github.com/charliewilco/elephas/internal/httpapi"
	"github.com/charliewilco/elephas/internal/migrate"
	"github.com/charliewilco/elephas/internal/observability"
	"github.com/charliewilco/elephas/internal/store/age"
	"github.com/charliewilco/elephas/internal/store/postgres"
	"github.com/charliewilco/elephas/internal/store/sqlite"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	logger := observability.NewLogger(slog.LevelInfo)
	logger.Info("starting elephas", "component", "bootstrap", "backend", cfg.DB.Backend)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, db, err := openStore(ctx, cfg)
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer db.Close()

	if err := migrate.NewRunner(db, cfg.DB.Backend).Run(ctx); err != nil {
		log.Fatalf("run migrations: %v", err)
	}

	serviceOptions := []elephas.ServiceOption{
		elephas.WithLogger(logger),
		elephas.WithResolveThreshold(cfg.Resolve.Threshold),
	}

	if cfg.Extraction.Endpoint != "" {
		extractor := openai.New(openai.Config{
			Endpoint:      cfg.Extraction.Endpoint,
			APIKey:        cfg.Extraction.APIKey,
			DefaultModel:  cfg.Extraction.Model,
			Timeout:       cfg.Extraction.Timeout,
			MaxCandidates: cfg.Extraction.MaxCandidates,
		})
		serviceOptions = append(serviceOptions, elephas.WithExtractor(extractor))
	}

	if cfg.Cache.DSN != "" {
		cache, err := rediscache.New(ctx, cfg.Cache.DSN, cfg.Cache.TTL)
		if err != nil {
			log.Fatalf("open redis cache: %v", err)
		}
		serviceOptions = append(serviceOptions, elephas.WithContextCache(cache))
	}

	service := elephas.NewService(store, serviceOptions...)
	defer func() {
		if err := service.Close(); err != nil {
			logger.Error("close service", "component", "bootstrap", "error", err)
		}
	}()

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Server.Port),
		Handler:           httpapi.New(service, cfg, logger),
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown failed", "component", "bootstrap", "error", err)
		}
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server failed", "component", "bootstrap", "error", err)
		log.Fatalf("listen and serve: %v", err)
	}
}

func openStore(ctx context.Context, cfg config.Config) (elephas.Store, *sql.DB, error) {
	switch cfg.DB.Backend {
	case "postgres":
		return postgres.Open(ctx, cfg.DB)
	case "sqlite":
		return sqlite.Open(ctx, cfg.DB)
	case "age":
		return age.Open(ctx, cfg.DB)
	default:
		return nil, nil, fmt.Errorf("unsupported backend %q", cfg.DB.Backend)
	}
}
