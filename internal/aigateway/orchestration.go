package aigateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	defaultRetryAttempts    = 3
	maxRetryAttempts        = 5
	defaultBackoff          = 10 * time.Millisecond
	defaultMaxBackoff       = 250 * time.Millisecond
	maxOrchestrationWorkers = 64
)

// OrchestrationBudget limits provider attempts and the normalized usage they
// may reserve. A zero limit means that dimension is not configured. Token
// prices are optional and are used only when MaxCostUSD is configured.
type OrchestrationBudget struct {
	MaxRequests        int
	MaxInputTokens     int
	MaxOutputTokens    int
	MaxCostUSD         float64
	InputTokenCostUSD  float64
	OutputTokenCostUSD float64
}

// BudgetSnapshot is a race-free copy of the consumption tracked by a
// BudgetLedger. Requests count started attempts, including failed attempts.
type BudgetSnapshot struct {
	Requests     int
	InputTokens  int
	OutputTokens int
	CostUSD      float64
}

// BudgetLedger serializes reservations shared by concurrent orchestrated
// calls. It never stores prompts, evidence, credentials or provider errors.
type BudgetLedger struct {
	mu    sync.Mutex
	limit OrchestrationBudget
	used  BudgetSnapshot
}

// NewBudgetLedger validates and creates a concurrent-safe budget ledger.
func NewBudgetLedger(limit OrchestrationBudget) (*BudgetLedger, error) {
	if err := validateOrchestrationBudget(limit); err != nil {
		return nil, err
	}
	return &BudgetLedger{limit: limit}, nil
}

