package query

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// TextSearcher is the small textual projection port consumed by hybrid
// retrieval. The concrete projection validates and scopes the SQL adapter.
type TextSearcher interface {
	Search(context.Context, retrieval.SearchOptions) ([]retrieval.TextHit, error)
}

// VectorSearcher is the exact pgvector projection port consumed by hybrid
// retrieval. A missing port is an explicit degraded mode, never a profile
// fallback.
type VectorSearcher interface {
	Search(context.Context, retrieval.VectorSearchQuery) (retrieval.VectorSearchResponse, error)
}

// EvidenceUnitResolver resolves a fused identity back to the canonical unit.
// It is the only source used to construct a package; a textual projection row
// alone is not enough to manufacture provenance or a valid Evidence Unit.
type EvidenceUnitResolver interface {
	Resolve(context.Context, Scope, string) (evidence.EvidenceUnit, error)
}

// RelationInputProvider optionally supplies bounded one-hop relation seeds and
// entity references. It receives only scoped identities, never source files.
type RelationInputProvider interface {
	RelationInputs(context.Context, QueryRetrievalInput, []retrieval.TextHit, []retrieval.VectorHit) ([]retrieval.RelationSeed, map[string][]retrieval.FusionEvidenceReference, error)
}

// HybridRetriever composes exact/textual retrieval, optional embeddings and
// deterministic fusion. Embedding/profile failures degrade to textual and
// exact signals unless the request context itself has been canceled.
type HybridRetriever struct {
	Text             TextSearcher
	Vector           VectorSearcher
	Embedder         aigateway.Embedder
	EmbeddingProfile aigateway.EmbeddingProfile
	VectorProfile    retrieval.EmbeddingProfile
	UnitResolver     EvidenceUnitResolver
	Relations        retrieval.RelationStore
	RelationInputs   RelationInputProvider
	Support          SupportAssessor
	Fusion           retrieval.FusionConfiguration
	Limit            int
}

var _ Retriever = (*HybridRetriever)(nil)

// Retrieve executes every configured signal inside the fixed scope and then
// resolves the fused identities to canonical Evidence Units. No provider is
// called after this method; generation is owned by QueryOrchestrator.
func (r *HybridRetriever) Retrieve(ctx context.Context, input QueryRetrievalInput) (QueryRetrievalResult, error) {
	if r == nil || r.UnitResolver == nil || r.Support == nil {
		return QueryRetrievalResult{}, ErrQueryRetrievalNotConfigured
	}
	if ctx == nil {
		return QueryRetrievalResult{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return QueryRetrievalResult{}, err
	}
	if err := input.Scope.Validate(); err != nil {
		return QueryRetrievalResult{}, ErrQueryScopeRequired
	}
	if strings.TrimSpace(input.Question) == "" || !validKnowledgeQuestionKind(input.QuestionKind) {
		return QueryRetrievalResult{}, ErrInvalidAbstentionInput
	}
	limit := input.Limit
	if limit == 0 {
		limit = r.Limit
	}
	if limit == 0 {
		limit = retrieval.DefaultTextSearchLimit
	}
	if limit < 1 || limit > retrieval.MaxTextSearchLimit {
		return QueryRetrievalResult{}, ErrQueryRetrievalNotConfigured
	}

	degradation := make([]string, 0, 3)
	textHits := []retrieval.TextHit{}
	if r.Text != nil {
		var err error
		textHits, err = r.Text.Search(ctx, retrieval.SearchOptions{
			OrganizationID: input.Scope.OrganizationID,
			SourceID:       input.Scope.SourceID,
			SnapshotID:     input.Scope.SnapshotID,
			Query:          input.Question,
			Limit:          limit,
		})
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return QueryRetrievalResult{}, contextErr
			}
			return QueryRetrievalResult{}, err
		}
	} else {
		degradation = append(degradation, "text_unavailable")
	}

	vectorHits, vectorReasons, err := r.vectorHits(ctx, input, limit)
	if err != nil {
		return QueryRetrievalResult{}, err
	}
	degradation = append(degradation, vectorReasons...)

	exactHits := make([]retrieval.ExactHit, 0, len(textHits))
	for index, hit := range textHits {
		if !hit.ExactMatch {
			continue
		}
		rank := index + 1
		if hit.Rank > 0 {
			rank = int(hit.Rank)
		}
		exactHits = append(exactHits, retrieval.ExactHit{
			EvidenceID:          hit.EvidenceID,
			OrganizationID:      hit.OrganizationID,
			SourceID:            hit.SourceID,
			SnapshotID:          hit.SnapshotID,
			EvidenceContentHash: hit.ContentHash,
			Rank:                rank,
		})
	}
	seeds := []retrieval.RelationSeed(nil)
	evidenceByEntity := map[string][]retrieval.FusionEvidenceReference(nil)
	relationEnabled := relationSignalEnabled(r.Fusion)
	if relationEnabled && r.RelationInputs != nil && r.Relations != nil {
		seeds, evidenceByEntity, err = r.RelationInputs.RelationInputs(ctx, input, textHits, vectorHits)
		if err != nil {
			return QueryRetrievalResult{}, err
		}
	}
	if relationEnabled && (r.RelationInputs == nil || r.Relations == nil) {
		degradation = append(degradation, "relation_unavailable")
	}
	fusion, err := retrieval.Fuse(ctx, retrieval.FusionRequest{
		Scope:              retrieval.FusionScope(input.Scope),
		Configuration:      r.Fusion,
		Exact:              exactHits,
		Textual:            textHits,
		Vector:             vectorHits,
		RelationStore:      r.Relations,
		RelationSeeds:      seeds,
		EvidenceByEntityID: evidenceByEntity,
	})
	if err != nil {
		return QueryRetrievalResult{}, err
	}
	degradation = append(degradation, fusion.DegradationReasons...)

	candidates := make([]PackageCandidate, 0, len(fusion.Candidates))
	localOnly := 0
	for _, fused := range fusion.Candidates {
		if err := ctx.Err(); err != nil {
			return QueryRetrievalResult{}, err
		}
		unit, resolveErr := r.UnitResolver.Resolve(ctx, input.Scope, fused.EvidenceID)
		if resolveErr != nil {
			// A missing canonical unit is not safe to turn into a synthetic
			// candidate. Surface it as a deterministic degraded retrieval result
			// only when the resolver explicitly reports absence; all other errors
			// remain operational failures.
			if errors.Is(resolveErr, ErrEvidenceUnitNotFound) {
				degradation = append(degradation, "evidence_unavailable")
				continue
			}
			return QueryRetrievalResult{}, resolveErr
		}
		if unit.ExternalTransfer != evidence.DecisionAllow || unit.ContentState != evidence.ContentStatePresent {
			localOnly++
		}
		candidates = append(candidates, PackageCandidate{
			Fusion:              fused,
			Unit:                unit,
			Kind:                candidateKindForSignals(fused),
			CanonicalEvidenceID: fused.EvidenceID,
		})
	}
	result := QueryRetrievalResult{
		Candidates:             candidates,
		Fusion:                 fusion,
		LocalOnlyEvidenceCount: localOnly,
		DegradationReasons:     uniqueStrings(degradation),
	}
	support, err := r.Support.Assess(ctx, input, result)
	if err != nil {
		return QueryRetrievalResult{}, err
	}
	result.Support = support
	return result, nil
}

