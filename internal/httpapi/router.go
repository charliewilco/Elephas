package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charliewilco/elephas"
	"github.com/charliewilco/elephas/internal/config"
	"github.com/google/uuid"
)

type Router struct {
	service   *elephas.Service
	config    config.Config
	logger    *slog.Logger
	readiness readinessChecker
}

type readinessChecker interface {
	Current(context.Context) (bool, error)
}

func New(service *elephas.Service, cfg config.Config, logger *slog.Logger, readiness readinessChecker) http.Handler {
	// Routes are grouped by resource (health, memories, entities, relationships,
	// ingest, graph) to mirror service capabilities and keep handler discovery easy.
	router := &Router{
		service:   service,
		config:    cfg,
		logger:    logger,
		readiness: readiness,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", router.handleHealth)
	mux.HandleFunc("GET /v1/ready", router.handleReady)
	mux.HandleFunc("GET /v1/stats", router.handleStats)

	mux.HandleFunc("POST /v1/memories", router.handleCreateMemory)
	mux.HandleFunc("GET /v1/memories/{id}", router.handleGetMemory)
	mux.HandleFunc("PATCH /v1/memories/{id}", router.handleUpdateMemory)
	mux.HandleFunc("DELETE /v1/memories/{id}", router.handleDeleteMemory)
	mux.HandleFunc("GET /v1/memories", router.handleListMemories)
	mux.HandleFunc("POST /v1/memories/search", router.handleSearchMemories)

	mux.HandleFunc("POST /v1/entities", router.handleCreateEntity)
	mux.HandleFunc("GET /v1/entities/{id}", router.handleGetEntity)
	mux.HandleFunc("PATCH /v1/entities/{id}", router.handleUpdateEntity)
	mux.HandleFunc("DELETE /v1/entities/{id}", router.handleDeleteEntity)
	mux.HandleFunc("GET /v1/entities", router.handleListEntities)
	mux.HandleFunc("GET /v1/entities/{id}/context", router.handleGetEntityContext)

	mux.HandleFunc("POST /v1/relationships", router.handleCreateRelationship)
	mux.HandleFunc("GET /v1/relationships/{id}", router.handleGetRelationship)
	mux.HandleFunc("DELETE /v1/relationships/{id}", router.handleDeleteRelationship)
	mux.HandleFunc("GET /v1/relationships", router.handleListRelationships)

	mux.HandleFunc("POST /v1/ingest", router.handleIngest)
	mux.HandleFunc("GET /v1/ingest/{id}", router.handleGetIngestSource)
	mux.HandleFunc("POST /v1/graph/path", router.handleFindPath)

	return router.withMiddleware(mux)
}

func (r *Router) withMiddleware(next http.Handler) http.Handler {
	// Middleware order is intentional:
	//   requestID runs outermost so every log and response has an ID,
	//   logging wraps auth + handlers to include final status,
	//   auth runs innermost and rejects unauthorized requests before handlers.
	return r.requestID(r.logging(r.auth(next)))
}

func (r *Router) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requestID := req.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, req.WithContext(elephas.WithRequestID(req.Context(), requestID)))
	})
}

func (r *Router) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Empty API key means auth is disabled (local/dev default).
		if r.config.Server.APIKey == "" {
			next.ServeHTTP(w, req)
			return
		}

		token := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")
		if token != r.config.Server.APIKey {
			writeError(w, req, r.logger, http.StatusBadRequest, elephas.NewError(elephas.ErrorCodeInvalidRequest, "missing or invalid bearer token", nil))
			return
		}

		next.ServeHTTP(w, req)
	})
}

func (r *Router) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, req)

		r.logger.Info("request completed",
			"component", "api",
			"method", req.Method,
			"path", req.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", elephas.RequestIDFromContext(req.Context()),
		)
	})
}

