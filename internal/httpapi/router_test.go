package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/charliewilco/elephas/internal/migrate"
	"github.com/charliewilco/elephas/internal/store/sqlstore"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func TestRouterAdminEndpointsAndRequestID(t *testing.T) {
	handler, cleanup := newTestRouter(t, "", fakeExtractor{})
	defer cleanup()

	health := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("expected health to return 200, got %d", health.Code)
	}
	if got := health.Header().Get("X-Request-ID"); got == "" {
		t.Fatalf("expected request id to be generated")
	}
	assertStatusField(t, health.Body.Bytes(), "ok")

	ready := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("expected ready to return 200, got %d", ready.Code)
	}
	assertStatusField(t, ready.Body.Bytes(), "ready")

	stats := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/stats", nil))
	if stats.Code != http.StatusOK {
		t.Fatalf("expected stats to return 200, got %d", stats.Code)
	}

	var payload elephas.Stats
	decodeResponse(t, stats.Body.Bytes(), &payload)
	if payload.Backend != "sqlite" {
		t.Fatalf("expected sqlite backend, got %s", payload.Backend)
	}
	if payload.EntityCount != 0 || payload.MemoryCount != 0 || payload.RelationshipCount != 0 || payload.IngestSourceCount != 0 {
		t.Fatalf("expected empty stats on a fresh database, got %+v", payload)
	}
}

func TestRouterReadyReturns503WhenStoreUnavailable(t *testing.T) {
	handler, db, cleanup := newTestRouterWithDB(t, "", fakeExtractor{})
	defer cleanup()

	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	ready := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/ready", nil))
	assertErrorResponse(t, ready, http.StatusServiceUnavailable, elephas.ErrorCodeStore)
}

func TestRouterReadyReturns503WhenMigrationsAreStale(t *testing.T) {
	handler, db, cleanup := newTestRouterWithDB(t, "", fakeExtractor{})
	defer cleanup()

	var migrationName string
	if err := db.QueryRow("SELECT name FROM elephas_migrations LIMIT 1").Scan(&migrationName); err != nil {
		t.Fatalf("select applied migration: %v", err)
	}
	if _, err := db.Exec("DELETE FROM elephas_migrations WHERE name = ?", migrationName); err != nil {
		t.Fatalf("delete applied migration: %v", err)
	}

	ready := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/ready", nil))
	assertErrorResponse(t, ready, http.StatusServiceUnavailable, elephas.ErrorCodeStore)
}

