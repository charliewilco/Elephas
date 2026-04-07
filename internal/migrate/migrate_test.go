package migrate

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRunAppliesSQLiteMigrationsAndIsIdempotent(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runner := NewRunner(db, "sqlite")
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(1) FROM elephas_migrations`).Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one applied migration, got %d", count)
	}

	for _, table := range []string{"entities", "memories", "relationships", "ingest_sources"} {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(1) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if exists != 1 {
			t.Fatalf("expected table %s to exist", table)
		}
	}
}

func TestLoadMigrationsSkipsAGEProjectionForPostgresBackend(t *testing.T) {
	migrations, err := loadMigrations("postgres")
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}

	for _, migration := range migrations {
		if migration.name == "0002_age_projection.sql" {
			t.Fatalf("postgres migrations should not include AGE projection")
		}
	}
}
