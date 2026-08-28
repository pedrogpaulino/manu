package evaluation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestBuildVariantReportsProducesValidatedDetachedArtifacts(t *testing.T) {
	cases := reportingCaseSet(t, 2)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 4, 2, 3, 5)
		return reportingResult(request.Case, &metrics, true)
	})
	metadata := reportingMetadata("run-reporting-1", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), time.Date(2026, 8, 28, 12, 1, 0, 0, time.UTC))
	metadata.Limitations = []string{"z-limitation", "a-limitation", "z-limitation"}

	raw, summary, err := BuildVariantReports(cases, execution, metadata)
	if err != nil {
		t.Fatalf("BuildVariantReports() error = %v", err)
	}
	if err := raw.Validate(); err != nil {
		t.Fatalf("raw Validate() error = %v", err)
	}
	if err := summary.Validate(); err != nil {
		t.Fatalf("summary Validate() error = %v", err)
	}
	if raw.CaseSetDigest == "" || raw.InputDigest == "" || raw.ResultDigest == "" || raw.ReproducibilityDigest == "" || raw.ArtifactDigest == "" {
		t.Fatalf("raw digests incomplete = %#v", raw)
	}
	if summary.RawDigest != raw.ArtifactDigest || summary.ArtifactDigest != raw.ArtifactDigest {
		t.Fatalf("summary/raw artifact identity diverged: raw=%q summary=%#v", raw.ArtifactDigest, summary)
	}
	if summary.SummaryDigest == "" {
		t.Fatal("summary digest is missing")
	}
	if got := len(summary.Configurations); got != 4 { // retrieval plus the three fixture variant configurations.
		t.Fatalf("summary configurations = %d, want 4", got)
	}
	if !sort.SliceIsSorted(summary.Configurations, func(i, j int) bool {
		return summary.Configurations[i].ID < summary.Configurations[j].ID
	}) {
		t.Fatalf("summary configurations are not sorted: %#v", summary.Configurations)
	}
	for index := 1; index < len(summary.Configurations); index++ {
		if summary.Configurations[index-1] == summary.Configurations[index] {
			t.Fatalf("summary configurations were not deduplicated: %#v", summary.Configurations)
		}
	}
	if !reflect.DeepEqual(raw.Metadata.Limitations, []string{"a-limitation", "z-limitation"}) {
		t.Fatalf("metadata limitations were not sorted/deduplicated: %#v", raw.Metadata.Limitations)
	}
	if len(summary.Limitations) == 0 {
		t.Fatal("summary omitted report limitations")
	}

	rawJSON, err := MarshalVariantRawReport(raw)
	if err != nil {
		t.Fatalf("MarshalVariantRawReport() error = %v", err)
	}
	if !bytes.HasSuffix(rawJSON, []byte("\n")) {
		t.Fatal("raw canonical JSON has no final newline")
	}
	summaryJSON, err := MarshalVariantSummaryReport(summary)
	if err != nil {
		t.Fatalf("MarshalVariantSummaryReport() error = %v", err)
	}
	for _, forbidden := range []string{"password=", "secret-value", "public class", "source excerpt", "response text"} {
		if strings.Contains(string(rawJSON), forbidden) || strings.Contains(string(summaryJSON), forbidden) {
			t.Fatalf("report serialized forbidden content %q", forbidden)
		}
	}

	// BuildVariantReports owns detached projections. Mutating any input after
	// the build must not alter either returned artifact.
	cases.Cases[0].Limitations[0] = "mutated-input-case"
	execution.Cases[0].Executions[0].ToolIDs[0] = "mutated-input-execution"
	metadata.Frontends[0].ID = "mutated-input-frontend"
	metadata.Retrieval.Settings["top_k"] = "999"
	if strings.Contains(raw.Metadata.Limitations[0], "mutated") || strings.Contains(raw.Execution.Cases[0].Executions[0].ToolIDs[0], "mutated") || strings.Contains(raw.Metadata.Frontends[0].ID, "mutated") {
		t.Fatal("raw report aliases an input collection")
	}
	if raw.Metadata.Retrieval.Settings["top_k"] == "999" {
		t.Fatal("raw report aliases retrieval settings")
	}

	// The raw and summary projections are detached from one another as well.
	summaryBeforeRawMutation := summary.Clone()
	for index := range raw.Execution.Cases {
		for executionIndex := range raw.Execution.Cases[index].Executions {
			record := &raw.Execution.Cases[index].Executions[executionIndex]
			if record.Result.Metrics != nil && record.Result.Metrics.ActualCost != nil {
				record.Result.Metrics.ActualCost.USD = 999
			}
		}
	}
	if !reflect.DeepEqual(summary, summaryBeforeRawMutation) {
		t.Fatal("mutating raw report changed summary projection")
	}
	rawBeforeSummaryMutation := raw.Clone()
	for index := range summary.Observed {
		if summary.Observed[index].ActualCostUSD != nil {
			summary.Observed[index].ActualCostUSD.Min = 999
			summary.Observed[index].ActualCostUSD.Mean = 999
			summary.Observed[index].ActualCostUSD.Median = 999
			summary.Observed[index].ActualCostUSD.Max = 999
			summary.Observed[index].ActualCostUSD.PopulationStdDev = 0
		}
	}
	if !reflect.DeepEqual(raw, rawBeforeSummaryMutation) {
		t.Fatal("mutating summary changed raw projection")
	}
}

