package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/store/sqlstore"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func Open(ctx context.Context, cfg config.DatabaseConfig, searchCfg config.SearchConfig) (elephas.Store, *sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, nil, err
	}

	configurePool(db, cfg)
	if err := pingWithTimeout(ctx, db, cfg.ConnTimeout); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	return sqlstore.New(
		db,
		"postgres",
		sqlstore.WithSearchLimits(searchCfg.DefaultLimit, searchCfg.MaxLimit),
	), db, nil
}

func configurePool(db *sql.DB, cfg config.DatabaseConfig) {
	db.SetMaxOpenConns(cfg.MaxConns)
	db.SetMaxIdleConns(cfg.IdleConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
}

func pingWithTimeout(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}
