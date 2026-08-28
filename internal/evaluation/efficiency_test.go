package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestVariantEfficiencyDerivesIndependentPerSuccessMetricsAndSavings(t *testing.T) {
	cases := efficiencyCaseSet(t, 1)
	values := map[string]struct {
		actual, estimated float64
		input, output     int64
		duration          int64
	}{
		"direct": {actual: 10, estimated: 12, input: 100, output: 40, duration: 10},
		"text":   {actual: 5, estimated: 6, input: 50, output: 20, duration: 5},
		"manu":   {actual: 8, estimated: 9, input: 80, output: 30, duration: 8},
	}
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		value := values[request.Variant.ID]
		metrics := efficiencyMetricsForTest(value.input, value.output, value.actual, value.estimated, value.duration)
		return efficiencyResultForTest(request.Case, metrics, true)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}

	direct := efficiencyAggregateForTest(t, report, "direct")
	text := efficiencyAggregateForTest(t, report, "text")
	if direct.AttemptedTasks != 1 || direct.SuccessfulTasks != 1 || text.AttemptedTasks != 1 || text.SuccessfulTasks != 1 {
		t.Fatalf("aggregate counts = direct:%#v text:%#v", direct, text)
	}
	assertEfficiencyValue(t, direct.ActualCostUSDPerSuccess, 10)
	assertEfficiencyValue(t, direct.EstimatedCostUSDPerSuccess, 12)
	assertEfficiencyValue(t, direct.MeasuredInputTokensPerSuccess, 100)
	assertEfficiencyValue(t, direct.MeasuredOutputTokensPerSuccess, 40)
	assertEfficiencyValue(t, direct.EstimatedInputTokensPerSuccess, 107)
	assertEfficiencyValue(t, direct.EstimatedOutputTokensPerSuccess, 47)
	assertEfficiencyValue(t, direct.ModelCallsPerSuccess, 2)
	if direct.DurationPerSuccess == nil || direct.DurationPerSuccess.Unit != VariantDurationNanoseconds || direct.DurationPerSuccess.Value != 10_000_000 {
		t.Fatalf("duration aggregate = %#v", direct.DurationPerSuccess)
	}

	comparison := efficiencyComparisonForTest(t, report, "text")
	if !comparison.Comparable || comparison.Reason != "" {
		t.Fatalf("comparison = %#v", comparison)
	}
	assertEfficiencyValue(t, comparison.ActualCostSaving, .5)
	assertEfficiencyValue(t, comparison.EstimatedCostSaving, .5)
	assertEfficiencyValue(t, comparison.MeasuredInputTokensSaving, .5)
	assertEfficiencyValue(t, comparison.MeasuredOutputTokensSaving, .5)
	assertEfficiencyValue(t, comparison.DurationSaving, .5)

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	for _, key := range []string{"actual_cost_usd_per_success", "estimated_cost_usd_per_success", "measured_input_tokens_per_success", "actual_cost_saving"} {
		if !strings.Contains(jsonText, key) {
			t.Fatalf("efficiency JSON omitted %q: %s", key, jsonText)
		}
	}
	if strings.Contains(jsonText, "\"score\"") {
		t.Fatalf("efficiency report introduced a composed score: %s", jsonText)
	}
}

func TestVariantEfficiencyUsesAllAttemptsAndOnlySuccessfulDenominator(t *testing.T) {
	cases := efficiencyCaseSet(t, 2)
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		isFailedText := request.Variant.ID == "text" && request.Case.CaseID == "EVAL-CTX-02"
		actual := float64(10)
		input := int64(10)
		if request.Variant.ID == "text" {
			actual = 5
			input = 20
			if isFailedText {
				actual = 7
				input = 30
			}
		}
		metrics := efficiencyMetricsForTest(input, 4, actual, actual+1, 1)
		return efficiencyResultForTest(request.Case, metrics, !isFailedText)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	text := efficiencyAggregateForTest(t, report, "text")
	if text.AttemptedTasks != 2 || text.SuccessfulTasks != 1 {
		t.Fatalf("text aggregate counts = %#v", text)
	}
	assertEfficiencyValue(t, text.ActualCostUSDPerSuccess, 12)
	assertEfficiencyValue(t, text.MeasuredInputTokensPerSuccess, 50)
	comparison := efficiencyComparisonForCaseTest(t, report, "EVAL-CTX-01", "text")
	if !comparison.Comparable {
		t.Fatalf("successful case comparison = %#v", comparison)
	}
	failedComparison := efficiencyComparisonForCaseTest(t, report, "EVAL-CTX-02", "text")
	if failedComparison.Comparable || failedComparison.Reason != efficiencyReasonCandidateNotSuccessful || anyEfficiencySaving(failedComparison) {
		t.Fatalf("failed case comparison = %#v", failedComparison)
	}
}

