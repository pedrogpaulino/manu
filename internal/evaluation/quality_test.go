package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestVariantQualityDerivesPositiveDimensionsAndCriteria(t *testing.T) {
	item := qualityCaseWithAllCriteria(t)
	result := qualityResultForCase(item, VariantConclusionPassed)
	quality, err := deriveVariantQuality(item, result)
	if err != nil {
		t.Fatal(err)
	}
	if !quality.Correct || !quality.Completed || !quality.RequiredCriteriaPassed {
		t.Fatalf("positive quality = %#v", quality)
	}
	if got := quality.Evidence; got.Expected != 1 || got.Retrieved != 1 || got.Relevant != 1 || got.K != 1 ||
		got.Recall == nil || *got.Recall != 1 || got.Precision == nil || *got.Precision != 1 {
		t.Fatalf("evidence quality = %#v", got)
	}
	if got := quality.Citations; got.Total != 1 || got.Valid != 1 || got.Rate == nil || *got.Rate != 1 {
		t.Fatalf("citation quality = %#v", got)
	}
	if got := quality.Gaps; got.Expected != 1 || got.Recognized != 1 || got.Recall == nil || *got.Recall != 1 {
		t.Fatalf("gap quality = %#v", got)
	}
	if quality.Abstention.Expected || quality.Abstention.Actual || !quality.Abstention.Appropriate {
		t.Fatalf("unexpected abstention quality = %#v", quality.Abstention)
	}
	if len(quality.Criteria) != len(item.Criteria.Items) {
		t.Fatalf("criteria count = %d, want %d", len(quality.Criteria), len(item.Criteria.Items))
	}
	for _, criterion := range quality.Criteria {
		if !criterion.Evaluated || !criterion.Passed {
			t.Fatalf("positive criterion = %#v", criterion)
		}
	}
}

func TestVariantQualityRejectsClaimEvidenceCitationAndGapMisses(t *testing.T) {
	item := qualityCaseWithAllCriteria(t)
	base := qualityResultForCase(item, VariantConclusionPassed)
	tests := []struct {
		name   string
		result VariantExecutionResult
		check  func(VariantQuality) bool
	}{
		{
			name: "extra claim",
			result: func() VariantExecutionResult {
				value := base.Clone()
				value.ClaimIDs = append(value.ClaimIDs, "extra-claim")
				return value
			}(),
			check: func(quality VariantQuality) bool { return !quality.Correct },
		},
		{
			name: "missing claim",
			result: func() VariantExecutionResult {
				value := base.Clone()
				value.ClaimIDs = nil
				value.Citations = nil
				return value
			}(),
			check: func(quality VariantQuality) bool { return !quality.Correct },
		},
		{
			name: "irrelevant evidence and unsupported citation",
			result: func() VariantExecutionResult {
				value := base.Clone()
				value.EvidenceIDs = []string{"unrelated-evidence"}
				value.Citations = []VariantCitation{{ID: "citation", ClaimID: "eval-ctx-claim", EvidenceID: "unrelated-evidence"}}
				return value
			}(),
			check: func(quality VariantQuality) bool {
				return quality.Evidence.Relevant == 0 && quality.Citations.Valid == 0 && quality.Citations.Rate != nil && *quality.Citations.Rate == 0
			},
		},
		{
			name: "missing gap",
			result: func() VariantExecutionResult {
				value := base.Clone()
				value.GapIDs = nil
				return value
			}(),
			check: func(quality VariantQuality) bool {
				return quality.Gaps.Recognized == 0 && quality.Gaps.Recall != nil && *quality.Gaps.Recall == 0
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			quality, err := deriveVariantQuality(item, test.result)
			if err != nil {
				t.Fatal(err)
			}
			if !test.check(quality) {
				t.Fatalf("quality = %#v", quality)
			}
			if quality.RequiredCriteriaPassed {
				t.Fatalf("failed criteria unexpectedly passed: %#v", quality.Criteria)
			}
		})
	}
}

