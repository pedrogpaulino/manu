package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

var (
	// ErrQueryOrchestratorNotConfigured identifies a missing application port.
	ErrQueryOrchestratorNotConfigured = errors.New("query: orchestrator is not configured")
	// ErrQueryScopeRequired identifies a request that cannot be searched without
	// a complete source snapshot boundary.
	ErrQueryScopeRequired = errors.New("query: source and snapshot are required")
	// ErrQueryRetrievalNotConfigured identifies a missing retrieval port.
	ErrQueryRetrievalNotConfigured = errors.New("query: retrieval is not configured")
	// ErrQueryGeneratorNotConfigured identifies a missing generation port after
	// the deterministic gate allowed the request to continue.
	ErrQueryGeneratorNotConfigured = errors.New("query: generator is not configured")
	// ErrQueryResponseUncited identifies a provider result that has no evidence
	// identity and therefore cannot be published as evidence-backed knowledge.
	ErrQueryResponseUncited = errors.New("query: generated response has no citation")
)

const (
	defaultQueryGenerationTimeout  = 60 * time.Second
	defaultQueryPersistenceTimeout = 10 * time.Second
)

// QueryRetrievalInput is the typed, organization-scoped input consumed by a
// retrieval adapter. QuestionKind is passed through explicitly; no adapter
// may infer it from Question.
type QueryRetrievalInput struct {
	ExecutionID  string
	RequestID    string
	Question     string
	QuestionKind KnowledgeQuestionKind
	Scope        Scope
	Limit        int
	Deadline     time.Time
}

// QueryRetrievalResult is the content-free retrieval audit plus the canonical
// Evidence Units that will be handed to the compositor. A unit may carry local
// content, but it is filtered again by ComposeEvidencePackage before crossing
// the provider boundary.
type QueryRetrievalResult struct {
	Candidates             []PackageCandidate
	Fusion                 retrieval.FusionResponse
	Support                SupportAssessment
	LocalOnlyEvidenceCount int
	DegradationReasons     []string
}

// Retriever is the consumer-side application port for hybrid retrieval.
// Implementations own exact, textual, vector and relation adapters and return
// only candidates in the requested scope.
type Retriever interface {
	Retrieve(context.Context, QueryRetrievalInput) (QueryRetrievalResult, error)
}

// QueryOutcome is the complete safe audit passed to a durable query store.
// It intentionally contains no provider request body, credentials or raw
// source outside the already-authorized package representation.
type QueryOutcome struct {
	ExecutionID    string
	Input          ExecutionInput
	State          ExecutionState
	QuestionDigest string
	PackageDigest  string
	Response       Response
	HasResponse    bool
	DiagnosticCode string
	StartedAt      time.Time
	FinishedAt     time.Time
	Retrieval      QueryRetrievalResult
	Composition    Composition
	Generation     *GenerationAudit
}

// GenerationAudit is the provider-neutral record needed by persistence. It
// contains aggregate usage and digests only; it never stores prompts or model
// payloads.
type GenerationAudit struct {
	Capability                string
	Provider                  aigateway.Provider
	ConfiguredModel           string
	EffectiveModel            string
	Protocol                  aigateway.Protocol
	RequestDigest             string
	ResponseDigest            string
	TransferredEvidenceDigest string
	TransferredEvidenceCount  int
	State                     string
	ErrorCode                 string
	AttemptCount              int
	Usage                     aigateway.Usage
	Latency                   time.Duration
	StartedAt                 time.Time
	FinishedAt                time.Time
}

// QueryRunStore owns durable lifecycle and the query-side audit tables. Start
// must persist a running execution; Finish must persist all terminal data
// before returning it to an API caller.
type QueryRunStore interface {
	Start(context.Context, string, ExecutionInput) (Execution, error)
	Finish(context.Context, string, QueryOutcome) (Execution, error)
	Get(context.Context, string, string) (Execution, error)
}

// ActiveScopeResolver supplies the immutable source/snapshot boundary used
// when the caller intentionally omits it. A resolver must apply the same
// organization scope as the query store and may restrict a supplied source
// to that source's active snapshot.
type ActiveScopeResolver interface {
	ResolveActiveScope(context.Context, string, string) (Scope, error)
}

// SupportAssessor is deliberately separate from retrieval ranking. It is the
// trusted component that declares whether the typed question kind is supported
// by the retrieved material; the query package never classifies Question.
type SupportAssessor interface {
	Assess(context.Context, QueryRetrievalInput, QueryRetrievalResult) (SupportAssessment, error)
}

