package evaluation

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestVariantMetricsKeepMeasuredAndEstimatedTokensSeparate(t *testing.T) {
	metrics := validVariantMetricsForTest()
	result := VariantExecutionResult{
		Status:       VariantStatusCompleted,
		OutputDigest: variantTestDigest("metrics-separated"),
		Metrics:      &metrics,
	}
	normalized, err := result.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Metrics == nil || normalized.Metrics.MeasuredTokens == nil || normalized.Metrics.EstimatedTokens == nil {
		t.Fatalf("normalized token groups = %#v", normalized.Metrics)
	}
	if normalized.Metrics.MeasuredTokens.InputTokens == nil || normalized.Metrics.MeasuredTokens.OutputTokens == nil ||
		*normalized.Metrics.MeasuredTokens.InputTokens != 7 || *normalized.Metrics.MeasuredTokens.OutputTokens != 11 {
		t.Fatalf("measured tokens = %#v", normalized.Metrics.MeasuredTokens)
	}
	if normalized.Metrics.EstimatedTokens.InputTokens == nil || normalized.Metrics.EstimatedTokens.OutputTokens == nil ||
		*normalized.Metrics.EstimatedTokens.InputTokens != 13 || *normalized.Metrics.EstimatedTokens.OutputTokens != 17 {
		t.Fatalf("estimated tokens = %#v", normalized.Metrics.EstimatedTokens)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, field := range []string{"\"measured_tokens\"", "\"estimated_tokens\"", "\"input_tokens\":7", "\"input_tokens\":13"} {
		if !strings.Contains(jsonText, field) {
			t.Fatalf("serialized metrics missing %s: %s", field, encoded)
		}
	}
	if strings.Contains(jsonText, "total_tokens") || strings.Contains(jsonText, "combined_tokens") {
		t.Fatalf("serialized metrics introduced an implicit token aggregate: %s", encoded)
	}
}

func TestVariantMetricsAllowOnlyObservedTokenSide(t *testing.T) {
	tests := []struct {
		name      string
		measured  *VariantMeasuredTokens
		estimated *VariantEstimatedTokens
		present   string
		absent    string
		valid     bool
	}{
		{
			name: "measured input only",
			measured: &VariantMeasuredTokens{
				InputTokens: metricInt64Pointer(4), SourceID: "provider-usage", SourceVersion: "v1",
			},
			present: `"input_tokens":4`, absent: "output_tokens", valid: true,
		},
		{
			name: "estimated output only",
			estimated: &VariantEstimatedTokens{
				OutputTokens: metricInt64Pointer(6), EstimatorID: "token-estimator", EstimatorVersion: "v1",
			},
			present: `"output_tokens":6`, absent: "input_tokens", valid: true,
		},
		{
			name:     "empty measured group",
			measured: &VariantMeasuredTokens{SourceID: "provider-usage", SourceVersion: "v1"},
			absent:   "input_tokens", valid: false,
		},
		{
			name:      "empty estimated group",
			estimated: &VariantEstimatedTokens{EstimatorID: "token-estimator", EstimatorVersion: "v1"},
			absent:    "output_tokens", valid: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := VariantMetrics{ObserverID: "observer", ObserverVersion: "v1", MeasuredTokens: test.measured, EstimatedTokens: test.estimated}
			if test.measured != nil && test.estimated != nil {
				t.Fatal("test case must contain one token group")
			}
			if !test.valid {
				if err := metrics.Validate(); err == nil {
					t.Fatal("empty token group unexpectedly validated")
				}
				return
			}
			if err := metrics.Validate(); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(metrics)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), test.present) || strings.Contains(string(encoded), `"`+test.absent+`"`) {
				t.Fatalf("partial token serialization = %s", encoded)
			}
		})
	}
}

func TestVariantMetricsDistinguishUnavailableFromObservedZero(t *testing.T) {
	zero := int64(0)
	metrics := VariantMetrics{
		ObserverID:      "observer",
		ObserverVersion: "v1",
		ModelCalls:      &zero,
	}
	result := VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("zero"), Metrics: &metrics}
	normalized, err := result.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	if !strings.Contains(jsonText, `"model_calls":0`) {
		t.Fatalf("observed zero was omitted: %s", encoded)
	}
	for _, absent := range []string{"measured_tokens", "estimated_tokens", "tool_calls", "files_read", "bytes_read", "duration", "estimated_cost", "actual_cost"} {
		if strings.Contains(jsonText, `"`+absent+`"`) {
			t.Fatalf("unavailable metric %q was fabricated: %s", absent, encoded)
		}
	}
}