func TestVariantQualityOmitsUndefinedRatesWithoutFabricatingZero(t *testing.T) {
	item := qualityCaseWithAllCriteria(t)
	result := qualityResultForCase(item, VariantConclusionPassed)
	result.EvidenceIDs = nil
	result.Citations = nil
	quality, err := deriveVariantQuality(item, result)
	if err != nil {
		t.Fatal(err)
	}
	if quality.Evidence.Recall == nil || *quality.Evidence.Recall != 0 {
		t.Fatalf("expected zero recall for expected evidence, got %#v", quality.Evidence)
	}
	if quality.Evidence.Precision != nil || quality.Citations.Rate != nil {
		t.Fatalf("undefined rates fabricated: evidence=%#v citations=%#v", quality.Evidence, quality.Citations)
	}
	for _, criterion := range quality.Criteria {
		if criterion.ID == "evidence-all" || criterion.ID == "citation-all" {
			if criterion.Passed {
				t.Fatalf("empty-reference criterion passed without coverage: %#v", criterion)
			}
		}
	}
	encoded, err := json.Marshal(quality)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(encoded)
	if !strings.Contains(jsonText, `"recall":0`) || strings.Contains(jsonText, `"precision"`) || strings.Contains(jsonText, `"rate"`) {
		t.Fatalf("undefined rates serialized incorrectly: %s", encoded)
	}
}

func TestVariantQualityDerivesExpectedAndUnexpectedAbstention(t *testing.T) {
	base := loadCurrentEvaluationFixture(t).Cases[0]
	expected := cloneCase(base)
	expected.Kind = CaseKindAbstention
	abstained := qualityResultForCase(expected, VariantConclusionAbstained)
	quality, err := deriveVariantQuality(expected, abstained)
	if err != nil {
		t.Fatal(err)
	}
	if !quality.Abstention.Expected || !quality.Abstention.Actual || !quality.Abstention.Appropriate || !quality.Completed {
		t.Fatalf("expected abstention quality = %#v", quality)
	}

	unexpected, err := deriveVariantQuality(base, qualityResultForCase(base, VariantConclusionAbstained))
	if err != nil {
		t.Fatal(err)
	}
	if unexpected.Abstention.Expected || !unexpected.Abstention.Actual || unexpected.Abstention.Appropriate || unexpected.Completed {
		t.Fatalf("unexpected abstention quality = %#v", unexpected)
	}

	failed := abstained
	failed.Status = VariantStatusFailed
	failed.OutputDigest = ""
	failedQuality, err := deriveVariantQuality(expected, failed)
	if err != nil {
		t.Fatal(err)
	}
	if !failedQuality.Abstention.Actual || failedQuality.Abstention.Appropriate || failedQuality.Completed {
		t.Fatalf("failed abstention quality = %#v", failedQuality)
	}
}

func TestVariantExecutionResultNormalizesAndValidatesStructuredCitations(t *testing.T) {
	result := VariantExecutionResult{
		Status:       VariantStatusCompleted,
		OutputDigest: variantTestDigest("structured-citation"),
		ClaimIDs:     []string{"claim-b", "claim-a"},
		EvidenceIDs:  []string{"evidence-b", "evidence-a"},
		Citations: []VariantCitation{
			{ID: "citation-b", ClaimID: "claim-b", EvidenceID: "evidence-b"},
			{ID: "citation-a", ClaimID: "claim-a", EvidenceID: "evidence-a"},
		},
	}
	normalized, err := result.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.ClaimIDs, []string{"claim-a", "claim-b"}) ||
		!reflect.DeepEqual(normalized.EvidenceIDs, []string{"evidence-a", "evidence-b"}) || normalized.Citations[0].ID != "citation-a" {
		t.Fatalf("normalized result = %#v", normalized)
	}
	result.ClaimIDs[0] = "mutated"
	result.Citations[0].ID = "mutated"
	if normalized.ClaimIDs[0] == "mutated" || normalized.Citations[0].ID == "mutated" {
		t.Fatal("Normalize retained mutable citation data")
	}

	invalid := normalized
	invalid.Citations = []VariantCitation{{ID: "citation", ClaimID: "missing", EvidenceID: "evidence-a"}}
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidVariantResult) {
		t.Fatalf("citation claim reference error = %v", err)
	}
	invalid = normalized
	invalid.Citations = []VariantCitation{{ID: "citation", ClaimID: "claim-a", EvidenceID: "missing"}}
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidVariantResult) {
		t.Fatalf("citation evidence reference error = %v", err)
	}
	invalid = normalized
	invalid.Citations = []VariantCitation{{ID: "citation", ClaimID: "claim-a", EvidenceID: "evidence-a"}, {ID: "citation", ClaimID: "claim-b", EvidenceID: "evidence-b"}}
	if err := invalid.Validate(); !errors.Is(err, ErrInvalidVariantResult) {
		t.Fatalf("duplicate citation error = %v", err)
	}
}

