package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	// Bootstrap order:
	//   config -> store -> migrations -> optional reconciler -> service -> HTTP server.
	// Each step fails fast because later stages depend on earlier initialization.
	logger := observability.NewLogger(slog.LevelInfo)
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		fatalBootstrap(logger, "load config", err, os.Exit)
	}

	logger.Info("starting elephas", "component", "bootstrap", "backend", cfg.DB.Backend)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, db, err := openStore(ctx, cfg)
	if err != nil {
		fatalBootstrap(logger, "open store", err, os.Exit)
	}
	defer db.Close()

	runner := migrate.NewRunner(db, cfg.DB.Backend)
	if err := runner.Run(ctx); err != nil {
		fatalBootstrap(logger, "run migrations", err, os.Exit)
	}
	if reconciler, ok := store.(interface{ Reconcile(context.Context) error }); ok {
		if err := reconciler.Reconcile(ctx); err != nil {
			fatalBootstrap(logger, "reconcile graph projection", err, os.Exit)
		}
	}

	serviceOptions := []elephas.ServiceOption{
		elephas.WithLogger(logger),
		elephas.WithResolveThreshold(cfg.Resolve.Threshold),
	}

	if cfg.Extraction.Endpoint != "" {
		// Extractor is optional; when absent the API still supports manual CRUD.
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
		// Cache is a best-effort acceleration layer for entity-context responses.
		cache, err := rediscache.New(ctx, cfg.Cache.DSN, cfg.Cache.TTL)
		if err != nil {
			fatalBootstrap(logger, "open redis cache", err, os.Exit)
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
		Handler:           httpapi.New(service, cfg, logger, runner),
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		// Graceful shutdown gives in-flight requests a bounded window to complete.
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown failed", "component", "bootstrap", "error", err)
		}
	}()

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatalBootstrap(logger, "listen and serve", err, os.Exit)
	}
}

func openStore(ctx context.Context, cfg config.Config) (elephas.Store, *sql.DB, error) {
	switch cfg.DB.Backend {
	case "postgres":
		return postgres.Open(ctx, cfg.DB, cfg.Search)
	case "sqlite":
		return sqlite.Open(ctx, cfg.DB, cfg.Search)
	case "age":
		return age.Open(ctx, cfg.DB, cfg.Search)
	default:
		return nil, nil, fmt.Errorf("unsupported backend %q", cfg.DB.Backend)
	}
}

func fatalBootstrap(logger *slog.Logger, action string, err error, exit func(int)) {
	logger.Error(action, "component", "bootstrap", "error", err)
	exit(1)
}
