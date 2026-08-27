package query

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestProjectContextCandidatesCompleteProjection(t *testing.T) {
	scope := packageTestScope()
	unit := packageTestUnit(scope, "projection.go", "src/projection.go", "complete projection")
	originalScope := fact.Scope{OrganizationID: "organization-external", SourceID: "source-external", SnapshotID: "snapshot-external"}
	original := contextProjectionTestFact(originalScope, "bundle-evidence-complete", unit.Locator, fact.PredicateDefinition, "symbol-complete", nil)
	request := ContextCandidateProjectionRequest{
		Scope: scope,
		Facts: []fact.CanonicalFact{original},
		Retrieval: QueryRetrievalResult{Candidates: []PackageCandidate{
			contextProjectionTestCandidate(scope, unit, "bundle-evidence-complete", 0.73, 2, false),
		}},
	}

	got, err := ProjectContextCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("ProjectContextCandidates() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projection Validate() error = %v", err)
	}
	if got.SupportIncomplete || len(got.Candidates) != 3 || len(got.Relations) != 0 {
		t.Fatalf("projection shape = candidates %d relations %d incomplete %v", len(got.Candidates), len(got.Relations), got.SupportIncomplete)
	}

	var evidenceCandidate, factCandidate, entityCandidate *ContextSelectionCandidate
	for index := range got.Candidates {
		candidate := &got.Candidates[index]
		switch candidate.Item.Kind {
		case ContextItemEvidence:
			evidenceCandidate = candidate
		case ContextItemFact:
			factCandidate = candidate
		case ContextItemEntity:
			entityCandidate = candidate
		}
		estimate, estimateErr := EstimateContextSelectionCandidateCosts(context.Background(), candidateClone(candidate), DefaultContextTokenEstimatorConfiguration(), ContextTokenEstimationLimits{})
		if estimateErr != nil {
			t.Fatalf("candidate %s cost recomputation error = %v", candidate.Item.ID, estimateErr)
		}
		if candidate.TokenCost != estimate.TokenEstimate || candidate.CharacterCost != estimate.Characters || candidate.ByteCost != estimate.Bytes {
			t.Fatalf("candidate %s costs = %d/%d/%d, want %d/%d/%d", candidate.Item.ID, candidate.TokenCost, candidate.CharacterCost, candidate.ByteCost, estimate.TokenEstimate, estimate.Characters, estimate.Bytes)
		}
	}
	if evidenceCandidate == nil || factCandidate == nil || entityCandidate == nil {
		t.Fatalf("projection did not contain evidence, fact and entity candidates")
	}
	if evidenceCandidate.Item.Evidence.ID != unit.ID || evidenceCandidate.Item.Scope != scope {
		t.Fatalf("evidence projection = %+v", evidenceCandidate.Item)
	}
	projected := factCandidate.Item.Fact
	if projected.Scope != (fact.Scope{OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID}) || projected.Evidence[0].ID != unit.ID {
		t.Fatalf("fact projection scope/evidence = %+v", projected)
	}
	wantFactID := *projected
	wantFactID.ID = ""
	wantID, err := fact.FactID(wantFactID)
	if err != nil || projected.ID != wantID {
		t.Fatalf("projected fact id = %q, want %q (err %v)", projected.ID, wantID, err)
	}
	if entityCandidate.Item.Entity.ID != "symbol-complete" || entityCandidate.Item.Locator != unit.Locator {
		t.Fatalf("entity projection = %+v", entityCandidate.Item)
	}
}