func (r *Router) handleHealth(w http.ResponseWriter, req *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (r *Router) handleReady(w http.ResponseWriter, req *http.Request) {
	if err := r.service.Ping(req.Context()); err != nil {
		writeError(w, req, r.logger, http.StatusServiceUnavailable, elephas.WrapError(elephas.ErrorCodeStore, "service not ready", err, nil))
		return
	}
	if r.readiness != nil {
		current, err := r.readiness.Current(req.Context())
		if err != nil {
			writeError(w, req, r.logger, http.StatusServiceUnavailable, elephas.WrapError(elephas.ErrorCodeStore, "service not ready", err, nil))
			return
		}
		if !current {
			writeError(w, req, r.logger, http.StatusServiceUnavailable, elephas.NewError(elephas.ErrorCodeStore, "service not ready", map[string]any{
				"reason": "migrations_not_current",
			}))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (r *Router) handleStats(w http.ResponseWriter, req *http.Request) {
	stats, err := r.service.Stats(req.Context())
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (r *Router) handleCreateMemory(w http.ResponseWriter, req *http.Request) {
	var body struct {
		EntityID   uuid.UUID              `json:"entity_id"`
		Content    string                 `json:"content"`
		Category   elephas.MemoryCategory `json:"category"`
		Confidence *float64               `json:"confidence"`
		ExpiresAt  *time.Time             `json:"expires_at"`
		Metadata   map[string]any         `json:"metadata"`
	}
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	confidence := 1.0
	if body.Confidence != nil {
		confidence = *body.Confidence
	}

	memory, err := r.service.CreateMemory(req.Context(), elephas.CreateMemoryParams{
		EntityID:   body.EntityID,
		Content:    body.Content,
		Category:   body.Category,
		Confidence: confidence,
		ExpiresAt:  body.ExpiresAt,
		Metadata:   body.Metadata,
	})
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}

	writeJSON(w, http.StatusCreated, memory)
}

func (r *Router) handleGetMemory(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	memory, err := r.service.GetMemory(req.Context(), id)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (r *Router) handleUpdateMemory(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	var body map[string]json.RawMessage
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	patch, err := decodeMemoryPatch(body)
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	memory, err := r.service.UpdateMemory(req.Context(), id, patch)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, memory)
}

func (r *Router) handleDeleteMemory(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	if err := r.service.DeleteMemory(req.Context(), id); err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) handleListMemories(w http.ResponseWriter, req *http.Request) {
	filter, err := parseMemoryFilter(req)
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	page, err := r.service.ListMemories(req.Context(), filter)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (r *Router) handleSearchMemories(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Query                string                   `json:"q"`
		EntityID             *uuid.UUID               `json:"entity_id"`
		Categories           []elephas.MemoryCategory `json:"categories"`
		IncludeExpired       bool                     `json:"include_expired"`
		IncludeEntityContext bool                     `json:"include_entity_context"`
		Limit                int                      `json:"limit"`
	}
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	results, err := r.service.SearchMemories(req.Context(), elephas.SearchQuery{
		Query:                body.Query,
		EntityID:             body.EntityID,
		Categories:           body.Categories,
		IncludeExpired:       body.IncludeExpired,
		IncludeEntityContext: body.IncludeEntityContext,
		Limit:                body.Limit,
	})
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (r *Router) handleCreateEntity(w http.ResponseWriter, req *http.Request) {
	var body struct {
		Name       string             `json:"name"`
		Type       elephas.EntityType `json:"type"`
		ExternalID *string            `json:"external_id"`
		Metadata   map[string]any     `json:"metadata"`
	}
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	entity, err := r.service.CreateEntity(req.Context(), elephas.CreateEntityParams{
		Name:       body.Name,
		Type:       body.Type,
		ExternalID: body.ExternalID,
		Metadata:   body.Metadata,
	})
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, entity)
}

func (r *Router) handleGetEntity(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	entity, err := r.service.GetEntity(req.Context(), id)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, entity)
}

func (r *Router) handleUpdateEntity(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	var body map[string]json.RawMessage
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	patch, err := decodeEntityPatch(body)
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	entity, err := r.service.UpdateEntity(req.Context(), id, patch)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, entity)
}

func (r *Router) handleDeleteEntity(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	if err := r.service.DeleteEntity(req.Context(), id); err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) handleListEntities(w http.ResponseWriter, req *http.Request) {
	filter, err := parseEntityFilter(req)
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	page, err := r.service.ListEntities(req.Context(), filter)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (r *Router) handleGetEntityContext(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	depth, err := parseIntQuery(req, "depth", 1)
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	result, err := r.service.GetEntityContext(req.Context(), id, depth)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) handleCreateRelationship(w http.ResponseWriter, req *http.Request) {
	var body struct {
		FromEntityID uuid.UUID      `json:"from_entity_id"`
		ToEntityID   uuid.UUID      `json:"to_entity_id"`
		Type         string         `json:"type"`
		Weight       *float64       `json:"weight"`
		Metadata     map[string]any `json:"metadata"`
	}
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	relationship, err := r.service.CreateRelationship(req.Context(), elephas.CreateRelationshipParams{
		FromEntityID: body.FromEntityID,
		ToEntityID:   body.ToEntityID,
		Type:         body.Type,
		Weight:       body.Weight,
		Metadata:     body.Metadata,
	})
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusCreated, relationship)
}

func (r *Router) handleGetRelationship(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	relationship, err := r.service.GetRelationship(req.Context(), id)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, relationship)
}

func (r *Router) handleDeleteRelationship(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	if err := r.service.DeleteRelationship(req.Context(), id); err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (r *Router) handleListRelationships(w http.ResponseWriter, req *http.Request) {
	filter, err := parseRelationshipFilter(req)
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	page, err := r.service.ListRelationships(req.Context(), filter)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (r *Router) handleIngest(w http.ResponseWriter, req *http.Request) {
	var body struct {
		RawText           string     `json:"raw_text"`
		SubjectEntityID   *uuid.UUID `json:"subject_entity_id"`
		SubjectExternalID string     `json:"subject_external_id"`
		ExtractorModel    string     `json:"extractor_model"`
		DryRun            bool       `json:"dry_run"`
	}
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	result, err := r.service.Ingest(req.Context(), elephas.IngestRequest{
		RawText:           body.RawText,
		SubjectEntityID:   body.SubjectEntityID,
		SubjectExternalID: body.SubjectExternalID,
		ExtractorModel:    body.ExtractorModel,
		DryRun:            body.DryRun,
	})
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (r *Router) handleGetIngestSource(w http.ResponseWriter, req *http.Request) {
	id, err := parseUUIDPath(req, "id")
	if err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}
	source, err := r.service.GetIngestSource(req.Context(), id)
	if err != nil {
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

func (r *Router) handleFindPath(w http.ResponseWriter, req *http.Request) {
	var body struct {
		FromEntityID uuid.UUID `json:"from_entity_id"`
		ToEntityID   uuid.UUID `json:"to_entity_id"`
		MaxDepth     int       `json:"max_depth"`
	}
	if err := decodeJSON(req, &body); err != nil {
		writeError(w, req, r.logger, http.StatusBadRequest, err)
		return
	}

	path, err := r.service.FindPath(req.Context(), body.FromEntityID, body.ToEntityID, body.MaxDepth)
	if err != nil {
		var elephasErr *elephas.Error
		if errors.As(err, &elephasErr) && elephasErr.Code == elephas.ErrorCodeNotFound {
			writeJSON(w, http.StatusOK, map[string]any{"path": []elephas.PathNode{}, "found": false})
			return
		}
		writeError(w, req, r.logger, statusForError(err), err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"path": path, "found": true})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, req *http.Request, logger *slog.Logger, status int, err error) {
	var apiErr *elephas.Error
	if !errors.As(err, &apiErr) {
		apiErr = elephas.WrapError(elephas.ErrorCodeStore, "internal server error", err, nil)
	}

	logger.Error("request failed",
		"component", "api",
		"error", apiErr.Error(),
		"request_id", elephas.RequestIDFromContext(req.Context()),
	)

	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    apiErr.Code,
			"message": apiErr.Message,
			"details": apiErr.Details,
		},
	})
}

func decodeJSON(req *http.Request, target any) error {
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid JSON body", err, nil)
	}
	return nil
}

func parseUUIDPath(req *http.Request, name string) (uuid.UUID, error) {
	value := req.PathValue(name)
	id, err := uuid.Parse(value)
	if err != nil {
		return uuid.Nil, elephas.WrapError(elephas.ErrorCodeInvalidRequest, fmt.Sprintf("invalid %s", name), err, nil)
	}
	return id, nil
}

func parseMemoryFilter(req *http.Request) (elephas.MemoryFilter, error) {
	query := req.URL.Query()
	filter := elephas.MemoryFilter{}
	if value := query.Get("entity_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return elephas.MemoryFilter{}, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid entity_id", err, nil)
		}
		filter.EntityID = &id
	}
	if value := query.Get("category"); value != "" {
		category := elephas.MemoryCategory(value)
		filter.Category = &category
	}
	includeExpired, err := parseBoolQuery(req, "include_expired", false)
	if err != nil {
		return elephas.MemoryFilter{}, err
	}
	filter.IncludeExpired = includeExpired
	if value := query.Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return elephas.MemoryFilter{}, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid since", err, nil)
		}
		filter.Since = &parsed
	}
	filter.Limit, err = parseIntQuery(req, "limit", 50)
	if err != nil {
		return elephas.MemoryFilter{}, err
	}
	filter.Offset, err = parseIntQuery(req, "offset", 0)
	if err != nil {
		return elephas.MemoryFilter{}, err
	}
	return filter, nil
}

