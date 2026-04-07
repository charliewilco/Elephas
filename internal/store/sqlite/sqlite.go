package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/store/sqlstore"
	_ "modernc.org/sqlite"
)

func Open(ctx context.Context, cfg config.DatabaseConfig) (elephas.Store, *sql.DB, error) {
	dsn := cfg.DSN
	if dsn == "" {
		dsn = "file:elephas.db"
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys(1)") {
		separator := "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
		dsn += separator + "_pragma=foreign_keys(1)"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, err
	}

	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxIdleTime(5 * time.Minute)

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnTimeout)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	return sqlstore.New(db, "sqlite"), db, nil
}
