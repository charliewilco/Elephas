package sqlstore

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/migrate"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func TestSQLiteStoreCRUDSearchAndTraversal(t *testing.T) {
	store, cleanup := newSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()

	charlie, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Charlie", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create charlie: %v", err)
	}
	weave, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Weave", Type: elephas.EntityTypeOrganization})
	if err != nil {
		t.Fatalf("create weave: %v", err)
	}
	austin, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Austin", Type: elephas.EntityTypePlace})
	if err != nil {
		t.Fatalf("create austin: %v", err)
	}

	relationship, err := store.CreateRelationship(ctx, elephas.CreateRelationshipParams{
		FromEntityID: charlie.ID,
		ToEntityID:   weave.ID,
		Type:         "works_at",
	})
	if err != nil {
		t.Fatalf("create relationship: %v", err)
	}
	if _, err := store.CreateRelationship(ctx, elephas.CreateRelationshipParams{
		FromEntityID: weave.ID,
		ToEntityID:   austin.ID,
		Type:         "located_in",
	}); err != nil {
		t.Fatalf("create second relationship: %v", err)
	}

	memory, err := store.CreateMemory(ctx, elephas.CreateMemoryParams{
		EntityID:   charlie.ID,
		Content:    "Prefers dark mode across all applications",
		Category:   elephas.MemoryCategoryPreference,
		Confidence: 1.0,
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}

	updated, err := store.UpdateMemory(ctx, memory.ID, elephas.MemoryPatch{
		Content:    stringPtr("Prefers light mode in the morning"),
		Confidence: floatPtr(0.8),
	})
	if err != nil {
		t.Fatalf("update memory: %v", err)
	}
	if updated.Content != "Prefers light mode in the morning" {
		t.Fatalf("expected content update to persist")
	}

	searchResults, err := store.SearchMemories(ctx, elephas.SearchQuery{Query: "light mode", Limit: 10})
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if len(searchResults) != 1 {
		t.Fatalf("expected one search result, got %d", len(searchResults))
	}

	contextValue, err := store.GetEntityContext(ctx, charlie.ID, 2)
	if err != nil {
		t.Fatalf("get entity context: %v", err)
	}
	if len(contextValue.Relationships) < 2 {
		t.Fatalf("expected depth-2 context to include relationship traversal, got %d", len(contextValue.Relationships))
	}

	path, err := store.FindPath(ctx, charlie.ID, austin.ID, 3)
	if err != nil {
		t.Fatalf("find path: %v", err)
	}
	if len(path) != 3 {
		t.Fatalf("expected path length 3, got %d", len(path))
	}

	if err := store.DeleteEntity(ctx, weave.ID); err != nil {
		t.Fatalf("delete entity: %v", err)
	}

	relationships, err := store.ListRelationships(ctx, elephas.RelationshipFilter{Limit: 10})
	if err != nil {
		t.Fatalf("list relationships: %v", err)
	}
	if len(relationships.Data) != 0 {
		t.Fatalf("expected cascade delete to remove relationships, got %d", len(relationships.Data))
	}
	if _, err := store.GetRelationship(ctx, relationship.ID); err == nil {
		t.Fatalf("expected relationship lookup to fail after cascade delete")
	}
}

