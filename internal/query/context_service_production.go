package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const (
	// ContextServiceAssemblyVersion identifies the deterministic production
	// composition. It is included in the continuation algorithm binding.
	ContextServiceAssemblyVersion = "context-service-v1"
	contextServiceAlgorithmPrefix = "context-service-"
)

var (
	// ErrContextServiceNotConfigured identifies a service without all required
	// application ports or with an invalid immutable configuration.
	ErrContextServiceNotConfigured = errors.New("query: context service is not configured")
	// ErrInvalidContextServiceConfig identifies a configuration that cannot be
	// used to assemble a bounded context package.
	ErrInvalidContextServiceConfig = errors.New("query: invalid context service configuration")
	// ErrContextSnapshotScopeMismatch identifies a reader result that does not
	// belong to the requested organization/source/snapshot tuple.
	ErrContextSnapshotScopeMismatch = errors.New("query: context snapshot scope mismatch")
	// ErrContextServiceSnapshot is the payload-free failure for an unreadable or
	// invalid factual snapshot.
	ErrContextServiceSnapshot = errors.New("query: context snapshot unavailable")
	// ErrContextServiceRetrieval is the payload-free failure for a retriever
	// result that cannot be consumed by the context compositor.
	ErrContextServiceRetrieval = errors.New("query: context retrieval unavailable")
	// ErrContextServiceComposition is the payload-free failure for a malformed
	// projected or assembled context value.
	ErrContextServiceComposition = errors.New("query: context composition failed")

	// Descriptive aliases keep adapters independent from the concrete type name.
	ErrInvalidProductionContextService = ErrContextServiceNotConfigured
	ErrInvalidProductionContextConfig  = ErrInvalidContextServiceConfig
)

// ContextServiceConfig contains the immutable, non-secret choices used by the
// production context composition. Zero utility and estimator values select
// their versioned defaults; zero RetrievalLimit selects the bounded retrieval
// default. TransferPolicy is copied at construction and never mutated.
type ContextServiceConfig struct {
	TransferPolicy   *evidence.Policy
	Utility          ContextUtilityConfiguration
	Estimator        ContextTokenEstimatorConfiguration
	EstimationLimits ContextTokenEstimationLimits
	RetrievalLimit   int
}

// ProductionContextServiceConfig is the descriptive spelling used by callers
// that want to make the concrete adapter explicit.
type ProductionContextServiceConfig = ContextServiceConfig

// ProductionContextService composes the ContextService port from a factual
// snapshot reader, hybrid retrieval port and stateless continuation codec. It
// deliberately has no Generator or persistence implementation dependency.
type ProductionContextService struct {
	snapshotReader ContextSnapshotReader
	retriever      Retriever
	continuation   *ContextContinuationCodec
	config         ContextServiceConfig
	algorithm      string
}

var _ ContextService = (*ProductionContextService)(nil)

// NewProductionContextService validates and freezes the application ports and
// bounded configuration used by BuildContext.
func NewProductionContextService(
	snapshotReader ContextSnapshotReader,
	retriever Retriever,
	continuation *ContextContinuationCodec,
	config ContextServiceConfig,
) (*ProductionContextService, error) {
	if snapshotReader == nil || retriever == nil || continuation == nil {
		return nil, ErrContextServiceNotConfigured
	}
	if err := continuation.validate(); err != nil {
		return nil, ErrContextServiceNotConfigured
	}
	if config.RetrievalLimit == 0 {
		config.RetrievalLimit = retrieval.DefaultTextSearchLimit
	}
	if config.RetrievalLimit < 1 || config.RetrievalLimit > retrieval.MaxTextSearchLimit {
		return nil, ErrInvalidContextServiceConfig
	}
	utility, err := config.Utility.Normalize()
	if err != nil {
		return nil, ErrInvalidContextServiceConfig
	}
	estimator, err := config.Estimator.Normalize()
	if err != nil {
		return nil, ErrInvalidContextServiceConfig
	}
	if err := config.EstimationLimits.Validate(); err != nil {
		return nil, ErrInvalidContextServiceConfig
	}
	if config.TransferPolicy != nil {
		if err := config.TransferPolicy.Validate(); err != nil {
			return nil, ErrInvalidContextServiceConfig
		}
	}
	config.Utility = utility
	config.Estimator = estimator
	config.TransferPolicy = cloneContextAuthorizedPolicy(config.TransferPolicy)
	algorithm, err := contextServiceAlgorithmVersion(config)
	if err != nil {
		return nil, ErrInvalidContextServiceConfig
	}
	return &ProductionContextService{
		snapshotReader: snapshotReader,
		retriever:      retriever,
		continuation:   continuation,
		config:         config,
		algorithm:      algorithm,
	}, nil
}

