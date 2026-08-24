package persistence

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

func TestPrepareFactualProjectionAggregatesDeterministically(t *testing.T) {
	input := factualProjectionInput(t)
	original := cloneFactualSnapshotInput(input)
	first, err := PrepareFactualProjection(input)
	if err != nil {
		t.Fatalf("first projection preparation: %v", err)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("PrepareFactualProjection() mutated its input")
	}

	permuted := cloneFactualSnapshotInput(input)
	sort.Slice(permuted.Facts, func(left, right int) bool {
		return permuted.Facts[left].ID > permuted.Facts[right].ID
	})
	second, err := PrepareFactualProjection(permuted)
	if err != nil {
		t.Fatalf("permuted projection preparation: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("fact permutation changed projection:\nfirst=%#v\nsecond=%#v", first, second)
	}

	if len(first.Entities) != 3 {
		t.Fatalf("entity count = %d, want 3", len(first.Entities))
	}
	if len(first.Relationships) != 1 {
		t.Fatalf("relationship count = %d, want one aggregated edge", len(first.Relationships))
	}

	entities := make(map[string]Entity, len(first.Entities))
	for _, entity := range first.Entities {
		entities[entity.Type+"\x00"+entity.Name] = entity
		if entity.ExternalID == "" || !strings.HasPrefix(entity.ExternalID, "factual-entity:v1:") {
			t.Fatalf("entity external ID = %q, want deterministic factual prefix", entity.ExternalID)
		}
		assertCanonicalProjectionJSON(t, entity.Attributes)
	}
	symbolEntity := entities[string(fact.ParticipantSymbol)+"\x00Shared"]
	artifactEntity := entities[string(fact.ParticipantArtifact)+"\x00Shared"]
	targetEntity := entities[string(fact.ParticipantNamedElement)+"\x00Target"]
	if symbolEntity.ID == "" || artifactEntity.ID == "" || targetEntity.ID == "" {
		t.Fatalf("entities by typed participant = %#v", entities)
	}
	if symbolEntity.ID == artifactEntity.ID || symbolEntity.ExternalID == artifactEntity.ExternalID {
		t.Fatal("participant kinds with the same ID collided")
	}
	if symbolEntity.Type != string(fact.ParticipantSymbol) || symbolEntity.Name != "Shared" {
		t.Fatalf("symbol entity identity = %#v", symbolEntity)
	}
	if got := symbolEntity.ID; got != identity.CanonicalUUID("factual-projection-entity", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, string(fact.ParticipantSymbol), "Shared") {
		t.Fatalf("symbol entity ID = %q, want deterministic ID %q", got, identity.CanonicalUUID("factual-projection-entity", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, string(fact.ParticipantSymbol), "Shared"))
	}

	var symbolAttributes factualProjectionEntityAttributes
	if err := json.Unmarshal(symbolEntity.Attributes, &symbolAttributes); err != nil {
		t.Fatalf("decode symbol attributes: %v", err)
	}
	if len(symbolAttributes.SubjectFactIDs) != 3 || len(symbolAttributes.ObjectFactIDs) != 0 {
		t.Fatalf("symbol support attributes = %#v, want three subject facts and no object facts", symbolAttributes)
	}
	var targetAttributes factualProjectionEntityAttributes
	if err := json.Unmarshal(targetEntity.Attributes, &targetAttributes); err != nil {
		t.Fatalf("decode target attributes: %v", err)
	}
	if len(targetAttributes.SubjectFactIDs) != 0 || len(targetAttributes.ObjectFactIDs) != 2 {
		t.Fatalf("target support attributes = %#v, want two object facts", targetAttributes)
	}
	var artifactAttributes factualProjectionEntityAttributes
	if err := json.Unmarshal(artifactEntity.Attributes, &artifactAttributes); err != nil {
		t.Fatalf("decode artifact attributes: %v", err)
	}
	if len(artifactAttributes.SubjectFactIDs) != 1 || len(artifactAttributes.ObjectFactIDs) != 0 {
		t.Fatalf("artifact support attributes = %#v, want one subject fact", artifactAttributes)
	}
	if symbolAttributes.Version != factualProjectionAttributesVersion || targetAttributes.Version != factualProjectionAttributesVersion || artifactAttributes.Version != factualProjectionAttributesVersion {
		t.Fatalf("entity attribute versions = %q/%q/%q", symbolAttributes.Version, targetAttributes.Version, artifactAttributes.Version)
	}

	relationship := first.Relationships[0]
	if relationship.FromEntityID != symbolEntity.ID || relationship.ToEntityID != targetEntity.ID || relationship.Type != string(fact.PredicateDependency) {
		t.Fatalf("aggregated relationship = %#v", relationship)
	}
	if !strings.HasPrefix(relationship.ExternalID, "factual-relationship:v1:") {
		t.Fatalf("relationship external ID = %q, want deterministic factual prefix", relationship.ExternalID)
	}
	var relationshipAttributes factualProjectionRelationshipAttributes
	if err := json.Unmarshal(relationship.Attributes, &relationshipAttributes); err != nil {
		t.Fatalf("decode relationship attributes: %v", err)
	}
	expectedFactIDs := []string{input.Facts[1].ID, input.Facts[2].ID}
	sort.Strings(expectedFactIDs)
	if len(relationshipAttributes.FactIDs) != 2 || !reflect.DeepEqual(relationshipAttributes.FactIDs, expectedFactIDs) || relationshipAttributes.Version != factualProjectionAttributesVersion {
		t.Fatalf("relationship support attributes = %#v, want two fact IDs and version", relationshipAttributes)
	}

	if got := countProjectionRelationshipsForPredicate(first.Relationships, fact.PredicateDefinition); got != 0 {
		t.Fatalf("unary/value predicate produced %d relationships", got)
	}
}

func TestPrepareFactualProjectionChangesIdentityWithScope(t *testing.T) {
	input := factualProjectionInput(t)
	first, err := PrepareFactualProjection(input)
	if err != nil {
		t.Fatalf("first projection preparation: %v", err)
	}

	other := cloneFactualSnapshotInput(input)
	other.Scope = fact.Scope{OrganizationID: "other-organization", SourceID: "other-source", SnapshotID: "other-snapshot"}
	other.OrganizationID = identity.CanonicalUUID("organization", other.Scope.OrganizationID)
	other.SourceID = identity.CanonicalUUID("source", other.Scope.OrganizationID, other.Scope.SourceID)
	other.SnapshotID = identity.CanonicalUUID("snapshot", other.Scope.OrganizationID, other.Scope.SourceID, other.Scope.SnapshotID)
	for index := range other.Facts {
		other.Facts[index].Scope = other.Scope
		other.Facts[index].ID = mustFactID(other.Facts[index])
	}
	second, err := PrepareFactualProjection(other)
	if err != nil {
		t.Fatalf("other-scope projection preparation: %v", err)
	}
	if len(first.Entities) == 0 || len(second.Entities) == 0 || first.Entities[0].ID == second.Entities[0].ID {
		t.Fatalf("scope did not change entity identity: first=%#v second=%#v", first.Entities, second.Entities)
	}
	if len(first.Relationships) == 0 || len(second.Relationships) == 0 || first.Relationships[0].ID == second.Relationships[0].ID {
		t.Fatalf("scope did not change relationship identity: first=%#v second=%#v", first.Relationships, second.Relationships)
	}
}

func TestPrepareFactualProjectionRejectsInvalidInputWithoutPartialOutput(t *testing.T) {
	base := factualProjectionInput(t)
	tests := []struct {
		name  string
		input func() FactualSnapshotInput
	}{
		{
			name: "invalid scope",
			input: func() FactualSnapshotInput {
				candidate := cloneFactualSnapshotInput(base)
				candidate.Scope.SourceID = ""
				return candidate
			},
		},
		{
			name: "invalid fact predicate",
			input: func() FactualSnapshotInput {
				candidate := cloneFactualSnapshotInput(base)
				candidate.Facts[0].Predicate = fact.Predicate("unknown")
				return candidate
			},
		},
		{
			name: "malformed rule configuration",
			input: func() FactualSnapshotInput {
				candidate := cloneFactualSnapshotInput(base)
				candidate.RuleVersions = []RuleVersion{{
					RuleID: "rule", Version: "1", ImplementationDigest: strings.Repeat("a", 64),
					Configuration: json.RawMessage(`{"secret":"unterminated`),
				}}
				return candidate
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := tt.input()
			original := cloneFactualSnapshotInput(input)
			got, err := PrepareFactualProjection(input)
			if err == nil {
				t.Fatal("PrepareFactualProjection() error = nil")
			}
			if !errors.Is(err, ErrInvalidFactualSnapshot) || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want factual snapshot/input classification", err)
			}
			if !reflect.DeepEqual(got, PreparedFactualProjection{}) {
				t.Fatalf("invalid preparation returned partial output: %#v", got)
			}
			if !reflect.DeepEqual(input, original) {
				t.Fatal("PrepareFactualProjection() mutated its input")
			}
		})
	}
}

