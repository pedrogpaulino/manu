package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestVariantRunnerRoutesBuiltInsInNormalizedOrderAndKeepsResultsSeparate(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	original := cloneCaseSet(cases)
	var mu sync.Mutex
	calls := make([]string, 0, 3)
	var sharedTask EvaluationTask
	var sharedPolicy EvaluationPolicy
	var sharedRevisions [4]string
	var sharedContextSet bool
	executor := func(expected VariantKind) VariantExecutor {
		return VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
			if request.Variant.Kind != expected {
				t.Fatalf("executor for %q received %q", expected, request.Variant.Kind)
			}
			mu.Lock()
			if !sharedContextSet {
				sharedTask = request.Task
				sharedPolicy = request.Policy
				sharedRevisions = [4]string{request.CorpusID, request.CorpusRevision, request.SourceID, request.SourceRevision}
				sharedContextSet = true
			} else if request.Task != sharedTask || !reflect.DeepEqual(request.Policy, sharedPolicy) ||
				[4]string{request.CorpusID, request.CorpusRevision, request.SourceID, request.SourceRevision} != sharedRevisions {
				mu.Unlock()
				t.Fatalf("variant context changed: %#v", request)
			}
			calls = append(calls, request.Variant.ID)
			mu.Unlock()
			// Mutating this request must not affect another variant or the case.
			request.Variant.ToolIDs[0] = "mutated-only-in-this-call"
			request.Configuration.Settings["local-only"] = "not-shared"
			return VariantExecutionResult{
				Status:       VariantStatusCompleted,
				OutputDigest: variantTestDigest(string(expected)),
				EvidenceIDs:  []string{"eval-ctx-manifest"},
				ClaimIDs:     []string{"eval-ctx-claim"},
				Citations:    []VariantCitation{{ID: "citation-" + string(expected), ClaimID: "eval-ctx-claim", EvidenceID: "eval-ctx-manifest"}},
			}, nil
		})
	}
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{VariantID: "direct", Kind: VariantDirectSource, Executor: executor(VariantDirectSource)},
		VariantExecutorRegistration{VariantID: "text", Kind: VariantTextRetrieval, Executor: executor(VariantTextRetrieval)},
		VariantExecutorRegistration{VariantID: "manu", Kind: VariantManuContext, Executor: executor(VariantManuContext)},
	)
	if err != nil {
		t.Fatalf("NewVariantExecutorRegistry() error = %v", err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatalf("NewVariantRunner() error = %v", err)
	}
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(cases, original) {
		t.Fatal("variant run mutated input case set")
	}
	if len(report.Cases) != 1 || len(report.Cases[0].Executions) != 3 {
		t.Fatalf("report executions = %#v", report)
	}
	if got, want := calls, []string{"direct", "manu", "text"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized call order = %v, want %v", got, want)
	}
	executions := report.Cases[0].Executions
	for index, execution := range executions {
		if execution.Outcome != VariantOutcomeCompleted || execution.Result.Status != VariantStatusCompleted {
			t.Fatalf("execution %d = %#v", index, execution)
		}
		if execution.Result.OutputDigest == "" || len(execution.Result.EvidenceIDs) != 1 || len(execution.Result.Citations) != 1 {
			t.Fatalf("content-free result %d = %#v", index, execution.Result)
		}
	}
	if executions[0].Differences != nil {
		t.Fatalf("direct-source baseline differences = %#v", executions[0].Differences)
	}
	if executions[1].Differences == nil || executions[2].Differences == nil {
		t.Fatalf("comparison differences = %#v", executions)
	}
	if !executions[1].Differences.ConfigurationIDChanged || !reflect.DeepEqual(executions[1].Differences.ToolIDsAdded, []string{"manu-context"}) || !reflect.DeepEqual(executions[1].Differences.ToolIDsRemoved, []string{"filesystem-search"}) {
		t.Fatalf("manu difference = %#v", executions[1].Differences)
	}
	if !executions[2].Differences.ConfigurationIDChanged || !reflect.DeepEqual(executions[2].Differences.ConfigurationKeysAdded, []string{"retrieval"}) {
		t.Fatalf("text difference = %#v", executions[2].Differences)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"mutated-only-in-this-call", "not-shared", "public class", "secret="} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestVariantRunnerUsesDefaultBuiltInExecutorsAndExternalIsolatedByID(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	item := cloneCase(cases.Cases[0])
	item.Variants = append(item.Variants, EvaluationVariant{
		ID: "external-one", Kind: VariantExternalContext, ToolIDs: []string{"filesystem-search"},
		ConfigurationID: "direct-read-only", Capabilities: []string{"external.read"}, Limitations: []string{"comparador opcional"},
	})
	item.Variants = append(item.Variants, EvaluationVariant{
		ID: "external-two", Kind: VariantExternalContext, ToolIDs: []string{"filesystem-search"},
		ConfigurationID: "direct-read-only", Capabilities: []string{"external.read"}, Limitations: []string{"comparador opcional"},
	})
	cases = CaseSet{Version: Version, Cases: []EvaluationCase{item}}
	called := make(map[VariantKind]int)
	var mu sync.Mutex
	makeExecutor := func(kind VariantKind) VariantExecutor {
		return VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
			mu.Lock()
			called[request.Variant.Kind]++
			mu.Unlock()
			return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest(string(kind))}, nil
		})
	}
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: makeExecutor(VariantDirectSource)},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: makeExecutor(VariantTextRetrieval)},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: makeExecutor(VariantManuContext)},
		VariantExecutorRegistration{VariantID: "external-two", Kind: VariantExternalContext, Executor: makeExecutor(VariantExternalContext)},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases[0].Executions) != 5 {
		t.Fatalf("execution count = %d", len(report.Cases[0].Executions))
	}
	var unavailable, exact VariantExecutionRecord
	for _, execution := range report.Cases[0].Executions {
		if execution.VariantID == "external-one" {
			unavailable = execution
		}
		if execution.VariantID == "external-two" {
			exact = execution
		}
	}
	if unavailable.Outcome != VariantOutcomeUnavailable || unavailable.ErrorCode != "executor_unavailable" || unavailable.Result.Status != VariantStatusUnavailable {
		t.Fatalf("unavailable external result = %#v", unavailable)
	}
	if exact.Outcome != VariantOutcomeCompleted || exact.VariantKind != VariantExternalContext {
		t.Fatalf("exact external result = %#v", exact)
	}
	if called[VariantDirectSource] != 1 || called[VariantTextRetrieval] != 1 || called[VariantManuContext] != 1 || called[VariantExternalContext] != 1 {
		t.Fatalf("executor calls = %#v", called)
	}
}