// NewContextService is the application-facing constructor for the production
// ContextService. It returns the concrete implementation so composition roots
// may keep its lifecycle while depending on the ContextService interface.
func NewContextService(
	snapshotReader ContextSnapshotReader,
	retriever Retriever,
	continuation *ContextContinuationCodec,
	config ContextServiceConfig,
) (*ProductionContextService, error) {
	return NewProductionContextService(snapshotReader, retriever, continuation, config)
}

// BuildContext reads one immutable snapshot and returns a validated,
// consumer-neutral local package. Local authorization is applied to the
// complete candidate universe before selection and continuation; external
// transfer remains a separate projection concern. No generator or backend is
// consulted by this method except through the supplied ports.
func (s *ProductionContextService) BuildContext(ctx context.Context, request ContextRequest) (ContextPackage, error) {
	if s == nil || s.snapshotReader == nil || s.retriever == nil || s.continuation == nil {
		return ContextPackage{}, ErrContextServiceNotConfigured
	}
	if ctx == nil {
		return ContextPackage{}, ErrInvalidContextRequest
	}
	if err := ctx.Err(); err != nil {
		return ContextPackage{}, err
	}
	if err := request.Validate(); err != nil {
		return ContextPackage{}, ErrInvalidContextRequest
	}
	request = request.Clone()

	snapshot, err := s.snapshotReader.ReadContextSnapshot(ctx, request.Scope)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceSnapshot)
	}
	if err := snapshot.Validate(); err != nil {
		return ContextPackage{}, ErrContextServiceSnapshot
	}
	if !sameScope(snapshot.Scope, request.Scope) {
		return ContextPackage{}, ErrContextSnapshotScopeMismatch
	}
	// Snapshot.Scope is the authorized relational boundary. Canonical facts
	// may retain external IDs until ProjectContextCandidates projects them into
	// the requested scope; ContextSnapshot.Validate already checks their own
	// factual coherence.

	plan, err := PlanContextRetrieval(ctx, request, s.config.RetrievalLimit)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceComposition)
	}
	retrieved, err := s.retriever.Retrieve(ctx, plan.Input)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceRetrieval)
	}
	if err := ctx.Err(); err != nil {
		return ContextPackage{}, err
	}

	projection, err := ProjectContextCandidates(ctx, ContextCandidateProjectionRequest{
		Scope:            request.Scope,
		Facts:            snapshot.Facts,
		Retrieval:        retrieved,
		Estimator:        s.config.Estimator,
		EstimationLimits: s.config.EstimationLimits,
	})
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceComposition)
	}
	authorized, err := AuthorizeLocalContextCandidateProjection(
		ctx,
		request.Scope,
		projection,
		s.config.TransferPolicy,
		s.config.Estimator,
		s.config.EstimationLimits,
	)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceComposition)
	}

	binding := ContextContinuationBinding{
		Scope:            request.Scope,
		SnapshotRevision: snapshot.Revision,
		IntentDigest:     plan.IntentDigest,
		PolicyDigest:     authorized.Policy.PolicyDigest,
		AlgorithmVersion: s.algorithm,
		Ordering:         ContextRetrievalPlanOrdering,
	}
	page, err := s.continuation.PageIDs(
		ctx,
		binding,
		append([]string(nil), authorized.Policy.ContinuationIDs...),
		request.Limits.MaxItems,
		request.Continuation,
	)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrInvalidContextContinuation)
	}

	pageCandidates, err := contextServicePageCandidates(authorized.Candidates, page.IDs)
	if err != nil {
		return ContextPackage{}, err
	}
	selectionRequest := ContextSelectionRequest{
		Scope:         request.Scope,
		Limits:        request.Limits,
		Candidates:    pageCandidates,
		Configuration: s.config.Utility,
	}
	selection, err := SelectContext(ctx, selectionRequest)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceComposition)
	}

	// Relation candidates are bounded by the current page, not by the greedy
	// selection. This keeps relation support candidates available to the
	// closure step while preventing a relation from crossing page boundaries.
	pageIDs := contextServiceCandidateIDs(pageCandidates)
	relationCandidates, relationSupportIncomplete := contextServiceRepresentableRelations(authorized.Relations, pageIDs)
	closureRequest := ContextSupportClosureRequest{
		Request:   selectionRequest,
		Base:      selection,
		Relations: relationCandidates,
	}
	closure, err := CloseContextSupport(ctx, closureRequest)
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceComposition)
	}

	degradations := contextServiceDegradations(
		retrieved.DegradationReasons,
		projection.SupportIncomplete || relationSupportIncomplete || closure.SupportIncomplete,
		selection.BudgetExhausted || closure.BudgetExhausted || page.Continuation != nil,
		snapshot,
		authorized.Policy,
	)
	packageMaterial := ContextPackage{
		Version:        ContextVersion,
		Revision:       snapshot.Revision,
		Scope:          request.Scope,
		Intent:         request.Intent,
		Limits:         request.Limits,
		Items:          closure.Selection.Items,
		Relations:      closure.Relations,
		Coverage:       snapshot.Coverage,
		Gaps:           snapshot.Gaps,
		Degradations:   degradations,
		Audit:          closure.Selection.Audit,
		TokenEstimate:  closure.TokenEstimate,
		CharactersUsed: closure.CharactersUsed,
		BytesUsed:      closure.BytesUsed,
		Truncated:      page.Continuation != nil,
		Continuation:   cloneContextContinuation(page.Continuation),
	}
	finalPolicyIDs := contextServicePolicyIDsForItems(authorized.Policy.ContinuationIDs, packageMaterial.Items)
	finalized, err := FinalizeContextPackage(ctx, packageMaterial, ContextPackageIdentityBinding{
		PolicyDigest:          authorized.Policy.PolicyDigest,
		PolicyContinuationIDs: finalPolicyIDs,
		PolicyFiltered:        authorized.Policy.PolicyFiltered,
	})
	if err != nil {
		return ContextPackage{}, contextServiceStageError(ctx, err, ErrContextServiceComposition)
	}
	if err := finalized.Validate(); err != nil {
		return ContextPackage{}, ErrContextServiceComposition
	}
	return finalized.Clone(), nil
}