func TestRouterAuthAndValidationFailures(t *testing.T) {
	handler, cleanup := newTestRouter(t, "secret", fakeExtractor{})
	defer cleanup()

	health := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("expected public health probe to return 200, got %d", health.Code)
	}
	assertStatusField(t, health.Body.Bytes(), "ok")

	ready := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("expected public ready probe to return 200, got %d", ready.Code)
	}
	assertStatusField(t, ready.Body.Bytes(), "ready")

	unauthorized := serveRequest(handler, newRequest(t, http.MethodGet, "/v1/stats", nil))
	assertErrorResponse(t, unauthorized, http.StatusUnauthorized, elephas.ErrorCodeInvalidRequest)
	assertBearerChallenge(t, unauthorized)
	if got := unauthorized.Header().Get("X-Request-ID"); got == "" {
		t.Fatalf("expected request id to be set on unauthorized response")
	}

	badToken := newRequest(t, http.MethodGet, "/v1/stats", nil)
	badToken.Header.Set("Authorization", "Bearer wrong")
	badTokenResponse := serveRequest(handler, badToken)
	assertErrorResponse(t, badTokenResponse, http.StatusUnauthorized, elephas.ErrorCodeInvalidRequest)
	assertBearerChallenge(t, badTokenResponse)

	authorized := newRequest(t, http.MethodGet, "/v1/stats", nil)
	authorized.Header.Set("Authorization", "Bearer secret")
	ok := serveRequest(handler, authorized)
	if ok.Code != http.StatusOK {
		t.Fatalf("expected authorized stats request to succeed, got %d", ok.Code)
	}

	badEntity := newJSONRequest(t, http.MethodPost, "/v1/entities", map[string]any{
		"name": "Charlie",
		"type": "planet",
	})
	badEntity.Header.Set("Authorization", "Bearer secret")
	assertErrorResponse(t, serveRequest(handler, badEntity), http.StatusBadRequest, elephas.ErrorCodeInvalidRequest)

	badPath := newJSONRequest(t, http.MethodGet, "/v1/entities/not-a-uuid", nil)
	badPath.Header.Set("Authorization", "Bearer secret")
	assertErrorResponse(t, serveRequest(handler, badPath), http.StatusBadRequest, elephas.ErrorCodeInvalidRequest)

	badDepth := newJSONRequest(t, http.MethodGet, "/v1/entities/"+uuid.NewString()+"/context?depth=4", nil)
	badDepth.Header.Set("Authorization", "Bearer secret")
	assertErrorResponse(t, serveRequest(handler, badDepth), http.StatusBadRequest, elephas.ErrorCodeInvalidRequest)

	badPathDepth := newJSONRequest(t, http.MethodPost, "/v1/graph/path", map[string]any{
		"from_entity_id": uuid.NewString(),
		"to_entity_id":   uuid.NewString(),
		"max_depth":      0,
	})
	badPathDepth.Header.Set("Authorization", "Bearer secret")
	assertErrorResponse(t, serveRequest(handler, badPathDepth), http.StatusBadRequest, elephas.ErrorCodeInvalidRequest)

	emptySearch := newJSONRequest(t, http.MethodPost, "/v1/memories/search", map[string]any{
		"q": "",
	})
	emptySearch.Header.Set("Authorization", "Bearer secret")
	assertErrorResponse(t, serveRequest(handler, emptySearch), http.StatusBadRequest, elephas.ErrorCodeInvalidRequest)

	trailingJSON := newAuthenticatedRequest(t, http.MethodPost, "/v1/entities", `{"name":"Delta","type":"person"}{"ignored":true}`)
	assertErrorResponse(t, serveRequest(handler, trailingJSON), http.StatusBadRequest, elephas.ErrorCodeInvalidRequest)

	notFound := newJSONRequest(t, http.MethodGet, "/v1/entities/"+uuid.NewString(), nil)
	notFound.Header.Set("Authorization", "Bearer secret")
	assertErrorResponse(t, serveRequest(handler, notFound), http.StatusNotFound, elephas.ErrorCodeNotFound)
}