func TestVariantEfficiencyLeavesAllPerSuccessMetricsUndefinedWithNoSuccess(t *testing.T) {
	cases := efficiencyCaseSet(t, 1)
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		return efficiencyResultForTest(request.Case, efficiencyMetricsForTest(4, 5, 2, 3, 1), false)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	for _, aggregate := range report.Efficiency.Aggregates {
		if aggregate.SuccessfulTasks != 0 || efficiencyAggregateHasMetric(aggregate) {
			t.Fatalf("zero-success aggregate fabricated effort: %#v", aggregate)
		}
	}
	for _, comparison := range report.Efficiency.Comparisons {
		if comparison.Comparable || comparison.Reason != efficiencyReasonBothNotSuccessful || anyEfficiencySaving(comparison) {
			t.Fatalf("zero-success comparison = %#v", comparison)
		}
	}
}

func TestVariantEfficiencyKeepsPartialObservationsAndProvenanceIndependent(t *testing.T) {
	cases := efficiencyCaseSet(t, 2)
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 20, 3, 4, 2)
		if request.Variant.ID == "text" && request.Case.CaseID == "EVAL-CTX-02" {
			metrics.MeasuredTokens.OutputTokens = nil
			metrics.EstimatedCost = nil
			metrics.FilesRead = nil
		}
		return efficiencyResultForTest(request.Case, metrics, true)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	text := efficiencyAggregateForTest(t, report, "text")
	if text.MeasuredInputTokensPerSuccess == nil || text.MeasuredOutputTokensPerSuccess != nil {
		t.Fatalf("token observations were not independent: %#v", text)
	}
	if text.EstimatedCostUSDPerSuccess != nil || text.ActualCostUSDPerSuccess == nil || text.FilesReadPerSuccess != nil {
		t.Fatalf("partial observations were mixed: %#v", text)
	}

	aggregateProvenanceRunner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 20, 3, 4, 2)
		if request.Variant.ID == "text" && request.Case.CaseID == "EVAL-CTX-02" {
			metrics.MeasuredTokens.SourceID = "other-usage"
		}
		return efficiencyResultForTest(request.Case, metrics, true)
	})
	aggregateProvenanceReport, err := aggregateProvenanceRunner.Run(context.Background(), efficiencyCaseSet(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	text = efficiencyAggregateForTest(t, aggregateProvenanceReport, "text")
	if text.MeasuredInputTokensPerSuccess != nil || text.MeasuredOutputTokensPerSuccess != nil || text.ActualCostUSDPerSuccess == nil {
		t.Fatalf("cross-task provenance was not isolated: %#v", text)
	}

	provenanceRunner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 20, 3, 4, 2)
		if request.Variant.ID == "text" {
			metrics.MeasuredTokens.SourceID = "other-usage"
			metrics.EstimatedTokens.EstimatorID = "other-token-estimator"
			metrics.ActualCost.SourceID = "other-billing"
			metrics.EstimatedCost.EstimatorID = "other-cost-estimator"
			metrics.ObserverID = "other-observer"
		}
		return efficiencyResultForTest(request.Case, metrics, true)
	})
	provenanceReport, err := provenanceRunner.Run(context.Background(), efficiencyCaseSet(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	comparison := efficiencyComparisonForTest(t, provenanceReport, "text")
	if !comparison.Comparable || comparison.Reason != "" || anyEfficiencySaving(comparison) {
		t.Fatalf("provenance-incompatible savings were reported: %#v", comparison)
	}
}

func TestVariantEfficiencySavingsHandlePositiveNegativeZeroAndIncorrectCases(t *testing.T) {
	tests := []struct {
		name       string
		baseline   float64
		candidate  float64
		want       *float64
		comparable bool
	}{
		{name: "positive", baseline: 10, candidate: 5, want: efficiencyFloatPointer(.5), comparable: true},
		{name: "regression", baseline: 10, candidate: 20, want: efficiencyFloatPointer(-1), comparable: true},
		{name: "candidate zero", baseline: 10, candidate: 0, want: efficiencyFloatPointer(1), comparable: true},
		{name: "baseline zero", baseline: 0, candidate: 5, want: nil, comparable: true},
		{name: "incorrect candidate", baseline: 10, candidate: 0, want: nil, comparable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cases := efficiencyCaseSet(t, 1)
			runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
				cost := test.baseline
				if request.Variant.ID == "text" {
					cost = test.candidate
				}
				result := efficiencyResultForTest(request.Case, efficiencyMetricsForTest(10, 20, cost, cost+1, 1), true)
				if request.Variant.ID == "text" && test.name == "incorrect candidate" {
					result.ClaimIDs = nil
					result.EvidenceIDs = nil
					result.Citations = nil
				}
				return result
			})
			report, err := runner.Run(context.Background(), cases)
			if err != nil {
				t.Fatal(err)
			}
			comparison := efficiencyComparisonForTest(t, report, "text")
			if comparison.Comparable != test.comparable {
				t.Fatalf("comparable = %v, want %v: %#v", comparison.Comparable, test.comparable, comparison)
			}
			if test.want == nil {
				if comparison.ActualCostSaving != nil {
					t.Fatalf("actual saving = %v, want undefined", *comparison.ActualCostSaving)
				}
			} else {
				assertEfficiencyValue(t, comparison.ActualCostSaving, *test.want)
			}
		})
	}
}

