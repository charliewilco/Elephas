package elephas_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/migrate"
	"github.com/charliewilco/elephas/internal/store/sqlstore"
	_ "modernc.org/sqlite"
)

func TestServiceIngestDryRunAndCommit(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	subject, err := store.CreateEntity(context.Background(), elephas.CreateEntityParams{
		Name:       "Charlie",
		Type:       elephas.EntityTypePerson,
		ExternalID: stringPointer("user-123"),
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}

	service := elephas.NewService(store, elephas.WithExtractor(fakeExtractor{candidates: []elephas.CandidateMemory{
		{
			Content:    "Prefers dark mode across all applications.",
			Category:   elephas.MemoryCategoryPreference,
			Confidence: 0.9,
			RelatedEntities: []elephas.CandidateEntity{
				{Name: "Elephas", Type: elephas.EntityTypeAgent},
			},
			RelationshipType: "uses",
		},
	}}))

	dryRun, err := service.Ingest(context.Background(), elephas.IngestRequest{
		RawText:           "Charlie prefers dark mode.",
		SubjectExternalID: "user-123",
		DryRun:            true,
	})
	if err != nil {
		t.Fatalf("dry run ingest: %v", err)
	}
	if dryRun.MemoriesCreated != 1 {
		t.Fatalf("expected one created memory in dry run, got %d", dryRun.MemoriesCreated)
	}

	memories, err := store.ListMemories(context.Background(), elephas.MemoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list memories after dry run: %v", err)
	}
	if len(memories.Data) != 0 {
		t.Fatalf("expected dry run to avoid writes, got %d memories", len(memories.Data))
	}

	committed, err := service.Ingest(context.Background(), elephas.IngestRequest{
		RawText:           "Charlie prefers dark mode.",
		SubjectExternalID: "user-123",
	})
	if err != nil {
		t.Fatalf("committed ingest: %v", err)
	}
	if committed.IngestSourceID == nil {
		t.Fatalf("expected ingest source id on committed ingest")
	}
	if len(committed.CommittedMemories) != 1 {
		t.Fatalf("expected one committed memory, got %d", len(committed.CommittedMemories))
	}

	contextValue, err := service.GetEntityContext(context.Background(), subject.ID, 1)
	if err != nil {
		t.Fatalf("get entity context: %v", err)
	}
	if len(contextValue.Relationships) != 1 {
		t.Fatalf("expected one related entity relationship, got %d", len(contextValue.Relationships))
	}
}

func TestServiceIngestPreferenceUpdatesExistingMemory(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	subject, err := store.CreateEntity(context.Background(), elephas.CreateEntityParams{
		Name: "Charlie",
		Type: elephas.EntityTypePerson,
	})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}

	original, err := store.CreateMemory(context.Background(), elephas.CreateMemoryParams{
		EntityID:   subject.ID,
		Content:    "Prefers dark mode across all applications.",
		Category:   elephas.MemoryCategoryPreference,
		Confidence: 0.9,
	})
	if err != nil {
		t.Fatalf("create original memory: %v", err)
	}

	service := elephas.NewService(store, elephas.WithExtractor(fakeExtractor{candidates: []elephas.CandidateMemory{
		{
			Content:    "Prefers light mode in the morning.",
			Category:   elephas.MemoryCategoryPreference,
			Confidence: 0.2,
			Subject: &elephas.CandidateEntity{
				Name: "Charlie",
				Type: elephas.EntityTypePerson,
			},
		},
	}}), elephas.WithResolveThreshold(0.1))

	response, err := service.Ingest(context.Background(), elephas.IngestRequest{
		RawText:         "Charlie now prefers light mode in the morning.",
		SubjectEntityID: &subject.ID,
	})
	if err != nil {
		t.Fatalf("ingest update: %v", err)
	}

	if response.MemoriesUpdated != 1 || response.MemoriesCreated != 0 {
		t.Fatalf("expected a singleton update, got %+v", response)
	}
	if len(response.CommittedMemories) != 1 || response.CommittedMemories[0].ID != original.ID {
		t.Fatalf("expected update to preserve memory identity")
	}
}