func TestRouterCrudListAndDeleteEndpoints(t *testing.T) {
	handler, cleanup := newTestRouter(t, "", fakeExtractor{})
	defer cleanup()

	subject := createEntityViaAPI(t, handler, map[string]any{
		"name":        "Charlie",
		"type":        "person",
		"external_id": "user-123",
		"metadata":    map[string]any{"team": "core"},
	})
	if subject.Name != "Charlie" || subject.ExternalID == nil || *subject.ExternalID != "user-123" {
		t.Fatalf("unexpected created entity: %+v", subject)
	}

	expiringAt := time.Now().Add(time.Hour).UTC()
	memory := createMemoryViaAPI(t, handler, map[string]any{
		"entity_id":  subject.ID,
		"content":    "Prefers dark mode across all applications.",
		"category":   "preference",
		"confidence": 0.7,
		"expires_at": expiringAt,
		"metadata":   map[string]any{"source": "api"},
	})

	updated := patchMemoryViaAPI(t, handler, memory.ID, map[string]any{
		"content":    "Prefers light mode in the morning.",
		"confidence": 0.9,
		"metadata":   map[string]any{"updated": true},
		"expires_at": nil,
	})
	if updated.Content != "Prefers light mode in the morning." {
		t.Fatalf("expected memory content to update, got %q", updated.Content)
	}
	if updated.ExpiresAt != nil {
		t.Fatalf("expected expires_at to be cleared")
	}

	_ = createMemoryViaAPI(t, handler, map[string]any{
		"entity_id":  subject.ID,
		"content":    "Used to prefer green themes.",
		"category":   "preference",
		"confidence": 0.4,
		"expires_at": time.Now().Add(-time.Hour).UTC(),
	})

	activeList := listMemoriesViaAPI(t, handler, "/v1/memories?entity_id="+subject.ID.String()+"&category=preference")
	if len(activeList.Data) != 1 {
		t.Fatalf("expected expired memory to be excluded, got %d items", len(activeList.Data))
	}

	allList := listMemoriesViaAPI(t, handler, "/v1/memories?entity_id="+subject.ID.String()+"&category=preference&include_expired=true")
	if len(allList.Data) != 2 {
		t.Fatalf("expected both memories when include_expired=true, got %d items", len(allList.Data))
	}
	if allList.Total < 2 {
		t.Fatalf("expected total count to reflect both memories, got %d", allList.Total)
	}

	fetched := getMemoryViaAPI(t, handler, memory.ID)
	if fetched.ID != memory.ID {
		t.Fatalf("expected to fetch the updated memory")
	}

	other := createEntityViaAPI(t, handler, map[string]any{
		"name": "Weave",
		"type": "organization",
	})

	relationship := createRelationshipViaAPI(t, handler, map[string]any{
		"from_entity_id": subject.ID,
		"to_entity_id":   other.ID,
		"type":           "works_at",
		"metadata":       map[string]any{"source": "api"},
	})

	fetchedRelationship := getRelationshipViaAPI(t, handler, relationship.ID)
	if fetchedRelationship.ID != relationship.ID {
		t.Fatalf("expected to fetch the created relationship")
	}

	listedEntities := listEntitiesViaAPI(t, handler, "/v1/entities?name=Char&type=person&external_id=user-123&limit=10&offset=0")
	if len(listedEntities.Data) != 1 {
		t.Fatalf("expected entity filter to return exactly one result, got %d", len(listedEntities.Data))
	}

	listedRelationships := listRelationshipsViaAPI(t, handler, "/v1/relationships?from_entity_id="+subject.ID.String()+"&type=works_at")
	if len(listedRelationships.Data) != 1 {
		t.Fatalf("expected relationship filter to return one result, got %d", len(listedRelationships.Data))
	}

	contextValue := getEntityContextViaAPI(t, handler, subject.ID, 1)
	if len(contextValue.Memories) != 2 {
		t.Fatalf("expected entity context to include memories, got %d", len(contextValue.Memories))
	}
	if len(contextValue.Relationships) != 1 {
		t.Fatalf("expected entity context to include one relationship, got %d", len(contextValue.Relationships))
	}

	deleteRequest := newAuthenticatedRequest(t, http.MethodDelete, "/v1/memories/"+memory.ID.String(), nil)
	deleteResponse := serveRequest(handler, deleteRequest)
	if deleteResponse.Code != http.StatusNoContent {
		t.Fatalf("expected memory delete to return 204, got %d", deleteResponse.Code)
	}

	missingMemory := newAuthenticatedRequest(t, http.MethodGet, "/v1/memories/"+memory.ID.String(), nil)
	assertErrorResponse(t, serveRequest(handler, missingMemory), http.StatusNotFound, elephas.ErrorCodeNotFound)

	deleteRelationshipRequest := newAuthenticatedRequest(t, http.MethodDelete, "/v1/relationships/"+relationship.ID.String(), nil)
	deleteRelationshipResponse := serveRequest(handler, deleteRelationshipRequest)
	if deleteRelationshipResponse.Code != http.StatusNoContent {
		t.Fatalf("expected relationship delete to return 204, got %d", deleteRelationshipResponse.Code)
	}

	temp := createEntityViaAPI(t, handler, map[string]any{
		"name": "Temp",
		"type": "concept",
	})
	deleteEntityRequest := newAuthenticatedRequest(t, http.MethodDelete, "/v1/entities/"+temp.ID.String(), nil)
	deleteEntityResponse := serveRequest(handler, deleteEntityRequest)
	if deleteEntityResponse.Code != http.StatusNoContent {
		t.Fatalf("expected entity delete to return 204, got %d", deleteEntityResponse.Code)
	}

	missingEntity := newAuthenticatedRequest(t, http.MethodGet, "/v1/entities/"+temp.ID.String(), nil)
	assertErrorResponse(t, serveRequest(handler, missingEntity), http.StatusNotFound, elephas.ErrorCodeNotFound)
}

