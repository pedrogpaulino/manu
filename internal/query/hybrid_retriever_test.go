package query

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestHybridRetrieverIntegratesCanonicalRelationalCandidate(t *testing.T) {
	scope := packageTestScope()
	anchorID := packageTestUUID(4)
	targetID := packageTestUUID(5)
	anchorEntityID := packageTestUUID(6)
	targetEntityID := packageTestUUID(7)
	relationID := packageTestUUID(8)
	anchor := packageTestUnit(scope, "anchor.go", "src/anchor.go", "service calls repository")
	target := packageTestUnit(scope, "config.yml", "config/application.yml", "repository.url: jdbc:test")
	text := &hybridRetrieverTextSearcher{hits: []retrieval.TextHit{{
		EvidenceID:     anchorID,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ContentHash:    anchor.ContentHash,
		Rank:           1,
		ExactMatch:     true,
	}}}
	provider := &hybridRetrieverRelationInputProvider{
		seeds: []retrieval.RelationSeed{{EvidenceID: anchorID, EntityID: anchorEntityID}},
		evidenceByEntity: map[string][]retrieval.FusionEvidenceReference{
			targetEntityID: {{
				EvidenceID:          targetID,
				OrganizationID:      scope.OrganizationID,
				SourceID:            scope.SourceID,
				SnapshotID:          scope.SnapshotID,
				EvidenceContentHash: target.ContentHash,
			}},
		},
	}
	store := &hybridRetrieverRelationStore{hits: []retrieval.RelationHit{{
		RelationID:         relationID,
		OrganizationID:     scope.OrganizationID,
		SourceID:           scope.SourceID,
		SnapshotID:         scope.SnapshotID,
		RelationExternalID: "relation-anchor-config",
		FromEntityID:       anchorEntityID,
		ToEntityID:         targetEntityID,
		RelationType:       "configures",
		Attributes:         json.RawMessage(`{}`),
		Hops:               1,
	}}}
	retriever := &HybridRetriever{
		Text:           text,
		Relations:      store,
		RelationInputs: provider,
		UnitResolver: hybridRetrieverUnitResolver{scope: scope, units: map[string]evidence.EvidenceUnit{
			anchorID: anchor,
			targetID: target,
		}},
		Support: hybridRetrieverSupportAssessor{},
		Fusion: retrieval.FusionConfiguration{
			ExactWeight: 1, TextualWeight: 1, RelationWeight: 1, MaxCandidates: 8,
		},
		Limit: 8,
	}

	result, err := retriever.Retrieve(context.Background(), hybridRetrieverInput(scope))
	if err != nil {
		t.Fatalf("Retrieve() error = %v", err)
	}
	if provider.calls != 1 || len(store.queries) != 1 {
		t.Fatalf("relation calls = provider %d, store %d", provider.calls, len(store.queries))
	}
	var relationCandidate PackageCandidate
	for _, candidate := range result.Candidates {
		if candidate.Fusion.EvidenceID == targetID {
			relationCandidate = candidate
			break
		}
	}
	if relationCandidate.Fusion.EvidenceID != targetID || relationCandidate.CanonicalEvidenceID != targetID {
		t.Fatalf("relation candidate identity = %#v, want canonical %q", relationCandidate, targetID)
	}
	if relationCandidate.Unit.ID != target.ID || len(relationCandidate.Fusion.RelationSignals) != 1 {
		t.Fatalf("relation candidate = %#v", relationCandidate)
	}
	if relationCandidate.Kind != CandidateKindRelation {
		t.Fatalf("relation candidate kind = %q, want %q", relationCandidate.Kind, CandidateKindRelation)
	}
	if countHybridReason(result.DegradationReasons, "relation_unavailable") != 0 {
		t.Fatalf("unexpected relation degradation = %v", result.DegradationReasons)
	}
}

