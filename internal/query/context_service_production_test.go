package query

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestProductionContextServiceBuildsDeterministicPackageForIntentKinds(t *testing.T) {
	fixture := productionContextTestFixture(t, 1)
	reader := &productionContextTestReader{snapshot: fixture.snapshot}
	retriever := &productionContextTestRetriever{result: fixture.retrieval}
	service := productionContextTestService(t, reader, retriever, &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow,
	}})

	tests := []struct {
		name   string
		intent Intent
	}{
		{
			name:   "question",
			intent: Intent{Version: ContextVersion, Kind: IntentKindQuestion, Question: "where is symbol context?"},
		},
		{
			name:   "symbol",
			intent: Intent{Version: ContextVersion, Kind: IntentKindSymbol, Target: IntentTarget{Kind: IntentTargetSymbol, ID: "symbol-context-a"}},
		},
		{
			name:   "possible impact",
			intent: Intent{Version: ContextVersion, Kind: IntentKindPossibleImpact, Target: IntentTarget{Kind: IntentTargetSymbol, ID: "symbol-context-a"}},
		},
		{
			name:   "evidence inspection",
			intent: Intent{Version: ContextVersion, Kind: IntentKindEvidenceInspection, Target: IntentTarget{Kind: IntentTargetEvidence, ID: fixture.units[0].ID}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := productionContextTestRequest(fixture.scope, tt.intent, contextTestLimits())
			first, err := service.BuildContext(context.Background(), request)
			if err != nil {
				t.Fatalf("first BuildContext() error = %v", err)
			}
			second, err := service.BuildContext(context.Background(), request)
			if err != nil {
				t.Fatalf("second BuildContext() error = %v", err)
			}
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("BuildContext() is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
			}
			if err := first.Validate(); err != nil {
				t.Fatalf("ContextPackage.Validate() error = %v", err)
			}
			if first.Scope != fixture.scope || first.Revision != fixture.snapshot.Revision {
				t.Fatalf("package scope/revision = %#v/%q, want %#v/%q", first.Scope, first.Revision, fixture.scope, fixture.snapshot.Revision)
			}
			if len(first.Items) == 0 {
				t.Fatal("BuildContext() returned no selected items")
			}
			if len(reader.scopes) < 2 || reader.scopes[0] != fixture.scope || reader.scopes[1] != fixture.scope {
				t.Fatalf("snapshot reader scopes = %#v", reader.scopes)
			}
			if len(retriever.inputs) < 2 || retriever.inputs[len(retriever.inputs)-1].Scope != fixture.scope {
				t.Fatalf("retriever input scope = %#v", retriever.inputs)
			}
		})
	}
}

func TestProductionContextServiceContinuationDoesNotOverlapAndRejectsIncompatibleCursor(t *testing.T) {
	fixture := productionContextTestFixture(t, 2)
	reader := &productionContextTestReader{snapshot: fixture.snapshot}
	retriever := &productionContextTestRetriever{result: fixture.retrieval}
	service := productionContextTestService(t, reader, retriever, &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow,
	}})
	request := productionContextTestRequest(fixture.scope, Intent{
		Version:  ContextVersion,
		Kind:     IntentKindQuestion,
		Question: "context service continuation",
	}, ContextLimits{MaxTokens: 4_096, MaxItems: 1, MaxCharacters: 64 << 10, MaxBytes: 64 << 10})

	first, err := service.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatalf("first BuildContext() error = %v", err)
	}
	if !first.Truncated || first.Continuation == nil || len(first.Items) != 1 {
		t.Fatalf("first page = %#v", first)
	}

	secondRequest := request
	secondRequest.Continuation = first.Continuation
	second, err := service.BuildContext(context.Background(), secondRequest)
	if err != nil {
		t.Fatalf("second BuildContext() error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("continuation pages overlap or have wrong size: first=%#v second=%#v", first.Items, second.Items)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("second ContextPackage.Validate() error = %v", err)
	}

	incompatible := request
	incompatible.Intent.Question = "different continuation intent"
	incompatible.Continuation = first.Continuation
	if _, err := service.BuildContext(context.Background(), incompatible); !errors.Is(err, ErrInvalidContextContinuation) {
		t.Fatalf("incompatible continuation error = %v, want %v", err, ErrInvalidContextContinuation)
	}
}