func TestVariantEfficiencySeparatesConfigurationsAndExternalNeverBecomesBaseline(t *testing.T) {
	cases := efficiencyCaseSet(t, 2)
	for index := range cases.Cases {
		cases.Cases[index].Variants = append(cases.Cases[index].Variants, EvaluationVariant{
			ID: "external", Kind: VariantExternalContext, ToolIDs: []string{"filesystem-search"},
			ConfigurationID: "direct-read-only", Capabilities: []string{"external.read"}, Limitations: []string{"optional"},
		})
	}
	for index := range cases.Cases[1].Configurations {
		if cases.Cases[1].Configurations[index].ID == "text-read-only" {
			cases.Cases[1].Configurations[index].Settings["retrieval"] = "structured"
		}
	}
	runner := newEfficiencyRunner(t, true, func(request VariantExecutionRequest) VariantExecutionResult {
		return efficiencyResultForTest(request.Case, efficiencyMetricsForTest(10, 20, 4, 5, 1), true)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	textAggregates := make([]VariantEfficiencyAggregate, 0, 2)
	for _, aggregate := range report.Efficiency.Aggregates {
		if aggregate.VariantID == "text" {
			textAggregates = append(textAggregates, aggregate)
		}
	}
	if len(textAggregates) != 2 || textAggregates[0].ConfigurationDigest == textAggregates[1].ConfigurationDigest {
		t.Fatalf("configuration identities were merged: %#v", textAggregates)
	}
	externalAggregate := efficiencyAggregateForTest(t, report, "external")
	if externalAggregate.VariantKind != VariantExternalContext || externalAggregate.AttemptedTasks != 2 {
		t.Fatalf("external aggregate = %#v", externalAggregate)
	}
	for _, comparison := range report.Efficiency.Comparisons {
		if comparison.BaselineVariantKind != VariantDirectSource {
			t.Fatalf("non-direct baseline = %#v", comparison)
		}
	}
	externalComparisons := 0
	for _, comparison := range report.Efficiency.Comparisons {
		if comparison.CandidateVariantID == "external" {
			externalComparisons++
		}
	}
	if externalComparisons != 2 {
		t.Fatalf("external comparisons = %d", externalComparisons)
	}
}

func TestVariantEfficiencyIsDeterministicDetachedAndRejectsAdulteration(t *testing.T) {
	cases := efficiencyCaseSet(t, 2)
	reversed := cloneCaseSet(cases)
	for index := range reversed.Cases {
		for left, right := 0, len(reversed.Cases[index].Variants)-1; left < right; left, right = left+1, right-1 {
			reversed.Cases[index].Variants[left], reversed.Cases[index].Variants[right] = reversed.Cases[index].Variants[right], reversed.Cases[index].Variants[left]
		}
	}
	for left, right := 0, len(reversed.Cases)-1; left < right; left, right = left+1, right-1 {
		reversed.Cases[left], reversed.Cases[right] = reversed.Cases[right], reversed.Cases[left]
	}
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		return efficiencyResultForTest(request.Case, efficiencyMetricsForTest(10, 20, 4, 5, 1), true)
	})
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
		t.Fatalf("efficiency reports differ after normalization:\n%s\n%s", firstJSON, secondJSON)
	}
	if !reflect.DeepEqual(cases, efficiencyCaseSet(t, 2)) {
		t.Fatal("efficiency run mutated input cases")
	}

	clone := first.Efficiency.Clone()
	if clone.Aggregates[0].ActualCostUSDPerSuccess == nil {
		t.Fatal("positive aggregate unexpectedly lacks cost")
	}
	*clone.Aggregates[0].ActualCostUSDPerSuccess = 999
	if *first.Efficiency.Aggregates[0].ActualCostUSDPerSuccess == 999 {
		t.Fatal("efficiency clone shares metric pointer")
	}
	adulterated := first
	adulterated.Efficiency.Aggregates[0].SuccessfulTasks++
	if adulterated.Validate() == nil {
		t.Fatal("adulterated efficiency report validated")
	}
	badSaving := efficiencyComparisonForTest(t, first, "text")
	badSaving.ActualCostSaving = efficiencyFloatPointer(1.1)
	if badSaving.Validate() == nil {
		t.Fatal("saving above one validated")
	}
}

