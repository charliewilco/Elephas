package elephas

import (
	"context"
	"sort"
	"strings"
	"unicode"

	"github.com/google/uuid"
)

type ingestPlan struct {
	// steps is the ordered execution plan produced by resolution.
	steps []plannedStep
	// entityRefs tracks all entities mentioned by candidates so they can be
	// resolved once and reused across memory/relationship operations.
	entityRefs map[string]*entityRef
	// relationships are deduplicated by canonical key before persistence.
	relationships map[string]*relationshipPlan
}

type plannedStep struct {
	response         ResolutionStep
	subjectKey       string
	subjectExisting  *Entity
	relatedKeys      []string
	relationshipType string
}

type entityRef struct {
	key       string
	existing  *Entity
	create    CreateEntityParams
	createdID *uuid.UUID
}

type relationshipPlan struct {
	key       string
	fromKey   string
	toKey     string
	typ       string
	createdID *uuid.UUID
}

func (s *Service) Ingest(ctx context.Context, request IngestRequest) (IngestResponse, error) {
	// Ingest is a 3-phase pipeline:
	//   1) validate + extract candidate memories from text
	//   2) build an in-memory resolution plan (pure computation)
	//   3) optionally commit the plan in a transaction, then audit + invalidate cache
	startedAt := s.now()
	ingestLogger := s.loggerFor(ctx, "ingest")
	extractorLogger := s.loggerFor(ctx, "extractor")
	resolverLogger := s.loggerFor(ctx, "resolver")

	if strings.TrimSpace(request.RawText) == "" {
		return IngestResponse{}, NewError(ErrorCodeInvalidRequest, "raw_text is required", nil)
	}
	if s.extractor == nil {
		return IngestResponse{}, NewError(ErrorCodeExtractorUnavailable, "extractor is not configured", nil)
	}

	subjectEntity, err := s.resolveExplicitSubject(ctx, request)
	if err != nil {
		return IngestResponse{}, err
	}

	extractRequest := ExtractRequest{
		RawText: request.RawText,
		Model:   request.ExtractorModel,
	}
	// Passing the explicit subject name helps extractor prompts ground pronouns
	// and implicit references around a known person/entity.
	if subjectEntity != nil {
		extractRequest.SubjectEntityName = subjectEntity.Name
	}

	subjectID := explicitSubjectID(subjectEntity)
	extractorModel := chooseString(request.ExtractorModel, extractRequest.Model)
	extractorLogger.Info("extractor started",
		"subject_entity_id", nullableUUIDString(subjectID),
		"extractor_model", extractorModel,
	)

	candidates, err := s.extractor.Extract(ctx, extractRequest)
	if err != nil {
		extractorLogger.Error("extractor failed",
			"subject_entity_id", nullableUUIDString(subjectID),
			"extractor_model", extractorModel,
			"error", err,
		)
		return IngestResponse{}, err
	}
	extractorLogger.Info("extractor completed",
		"subject_entity_id", nullableUUIDString(subjectID),
		"extractor_model", extractorModel,
		"candidates", len(candidates),
	)

	if len(candidates) == 0 {
		response := IngestResponse{
			ResolutionPlan: ResolutionPlan{Steps: []ResolutionStep{}},
		}
		ingestLogger.Info("ingest completed",
			"ingest_source_id", nil,
			"subject_entity_id", nullableUUIDString(subjectID),
			"extractor_model", extractorModel,
			"candidates", 0,
			"created", 0,
			"updated", 0,
			"merged", 0,
			"no_op", 0,
			"duration_ms", s.now().Sub(startedAt).Milliseconds(),
		)
		return response, nil
	}

	plan, err := s.buildIngestPlan(ctx, subjectEntity, candidates)
	if err != nil {
		resolverLogger.Error("resolution planning failed",
			"subject_entity_id", nullableUUIDString(subjectID),
			"extractor_model", extractorModel,
			"candidates", len(candidates),
			"error", err,
		)
		return IngestResponse{}, err
	}

	response := IngestResponse{
		ResolutionPlan: ResolutionPlan{Steps: flattenSteps(plan.steps)},
	}
	response.EntitiesCreated = countEntitiesToCreate(plan.entityRefs)
	response.RelationshipsCreated = len(plan.relationships)
	for _, step := range plan.steps {
		switch step.response.Action {
		case ResolutionActionCreate:
			response.MemoriesCreated++
		case ResolutionActionUpdate:
			response.MemoriesUpdated++
		case ResolutionActionMerge:
			response.MemoriesMerged++
		case ResolutionActionNoOp:
			response.MemoriesNoOp++
		}
	}
	resolverLogger.Info("resolution plan built",
		"subject_entity_id", nullableUUIDString(subjectID),
		"extractor_model", extractorModel,
		"candidates", len(candidates),
		"created", response.MemoriesCreated,
		"updated", response.MemoriesUpdated,
		"merged", response.MemoriesMerged,
		"no_op", response.MemoriesNoOp,
	)

	if request.DryRun {
		// Dry-run exits before any writes; counts still reflect intended actions.
		ingestLogger.Info("ingest completed",
			"ingest_source_id", nil,
			"subject_entity_id", nullableUUIDString(subjectID),
			"extractor_model", extractorModel,
			"candidates", len(candidates),
			"created", response.MemoriesCreated,
			"updated", response.MemoriesUpdated,
			"merged", response.MemoriesMerged,
			"no_op", response.MemoriesNoOp,
			"duration_ms", s.now().Sub(startedAt).Milliseconds(),
		)
		return response, nil
	}

	var committed []Memory
	// Persist entities, relationships, and memories in one transaction so the
	// ingest result is all-or-nothing.
	if err := s.store.RunInTx(ctx, func(ctx context.Context, tx Store) error {
		if err := createPlannedEntities(ctx, tx, plan); err != nil {
			return err
		}
		if err := createPlannedRelationships(ctx, tx, plan); err != nil {
			return err
		}

		committed, err = executePlannedMemories(ctx, tx, plan)
		if err != nil {
			return err
		}

		return nil
	}); err != nil {
		ingestLogger.Error("ingest commit failed",
			"subject_entity_id", nullableUUIDString(subjectID),
			"extractor_model", extractorModel,
			"candidates", len(candidates),
			"error", err,
			"duration_ms", s.now().Sub(startedAt).Milliseconds(),
		)
		return IngestResponse{}, err
	}

	finalPlan := ResolutionPlan{Steps: flattenSteps(plan.steps)}
	response.IngestSourceID, committed = s.auditIngest(ctx, request, subjectID, extractorModel, finalPlan, committed)
	response.CommittedMemories = committed
	response.ResolutionPlan = finalPlan
	response.MemoriesCreated = 0
	response.MemoriesUpdated = 0
	response.MemoriesMerged = 0
	response.MemoriesNoOp = 0
	for _, step := range plan.steps {
		switch step.response.Action {
		case ResolutionActionCreate:
			response.MemoriesCreated++
		case ResolutionActionUpdate:
			response.MemoriesUpdated++
		case ResolutionActionMerge:
			response.MemoriesMerged++
		case ResolutionActionNoOp:
			response.MemoriesNoOp++
		}
	}

	for _, step := range plan.steps {
		// Invalidate all touched entities so future context reads are rebuilt from
		// source-of-truth data after ingest commits.
		s.invalidateEntityContext(ctx, derefEntityID(step.subjectExisting), derefCreatedEntityID(plan.entityRefs, step.subjectKey))
		for _, key := range step.relatedKeys {
			s.invalidateEntityContext(ctx, derefCreatedEntityID(plan.entityRefs, key))
		}
	}

	ingestLogger.Info("ingest completed",
		"ingest_source_id", nullableUUIDString(response.IngestSourceID),
		"subject_entity_id", nullableUUIDString(subjectID),
		"extractor_model", extractorModel,
		"candidates", len(candidates),
		"created", response.MemoriesCreated,
		"updated", response.MemoriesUpdated,
		"merged", response.MemoriesMerged,
		"no_op", response.MemoriesNoOp,
		"duration_ms", s.now().Sub(startedAt).Milliseconds(),
	)

	return response, nil
}

