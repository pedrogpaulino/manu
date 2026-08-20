package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/identity"
)

type orchestratorStore struct {
	mu       sync.Mutex
	started  Execution
	finished QueryOutcome
}

func (s *orchestratorStore) Start(_ context.Context, organization string, input ExecutionInput) (Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	started := Execution{
		ID:             testUUID(70),
		OrganizationID: organization,
		SourceID:       input.SourceID,
		SnapshotID:     input.SnapshotID,
		State:          ExecutionStateRunning,
		QuestionDigest: input.QuestionDigest,
		CreatedAt:      now,
		StartedAt:      &now,
	}
	s.started = started
	return started, nil
}

type staticActiveScopeResolver struct {
	scope Scope
}

func (r staticActiveScopeResolver) ResolveActiveScope(context.Context, string, string) (Scope, error) {
	return r.scope, nil
}

func (s *orchestratorStore) Finish(_ context.Context, organization string, outcome QueryOutcome) (Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = outcome
	finished := Execution{
		ID:             outcome.ExecutionID,
		OrganizationID: organization,
		State:          outcome.State,
		QuestionDigest: outcome.QuestionDigest,
		PackageDigest:  outcome.PackageDigest,
		DiagnosticCode: outcome.DiagnosticCode,
		CreatedAt:      s.started.CreatedAt,
		StartedAt:      s.started.StartedAt,
		FinishedAt:     &outcome.FinishedAt,
	}
	if outcome.HasResponse {
		encoded, err := json.Marshal(outcome.Response)
		if err != nil {
			return Execution{}, err
		}
		finished.Response = encoded
	}
	return finished, nil
}

func (s *orchestratorStore) Get(_ context.Context, organization, _ string) (Execution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := s.started
	result.OrganizationID = organization
	return result, nil
}

type staticRetriever struct {
	result QueryRetrievalResult
}

func (r staticRetriever) Retrieve(context.Context, QueryRetrievalInput) (QueryRetrievalResult, error) {
	return r.result, nil
}

type countingGenerator struct {
	mu     sync.Mutex
	calls  int
	result aigateway.GenerationResult
}

func (g *countingGenerator) Generate(_ context.Context, request aigateway.GenerationRequest) (aigateway.GenerationResult, error) {
	g.mu.Lock()
	g.calls++
	g.mu.Unlock()
	result := g.result
	result.ExecutionID = request.ExecutionID
	result.RequestID = request.RequestID
	result.Provider = request.Profile.Provider
	result.Model = request.Profile.Model
	result.Output.PackageDigest = request.Package.Digest
	return result, nil
}