func TestHybridRetrieverDegradesMissingRelationalPortWithoutDroppingOtherCandidates(t *testing.T) {
	scope := packageTestScope()
	anchorID := packageTestUUID(4)
	anchor := packageTestUnit(scope, "anchor.go", "src/anchor.go", "service calls repository")
	text := &hybridRetrieverTextSearcher{hits: []retrieval.TextHit{{
		EvidenceID:     anchorID,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ContentHash:    anchor.ContentHash,
		Rank:           1,
		ExactMatch:     true,
	}}}
	base := func() *HybridRetriever {
		return &HybridRetriever{
			Text: text,
			UnitResolver: hybridRetrieverUnitResolver{scope: scope, units: map[string]evidence.EvidenceUnit{
				anchorID: anchor,
			}},
			Support: hybridRetrieverSupportAssessor{},
			Fusion: retrieval.FusionConfiguration{
				ExactWeight: 1, TextualWeight: 1, RelationWeight: 1, MaxCandidates: 8,
			},
			Limit: 8,
		}
	}

	tests := []struct {
		name               string
		configure          func(*HybridRetriever)
		wantProviderCalls  int
		wantRelationReason bool
	}{
		{
			name:               "provider missing",
			wantRelationReason: true,
			configure: func(retriever *HybridRetriever) {
				retriever.Relations = &hybridRetrieverRelationStore{}
			},
		},
		{
			name:               "store missing",
			wantProviderCalls:  0,
			wantRelationReason: true,
			configure: func(retriever *HybridRetriever) {
				retriever.RelationInputs = &hybridRetrieverRelationInputProvider{
					seeds: []retrieval.RelationSeed{{EvidenceID: anchorID, EntityID: packageTestUUID(6)}},
				}
			},
		},
		{
			name:               "relation disabled",
			wantProviderCalls:  0,
			wantRelationReason: false,
			configure: func(retriever *HybridRetriever) {
				retriever.Fusion.RelationWeight = 0
				retriever.RelationInputs = &hybridRetrieverRelationInputProvider{
					err: errors.New("provider must not be called"),
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			retriever := base()
			tt.configure(retriever)
			result, err := retriever.Retrieve(context.Background(), hybridRetrieverInput(scope))
			if err != nil {
				t.Fatalf("Retrieve() error = %v", err)
			}
			if len(result.Candidates) != 1 || result.Candidates[0].Fusion.EvidenceID != anchorID {
				t.Fatalf("candidates = %#v, want textual candidate %q", result.Candidates, anchorID)
			}
			wantRelationReasons := 0
			if tt.wantRelationReason {
				wantRelationReasons = 1
			}
			if countHybridReason(result.DegradationReasons, "relation_unavailable") != wantRelationReasons {
				t.Fatalf("degradation reasons = %v, want %d relation_unavailable", result.DegradationReasons, wantRelationReasons)
			}
			if countHybridReason(result.Fusion.DegradationReasons, "relation_unavailable") > 1 {
				t.Fatalf("fusion degradation reasons = %v, want no duplicate", result.Fusion.DegradationReasons)
			}
			if provider, ok := retriever.RelationInputs.(*hybridRetrieverRelationInputProvider); ok && provider.calls != tt.wantProviderCalls {
				t.Fatalf("provider calls = %d, want %d", provider.calls, tt.wantProviderCalls)
			}
		})
	}
}

func TestHybridRetrieverPropagatesRelationProviderErrorsAndCancellation(t *testing.T) {
	scope := packageTestScope()
	wantOperational := errors.New("relation provider unavailable")
	tests := []struct {
		name string
		err  error
	}{
		{name: "operational error", err: wantOperational},
		{name: "cancellation", err: context.Canceled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &hybridRetrieverRelationInputProvider{err: tt.err}
			retriever := &HybridRetriever{
				RelationInputs: provider,
				Relations:      &hybridRetrieverRelationStore{},
				UnitResolver:   hybridRetrieverUnitResolver{scope: scope, units: map[string]evidence.EvidenceUnit{}},
				Support:        hybridRetrieverSupportAssessor{},
				Fusion:         retrieval.FusionConfiguration{TextualWeight: 1, RelationWeight: 1},
			}
			_, err := retriever.Retrieve(context.Background(), hybridRetrieverInput(scope))
			if !errors.Is(err, tt.err) {
				t.Fatalf("Retrieve() error = %v, want %v", err, tt.err)
			}
			if provider.calls != 1 {
				t.Fatalf("provider calls = %d, want 1", provider.calls)
			}
		})
	}
}

func hybridRetrieverInput(scope Scope) QueryRetrievalInput {
	return QueryRetrievalInput{
		Question:     "what does the service call?",
		QuestionKind: KnowledgeQuestionPossibleFlow,
		Scope:        scope,
		Limit:        8,
	}
}

func countHybridReason(reasons []string, want string) int {
	count := 0
	for _, reason := range reasons {
		if reason == want {
			count++
		}
	}
	return count
}

type hybridRetrieverTextSearcher struct {
	hits []retrieval.TextHit
	err  error
}

func (s *hybridRetrieverTextSearcher) Search(ctx context.Context, _ retrieval.SearchOptions) ([]retrieval.TextHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.err != nil {
		return nil, s.err
	}
	return append([]retrieval.TextHit(nil), s.hits...), nil
}

type hybridRetrieverRelationInputProvider struct {
	seeds            []retrieval.RelationSeed
	evidenceByEntity map[string][]retrieval.FusionEvidenceReference
	err              error
	calls            int
}

func (p *hybridRetrieverRelationInputProvider) RelationInputs(ctx context.Context, _ QueryRetrievalInput, _ []retrieval.TextHit, _ []retrieval.VectorHit) ([]retrieval.RelationSeed, map[string][]retrieval.FusionEvidenceReference, error) {
	p.calls++
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if p.err != nil {
		return nil, nil, p.err
	}
	return append([]retrieval.RelationSeed(nil), p.seeds...), p.evidenceByEntity, nil
}

type hybridRetrieverRelationStore struct {
	hits    []retrieval.RelationHit
	err     error
	queries []retrieval.RelationQuery
}

func (s *hybridRetrieverRelationStore) Expand(ctx context.Context, query retrieval.RelationQuery) ([]retrieval.RelationHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.queries = append(s.queries, query)
	if s.err != nil {
		return nil, s.err
	}
	return append([]retrieval.RelationHit(nil), s.hits...), nil
}

type hybridRetrieverUnitResolver struct {
	scope Scope
	units map[string]evidence.EvidenceUnit
}

func (r hybridRetrieverUnitResolver) Resolve(ctx context.Context, scope Scope, evidenceID string) (evidence.EvidenceUnit, error) {
	if err := ctx.Err(); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	if scope != r.scope {
		return evidence.EvidenceUnit{}, ErrQueryScopeRequired
	}
	unit, ok := r.units[evidenceID]
	if !ok {
		return evidence.EvidenceUnit{}, ErrEvidenceUnitNotFound
	}
	return unit, nil
}

type hybridRetrieverSupportAssessor struct{}

func (hybridRetrieverSupportAssessor) Assess(ctx context.Context, _ QueryRetrievalInput, _ QueryRetrievalResult) (SupportAssessment, error) {
	if err := ctx.Err(); err != nil {
		return SupportAssessment{}, err
	}
	return SupportAssessment{Kind: KnowledgeQuestionPossibleFlow, Level: EvidenceSupportSufficient}, nil
}
