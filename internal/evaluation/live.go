package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const (
	LiveTransferPolicyAllow  = "allow"
	LiveTerminationCompleted = "completed"
	LiveTerminationAbstained = "abstained"
	LiveTerminationPartial   = "partial"
	maxLiveOutputTokens      = 1_000_000
	maxLiveTokenPriceUSD     = 1_000_000
)

var (
	ErrLiveNotOptedIn       = errors.New("evaluation: live mode is not explicitly enabled")
	ErrLiveConfirmation     = errors.New("evaluation: live policy confirmation is required")
	ErrLivePolicy           = errors.New("evaluation: external transfer policy is not approved")
	ErrLiveBudget           = errors.New("evaluation: live budget exceeded or invalid")
	ErrLivePriceTable       = errors.New("evaluation: explicit price table is required")
	ErrLiveProvider         = errors.New("evaluation: live provider is unavailable")
	ErrLiveProviderResponse = errors.New("evaluation: live provider response is invalid")
)

// LiveProvider is the only execution port used by RunLive. It is the same
// gateway contract used by the query pipeline: the request carries the
// already policy-authorized package in memory, while the report stores only
// identifiers and hashes. Tests use deterministic fakes and no default
// implementation opens a socket.
type LiveProvider = aigateway.Generator

// LiveConfig controls one live evaluation. OptIn and both confirmations are
// intentionally independent so a caller cannot activate external transfer by
// merely changing a provider or model flag.
type LiveConfig struct {
	Cases           CaseSet
	CasesPath       string
	RunID           string
	TopK            int
	Repeat          bool
	ToolVersion     string
	Now             func() time.Time
	OptIn           bool
	ConfirmPolicy   bool
	ConfirmTransfer bool
	TransferPolicy  string
	Provider        string
	RequestedModel  string
	MaxOutputTokens int
	Generation      aigateway.GenerationProfile
	Timeout         time.Duration
	Budget          LiveBudgetConfig
	PriceTable      LivePriceTable
	ProviderClient  LiveProvider
	Extractor       Extractor
}

// Validate checks every live activation boundary without contacting a
// provider. ProviderClient is deliberately checked here so RunLive cannot
// silently fall back to simulated generation.
func (c LiveConfig) Validate() error {
	if !c.OptIn {
		return ErrLiveNotOptedIn
	}
	if !c.ConfirmPolicy || !c.ConfirmTransfer {
		return ErrLiveConfirmation
	}
	if c.TransferPolicy != LiveTransferPolicyAllow {
		return ErrLivePolicy
	}
	if !liveIdentifier(c.Provider) || !liveIdentifier(c.RequestedModel) || c.MaxOutputTokens <= 0 || c.MaxOutputTokens > maxLiveOutputTokens {
		return fmt.Errorf("%w: provider/model/output configuration", ErrLiveProvider)
	}
	if c.Provider == string(aigateway.ProviderSimulated) {
		return fmt.Errorf("%w: simulated provider is not allowed in live mode", ErrLiveProvider)
	}
	if c.Timeout <= 0 {
		return fmt.Errorf("%w: generation timeout must be positive", ErrLiveProvider)
	}
	if err := c.Generation.Validate(); err != nil {
		return fmt.Errorf("%w: generation profile", ErrLiveProvider)
	}
	if c.Generation.Provider != aigateway.Provider(c.Provider) || c.Generation.Model != c.RequestedModel {
		return fmt.Errorf("%w: generation profile does not match configured provider/model", ErrLiveProvider)
	}
	if c.Generation.MaxOutputBytes <= 0 {
		return fmt.Errorf("%w: generation profile output limit", ErrLiveProvider)
	}
	if err := c.Budget.Validate(); err != nil {
		return err
	}
	if err := c.PriceTable.Validate(); err != nil {
		return err
	}
	if c.PriceTable.Provider != c.Provider || c.PriceTable.Model != c.RequestedModel {
		return fmt.Errorf("%w: price table does not match provider model", ErrLivePriceTable)
	}
	if c.ProviderClient == nil {
		return ErrLiveProvider
	}
	return nil
}