func TestProjectContextCandidatesEvidencePartialDedupAndOrder(t *testing.T) {
	scope := packageTestScope()
	first := packageTestUnit(scope, "projection-a.go", "src/projection-a.go", "first")
	second := packageTestUnit(scope, "projection-b.go", "src/projection-b.go", "second")
	partialFact := contextProjectionTestFact(
		fact.Scope{OrganizationID: "organization-external", SourceID: "source-external", SnapshotID: "snapshot-external"},
		"bundle-first", first.Locator, fact.PredicateReference, "symbol-partial", []contextProjectionFactEvidence{
			{ID: "bundle-second", Locator: second.Locator},
			{ID: "bundle-missing", Locator: second.Locator},
		},
	)

	request := ContextCandidateProjectionRequest{
		Scope: scope,
		Facts: []fact.CanonicalFact{partialFact},
		Retrieval: QueryRetrievalResult{Candidates: []PackageCandidate{
			contextProjectionTestCandidate(scope, first, "bundle-first", 0.90, 1, false),
			contextProjectionTestCandidate(scope, first, "bundle-first", 0.30, 3, false),
		}},
	}
	got, err := ProjectContextCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("ProjectContextCandidates() error = %v", err)
	}
	if !got.SupportIncomplete || len(got.Candidates) != 1 {
		t.Fatalf("partial projection shape = candidates %d incomplete %v", len(got.Candidates), got.SupportIncomplete)
	}
	for _, candidate := range got.Candidates {
		if candidate.Item.Kind == ContextItemFact || candidate.Item.Kind == ContextItemEntity {
			t.Fatalf("partial fact unexpectedly projected: %+v", candidate.Item)
		}
	}
	for index := 1; index < len(got.Candidates); index++ {
		left, right := got.Candidates[index-1], got.Candidates[index]
		if left.Rank > right.Rank || left.Rank == right.Rank && (left.Item.Kind > right.Item.Kind || left.Item.Kind == right.Item.Kind && left.Item.ID > right.Item.ID) {
			t.Fatalf("candidate order is not rank/kind/id stable: %+v then %+v", left, right)
		}
	}
	var firstCandidate *ContextSelectionCandidate
	for index := range got.Candidates {
		if got.Candidates[index].Item.ID == first.ID {
			firstCandidate = &got.Candidates[index]
		}
	}
	if firstCandidate == nil || firstCandidate.Rank != 1 || firstCandidate.Relevance != 0.90 {
		t.Fatalf("deduplicated first candidate = %+v", firstCandidate)
	}
}

func TestProjectContextCandidatesRelationLineageAndCosts(t *testing.T) {
	scope := packageTestScope()
	baseUnit := packageTestUnit(scope, "projection-base.go", "src/projection-base.go", "base")
	derivedUnit := packageTestUnit(scope, "projection-derived.go", "src/projection-derived.go", "derived")
	originalScope := fact.Scope{OrganizationID: "organization-external", SourceID: "source-external", SnapshotID: "snapshot-external"}
	base := contextProjectionTestFact(originalScope, "bundle-base", baseUnit.Locator, fact.PredicateDefinition, "symbol-base", nil)
	derived := contextProjectionTestFact(originalScope, "bundle-derived", derivedUnit.Locator, fact.PredicateReference, "symbol-base", nil)
	derived.Object = &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "element-derived"}
	derived.Lineage = &fact.Lineage{RuleID: "rule-projection", RuleVersion: "v1", InputFactIDs: []string{base.ID}}
	derived.ID = ""
	derived.ID, _ = fact.FactID(derived)
	request := ContextCandidateProjectionRequest{
		Scope: scope,
		Facts: []fact.CanonicalFact{derived, base},
		Retrieval: QueryRetrievalResult{Candidates: []PackageCandidate{
			contextProjectionTestCandidate(scope, baseUnit, "bundle-base", 0.60, 2, false),
			contextProjectionTestCandidate(scope, derivedUnit, "bundle-derived", 0.80, 1, true),
		}},
	}

	got, err := ProjectContextCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("ProjectContextCandidates() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projection Validate() error = %v", err)
	}
	if len(got.Relations) != 1 || got.Relations[0].Relation.FromID != "symbol-base" || got.Relations[0].Relation.ToID != "element-derived" {
		t.Fatalf("relations = %+v", got.Relations)
	}
	relation := got.Relations[0]
	if relation.Relation.Path[0] != relation.Relation.FromID || relation.Relation.Path[len(relation.Relation.Path)-1] != relation.Relation.ToID {
		t.Fatalf("relation path = %v", relation.Relation.Path)
	}
	if relation.Rank != 1 || relation.Score != 0.80 || len(relation.Relation.SupportIDs) != 2 {
		t.Fatalf("relation ranking/support = %+v", relation)
	}
	relationEstimate, err := EstimateContextRelationCandidateCosts(context.Background(), relationClone(&relation), DefaultContextTokenEstimatorConfiguration(), ContextTokenEstimationLimits{})
	if err != nil {
		t.Fatalf("relation cost recomputation error = %v", err)
	}
	if relation.TokenCost != relationEstimate.TokenEstimate || relation.CharacterCost != relationEstimate.Characters || relation.ByteCost != relationEstimate.Bytes {
		t.Fatalf("relation costs = %d/%d/%d, want %d/%d/%d", relation.TokenCost, relation.CharacterCost, relation.ByteCost, relationEstimate.TokenEstimate, relationEstimate.Characters, relationEstimate.Bytes)
	}
	var projectedBaseID string
	for _, candidate := range got.Candidates {
		if candidate.Item.Kind == ContextItemFact && candidate.Item.Fact.Subject.ID == "symbol-base" {
			projectedBaseID = candidate.Item.ID
		}
	}
	if projectedBaseID == "" {
		t.Fatalf("projected base fact not found")
	}
	for _, candidate := range got.Candidates {
		if candidate.Item.Kind == ContextItemFact && candidate.Item.Fact.Lineage != nil {
			if candidate.Item.Origin != ContextKnowledgeDerived || candidate.Item.Fact.Lineage.InputFactIDs[0] != projectedBaseID {
				t.Fatalf("derived lineage = %+v, base id %q", candidate.Item.Fact.Lineage, projectedBaseID)
			}
		}
	}
}