func TestVariantEfficiencyHandlesLargeCountersAndOverflowByDimension(t *testing.T) {
	cases := efficiencyCaseSet(t, 2)
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(1, 1, 1, 1, 1)
		if request.Variant.ID == "text" {
			metrics.MeasuredTokens.InputTokens = metricInt64Pointer(math.MaxInt64)
			metrics.ModelCalls = metricInt64Pointer(math.MaxInt64)
		}
		return efficiencyResultForTest(request.Case, metrics, true)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	text := efficiencyAggregateForTest(t, report, "text")
	if text.MeasuredInputTokensPerSuccess != nil || text.ModelCallsPerSuccess != nil {
		t.Fatalf("overflow dimensions fabricated values: %#v", text)
	}
	if text.ActualCostUSDPerSuccess == nil || text.DurationPerSuccess == nil {
		t.Fatalf("independent dimensions lost after overflow: %#v", text)
	}
}

func TestVariantEfficiencyDurationOverflowLeavesOnlyDurationUndefined(t *testing.T) {
	cases := efficiencyCaseSet(t, 2)
	runner := newEfficiencyRunner(t, false, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(1, 1, 1, 1, 1)
		if request.Variant.ID == "text" {
			metrics.Duration = &VariantDuration{Value: math.MaxInt64, Unit: VariantDurationNanoseconds}
		}
		return efficiencyResultForTest(request.Case, metrics, true)
	})
	report, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatal(err)
	}
	text := efficiencyAggregateForTest(t, report, "text")
	if text.DurationPerSuccess != nil || text.ActualCostUSDPerSuccess == nil {
		t.Fatalf("duration overflow affected unrelated dimensions: %#v", text)
	}
}

func efficiencyCaseSet(t *testing.T, count int) CaseSet {
	t.Helper()
	base := loadCurrentEvaluationFixture(t).Cases[0]
	result := CaseSet{Version: Version, Cases: make([]EvaluationCase, count)}
	for index := range result.Cases {
		item := cloneCase(base)
		item.CaseID = fmt.Sprintf("EVAL-CTX-%02d", index+1)
		item.CaseVersion = 1
		result.Cases[index] = item
	}
	return result
}