func TestRouterIngestSearchContextPathAndIngestSourceLookup(t *testing.T) {
	handler, cleanup := newTestRouter(t, "", fakeExtractor{
		candidates: []elephas.CandidateMemory{
			{
				Content:    "Prefers dark mode across all applications.",
				Category:   elephas.MemoryCategoryFact,
				Confidence: 0.9,
				RelatedEntities: []elephas.CandidateEntity{
					{Name: "Elephas", Type: elephas.EntityTypeAgent},
				},
				RelationshipType: "uses",
			},
		},
	})
	defer cleanup()

	subject := createEntityViaAPI(t, handler, map[string]any{
		"name":        "Charlie",
		"type":        "person",
		"external_id": "subject-1",
	})

	dryRun := ingestViaAPI(t, handler, map[string]any{
		"raw_text":            "Charlie prefers dark mode.",
		"subject_external_id": "subject-1",
		"extractor_model":     "gpt-test",
		"dry_run":             true,
	})
	if dryRun.IngestSourceID != nil {
		t.Fatalf("expected dry_run to omit ingest source id")
	}
	if dryRun.MemoriesCreated != 1 || dryRun.RelationshipsCreated != 1 || dryRun.EntitiesCreated != 1 {
		t.Fatalf("unexpected dry-run counts: %+v", dryRun)
	}
	if len(dryRun.CommittedMemories) != 0 {
		t.Fatalf("expected dry_run to avoid committed memories")
	}

	committed := ingestViaAPI(t, handler, map[string]any{
		"raw_text":            "Charlie prefers dark mode.",
		"subject_external_id": "subject-1",
		"extractor_model":     "gpt-test",
	})
	if committed.IngestSourceID == nil {
		t.Fatalf("expected ingest source id to be returned")
	}
	if committed.MemoriesCreated != 1 || committed.RelationshipsCreated != 1 || committed.EntitiesCreated != 1 {
		t.Fatalf("unexpected ingest counts: %+v", committed)
	}
	if len(committed.CommittedMemories) != 1 {
		t.Fatalf("expected one committed memory, got %d", len(committed.CommittedMemories))
	}

	source := getIngestSourceViaAPI(t, handler, *committed.IngestSourceID)
	if source.ExtractorModel != "gpt-test" {
		t.Fatalf("expected ingest source to preserve extractor model override, got %q", source.ExtractorModel)
	}

	searchResponse := searchMemoriesViaAPI(t, handler, map[string]any{
		"q":                      "dark mode",
		"include_entity_context": true,
		"limit":                  10,
	})
	if len(searchResponse) != 1 {
		t.Fatalf("expected one search result, got %d", len(searchResponse))
	}
	if searchResponse[0].Entity == nil || searchResponse[0].Entity.ID != subject.ID {
		t.Fatalf("expected search result to include entity context")
	}
	if len(searchResponse[0].Relationships) != 1 {
		t.Fatalf("expected search result to include entity relationships")
	}

	contextValue := getEntityContextViaAPI(t, handler, subject.ID, 1)
	if len(contextValue.Relationships) != 1 {
		t.Fatalf("expected entity context to include the ingest-created relationship")
	}

	relatedList := listEntitiesViaAPI(t, handler, "/v1/entities?name=Elephas&type=agent")
	if len(relatedList.Data) != 1 {
		t.Fatalf("expected to find related entity created during ingest, got %d", len(relatedList.Data))
	}

	pathResponse := findPathViaAPI(t, handler, map[string]any{
		"from_entity_id": subject.ID,
		"to_entity_id":   relatedList.Data[0].ID,
		"max_depth":      3,
	})
	if !pathResponse.Found {
		t.Fatalf("expected path to be found")
	}
	if len(pathResponse.Path) == 0 {
		t.Fatalf("expected path response to include nodes")
	}
}

func TestRouterIngestRequestLoggingIncludesRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	handler, cleanup := newTestRouterWithLogger(t, "", fakeExtractor{}, logger)
	defer cleanup()

	req := newJSONRequest(t, http.MethodPost, "/v1/ingest", map[string]any{
		"raw_text": "Charlie prefers dark mode.",
	})
	req.Header.Set("X-Request-ID", "request-id-123")

	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected ingest request to succeed, got %d: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("X-Request-ID"); got != "request-id-123" {
		t.Fatalf("expected response request id to be preserved, got %q", got)
	}

	logOutput := buf.String()
	if !strings.Contains(logOutput, `"component":"api"`) {
		t.Fatalf("expected api component in request log, got %s", logOutput)
	}
	if !strings.Contains(logOutput, `"request_id":"request-id-123"`) {
		t.Fatalf("expected request id in request log, got %s", logOutput)
	}
	if !strings.Contains(logOutput, `"path":"/v1/ingest"`) {
		t.Fatalf("expected ingest path in request log, got %s", logOutput)
	}
}