func (s *Service) resolveExplicitSubject(ctx context.Context, request IngestRequest) (*Entity, error) {
	if request.SubjectEntityID != nil {
		entity, err := s.store.GetEntity(ctx, *request.SubjectEntityID)
		if err != nil {
			return nil, err
		}
		return &entity, nil
	}

	if strings.TrimSpace(request.SubjectExternalID) != "" {
		entity, err := s.store.GetEntityByExternalID(ctx, request.SubjectExternalID)
		if err != nil {
			return nil, err
		}
		return &entity, nil
	}

	return nil, nil
}

func (s *Service) buildIngestPlan(ctx context.Context, explicitSubject *Entity, candidates []CandidateMemory) (ingestPlan, error) {
	plan := ingestPlan{
		entityRefs:    map[string]*entityRef{},
		relationships: map[string]*relationshipPlan{},
	}

	memoryCache := map[string][]Memory{}

	if explicitSubject != nil {
		key := entityKey(explicitSubject.Name)
		copyEntity := *explicitSubject
		plan.entityRefs[key] = &entityRef{key: key, existing: &copyEntity}
	}

	for _, candidate := range candidates {
		if err := validateCandidate(candidate); err != nil {
			return ingestPlan{}, err
		}

		subjectRef, err := s.resolveCandidateSubject(ctx, plan.entityRefs, explicitSubject, candidate)
		if err != nil {
			return ingestPlan{}, err
		}

		step := plannedStep{
			response: ResolutionStep{
				Candidate:       candidate,
				SubjectEntityID: explicitSubjectID(subjectRef.existing),
			},
			subjectKey:      subjectRef.key,
			subjectExisting: subjectRef.existing,
		}

		relatedRefs, err := s.resolveRelatedEntities(ctx, plan.entityRefs, candidate)
		if err != nil {
			return ingestPlan{}, err
		}
		for _, related := range relatedRefs {
			step.relatedKeys = append(step.relatedKeys, related.key)
		}

		relationshipType := strings.TrimSpace(candidate.RelationshipType)
		if relationshipType == "" && candidate.Category == MemoryCategoryRelationship && len(relatedRefs) > 0 {
			relationshipType = "related_to"
		}
		step.relationshipType = relationshipType

		if candidate.Category == MemoryCategoryRelationship && relationshipType != "" && len(relatedRefs) > 0 {
			mergedReason, queued := s.queueRelationships(ctx, plan.relationships, subjectRef, relatedRefs, relationshipType)
			step.response.Action = ResolutionActionMerge
			step.response.Reason = mergedReason
			if !queued {
				step.response.Reason = "Relationship edge already exists for this fact."
			}
			plan.steps = append(plan.steps, step)
			continue
		}

		existingMemories, err := s.cachedSubjectMemories(ctx, memoryCache, subjectRef, candidate.Category)
		if err != nil {
			return ingestPlan{}, err
		}

		step.response.Action, step.response.TargetID, step.response.Reason = resolveMemoryAction(candidate, existingMemories, s.resolveThreshold)

		if relationshipType != "" && len(relatedRefs) > 0 {
			_, _ = s.queueRelationships(ctx, plan.relationships, subjectRef, relatedRefs, relationshipType)
		}

		plan.steps = append(plan.steps, step)
	}

	return plan, nil
}