// Snapshot returns the current usage without exposing mutable ledger state.
func (l *BudgetLedger) Snapshot() BudgetSnapshot {
	if l == nil {
		return BudgetSnapshot{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.used
}

type budgetReservation struct {
	ledger       *BudgetLedger
	provider     Provider
	reserved     Usage
	reservedCost float64
	settled      bool
}

func (l *BudgetLedger) reserve(provider Provider, usage Usage) (*budgetReservation, error) {
	if l == nil {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	if !validUsageNumbers(usage) {
		return nil, NewGatewayError(ErrorKindInvalidResponse, CapabilityUnknown, nil)
	}
	cost := l.cost(usage)
	if !validCost(cost) {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.checkLocked(provider, usage, cost); err != nil {
		return nil, err
	}
	l.used.Requests++
	l.used.InputTokens += usage.InputTokens
	l.used.OutputTokens += usage.OutputTokens
	l.used.CostUSD += cost
	return &budgetReservation{ledger: l, provider: provider, reserved: usage, reservedCost: cost}, nil
}

func (l *BudgetLedger) check(provider Provider, usage Usage) error {
	if l == nil {
		return NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	if !validUsageNumbers(usage) {
		return NewGatewayError(ErrorKindInvalidResponse, CapabilityUnknown, nil)
	}
	cost := l.cost(usage)
	if !validCost(cost) {
		return NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.checkLocked(provider, usage, cost)
}

func (l *BudgetLedger) checkLocked(provider Provider, usage Usage, cost float64) error {
	if provider != ProviderSimulated && !hasPositiveBudgetLimit(l.limit) {
		return NewGatewayError(ErrorKindBudget, CapabilityUnknown, nil)
	}
	if exceedsInt(l.used.Requests, 1, l.limit.MaxRequests) ||
		exceedsInt(l.used.InputTokens, usage.InputTokens, l.limit.MaxInputTokens) ||
		exceedsInt(l.used.OutputTokens, usage.OutputTokens, l.limit.MaxOutputTokens) ||
		exceedsFloat(l.used.CostUSD, cost, l.limit.MaxCostUSD) {
		return NewGatewayError(ErrorKindBudget, CapabilityUnknown, nil)
	}
	return nil
}

func (l *BudgetLedger) cost(usage Usage) float64 {
	return float64(usage.InputTokens)*l.limit.InputTokenCostUSD + float64(usage.OutputTokens)*l.limit.OutputTokenCostUSD
}

func (r *budgetReservation) settle(actual Usage) error {
	if r == nil || r.ledger == nil || r.settled {
		return NewGatewayError(ErrorKindConfiguration, CapabilityUnknown, nil)
	}
	if !validUsageNumbers(actual) {
		actual = Usage{}
	}
	ledger := r.ledger
	actualCost := ledger.cost(actual)
	if !validCost(actualCost) {
		actual = Usage{}
		actualCost = 0
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	r.settled = true
	ledger.used.InputTokens -= r.reserved.InputTokens
	ledger.used.OutputTokens -= r.reserved.OutputTokens
	ledger.used.CostUSD -= r.reservedCost
	ledger.used.InputTokens += actual.InputTokens
	ledger.used.OutputTokens += actual.OutputTokens
	ledger.used.CostUSD += actualCost
	if exceedsInt(ledger.used.InputTokens, 0, ledger.limit.MaxInputTokens) ||
		exceedsInt(ledger.used.OutputTokens, 0, ledger.limit.MaxOutputTokens) ||
		exceedsFloat(ledger.used.CostUSD, 0, ledger.limit.MaxCostUSD) {
		return NewGatewayError(ErrorKindBudget, CapabilityUnknown, nil)
	}
	return nil
}

func validUsageNumbers(usage Usage) bool {
	return usage.InputItems >= 0 && usage.OutputItems >= 0 &&
		usage.InputTokens >= 0 && usage.OutputTokens >= 0
}

func exceedsInt(current, additional, limit int) bool {
	if limit <= 0 {
		return false
	}
	if current < 0 || additional < 0 || current > limit {
		return true
	}
	return additional > limit-current
}

func exceedsFloat(current, additional, limit float64) bool {
	if limit <= 0 {
		return false
	}
	return current > limit || additional > limit-current
}

func validateOrchestrationBudget(limit OrchestrationBudget) error {
	if limit.MaxRequests < 0 || limit.MaxInputTokens < 0 || limit.MaxOutputTokens < 0 ||
		limit.MaxCostUSD < 0 || math.IsNaN(limit.MaxCostUSD) || math.IsInf(limit.MaxCostUSD, 0) ||
		limit.InputTokenCostUSD < 0 || math.IsNaN(limit.InputTokenCostUSD) || math.IsInf(limit.InputTokenCostUSD, 0) ||
		limit.OutputTokenCostUSD < 0 || math.IsNaN(limit.OutputTokenCostUSD) || math.IsInf(limit.OutputTokenCostUSD, 0) {
		return fmt.Errorf("orchestration budget: %w", ErrConfiguration)
	}
	return nil
}

func validCost(cost float64) bool {
	return !math.IsNaN(cost) && !math.IsInf(cost, 0)
}

func hasPositiveBudgetLimit(limit OrchestrationBudget) bool {
	// External operation configuration requires every canonical dimension;
	// simulated operation remains usable with the zero-value budget.
	return limit.MaxRequests > 0 && limit.MaxInputTokens > 0 && limit.MaxOutputTokens > 0 && limit.MaxCostUSD > 0
}

// BackoffFunc computes the delay before a retry. Attempt is one-based.
type BackoffFunc func(attempt int) time.Duration

// JitterFunc returns the final delay after backoff. It is injectable so tests
// can use deterministic jitter without relying on a global random source.
type JitterFunc func(attempt int, backoff time.Duration) time.Duration

// SleepFunc abstracts waiting between attempts while retaining context
// cancellation in the caller's effective context.
type SleepFunc func(ctx context.Context, delay time.Duration) error

// RetryPolicy controls only transient retries. MaxAttempts includes the first
// provider call and is capped at a small value to prevent retry storms.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Backoff     BackoffFunc
	Jitter      JitterFunc
	Sleep       SleepFunc
}

func normalizeRetryPolicy(policy RetryPolicy) (RetryPolicy, error) {
	if policy.MaxAttempts == 0 {
		policy.MaxAttempts = defaultRetryAttempts
	}
	if policy.BaseDelay == 0 {
		policy.BaseDelay = defaultBackoff
	}
	if policy.MaxDelay == 0 {
		policy.MaxDelay = defaultMaxBackoff
	}
	if policy.MaxAttempts < 1 || policy.MaxAttempts > maxRetryAttempts || policy.BaseDelay < 0 || policy.MaxDelay < policy.BaseDelay {
		return RetryPolicy{}, fmt.Errorf("retry policy: %w", ErrConfiguration)
	}
	if policy.Backoff == nil {
		base, maximum := policy.BaseDelay, policy.MaxDelay
		policy.Backoff = func(attempt int) time.Duration {
			if attempt <= 1 {
				return base
			}
			delay := base
			for count := 1; count < attempt && delay < maximum; count++ {
				if delay > maximum/2 {
					return maximum
				}
				delay *= 2
			}
			if delay > maximum {
				return maximum
			}
			return delay
		}
	}
	if policy.Jitter == nil {
		policy.Jitter = func(_ int, delay time.Duration) time.Duration { return delay }
	}
	if policy.Sleep == nil {
		policy.Sleep = sleepContext
	}
	return policy, nil
}

// AttemptOutcome is the safe outcome category sent to an AttemptObserver.
type AttemptOutcome string

const (
	AttemptOutcomeSuccess AttemptOutcome = "success"
	AttemptOutcomeFailure AttemptOutcome = "failure"
	AttemptOutcomeBudget  AttemptOutcome = "budget"
)

// AttemptEvent contains only bounded audit dimensions. It deliberately has
// no error, prompt, evidence, response text, credential or provider payload.
type AttemptEvent struct {
	ExecutionID string         `json:"execution_id"`
	RequestID   string         `json:"request_id"`
	Capability  Capability     `json:"capability"`
	Provider    Provider       `json:"provider"`
	Model       string         `json:"model"`
	Batch       int            `json:"batch"`
	Attempt     int            `json:"attempt"`
	Latency     time.Duration  `json:"latency"`
	Usage       Usage          `json:"usage"`
	Outcome     AttemptOutcome `json:"outcome"`
	ErrorKind   ErrorKind      `json:"error_kind"`
}

// AttemptObserver receives one event for every provider attempt. Implementors
// must return promptly; orchestrators serialize callbacks for safety.
type AttemptObserver interface {
	ObserveAttempt(AttemptEvent)
}

// OrchestrationConfig configures one independent capability wrapper.
type OrchestrationConfig struct {
	Budget         OrchestrationBudget
	Retry          RetryPolicy
	MaxConcurrency int
	Observer       AttemptObserver
}

func normalizeOrchestrationConfig(config OrchestrationConfig) (OrchestrationConfig, *BudgetLedger, error) {
	if err := validateOrchestrationBudget(config.Budget); err != nil {
		return OrchestrationConfig{}, nil, err
	}
	retry, err := normalizeRetryPolicy(config.Retry)
	if err != nil {
		return OrchestrationConfig{}, nil, err
	}
	if config.MaxConcurrency == 0 {
		config.MaxConcurrency = 1
	}
	if config.MaxConcurrency < 1 || config.MaxConcurrency > maxOrchestrationWorkers {
		return OrchestrationConfig{}, nil, fmt.Errorf("orchestration concurrency: %w", ErrConfiguration)
	}
	config.Retry = retry
	ledger, err := NewBudgetLedger(config.Budget)
	if err != nil {
		return OrchestrationConfig{}, nil, err
	}
	return config, ledger, nil
}

// EmbeddingOrchestrator adds batching, concurrent budget reservations,
// transient retry and per-attempt observation to an Embedder.
type EmbeddingOrchestrator struct {
	backend    Embedder
	config     OrchestrationConfig
	ledger     *BudgetLedger
	slots      chan struct{}
	observerMu sync.Mutex
}

var _ Embedder = (*EmbeddingOrchestrator)(nil)

// NewEmbeddingOrchestrator wraps an Embedder without adding provider types.
func NewEmbeddingOrchestrator(backend Embedder, config OrchestrationConfig) (*EmbeddingOrchestrator, error) {
	if backend == nil {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	normalized, ledger, err := normalizeOrchestrationConfig(config)
	if err != nil {
		return nil, capabilityError(CapabilityEmbedding, err)
	}
	return &EmbeddingOrchestrator{
		backend: backend,
		config:  normalized,
		ledger:  ledger,
		slots:   make(chan struct{}, normalized.MaxConcurrency),
	}, nil
}

// Embed validates and splits a logical batch, then recombines vectors in the
// original item order after bounded concurrent execution.
func (o *EmbeddingOrchestrator) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	if o == nil || o.backend == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if ctx == nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if err := validateEmbeddingOrchestrationRequest(request); err != nil {
		return EmbeddingResult{}, capabilityError(CapabilityEmbedding, err)
	}
	effective, cancel, err := effectiveOrchestrationContext(ctx, request.Deadline, CapabilityEmbedding)
	if err != nil {
		return EmbeddingResult{}, err
	}
	defer cancel()
	itemsPerBatch := request.Profile.MaxBatchSize
	batches := make([]EmbeddingRequest, 0, (len(request.Items)+itemsPerBatch-1)/itemsPerBatch)
	for start := 0; start < len(request.Items); start += itemsPerBatch {
		end := start + itemsPerBatch
		if end > len(request.Items) {
			end = len(request.Items)
		}
		batch := request
		batch.Items = append([]EmbeddingItem(nil), request.Items[start:end]...)
		batches = append(batches, batch)
	}
	logicalStarted := time.Now()
	results, err := o.runEmbeddingBatches(effective, request, batches)
	if err != nil {
		return EmbeddingResult{}, err
	}

	combined := EmbeddingResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    results[0].Provider,
		Model:       results[0].Model,
		Vectors:     make([][]float64, 0, len(request.Items)),
		Termination: TerminationCompleted,
		Metadata:    cloneMetadata(results[0].Metadata),
	}
	for _, result := range results {
		if result.Provider != combined.Provider || result.Model != combined.Model {
			return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, nil)
		}
		combined.Vectors = append(combined.Vectors, result.Vectors...)
		combined.Usage.InputItems += result.Usage.InputItems
		combined.Usage.OutputItems += result.Usage.OutputItems
		combined.Usage.InputTokens += result.Usage.InputTokens
		combined.Usage.OutputTokens += result.Usage.OutputTokens
	}
	combined.Latency = time.Since(logicalStarted)
	validationRequest := request
	validationRequest.Profile.MaxBatchSize = len(request.Items)
	if err := combined.Validate(validationRequest); err != nil {
		return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, err)
	}
	return combined, nil
}

func (o *EmbeddingOrchestrator) runEmbeddingBatches(ctx context.Context, request EmbeddingRequest, batches []EmbeddingRequest) ([]EmbeddingResult, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	results := make([]EmbeddingResult, len(batches))
	jobs := make(chan int)
	workerCount := o.config.MaxConcurrency
	if workerCount > len(batches) {
		workerCount = len(batches)
	}
	var wg sync.WaitGroup
	var firstErr error
	var firstErrOnce sync.Once
	recordError := func(err error) {
		firstErrOnce.Do(func() {
			firstErr = err
			stop()
		})
	}
	worker := func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case index, ok := <-jobs:
				if !ok {
					return
				}
				result, err := o.embedBatch(ctx, batches[index], index+1)
				if err != nil {
					recordError(err)
					return
				}
				results[index] = result
			}
		}
	}
	wg.Add(workerCount)
	for count := 0; count < workerCount; count++ {
		go worker()
	}
send:
	for index := range batches {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := contextError(ctx, request.Deadline, CapabilityEmbedding); err != nil {
		return nil, err
	}
	return results, nil
}

func (o *EmbeddingOrchestrator) embedBatch(ctx context.Context, request EmbeddingRequest, batch int) (EmbeddingResult, error) {
	estimate := estimateEmbeddingUsage(request)
	for attempt := 1; attempt <= o.config.Retry.MaxAttempts; attempt++ {
		if err := acquireOrchestrationSlot(ctx, o.slots, CapabilityEmbedding); err != nil {
			return EmbeddingResult{}, err
		}
		if err := contextError(ctx, request.Deadline, CapabilityEmbedding); err != nil {
			releaseOrchestrationSlot(o.slots)
			return EmbeddingResult{}, err
		}
		reservation, err := o.ledger.reserve(request.Profile.Provider, estimate)
		if err != nil {
			releaseOrchestrationSlot(o.slots)
			o.observe(AttemptEvent{ExecutionID: request.ExecutionID, RequestID: request.RequestID, Capability: CapabilityEmbedding, Provider: request.Profile.Provider, Model: effectiveModel(request.Profile.Model, ""), Batch: batch, Attempt: attempt, Usage: estimate, Outcome: AttemptOutcomeBudget, ErrorKind: gatewayErrorKind(err)})
			return EmbeddingResult{}, capabilityError(CapabilityEmbedding, err)
		}
		started := time.Now()
		result, callErr := o.backend.Embed(ctx, request)
		latency := time.Since(started)
		releaseOrchestrationSlot(o.slots)
		if deadlineErr := contextError(ctx, request.Deadline, CapabilityEmbedding); deadlineErr != nil {
			callErr = deadlineErr
		}
		actual := usableUsage(result.Usage)
		settleErr := reservation.settle(actual)
		if settleErr != nil {
			o.observe(AttemptEvent{ExecutionID: request.ExecutionID, RequestID: request.RequestID, Capability: CapabilityEmbedding, Provider: effectiveProvider(request.Profile.Provider, result.Provider), Model: effectiveModel(request.Profile.Model, result.Model), Batch: batch, Attempt: attempt, Latency: latency, Usage: actual, Outcome: AttemptOutcomeBudget, ErrorKind: ErrorKindBudget})
			return EmbeddingResult{}, capabilityError(CapabilityEmbedding, settleErr)
		}
		if callErr == nil {
			if err := result.Validate(request); err != nil {
				o.observeAttempt(request, result, batch, attempt, latency, AttemptOutcomeFailure, ErrorKindInvalidResponse)
				return EmbeddingResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityEmbedding, err)
			}
			o.observeAttempt(request, result, batch, attempt, latency, AttemptOutcomeSuccess, "")
			return result, nil
		}
		normalized := NormalizeError(CapabilityEmbedding, callErr)
		kind := gatewayErrorKind(normalized)
		o.observeAttempt(request, result, batch, attempt, latency, AttemptOutcomeFailure, kind)
		if !retryableGatewayError(normalized) || attempt == o.config.Retry.MaxAttempts || hasUsage(result.Usage) {
			return EmbeddingResult{}, normalized
		}
		if err := o.ledger.check(request.Profile.Provider, estimate); err != nil {
			return EmbeddingResult{}, capabilityError(CapabilityEmbedding, err)
		}
		if err := o.backoff(ctx, request.Deadline, attempt); err != nil {
			return EmbeddingResult{}, err
		}
	}
	return EmbeddingResult{}, NewGatewayError(ErrorKindUnavailable, CapabilityEmbedding, nil)
}

