package retrieval

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

type fusionRelationStore struct {
	hits    []RelationHit
	queries []RelationQuery
	err     error
}

func (s *fusionRelationStore) Expand(_ context.Context, query RelationQuery) ([]RelationHit, error) {
	s.queries = append(s.queries, query)
	if s.err != nil {
		return nil, s.err
	}
	return append([]RelationHit(nil), s.hits...), nil
}

func fusionTestScope() FusionScope {
	scope := embeddingTestScope()
	return FusionScope{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
	}
}

func fusionTestExact(scope FusionScope, number, rank int) ExactHit {
	return ExactHit{
		EvidenceID:     embeddingTestUUID(600 + number),
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Rank:           rank,
	}
}

func fusionTestText(scope FusionScope, number int, rank float64, exact bool) TextHit {
	return TextHit{
		EvidenceID:     embeddingTestUUID(600 + number),
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ContentHash:    evidence.ContentDigest("fusion-text-" + string(rune('a'+number))),
		Rank:           rank,
		ExactMatch:     exact,
	}
}

func fusionTestRelation(scope FusionScope, number int, from, to string) RelationHit {
	return RelationHit{
		RelationID:         embeddingTestUUID(800 + number),
		OrganizationID:     scope.OrganizationID,
		SourceID:           scope.SourceID,
		SnapshotID:         scope.SnapshotID,
		RelationExternalID: "relation-" + string(rune('a'+number)),
		FromEntityID:       from,
		ToEntityID:         to,
		RelationType:       "depends_on",
		Attributes:         json.RawMessage(`{}`),
		Hops:               1,
	}
}

func fusionTestReference(scope FusionScope, number int) FusionEvidenceReference {
	return FusionEvidenceReference{
		EvidenceID:          embeddingTestUUID(900 + number),
		OrganizationID:      scope.OrganizationID,
		SourceID:            scope.SourceID,
		SnapshotID:          scope.SnapshotID,
		EvidenceContentHash: evidence.ContentDigest("fusion-relation-" + string(rune('a'+number))),
	}
}

func TestFusionConfigurationRegistersDigestAndRejectsMismatch(t *testing.T) {
	configuration := FusionConfiguration{
		Version: "rrf-test", RRFK: 11, ExactWeight: 2, TextualWeight: 1,
		VectorWeight: 0.5, RelationWeight: 3, MaxCandidates: 7,
		RelationBudget: 9, RelationFanOut: 4, RelationMaxHops: 1,
		RelationDirection: RelationDirectionOutbound,
	}
	normalized, err := configuration.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(normalized.Digest) != 64 || normalized.Digest == configuration.Digest {
		t.Fatalf("configuration digest = %q", normalized.Digest)
	}
	canonical, err := json.Marshal(normalized)
	if err != nil || strings.Contains(string(canonical), normalized.Digest) {
		t.Fatalf("registered configuration = %s / %v", canonical, err)
	}
	bad := normalized
	bad.Digest = strings.Repeat("0", 64)
	if _, err := bad.Normalize(); !errors.Is(err, ErrInvalidFusion) {
		t.Fatalf("mismatched digest error = %v", err)
	}
}