func TestProjectContextCandidatesOmitsSelfReferentialRelation(t *testing.T) {
	scope := packageTestScope()
	unit := packageTestUnit(scope, "projection-self.go", "src/projection-self.go", "self reference")
	factValue := contextProjectionTestFact(scopeAsFact(scope), "bundle-self", unit.Locator, fact.PredicateReference, "symbol-self", nil)
	factValue.Object = &fact.Participant{Kind: fact.ParticipantSymbol, ID: "symbol-self"}
	factValue.ID = ""
	factValue.ID, _ = fact.FactID(factValue)
	request := ContextCandidateProjectionRequest{
		Scope: scope,
		Facts: []fact.CanonicalFact{factValue},
		Retrieval: QueryRetrievalResult{Candidates: []PackageCandidate{
			contextProjectionTestCandidate(scope, unit, "bundle-self", 0.7, 1, true),
		}},
	}
	got, err := ProjectContextCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("ProjectContextCandidates() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("projection Validate() error = %v", err)
	}
	if len(got.Relations) != 0 || len(got.Candidates) != 3 {
		t.Fatalf("self-referential projection = candidates %d relations %d", len(got.Candidates), len(got.Relations))
	}
}

func TestProjectContextCandidatesRejectsConflictCancellationAndPreservesInputs(t *testing.T) {
	scope := packageTestScope()
	first := packageTestUnit(scope, "projection-conflict-a.go", "src/projection-conflict-a.go", "first")
	second := packageTestUnit(scope, "projection-conflict-b.go", "src/projection-conflict-b.go", "second")
	conflictRequest := ContextCandidateProjectionRequest{
		Scope: scope,
		Retrieval: QueryRetrievalResult{Candidates: []PackageCandidate{
			contextProjectionTestCandidate(scope, first, "bundle-conflict", 0.9, 1, false),
			contextProjectionTestCandidate(scope, second, "bundle-conflict", 0.8, 2, false),
		}},
	}
	if _, err := ProjectContextCandidates(context.Background(), conflictRequest); !errors.Is(err, ErrInvalidContextCandidateProjection) {
		t.Fatalf("conflicting identities error = %v", err)
	}

	unit := packageTestUnit(scope, "projection-immutable.go", "src/projection-immutable.go", "immutable")
	originalScope := fact.Scope{OrganizationID: "organization-external", SourceID: "source-external", SnapshotID: "snapshot-external"}
	factValue := contextProjectionTestFact(originalScope, "bundle-immutable", unit.Locator, fact.PredicateDefinition, "symbol-immutable", nil)
	request := ContextCandidateProjectionRequest{
		Scope: scope,
		Facts: []fact.CanonicalFact{factValue},
		Retrieval: QueryRetrievalResult{Candidates: []PackageCandidate{
			contextProjectionTestCandidate(scope, unit, "bundle-immutable", 0.4, 1, false),
		}},
	}
	before := reflect.DeepEqual(request, request.CloneForTest())
	if !before {
		t.Fatalf("test request clone differs before projection")
	}
	requestBefore := cloneContextProjectionRequest(request)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProjectContextCandidates(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled projection error = %v", err)
	}
	got, err := ProjectContextCandidates(context.Background(), request)
	if err != nil {
		t.Fatalf("ProjectContextCandidates() error = %v", err)
	}
	if !reflect.DeepEqual(request, requestBefore) {
		t.Fatalf("projection mutated request")
	}
	got.Candidates[0].Item.ID = "mutated-result"
	if !reflect.DeepEqual(request, requestBefore) {
		t.Fatalf("result unexpectedly aliases request")
	}
}