func parseEntityFilter(req *http.Request) (elephas.EntityFilter, error) {
	query := req.URL.Query()
	filter := elephas.EntityFilter{
		Name:       query.Get("name"),
		ExternalID: query.Get("external_id"),
	}
	if value := query.Get("type"); value != "" {
		entityType := elephas.EntityType(value)
		filter.Type = &entityType
	}
	var err error
	filter.Limit, err = parseIntQuery(req, "limit", 50)
	if err != nil {
		return elephas.EntityFilter{}, err
	}
	filter.Offset, err = parseIntQuery(req, "offset", 0)
	if err != nil {
		return elephas.EntityFilter{}, err
	}
	return filter, nil
}

func parseRelationshipFilter(req *http.Request) (elephas.RelationshipFilter, error) {
	query := req.URL.Query()
	filter := elephas.RelationshipFilter{Type: query.Get("type")}
	if value := query.Get("from_entity_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return elephas.RelationshipFilter{}, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid from_entity_id", err, nil)
		}
		filter.FromEntityID = &id
	}
	if value := query.Get("to_entity_id"); value != "" {
		id, err := uuid.Parse(value)
		if err != nil {
			return elephas.RelationshipFilter{}, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid to_entity_id", err, nil)
		}
		filter.ToEntityID = &id
	}
	var err error
	filter.Limit, err = parseIntQuery(req, "limit", 50)
	if err != nil {
		return elephas.RelationshipFilter{}, err
	}
	filter.Offset, err = parseIntQuery(req, "offset", 0)
	if err != nil {
		return elephas.RelationshipFilter{}, err
	}
	return filter, nil
}