func newEfficiencyRunner(t *testing.T, includeExternal bool, result func(VariantExecutionRequest) VariantExecutionResult) *VariantRunner {
	t.Helper()
	executor := VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
		return result(request), nil
	})
	registrations := []VariantExecutorRegistration{
		{Kind: VariantDirectSource, Executor: executor},
		{Kind: VariantTextRetrieval, Executor: executor},
		{Kind: VariantManuContext, Executor: executor},
	}
	if includeExternal {
		registrations = append(registrations, VariantExecutorRegistration{VariantID: "external", Kind: VariantExternalContext, Executor: executor})
	}
	registry, err := NewVariantExecutorRegistry(registrations...)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewVariantRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func efficiencyMetricsForTest(input, output int64, actual, estimated float64, duration int64) VariantMetrics {
	estimatedInput, estimatedOutput := input, output
	if estimatedInput <= math.MaxInt64-7 {
		estimatedInput += 7
	}
	if estimatedOutput <= math.MaxInt64-7 {
		estimatedOutput += 7
	}
	return VariantMetrics{
		ObserverID: "efficiency-observer", ObserverVersion: "v1",
		MeasuredTokens: &VariantMeasuredTokens{
			InputTokens: metricInt64Pointer(input), OutputTokens: metricInt64Pointer(output), SourceID: "provider-usage", SourceVersion: "v1",
		},
		EstimatedTokens: &VariantEstimatedTokens{
			InputTokens: metricInt64Pointer(estimatedInput), OutputTokens: metricInt64Pointer(estimatedOutput), EstimatorID: "token-estimator", EstimatorVersion: "v1",
		},
		ModelCalls: metricInt64Pointer(2), ToolCalls: metricInt64Pointer(3), FilesRead: metricInt64Pointer(4), BytesRead: metricInt64Pointer(5),
		Duration:      &VariantDuration{Value: duration, Unit: VariantDurationMilliseconds},
		EstimatedCost: &VariantEstimatedCost{USD: estimated, EstimatorID: "cost-estimator", EstimatorVersion: "v1"},
		ActualCost:    &VariantActualCost{USD: actual, SourceID: "billing-usage", SourceVersion: "v1"},
	}
}

func efficiencyResultForTest(item EvaluationCase, metrics VariantMetrics, success bool) VariantExecutionResult {
	result := qualityResultForCase(item, VariantConclusionPassed)
	result.Metrics = &metrics
	if !success {
		result.Status = VariantStatusFailed
		result.Conclusion = VariantConclusionFailed
		result.OutputDigest = ""
	}
	return result
}

func efficiencyAggregateForTest(t *testing.T, report VariantExecutionReport, variantID string) VariantEfficiencyAggregate {
	t.Helper()
	for _, aggregate := range report.Efficiency.Aggregates {
		if aggregate.VariantID == variantID {
			return aggregate
		}
	}
	t.Fatalf("aggregate for %q not found: %#v", variantID, report.Efficiency)
	return VariantEfficiencyAggregate{}
}

func efficiencyComparisonForTest(t *testing.T, report VariantExecutionReport, variantID string) VariantEfficiencyComparison {
	return efficiencyComparisonForCaseTest(t, report, report.Cases[0].CaseID, variantID)
}

func efficiencyComparisonForCaseTest(t *testing.T, report VariantExecutionReport, caseID, variantID string) VariantEfficiencyComparison {
	t.Helper()
	for _, comparison := range report.Efficiency.Comparisons {
		if comparison.CaseID == caseID && comparison.CandidateVariantID == variantID {
			return comparison
		}
	}
	t.Fatalf("comparison for case %q variant %q not found: %#v", caseID, variantID, report.Efficiency)
	return VariantEfficiencyComparison{}
}

func assertEfficiencyValue(t *testing.T, value *float64, want float64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("efficiency value = %v, want %v", value, want)
	}
}

func efficiencyAggregateHasMetric(aggregate VariantEfficiencyAggregate) bool {
	return aggregate.ActualCostUSDPerSuccess != nil || aggregate.EstimatedCostUSDPerSuccess != nil ||
		aggregate.MeasuredInputTokensPerSuccess != nil || aggregate.MeasuredOutputTokensPerSuccess != nil ||
		aggregate.EstimatedInputTokensPerSuccess != nil || aggregate.EstimatedOutputTokensPerSuccess != nil ||
		aggregate.ModelCallsPerSuccess != nil || aggregate.ToolCallsPerSuccess != nil || aggregate.FilesReadPerSuccess != nil ||
		aggregate.BytesReadPerSuccess != nil || aggregate.DurationPerSuccess != nil
}

func efficiencyFloatPointer(value float64) *float64 { return &value }
