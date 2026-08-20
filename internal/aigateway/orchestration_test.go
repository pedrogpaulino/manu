package aigateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type orchestrationStep struct {
	result EmbeddingResult
	gen    GenerationResult
	err    error
}

type orchestrationObserver struct {
	mu     sync.Mutex
	events []AttemptEvent
}

func (o *orchestrationObserver) ObserveAttempt(event AttemptEvent) {
	o.mu.Lock()
	o.events = append(o.events, event)
	o.mu.Unlock()
}

func (o *orchestrationObserver) snapshot() []AttemptEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]AttemptEvent(nil), o.events...)
}

type orchestrationEmbedder struct {
	mu         sync.Mutex
	calls      int
	active     int
	maxActive  int
	wait       time.Duration
	steps      []orchestrationStep
	callNotify chan<- struct{}
}

func (e *orchestrationEmbedder) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	e.mu.Lock()
	index := e.calls
	e.calls++
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	if e.callNotify != nil {
		select {
		case e.callNotify <- struct{}{}:
		default:
		}
	}
	var step orchestrationStep
	if index < len(e.steps) {
		step = e.steps[index]
	}
	e.mu.Unlock()

	if e.wait > 0 {
		timer := time.NewTimer(e.wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			e.finishCall()
			return step.result, ctx.Err()
		case <-timer.C:
		}
	}
	e.finishCall()
	if step.err != nil {
		return step.result, step.err
	}
	return orchestrationEmbeddingResult(request), nil
}

func (e *orchestrationEmbedder) finishCall() {
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
}

func (e *orchestrationEmbedder) stats() (calls, maxActive int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls, e.maxActive
}

type orchestrationGenerator struct {
	mu         sync.Mutex
	calls      int
	active     int
	maxActive  int
	wait       time.Duration
	steps      []orchestrationStep
	callNotify chan<- struct{}
}

func (g *orchestrationGenerator) Generate(ctx context.Context, request GenerationRequest) (GenerationResult, error) {
	g.mu.Lock()
	index := g.calls
	g.calls++
	g.active++
	if g.active > g.maxActive {
		g.maxActive = g.active
	}
	if g.callNotify != nil {
		select {
		case g.callNotify <- struct{}{}:
		default:
		}
	}
	var step orchestrationStep
	if index < len(g.steps) {
		step = g.steps[index]
	}
	g.mu.Unlock()

	if g.wait > 0 {
		timer := time.NewTimer(g.wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			g.finishCall()
			return step.gen, ctx.Err()
		case <-timer.C:
		}
	}
	g.finishCall()
	if step.err != nil {
		return step.gen, step.err
	}
	return orchestrationGenerationResult(request), nil
}

func (g *orchestrationGenerator) finishCall() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}

func (g *orchestrationGenerator) stats() (calls, maxActive int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls, g.maxActive
}

func orchestrationEmbeddingResult(request EmbeddingRequest) EmbeddingResult {
	vectors := make([][]float64, len(request.Items))
	for index, item := range request.Items {
		digest := sha256.Sum256([]byte(item.ID))
		vector := make([]float64, request.Profile.Dimension)
		for dimension := range vector {
			vector[dimension] = float64(digest[dimension%len(digest)]) / 255
		}
		vectors[index] = vector
	}
	return EmbeddingResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    request.Profile.Provider,
		Model:       request.Profile.Model,
		Vectors:     vectors,
		Usage: Usage{
			InputItems:  len(request.Items),
			OutputItems: len(request.Items),
			InputTokens: orchestrationEmbeddingTokens(request),
		},
		Latency:     time.Nanosecond,
		Termination: TerminationCompleted,
	}
}

func orchestrationEmbeddingTokens(request EmbeddingRequest) int {
	tokens := 0
	for _, item := range request.Items {
		tokens += tokenEstimate(item.Content)
	}
	return tokens
}