// Validate checks positive independent limits. Every dimension is reserved
// before a call, so a missing dimension cannot accidentally authorize spend.
func (b LiveBudgetConfig) Validate() error {
	if b.MaxRequests <= 0 || b.MaxInputTokens <= 0 || b.MaxOutputTokens <= 0 ||
		!finitePositive(b.MaxCostUSD) {
		return ErrLiveBudget
	}
	return nil
}

// Validate checks an explicit versioned price table. Prices must be positive
// because a zero-value table is indistinguishable from an absent table and
// must never authorize an external call.
func (p LivePriceTable) Validate() error {
	if !liveIdentifier(p.Version) || !liveIdentifier(p.Provider) || !liveIdentifier(p.Model) ||
		!boundedLivePrice(p.InputTokenUSD) || !boundedLivePrice(p.OutputTokenUSD) {
		return ErrLivePriceTable
	}
	return nil
}

// LiveUsageEstimate is the conservative reservation made before a call.
type LiveUsageEstimate struct {
	InputTokens  int
	OutputTokens int
}

// LiveUsage remains a descriptive alias for budget tests and callers. Gateway
// usage is normalized into this bounded pair before settlement.
type LiveUsage = LiveUsageEstimate

// LiveBudgetLedger is safe for concurrent callers and reserves the maximum
// permitted output before invoking a provider. Reservations are not released
// after settlement, which keeps the next-call check conservative even when a
// provider reports lower usage.
type LiveBudgetLedger struct {
	mu     sync.Mutex
	limit  LiveBudgetConfig
	prices LivePriceTable
	used   LiveUsageSnapshot
}

// LiveBudgetReservation is one pre-call reservation.
type LiveBudgetReservation struct {
	ledger        *LiveBudgetLedger
	estimate      LiveUsageEstimate
	estimatedCost float64
	settled       bool
}

// NewLiveBudgetLedger validates explicit limits and pricing before any call.
func NewLiveBudgetLedger(limit LiveBudgetConfig, prices LivePriceTable) (*LiveBudgetLedger, error) {
	if err := limit.Validate(); err != nil {
		return nil, err
	}
	if err := prices.Validate(); err != nil {
		return nil, err
	}
	return &LiveBudgetLedger{limit: limit, prices: prices}, nil
}