func TestFusionRRFIsDeterministicAndDeduplicatesEvidence(t *testing.T) {
	scope := fusionTestScope()
	profile := embeddingTestProfile()
	vectorOne := vectorSearchTestHit(3, 0.1)
	vectorOne.EvidenceID = embeddingTestUUID(603)
	vectorTwo := vectorSearchTestHit(1, 0.4)
	vectorTwo.EvidenceID = embeddingTestUUID(601)
	configuration := FusionConfiguration{
		RRFK: 1, ExactWeight: 2, TextualWeight: 1, VectorWeight: 3,
		RelationWeight: 0, MaxCandidates: 10,
	}
	textTwo := fusionTestText(scope, 2, 3, false)
	textTwo.ContentHash = vectorTwo.EvidenceContentHash
	textThree := fusionTestText(scope, 3, 2, true)
	textThree.ContentHash = vectorOne.EvidenceContentHash
	request := FusionRequest{
		Scope:         scope,
		Configuration: configuration,
		Exact:         []ExactHit{fusionTestExact(scope, 1, 1), fusionTestExact(scope, 2, 2)},
		Textual:       []TextHit{textTwo, textThree},
		Vector:        []VectorHit{vectorOne, vectorTwo},
	}
	request.Vector[0].Profile = profile
	request.Vector[1].Profile = profile
	request.Vector[0].ProfileID = profile.ID
	request.Vector[1].ProfileID = profile.ID
	first, err := Fuse(context.Background(), request)
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	permuted := request
	permuted.Exact = []ExactHit{request.Exact[1], request.Exact[0]}
	permuted.Textual = []TextHit{request.Textual[1], request.Textual[0]}
	permuted.Vector = []VectorHit{request.Vector[1], request.Vector[0]}
	second, err := Fuse(context.Background(), permuted)
	if err != nil {
		t.Fatalf("Fuse(permuted) error = %v", err)
	}
	if len(first.Candidates) != 3 || len(second.Candidates) != 3 {
		t.Fatalf("candidate counts = %d/%d", len(first.Candidates), len(second.Candidates))
	}
	for index := range first.Candidates {
		left, right := first.Candidates[index], second.Candidates[index]
		if left.EvidenceID != right.EvidenceID || left.Score != right.Score || len(left.Signals) != len(right.Signals) {
			t.Fatalf("unstable candidate %d: %#v / %#v", index, left, right)
		}
		if left.ConfigurationDigest != first.Configuration.Digest {
			t.Fatalf("candidate configuration digest = %q", left.ConfigurationDigest)
		}
	}
	if first.Candidates[0].EvidenceID != embeddingTestUUID(601) {
		t.Fatalf("fused ranking = %#v", first.Candidates)
	}
	if first.Degraded || !first.Telemetry.VectorAvailable {
		t.Fatalf("unexpected degradation/telemetry = %v/%+v", first.Degraded, first.Telemetry)
	}
}

func TestFusionRejectsScopeAndEmbeddingProfileMix(t *testing.T) {
	scope := fusionTestScope()
	wrongScope := scope
	wrongScope.SourceID = embeddingTestUUID(999)
	request := FusionRequest{
		Scope:         scope,
		Configuration: FusionConfiguration{TextualWeight: 1},
		Textual:       []TextHit{fusionTestText(wrongScope, 1, 1, false)},
	}
	if _, err := Fuse(context.Background(), request); !errors.Is(err, ErrFusionScopeMismatch) {
		t.Fatalf("scope mismatch error = %v", err)
	}
	profile := embeddingTestProfile()
	otherProfile := profile
	otherProfile.ID = embeddingTestUUID(999)
	first := vectorSearchTestHit(1, 0.1)
	first.Profile = profile
	first.ProfileID = profile.ID
	second := vectorSearchTestHit(2, 0.2)
	second.Profile = otherProfile
	second.ProfileID = otherProfile.ID
	request = FusionRequest{
		Scope:         scope,
		Configuration: FusionConfiguration{VectorWeight: 1},
		Vector:        []VectorHit{first, second},
	}
	if _, err := Fuse(context.Background(), request); !errors.Is(err, ErrFusionProfileMix) {
		t.Fatalf("profile mix error = %v", err)
	}
}

func TestFusionDegradesExplicitlyWithoutEmbeddings(t *testing.T) {
	scope := fusionTestScope()
	response, err := Fuse(context.Background(), FusionRequest{
		Scope:         scope,
		Configuration: FusionConfiguration{TextualWeight: 1, VectorWeight: 1},
		Textual:       []TextHit{fusionTestText(scope, 1, 1, true)},
	})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if !response.Degraded || len(response.Candidates) != 1 || response.Telemetry.VectorAvailable {
		t.Fatalf("degraded response = %+v", response)
	}
	if !reflect.DeepEqual(response.DegradationReasons, []string{"vector_unavailable"}) {
		t.Fatalf("degradation reasons = %v", response.DegradationReasons)
	}
	for _, signal := range response.Candidates[0].Signals {
		if signal.Kind == FusionSignalVector {
			t.Fatal("textual degradation invented a vector signal")
		}
	}
}