func (s *Service) cachedSubjectMemories(ctx context.Context, cache map[string][]Memory, subject *entityRef, category MemoryCategory) ([]Memory, error) {
	if subject.existing == nil {
		return nil, nil
	}

	cacheKey := subject.existing.ID.String() + ":" + string(category)
	if cached, ok := cache[cacheKey]; ok {
		return cached, nil
	}

	memories, err := s.store.ListActiveMemoriesByEntityAndCategory(ctx, subject.existing.ID, category)
	if err != nil {
		return nil, err
	}
	cache[cacheKey] = memories
	return memories, nil
}

func (s *Service) resolveCandidateSubject(ctx context.Context, refs map[string]*entityRef, explicitSubject *Entity, candidate CandidateMemory) (*entityRef, error) {
	if explicitSubject != nil {
		key := entityKey(explicitSubject.Name)
		if candidate.Subject == nil || strings.EqualFold(strings.TrimSpace(candidate.Subject.Name), explicitSubject.Name) {
			return refs[key], nil
		}
	}

	if candidate.Subject == nil || strings.TrimSpace(candidate.Subject.Name) == "" {
		return nil, NewError(ErrorCodeInvalidRequest, "candidate subject is required when no explicit subject is provided", nil)
	}

	return s.resolveEntityRef(ctx, refs, *candidate.Subject)
}