func orchestrationGenerationResult(request GenerationRequest) GenerationResult {
	return GenerationResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    request.Profile.Provider,
		Model:       request.Profile.Model,
		Output: GenerationEnvelope{
			Version:       GenerationEnvelopeVersion,
			Text:          "fixture answer",
			PackageDigest: request.Package.Digest,
			EvidenceIDs:   []string{"evidence-1"},
		},
		Usage:       Usage{InputItems: 1, OutputItems: 1, InputTokens: 1, OutputTokens: 2},
		Latency:     time.Nanosecond,
		Termination: TerminationCompleted,
	}
}

func orchestrationRetryConfig() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   time.Nanosecond,
		MaxDelay:    time.Microsecond,
		Sleep: func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		},
	}
}

func TestEmbeddingOrchestratorBatchesPreserveOrderAndLogicalLatency(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	profile.MaxBatchSize = 2
	request := testEmbeddingRequest(profile)
	request.Items = []EmbeddingItem{
		{ID: "item-1", Content: "first"},
		{ID: "item-2", Content: "second"},
		{ID: "item-3", Content: "third"},
		{ID: "item-4", Content: "fourth"},
		{ID: "item-5", Content: "fifth"},
	}
	backend := &orchestrationEmbedder{wait: 2 * time.Millisecond}
	observer := &orchestrationObserver{}
	orchestrator, err := NewEmbeddingOrchestrator(backend, OrchestrationConfig{
		MaxConcurrency: 2,
		Observer:       observer,
		Retry:          orchestrationRetryConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := orchestrator.Embed(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Latency <= 0 {
		t.Fatalf("combined latency = %s, want positive logical latency", result.Latency)
	}
	if len(result.Vectors) != len(request.Items) {
		t.Fatalf("vector count = %d, want %d", len(result.Vectors), len(request.Items))
	}
	for index, item := range request.Items {
		digest := sha256.Sum256([]byte(item.ID))
		want := float64(digest[0]) / 255
		if result.Vectors[index][0] != want {
			t.Fatalf("vector %d belongs to %q, got first value %v want %v", index, item.ID, result.Vectors[index][0], want)
		}
	}
	events := observer.snapshot()
	if len(events) != 3 {
		t.Fatalf("attempt events = %d, want 3", len(events))
	}
	batches := make(map[int]bool)
	for _, event := range events {
		if event.Outcome != AttemptOutcomeSuccess || event.Attempt != 1 || event.Batch < 1 || event.Batch > 3 {
			t.Fatalf("unexpected batch event: %+v", event)
		}
		batches[event.Batch] = true
		if event.Latency <= 0 {
			t.Fatalf("event latency = %s, want positive", event.Latency)
		}
	}
	if !reflect.DeepEqual(batches, map[int]bool{1: true, 2: true, 3: true}) {
		t.Fatalf("batch identities = %#v", batches)
	}
}

func TestOrchestrationRetryMatrixAndSafeAttemptAudit(t *testing.T) {
	profile := testGenerationProfile("generate-v1")
	request := testGenerationRequest(profile)
	secret := "api_key=super-secret prompt body"
	kinds := []ErrorKind{
		ErrorKindAuthentication,
		ErrorKindConfiguration,
		ErrorKindCapability,
		ErrorKindBudget,
		ErrorKindTimeout,
		ErrorKindCancelled,
		ErrorKindContentBlocked,
		ErrorKindInvalidResponse,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			backend := &orchestrationGenerator{steps: []orchestrationStep{{err: NewGatewayError(kind, CapabilityGeneration, errors.New(secret))}}}
			orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{Retry: orchestrationRetryConfig(), MaxConcurrency: 1})
			if err != nil {
				t.Fatal(err)
			}
			_, err = orchestrator.Generate(context.Background(), request)
			if !errors.Is(err, &GatewayError{Kind: kind}) {
				t.Fatalf("error = %v, want %s", err, kind)
			}
			calls, _ := backend.stats()
			if calls != 1 {
				t.Fatalf("calls = %d, want one non-transient attempt", calls)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret: %q", err)
			}
		})
	}

	for _, kind := range []ErrorKind{ErrorKindRateLimit, ErrorKindUnavailable} {
		t.Run(string(kind)+"-retries", func(t *testing.T) {
			observer := &orchestrationObserver{}
			backend := &orchestrationGenerator{steps: []orchestrationStep{
				{gen: GenerationResult{Provider: ProviderOpenRouter, Model: "effective-model"}, err: NewGatewayError(kind, CapabilityGeneration, errors.New(secret))},
			}}
			orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{Retry: orchestrationRetryConfig(), MaxConcurrency: 1, Observer: observer})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := orchestrator.Generate(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			calls, _ := backend.stats()
			if calls != 2 {
				t.Fatalf("calls = %d, want transient retry", calls)
			}
			events := observer.snapshot()
			if len(events) != 2 || events[0].Provider != ProviderOpenRouter || events[0].Model != "effective-model" || events[0].Attempt != 1 || events[1].Attempt != 2 {
				t.Fatalf("unexpected retry audit: %+v", events)
			}
			encoded, err := json.Marshal(events)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "prompt body") {
				t.Fatalf("attempt audit leaked secret/content: %s", encoded)
			}
		})
	}

	backend := &orchestrationGenerator{steps: []orchestrationStep{
		{gen: GenerationResult{Usage: Usage{InputItems: 1}}, err: NewGatewayError(ErrorKindUnavailable, CapabilityGeneration, errors.New(secret))},
	}}
	orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{Retry: orchestrationRetryConfig(), MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = orchestrator.Generate(context.Background(), request)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ambiguous error = %v, want unavailable", err)
	}
	calls, _ := backend.stats()
	if calls != 1 {
		t.Fatalf("ambiguous calls = %d, want no retry", calls)
	}
}