type contextProjectionFactEvidence struct {
	ID      string
	Locator contract.Locator
}

func contextProjectionTestFact(scope fact.Scope, externalID string, locator contract.Locator, predicate fact.Predicate, subjectID string, extra []contextProjectionFactEvidence) fact.CanonicalFact {
	locator.SourceID = scope.SourceID
	evidenceRefs := []fact.EvidenceRef{{ID: externalID, Locator: locator}}
	for _, reference := range extra {
		reference.Locator.SourceID = scope.SourceID
		evidenceRefs = append(evidenceRefs, fact.EvidenceRef{ID: reference.ID, Locator: reference.Locator})
	}
	value := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: predicate,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
		Producer:  fact.Producer{ID: "frontend-projection", Version: "v1", Method: "test"},
		Evidence:  evidenceRefs,
	}
	value.ID, _ = fact.FactID(value)
	return value
}

func contextProjectionTestCandidate(scope Scope, unit evidence.EvidenceUnit, externalID string, score float64, rank int, relation bool) PackageCandidate {
	fusionID := packageTestUUID(byte(rank + 20))
	fusion := retrieval.FusionCandidate{
		EvidenceID:     fusionID,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Score:          score,
		Rank:           rank,
		Provenance: retrieval.FusionProvenance{
			EvidenceID:          fusionID,
			OrganizationID:      scope.OrganizationID,
			SourceID:            scope.SourceID,
			SnapshotID:          scope.SnapshotID,
			EvidenceContentHash: unit.ContentHash,
		},
	}
	if relation {
		fusion.RelationSignals = []retrieval.RelationSignal{{Rank: rank}}
	}
	return PackageCandidate{Fusion: fusion, Unit: unit, ExternalEvidenceID: externalID}
}

func scopeAsFact(scope Scope) fact.Scope {
	return fact.Scope{OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID}
}

func candidateClone(candidate *ContextSelectionCandidate) *ContextSelectionCandidate {
	clone := *candidate
	clone.Item = cloneContextItem(candidate.Item)
	return &clone
}

func relationClone(candidate *ContextRelationCandidate) *ContextRelationCandidate {
	clone := *candidate
	clone.Relation = cloneContextRelation(candidate.Relation)
	return &clone
}

func cloneContextProjectionRequest(request ContextCandidateProjectionRequest) ContextCandidateProjectionRequest {
	clone := request
	clone.Facts = make([]fact.CanonicalFact, len(request.Facts))
	for index := range request.Facts {
		clone.Facts[index] = *cloneContextFact(&request.Facts[index])
	}
	clone.Retrieval.Candidates = append([]PackageCandidate(nil), request.Retrieval.Candidates...)
	for index := range clone.Retrieval.Candidates {
		clone.Retrieval.Candidates[index].Unit = *cloneContextEvidence(&clone.Retrieval.Candidates[index].Unit)
	}
	return clone
}

func (request ContextCandidateProjectionRequest) CloneForTest() ContextCandidateProjectionRequest {
	return cloneContextProjectionRequest(request)
}