func TestSQLiteStoreFiltersExpiryAndIngestSource(t *testing.T) {
	store, cleanup := newSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	entity, err := store.CreateEntity(ctx, elephas.CreateEntityParams{
		Name:       "Charlie",
		Type:       elephas.EntityTypePerson,
		ExternalID: stringPtr("user-1"),
	})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	source, err := store.CreateIngestSource(ctx, elephas.CreateIngestSourceParams{
		RawText:         "source text",
		SubjectEntityID: &entity.ID,
		ExtractorModel:  "gpt-4o",
		ResolutionPlan:  elephas.ResolutionPlan{},
	})
	if err != nil {
		t.Fatalf("create ingest source: %v", err)
	}

	past := nowMinusHour()
	if _, err := store.CreateMemory(ctx, elephas.CreateMemoryParams{
		EntityID:   entity.ID,
		Content:    "Expired memory",
		Category:   elephas.MemoryCategoryFact,
		Confidence: 0.7,
		ExpiresAt:  &past,
	}); err != nil {
		t.Fatalf("create expired memory: %v", err)
	}
	active, err := store.CreateMemory(ctx, elephas.CreateMemoryParams{
		EntityID:   entity.ID,
		Content:    "Active memory",
		Category:   elephas.MemoryCategoryFact,
		Confidence: 0.9,
		SourceID:   &source.ID,
	})
	if err != nil {
		t.Fatalf("create active memory: %v", err)
	}

	page, err := store.ListMemories(ctx, elephas.MemoryFilter{EntityID: &entity.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list active memories: %v", err)
	}
	if len(page.Data) != 1 || page.Data[0].ID != active.ID {
		t.Fatalf("expected only non-expired memories by default, got %+v", page.Data)
	}

	allPage, err := store.ListMemories(ctx, elephas.MemoryFilter{EntityID: &entity.ID, IncludeExpired: true, Limit: 10})
	if err != nil {
		t.Fatalf("list all memories: %v", err)
	}
	if len(allPage.Data) != 2 {
		t.Fatalf("expected expired memories when included, got %d", len(allPage.Data))
	}

	searchResults, err := store.SearchMemories(ctx, elephas.SearchQuery{
		Query:                "Active",
		EntityID:             &entity.ID,
		IncludeEntityContext: true,
		Limit:                10,
	})
	if err != nil {
		t.Fatalf("search memories: %v", err)
	}
	if len(searchResults) != 1 || searchResults[0].Entity == nil || searchResults[0].Entity.ID != entity.ID {
		t.Fatalf("expected entity context in search results, got %+v", searchResults)
	}

	fetchedSource, err := store.GetIngestSource(ctx, source.ID)
	if err != nil {
		t.Fatalf("get ingest source: %v", err)
	}
	if fetchedSource.ID != source.ID {
		t.Fatalf("expected ingest source to round-trip")
	}

	byExternalID, err := store.GetEntityByExternalID(ctx, "user-1")
	if err != nil {
		t.Fatalf("get entity by external id: %v", err)
	}
	if byExternalID.ID != entity.ID {
		t.Fatalf("expected external id lookup to match entity")
	}
}

func TestSQLiteStoreTransactionsFiltersAndNotFounds(t *testing.T) {
	store, cleanup := newSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	person, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Charlie", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}
	other, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Weave", Type: elephas.EntityTypeOrganization})
	if err != nil {
		t.Fatalf("create other entity: %v", err)
	}

	if err := store.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		_, err := tx.CreateMemory(ctx, elephas.CreateMemoryParams{
			EntityID:   person.ID,
			Content:    "Transactional memory",
			Category:   elephas.MemoryCategoryFact,
			Confidence: 0.5,
		})
		return err
	}); err != nil {
		t.Fatalf("transaction commit: %v", err)
	}

	committed, err := store.ListMemories(ctx, elephas.MemoryFilter{EntityID: &person.ID, IncludeExpired: true, Limit: 10})
	if err != nil {
		t.Fatalf("list committed memories: %v", err)
	}
	if len(committed.Data) != 1 {
		t.Fatalf("expected committed transaction to persist memory")
	}

	if err := store.RunInTx(ctx, func(ctx context.Context, tx elephas.Store) error {
		_, err := tx.CreateMemory(ctx, elephas.CreateMemoryParams{
			EntityID:   person.ID,
			Content:    "Rolled back memory",
			Category:   elephas.MemoryCategoryFact,
			Confidence: 0.5,
		})
		if err != nil {
			return err
		}
		return elephas.NewError(elephas.ErrorCodeStore, "force rollback", nil)
	}); err == nil {
		t.Fatalf("expected rollback error")
	}

	afterRollback, err := store.ListMemories(ctx, elephas.MemoryFilter{EntityID: &person.ID, IncludeExpired: true, Limit: 10})
	if err != nil {
		t.Fatalf("list rolled back memories: %v", err)
	}
	if len(afterRollback.Data) != 1 {
		t.Fatalf("expected rollback to preserve prior count, got %d", len(afterRollback.Data))
	}

	if _, err := store.CreateRelationship(ctx, elephas.CreateRelationshipParams{
		FromEntityID: person.ID,
		ToEntityID:   other.ID,
		Type:         "works_at",
	}); err != nil {
		t.Fatalf("create relationship: %v", err)
	}

	entityPage, err := store.ListEntities(ctx, elephas.EntityFilter{Name: "char", Limit: 10})
	if err != nil {
		t.Fatalf("list entities by name: %v", err)
	}
	if len(entityPage.Data) != 1 || entityPage.Data[0].ID != person.ID {
		t.Fatalf("expected name filter to match person")
	}

	entityType := elephas.EntityTypeOrganization
	entityPage, err = store.ListEntities(ctx, elephas.EntityFilter{Type: &entityType, Limit: 10})
	if err != nil {
		t.Fatalf("list entities by type: %v", err)
	}
	if len(entityPage.Data) != 1 || entityPage.Data[0].ID != other.ID {
		t.Fatalf("expected type filter to match organization")
	}

	relPage, err := store.ListRelationships(ctx, elephas.RelationshipFilter{FromEntityID: &person.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list relationships by from entity: %v", err)
	}
	if len(relPage.Data) != 1 {
		t.Fatalf("expected relationship filter to match one edge")
	}

	exists, err := store.RelationshipExists(ctx, person.ID, other.ID, "works_at")
	if err != nil {
		t.Fatalf("relationship exists: %v", err)
	}
	if !exists {
		t.Fatalf("expected relationship to exist")
	}

	if _, err := store.GetMemory(ctx, uuidMustParse(t, "8d260f2d-c39a-48de-99d8-540c1b1c8bdb")); err == nil {
		t.Fatalf("expected missing memory to error")
	}
	if err := store.DeleteMemory(ctx, uuidMustParse(t, "8d260f2d-c39a-48de-99d8-540c1b1c8bdb")); err == nil {
		t.Fatalf("expected deleting missing memory to error")
	}
	if _, err := store.FindPath(ctx, other.ID, person.ID, 1); err == nil {
		t.Fatalf("expected no reverse path with directed edges")
	}
}