func TestFusionExpandsOneHopWithSeparateBoundedRelationBudget(t *testing.T) {
	scope := fusionTestScope()
	anchorEntity := embeddingTestUUID(1001)
	targetEntity := embeddingTestUUID(1002)
	store := &fusionRelationStore{hits: []RelationHit{fusionTestRelation(scope, 1, anchorEntity, targetEntity)}}
	references := make([]FusionEvidenceReference, 0, 10)
	for index := 0; index < 10; index++ {
		references = append(references, fusionTestReference(scope, index))
	}
	response, err := Fuse(context.Background(), FusionRequest{
		Scope: scope,
		Configuration: FusionConfiguration{
			RRFK: 1, ExactWeight: 1, RelationWeight: 1,
			MaxCandidates: 20, RelationBudget: 2, RelationFanOut: 5,
			RelationMaxHops: 1, RelationDirection: RelationDirectionOutbound,
		},
		Exact:              []ExactHit{fusionTestExact(scope, 1, 1)},
		RelationStore:      store,
		RelationSeeds:      []RelationSeed{{EvidenceID: embeddingTestUUID(601), EntityID: anchorEntity}},
		EvidenceByEntityID: map[string][]FusionEvidenceReference{targetEntity: references},
	})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if len(store.queries) != 1 || store.queries[0].MaxHops != 1 || store.queries[0].FanOut != 2 {
		t.Fatalf("relation query = %+v", store.queries)
	}
	if response.Telemetry.RelationResultCount != 1 || response.Telemetry.RelationCandidateCount != 1 {
		t.Fatalf("relation budget telemetry = %+v", response.Telemetry)
	}
	if len(response.Candidates) != 2 || len(response.Candidates[1].RelationSignals) != 1 {
		t.Fatalf("relation candidates = %+v", response.Candidates)
	}
	relation := response.Candidates[1].RelationSignals[0]
	if relation.Relation.Provenance.RelationID != relation.Relation.RelationID || relation.SeedEvidenceID != embeddingTestUUID(601) {
		t.Fatalf("relation provenance = %+v", relation)
	}
}

func TestFusionRejectsRelationReferenceScopeAndPropagatesRelationScope(t *testing.T) {
	scope := fusionTestScope()
	anchorEntity := embeddingTestUUID(1001)
	targetEntity := embeddingTestUUID(1002)
	badReference := fusionTestReference(scope, 1)
	badReference.SourceID = embeddingTestUUID(999)
	request := FusionRequest{
		Scope:              scope,
		Configuration:      FusionConfiguration{ExactWeight: 1, RelationWeight: 1, RelationBudget: 2},
		Exact:              []ExactHit{fusionTestExact(scope, 1, 1)},
		RelationStore:      &fusionRelationStore{hits: []RelationHit{fusionTestRelation(scope, 1, anchorEntity, targetEntity)}},
		RelationSeeds:      []RelationSeed{{EvidenceID: embeddingTestUUID(601), EntityID: anchorEntity}},
		EvidenceByEntityID: map[string][]FusionEvidenceReference{targetEntity: {badReference}},
	}
	if _, err := Fuse(context.Background(), request); !errors.Is(err, ErrFusionScopeMismatch) {
		t.Fatalf("reference scope error = %v", err)
	}
	wrongRelation := fusionTestRelation(scope, 1, anchorEntity, targetEntity)
	wrongRelation.SourceID = embeddingTestUUID(999)
	request.EvidenceByEntityID = nil
	request.RelationStore = &fusionRelationStore{hits: []RelationHit{wrongRelation}}
	if _, err := Fuse(context.Background(), request); !errors.Is(err, ErrRelationScopeMismatch) {
		t.Fatalf("relation scope error = %v", err)
	}
}