func (s *Service) resolveRelatedEntities(ctx context.Context, refs map[string]*entityRef, candidate CandidateMemory) ([]*entityRef, error) {
	result := make([]*entityRef, 0, len(candidate.RelatedEntities))
	for _, related := range candidate.RelatedEntities {
		if strings.TrimSpace(related.Name) == "" {
			continue
		}
		ref, err := s.resolveEntityRef(ctx, refs, related)
		if err != nil {
			return nil, err
		}
		result = append(result, ref)
	}
	return result, nil
}

func (s *Service) resolveEntityRef(ctx context.Context, refs map[string]*entityRef, candidate CandidateEntity) (*entityRef, error) {
	key := entityKey(candidate.Name)
	if ref, ok := refs[key]; ok {
		return ref, nil
	}

	if entity, err := s.store.FindEntityByName(ctx, candidate.Name); err == nil {
		copyEntity := entity
		ref := &entityRef{key: key, existing: &copyEntity}
		refs[key] = ref
		return ref, nil
	}

	entityType := candidate.Type
	if entityType == "" {
		entityType = EntityTypeConcept
	}

	ref := &entityRef{
		key: key,
		create: CreateEntityParams{
			Name:     strings.TrimSpace(candidate.Name),
			Type:     entityType,
			Metadata: map[string]any{"created_via": "ingest"},
		},
	}
	refs[key] = ref
	return ref, nil
}

func (s *Service) queueRelationships(ctx context.Context, plans map[string]*relationshipPlan, subject *entityRef, related []*entityRef, relationshipType string) (string, bool) {
	queued := false
	for _, entity := range related {
		key := subject.key + "->" + entity.key + ":" + relationshipType
		if _, exists := plans[key]; exists {
			continue
		}

		if subject.existing != nil && entity.existing != nil {
			exists, err := s.store.RelationshipExists(ctx, subject.existing.ID, entity.existing.ID, relationshipType)
			if err == nil && exists {
				continue
			}
		}

		plans[key] = &relationshipPlan{
			key:     key,
			fromKey: subject.key,
			toKey:   entity.key,
			typ:     relationshipType,
		}
		queued = true
	}

	if queued {
		return "Queued explicit relationship edge for extracted fact.", true
	}
	return "Relationship edge already exists for this fact.", false
}

func createPlannedEntities(ctx context.Context, tx Store, plan ingestPlan) error {
	keys := make([]string, 0, len(plan.entityRefs))
	for key := range plan.entityRefs {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		ref := plan.entityRefs[key]
		if ref.existing != nil {
			continue
		}
		entity, err := tx.CreateEntity(ctx, ref.create)
		if err != nil {
			return err
		}
		ref.createdID = &entity.ID
		ref.existing = &entity
	}

	return nil
}

func createPlannedRelationships(ctx context.Context, tx Store, plan ingestPlan) error {
	keys := make([]string, 0, len(plan.relationships))
	for key := range plan.relationships {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		relationship := plan.relationships[key]
		from := plan.entityRefs[relationship.fromKey]
		to := plan.entityRefs[relationship.toKey]
		if from == nil || from.existing == nil || to == nil || to.existing == nil {
			return NewError(ErrorCodeStore, "relationship entity resolution failed", map[string]any{"relationship": key})
		}

		exists, err := tx.RelationshipExists(ctx, from.existing.ID, to.existing.ID, relationship.typ)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		created, err := tx.CreateRelationship(ctx, CreateRelationshipParams{
			FromEntityID: from.existing.ID,
			ToEntityID:   to.existing.ID,
			Type:         relationship.typ,
			Metadata:     map[string]any{"created_via": "ingest"},
		})
		if err != nil {
			return err
		}
		relationship.createdID = &created.ID
	}

	return nil
}

