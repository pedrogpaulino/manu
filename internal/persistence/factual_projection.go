package persistence

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

const factualProjectionAttributesVersion = "v1alpha1"

// PreparedFactualProjection is the detached relational projection of one
// validated factual snapshot. It contains no transaction or database state.
type PreparedFactualProjection struct {
	Entities      []Entity
	Relationships []Relationship
}

// PrepareFactualProjection validates and materializes the rebuildable entity
// and relationship projection without performing I/O or mutating input.
func PrepareFactualProjection(input FactualSnapshotInput) (PreparedFactualProjection, error) {
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		return PreparedFactualProjection{}, err
	}

	entities := make(map[string]*factualProjectionEntityAccumulator)
	relationships := make(map[string]*factualProjectionRelationshipAccumulator)
	for _, item := range prepared.Facts {
		factID := item.ExternalID
		subject := item.Fact.Subject
		subjectEntity := factualProjectionEntity(entities, subject)
		subjectEntity.subjectFactIDs[factID] = struct{}{}

		if item.Fact.Object == nil {
			continue
		}
		object := *item.Fact.Object
		objectEntity := factualProjectionEntity(entities, object)
		objectEntity.objectFactIDs[factID] = struct{}{}

		relationshipKey := factualProjectionKey(
			"relationship",
			string(subject.Kind), subject.ID,
			string(item.Fact.Predicate),
			string(object.Kind), object.ID,
		)
		relationship := relationships[relationshipKey]
		if relationship == nil {
			relationship = &factualProjectionRelationshipAccumulator{
				subject:   subject,
				predicate: item.Fact.Predicate,
				object:    object,
				factIDs:   make(map[string]struct{}),
			}
			relationships[relationshipKey] = relationship
		}
		relationship.factIDs[factID] = struct{}{}
	}

	result := PreparedFactualProjection{
		Entities:      make([]Entity, 0, len(entities)),
		Relationships: make([]Relationship, 0, len(relationships)),
	}
	entityIDs := make(map[string]string, len(entities))
	entityKeys := make([]string, 0, len(entities))
	for key := range entities {
		entityKeys = append(entityKeys, key)
	}
	sort.Strings(entityKeys)
	for _, key := range entityKeys {
		item := entities[key]
		entityID := factualProjectionEntityID(prepared.Scope, item.participant)
		entityIDs[key] = entityID
		attributes, err := marshalFactualProjectionAttributes(factualProjectionEntityAttributes{
			Version:        factualProjectionAttributesVersion,
			SubjectFactIDs: sortedFactIDs(item.subjectFactIDs),
			ObjectFactIDs:  sortedFactIDs(item.objectFactIDs),
		})
		if err != nil {
			return PreparedFactualProjection{}, err
		}
		result.Entities = append(result.Entities, Entity{
			ID:         entityID,
			SourceID:   prepared.SourceID,
			SnapshotID: prepared.SnapshotID,
			ExternalID: factualProjectionExternalID("factual-entity:v1", string(item.participant.Kind), item.participant.ID),
			Type:       string(item.participant.Kind),
			Name:       item.participant.ID,
			Attributes: attributes,
		})
	}

	relationshipKeys := make([]string, 0, len(relationships))
	for key := range relationships {
		relationshipKeys = append(relationshipKeys, key)
	}
	sort.Strings(relationshipKeys)
	for _, key := range relationshipKeys {
		item := relationships[key]
		fromKey := factualProjectionKey("entity", string(item.subject.Kind), item.subject.ID)
		toKey := factualProjectionKey("entity", string(item.object.Kind), item.object.ID)
		fromEntityID, fromExists := entityIDs[fromKey]
		toEntityID, toExists := entityIDs[toKey]
		if !fromExists || !toExists {
			return PreparedFactualProjection{}, fmt.Errorf("%w: factual projection participant", ErrInvalidFactualSnapshot)
		}
		attributes, err := marshalFactualProjectionAttributes(factualProjectionRelationshipAttributes{
			Version: factualProjectionAttributesVersion,
			FactIDs: sortedFactIDs(item.factIDs),
		})
		if err != nil {
			return PreparedFactualProjection{}, err
		}
		result.Relationships = append(result.Relationships, Relationship{
			ID:           identity.CanonicalUUID("factual-projection-relationship", prepared.Scope.OrganizationID, prepared.Scope.SourceID, prepared.Scope.SnapshotID, string(item.subject.Kind), item.subject.ID, string(item.predicate), string(item.object.Kind), item.object.ID),
			SourceID:     prepared.SourceID,
			SnapshotID:   prepared.SnapshotID,
			ExternalID:   factualProjectionExternalID("factual-relationship:v1", string(item.subject.Kind), item.subject.ID, string(item.predicate), string(item.object.Kind), item.object.ID),
			FromEntityID: fromEntityID,
			ToEntityID:   toEntityID,
			Type:         string(item.predicate),
			Attributes:   attributes,
		})
	}
	return result, nil
}

// factualProjectionEntityID is the one canonical identity function for
// factual participants. Both the rebuildable relational projection and
// retrieval adapters must use the external scope identities from the fact
// contract, not relational UUIDs or external evidence identities.
func factualProjectionEntityID(scope fact.Scope, participant fact.Participant) string {
	return identity.CanonicalUUID(
		"factual-projection-entity",
		scope.OrganizationID,
		scope.SourceID,
		scope.SnapshotID,
		string(participant.Kind), participant.ID,
	)
}

type factualProjectionEntityAccumulator struct {
	participant    fact.Participant
	subjectFactIDs map[string]struct{}
	objectFactIDs  map[string]struct{}
}

type factualProjectionRelationshipAccumulator struct {
	subject   fact.Participant
	predicate fact.Predicate
	object    fact.Participant
	factIDs   map[string]struct{}
}

type factualProjectionEntityAttributes struct {
	Version        string   `json:"projection_version"`
	SubjectFactIDs []string `json:"subject_fact_ids"`
	ObjectFactIDs  []string `json:"object_fact_ids"`
}

type factualProjectionRelationshipAttributes struct {
	Version string   `json:"projection_version"`
	FactIDs []string `json:"fact_ids"`
}

func factualProjectionEntity(entities map[string]*factualProjectionEntityAccumulator, participant fact.Participant) *factualProjectionEntityAccumulator {
	key := factualProjectionKey("entity", string(participant.Kind), participant.ID)
	item := entities[key]
	if item == nil {
		item = &factualProjectionEntityAccumulator{
			participant:    participant,
			subjectFactIDs: make(map[string]struct{}),
			objectFactIDs:  make(map[string]struct{}),
		}
		entities[key] = item
	}
	return item
}

func factualProjectionKey(prefix string, values ...string) string {
	return factualProjectionExternalID(prefix, values...)
}

func factualProjectionExternalID(prefix string, values ...string) string {
	var builder strings.Builder
	builder.WriteString(prefix)
	for _, value := range values {
		builder.WriteByte(':')
		builder.WriteString(strconv.Itoa(len(value)))
		builder.WriteByte(':')
		builder.WriteString(value)
	}
	return builder.String()
}

func sortedFactIDs(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func marshalFactualProjectionAttributes(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: factual projection attributes", ErrInvalidFactualSnapshot)
	}
	return append(json.RawMessage(nil), encoded...), nil
}