func newTestRouter(t *testing.T, apiKey string, extractor elephas.Extractor) (http.Handler, func()) {
	t.Helper()
	handler, cleanup := newTestRouterWithLogger(t, apiKey, extractor, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
	return handler, cleanup
}

func newTestRouterWithDB(t *testing.T, apiKey string, extractor elephas.Extractor) (http.Handler, *sql.DB, func()) {
	t.Helper()
	return newTestRouterWithLoggerAndDB(t, apiKey, extractor, slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

func newTestRouterWithLogger(t *testing.T, apiKey string, extractor elephas.Extractor, logger *slog.Logger) (http.Handler, func()) {
	t.Helper()
	handler, _, cleanup := newTestRouterWithLoggerAndDB(t, apiKey, extractor, logger)
	return handler, cleanup
}

func newTestRouterWithLoggerAndDB(t *testing.T, apiKey string, extractor elephas.Extractor, logger *slog.Logger) (http.Handler, *sql.DB, func()) {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	runner := migrate.NewRunner(db, "sqlite")
	if err := runner.Run(context.Background()); err != nil {
		t.Fatalf("run migrations: %v", err)
	}

	store := sqlstore.New(db, "sqlite")
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	options := []elephas.ServiceOption{elephas.WithLogger(logger)}
	if extractor != nil {
		options = append(options, elephas.WithExtractor(extractor))
	}

	service := elephas.NewService(store, options...)
	cfg := config.Config{}
	cfg.Server.APIKey = apiKey

	handler := New(service, cfg, logger, runner)
	return handler, db, func() {
		_ = service.Close()
		_ = db.Close()
	}
}

type fakeExtractor struct {
	candidates []elephas.CandidateMemory
	err        error
}

func (f fakeExtractor) Extract(_ context.Context, _ elephas.ExtractRequest) ([]elephas.CandidateMemory, error) {
	return f.candidates, f.err
}

func serveRequest(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func newRequest(t *testing.T, method, target string, payload any) *http.Request {
	t.Helper()
	req := newJSONRequest(t, method, target, payload)
	return req
}

func newAuthenticatedRequest(t *testing.T, method, target string, payload any) *http.Request {
	t.Helper()
	req := newJSONRequest(t, method, target, payload)
	req.Header.Set("Authorization", "Bearer secret")
	return req
}

func newJSONRequest(t *testing.T, method, target string, payload any) *http.Request {
	t.Helper()

	var reader io.Reader
	switch value := payload.(type) {
	case nil:
	case string:
		reader = strings.NewReader(value)
	case []byte:
		reader = bytes.NewReader(value)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal request body: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req := httptest.NewRequest(method, target, reader)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func decodeResponse[T any](t *testing.T, body []byte, target *T) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response: %v\nbody: %s", err, string(body))
	}
}

func assertStatusField(t *testing.T, body []byte, expected string) {
	t.Helper()
	var payload map[string]string
	decodeResponse(t, body, &payload)
	if payload["status"] != expected {
		t.Fatalf("expected status %q, got %q", expected, payload["status"])
	}
}

func assertErrorResponse(t *testing.T, rr *httptest.ResponseRecorder, expectedStatus int, expectedCode elephas.ErrorCode) {
	t.Helper()
	if rr.Code != expectedStatus {
		t.Fatalf("expected status %d, got %d", expectedStatus, rr.Code)
	}

	var payload struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	decodeResponse(t, rr.Body.Bytes(), &payload)
	if payload.Error.Code != string(expectedCode) {
		t.Fatalf("expected error code %q, got %q", expectedCode, payload.Error.Code)
	}
	if strings.TrimSpace(payload.Error.Message) == "" {
		t.Fatalf("expected error message to be populated")
	}
}

func assertBearerChallenge(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if got := rr.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Fatalf("expected WWW-Authenticate header %q, got %q", "Bearer", got)
	}
}

func createEntityViaAPI(t *testing.T, handler http.Handler, payload map[string]any) elephas.Entity {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPost, "/v1/entities", payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected entity create to return 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var entity elephas.Entity
	decodeResponse(t, rr.Body.Bytes(), &entity)
	return entity
}

func createMemoryViaAPI(t *testing.T, handler http.Handler, payload map[string]any) elephas.Memory {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPost, "/v1/memories", payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected memory create to return 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var memory elephas.Memory
	decodeResponse(t, rr.Body.Bytes(), &memory)
	return memory
}

func patchMemoryViaAPI(t *testing.T, handler http.Handler, id uuid.UUID, payload map[string]any) elephas.Memory {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPatch, "/v1/memories/"+id.String(), payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected memory patch to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var memory elephas.Memory
	decodeResponse(t, rr.Body.Bytes(), &memory)
	return memory
}

func getMemoryViaAPI(t *testing.T, handler http.Handler, id uuid.UUID) elephas.Memory {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, "/v1/memories/"+id.String(), nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected memory get to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var memory elephas.Memory
	decodeResponse(t, rr.Body.Bytes(), &memory)
	return memory
}

func listMemoriesViaAPI(t *testing.T, handler http.Handler, target string) elephas.Page[elephas.Memory] {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, target, nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected memory list to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var page elephas.Page[elephas.Memory]
	decodeResponse(t, rr.Body.Bytes(), &page)
	return page
}

func createRelationshipViaAPI(t *testing.T, handler http.Handler, payload map[string]any) elephas.Relationship {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPost, "/v1/relationships", payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected relationship create to return 201, got %d: %s", rr.Code, rr.Body.String())
	}
	var relationship elephas.Relationship
	decodeResponse(t, rr.Body.Bytes(), &relationship)
	return relationship
}

func getRelationshipViaAPI(t *testing.T, handler http.Handler, id uuid.UUID) elephas.Relationship {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, "/v1/relationships/"+id.String(), nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected relationship get to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var relationship elephas.Relationship
	decodeResponse(t, rr.Body.Bytes(), &relationship)
	return relationship
}

func listRelationshipsViaAPI(t *testing.T, handler http.Handler, target string) elephas.Page[elephas.Relationship] {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, target, nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected relationship list to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var page elephas.Page[elephas.Relationship]
	decodeResponse(t, rr.Body.Bytes(), &page)
	return page
}

func listEntitiesViaAPI(t *testing.T, handler http.Handler, target string) elephas.Page[elephas.Entity] {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, target, nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected entity list to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var page elephas.Page[elephas.Entity]
	decodeResponse(t, rr.Body.Bytes(), &page)
	return page
}

func getEntityContextViaAPI(t *testing.T, handler http.Handler, id uuid.UUID, depth int) elephas.EntityContext {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, fmt.Sprintf("/v1/entities/%s/context?depth=%d", id.String(), depth), nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected entity context to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var contextValue elephas.EntityContext
	decodeResponse(t, rr.Body.Bytes(), &contextValue)
	return contextValue
}

func ingestViaAPI(t *testing.T, handler http.Handler, payload map[string]any) elephas.IngestResponse {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPost, "/v1/ingest", payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected ingest to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var response elephas.IngestResponse
	decodeResponse(t, rr.Body.Bytes(), &response)
	return response
}

func getIngestSourceViaAPI(t *testing.T, handler http.Handler, id uuid.UUID) elephas.IngestSource {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodGet, "/v1/ingest/"+id.String(), nil)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected ingest source lookup to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var source elephas.IngestSource
	decodeResponse(t, rr.Body.Bytes(), &source)
	return source
}

func searchMemoriesViaAPI(t *testing.T, handler http.Handler, payload map[string]any) []elephas.MemorySearchResult {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPost, "/v1/memories/search", payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected search to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var results []elephas.MemorySearchResult
	decodeResponse(t, rr.Body.Bytes(), &results)
	return results
}

func findPathViaAPI(t *testing.T, handler http.Handler, payload map[string]any) struct {
	Path  []elephas.PathNode `json:"path"`
	Found bool               `json:"found"`
} {
	t.Helper()
	req := newAuthenticatedRequest(t, http.MethodPost, "/v1/graph/path", payload)
	rr := serveRequest(handler, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected path search to return 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		Path  []elephas.PathNode `json:"path"`
		Found bool               `json:"found"`
	}
	decodeResponse(t, rr.Body.Bytes(), &response)
	return response
}