func TestPrepareFactualProjectionAttributesAreCanonicalObjects(t *testing.T) {
	prepared, err := PrepareFactualProjection(factualProjectionInput(t))
	if err != nil {
		t.Fatalf("PrepareFactualProjection() error = %v", err)
	}
	for _, entity := range prepared.Entities {
		assertCanonicalProjectionJSON(t, entity.Attributes)
	}
	for _, relationship := range prepared.Relationships {
		assertCanonicalProjectionJSON(t, relationship.Attributes)
	}
}

func factualProjectionInput(t *testing.T) FactualSnapshotInput {
	t.Helper()
	scope := fact.Scope{OrganizationID: "organization-local", SourceID: "source-app", SnapshotID: "snapshot-1"}
	java := fact.Producer{ID: "java", Version: "1", Method: "symbols"}
	wso2 := fact.Producer{ID: "wso2", Version: "2", Method: "elements"}
	return FactualSnapshotInput{
		OrganizationID: identity.CanonicalUUID("organization", scope.OrganizationID),
		SourceID:       identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
		SnapshotID:     identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
		Scope:          scope,
		FrontendManifests: []fact.FrontendManifest{
			validFrontendManifest("java", "1", "symbols"),
			validFrontendManifest("wso2", "2", "elements"),
		},
		Facts: []fact.CanonicalFact{
			factualProjectionFact(scope, fact.ParticipantSymbol, "Shared", fact.PredicateDefinition, nil, java, "definition"),
			factualProjectionFact(scope, fact.ParticipantSymbol, "Shared", fact.PredicateDependency, &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "Target"}, java, "dependency-java"),
			factualProjectionFact(scope, fact.ParticipantSymbol, "Shared", fact.PredicateDependency, &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "Target"}, wso2, "dependency-wso2"),
			factualProjectionFact(scope, fact.ParticipantArtifact, "Shared", fact.PredicateArtifact, nil, java, "artifact"),
		},
	}
}