func TestVariantReportsCanonicalizeOrderAndSeparateArtifactFromReproducibilityDigests(t *testing.T) {
	cases := reportingCaseSet(t, 2)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 4, 2, 3, 5)
		return reportingResult(request.Case, &metrics, true)
	})
	metadata := reportingMetadata("run-reporting-order-a", time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), time.Date(2026, 8, 28, 12, 1, 0, 0, time.UTC))
	firstRaw, firstSummary, err := BuildVariantReports(cases, execution, metadata)
	if err != nil {
		t.Fatalf("first BuildVariantReports() error = %v", err)
	}

	reorderedCases := cloneCaseSet(cases)
	reverseEvaluationCases(reorderedCases.Cases)
	reorderedExecution := cloneVariantExecutionReport(execution)
	reverseVariantExecutionReport(reorderedExecution)
	reorderedMetadata := metadata.Clone()
	reverseEvaluationComponents(reorderedMetadata.Frontends)
	reverseEvaluationComponents(reorderedMetadata.Rules)
	reverseEvaluationComponents(reorderedMetadata.Tools)
	reverseStrings(reorderedMetadata.Limitations)
	reorderedMetadata.Retrieval.Settings = map[string]string{"timeout_ms": "1000", "top_k": "5", "strategy": "hybrid"}

	secondRaw, secondSummary, err := BuildVariantReports(reorderedCases, reorderedExecution, reorderedMetadata)
	if err != nil {
		t.Fatalf("reordered BuildVariantReports() error = %v", err)
	}
	firstRawJSON, err := MarshalVariantRawReport(firstRaw)
	if err != nil {
		t.Fatal(err)
	}
	secondRawJSON, err := MarshalVariantRawReport(secondRaw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstRawJSON, secondRawJSON) {
		t.Fatalf("raw canonical bytes changed after reordering inputs")
	}
	firstSummaryJSON, err := MarshalVariantSummaryReport(firstSummary)
	if err != nil {
		t.Fatal(err)
	}
	secondSummaryJSON, err := MarshalVariantSummaryReport(secondSummary)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstSummaryJSON, secondSummaryJSON) {
		t.Fatalf("summary canonical bytes changed after reordering inputs")
	}
	if firstRaw.ArtifactDigest != secondRaw.ArtifactDigest || firstRaw.InputDigest != secondRaw.InputDigest || firstRaw.ResultDigest != secondRaw.ResultDigest || firstRaw.ReproducibilityDigest != secondRaw.ReproducibilityDigest {
		t.Fatalf("digests changed after semantically irrelevant reordering: first=%#v second=%#v", firstRaw, secondRaw)
	}

	changedMetadata := metadata.Clone()
	changedMetadata.RunID = "run-reporting-order-b"
	changedMetadata.StartedAt = time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	changedMetadata.FinishedAt = time.Date(2026, 8, 29, 12, 1, 0, 0, time.UTC)
	secondRunRaw, _, err := BuildVariantReports(cases, execution, changedMetadata)
	if err != nil {
		t.Fatalf("changed-run BuildVariantReports() error = %v", err)
	}
	if firstRaw.InputDigest != secondRunRaw.InputDigest || firstRaw.ResultDigest != secondRunRaw.ResultDigest || firstRaw.ReproducibilityDigest != secondRunRaw.ReproducibilityDigest {
		t.Fatalf("run identity/timestamps changed stable digests: first=%#v second=%#v", firstRaw, secondRunRaw)
	}
	if firstRaw.ArtifactDigest == secondRunRaw.ArtifactDigest {
		t.Fatal("artifact digest ignored run identity/timestamps")
	}
	comparison, err := CompareVariantReports(firstRaw, secondRunRaw)
	if err != nil {
		t.Fatalf("CompareVariantReports() error = %v", err)
	}
	if comparison.Equal || !comparison.Reproducible || len(comparison.ChangedDimensions) != 0 {
		t.Fatalf("run-only comparison = %#v", comparison)
	}
	sameComparison, err := CompareVariantReports(firstRaw, firstRaw)
	if err != nil {
		t.Fatalf("CompareVariantReports(equal) error = %v", err)
	}
	if !sameComparison.Equal || !sameComparison.Reproducible || len(sameComparison.ChangedDimensions) != 0 {
		t.Fatalf("equal comparison = %#v", sameComparison)
	}
}