func TestFusionMarksUnavailableRelationExpansionAsDegraded(t *testing.T) {
	scope := fusionTestScope()
	anchor := embeddingTestUUID(1001)
	response, err := Fuse(context.Background(), FusionRequest{
		Scope:         scope,
		Configuration: FusionConfiguration{ExactWeight: 1, RelationWeight: 1},
		Exact:         []ExactHit{fusionTestExact(scope, 1, 1)},
		RelationSeeds: []RelationSeed{{EvidenceID: embeddingTestUUID(601), EntityID: anchor}},
	})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if !response.Degraded || !containsString(response.DegradationReasons, "relation_unavailable") {
		t.Fatalf("relation degradation = %+v", response)
	}
}

func TestFusionRejectsExcessSignalAndRelationInputs(t *testing.T) {
	scope := fusionTestScope()
	base := FusionRequest{
		Scope:         scope,
		Configuration: FusionConfiguration{ExactWeight: 1},
	}
	tests := []struct {
		name    string
		request func() FusionRequest
	}{
		{
			name: "exact signals",
			request: func() FusionRequest {
				request := base
				request.Exact = make([]ExactHit, MaxFusionCandidates+1)
				return request
			},
		},
		{
			name: "textual signals",
			request: func() FusionRequest {
				request := base
				request.Textual = make([]TextHit, MaxFusionCandidates+1)
				return request
			},
		},
		{
			name: "vector signals",
			request: func() FusionRequest {
				request := base
				request.Vector = make([]VectorHit, MaxFusionCandidates+1)
				return request
			},
		},
		{
			name: "relation seeds",
			request: func() FusionRequest {
				request := base
				request.RelationSeeds = make([]RelationSeed, MaxFusionRelationBudget+1)
				return request
			},
		},
		{
			name: "relation entities",
			request: func() FusionRequest {
				request := base
				request.EvidenceByEntityID = make(map[string][]FusionEvidenceReference, MaxFusionRelationBudget+1)
				for index := 0; index <= MaxFusionRelationBudget; index++ {
					request.EvidenceByEntityID[embeddingTestUUID(20000+index)] = nil
				}
				return request
			},
		},
		{
			name: "global relation references",
			request: func() FusionRequest {
				request := base
				request.EvidenceByEntityID = map[string][]FusionEvidenceReference{
					embeddingTestUUID(30000): makeFusionTestReferences(scope, MaxFusionRelationBudget/2+1, 0),
					embeddingTestUUID(30001): makeFusionTestReferences(scope, MaxFusionRelationBudget/2+1, 10000),
				}
				return request
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Fuse(context.Background(), test.request()); !errors.Is(err, ErrInvalidFusion) {
				t.Fatalf("Fuse() error = %v, want invalid fusion input", err)
			}
		})
	}
}

func TestFusionDoesNotDegradeDisabledSignalsOrCallRelationStore(t *testing.T) {
	scope := fusionTestScope()
	store := &fusionRelationStore{err: errors.New("relation store must not be called")}
	response, err := Fuse(context.Background(), FusionRequest{
		Scope: scope,
		Configuration: FusionConfiguration{
			TextualWeight:  1,
			VectorWeight:   0,
			RelationWeight: 0,
		},
		Textual:       []TextHit{fusionTestText(scope, 1, 1, true)},
		RelationStore: store,
		RelationSeeds: []RelationSeed{{EvidenceID: embeddingTestUUID(601), EntityID: embeddingTestUUID(1001)}},
	})
	if err != nil {
		t.Fatalf("Fuse() error = %v", err)
	}
	if response.Degraded || len(response.DegradationReasons) != 0 {
		t.Fatalf("disabled signals degraded response = %+v", response)
	}
	if response.Telemetry.VectorAvailable {
		t.Fatal("disabled vector signal was reported as available")
	}
	if len(store.queries) != 0 {
		t.Fatalf("disabled relation signal called store with queries = %+v", store.queries)
	}
}

func makeFusionTestReferences(scope FusionScope, count, offset int) []FusionEvidenceReference {
	references := make([]FusionEvidenceReference, 0, count)
	for index := 0; index < count; index++ {
		references = append(references, fusionTestReference(scope, offset+index))
	}
	return references
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