func factualProjectionFact(scope fact.Scope, subjectKind fact.ParticipantKind, subjectID string, predicate fact.Predicate, object *fact.Participant, producer fact.Producer, evidenceID string) fact.CanonicalFact {
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: predicate,
		Subject:   fact.Participant{Kind: subjectKind, ID: subjectID},
		Object:    object,
		Producer:  producer,
		Evidence:  []fact.EvidenceRef{{ID: "evidence-" + evidenceID, Locator: contract.Locator{Path: evidenceID + ".java", StartLine: 1, EndLine: 1}}},
	}
	if object == nil {
		candidate.Value = &fact.TypedValue{Kind: fact.ValueString, String: evidenceID}
	}
	candidate.ID = mustFactID(candidate)
	return candidate
}

func countProjectionRelationshipsForPredicate(relationships []Relationship, predicate fact.Predicate) int {
	count := 0
	for _, relationship := range relationships {
		if relationship.Type == string(predicate) {
			count++
		}
	}
	return count
}

func assertCanonicalProjectionJSON(t *testing.T, raw json.RawMessage) {
	t.Helper()
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		t.Fatalf("projection attributes are not a JSON object: %s", raw)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode projection attributes: %v", err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal projection attributes: %v", err)
	}
	if !bytesEqualJSON(raw, canonical) {
		t.Fatalf("projection attributes are not canonical JSON: got %s canonical %s", raw, canonical)
	}
}

func bytesEqualJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, _ := json.Marshal(leftValue)
	rightCanonical, _ := json.Marshal(rightValue)
	return string(leftCanonical) == string(rightCanonical)
}