// OrchestratorConfig contains bounded, non-secret application choices.
type OrchestratorConfig struct {
	PackageLimits      PackageLimits
	TransferPolicy     *evidence.Policy
	GenerationProfile  aigateway.GenerationProfile
	ActiveScope        ActiveScopeResolver
	GenerationTimeout  time.Duration
	PersistenceTimeout time.Duration
	RepairPolicy       RepairPolicy
	Now                func() time.Time
}

// QueryOrchestrator composes retrieval, package authorization, the
// pre-provider abstention gate, generation, response validation and durable
// lifecycle. It implements the small API-facing ExecutionService port.
type QueryOrchestrator struct {
	store     QueryRunStore
	retriever Retriever
	generator aigateway.Generator
	repairer  ResponseRepairer
	config    OrchestratorConfig
}

var _ ExecutionService = (*QueryOrchestrator)(nil)

// NewQueryOrchestrator validates the required application ports. A generator
// is optional at construction so deterministic abstention remains usable in a
// local installation; it is required when the gate allows generation.
func NewQueryOrchestrator(store QueryRunStore, retriever Retriever, generator aigateway.Generator, config OrchestratorConfig) (*QueryOrchestrator, error) {
	if store == nil || retriever == nil {
		return nil, ErrQueryOrchestratorNotConfigured
	}
	if config.GenerationTimeout == 0 {
		config.GenerationTimeout = defaultQueryGenerationTimeout
	}
	if config.PersistenceTimeout == 0 {
		config.PersistenceTimeout = defaultQueryPersistenceTimeout
	}
	if config.GenerationTimeout < 0 || config.PersistenceTimeout < 0 {
		return nil, ErrQueryOrchestratorNotConfigured
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RepairPolicy.MaxAttempts < 0 || config.RepairPolicy.MaxAttempts > 1 {
		return nil, ErrInvalidRepairPolicy
	}
	if generator != nil {
		if err := config.GenerationProfile.Validate(); err != nil {
			return nil, err
		}
	}
	if config.ActiveScope == nil {
		if resolver, ok := store.(ActiveScopeResolver); ok {
			config.ActiveScope = resolver
		}
	}
	return &QueryOrchestrator{
		store:     store,
		retriever: retriever,
		generator: generator,
		repairer:  nil,
		config:    config,
	}, nil
}

// WithRepairer returns a copy of the orchestrator with the single bounded
// repair port installed. The original orchestrator is unchanged.
func (o *QueryOrchestrator) WithRepairer(repairer ResponseRepairer) *QueryOrchestrator {
	if o == nil {
		return nil
	}
	clone := *o
	clone.repairer = repairer
	return &clone
}

// Create executes one synchronous query and returns only after Start and
// Finish have durably committed the lifecycle and terminal result.
func (o *QueryOrchestrator) Create(ctx context.Context, organization string, input ExecutionInput) (Execution, error) {
	if o == nil || o.store == nil || o.retriever == nil {
		return Execution{}, ErrQueryOrchestratorNotConfigured
	}
	if err := validateOrchestratorInput(ctx, input); err != nil {
		return Execution{}, err
	}
	if input.SnapshotID != "" && input.SourceID == "" {
		return Execution{}, ErrQueryScopeRequired
	}
	if input.SnapshotID == "" {
		if o.config.ActiveScope == nil {
			return Execution{}, ErrQueryScopeRequired
		}
		active, resolveErr := o.config.ActiveScope.ResolveActiveScope(ctx, organization, input.SourceID)
		if resolveErr != nil {
			return Execution{}, resolveErr
		}
		organizationID := identity.CanonicalUUID("organization", organization)
		if !sameScopeOrganization(active.OrganizationID, organizationID) || active.SourceID == "" || active.SnapshotID == "" {
			return Execution{}, ErrQueryScopeRequired
		}
		if err := active.Validate(); err != nil {
			return Execution{}, ErrQueryScopeRequired
		}
		if input.SourceID != "" && !strings.EqualFold(input.SourceID, active.SourceID) {
			return Execution{}, ErrQueryScopeRequired
		}
		input.SourceID = active.SourceID
		input.SnapshotID = active.SnapshotID
	}
	startedExecution, err := o.store.Start(ctx, organization, input)
	if err != nil {
		return Execution{}, err
	}
	if startedExecution.State != ExecutionStateRunning || startedExecution.ID == "" {
		return Execution{}, fmt.Errorf("%w: store returned non-running execution", ErrQueryOrchestratorNotConfigured)
	}
	startedAt := startedExecution.StartedAt
	if startedAt == nil || startedAt.IsZero() {
		now := o.config.Now().UTC()
		startedAt = &now
	}

	request := QueryRetrievalInput{
		ExecutionID:  startedExecution.ID,
		RequestID:    startedExecution.ID,
		Question:     input.Question,
		QuestionKind: input.QuestionKind,
		Scope: Scope{
			OrganizationID: identity.CanonicalUUID("organization", organization),
			SourceID:       input.SourceID,
			SnapshotID:     input.SnapshotID,
		},
		Limit:    0,
		Deadline: o.deadline(ctx),
	}
	outcome, pipelineErr := o.execute(ctx, request, input, *startedAt)
	if pipelineErr != nil {
		outcome.FinishedAt = o.config.Now().UTC()
		if outcome.FinishedAt.Before(outcome.StartedAt) {
			outcome.FinishedAt = outcome.StartedAt
		}
		outcome.State = ExecutionStateFailed
		outcome.DiagnosticCode = queryDiagnostic(pipelineErr)
		outcome.HasResponse = false
		outcome.Response = Response{}
		outcome.PackageDigest = ""
	}
	finishCtx, cancel := durableContext(ctx, o.config.PersistenceTimeout)
	defer cancel()
	finished, finishErr := o.store.Finish(finishCtx, organization, outcome)
	if finishErr != nil {
		return Execution{}, finishErr
	}
	if pipelineErr != nil {
		return Execution{}, pipelineErr
	}
	return finished, nil
}

func sameScopeOrganization(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

// Get re-inspects only the configured organization's durable execution.
func (o *QueryOrchestrator) Get(ctx context.Context, organization, executionID string) (Execution, error) {
	if o == nil || o.store == nil {
		return Execution{}, ErrQueryOrchestratorNotConfigured
	}
	return o.store.Get(ctx, organization, executionID)
}

func (o *QueryOrchestrator) execute(ctx context.Context, request QueryRetrievalInput, input ExecutionInput, startedAt time.Time) (QueryOutcome, error) {
	outcome := QueryOutcome{
		ExecutionID:    request.ExecutionID,
		Input:          input,
		QuestionDigest: input.QuestionDigest,
		StartedAt:      startedAt.UTC(),
	}
	if err := request.Scope.Validate(); err != nil {
		return outcome, ErrQueryScopeRequired
	}
	if err := contextErr(ctx); err != nil {
		return outcome, err
	}
	retrieved, err := o.retriever.Retrieve(ctx, request)
	if err != nil {
		return outcome, err
	}
	outcome.Retrieval = retrieved
	composition, err := ComposeEvidencePackage(ctx, PackageRequest{
		Scope:          request.Scope,
		Candidates:     retrieved.Candidates,
		Limits:         o.config.PackageLimits,
		TransferPolicy: o.config.TransferPolicy,
	})
	if err != nil {
		return outcome, err
	}
	outcome.Composition = composition

	abstention, err := EvaluateAbstention(AbstentionInput{
		Package:                composition.ValidationPackage,
		QueryID:                request.ExecutionID,
		QueryDigest:            input.QuestionDigest,
		QuestionKind:           request.QuestionKind,
		Support:                retrieved.Support,
		LocalOnlyEvidenceCount: retrieved.LocalOnlyEvidenceCount,
	})
	if err != nil {
		return outcome, err
	}
	if abstention.Decision.Abstain {
		abstention.Response = ensureStableClaimIDs(abstention.Response, request.Scope.OrganizationID, request.ExecutionID, composition.ValidationPackage.Digest)
		responseBytes, err := json.Marshal(abstention.Response)
		if err != nil {
			return outcome, err
		}
		outcome.State = ExecutionStateAbstained
		outcome.PackageDigest = composition.ValidationPackage.Digest
		outcome.Response = abstention.Response
		outcome.HasResponse = true
		outcome.FinishedAt = o.config.Now().UTC()
		_ = responseBytes // marshaling above also verifies the persisted shape.
		return outcome, nil
	}
	if o.generator == nil {
		return outcome, ErrQueryGeneratorNotConfigured
	}
	generationStarted := o.config.Now().UTC()
	generationRequest := aigateway.GenerationRequest{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Deadline:    request.Deadline,
		Profile:     o.config.GenerationProfile,
		Question:    input.Question,
		Package:     composition.GatewayPackage,
	}
	result, err := o.generator.Generate(ctx, generationRequest)
	generationFinished := generationStarted.Add(result.Latency)
	outcome.Generation = generationAudit(generationRequest, result, generationStarted, generationFinished, err)
	if err != nil {
		return outcome, err
	}
	candidate, err := o.responseFromGeneration(generationRequest, result, composition.ValidationPackage, generationStarted, generationFinished, input.QuestionDigest)
	if err != nil {
		return outcome, err
	}
	validated, err := ValidateAndRepairResponse(ctx, candidate, ResponseValidationContext{
		Package:     composition.ValidationPackage,
		QueryID:     request.ExecutionID,
		QueryDigest: input.QuestionDigest,
	}, o.repairer, o.config.RepairPolicy)
	if err != nil {
		return outcome, err
	}
	outcome.Response = validated.Response
	outcome.HasResponse = true
	outcome.PackageDigest = composition.ValidationPackage.Digest
	outcome.FinishedAt = generationFinished
	switch validated.Response.Generation.Termination {
	case TerminationCompleted:
		outcome.State = ExecutionStateCompleted
	case TerminationPartial:
		outcome.State = ExecutionStatePartial
	default:
		return outcome, ErrQueryResponseUncited
	}
	return outcome, nil
}

func ensureStableClaimIDs(response Response, organizationID, executionID, packageDigest string) Response {
	for index := range response.Claims {
		if response.Claims[index].ID != "" {
			continue
		}
		response.Claims[index].ID = identity.CanonicalUUID(
			"claim", organizationID, executionID, packageDigest, fmt.Sprint(response.Claims[index].Ordinal),
		)
	}
	return response
}

func (o *QueryOrchestrator) responseFromGeneration(request aigateway.GenerationRequest, result aigateway.GenerationResult, validationPackage EvidencePackage, startedAt, finishedAt time.Time, queryDigest string) ([]byte, error) {
	if len(result.Output.EvidenceIDs) == 0 {
		return nil, ErrQueryResponseUncited
	}
	provider, ok := queryProvider(result.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: provider", ErrQueryResponseUncited)
	}
	protocol, ok := queryProtocol(request.Profile.Protocol)
	if !ok {
		return nil, fmt.Errorf("%w: protocol", ErrQueryResponseUncited)
	}
	byID := make(map[string]EvidenceReference, len(validationPackage.Evidence))
	for _, reference := range validationPackage.Evidence {
		byID[reference.ID] = reference
	}
	citations := make([]Citation, 0, len(result.Output.EvidenceIDs))
	seen := make(map[string]struct{}, len(result.Output.EvidenceIDs))
	for index, id := range result.Output.EvidenceIDs {
		reference, exists := byID[id]
		if !exists {
			return nil, ErrQueryResponseUncited
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, ErrQueryResponseUncited
		}
		seen[id] = struct{}{}
		citations = append(citations, Citation{
			Ordinal:        index + 1,
			OrganizationID: reference.OrganizationID,
			SourceID:       reference.SourceID,
			SnapshotID:     reference.SnapshotID,
			EvidenceID:     reference.ID,
			Locator:        reference.Locator,
			Role:           CitationRoleSupports,
		})
	}
	claimText := strings.TrimSpace(result.Output.Text)
	if claimText == "" {
		return nil, ErrQueryResponseUncited
	}
	claim := Claim{
		ID:               identity.CanonicalUUID("claim", validationPackage.OrganizationID, request.ExecutionID, validationPackage.Digest, "1"),
		Ordinal:          1,
		Kind:             ClaimKindGenerated,
		Support:          SupportSupported,
		Text:             claimText,
		CitationOrdinals: make([]int, len(citations)),
	}
	for index := range citations {
		claim.CitationOrdinals[index] = index + 1
	}
	gaps := make([]Gap, 0, len(result.Output.Gaps))
	for index, gap := range result.Output.Gaps {
		gaps = append(gaps, Gap{
			Ordinal: index + 1,
			ID:      "gap-" + gap,
			Code:    gap,
			Message: "A material gap was reported for this response.",
		})
	}
	response := Response{
		Version:        Version,
		KnowledgeState: KnowledgeStateGeneratedReviewable,
		Text:           claimText,
		Claims:         []Claim{claim},
		Citations:      citations,
		Gaps:           gaps,
		Generation: GenerationMetadata{
			Provider:      provider,
			Model:         result.Model,
			Profile:       request.Profile.Model,
			Protocol:      protocol,
			Usage:         Usage{InputItems: result.Usage.InputItems, OutputItems: result.Usage.OutputItems, InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens},
			Termination:   queryTermination(result.Termination),
			PackageID:     validationPackage.ID,
			PackageDigest: validationPackage.Digest,
			QueryID:       request.ExecutionID,
			QueryDigest:   queryDigest,
			StartedAt:     startedAt,
			FinishedAt:    finishedAt,
			Latency:       finishedAt.Sub(startedAt),
		},
	}
	if err := response.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func (o *QueryOrchestrator) deadline(ctx context.Context) time.Time {
	if deadline, ok := ctx.Deadline(); ok {
		return deadline
	}
	return o.config.Now().UTC().Add(o.config.GenerationTimeout)
}

func validateOrchestratorInput(ctx context.Context, input ExecutionInput) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(input.Question) == "" || strings.ContainsAny(input.Question, "\x00\r\n") {
		return ErrInvalidResponse
	}
	digest := sha256.Sum256([]byte(input.Question))
	if input.QuestionDigest != hex.EncodeToString(digest[:]) {
		return ErrInvalidDigest
	}
	if !validKnowledgeQuestionKind(input.QuestionKind) {
		return ErrInvalidAbstentionInput
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func durableContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	base := context.WithoutCancel(ctx)
	if timeout <= 0 {
		return base, func() {}
	}
	return context.WithTimeout(base, timeout)
}

func queryDiagnostic(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "request_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "request_timeout"
	case errors.Is(err, ErrQueryScopeRequired):
		return "query_scope_required"
	case errors.Is(err, ErrQueryGeneratorNotConfigured):
		return "generator_not_configured"
	case errors.Is(err, ErrQueryResponseUncited):
		return "response_not_evidence_backed"
	default:
		return "query_pipeline_failed"
	}
}

func generationAudit(request aigateway.GenerationRequest, result aigateway.GenerationResult, startedAt, finishedAt time.Time, callErr error) *GenerationAudit {
	requestDigest := digestJSON(struct {
		ExecutionID   string
		PackageID     string
		PackageDigest string
		Profile       aigateway.GenerationProfile
	}{request.ExecutionID, request.Package.ID, request.Package.Digest, request.Profile})
	responseDigest := ""
	if result.Output.PackageDigest != "" {
		responseDigest = digestJSON(result.Output)
	}
	state := "succeeded"
	errorCode := ""
	if callErr != nil {
		state = "failed"
		errorCode = queryDiagnostic(callErr)
	}
	return &GenerationAudit{
		Capability:                string(aigateway.CapabilityGeneration),
		Provider:                  request.Profile.Provider,
		ConfiguredModel:           request.Profile.Model,
		EffectiveModel:            result.Model,
		Protocol:                  request.Profile.Protocol,
		RequestDigest:             requestDigest,
		ResponseDigest:            responseDigest,
		TransferredEvidenceDigest: request.Package.Digest,
		TransferredEvidenceCount:  len(request.Package.Evidence),
		State:                     state,
		ErrorCode:                 errorCode,
		AttemptCount:              1,
		Usage:                     result.Usage,
		Latency:                   result.Latency,
		StartedAt:                 startedAt,
		FinishedAt:                finishedAt,
	}
}

func digestJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func queryProvider(provider aigateway.Provider) (Provider, bool) {
	switch provider {
	case aigateway.ProviderSimulated:
		return ProviderSimulated, true
	case aigateway.ProviderOpenAI:
		return ProviderOpenAI, true
	case aigateway.ProviderOpenAICompatible:
		return ProviderOpenAICompatible, true
	case aigateway.ProviderOpenRouter:
		return ProviderOpenRouter, true
	default:
		return ProviderNone, false
	}
}

func queryProtocol(protocol aigateway.Protocol) (Protocol, bool) {
	switch protocol {
	case aigateway.ProtocolResponses:
		return ProtocolResponses, true
	case aigateway.ProtocolChatCompletions:
		return ProtocolChatCompletions, true
	default:
		return ProtocolNone, false
	}
}

func queryTermination(termination aigateway.Termination) Termination {
	switch termination {
	case aigateway.TerminationCompleted:
		return TerminationCompleted
	case aigateway.TerminationPartial:
		return TerminationPartial
	case aigateway.TerminationAbstained:
		return TerminationAbstained
	default:
		return TerminationPartial
	}
}