func executePlannedMemories(ctx context.Context, tx Store, plan ingestPlan) ([]Memory, error) {
	committed := make([]Memory, 0)
	for i := range plan.steps {
		step := &plan.steps[i]
		subject := plan.entityRefs[step.subjectKey]
		if subject == nil || subject.existing == nil {
			return nil, NewError(ErrorCodeStore, "subject entity missing during ingest commit", map[string]any{"subject_key": step.subjectKey})
		}

		step.response.SubjectEntityID = &subject.existing.ID
		switch step.response.Action {
		case ResolutionActionCreate:
			memory, err := tx.CreateMemory(ctx, CreateMemoryParams{
				EntityID:   subject.existing.ID,
				Content:    step.response.Candidate.Content,
				Category:   step.response.Candidate.Category,
				Confidence: step.response.Candidate.Confidence,
				Metadata:   map[string]any{"created_via": "ingest"},
			})
			if err != nil {
				return nil, err
			}
			step.response.CreatedMemoryID = &memory.ID
			committed = append(committed, memory)
		case ResolutionActionUpdate:
			if step.response.TargetID == nil {
				return nil, NewError(ErrorCodeStore, "missing target memory for update", nil)
			}
			memory, err := tx.UpdateMemory(ctx, *step.response.TargetID, MemoryPatch{
				Content:     stringPtr(step.response.Candidate.Content),
				Confidence:  floatPtr(step.response.Candidate.Confidence),
				SetMetadata: true,
				Metadata:    map[string]any{"updated_via": "ingest"},
			})
			if err != nil {
				return nil, err
			}
			committed = append(committed, memory)
		}

		for _, key := range step.relatedKeys {
			if ref := plan.entityRefs[key]; ref != nil && ref.createdID != nil {
				step.response.CreatedEntityIDs = append(step.response.CreatedEntityIDs, *ref.createdID)
			}
		}
		if ref := plan.entityRefs[step.subjectKey]; ref != nil && ref.createdID != nil {
			step.response.CreatedEntityIDs = append(step.response.CreatedEntityIDs, *ref.createdID)
		}

		for _, relationship := range plan.relationships {
			if relationship.createdID == nil {
				continue
			}
			if relationship.fromKey == step.subjectKey {
				step.response.CreatedRelationID = append(step.response.CreatedRelationID, *relationship.createdID)
			}
		}
	}

	return committed, nil
}

func flattenSteps(steps []plannedStep) []ResolutionStep {
	result := make([]ResolutionStep, 0, len(steps))
	for _, step := range steps {
		result = append(result, step.response)
	}
	return result
}

func countEntitiesToCreate(refs map[string]*entityRef) int {
	total := 0
	for _, ref := range refs {
		if ref.existing == nil {
			total++
		}
	}
	return total
}

func resolveMemoryAction(candidate CandidateMemory, existing []Memory, threshold float64) (ResolutionAction, *uuid.UUID, string) {
	if len(existing) == 0 {
		return ResolutionActionCreate, nil, "No similar existing memory was found."
	}

	bestIndex := -1
	bestScore := 0.0
	for i := range existing {
		score := trigramSimilarity(candidate.Content, existing[i].Content)
		if score > bestScore {
			bestScore = score
			bestIndex = i
		}
	}

	if bestIndex == -1 || bestScore < threshold {
		return ResolutionActionCreate, nil, "Similarity threshold not met, creating a new memory."
	}

	target := existing[bestIndex]
	switch candidate.Category {
	case MemoryCategoryPreference, MemoryCategoryInstruction:
		return ResolutionActionUpdate, &target.ID, "Singleton category supersedes the existing memory."
	default:
		if candidate.Confidence > target.Confidence {
			return ResolutionActionUpdate, &target.ID, "Candidate is more confident than the existing memory."
		}
		if normalizedText(candidate.Content) == normalizedText(target.Content) || bestScore >= 0.98 {
			return ResolutionActionMerge, &target.ID, "Candidate matches an existing memory."
		}
		return ResolutionActionNoOp, &target.ID, "Existing memory is stronger and materially different."
	}
}