func TestVariantMetricsPreserveIndependentCountersDurationAndCosts(t *testing.T) {
	metrics := validVariantMetricsForTest()
	normalized, err := metrics.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if normalized.ModelCalls == nil || *normalized.ModelCalls != 2 || normalized.ToolCalls == nil || *normalized.ToolCalls != 3 ||
		normalized.FilesRead == nil || *normalized.FilesRead != 5 || normalized.BytesRead == nil || *normalized.BytesRead != 8 {
		t.Fatalf("independent counters = %#v", normalized)
	}
	if normalized.Duration == nil || normalized.Duration.Value != 21 || normalized.Duration.Unit != VariantDurationMilliseconds {
		t.Fatalf("duration = %#v", normalized.Duration)
	}
	if normalized.EstimatedCost == nil || normalized.EstimatedCost.USD != 0.25 || normalized.ActualCost == nil || normalized.ActualCost.USD != 0.5 {
		t.Fatalf("costs = %#v", normalized)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"model_calls":2`, `"tool_calls":3`, `"files_read":5`, `"bytes_read":8`, `"value":21`, `"unit":"milliseconds"`, `"estimated_cost":{"usd":0.25`, `"actual_cost":{"usd":0.5`} {
		if !strings.Contains(string(encoded), field) {
			t.Fatalf("serialized metric missing %s: %s", field, encoded)
		}
	}
}

func TestVariantMetricsRejectInvalidValuesAndMissingProvenance(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*VariantMetrics)
	}{
		{name: "negative measured input", mutate: func(metrics *VariantMetrics) { *metrics.MeasuredTokens.InputTokens = -1 }},
		{name: "negative estimated output", mutate: func(metrics *VariantMetrics) { *metrics.EstimatedTokens.OutputTokens = -1 }},
		{name: "negative model calls", mutate: func(metrics *VariantMetrics) { *metrics.ModelCalls = -1 }},
		{name: "negative tool calls", mutate: func(metrics *VariantMetrics) { *metrics.ToolCalls = -1 }},
		{name: "negative files", mutate: func(metrics *VariantMetrics) { *metrics.FilesRead = -1 }},
		{name: "negative bytes", mutate: func(metrics *VariantMetrics) { *metrics.BytesRead = -1 }},
		{name: "negative duration", mutate: func(metrics *VariantMetrics) { metrics.Duration.Value = -1 }},
		{name: "unknown duration unit", mutate: func(metrics *VariantMetrics) { metrics.Duration.Unit = "ticks" }},
		{name: "negative estimated cost", mutate: func(metrics *VariantMetrics) { metrics.EstimatedCost.USD = -1 }},
		{name: "nan actual cost", mutate: func(metrics *VariantMetrics) { metrics.ActualCost.USD = math.NaN() }},
		{name: "infinite estimated cost", mutate: func(metrics *VariantMetrics) { metrics.EstimatedCost.USD = math.Inf(1) }},
		{name: "missing observer id", mutate: func(metrics *VariantMetrics) { metrics.ObserverID = "" }},
		{name: "missing observer version", mutate: func(metrics *VariantMetrics) { metrics.ObserverVersion = "" }},
		{name: "missing measured source id", mutate: func(metrics *VariantMetrics) { metrics.MeasuredTokens.SourceID = "" }},
		{name: "missing measured source version", mutate: func(metrics *VariantMetrics) { metrics.MeasuredTokens.SourceVersion = "" }},
		{name: "missing estimated estimator id", mutate: func(metrics *VariantMetrics) { metrics.EstimatedTokens.EstimatorID = "" }},
		{name: "missing estimated estimator version", mutate: func(metrics *VariantMetrics) { metrics.EstimatedTokens.EstimatorVersion = "" }},
		{name: "empty metrics", mutate: func(metrics *VariantMetrics) {
			*metrics = VariantMetrics{ObserverID: "observer", ObserverVersion: "v1"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metrics := validVariantMetricsForTest()
			test.mutate(&metrics)
			if err := metrics.Validate(); err == nil {
				t.Fatal("invalid metrics unexpectedly validated")
			}
		})
	}
}

func TestVariantMetricsNormalizeDeepCopiesAndRunnerIsolatesResults(t *testing.T) {
	metrics := validVariantMetricsForTest()
	clone := metrics.Clone()
	*metrics.MeasuredTokens.InputTokens = 100
	*clone.ModelCalls = 101
	if *clone.MeasuredTokens.InputTokens == 100 || *metrics.ModelCalls == 101 {
		t.Fatal("Clone() retained mutable metric pointers")
	}
	metrics = validVariantMetricsForTest()
	resultClone := (VariantExecutionResult{Metrics: &metrics, EvidenceIDs: []string{"evidence"}}).Clone()
	if resultClone.Metrics == &metrics || resultClone.Metrics.MeasuredTokens == metrics.MeasuredTokens {
		t.Fatal("VariantExecutionResult.Clone() retained metric pointers")
	}
	normalized, err := metrics.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	*metrics.MeasuredTokens.InputTokens = 101
	*metrics.ModelCalls = 102
	if *normalized.MeasuredTokens.InputTokens == 101 || *normalized.ModelCalls == 102 {
		t.Fatal("Normalize() retained input metric pointers")
	}
	*normalized.EstimatedTokens.OutputTokens = 103
	*normalized.ToolCalls = 104
	if *metrics.EstimatedTokens.OutputTokens == 103 || *metrics.ToolCalls == 104 {
		t.Fatal("Normalize() exposed mutable metric pointers")
	}

	cases := loadCurrentEvaluationFixture(t)
	shared := validVariantMetricsForTest()
	executor := VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
		return VariantExecutionResult{
			Status:       VariantStatusCompleted,
			OutputDigest: variantTestDigest(request.Variant.ID),
			Metrics:      &shared,
		}, nil
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
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	executions := report.Cases[0].Executions
	if len(executions) != 3 || executions[0].Result.Metrics == nil || executions[1].Result.Metrics == nil || executions[2].Result.Metrics == nil {
		t.Fatalf("isolated metrics = %#v", executions)
	}
	if executions[0].Result.Metrics == executions[1].Result.Metrics || executions[1].Result.Metrics == executions[2].Result.Metrics ||
		executions[0].Result.Metrics.MeasuredTokens == executions[1].Result.Metrics.MeasuredTokens {
		t.Fatal("variant results share metric pointers")
	}
	*executions[0].Result.Metrics.MeasuredTokens.InputTokens = 999
	if *executions[1].Result.Metrics.MeasuredTokens.InputTokens == 999 || *shared.MeasuredTokens.InputTokens == 999 {
		t.Fatal("mutating one result changed another result or executor state")
	}
}

func TestVariantRunnerSanitizesInvalidMetricsWithoutBlockingHealthyVariants(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	invalid := validVariantMetricsForTest()
	invalid.ModelCalls = metricInt64Pointer(-7)
	healthy := validVariantMetricsForTest()
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
			return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("direct"), Metrics: &invalid}, nil
		})},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
			return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("text"), Metrics: &healthy}, nil
		})},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
			return VariantExecutionResult{Status: VariantStatusCompleted, OutputDigest: variantTestDigest("manu"), Metrics: &healthy}, nil
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
	byID := make(map[string]VariantExecutionRecord, len(report.Cases[0].Executions))
	for _, execution := range report.Cases[0].Executions {
		byID[execution.VariantID] = execution
	}
	if byID["direct"].ErrorCode != "invalid_executor_result" || byID["direct"].Result.Metrics != nil {
		t.Fatalf("invalid metric result = %#v", byID["direct"])
	}
	if byID["text"].Outcome != VariantOutcomeCompleted || byID["text"].Result.Metrics == nil || byID["manu"].Outcome != VariantOutcomeCompleted || byID["manu"].Result.Metrics == nil {
		t.Fatalf("healthy variants blocked = %#v", byID)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "-7") {
		t.Fatalf("invalid metric value leaked: %s", encoded)
	}
}

func validVariantMetricsForTest() VariantMetrics {
	return VariantMetrics{
		ObserverID:      "observer",
		ObserverVersion: "v1",
		MeasuredTokens: &VariantMeasuredTokens{
			InputTokens: metricInt64Pointer(7), OutputTokens: metricInt64Pointer(11), SourceID: "provider-usage", SourceVersion: "v1",
		},
		EstimatedTokens: &VariantEstimatedTokens{
			InputTokens: metricInt64Pointer(13), OutputTokens: metricInt64Pointer(17), EstimatorID: "token-estimator", EstimatorVersion: "v1",
		},
		ModelCalls:    metricInt64Pointer(2),
		ToolCalls:     metricInt64Pointer(3),
		FilesRead:     metricInt64Pointer(5),
		BytesRead:     metricInt64Pointer(8),
		Duration:      &VariantDuration{Value: 21, Unit: VariantDurationMilliseconds},
		EstimatedCost: &VariantEstimatedCost{USD: 0.25, EstimatorID: "cost-estimator", EstimatorVersion: "v1"},
		ActualCost:    &VariantActualCost{USD: 0.5, SourceID: "billing-usage", SourceVersion: "v1"},
	}
}

func metricInt64Pointer(value int64) *int64 {
	return &value
}
