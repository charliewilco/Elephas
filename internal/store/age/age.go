package age

import (
	"context"
	"database/sql"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/store/sqlstore"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Open returns a Postgres-backed store configured for AGE deployments.
// The relational tables remain the source of truth; AGE migrations set up
// the graph extension for deployments that want cypher-based traversal later.
func Open(ctx context.Context, cfg config.DatabaseConfig) (elephas.Store, *sql.DB, error) {
	db, err := sql.Open("pgx", cfg.DSN)
	if err != nil {
		return nil, nil, err
	}

	db.SetMaxOpenConns(cfg.MaxConns)
	db.SetMaxIdleConns(cfg.IdleConns)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, err
	}

	return sqlstore.New(db, "age"), db, nil
}