func TestSQLiteStoreAppliesConfiguredSearchLimits(t *testing.T) {
	store, cleanup := newSQLiteStoreWithOptions(t, WithSearchLimits(2, 3))
	defer cleanup()

	ctx := context.Background()
	entity, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Charlie", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	for i := 0; i < 4; i++ {
		_, err := store.CreateMemory(ctx, elephas.CreateMemoryParams{
			EntityID:   entity.ID,
			Content:    "Prefers light mode",
			Category:   elephas.MemoryCategoryPreference,
			Confidence: 1,
		})
		if err != nil {
			t.Fatalf("create memory %d: %v", i, err)
		}
	}

	defaultLimited, err := store.SearchMemories(ctx, elephas.SearchQuery{Query: "light"})
	if err != nil {
		t.Fatalf("search with default limit: %v", err)
	}
	if len(defaultLimited) != 2 {
		t.Fatalf("expected configured default search limit of 2, got %d", len(defaultLimited))
	}

	maxLimited, err := store.SearchMemories(ctx, elephas.SearchQuery{Query: "light", Limit: 10})
	if err != nil {
		t.Fatalf("search with capped limit: %v", err)
	}
	if len(maxLimited) != 3 {
		t.Fatalf("expected configured max search limit of 3, got %d", len(maxLimited))
	}

	inRange, err := store.SearchMemories(ctx, elephas.SearchQuery{Query: "light", Limit: 1})
	if err != nil {
		t.Fatalf("search with explicit in-range limit: %v", err)
	}
	if len(inRange) != 1 {
		t.Fatalf("expected explicit search limit to be preserved, got %d", len(inRange))
	}
}