func TestVariantRunnerSanitizesMalformedCitationAndKeepsQualityIsolated(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	original := cloneCaseSet(cases)
	valid := func(request VariantExecutionRequest) VariantExecutionResult {
		return qualityResultForCase(request.Case, VariantConclusionPassed)
	}
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
			result := valid(request)
			result.Citations = []VariantCitation{{ID: "bad", ClaimID: "missing", EvidenceID: "eval-ctx-manifest"}}
			return result, nil
		})},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
			return valid(request), nil
		})},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
			return valid(request), nil
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
		if execution.Quality == nil {
			t.Fatalf("missing quality for %q", execution.VariantID)
		}
	}
	if byID["direct"].ErrorCode != "invalid_executor_result" || byID["direct"].Result.Status != VariantStatusFailed {
		t.Fatalf("malformed citation result = %#v", byID["direct"])
	}
	if byID["text"].Outcome != VariantOutcomeCompleted || byID["manu"].Outcome != VariantOutcomeCompleted {
		t.Fatalf("healthy variants blocked = %#v", byID)
	}
	if !reflect.DeepEqual(cases, original) {
		t.Fatal("runner mutated case input")
	}
	if byID["text"].Quality == byID["manu"].Quality || byID["text"].Quality.Evidence.Recall == byID["manu"].Quality.Evidence.Recall {
		t.Fatal("variant qualities share pointers")
	}
	*byID["text"].Quality.Evidence.Recall = 0
	if *byID["manu"].Quality.Evidence.Recall == 0 {
		t.Fatal("mutating one quality changed another variant")
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "missing") {
		t.Fatalf("malformed citation leaked: %s", encoded)
	}
}

func TestVariantRunnerAttachesQualityToFailedAndUnavailableExecutions(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	item := cloneCase(cases.Cases[0])
	item.Variants = append(item.Variants, EvaluationVariant{
		ID: "external-unavailable", Kind: VariantExternalContext, ToolIDs: []string{"filesystem-search"},
		ConfigurationID: "direct-read-only", Capabilities: []string{"external.read"}, Limitations: []string{"optional"},
	})
	cases = CaseSet{Version: Version, Cases: []EvaluationCase{item}}
	failed := VariantExecutorFunc(func(context.Context, VariantExecutionRequest) (VariantExecutionResult, error) {
		return VariantExecutionResult{}, errors.New("executor failure")
	})
	healthy := VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
		return qualityResultForCase(request.Case, VariantConclusionPassed), nil
	})
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: failed},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: healthy},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: healthy},
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
	for _, execution := range report.Cases[0].Executions {
		if execution.Quality == nil {
			t.Fatalf("missing quality for %q", execution.VariantID)
		}
		switch execution.VariantID {
		case "direct", "external-unavailable":
			if execution.Quality.Completed || execution.Quality.Abstention.Appropriate {
				t.Fatalf("ineligible execution gained quality success: %#v", execution)
			}
			if err := execution.Validate(); err != nil {
				t.Fatalf("failed/unavailable record invalid: %v", err)
			}
		}
	}
}

func TestVariantExecutionRecordRejectsQualityDisconnectedFromResult(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	healthy := VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
		return qualityResultForCase(request.Case, VariantConclusionPassed), nil
	})
	registry, err := NewVariantExecutorRegistry(
		VariantExecutorRegistration{Kind: VariantDirectSource, Executor: healthy},
		VariantExecutorRegistration{Kind: VariantTextRetrieval, Executor: healthy},
		VariantExecutorRegistration{Kind: VariantManuContext, Executor: healthy},
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
	base := report.Cases[0].Executions[0]
	tests := []struct {
		name   string
		mutate func(*VariantExecutionRecord)
	}{
		{
			name: "retrieved count",
			mutate: func(record *VariantExecutionRecord) {
				record.Quality.Evidence.Retrieved++
				record.Quality.Evidence.K++
				wrong := qualityRatePointer(0.5)
				record.Quality.Evidence.Precision = wrong
			},
		},
		{
			name: "citation total",
			mutate: func(record *VariantExecutionRecord) {
				record.Quality.Citations.Total++
				record.Quality.Citations.Rate = qualityRatePointer(0.5)
			},
		},
		{
			name: "recognized gaps",
			mutate: func(record *VariantExecutionRecord) {
				record.Result.GapIDs = nil
				record.Quality.Gaps.Recognized = 1
				record.Quality.Gaps.Recall = qualityRatePointer(1)
			},
		},
		{
			name:   "abstention actual",
			mutate: func(record *VariantExecutionRecord) { record.Quality.Abstention.Actual = true },
		},
		{
			name:   "completion",
			mutate: func(record *VariantExecutionRecord) { record.Quality.Completed = false },
		},
		{
			name:   "abstention appropriate",
			mutate: func(record *VariantExecutionRecord) { record.Quality.Abstention.Appropriate = false },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := base
			record.Result = base.Result.Clone()
			quality := base.Quality.Clone()
			record.Quality = &quality
			test.mutate(&record)
			if err := record.Validate(); !errors.Is(err, ErrInvalidVariantResult) {
				t.Fatalf("adulterated record error = %v", err)
			}
		})
	}
}