func parseIntQuery(req *http.Request, key string, fallback int) (int, error) {
	value := req.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, elephas.WrapError(elephas.ErrorCodeInvalidRequest, fmt.Sprintf("invalid %s", key), err, nil)
	}
	return parsed, nil
}

func parseBoolQuery(req *http.Request, key string, fallback bool) (bool, error) {
	value := req.URL.Query().Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, elephas.WrapError(elephas.ErrorCodeInvalidRequest, fmt.Sprintf("invalid %s", key), err, nil)
	}
	return parsed, nil
}

func decodeMemoryPatch(body map[string]json.RawMessage) (elephas.MemoryPatch, error) {
	var patch elephas.MemoryPatch
	if value, ok := body["content"]; ok {
		var parsed string
		if err := json.Unmarshal(value, &parsed); err != nil {
			return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid content", err, nil)
		}
		patch.Content = &parsed
	}
	if value, ok := body["confidence"]; ok {
		var parsed float64
		if err := json.Unmarshal(value, &parsed); err != nil {
			return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid confidence", err, nil)
		}
		patch.Confidence = &parsed
	}
	if value, ok := body["expires_at"]; ok {
		if string(value) == "null" {
			patch.ClearExpiresAt = true
		} else {
			var parsed time.Time
			if err := json.Unmarshal(value, &parsed); err != nil {
				return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid expires_at", err, nil)
			}
			patch.ExpiresAt = &parsed
		}
	}
	if value, ok := body["metadata"]; ok {
		var parsed map[string]any
		if err := json.Unmarshal(value, &parsed); err != nil {
			return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid metadata", err, nil)
		}
		patch.Metadata = parsed
		patch.SetMetadata = true
	}
	return patch, nil
}

func decodeEntityPatch(body map[string]json.RawMessage) (elephas.EntityPatch, error) {
	var patch elephas.EntityPatch
	if value, ok := body["name"]; ok {
		var parsed string
		if err := json.Unmarshal(value, &parsed); err != nil {
			return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid name", err, nil)
		}
		patch.Name = &parsed
	}
	if value, ok := body["type"]; ok {
		var parsed elephas.EntityType
		if err := json.Unmarshal(value, &parsed); err != nil {
			return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid type", err, nil)
		}
		patch.Type = &parsed
	}
	if value, ok := body["external_id"]; ok {
		if string(value) == "null" {
			patch.ClearExternalID = true
		} else {
			var parsed string
			if err := json.Unmarshal(value, &parsed); err != nil {
				return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid external_id", err, nil)
			}
			patch.ExternalID = &parsed
		}
	}
	if value, ok := body["metadata"]; ok {
		var parsed map[string]any
		if err := json.Unmarshal(value, &parsed); err != nil {
			return patch, elephas.WrapError(elephas.ErrorCodeInvalidRequest, "invalid metadata", err, nil)
		}
		patch.Metadata = parsed
		patch.SetMetadata = true
	}
	return patch, nil
}

func statusForError(err error) int {
	var apiErr *elephas.Error
	if !errors.As(err, &apiErr) {
		return http.StatusInternalServerError
	}
	switch apiErr.Code {
	case elephas.ErrorCodeNotFound:
		return http.StatusNotFound
	case elephas.ErrorCodeInvalidRequest:
		return http.StatusBadRequest
	case elephas.ErrorCodeConflict:
		return http.StatusConflict
	case elephas.ErrorCodeExtractionFailed:
		return http.StatusUnprocessableEntity
	case elephas.ErrorCodeExtractorUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