func contextServiceStageError(ctx context.Context, stageErr, fallback error) error {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if stageErr == nil {
		return fallback
	}
	return fallback
}

func contextServicePageCandidates(candidates []ContextSelectionCandidate, ids []string) ([]ContextSelectionCandidate, error) {
	byID := make(map[string]ContextSelectionCandidate, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := byID[candidate.Item.ID]; duplicate {
			return nil, ErrContextServiceComposition
		}
		byID[candidate.Item.ID] = cloneContextSelectionCandidate(candidate)
	}
	page := make([]ContextSelectionCandidate, 0, len(ids))
	for _, id := range ids {
		candidate, exists := byID[id]
		if !exists {
			return nil, ErrContextServiceComposition
		}
		page = append(page, candidate)
	}
	return page, nil
}

func contextServiceItemIDs(items []ContextItem) map[string]struct{} {
	ids := make(map[string]struct{}, len(items))
	for _, item := range items {
		ids[item.ID] = struct{}{}
	}
	return ids
}

func contextServiceCandidateIDs(candidates []ContextSelectionCandidate) map[string]struct{} {
	ids := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		ids[candidate.Item.ID] = struct{}{}
	}
	return ids
}

func contextServiceRepresentableRelations(candidates []ContextRelationCandidate, itemIDs map[string]struct{}) ([]ContextRelationCandidate, bool) {
	result := make([]ContextRelationCandidate, 0, len(candidates))
	incomplete := false
	for _, candidate := range candidates {
		representable := true
		for _, id := range relationRequiredIDs(candidate.Relation) {
			if _, exists := itemIDs[id]; !exists {
				representable = false
				break
			}
		}
		if !representable {
			incomplete = true
			continue
		}
		result = append(result, cloneContextRelationCandidate(candidate))
	}
	return result, incomplete
}