// GenerationOrchestrator adds budget, effective deadlines, transient retry
// and per-attempt observation to a Generator.
type GenerationOrchestrator struct {
	backend    Generator
	config     OrchestrationConfig
	ledger     *BudgetLedger
	slots      chan struct{}
	observerMu sync.Mutex
}

var _ Generator = (*GenerationOrchestrator)(nil)

// NewGenerationOrchestrator wraps a Generator without adding provider types.
func NewGenerationOrchestrator(backend Generator, config OrchestrationConfig) (*GenerationOrchestrator, error) {
	if backend == nil {
		return nil, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	normalized, ledger, err := normalizeOrchestrationConfig(config)
	if err != nil {
		return nil, capabilityError(CapabilityGeneration, err)
	}
	return &GenerationOrchestrator{
		backend: backend,
		config:  normalized,
		ledger:  ledger,
		slots:   make(chan struct{}, normalized.MaxConcurrency),
	}, nil
}

// Generate executes one bounded generation request. A transient error with
// any reported usage is treated as ambiguous consumption and is not retried.
func (o *GenerationOrchestrator) Generate(ctx context.Context, request GenerationRequest) (GenerationResult, error) {
	if o == nil || o.backend == nil {
		return GenerationResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if ctx == nil {
		return GenerationResult{}, NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if err := request.Validate(); err != nil {
		return GenerationResult{}, capabilityError(CapabilityGeneration, err)
	}
	effective, cancel, err := effectiveOrchestrationContext(ctx, request.Deadline, CapabilityGeneration)
	if err != nil {
		return GenerationResult{}, err
	}
	defer cancel()
	return o.generate(effective, request)
}

func (o *GenerationOrchestrator) generate(ctx context.Context, request GenerationRequest) (GenerationResult, error) {
	estimate := estimateGenerationUsage(request)
	for attempt := 1; attempt <= o.config.Retry.MaxAttempts; attempt++ {
		if err := acquireOrchestrationSlot(ctx, o.slots, CapabilityGeneration); err != nil {
			return GenerationResult{}, err
		}
		if err := contextError(ctx, request.Deadline, CapabilityGeneration); err != nil {
			releaseOrchestrationSlot(o.slots)
			return GenerationResult{}, err
		}
		reservation, err := o.ledger.reserve(request.Profile.Provider, estimate)
		if err != nil {
			releaseOrchestrationSlot(o.slots)
			o.observe(AttemptEvent{ExecutionID: request.ExecutionID, RequestID: request.RequestID, Capability: CapabilityGeneration, Provider: request.Profile.Provider, Model: effectiveModel(request.Profile.Model, ""), Batch: 1, Attempt: attempt, Usage: estimate, Outcome: AttemptOutcomeBudget, ErrorKind: gatewayErrorKind(err)})
			return GenerationResult{}, capabilityError(CapabilityGeneration, err)
		}
		started := time.Now()
		result, callErr := o.backend.Generate(ctx, request)
		latency := time.Since(started)
		releaseOrchestrationSlot(o.slots)
		if deadlineErr := contextError(ctx, request.Deadline, CapabilityGeneration); deadlineErr != nil {
			callErr = deadlineErr
		}
		actual := usableUsage(result.Usage)
		settleErr := reservation.settle(actual)
		if settleErr != nil {
			o.observe(AttemptEvent{ExecutionID: request.ExecutionID, RequestID: request.RequestID, Capability: CapabilityGeneration, Provider: effectiveProvider(request.Profile.Provider, result.Provider), Model: effectiveModel(request.Profile.Model, result.Model), Batch: 1, Attempt: attempt, Latency: latency, Usage: actual, Outcome: AttemptOutcomeBudget, ErrorKind: ErrorKindBudget})
			return GenerationResult{}, capabilityError(CapabilityGeneration, settleErr)
		}
		if callErr == nil {
			if err := result.Validate(request); err != nil {
				o.observeAttempt(request, result, 1, attempt, latency, AttemptOutcomeFailure, ErrorKindInvalidResponse)
				return GenerationResult{}, NewGatewayError(ErrorKindInvalidResponse, CapabilityGeneration, err)
			}
			o.observeAttempt(request, result, 1, attempt, latency, AttemptOutcomeSuccess, "")
			return result, nil
		}
		normalized := NormalizeError(CapabilityGeneration, callErr)
		kind := gatewayErrorKind(normalized)
		o.observeAttempt(request, result, 1, attempt, latency, AttemptOutcomeFailure, kind)
		if !retryableGatewayError(normalized) || attempt == o.config.Retry.MaxAttempts || hasUsage(result.Usage) {
			return GenerationResult{}, normalized
		}
		if err := o.ledger.check(request.Profile.Provider, estimate); err != nil {
			return GenerationResult{}, capabilityError(CapabilityGeneration, err)
		}
		if err := o.backoff(ctx, request.Deadline, attempt); err != nil {
			return GenerationResult{}, err
		}
	}
	return GenerationResult{}, NewGatewayError(ErrorKindUnavailable, CapabilityGeneration, nil)
}

func (o *EmbeddingOrchestrator) backoff(ctx context.Context, deadline time.Time, attempt int) error {
	delay := o.config.Retry.Backoff(attempt)
	if delay < 0 {
		return NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if delay > o.config.Retry.MaxDelay {
		delay = o.config.Retry.MaxDelay
	}
	delay = o.config.Retry.Jitter(attempt, delay)
	if delay < 0 {
		return NewGatewayError(ErrorKindConfiguration, CapabilityEmbedding, nil)
	}
	if delay > o.config.Retry.MaxDelay {
		delay = o.config.Retry.MaxDelay
	}
	if err := o.config.Retry.Sleep(ctx, delay); err != nil {
		return NormalizeError(CapabilityEmbedding, err)
	}
	return contextError(ctx, deadline, CapabilityEmbedding)
}

func (o *GenerationOrchestrator) backoff(ctx context.Context, deadline time.Time, attempt int) error {
	delay := o.config.Retry.Backoff(attempt)
	if delay < 0 {
		return NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if delay > o.config.Retry.MaxDelay {
		delay = o.config.Retry.MaxDelay
	}
	delay = o.config.Retry.Jitter(attempt, delay)
	if delay < 0 {
		return NewGatewayError(ErrorKindConfiguration, CapabilityGeneration, nil)
	}
	if delay > o.config.Retry.MaxDelay {
		delay = o.config.Retry.MaxDelay
	}
	if err := o.config.Retry.Sleep(ctx, delay); err != nil {
		return NormalizeError(CapabilityGeneration, err)
	}
	return contextError(ctx, deadline, CapabilityGeneration)
}

func (o *EmbeddingOrchestrator) observeAttempt(request EmbeddingRequest, result EmbeddingResult, batch, attempt int, latency time.Duration, outcome AttemptOutcome, kind ErrorKind) {
	o.observe(AttemptEvent{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Capability:  CapabilityEmbedding,
		Provider:    effectiveProvider(request.Profile.Provider, result.Provider),
		Model:       effectiveModel(request.Profile.Model, result.Model),
		Batch:       batch,
		Attempt:     attempt,
		Latency:     latency,
		Usage:       usableUsage(result.Usage),
		Outcome:     outcome,
		ErrorKind:   kind,
	})
}

func (o *GenerationOrchestrator) observeAttempt(request GenerationRequest, result GenerationResult, batch, attempt int, latency time.Duration, outcome AttemptOutcome, kind ErrorKind) {
	o.observe(AttemptEvent{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Capability:  CapabilityGeneration,
		Provider:    effectiveProvider(request.Profile.Provider, result.Provider),
		Model:       effectiveModel(request.Profile.Model, result.Model),
		Batch:       batch,
		Attempt:     attempt,
		Latency:     latency,
		Usage:       usableUsage(result.Usage),
		Outcome:     outcome,
		ErrorKind:   kind,
	})
}

func effectiveProvider(requested, returned Provider) Provider {
	if returned != ProviderUnknown && validateProvider(returned) == nil {
		return returned
	}
	return requested
}

func effectiveModel(requested, returned string) string {
	if safeModel(returned) {
		return returned
	}
	if safeModel(requested) {
		return requested
	}
	return ""
}

func safeModel(model string) bool {
	return model != "" && validateIdentifier(model, maxIdentifierBytes) == nil && !containsCredentialPattern(model)
}

func acquireOrchestrationSlot(ctx context.Context, slots chan struct{}, capability Capability) error {
	if ctx == nil || slots == nil {
		return NewGatewayError(ErrorKindConfiguration, capability, nil)
	}
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return NormalizeError(capability, ctx.Err())
	}
}

func releaseOrchestrationSlot(slots chan struct{}) {
	if slots != nil {
		<-slots
	}
}

func (o *EmbeddingOrchestrator) observe(event AttemptEvent) {
	if o.config.Observer == nil {
		return
	}
	o.observerMu.Lock()
	defer o.observerMu.Unlock()
	o.config.Observer.ObserveAttempt(event)
}

func (o *GenerationOrchestrator) observe(event AttemptEvent) {
	if o.config.Observer == nil {
		return
	}
	o.observerMu.Lock()
	defer o.observerMu.Unlock()
	o.config.Observer.ObserveAttempt(event)
}

func validateEmbeddingOrchestrationRequest(request EmbeddingRequest) error {
	if err := validateRequestIdentity(request.ExecutionID, request.RequestID); err != nil {
		return err
	}
	if err := validateDeadline(request.Deadline); err != nil {
		return err
	}
	if err := request.Profile.Validate(); err != nil {
		return err
	}
	if len(request.Items) == 0 || len(request.Items) > maxEmbeddingBatchSize {
		return fmt.Errorf("embedding batch: %w", ErrBudget)
	}
	seen := make(map[string]struct{}, len(request.Items))
	totalBytes := 0
	for index, item := range request.Items {
		if err := item.validate(); err != nil {
			return fmt.Errorf("embedding item %d: %w", index, err)
		}
		if totalBytes > maxEmbeddingBatchBytes-len(item.Content) {
			return fmt.Errorf("embedding batch content: %w", ErrBudget)
		}
		totalBytes += len(item.Content)
		if _, exists := seen[item.ID]; exists {
			return fmt.Errorf("embedding batch: %w", ErrConfiguration)
		}
		seen[item.ID] = struct{}{}
	}
	return nil
}

func effectiveOrchestrationContext(ctx context.Context, deadline time.Time, capability Capability) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, NewGatewayError(ErrorKindConfiguration, capability, nil)
	}
	if err := contextError(ctx, deadline, capability); err != nil {
		return nil, nil, err
	}
	if parentDeadline, ok := ctx.Deadline(); ok && !deadline.Before(parentDeadline) {
		return ctx, func() {}, nil
	}
	derived, cancel := context.WithDeadline(ctx, deadline)
	return derived, cancel, nil
}

func estimateEmbeddingUsage(request EmbeddingRequest) Usage {
	usage := Usage{InputItems: len(request.Items), OutputItems: len(request.Items)}
	for _, item := range request.Items {
		usage.InputTokens += tokenEstimate(item.Content)
	}
	return usage
}

func estimateGenerationUsage(request GenerationRequest) Usage {
	usage := Usage{InputItems: 1, OutputItems: 1, InputTokens: tokenEstimate(request.Question), OutputTokens: (request.Profile.MaxOutputBytes + 3) / 4}
	for _, evidence := range request.Package.Evidence {
		usage.InputTokens += tokenEstimate(evidence.Content)
	}
	return usage
}

func usableUsage(usage Usage) Usage {
	if !validUsageNumbers(usage) {
		return Usage{}
	}
	return usage
}

func hasUsage(usage Usage) bool {
	return usage.InputItems > 0 || usage.OutputItems > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0
}

func gatewayErrorKind(err error) ErrorKind {
	var gatewayErr *GatewayError
	if errors.As(err, &gatewayErr) && validErrorKind(gatewayErr.Kind) {
		return gatewayErr.Kind
	}
	return ErrorKindInvalidResponse
}

func retryableGatewayError(err error) bool {
	return errors.Is(err, ErrRateLimit) || errors.Is(err, ErrUnavailable)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if ctx == nil {
		return context.Canceled
	}
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