func TestProductionContextServiceRejectsScopeMismatchCancellationAndInvalidConstructor(t *testing.T) {
	fixture := productionContextTestFixture(t, 1)
	codec := productionContextTestCodec(t)
	validPolicy := &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow,
	}}

	reader := &productionContextTestReader{snapshot: fixture.snapshot}
	retriever := &productionContextTestRetriever{result: fixture.retrieval}
	service, err := NewProductionContextService(reader, retriever, codec, ContextServiceConfig{TransferPolicy: validPolicy})
	if err != nil {
		t.Fatalf("NewProductionContextService() error = %v", err)
	}
	request := productionContextTestRequest(fixture.scope, Intent{
		Version:  ContextVersion,
		Kind:     IntentKindQuestion,
		Question: "scope and cancellation",
	}, ContextLimits{MaxTokens: 4_096, MaxItems: 8, MaxCharacters: 64 << 10, MaxBytes: 64 << 10})

	wrongSnapshot := fixture.snapshot.Clone()
	wrongSnapshot.Scope.OrganizationID = contextTestUUID(9)
	reader.snapshot = wrongSnapshot
	if _, err := service.BuildContext(context.Background(), request); !errors.Is(err, ErrContextSnapshotScopeMismatch) {
		t.Fatalf("scope mismatch error = %v, want %v", err, ErrContextSnapshotScopeMismatch)
	}
	reader.snapshot = fixture.snapshot

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.BuildContext(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled BuildContext() error = %v, want %v", err, context.Canceled)
	}

	tests := []struct {
		name      string
		reader    ContextSnapshotReader
		retriever Retriever
		codec     *ContextContinuationCodec
		config    ContextServiceConfig
		want      error
	}{
		{name: "nil reader", reader: nil, retriever: retriever, codec: codec, config: ContextServiceConfig{}, want: ErrContextServiceNotConfigured},
		{name: "nil retriever", reader: reader, retriever: nil, codec: codec, config: ContextServiceConfig{}, want: ErrContextServiceNotConfigured},
		{name: "nil codec", reader: reader, retriever: retriever, codec: nil, config: ContextServiceConfig{}, want: ErrContextServiceNotConfigured},
		{name: "invalid retrieval limit", reader: reader, retriever: retriever, codec: codec, config: ContextServiceConfig{RetrievalLimit: -1}, want: ErrInvalidContextServiceConfig},
		{name: "invalid utility", reader: reader, retriever: retriever, codec: codec, config: ContextServiceConfig{Utility: ContextUtilityConfiguration{Version: "unsupported"}}, want: ErrInvalidContextServiceConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewProductionContextService(tt.reader, tt.retriever, tt.codec, tt.config); !errors.Is(err, tt.want) {
				t.Fatalf("NewProductionContextService() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestProductionContextServiceAppliesPolicyAndReportsDegradations(t *testing.T) {
	fixture := productionContextTestFixture(t, 1)
	request := productionContextTestRequest(fixture.scope, Intent{
		Version:  ContextVersion,
		Kind:     IntentKindQuestion,
		Question: "policy and degraded retrieval",
	}, ContextLimits{MaxTokens: 4_096, MaxItems: 8, MaxCharacters: 64 << 10, MaxBytes: 64 << 10})

	deniedService := productionContextTestService(t, &productionContextTestReader{snapshot: fixture.snapshot}, &productionContextTestRetriever{result: fixture.retrieval}, &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionDeny,
	}})
	denied, err := deniedService.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatalf("denied BuildContext() error = %v", err)
	}
	if len(denied.Items) == 0 {
		t.Fatalf("denied package = %#v", denied)
	}
	var deniedEvidence bool
	for _, item := range denied.Items {
		if item.Kind == ContextItemEvidence && item.Evidence != nil {
			deniedEvidence = item.Evidence.ExternalTransfer == evidence.DecisionDeny && item.Evidence.Content == fixture.units[0].Content
		}
	}
	if !deniedEvidence {
		t.Fatalf("local package did not preserve denied transfer evidence: %#v", denied.Items)
	}
	if _, err := ProjectContextPackage(context.Background(), denied); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("gateway projection for denied transfer error = %v, want %v", err, ErrInvalidContextGatewayProjection)
	}

	redactedService := productionContextTestService(t, &productionContextTestReader{snapshot: fixture.snapshot}, &productionContextTestRetriever{result: fixture.retrieval}, &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionRedact,
	}})
	redacted, err := redactedService.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatalf("redacted BuildContext() error = %v", err)
	}
	if len(redacted.Items) == 0 {
		t.Fatalf("redacted package = %#v", redacted)
	}
	var redactedEvidence bool
	for _, item := range redacted.Items {
		if item.Kind == ContextItemEvidence && item.Evidence != nil {
			redactedEvidence = item.Evidence.ExternalTransfer == evidence.DecisionRedact && item.Evidence.Content == fixture.units[0].Content
		}
	}
	if !redactedEvidence {
		t.Fatalf("local package did not preserve independently redacted transfer evidence: %#v", redacted.Items)
	}
	if _, err := ProjectContextPackage(context.Background(), redacted); !errors.Is(err, ErrInvalidContextGatewayProjection) {
		t.Fatalf("gateway projection for redacted transfer error = %v, want %v", err, ErrInvalidContextGatewayProjection)
	}

	degradedRetrieval := fixture.retrieval
	degradedRetrieval.DegradationReasons = []string{
		"vector_profile_unavailable",
		string(ContextDegradationTextUnavailable),
		string(ContextDegradationRelationUnavailable),
		string(ContextDegradationExactUnavailable),
		"evidence_unavailable",
		"unknown_optional_signal",
	}
	validPolicy := &evidence.Policy{Installation: evidence.PolicyLayer{
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow,
	}}
	degradedService := productionContextTestService(t, &productionContextTestReader{snapshot: fixture.snapshot}, &productionContextTestRetriever{result: degradedRetrieval}, validPolicy)
	degraded, err := degradedService.BuildContext(context.Background(), request)
	if err != nil {
		t.Fatalf("degraded BuildContext() error = %v", err)
	}
	for _, code := range []ContextDegradationCode{
		ContextDegradationVectorUnavailable,
		ContextDegradationTextUnavailable,
		ContextDegradationRelationUnavailable,
		ContextDegradationExactUnavailable,
		ContextDegradationSupportIncomplete,
	} {
		if !productionContextTestHasDegradation(degraded, code) {
			t.Errorf("degraded package missing %q: %#v", code, degraded.Degradations)
		}
	}
}

