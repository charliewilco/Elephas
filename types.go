package elephas

import (
	"time"

	"github.com/google/uuid"
)

type MemoryCategory string

const (
	MemoryCategoryPreference   MemoryCategory = "preference"
	MemoryCategoryFact         MemoryCategory = "fact"
	MemoryCategoryRelationship MemoryCategory = "relationship"
	MemoryCategoryEvent        MemoryCategory = "event"
	MemoryCategoryInstruction  MemoryCategory = "instruction"
)

type EntityType string

const (
	EntityTypePerson       EntityType = "person"
	EntityTypeOrganization EntityType = "organization"
	EntityTypePlace        EntityType = "place"
	EntityTypeConcept      EntityType = "concept"
	EntityTypeObject       EntityType = "object"
	EntityTypeAgent        EntityType = "agent"
)

type ResolutionAction string

const (
	ResolutionActionCreate ResolutionAction = "create"
	ResolutionActionUpdate ResolutionAction = "update"
	ResolutionActionMerge  ResolutionAction = "merge"
	ResolutionActionNoOp   ResolutionAction = "no_op"
)

type Memory struct {
	ID         uuid.UUID      `json:"id"`
	EntityID   uuid.UUID      `json:"entity_id"`
	Content    string         `json:"content"`
	Category   MemoryCategory `json:"category"`
	Confidence float64        `json:"confidence"`
	SourceID   *uuid.UUID     `json:"source_id,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ExpiresAt  *time.Time     `json:"expires_at,omitempty"`
	Metadata   map[string]any `json:"metadata"`
}

type Entity struct {
	ID         uuid.UUID      `json:"id"`
	Name       string         `json:"name"`
	Type       EntityType     `json:"type"`
	ExternalID *string        `json:"external_id,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	Metadata   map[string]any `json:"metadata"`
}