func TestOrchestrationBudgetsAreFailClosedAndConcurrentSafe(t *testing.T) {
	profile := testGenerationProfile("generate-v1")
	request := testGenerationRequest(profile)
	backend := &orchestrationGenerator{}
	orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{
		Budget:         OrchestrationBudget{MaxRequests: 2},
		MaxConcurrency: 4,
		Retry:          orchestrationRetryConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	const calls = 12
	var wg sync.WaitGroup
	errorsCh := make(chan error, calls)
	for index := 0; index < calls; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, callErr := orchestrator.Generate(context.Background(), request)
			errorsCh <- callErr
		}()
	}
	wg.Wait()
	close(errorsCh)
	successes := 0
	budgetFailures := 0
	for callErr := range errorsCh {
		if callErr == nil {
			successes++
		} else if errors.Is(callErr, ErrBudget) {
			budgetFailures++
		} else {
			t.Fatalf("unexpected concurrent budget error: %v", callErr)
		}
	}
	backendCalls, _ := backend.stats()
	if successes != 2 || backendCalls != 2 || budgetFailures != calls-successes {
		t.Fatalf("successes=%d backend_calls=%d budget_failures=%d", successes, backendCalls, budgetFailures)
	}

	limitedProfile := profile
	limitedProfile.MaxOutputBytes = 64
	limitedRequest := request
	limitedRequest.Profile = limitedProfile
	limitedBackend := &orchestrationGenerator{}
	limited, err := NewGenerationOrchestrator(limitedBackend, OrchestrationConfig{
		Budget:         OrchestrationBudget{MaxOutputTokens: 1},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Generate(context.Background(), limitedRequest); !errors.Is(err, ErrBudget) {
		t.Fatalf("output-token budget error = %v, want budget", err)
	}
	limitedCalls, _ := limitedBackend.stats()
	if limitedCalls != 0 {
		t.Fatalf("provider calls after rejected estimate = %d, want zero", limitedCalls)
	}

	externalProfile := profile
	externalProfile.Provider = ProviderOpenAI
	externalRequest := request
	externalRequest.Profile = externalProfile
	externalBackend := &orchestrationGenerator{}
	external, err := NewGenerationOrchestrator(externalBackend, OrchestrationConfig{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := external.Generate(context.Background(), externalRequest); !errors.Is(err, ErrBudget) {
		t.Fatalf("external zero-budget error = %v, want budget", err)
	}
	externalCalls, _ := externalBackend.stats()
	if externalCalls != 0 {
		t.Fatalf("external provider called without positive budget: %d", externalCalls)
	}

	partialBackend := &orchestrationGenerator{}
	partial, err := NewGenerationOrchestrator(partialBackend, OrchestrationConfig{
		Budget:         OrchestrationBudget{MaxRequests: 10, MaxInputTokens: 10, MaxOutputTokens: 10},
		MaxConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := partial.Generate(context.Background(), externalRequest); !errors.Is(err, ErrBudget) {
		t.Fatalf("external partial-budget error = %v, want budget", err)
	}
	partialCalls, _ := partialBackend.stats()
	if partialCalls != 0 {
		t.Fatalf("external provider called with partial budget: %d", partialCalls)
	}
}

func TestOrchestrationMaxConcurrencyIsSharedAcrossCalls(t *testing.T) {
	profile := testEmbeddingProfile("embed-v1")
	profile.MaxBatchSize = 1
	backend := &orchestrationEmbedder{wait: 20 * time.Millisecond}
	orchestrator, err := NewEmbeddingOrchestrator(backend, OrchestrationConfig{MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	const calls = 8
	var wg sync.WaitGroup
	for index := 0; index < calls; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			request := testEmbeddingRequest(profile)
			request.ExecutionID = "execution-" + string(rune('a'+index))
			request.RequestID = "request-" + string(rune('a'+index))
			request.Items = []EmbeddingItem{{ID: "item-" + string(rune('a'+index)), Content: "content"}}
			if _, callErr := orchestrator.Embed(context.Background(), request); callErr != nil {
				t.Errorf("embed %d: %v", index, callErr)
			}
		}(index)
	}
	wg.Wait()
	backendCalls, maxActive := backend.stats()
	if backendCalls != calls || maxActive != 2 {
		t.Fatalf("embedding calls=%d max_active=%d, want %d/2", backendCalls, maxActive, calls)
	}

	generationProfile := testGenerationProfile("generate-v1")
	generationBackend := &orchestrationGenerator{wait: 20 * time.Millisecond}
	generation, err := NewGenerationOrchestrator(generationBackend, OrchestrationConfig{MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < calls; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			request := testGenerationRequest(generationProfile)
			request.ExecutionID = "generation-execution-" + string(rune('a'+index))
			request.RequestID = "generation-request-" + string(rune('a'+index))
			if _, callErr := generation.Generate(context.Background(), request); callErr != nil {
				t.Errorf("generate %d: %v", index, callErr)
			}
		}(index)
	}
	wg.Wait()
	generationCalls, generationMaxActive := generationBackend.stats()
	if generationCalls != calls || generationMaxActive != 2 {
		t.Fatalf("generation calls=%d max_active=%d, want %d/2", generationCalls, generationMaxActive, calls)
	}
}

func TestOrchestrationCancellationStopsBackoffAndDeadlineStopsRetry(t *testing.T) {
	profile := testGenerationProfile("generate-v1")
	request := testGenerationRequest(profile)
	firstCall := make(chan struct{}, 1)
	backend := &orchestrationGenerator{
		callNotify: firstCall,
		steps:      []orchestrationStep{{err: NewGatewayError(ErrorKindRateLimit, CapabilityGeneration, nil)}},
	}
	sleepStarted := make(chan struct{}, 1)
	orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{
		MaxConcurrency: 1,
		Retry: RetryPolicy{
			MaxAttempts: 3,
			BaseDelay:   time.Second,
			MaxDelay:    time.Second,
			Sleep: func(ctx context.Context, _ time.Duration) error {
				sleepStarted <- struct{}{}
				<-ctx.Done()
				return ctx.Err()
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		_, callErr := orchestrator.Generate(ctx, request)
		resultCh <- callErr
	}()
	select {
	case <-firstCall:
	case <-time.After(time.Second):
		t.Fatal("provider was not called")
	}
	select {
	case <-sleepStarted:
	case <-time.After(time.Second):
		t.Fatal("retry did not enter backoff")
	}
	cancel()
	if callErr := <-resultCh; !errors.Is(callErr, ErrCancelled) || !errors.Is(callErr, context.Canceled) {
		t.Fatalf("cancelled backoff error = %v, want cancelled", callErr)
	}
	calls, _ := backend.stats()
	if calls != 1 {
		t.Fatalf("calls after cancellation = %d, want one", calls)
	}

	deadlineBackend := &orchestrationGenerator{steps: []orchestrationStep{{err: NewGatewayError(ErrorKindUnavailable, CapabilityGeneration, nil)}}}
	deadlineRequest := request
	deadlineRequest.Deadline = time.Now().Add(20 * time.Millisecond)
	deadline, err := NewGenerationOrchestrator(deadlineBackend, OrchestrationConfig{
		MaxConcurrency: 1,
		Retry:          RetryPolicy{MaxAttempts: 3, BaseDelay: 100 * time.Millisecond, MaxDelay: 100 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := deadline.Generate(context.Background(), deadlineRequest); !errors.Is(err, ErrTimeout) {
		t.Fatalf("deadline backoff error = %v, want timeout", err)
	}
	deadlineCalls, _ := deadlineBackend.stats()
	if deadlineCalls != 1 {
		t.Fatalf("calls after deadline = %d, want one", deadlineCalls)
	}
}

func TestOrchestrationBackoffAndJitterAreInjectable(t *testing.T) {
	profile := testGenerationProfile("generate-v1")
	request := testGenerationRequest(profile)
	backend := &orchestrationGenerator{steps: []orchestrationStep{
		{err: NewGatewayError(ErrorKindRateLimit, CapabilityGeneration, nil)},
		{err: NewGatewayError(ErrorKindUnavailable, CapabilityGeneration, nil)},
	}}
	var mu sync.Mutex
	var backoffAttempts []int
	var jitterAttempts []int
	var slept []time.Duration
	orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{
		MaxConcurrency: 1,
		Retry: RetryPolicy{
			MaxAttempts: 3,
			BaseDelay:   time.Millisecond,
			MaxDelay:    5 * time.Millisecond,
			Backoff: func(attempt int) time.Duration {
				mu.Lock()
				backoffAttempts = append(backoffAttempts, attempt)
				mu.Unlock()
				return time.Duration(attempt) * time.Millisecond
			},
			Jitter: func(attempt int, delay time.Duration) time.Duration {
				mu.Lock()
				jitterAttempts = append(jitterAttempts, attempt)
				mu.Unlock()
				return delay + time.Microsecond
			},
			Sleep: func(_ context.Context, delay time.Duration) error {
				mu.Lock()
				slept = append(slept, delay)
				mu.Unlock()
				return nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := orchestrator.Generate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(backoffAttempts, []int{1, 2}) || !reflect.DeepEqual(jitterAttempts, []int{1, 2}) || !reflect.DeepEqual(slept, []time.Duration{time.Millisecond + time.Microsecond, 2*time.Millisecond + time.Microsecond}) {
		t.Fatalf("retry timing calls backoff=%v jitter=%v sleep=%v", backoffAttempts, jitterAttempts, slept)
	}
}

func TestOrchestrationEffectiveModelRejectsCredentialShapedAuditValue(t *testing.T) {
	profile := testGenerationProfile("generate-v1")
	request := testGenerationRequest(profile)
	observer := &orchestrationObserver{}
	backend := &orchestrationGenerator{steps: []orchestrationStep{{gen: GenerationResult{Provider: ProviderOpenRouter, Model: "api_key=leaked"}, err: NewGatewayError(ErrorKindUnavailable, CapabilityGeneration, nil)}}}
	orchestrator, err := NewGenerationOrchestrator(backend, OrchestrationConfig{MaxConcurrency: 1, Observer: observer, Retry: RetryPolicy{MaxAttempts: 1}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = orchestrator.Generate(context.Background(), request)
	events := observer.snapshot()
	if len(events) != 1 || events[0].Model != profile.Model || events[0].Provider != ProviderOpenRouter {
		t.Fatalf("unsafe effective audit fallback = %+v", events)
	}
	if strings.Contains(events[0].Model, "api_key") {
		t.Fatalf("credential-shaped model escaped into event: %+v", events[0])
	}
}