func TestVariantRegistryRequiresBuiltInsAndRejectsTypedNil(t *testing.T) {
	if _, err := NewVariantExecutorRegistry(); !errors.Is(err, ErrInvalidVariantRegistry) {
		t.Fatalf("empty registry error = %v", err)
	}
	called := 0
	counting := VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
		called++
		return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("unexpected")}, nil
	})
	if _, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: counting},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: counting},
	); !errors.Is(err, ErrInvalidVariantRegistry) {
		t.Fatalf("registry missing builtin error = %v", err)
	}
	if called != 0 {
		t.Fatalf("missing builtin executed an executor: %d calls", called)
	}
	var nilExecutor *variantExecutorStruct
	_, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: nilExecutor},
	)
	if !errors.Is(err, ErrInvalidVariantRegistry) {
		t.Fatalf("typed nil error = %v", err)
	}
	valid := VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
		return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("ok")}, nil
	})
	_, err = NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: valid},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: valid},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: valid},
		VariantExecutorRegistration{VariantID: "external", Kind: VariantExternalContext, Executor: valid},
		VariantExecutorRegistration{VariantID: "external", Kind: VariantExternalContext, Executor: valid},
	)
	if !errors.Is(err, ErrDuplicateVariantExecutor) {
		t.Fatalf("duplicate external error = %v", err)
	}
}