func relationSignalEnabled(configuration retrieval.FusionConfiguration) bool {
	if configuration.RelationWeight > 0 {
		return true
	}
	return configuration.RelationWeight == 0 && configuration.ExactWeight == 0 &&
		configuration.TextualWeight == 0 && configuration.VectorWeight == 0
}

var ErrEvidenceUnitNotFound = errors.New("query: evidence unit not found")

func (r *HybridRetriever) vectorHits(ctx context.Context, input QueryRetrievalInput, limit int) ([]retrieval.VectorHit, []string, error) {
	if r.Vector == nil || r.Embedder == nil {
		return nil, []string{"vector_unavailable"}, nil
	}
	if err := r.EmbeddingProfile.Validate(); err != nil {
		return nil, []string{"vector_profile_unavailable"}, nil
	}
	if err := r.VectorProfile.Validate(); err != nil {
		return nil, []string{"vector_profile_unavailable"}, nil
	}
	deadline := input.Deadline
	if deadline.IsZero() {
		deadline = time.Now().UTC().Add(defaultQueryGenerationTimeout)
	}
	embedding, err := r.Embedder.Embed(ctx, aigateway.EmbeddingRequest{
		ExecutionID: input.ExecutionID,
		RequestID:   input.RequestID,
		Deadline:    deadline,
		Profile:     r.EmbeddingProfile,
		Items: []aigateway.EmbeddingItem{{
			ID:      "query",
			Content: input.Question,
		}},
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, contextErr
		}
		return nil, []string{"vector_unavailable"}, nil
	}
	if len(embedding.Vectors) != 1 {
		return nil, []string{"vector_unavailable"}, nil
	}
	vector := make([]float32, len(embedding.Vectors[0]))
	for index, value := range embedding.Vectors[0] {
		vector[index] = float32(value)
	}
	response, err := r.Vector.Search(ctx, retrieval.VectorSearchQuery{
		OrganizationID: input.Scope.OrganizationID,
		SourceID:       input.Scope.SourceID,
		SnapshotID:     input.Scope.SnapshotID,
		Profile:        r.VectorProfile,
		Vector:         vector,
		Limit:          limit,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, nil, contextErr
		}
		return nil, []string{"vector_unavailable"}, nil
	}
	return response.Hits, nil, nil
}

func candidateKindForSignals(candidate retrieval.FusionCandidate) CandidateKind {
	for _, signal := range candidate.Signals {
		switch signal.Kind {
		case retrieval.FusionSignalExact, retrieval.FusionSignalTextual:
			return CandidateKindText
		case retrieval.FusionSignalVector:
			return CandidateKindSymbol
		}
	}
	if len(candidate.RelationSignals) > 0 {
		return CandidateKindRelation
	}
	return CandidateKindUnknown
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