func contextServicePolicyIDsForItems(sequence []string, items []ContextItem) []string {
	selected := contextServiceItemIDs(items)
	ids := make([]string, 0, len(items))
	for _, id := range sequence {
		if _, exists := selected[id]; exists {
			ids = append(ids, id)
		}
	}
	return ids
}

func contextServiceDegradations(
	retrievalReasons []string,
	supportIncomplete bool,
	budgetExhausted bool,
	snapshot ContextSnapshot,
	policy ContextPolicyResult,
) []ContextDegradation {
	seen := make(map[ContextDegradationCode]struct{})
	add := func(code ContextDegradationCode) {
		seen[code] = struct{}{}
	}
	for _, reason := range retrievalReasons {
		switch reason {
		case string(ContextDegradationVectorUnavailable), "vector_profile_unavailable":
			add(ContextDegradationVectorUnavailable)
		case string(ContextDegradationTextUnavailable):
			add(ContextDegradationTextUnavailable)
		case string(ContextDegradationRelationUnavailable):
			add(ContextDegradationRelationUnavailable)
		case string(ContextDegradationExactUnavailable):
			add(ContextDegradationExactUnavailable)
		case "evidence_unavailable":
			add(ContextDegradationSupportIncomplete)
		default:
			// An unknown optional-signal failure is never surfaced verbatim. It
			// remains visible as a bounded support limitation.
			if reason != "" {
				add(ContextDegradationSupportIncomplete)
			}
		}
	}
	if supportIncomplete {
		add(ContextDegradationSupportIncomplete)
	}
	if budgetExhausted {
		add(ContextDegradationBudgetExhausted)
	}
	if len(snapshot.Gaps) > 0 {
		add(ContextDegradationCoverageIncomplete)
	}
	if policy.PolicyFiltered {
		add(ContextDegradationPolicyFiltered)
	}

	ordered := []ContextDegradationCode{
		ContextDegradationVectorUnavailable,
		ContextDegradationTextUnavailable,
		ContextDegradationRelationUnavailable,
		ContextDegradationExactUnavailable,
		ContextDegradationSupportIncomplete,
		ContextDegradationBudgetExhausted,
		ContextDegradationCoverageIncomplete,
		ContextDegradationPolicyFiltered,
	}
	result := make([]ContextDegradation, 0, len(ordered))
	for _, code := range ordered {
		if _, exists := seen[code]; exists {
			result = append(result, ContextDegradation{Code: code})
		}
	}
	return result
}

type contextServiceAlgorithmMaterial struct {
	Assembly         string
	Ordering         string
	Utility          ContextUtilityConfiguration
	Estimator        ContextTokenEstimatorConfiguration
	EstimationLimits ContextTokenEstimationLimits
	RetrievalLimit   int
}

func contextServiceAlgorithmVersion(config ContextServiceConfig) (string, error) {
	encoded, err := json.Marshal(contextServiceAlgorithmMaterial{
		Assembly:         ContextServiceAssemblyVersion,
		Ordering:         ContextRetrievalPlanOrdering,
		Utility:          config.Utility,
		Estimator:        config.Estimator,
		EstimationLimits: config.EstimationLimits,
		RetrievalLimit:   config.RetrievalLimit,
	})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return contextServiceAlgorithmPrefix + hex.EncodeToString(digest[:]), nil
}