func TestQueryOrchestratorAbstainsBeforeGeneratorAndPersistsTerminalResult(t *testing.T) {
	scope := packageTestScope()
	scope.OrganizationID = identity.CanonicalUUID("organization", "local")
	unit := packageTestUnit(scope, "a71", "s.go", "safe evidence")
	store := &orchestratorStore{}
	generator := &countingGenerator{}
	question := "which flow is observed?"
	service, err := NewQueryOrchestrator(store, staticRetriever{result: QueryRetrievalResult{
		Candidates: []PackageCandidate{{
			Fusion: packageTestFusionCandidate(scope, unit, 1, 1),
			Unit:   unit,
		}},
		Support: SupportAssessment{Kind: KnowledgeQuestionObservedExecution, Level: EvidenceSupportInsufficient},
	}}, generator, OrchestratorConfig{
		GenerationProfile: aigateway.GenerationProfile{
			Provider:       aigateway.ProviderSimulated,
			Model:          "simulated-model",
			Version:        aigateway.GenerationProfileVersion,
			Protocol:       aigateway.ProtocolResponses,
			MaxOutputBytes: 1024,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(question))
	execution, err := service.Create(context.Background(), "local", ExecutionInput{
		Question:       question,
		QuestionKind:   KnowledgeQuestionObservedExecution,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		QuestionDigest: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if execution.State != ExecutionStateAbstained || execution.PackageDigest == "" || len(execution.Response) == 0 {
		t.Fatalf("abstention execution = %#v", execution)
	}
	var abstainedResponse Response
	if err := json.Unmarshal(execution.Response, &abstainedResponse); err != nil {
		t.Fatal(err)
	}
	if len(abstainedResponse.Claims) != 1 || abstainedResponse.Claims[0].ID == "" {
		t.Fatalf("abstention claim identity = %#v", abstainedResponse.Claims)
	}
	generator.mu.Lock()
	calls := generator.calls
	generator.mu.Unlock()
	if calls != 0 {
		t.Fatalf("generator calls = %d, want zero for pre-provider abstention", calls)
	}
	store.mu.Lock()
	finished := store.finished
	store.mu.Unlock()
	if finished.State != ExecutionStateAbstained || !finished.HasResponse || finished.FinishedAt.IsZero() {
		t.Fatalf("persisted outcome = %#v", finished)
	}
}

func TestQueryOrchestratorRejectsMissingTypedKindWithoutStartingRun(t *testing.T) {
	store := &orchestratorStore{}
	service, err := NewQueryOrchestrator(store, staticRetriever{}, nil, OrchestratorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	question := "inventory"
	digest := sha256.Sum256([]byte(question))
	_, err = service.Create(context.Background(), "local", ExecutionInput{
		Question:       question,
		QuestionDigest: hex.EncodeToString(digest[:]),
	})
	if !errors.Is(err, ErrInvalidAbstentionInput) {
		t.Fatalf("Create() error = %v, want invalid typed kind", err)
	}
	store.mu.Lock()
	started := store.started
	store.mu.Unlock()
	if started.ID != "" {
		t.Fatalf("invalid request started a run: %#v", started)
	}
}

func TestQueryOrchestratorResolvesActiveScopeWhenOmitted(t *testing.T) {
	scope := packageTestScope()
	scope.OrganizationID = identity.CanonicalUUID("organization", "local")
	unit := packageTestUnit(scope, "a73", "s.go", "active snapshot evidence")
	store := &orchestratorStore{}
	service, err := NewQueryOrchestrator(store, staticRetriever{result: QueryRetrievalResult{
		Candidates: []PackageCandidate{{Fusion: packageTestFusionCandidate(scope, unit, 1, 1), Unit: unit}},
		Support:    SupportAssessment{Kind: KnowledgeQuestionInventory, Level: EvidenceSupportInsufficient},
	}}, nil, OrchestratorConfig{ActiveScope: staticActiveScopeResolver{scope: scope}})
	if err != nil {
		t.Fatal(err)
	}
	question := "inventory"
	digest := sha256.Sum256([]byte(question))
	if _, err := service.Create(context.Background(), "local", ExecutionInput{
		Question: question, QuestionKind: KnowledgeQuestionInventory,
		QuestionDigest: hex.EncodeToString(digest[:]),
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	store.mu.Lock()
	started := store.started
	store.mu.Unlock()
	if started.SourceID != scope.SourceID || started.SnapshotID != scope.SnapshotID {
		t.Fatalf("resolved scope was not persisted: %#v", started)
	}
}

func TestQueryOrchestratorGeneratesAndValidatesCitations(t *testing.T) {
	scope := packageTestScope()
	scope.OrganizationID = identity.CanonicalUUID("organization", "local")
	unit := packageTestUnit(scope, "a72", "s.go", "the service calls the repository")
	question := "what does the service call?"
	digest := sha256.Sum256([]byte(question))
	store := &orchestratorStore{}
	generator := &countingGenerator{result: aigateway.GenerationResult{
		Usage:       aigateway.Usage{InputItems: 1, OutputItems: 1, InputTokens: 5, OutputTokens: 8},
		Latency:     time.Millisecond,
		Termination: aigateway.TerminationCompleted,
		Output: aigateway.GenerationEnvelope{
			Version:     aigateway.GenerationEnvelopeVersion,
			Text:        "The service calls the repository.",
			EvidenceIDs: []string{unit.ID},
		},
	}}
	service, err := NewQueryOrchestrator(store, staticRetriever{result: QueryRetrievalResult{
		Candidates: []PackageCandidate{{Fusion: packageTestFusionCandidate(scope, unit, 1, 1), Unit: unit}},
		Support:    SupportAssessment{Kind: KnowledgeQuestionPossibleFlow, Level: EvidenceSupportSufficient},
	}}, generator, OrchestratorConfig{
		GenerationProfile: aigateway.GenerationProfile{
			Provider:       aigateway.ProviderSimulated,
			Model:          "simulated-model",
			Version:        aigateway.GenerationProfileVersion,
			Protocol:       aigateway.ProtocolResponses,
			MaxOutputBytes: 1024,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := service.Create(context.Background(), "local", ExecutionInput{
		Question:       question,
		QuestionKind:   KnowledgeQuestionPossibleFlow,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		QuestionDigest: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if execution.State != ExecutionStateCompleted {
		t.Fatalf("execution state = %s, want completed", execution.State)
	}
	var response Response
	if err := json.Unmarshal(execution.Response, &response); err != nil {
		t.Fatal(err)
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("persisted response is invalid: %v", err)
	}
	if len(response.Citations) != 1 || response.Citations[0].EvidenceID != unit.ID {
		t.Fatalf("response citations = %#v", response.Citations)
	}
	if response.Claims[0].ID == "" {
		t.Fatal("generated claim has no stable identifier")
	}
}