func TestVariantReportsRejectIncoherenceAndCorruptedDigests(t *testing.T) {
	cases := reportingCaseSet(t, 1)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 4, 2, 3, 5)
		return reportingResult(request.Case, &metrics, true)
	})
	metadata := reportingMetadata("run-reporting-invalid", time.Time{}, time.Time{})
	raw, summary, err := BuildVariantReports(cases, execution, metadata)
	if err != nil {
		t.Fatal(err)
	}

	badInput := raw.Clone()
	badInput.InputDigest = variantTestDigest("wrong-input")
	if err := badInput.Validate(); !errors.Is(err, ErrVariantReportDigestMismatch) {
		t.Fatalf("corrupted input digest error = %v", err)
	}
	if _, err := MarshalVariantRawReport(badInput); !errors.Is(err, ErrVariantReportDigestMismatch) {
		t.Fatalf("MarshalVariantRawReport(corrupted input) error = %v", err)
	}
	badSummary := summary.Clone()
	badSummary.RawDigest = variantTestDigest("wrong-raw")
	if err := badSummary.Validate(); !errors.Is(err, ErrVariantReportDigestMismatch) {
		t.Fatalf("corrupted summary digest error = %v", err)
	}
	if _, err := MarshalVariantSummaryReport(badSummary); !errors.Is(err, ErrVariantReportDigestMismatch) {
		t.Fatalf("MarshalVariantSummaryReport(corrupted raw) error = %v", err)
	}
	badSummary = summary.Clone()
	badSummary.SummaryDigest = variantTestDigest("wrong-summary")
	if err := badSummary.Validate(); !errors.Is(err, ErrVariantReportDigestMismatch) {
		t.Fatalf("corrupted summary digest error = %v", err)
	}

	badExecution := cloneVariantExecutionReport(execution)
	badExecution.Cases[0].SourceRevision = "different-source-revision"
	if _, _, err := BuildVariantReports(cases, badExecution, metadata); !errors.Is(err, ErrInvalidVariantReport) {
		t.Fatalf("incoherent execution error = %v", err)
	}
	badCaseSet := cloneCaseSet(cases)
	badCaseSet.Cases[0].SourceRevision = "different-source-revision"
	if _, _, err := BuildVariantReports(badCaseSet, execution, metadata); !errors.Is(err, ErrInvalidVariantReport) {
		t.Fatalf("incoherent case-set error = %v", err)
	}
}