type Relationship struct {
	ID           uuid.UUID      `json:"id"`
	FromEntityID uuid.UUID      `json:"from_entity_id"`
	ToEntityID   uuid.UUID      `json:"to_entity_id"`
	Type         string         `json:"type"`
	Weight       *float64       `json:"weight,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	Metadata     map[string]any `json:"metadata"`
}

type IngestSource struct {
	ID              uuid.UUID      `json:"id"`
	RawText         string         `json:"raw_text"`
	SubjectEntityID *uuid.UUID     `json:"subject_entity_id,omitempty"`
	ExtractorModel  string         `json:"extractor_model"`
	ResolutionPlan  ResolutionPlan `json:"resolution_plan"`
	CreatedAt       time.Time      `json:"created_at"`
}

type CandidateEntity struct {
	Name string     `json:"name"`
	Type EntityType `json:"type"`
}

type CandidateMemory struct {
	Content          string            `json:"content"`
	Category         MemoryCategory    `json:"category"`
	Confidence       float64           `json:"confidence"`
	Subject          *CandidateEntity  `json:"subject,omitempty"`
	RelatedEntities  []CandidateEntity `json:"related_entities,omitempty"`
	RelationshipType string            `json:"relationship_type,omitempty"`
}

type ResolutionStep struct {
	Action            ResolutionAction `json:"action"`
	Candidate         CandidateMemory  `json:"candidate"`
	TargetID          *uuid.UUID       `json:"target_id,omitempty"`
	Reason            string           `json:"reason"`
	SubjectEntityID   *uuid.UUID       `json:"subject_entity_id,omitempty"`
	CreatedEntityIDs  []uuid.UUID      `json:"created_entity_ids,omitempty"`
	CreatedMemoryID   *uuid.UUID       `json:"created_memory_id,omitempty"`
	CreatedRelationID []uuid.UUID      `json:"created_relationship_ids,omitempty"`
}

type ResolutionPlan struct {
	Steps []ResolutionStep `json:"steps"`
}

type ResolvedRelationship struct {
	Relationship  Relationship `json:"relationship"`
	RelatedEntity Entity       `json:"related_entity"`
}

type EntityContext struct {
	Entity        Entity                 `json:"entity"`
	Memories      []Memory               `json:"memories"`
	Relationships []ResolvedRelationship `json:"relationships"`
}

type MemorySearchResult struct {
	Memory        Memory                 `json:"memory"`
	Score         float64                `json:"score"`
	Entity        *Entity                `json:"entity,omitempty"`
	Relationships []ResolvedRelationship `json:"relationships,omitempty"`
}

type PathNode struct {
	Entity       Entity        `json:"entity"`
	Relationship *Relationship `json:"relationship,omitempty"`
}

type Stats struct {
	EntityCount       int64  `json:"entity_count"`
	MemoryCount       int64  `json:"memory_count"`
	RelationshipCount int64  `json:"relationship_count"`
	IngestSourceCount int64  `json:"ingest_source_count"`
	Backend           string `json:"backend"`
}

type Page[T any] struct {
	Data    []T  `json:"data"`
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

type CreateMemoryParams struct {
	EntityID   uuid.UUID
	Content    string
	Category   MemoryCategory
	Confidence float64
	SourceID   *uuid.UUID
	ExpiresAt  *time.Time
	Metadata   map[string]any
}

type MemoryPatch struct {
	Content        *string
	Confidence     *float64
	ExpiresAt      *time.Time
	ClearExpiresAt bool
	Metadata       map[string]any
	SetMetadata    bool
	SourceID       *uuid.UUID
	ClearSourceID  bool
}

type MemoryFilter struct {
	EntityID       *uuid.UUID
	Category       *MemoryCategory
	IncludeExpired bool
	Since          *time.Time
	Limit          int
	Offset         int
}

type SearchQuery struct {
	Query                string
	EntityID             *uuid.UUID
	Categories           []MemoryCategory
	IncludeExpired       bool
	IncludeEntityContext bool
	Limit                int
}

type CreateEntityParams struct {
	Name       string
	Type       EntityType
	ExternalID *string
	Metadata   map[string]any
}

type EntityPatch struct {
	Name            *string
	Type            *EntityType
	ExternalID      *string
	ClearExternalID bool
	Metadata        map[string]any
	SetMetadata     bool
}

type EntityFilter struct {
	Name       string
	Type       *EntityType
	ExternalID string
	Limit      int
	Offset     int
}

type CreateRelationshipParams struct {
	FromEntityID uuid.UUID
	ToEntityID   uuid.UUID
	Type         string
	Weight       *float64
	Metadata     map[string]any
}

type RelationshipFilter struct {
	FromEntityID *uuid.UUID
	ToEntityID   *uuid.UUID
	Type         string
	Limit        int
	Offset       int
}

type CreateIngestSourceParams struct {
	RawText         string
	SubjectEntityID *uuid.UUID
	ExtractorModel  string
	ResolutionPlan  ResolutionPlan
}

type ExtractRequest struct {
	RawText              string
	SubjectEntityName    string
	Model                string
	SystemPromptOverride string
}

type IngestRequest struct {
	RawText           string
	SubjectEntityID   *uuid.UUID
	SubjectExternalID string
	ExtractorModel    string
	DryRun            bool
}

type IngestResponse struct {
	IngestSourceID       *uuid.UUID     `json:"ingest_source_id,omitempty"`
	MemoriesCreated      int            `json:"memories_created"`
	MemoriesUpdated      int            `json:"memories_updated"`
	MemoriesMerged       int            `json:"memories_merged"`
	MemoriesNoOp         int            `json:"memories_no_op"`
	EntitiesCreated      int            `json:"entities_created"`
	RelationshipsCreated int            `json:"relationships_created"`
	ResolutionPlan       ResolutionPlan `json:"resolution_plan"`
	CommittedMemories    []Memory       `json:"committed_memories"`
}