func validateCandidate(candidate CandidateMemory) error {
	if strings.TrimSpace(candidate.Content) == "" {
		return NewError(ErrorCodeExtractionFailed, "candidate content is required", nil)
	}
	if err := validateMemoryCategory(candidate.Category); err != nil {
		return err
	}
	if candidate.Confidence < 0 || candidate.Confidence > 1 {
		return NewError(ErrorCodeExtractionFailed, "candidate confidence must be between 0 and 1", nil)
	}
	if candidate.Subject != nil && candidate.Subject.Type != "" {
		if err := validateEntityType(candidate.Subject.Type); err != nil {
			return err
		}
	}
	for _, related := range candidate.RelatedEntities {
		if related.Type != "" {
			if err := validateEntityType(related.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

func trigramSimilarity(a, b string) float64 {
	a = normalizedText(a)
	b = normalizedText(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}

	gramsA := make(map[string]int)
	gramsB := make(map[string]int)
	for _, gram := range trigrams(a) {
		gramsA[gram]++
	}
	for _, gram := range trigrams(b) {
		gramsB[gram]++
	}

	intersection := 0
	total := 0
	for gram, count := range gramsA {
		total += count
		if other, ok := gramsB[gram]; ok {
			if count < other {
				intersection += count
			} else {
				intersection += other
			}
		}
	}
	for _, count := range gramsB {
		total += count
	}
	if total == 0 {
		return 0
	}
	return (2 * float64(intersection)) / float64(total)
}

func normalizedText(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			builder.WriteRune(r)
		case unicode.IsSpace(r):
			builder.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func trigrams(value string) []string {
	padded := "  " + value + "  "
	result := make([]string, 0, len(padded))
	for i := 0; i+3 <= len(padded); i++ {
		result = append(result, padded[i:i+3])
	}
	return result
}

func entityKey(name string) string {
	return normalizedText(name)
}

func chooseString(primary, fallback string) string {
	if strings.TrimSpace(primary) != "" {
		return primary
	}
	return fallback
}

func explicitSubjectID(entity *Entity) *uuid.UUID {
	if entity == nil {
		return nil
	}
	return &entity.ID
}

func derefEntityID(entity *Entity) uuid.UUID {
	if entity == nil {
		return uuid.Nil
	}
	return entity.ID
}

func derefCreatedEntityID(refs map[string]*entityRef, key string) uuid.UUID {
	ref := refs[key]
	if ref == nil || ref.createdID == nil {
		return uuid.Nil
	}
	return *ref.createdID
}

func stringPtr(value string) *string {
	return &value
}

func floatPtr(value float64) *float64 {
	return &value
}

func (s *Service) auditIngest(ctx context.Context, request IngestRequest, subjectID *uuid.UUID, extractorModel string, plan ResolutionPlan, committed []Memory) (*uuid.UUID, []Memory) {
	storeLogger := s.loggerFor(ctx, "store")
	source, err := s.store.CreateIngestSource(ctx, CreateIngestSourceParams{
		RawText:         request.RawText,
		SubjectEntityID: subjectID,
		ExtractorModel:  extractorModel,
		ResolutionPlan:  plan,
	})
	if err != nil {
		storeLogger.Warn("ingest audit creation failed",
			"subject_entity_id", nullableUUIDString(subjectID),
			"extractor_model", extractorModel,
			"error", err,
		)
		return nil, committed
	}

	storeLogger.Info("ingest audit recorded",
		"ingest_source_id", source.ID.String(),
		"subject_entity_id", nullableUUIDString(subjectID),
		"extractor_model", extractorModel,
	)

	audited := append([]Memory(nil), committed...)
	for i := range audited {
		updated, err := s.store.UpdateMemory(ctx, audited[i].ID, MemoryPatch{
			SourceID: &source.ID,
		})
		if err != nil {
			storeLogger.Warn("ingest audit backfill failed",
				"ingest_source_id", source.ID.String(),
				"memory_id", audited[i].ID.String(),
				"error", err,
			)
			continue
		}
		audited[i] = updated
	}

	return &source.ID, audited
}

func nullableUUIDString(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return id.String()
}