func TestCompareVariantReportsDetectsIndependentDimensionsAndStableOrder(t *testing.T) {
	cases := reportingCaseSet(t, 1)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 4, 2, 3, 5)
		return reportingResult(request.Case, &metrics, true)
	})
	metadata := reportingMetadata("run-reporting-comparison", time.Time{}, time.Time{})
	base, _, err := BuildVariantReports(cases, execution, metadata)
	if err != nil {
		t.Fatal(err)
	}

	frontend := metadata.Clone()
	frontend.Frontends[0].Digest = variantTestDigest("frontend-v2")
	frontendRaw, _, err := BuildVariantReports(cases, execution, frontend)
	if err != nil {
		t.Fatal(err)
	}
	rule := metadata.Clone()
	rule.Rules[0].Digest = variantTestDigest("rule-v2")
	ruleRaw, _, err := BuildVariantReports(cases, execution, rule)
	if err != nil {
		t.Fatal(err)
	}
	retrieval := metadata.Clone()
	retrieval.Retrieval.Settings["top_k"] = "7"
	retrievalRaw, _, err := BuildVariantReports(cases, execution, retrieval)
	if err != nil {
		t.Fatal(err)
	}
	changedExecution := cloneVariantExecutionReport(execution)
	changedExecution.Cases[0].Executions[0].Result.OutputDigest = variantTestDigest("changed-output")
	resultsRaw, _, err := BuildVariantReports(cases, changedExecution, metadata)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		raw  VariantRawReport
		want string
	}{
		{name: "frontend", raw: frontendRaw, want: "frontend"},
		{name: "rule", raw: ruleRaw, want: "rule"},
		{name: "retrieval", raw: retrievalRaw, want: "retrieval"},
		{name: "results", raw: resultsRaw, want: "results"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			comparison, err := CompareVariantReports(base, test.raw)
			if err != nil {
				t.Fatalf("CompareVariantReports() error = %v", err)
			}
			if comparison.Equal || comparison.Reproducible || !reflect.DeepEqual(comparison.ChangedDimensions, []string{test.want}) {
				t.Fatalf("comparison = %#v, want only %q", comparison, test.want)
			}
			if strings.Contains(strings.Join(comparison.ChangedDimensions, ","), "improvement") {
				t.Fatal("comparison classified a change as improvement")
			}
		})
	}

	allChangedMetadata := metadata.Clone()
	allChangedMetadata.Frontends[0].Digest = variantTestDigest("frontend-v2")
	allChangedMetadata.Rules[0].Digest = variantTestDigest("rule-v2")
	allChangedMetadata.Retrieval.Settings["top_k"] = "7"
	allChangedExecution := cloneVariantExecutionReport(execution)
	allChangedExecution.Cases[0].Executions[0].Result.OutputDigest = variantTestDigest("changed-output")
	allChanged, _, err := BuildVariantReports(cases, allChangedExecution, allChangedMetadata)
	if err != nil {
		t.Fatal(err)
	}
	comparison, err := CompareVariantReports(base, allChanged)
	if err != nil {
		t.Fatal(err)
	}
	wantDimensions := []string{"frontend", "results", "retrieval", "rule"}
	if !reflect.DeepEqual(comparison.ChangedDimensions, wantDimensions) {
		t.Fatalf("changed dimensions = %v, want sorted %v", comparison.ChangedDimensions, wantDimensions)
	}
}