type productionContextTestReader struct {
	snapshot ContextSnapshot
	scopes   []Scope
}

func (r *productionContextTestReader) ReadContextSnapshot(ctx context.Context, scope Scope) (ContextSnapshot, error) {
	if ctx == nil {
		return ContextSnapshot{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return ContextSnapshot{}, err
	}
	r.scopes = append(r.scopes, scope)
	return r.snapshot.Clone(), nil
}

type productionContextTestRetriever struct {
	result QueryRetrievalResult
	inputs []QueryRetrievalInput
}

func (r *productionContextTestRetriever) Retrieve(ctx context.Context, input QueryRetrievalInput) (QueryRetrievalResult, error) {
	if ctx == nil {
		return QueryRetrievalResult{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return QueryRetrievalResult{}, err
	}
	r.inputs = append(r.inputs, input)
	result := r.result
	result.Candidates = append([]PackageCandidate(nil), r.result.Candidates...)
	result.DegradationReasons = append([]string(nil), r.result.DegradationReasons...)
	return result, nil
}

func productionContextTestFixture(t *testing.T, count int) struct {
	scope     Scope
	snapshot  ContextSnapshot
	retrieval QueryRetrievalResult
	units     []evidence.EvidenceUnit
} {
	t.Helper()
	if count < 1 || count > 2 {
		t.Fatalf("unsupported production context fixture count %d", count)
	}
	scope := packageTestScope()
	artifactIDs := []string{"context-service-a.go", "context-service-b.go"}
	paths := []string{"src/context-service-a.go", "src/context-service-b.go"}
	contents := []string{"safe context service evidence A", "safe context service evidence B"}
	subjectIDs := []string{"symbol-context-a", "symbol-context-b"}
	units := make([]evidence.EvidenceUnit, 0, count)
	facts := make([]fact.CanonicalFact, 0, count)
	candidates := make([]PackageCandidate, 0, count)
	for index := 0; index < count; index++ {
		unit := packageTestUnit(scope, artifactIDs[index], paths[index], contents[index])
		units = append(units, unit)
		facts = append(facts, contextProjectionTestFact(scopeAsFact(scope), unit.ID, unit.Locator, fact.PredicateDefinition, subjectIDs[index], nil))
		candidates = append(candidates, contextProjectionTestCandidate(scope, unit, unit.ID, 0.9-float64(index)*0.1, index+1, false))
	}
	return struct {
		scope     Scope
		snapshot  ContextSnapshot
		retrieval QueryRetrievalResult
		units     []evidence.EvidenceUnit
	}{
		scope: scope,
		snapshot: ContextSnapshot{
			Scope:    scope,
			Revision: "revision-context-service",
			Facts:    facts,
		},
		retrieval: QueryRetrievalResult{Candidates: candidates},
		units:     units,
	}
}

func productionContextTestCodec(t *testing.T) *ContextContinuationCodec {
	t.Helper()
	codec, err := NewContextContinuationCodec([]byte("production-context-service-test-key-32-bytes"))
	if err != nil {
		t.Fatalf("NewContextContinuationCodec() error = %v", err)
	}
	return codec
}

func productionContextTestService(t *testing.T, reader ContextSnapshotReader, retriever Retriever, policy *evidence.Policy) *ProductionContextService {
	t.Helper()
	service, err := NewProductionContextService(reader, retriever, productionContextTestCodec(t), ContextServiceConfig{TransferPolicy: policy})
	if err != nil {
		t.Fatalf("NewProductionContextService() error = %v", err)
	}
	return service
}

func productionContextTestRequest(scope Scope, intent Intent, limits ContextLimits) ContextRequest {
	return ContextRequest{Version: ContextVersion, Scope: scope, Intent: intent, Limits: limits}
}

func productionContextTestHasDegradation(packageContext ContextPackage, code ContextDegradationCode) bool {
	for _, degradation := range packageContext.Degradations {
		if degradation.Code == code {
			return true
		}
	}
	return false
}