func TestSQLiteStoreFindPathPagesPastFirstRelationshipBatch(t *testing.T) {
	store, cleanup := newSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()

	root, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Root", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	target, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Target", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	if _, err := store.CreateRelationship(ctx, elephas.CreateRelationshipParams{
		FromEntityID: root.ID,
		ToEntityID:   target.ID,
		Type:         "knows",
	}); err != nil {
		t.Fatalf("create target relationship: %v", err)
	}

	for i := 0; i < 500; i++ {
		filler, err := store.CreateEntity(ctx, elephas.CreateEntityParams{
			Name: "Filler " + uuid.NewString(),
			Type: elephas.EntityTypePerson,
		})
		if err != nil {
			t.Fatalf("create filler entity %d: %v", i, err)
		}
		if _, err := store.CreateRelationship(ctx, elephas.CreateRelationshipParams{
			FromEntityID: root.ID,
			ToEntityID:   filler.ID,
			Type:         "related_to",
		}); err != nil {
			t.Fatalf("create filler relationship %d: %v", i, err)
		}
	}

	path, err := store.FindPath(ctx, root.ID, target.ID, 1)
	if err != nil {
		t.Fatalf("find path across paged relationships: %v", err)
	}
	if len(path) != 2 {
		t.Fatalf("expected direct path to target, got %d nodes", len(path))
	}
	if path[1].Entity.ID != target.ID {
		t.Fatalf("expected target entity at end of path")
	}
}

func TestSQLiteStoreGetEntityContextReturnsAllMemories(t *testing.T) {
	store, cleanup := newSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	entity, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Charlie", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	for i := 0; i < 501; i++ {
		_, err := store.CreateMemory(ctx, elephas.CreateMemoryParams{
			EntityID:   entity.ID,
			Content:    "Memory " + uuid.NewString(),
			Category:   elephas.MemoryCategoryFact,
			Confidence: 1,
		})
		if err != nil {
			t.Fatalf("create memory %d: %v", i, err)
		}
	}

	contextValue, err := store.GetEntityContext(ctx, entity.ID, 0)
	if err != nil {
		t.Fatalf("get entity context: %v", err)
	}
	if len(contextValue.Memories) != 501 {
		t.Fatalf("expected all memories in entity context, got %d", len(contextValue.Memories))
	}
}

func TestSQLiteStoreSearchMemoriesEscapesFTSQuerySyntax(t *testing.T) {
	store, cleanup := newSQLiteStore(t)
	defer cleanup()

	ctx := context.Background()
	entity, err := store.CreateEntity(ctx, elephas.CreateEntityParams{Name: "Charlie", Type: elephas.EntityTypePerson})
	if err != nil {
		t.Fatalf("create entity: %v", err)
	}

	if _, err := store.CreateMemory(ctx, elephas.CreateMemoryParams{
		EntityID:   entity.ID,
		Content:    "Working on C++ tooling",
		Category:   elephas.MemoryCategoryFact,
		Confidence: 1,
	}); err != nil {
		t.Fatalf("create memory: %v", err)
	}

	if _, err := store.SearchMemories(ctx, elephas.SearchQuery{Query: "C++", Limit: 10}); err != nil {
		t.Fatalf("search with FTS syntax characters: %v", err)
	}
}

func newSQLiteStore(t *testing.T) (*Store, func()) {
	return newSQLiteStoreWithOptions(t)
}

func newSQLiteStoreWithOptions(t *testing.T, opts ...Option) (*Store, func()) {
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
	store := New(db, "sqlite", opts...)
	return store, func() { _ = store.Close() }
}

func stringPtr(value string) *string {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}

func nowMinusHour() time.Time {
	return time.Now().UTC().Add(-time.Hour)
}

func uuidMustParse(t *testing.T, value string) uuid.UUID {
	t.Helper()
	parsed, err := uuid.Parse(value)
	if err != nil {
		t.Fatalf("parse uuid: %v", err)
	}
	return parsed
}