func TestVariantSummaryReportsSamplesOutcomesAndComparableSavings(t *testing.T) {
	cases := reportingCaseSet(t, 2)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 4, 10, 12, 5)
		if request.Variant.ID == "text" {
			metrics = efficiencyMetricsForTest(5, 2, 5, 6, 3)
		}
		failed := request.Variant.ID == "text" && request.Case.CaseID == "EVAL-CTX-02"
		return reportingResult(request.Case, &metrics, !failed)
	})
	raw, summary, err := BuildVariantReports(cases, execution, reportingMetadata("run-reporting-samples", time.Time{}, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := summary.Samples, (VariantReportSample{Cases: 2, Executions: 6, Successful: 5, Completed: 5, Failed: 1, Comparisons: 4, ComparableComparisons: 3}); got != want {
		t.Fatalf("summary sample = %#v, want %#v", got, want)
	}
	if len(summary.Outcomes) != 3 || !sort.SliceIsSorted(summary.Outcomes, func(i, j int) bool {
		return variantOutcomeKey(summary.Outcomes[i]) < variantOutcomeKey(summary.Outcomes[j])
	}) {
		t.Fatalf("outcomes are not deterministic = %#v", summary.Outcomes)
	}
	textOutcome := reportingOutcome(t, summary, "text")
	if textOutcome.Attempted != 2 || textOutcome.Successful != 1 || textOutcome.Completed != 1 || textOutcome.Failed != 1 {
		t.Fatalf("text outcome = %#v", textOutcome)
	}
	directOutcome := reportingOutcome(t, summary, "direct")
	if directOutcome.Attempted != 2 || directOutcome.Successful != 2 || directOutcome.Completed != 2 || directOutcome.Failed != 0 {
		t.Fatalf("direct outcome = %#v", directOutcome)
	}
	if len(summary.Savings) != 2 {
		t.Fatalf("savings groups = %#v", summary.Savings)
	}
	for _, saving := range summary.Savings {
		if saving.BaselineVariantID != "direct" || saving.BaselineVariantKind != VariantDirectSource {
			t.Fatalf("invalid savings baseline = %#v", saving)
		}
		if saving.CandidateVariantID == "text" && (saving.ActualCostSaving == nil || saving.ActualCostSaving.Available != 1) {
			t.Fatalf("text comparable savings = %#v", saving)
		}
		if saving.CandidateVariantID == "manu" && (saving.ActualCostSaving == nil || saving.ActualCostSaving.Available != 2) {
			t.Fatalf("Manu actual-cost savings = %#v", saving)
		}
	}
	if raw.ResultDigest == "" || summary.ResultDigest != raw.ResultDigest {
		t.Fatalf("summary result digest = %q, raw = %q", summary.ResultDigest, raw.ResultDigest)
	}
}

func TestVariantSummaryReportsDistributionsPreserveMissingMetricsAndStatistics(t *testing.T) {
	values := map[string]struct {
		input, output int64
		actual        float64
		estimated     float64
	}{
		"EVAL-CTX-01": {input: 1, output: 2, actual: 1, estimated: 2},
		"EVAL-CTX-02": {input: 3, output: 4, actual: 3, estimated: 4},
		"EVAL-CTX-03": {input: 5, output: 6, actual: 5, estimated: 6},
	}
	cases := reportingCaseSet(t, 3)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		value := values[request.Case.CaseID]
		var metrics VariantMetrics
		switch request.Variant.ID {
		case "direct":
			metrics = efficiencyMetricsForTest(value.input, value.output, value.actual, value.estimated, value.input)
		case "text":
			metrics = efficiencyMetricsForTest(value.input*2, value.output*2, value.actual/2, value.estimated/2, value.input)
		case "manu":
			metrics = VariantMetrics{
				ObserverID: "partial-observer", ObserverVersion: "v1",
				MeasuredTokens:  &VariantMeasuredTokens{InputTokens: metricInt64Pointer(value.input), SourceID: "provider-usage", SourceVersion: "v1"},
				EstimatedTokens: &VariantEstimatedTokens{OutputTokens: metricInt64Pointer(value.output), EstimatorID: "token-estimator", EstimatorVersion: "v1"},
			}
		}
		return reportingResult(request.Case, &metrics, true)
	})
	_, summary, err := BuildVariantReports(cases, execution, reportingMetadata("run-reporting-distribution", time.Time{}, time.Time{}))
	if err != nil {
		t.Fatal(err)
	}
	direct := reportingMetricDistribution(t, summary, "direct")
	if direct.MeasuredInputTokens == nil || direct.MeasuredInputTokens.Available != 3 {
		t.Fatalf("direct measured input distribution = %#v", direct.MeasuredInputTokens)
	}
	assertReportingDistribution(t, direct.MeasuredInputTokens, 1, 3, 3, 5, math.Sqrt(8.0/3.0))
	if direct.EstimatedInputTokens == nil {
		t.Fatal("direct estimated input distribution is unavailable")
	}
	assertReportingDistribution(t, direct.EstimatedInputTokens, 8, 10, 10, 12, math.Sqrt(8.0/3.0))
	if direct.ActualCostUSD == nil || direct.EstimatedCostUSD == nil {
		t.Fatalf("direct cost distributions = %#v", direct)
	}
	assertReportingDistribution(t, direct.ActualCostUSD, 1, 3, 3, 5, math.Sqrt(8.0/3.0))
	assertReportingDistribution(t, direct.EstimatedCostUSD, 2, 4, 4, 6, math.Sqrt(8.0/3.0))
	for name, distribution := range map[string]*MetricDistribution{
		"correct":                direct.Correct,
		"completed":              direct.Completed,
		"criteria passed":        direct.CriteriaPassed,
		"evidence recall":        direct.EvidenceRecall,
		"evidence precision":     direct.EvidencePrecision,
		"citation rate":          direct.CitationRate,
		"gap recall":             direct.GapRecall,
		"abstention appropriate": direct.AbstentionAppropriate,
	} {
		if distribution == nil || distribution.Available != 3 || distribution.Mean != 1 {
			t.Errorf("quality distribution %q = %#v, want three observations at one", name, distribution)
		}
	}

	manu := reportingMetricDistribution(t, summary, "manu")
	if manu.ActualCostUSD != nil || manu.EstimatedCostUSD != nil || manu.MeasuredOutputTokens != nil || manu.EstimatedInputTokens != nil {
		t.Fatalf("missing metrics fabricated distributions = %#v", manu)
	}
	if manu.MeasuredInputTokens == nil || manu.EstimatedOutputTokens == nil {
		t.Fatalf("partial metrics lost observed sides = %#v", manu)
	}
	for _, saving := range summary.Savings {
		if saving.CandidateVariantID != "text" {
			continue
		}
		if saving.MeasuredInputTokensSaving == nil || saving.EstimatedInputTokensSaving == nil {
			t.Fatalf("measured/estimated savings were conflated or omitted = %#v", saving)
		}
		if saving.ActualCostSaving == nil || saving.EstimatedCostSaving == nil {
			t.Fatalf("cost savings missing = %#v", saving)
		}
	}
}