func TestVariantRunnerSanitizesExecutorErrorsAndInvalidResults(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
			return VariantExecutionResult{}, errors.New("executor secret=do-not-leak")
		})},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
			return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: "not-a-digest"}, nil
		})},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
			return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("manu")}, nil
		})},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "do-not-leak") || strings.Contains(string(encoded), "executor secret") {
		t.Fatalf("executor error leaked: %s", encoded)
	}
	byID := make(map[string]VariantExecutionRecord, len(report.Cases[0].Executions))
	for _, execution := range report.Cases[0].Executions {
		byID[execution.VariantID] = execution
	}
	if byID["direct"].ErrorCode != "executor_failed" || byID["direct"].Result.Status != VariantStatusFailed {
		t.Fatalf("executor failure = %#v", byID["direct"])
	}
	if byID["text"].ErrorCode != "invalid_executor_result" || byID["text"].Result.Status != VariantStatusFailed {
		t.Fatalf("invalid result = %#v", byID["text"])
	}
	if byID["manu"].Outcome != VariantOutcomeCompleted {
		t.Fatalf("healthy result = %#v", byID["manu"])
	}
}

func TestVariantRunnerPropagatesCancellationAndDoesNotRunNextVariant(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	called := 0
	canceling := VariantExecutorFunc(func(ctx context.Context, _ VariantExecutionRequest) (VariantExecutionResult, error) {
		called++
		return VariantExecutionResult{}, context.Canceled
	})
	never := VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
		called++
		return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("never")}, nil
	})
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: canceling},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: never},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: never},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), cases)
	if !errors.Is(err, context.Canceled) || called != 1 {
		t.Fatalf("cancellation = %v, calls = %d", err, called)
	}

	canceled := func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := runner.Run(ctx, cases)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pre-cancel error = %v", err)
		}
	}
	canceled()
}

func TestVariantExecutionRequestIsDefensiveAndConfigurationDigestDeterministic(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	item := cases.Cases[0]
	variant := item.Variants[0]
	request, err := buildVariantRequest(item, variant)
	if err != nil {
		t.Fatal(err)
	}
	clone := request.Clone()
	clone.Case.Tools[0].Capabilities[0] = "changed"
	clone.Policy.Permissions[0] = "changed"
	clone.Configuration.Settings["new"] = "value"
	clone.Variant.ToolIDs[0] = "changed"
	if err := request.Validate(); err != nil {
		t.Fatalf("original request became invalid: %v", err)
	}
	if reflect.DeepEqual(request, clone) {
		t.Fatal("Clone() did not detach request")
	}
	configuration := EvaluationConfiguration{ID: "cfg", Version: "v1", Settings: map[string]string{"b": "2", "a": "1"}}
	first, err := ConfigurationDigest(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configuration.Settings = map[string]string{"a": "1", "b": "2"}
	second, err := configuration.Digest()
	if err != nil || first != second || !isVariantSHA256(first) {
		t.Fatalf("configuration digest = %q/%q/%v", first, second, err)
	}
}

func TestVariantRunnerRejectsLegacyOrMalformedCaseWithoutExecuting(t *testing.T) {
	legacy := loadEvaluationFixture(t)
	valid := VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
		return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("valid")}, nil
	})
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: valid},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: valid},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: valid},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), legacy); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("legacy run error = %v", err)
	}
	current := loadCurrentEvaluationFixture(t)
	current.Cases[0].Variants[0].ConfigurationID = "missing"
	if _, err := runner.Run(context.Background(), current); err == nil {
		t.Fatal("malformed reference unexpectedly executed")
	}
}

func TestVariantRunnerReportIsDeterministicAfterNormalization(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	reversed := cloneCaseSet(cases)
	for index := range reversed.Cases {
		variants := reversed.Cases[index].Variants
		for left, right := 0, len(variants)-1; left < right; left, right = left+1, right-1 {
			variants[left], variants[right] = variants[right], variants[left]
		}
	}
	for left, right := 0, len(reversed.Cases)-1; left < right; left, right = left+1, right-1 {
		reversed.Cases[left], reversed.Cases[right] = reversed.Cases[right], reversed.Cases[left]
	}
	executor := VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
		return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest(request.Variant.ID)}, nil
	})
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: executor},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: executor},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: executor},
	)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(context.Background(), reversed)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("normalized reports differ:\n%s\n%s", firstJSON, secondJSON)
	}
}

type variantExecutorStruct struct{}

func (*variantExecutorStruct) Execute(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
	return VariantExecutionResult{}, nil
}

func variantTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