func TestServiceIngestValidationAndZeroCandidatePaths(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	t.Run("missing raw text", func(t *testing.T) {
		service := elephas.NewService(store)
		_, err := service.Ingest(context.Background(), elephas.IngestRequest{})
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("missing extractor", func(t *testing.T) {
		service := elephas.NewService(store)
		_, err := service.Ingest(context.Background(), elephas.IngestRequest{RawText: "hello"})
		if err == nil {
			t.Fatalf("expected error")
		}
	})

	t.Run("zero candidates succeeds", func(t *testing.T) {
		service := elephas.NewService(store, elephas.WithExtractor(fakeExtractor{}))
		response, err := service.Ingest(context.Background(), elephas.IngestRequest{RawText: "hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(response.ResolutionPlan.Steps) != 0 || response.IngestSourceID != nil {
			t.Fatalf("expected empty dry success response, got %+v", response)
		}
	})
}

func TestServiceIngestCreatesEntitiesAndRelationshipWithoutExplicitSubject(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	service := elephas.NewService(store, elephas.WithExtractor(fakeExtractor{candidates: []elephas.CandidateMemory{
		{
			Content:    "Works at Weave",
			Category:   elephas.MemoryCategoryFact,
			Confidence: 0.9,
			Subject: &elephas.CandidateEntity{
				Name: "Charlie",
				Type: elephas.EntityTypePerson,
			},
			RelatedEntities: []elephas.CandidateEntity{
				{Name: "Weave", Type: elephas.EntityTypeOrganization},
			},
			RelationshipType: "works_at",
		},
	}}))

	response, err := service.Ingest(context.Background(), elephas.IngestRequest{RawText: "Charlie works at Weave."})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if response.EntitiesCreated != 2 {
		t.Fatalf("expected 2 created entities, got %d", response.EntitiesCreated)
	}
	if response.RelationshipsCreated != 1 {
		t.Fatalf("expected 1 created relationship, got %d", response.RelationshipsCreated)
	}

	entities, err := store.ListEntities(context.Background(), elephas.EntityFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if len(entities.Data) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(entities.Data))
	}
}

func TestServiceIngestRelationshipCandidateMergesWhenEdgeExists(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	charlie, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Charlie", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create subject: %v", err)
	}
	weave, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Weave", Type: elephas.EntityTypeOrganization})
	if err != nil {
		t.Fatalf("create related: %v", err)
	}
	if _, err := store.CreateRelationship(ctx, elephas.CreateRelationshipParams{
		FromEntityID: charlie.ID,
		ToEntityID:   weave.ID,
		Type:         "works_at",
	}); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	service := elephas.NewService(store, elephas.WithExtractor(fakeExtractor{candidates: []elephas.CandidateMemory{
		{
			Content:    "Charlie works at Weave",
			Category:   elephas.MemoryCategoryRelationship,
			Confidence: 0.8,
			Subject: &elephas.CandidateEntity{
				Name: "Charlie",
				Type: elephas.EntityTypePerson,
			},
			RelatedEntities: []elephas.CandidateEntity{
				{Name: "Weave", Type: elephas.EntityTypeOrganization},
			},
			RelationshipType: "works_at",
		},
	}}))

	response, err := service.Ingest(ctx, elephas.IngestRequest{RawText: "Charlie works at Weave."})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if response.MemoriesMerged != 1 {
		t.Fatalf("expected merge response, got %+v", response)
	}
	if response.RelationshipsCreated != 0 {
		t.Fatalf("expected no new relationships, got %d", response.RelationshipsCreated)
	}
}

func TestServiceIngestRejectsInvalidCandidateAndMissingSubject(t *testing.T) {
	store, cleanup := newTestSQLiteStore(t)
	defer cleanup()

	tests := []struct {
		name       string
		candidates []elephas.CandidateMemory
	}{
		{
			name: "invalid confidence",
			candidates: []elephas.CandidateMemory{{
				Content:    "hello",
				Category:   elephas.MemoryCategoryFact,
				Confidence: 2,
				Subject: &elephas.CandidateEntity{
					Name: "Charlie",
					Type: elephas.EntityTypePerson,
				},
			}},
		},
		{
			name: "missing subject",
			candidates: []elephas.CandidateMemory{{
				Content:    "hello",
				Category:   elephas.MemoryCategoryFact,
				Confidence: 0.5,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := elephas.NewService(store, elephas.WithExtractor(fakeExtractor{candidates: tt.candidates}))
			if _, err := service.Ingest(context.Background(), elephas.IngestRequest{RawText: "hello"}); err == nil {
				t.Fatalf("expected error")
			}
		})
	}
}

func newTestSQLiteStore(t *testing.T) (*sqlstore.Store, func()) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	if err := migrate.NewRunner(db, "sqlite").Run(context.Background()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	store := sqlstore.New(db, "sqlite")
	return store, func() { _ = store.Close() }
}

type fakeExtractor struct {
	candidates []elephas.CandidateMemory
	err        error
}

func (f fakeExtractor) Extract(_ context.Context, _ elephas.ExtractRequest) ([]elephas.CandidateMemory, error) {
	return f.candidates, f.err
}

func stringPointer(value string) *string {
	return &value
}