// Snapshot returns safe counters and cost values.
func (l *LiveBudgetLedger) Snapshot() LiveUsageSnapshot {
	if l == nil {
		return LiveUsageSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used
}

// Reserve performs all limit checks before the provider call.
func (l *LiveBudgetLedger) Reserve(estimate LiveUsageEstimate) (*LiveBudgetReservation, error) {
	if l == nil || estimate.InputTokens <= 0 || estimate.OutputTokens <= 0 {
		return nil, ErrLiveBudget
	}
	cost := l.cost(estimate)
	if !finiteNonNegative(cost) {
		return nil, ErrLiveBudget
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used.Requests >= l.limit.MaxRequests ||
		estimate.InputTokens > l.limit.MaxInputTokens-l.used.ReservedInputTokens ||
		estimate.OutputTokens > l.limit.MaxOutputTokens-l.used.ReservedOutputTokens ||
		cost > l.limit.MaxCostUSD-l.used.ReservedCostUSD {
		return nil, ErrLiveBudget
	}
	l.used.Requests++
	l.used.ReservedInputTokens += estimate.InputTokens
	l.used.ReservedOutputTokens += estimate.OutputTokens
	l.used.ReservedCostUSD += cost
	return &LiveBudgetReservation{ledger: l, estimate: estimate, estimatedCost: cost}, nil
}

// Settle records provider-reported usage. A provider cannot increase a
// reservation; an over-report is a sanitized budget failure and prevents any
// subsequent call from being made by RunLive.
func (r *LiveBudgetReservation) Settle(actual LiveUsage) error {
	if r == nil || r.ledger == nil || r.settled || actual.InputTokens < 0 || actual.OutputTokens < 0 {
		return ErrLiveBudget
	}
	actualCost := r.ledger.cost(LiveUsageEstimate{InputTokens: actual.InputTokens, OutputTokens: actual.OutputTokens})
	r.ledger.mu.Lock()
	defer r.ledger.mu.Unlock()
	r.settled = true
	r.ledger.used.ActualInputTokens += actual.InputTokens
	r.ledger.used.ActualOutputTokens += actual.OutputTokens
	r.ledger.used.ActualCostUSD += actualCost
	if actual.InputTokens > r.estimate.InputTokens || actual.OutputTokens > r.estimate.OutputTokens || actualCost > r.estimatedCost || !finiteNonNegative(actualCost) {
		return ErrLiveBudget
	}
	return nil
}

// RunLive executes only after all explicit live gates pass. Extraction and
// retrieval remain local and deterministic; only generation crosses the
// injected gateway port. The package passed to that port is the exact
// policy-filtered GatewayPackage produced by the compositor and exists only
// in memory. Provider errors are reduced to safe categories, and no
// simulated generation is substituted after a live failure.
func RunLive(ctx context.Context, config LiveConfig) (Report, error) {
	if ctx == nil {
		return Report{}, ErrLiveProvider
	}
	if err := config.Validate(); err != nil {
		return Report{}, err
	}
	cases, err := loadConfiguredCases(Config{Cases: config.Cases, CasesPath: config.CasesPath})
	if err != nil {
		return Report{}, err
	}
	extractor := config.Extractor
	if extractor == nil {
		extractor = simulatedExtractor{}
	}
	topK := config.TopK
	if topK == 0 {
		topK = defaultTopK
	}
	base, err := Run(ctx, Config{
		Cases: cases, Mode: ModeSimulated, RunID: config.RunID, TopK: config.TopK,
		Repeat: config.Repeat, ToolVersion: config.ToolVersion, Now: config.Now, Extractor: extractor,
	})
	if err != nil {
		return Report{}, err
	}
	base.Mode = ModeLive
	for index := range base.Cases {
		base.Cases[index].Mode = ModeLive
	}
	base.Limitations = append(base.Limitations,
		"execução live autorizada somente pelo gateway configurado; nenhum fallback simulado é usado",
		"o relatório conserva apenas IDs e hashes das evidências transferíveis",
		"o conteúdo autorizado existe somente em memória durante a chamada externa",
		"custo é calculado exclusivamente pela tabela de preços explícita e versionada",
	)
	liveReport := &LiveReport{
		Provider:          config.Provider,
		RequestedModel:    config.RequestedModel,
		TransferPolicy:    config.TransferPolicy,
		PolicyConfirmed:   config.ConfirmPolicy,
		TransferConfirmed: config.ConfirmTransfer,
		PriceTable:        config.PriceTable,
		Budget:            config.Budget,
	}
	ledger, err := NewLiveBudgetLedger(config.Budget, config.PriceTable)
	if err != nil {
		return Report{}, err
	}
	for index := range base.Cases {
		if err := ctx.Err(); err != nil {
			liveReport.Halted = true
			liveReport.HaltCode = "cancelled"
			markLiveNotAttempted(base.Cases[index+1:], "live_cancelled")
			break
		}
		item := cases.Cases[index]
		caseReport := &base.Cases[index]
		if item.Kind == CaseKindAbstention {
			continue
		}
		resetLiveGeneration(caseReport)
		if caseReport.Extraction.Status == StageFailed || caseReport.Ingestion.Status == StageFailed || caseReport.Retrieval.Status == StageFailed {
			continue
		}
		composition, scope, prepareErr := prepareLivePackage(ctx, item, extractor, topK, config.Repeat, config.Now)
		if prepareErr != nil {
			markLiveFailure(caseReport, FailureStageRetrieval, "live_package_prepare_failed")
			continue
		}
		if err := composition.Validate(); err != nil {
			markLiveFailure(caseReport, FailureStagePolicy, "live_transfer_not_authorized")
			continue
		}
		packageInput := composition.GatewayPackage
		evidence := transferEvidencePackage(packageInput)
		if len(evidence) == 0 {
			markLiveFailure(caseReport, FailureStagePolicy, "live_transfer_not_authorized")
			continue
		}
		request := aigateway.GenerationRequest{
			ExecutionID: "live-" + digestString(config.Provider + "\x00" + config.RequestedModel)[:24],
			RequestID:   "live-" + digestString(item.CaseID + "\x00" + fmt.Sprint(item.CaseVersion))[:24],
			Deadline:    time.Now().Add(config.Timeout), Profile: config.Generation,
			Question: item.CompetenceQuestion, Package: packageInput,
		}
		if err := request.Validate(); err != nil {
			markLiveFailure(caseReport, FailureStageGeneration, "live_request_invalid")
			continue
		}
		inputEstimate := conservativeLiveInputTokens(request)
		estimate := LiveUsageEstimate{InputTokens: inputEstimate, OutputTokens: config.MaxOutputTokens}
		reservation, reserveErr := ledger.Reserve(estimate)
		if reserveErr != nil {
			liveReport.Calls = append(liveReport.Calls, blockedLiveCall(item.CaseID, request.RequestID, config.Provider, config.RequestedModel, estimate, ledger.cost(estimate), "live_budget_exceeded"))
			liveReport.Halted = true
			liveReport.HaltCode = "live_budget_exceeded"
			markLiveFailure(caseReport, FailureStageGeneration, "live_budget_exceeded")
			markLiveNotAttempted(base.Cases[index+1:], "live_budget_not_attempted")
			break
		}
		started := time.Now()
		result, providerErr := config.ProviderClient.Generate(ctx, request)
		latency := time.Since(started)
		actualUsage := safeLiveUsage(result.Usage)
		settleErr := reservation.Settle(actualUsage)
		reportedProvider := safeLiveReportIdentifier(string(result.Provider))
		reportedModel := safeLiveReportIdentifier(result.Model)
		reportedDigest := liveOutputDigest(result.Output.Text)
		call := LiveCallMetric{
			CaseID: item.CaseID, RequestID: request.RequestID, Provider: reportedProvider, Model: reportedModel,
			InputTokens: actualUsage.InputTokens, OutputTokens: actualUsage.OutputTokens,
			EstimatedCostUSD: ledger.cost(estimate), ActualCostUSD: ledger.cost(LiveUsageEstimate{InputTokens: actualUsage.InputTokens, OutputTokens: actualUsage.OutputTokens}),
			Latency: latency, Termination: safeLiveTermination(string(result.Termination)), OutputDigest: reportedDigest, TransferredEvidence: append([]LiveEvidenceRef(nil), evidence...),
		}
		if settleErr != nil {
			call.Outcome = "failed"
			call.ErrorCode = "live_budget_exceeded"
			liveReport.Calls = append(liveReport.Calls, call)
			markLiveFailure(caseReport, FailureStageGeneration, call.ErrorCode)
			liveReport.Halted = true
			liveReport.HaltCode = call.ErrorCode
			markLiveNotAttempted(base.Cases[index+1:], "live_budget_not_attempted")
			break
		}
		if providerErr != nil {
			call.Outcome = "failed"
			call.ErrorCode = sanitizedLiveError(providerErr)
			liveReport.Calls = append(liveReport.Calls, call)
			markLiveFailure(caseReport, FailureStageGeneration, call.ErrorCode)
			continue
		}
		if err := result.Validate(request); err != nil {
			call.Outcome = "failed"
			call.ErrorCode = "live_response_invalid"
			liveReport.Calls = append(liveReport.Calls, call)
			markLiveFailure(caseReport, FailureStageGeneration, call.ErrorCode)
			continue
		}
		response := responseFromGeneration(item, composition, scope, result, retrieval.FusionResponse{})
		if err := (query.ResponseValidationContext{Package: composition.ValidationPackage, QueryID: queryID(item), QueryDigest: queryDigest(item.CompetenceQuestion)}).Validate(response); err != nil {
			call.Outcome = "failed"
			call.ErrorCode = "live_response_invalid"
			liveReport.Calls = append(liveReport.Calls, call)
			markLiveFailure(caseReport, FailureStageGeneration, call.ErrorCode)
			continue
		}
		call.Outcome = "completed"
		liveReport.Calls = append(liveReport.Calls, call)
		metric := generationMetricFromResponse(response, result.Termination == aigateway.TerminationAbstained, false, latency)
		metric.Status = StageCompleted
		metric.Provider = reportedProvider
		metric.Model = reportedModel
		metric.Profile = config.PriceTable.Version
		metric.InputTokens = actualUsage.InputTokens
		metric.OutputTokens = actualUsage.OutputTokens
		metric.LocalOnly = false
		caseReport.Generation = metric
		if caseReport.FailureStage == "" {
			caseReport.Outcome = "passed"
		}
	}
	for _, call := range liveReport.Calls {
		liveReport.TransferredEvidence = appendUniqueLiveEvidence(liveReport.TransferredEvidence, call.TransferredEvidence...)
	}
	liveReport.Usage = ledger.Snapshot()
	base.Live = liveReport
	base.FinishedAt = time.Now().UTC()
	if config.Now != nil {
		base.FinishedAt = config.Now().UTC()
	}
	base.Summary = summarize(base.Cases)
	base.ReproducibilityDigest = reportDigest(base)
	return base, nil
}

// prepareLivePackage reruns the same local extraction/ingestion/retrieval
// pipeline used by Run and returns the exact compositor output. This keeps the
// live boundary honest: it never reconstructs evidence from report IDs or
// from the expected rubric.
func prepareLivePackage(ctx context.Context, item EvaluationCase, extractor Extractor, topK int, repeat bool, now func() time.Time) (query.Composition, query.Scope, error) {
	bundleInput, expectedToCanonical, err := extractor.Extract(ctx, item)
	if err != nil {
		return query.Composition{}, query.Scope{}, errors.New("evaluation: live package extraction failed")
	}
	if err := bundleInput.Validate(); err != nil {
		return query.Composition{}, query.Scope{}, errors.New("evaluation: live package bundle is invalid")
	}
	clock := time.Now().UTC()
	if now != nil {
		clock = now().UTC()
	}
	workspace := newSimulationWorkspace(bundleInput, expectedToCanonical, clock)
	if err := workspace.ingest(ctx, bundleInput, clock, "live"); err != nil {
		return query.Composition{}, query.Scope{}, errors.New("evaluation: live package ingestion failed")
	}
	if repeat {
		if err := workspace.ingest(ctx, bundleInput, clock, "live-repeat"); err != nil {
			return query.Composition{}, query.Scope{}, errors.New("evaluation: live package repeat failed")
		}
	}
	_, composition, scope, err := workspace.retrieveAndCompose(ctx, item, topK)
	if err != nil {
		return query.Composition{}, query.Scope{}, errors.New("evaluation: live package retrieval failed")
	}
	return composition, scope, nil
}

func resetLiveGeneration(item *CaseReport) {
	if item == nil {
		return
	}
	item.Generation = GenerationMetric{Status: StageSkipped, ExpectedAbstention: false, AbstentionCorrect: true}
	if item.FailureStage == FailureStageGeneration && item.FailureCode == "generation_failed" {
		item.FailureStage, item.FailureCode = "", ""
	}
	if item.Outcome == "passed" {
		item.Outcome = "partial"
	}
}

func markLiveFailure(item *CaseReport, stage FailureStage, code string) {
	if item == nil {
		return
	}
	item.Generation.Status = StageFailed
	item.Generation.ErrorCode = code
	item.FailureStage, item.FailureCode = stage, code
	item.Outcome = "failed"
}

func markLiveNotAttempted(items []CaseReport, code string) {
	for index := range items {
		if items[index].Generation.ExpectedAbstention {
			continue
		}
		resetLiveGeneration(&items[index])
		markLiveFailure(&items[index], FailureStageGeneration, code)
	}
}

func transferEvidencePackage(input aigateway.EvidencePackage) []LiveEvidenceRef {
	result := make([]LiveEvidenceRef, 0, len(input.Evidence))
	for _, item := range input.Evidence {
		if item.ID == "" || !isSHA256Digest(item.ContentDigest) {
			continue
		}
		result = appendUniqueLiveEvidence(result, LiveEvidenceRef{EvidenceID: item.ID, ContentHash: item.ContentDigest})
	}
	return result
}

func appendUniqueLiveEvidence(items []LiveEvidenceRef, additions ...LiveEvidenceRef) []LiveEvidenceRef {
	seen := make(map[string]struct{}, len(items)+len(additions))
	for _, item := range items {
		seen[item.EvidenceID+"\x00"+item.ContentHash] = struct{}{}
	}
	for _, item := range additions {
		key := item.EvidenceID + "\x00" + item.ContentHash
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, item)
	}
	return items
}

func blockedLiveCall(caseID, requestID, provider, model string, estimate LiveUsageEstimate, cost float64, code string) LiveCallMetric {
	return LiveCallMetric{CaseID: caseID, RequestID: requestID, Provider: provider, Model: model,
		InputTokens: estimate.InputTokens, OutputTokens: estimate.OutputTokens, EstimatedCostUSD: cost, Outcome: "blocked", ErrorCode: code}
}

func conservativeLiveInputTokens(request aigateway.GenerationRequest) int {
	total := len([]byte(request.Question)) + len([]byte(request.Package.ID)) + len([]byte(request.Package.Digest))
	for _, gap := range request.Package.Gaps {
		total += len([]byte(gap))
	}
	for _, item := range request.Package.Evidence {
		total += len([]byte(item.ID)) + len([]byte(item.Content)) + len([]byte(item.ContentDigest)) + len([]byte(item.Locator))
	}
	if total < 1 {
		return 1
	}
	return total
}

func (l *LiveBudgetLedger) cost(usage LiveUsageEstimate) float64 {
	return float64(usage.InputTokens)*l.prices.InputTokenUSD + float64(usage.OutputTokens)*l.prices.OutputTokenUSD
}

func sanitizedLiveError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "live_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "live_timeout"
	}
	if errors.Is(err, ErrLiveBudget) {
		return "live_budget_exceeded"
	}
	if errors.Is(err, ErrLiveProviderResponse) {
		return "live_response_invalid"
	}
	return "live_provider_failed"
}

func liveIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\t ") && !containsLiveSecretPattern(value)
}

func isSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func boundedLivePrice(value float64) bool {
	return finitePositive(value) && value <= maxLiveTokenPriceUSD
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func containsLiveSecretPattern(value string) bool {
	lower := strings.ToLower(value)
	for _, pattern := range []string{"api_key", "apikey", "authorization", "bearer", "secret", "password", "token"} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func safeLiveUsage(usage aigateway.Usage) LiveUsageEstimate {
	return LiveUsageEstimate{InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens}
}

func safeLiveReportIdentifier(value string) string {
	if liveIdentifier(value) {
		return value
	}
	return ""
}

func safeLiveTermination(value string) string {
	if value == LiveTerminationCompleted || value == LiveTerminationAbstained || value == LiveTerminationPartial {
		return value
	}
	return ""
}

func liveOutputDigest(text string) string {
	if text == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}
