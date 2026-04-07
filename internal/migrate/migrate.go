package migrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/postgres/*.sql migrations/sqlite/*.sql
var migrations embed.FS

type Runner struct {
	db      *sql.DB
	backend string
}

func NewRunner(db *sql.DB, backend string) *Runner {
	return &Runner{db: db, backend: backend}
}

func (r *Runner) Run(ctx context.Context) error {
	entries, err := loadMigrations(r.backend)
	if err != nil {
		return err
	}

	if err := r.ensureMigrationsTable(ctx); err != nil {
		return err
	}

	for _, entry := range entries {
		applied, err := r.applied(ctx, entry.name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}

		tx, err := r.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}

		if _, err := tx.ExecContext(ctx, entry.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", entry.name, err)
		}

		recordQuery := "INSERT INTO elephas_migrations (name) VALUES (?)"
		if r.backend != "sqlite" {
			recordQuery = "INSERT INTO elephas_migrations (name) VALUES ($1)"
		}

		if _, err := tx.ExecContext(ctx, recordQuery, entry.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", entry.name, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", entry.name, err)
		}
	}

	return nil
}

func loadMigrations(backend string) ([]migration, error) {
	dir := "migrations/postgres"
	if backend == "sqlite" {
		dir = "migrations/sqlite"
	}

	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return nil, err
	}

	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if backend == "postgres" && strings.Contains(entry.Name(), "age_projection") {
			continue
		}

		content, err := fs.ReadFile(migrations, dir+"/"+entry.Name())
		if err != nil {
			return nil, err
		}

		result = append(result, migration{name: entry.Name(), sql: string(content)})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].name < result[j].name
	})

	return result, nil
}

func (r *Runner) ensureMigrationsTable(ctx context.Context) error {
	stmt := `
CREATE TABLE IF NOT EXISTS elephas_migrations (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
)`
	if r.backend != "sqlite" {
		stmt = `
CREATE TABLE IF NOT EXISTS elephas_migrations (
  id SERIAL PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`
	}

	_, err := r.db.ExecContext(ctx, stmt)
	return err
}

func (r *Runner) applied(ctx context.Context, name string) (bool, error) {
	query := "SELECT COUNT(1) FROM elephas_migrations WHERE name = ?"
	if r.backend != "sqlite" {
		query = "SELECT COUNT(1) FROM elephas_migrations WHERE name = $1"
	}

	var count int
	if err := r.db.QueryRowContext(ctx, query, name).Scan(&count); err != nil {
		return false, err
	}

	return count > 0, nil
}

type migration struct {
	name string
	sql  string
}