func TestVariantSummaryReportsEvenMedianAndRejectsNonFiniteUnsafeOrExcessiveData(t *testing.T) {
	for _, test := range []struct {
		name  string
		count int
		want  float64
	}{
		{name: "odd", count: 3, want: 3},
		{name: "even", count: 2, want: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			cases := reportingCaseSet(t, test.count)
			execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
				value := int64(1)
				if request.Case.CaseID == "EVAL-CTX-02" {
					value = 3
				}
				if request.Case.CaseID == "EVAL-CTX-03" {
					value = 5
				}
				metrics := efficiencyMetricsForTest(value, 1, float64(value), float64(value), value)
				return reportingResult(request.Case, &metrics, true)
			})
			_, summary, err := BuildVariantReports(cases, execution, reportingMetadata("run-reporting-median-"+test.name, time.Time{}, time.Time{}))
			if err != nil {
				t.Fatal(err)
			}
			direct := reportingMetricDistribution(t, summary, "direct")
			if direct.ActualCostUSD == nil || direct.ActualCostUSD.Median != test.want {
				t.Fatalf("median = %#v, want %v", direct.ActualCostUSD, test.want)
			}
		})
	}

	cases := reportingCaseSet(t, 1)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		metrics := efficiencyMetricsForTest(10, 4, 2, 3, 5)
		return reportingResult(request.Case, &metrics, true)
	})
	metadata := reportingMetadata("run-reporting-unsafe", time.Time{}, time.Time{})
	for _, test := range []struct {
		name   string
		modify func(*VariantReportMetadata)
		want   error
	}{
		{name: "secret marker", modify: func(value *VariantReportMetadata) { value.Limitations = []string{"secret=do-not-store"} }, want: ErrInvalidVariantReport},
		{name: "raw marker", modify: func(value *VariantReportMetadata) { value.Limitations = []string{"public class Example {}"} }, want: ErrInvalidVariantReport},
		{name: "excessive limitations", modify: func(value *VariantReportMetadata) {
			value.Limitations = make([]string, maxListItems+1)
			for index := range value.Limitations {
				value.Limitations[index] = fmt.Sprintf("limitation-%d", index)
			}
		}, want: ErrVariantReportLimitExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := metadata.Clone()
			test.modify(&candidate)
			_, _, err := BuildVariantReports(cases, execution, candidate)
			if !errors.Is(err, test.want) {
				t.Fatalf("BuildVariantReports() error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "do-not-store") {
				t.Fatal("unsafe report error echoed secret content")
			}
		})
	}

	raw, _, err := BuildVariantReports(cases, execution, metadata)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		candidate := raw.Clone()
		candidate.Execution.Cases[0].Executions[0].Result.Metrics.ActualCost.USD = value
		if _, err := MarshalVariantRawReport(candidate); err == nil {
			t.Fatalf("non-finite metric %v was accepted", value)
		}
	}
}