func TestVariantQualityIsDeterministicAfterNormalization(t *testing.T) {
	cases := loadCurrentEvaluationFixture(t)
	reversed := cloneCaseSet(cases)
	for index := range reversed.Cases {
		variants := reversed.Cases[index].Variants
		for left, right := 0, len(variants)-1; left < right; left, right = left+1, right-1 {
			variants[left], variants[right] = variants[right], variants[left]
		}
	}
	executor := VariantExecutorFunc(func(_ context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
		return qualityResultForCase(request.Case, VariantConclusionPassed), nil
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
		t.Fatalf("reports differ:\n%s\n%s", firstJSON, secondJSON)
	}
}

func TestVariantQualityRejectsInvalidRatesAndRequiredCriterionCoherence(t *testing.T) {
	one := 1.0
	quality := VariantQuality{
		Correct:                true,
		Evidence:               VariantEvidenceQuality{Expected: 1, Retrieved: 1, Relevant: 1, K: 1, Recall: &one, Precision: &one},
		Citations:              VariantCitationQuality{Total: 0, Valid: 0},
		Gaps:                   VariantGapQuality{Expected: 0, Recognized: 0},
		RequiredCriteriaPassed: true,
		Criteria:               []VariantCriterionEvaluation{{ID: "criterion", Kind: CriterionCorrectness, Required: true, Evaluated: true, Passed: true, Reason: qualityReasonCorrectness}},
	}
	if err := quality.Validate(); err != nil {
		t.Fatalf("valid quality rejected: %v", err)
	}
	invalids := []VariantQuality{
		func() VariantQuality {
			value := quality.Clone()
			value.Evidence.Recall = qualityRatePointer(math.NaN())
			return value
		}(),
		func() VariantQuality {
			value := quality.Clone()
			value.Evidence.K = 2
			return value
		}(),
		func() VariantQuality {
			value := quality.Clone()
			value.RequiredCriteriaPassed = false
			return value
		}(),
		func() VariantQuality {
			value := quality.Clone()
			wrong := 0.5
			value.Evidence.Recall = &wrong
			return value
		}(),
		func() VariantQuality {
			value := quality.Clone()
			value.Criteria[0].Passed = false
			return value
		}(),
	}
	for index, value := range invalids {
		if err := value.Validate(); !errors.Is(err, ErrInvalidVariantResult) {
			t.Fatalf("invalid quality %d error = %v", index, err)
		}
	}
}

func qualityCaseWithAllCriteria(t *testing.T) EvaluationCase {
	t.Helper()
	item := cloneCase(loadCurrentEvaluationFixture(t).Cases[0])
	item.Criteria.Items = append(item.Criteria.Items,
		SuccessCriterion{ID: "completion", Kind: CriterionCompletion, Description: "completion", Required: true},
		SuccessCriterion{ID: "evidence-all", Kind: CriterionEvidence, Description: "all evidence", Required: true},
		SuccessCriterion{ID: "citation-all", Kind: CriterionCitation, Description: "all citations", Required: true},
		SuccessCriterion{ID: "gap-all", Kind: CriterionGap, Description: "all gaps", Required: true},
		SuccessCriterion{ID: "evidence", Kind: CriterionEvidence, Description: "evidence", Required: true, EvidenceIDs: []string{"eval-ctx-manifest"}},
		SuccessCriterion{ID: "citation", Kind: CriterionCitation, Description: "citation", Required: true, EvidenceIDs: []string{"eval-ctx-manifest"}},
	)
	if err := item.Validate(); err != nil {
		t.Fatalf("quality test case invalid: %v", err)
	}
	return item
}

func qualityResultForCase(item EvaluationCase, conclusion VariantConclusion) VariantExecutionResult {
	return VariantExecutionResult{
		Status:       VariantStatusCompleted,
		Conclusion:   conclusion,
		OutputDigest: variantTestDigest("quality-" + item.CaseID),
		ClaimIDs:     append([]string(nil), item.ReferenceAnswer.ClaimIDs...),
		EvidenceIDs:  []string{"eval-ctx-manifest"},
		GapIDs:       append([]string(nil), item.ReferenceAnswer.GapIDs...),
		Citations: []VariantCitation{{
			ID: "quality-citation", ClaimID: "eval-ctx-claim", EvidenceID: "eval-ctx-manifest",
		}},
	}
}

func qualityRatePointer(value float64) *float64 { return &value }