func TestVariantSummaryReportsRetainNegativeSavingsAsRegressions(t *testing.T) {
	cases := reportingCaseSet(t, 1)
	execution := reportingExecution(t, cases, func(request VariantExecutionRequest) VariantExecutionResult {
		actual, estimated := 1.0, 1.0
		if request.Variant.ID == "text" {
			actual, estimated = 2.0, 2.0
		}
		metrics := efficiencyMetricsForTest(10, 4, actual, estimated, 1)
		return reportingResult(request.Case, &metrics, true)
	})
	_, summary, err := BuildVariantReports(cases, execution, reportingMetadata("run-reporting-regression", time.Time{}, time.Time{}))
	if err != nil {
		t.Fatalf("BuildVariantReports() rejected a comparable regression: %v", err)
	}
	for _, saving := range summary.Savings {
		if saving.CandidateVariantID != "text" {
			continue
		}
		if saving.ActualCostSaving == nil || saving.ActualCostSaving.Min != -1 || saving.ActualCostSaving.Mean != -1 || saving.ActualCostSaving.Max != -1 {
			t.Fatalf("negative actual-cost saving was not retained: %#v", saving)
		}
		return
	}
	t.Fatal("text regression savings group not found")
}

func reportingCaseSet(t *testing.T, count int) CaseSet {
	t.Helper()
	return efficiencyCaseSet(t, count)
}

func reportingExecution(t *testing.T, cases CaseSet, result func(VariantExecutionRequest) VariantExecutionResult) VariantExecutionReport {
	t.Helper()
	runner := newEfficiencyRunner(t, false, result)
	execution, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatalf("VariantRunner.Run() error = %v", err)
	}
	return execution
}

func reportingResult(item EvaluationCase, metrics *VariantMetrics, success bool) VariantExecutionResult {
	result := qualityResultForCase(item, VariantConclusionPassed)
	result.Metrics = metrics
	if !success {
		result.Status = VariantStatusFailed
		result.Conclusion = VariantConclusionFailed
		result.OutputDigest = ""
	}
	return result
}

func reportingMetadata(runID string, startedAt, finishedAt time.Time) VariantReportMetadata {
	return VariantReportMetadata{
		RunID: runID, StartedAt: startedAt, FinishedAt: finishedAt,
		Agent:         EvaluationComponent{ID: "manu-agent", Version: "v1"},
		Model:         EvaluationComponent{ID: "test-model", Version: "v1"},
		ContextServer: EvaluationComponent{ID: "manu-context", Version: "v1", Digest: variantTestDigest("context-server")},
		Frontends: []EvaluationComponent{
			{ID: "java-quarkus", Version: "v1", Digest: variantTestDigest("frontend-java")},
			{ID: "python-frappe", Version: "v1", Digest: variantTestDigest("frontend-python")},
		},
		Rules: []EvaluationComponent{{ID: "membership", Version: "v1", Digest: variantTestDigest("rule-membership")}},
		Retrieval: EvaluationConfiguration{ID: "hybrid-retrieval", Version: "v1", Settings: map[string]string{
			"strategy": "hybrid", "top_k": "5", "timeout_ms": "1000",
		}},
		Tools:       []EvaluationComponent{{ID: "filesystem-search", Version: "v1"}},
		Limitations: []string{"fixture-only", "local bounded sample"},
	}
}

func reportingOutcome(t *testing.T, summary VariantSummaryReport, variantID string) VariantReportOutcome {
	t.Helper()
	for _, outcome := range summary.Outcomes {
		if outcome.VariantID == variantID {
			return outcome
		}
	}
	t.Fatalf("summary outcome %q not found: %#v", variantID, summary.Outcomes)
	return VariantReportOutcome{}
}

func reportingMetricDistribution(t *testing.T, summary VariantSummaryReport, variantID string) VariantMetricDistribution {
	t.Helper()
	for _, metrics := range summary.Observed {
		if metrics.VariantID == variantID {
			return metrics
		}
	}
	t.Fatalf("summary metric distribution %q not found: %#v", variantID, summary.Observed)
	return VariantMetricDistribution{}
}

func assertReportingDistribution(t *testing.T, got *MetricDistribution, min, mean, median, max, stddev float64) {
	t.Helper()
	if got == nil {
		t.Fatal("distribution is nil")
	}
	if got.Available == 0 || math.Abs(got.Min-min) > 1e-12 || math.Abs(got.Mean-mean) > 1e-12 || math.Abs(got.Median-median) > 1e-12 || math.Abs(got.Max-max) > 1e-12 || math.Abs(got.PopulationStdDev-stddev) > 1e-12 {
		t.Fatalf("distribution = %#v, want min=%v mean=%v median=%v max=%v stddev=%v", got, min, mean, median, max, stddev)
	}
}

func reverseEvaluationCases(values []EvaluationCase) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseVariantExecutionReport(report VariantExecutionReport) {
	reverseVariantCaseReports(report.Cases)
	for index := range report.Cases {
		reverseVariantExecutionRecords(report.Cases[index].Executions)
	}
	reverseVariantEfficiencyAggregates(report.Efficiency.Aggregates)
	reverseVariantEfficiencyComparisons(report.Efficiency.Comparisons)
}

func reverseVariantCaseReports(values []VariantCaseReport) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseVariantExecutionRecords(values []VariantExecutionRecord) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseVariantEfficiencyAggregates(values []VariantEfficiencyAggregate) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseVariantEfficiencyComparisons(values []VariantEfficiencyComparison) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseEvaluationComponents(values []EvaluationComponent) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
