package cli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
)

func TestValidateVariantEvaluationInputsRejectsInvalidBoundaryValues(t *testing.T) {
	t.Parallel()
	cases := loadEvaluationSeedCases(t)
	validRoot := "/workspace/manu"
	validConfiguration := config.Default()

	tests := []struct {
		name string
		ctx  context.Context
		root string
		set  func(*evaluation.CaseSet, *config.Config)
		want error
	}{
		{name: "nil context", root: validRoot, want: ErrVariantEvaluationValidation},
		{name: "relative root", ctx: context.Background(), root: "relative", want: ErrVariantEvaluationValidation},
		{name: "root whitespace", ctx: context.Background(), root: validRoot + " ", want: ErrVariantEvaluationValidation},
		{name: "legacy cases", ctx: context.Background(), root: validRoot, set: func(value *evaluation.CaseSet, _ *config.Config) { value.Version = evaluation.LegacyVersion }, want: ErrVariantEvaluationValidation},
		{name: "invalid configuration", ctx: context.Background(), root: validRoot, set: func(_ *evaluation.CaseSet, value *config.Config) { value.Organization.ID = "" }, want: ErrVariantEvaluationValidation},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			inputCases := cases
			inputConfiguration := validConfiguration
			if test.set != nil {
				test.set(&inputCases, &inputConfiguration)
			}
			err := validateVariantEvaluationInputs(test.ctx, test.root, inputCases, inputConfiguration)
			if !errors.Is(err, test.want) {
				t.Fatalf("validateVariantEvaluationInputs() error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), test.root) {
				t.Fatalf("validation error leaked root: %v", err)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := validateVariantEvaluationInputs(cancelled, validRoot, cases, validConfiguration); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled validation error = %v, want context.Canceled", err)
	}
}

func TestConfigForEvaluationCorpusDerivesSyntheticOrganizationWithoutMutation(t *testing.T) {
	t.Parallel()
	corpus := loadEvaluationSeedCorpus(t)
	input := config.Default()
	input.Organization.ID = "caller-organization"
	input.Organization.Name = "Caller organization"
	original := input

	derived, err := configForEvaluationCorpus(input, corpus)
	if err != nil {
		t.Fatalf("configForEvaluationCorpus() error = %v", err)
	}
	if derived.Organization.ID != "evaluation-fixture" {
		t.Fatalf("derived organization ID = %q, want evaluation-fixture", derived.Organization.ID)
	}
	wantName := corpus.Snapshots[0].Bundle.Manifest.Organization.Name
	if derived.Organization.Name != wantName {
		t.Fatalf("derived organization name = %q, want %q", derived.Organization.Name, wantName)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatal("configForEvaluationCorpus mutated caller configuration")
	}
	if err := derived.Validate(); err != nil {
		t.Fatalf("derived configuration is invalid: %v", err)
	}
}

func TestConfigForEvaluationCorpusRejectsMixedOrganizationCorpus(t *testing.T) {
	t.Parallel()
	corpus := loadEvaluationSeedCorpus(t)
	corpus.Snapshots[0].Bundle.Manifest.Organization.ID = "other-organization"
	if _, err := configForEvaluationCorpus(config.Default(), corpus); !errors.Is(err, ErrVariantEvaluationValidation) {
		t.Fatalf("mixed organization error = %v, want validation sentinel", err)
	}
}

func TestUnavailableTextRetrievalExecutorReturnsControlledResult(t *testing.T) {
	t.Parallel()
	cases := loadEvaluationSeedCases(t)
	request := textVariantRequest(t, cases.Cases[0])
	result, err := unavailableTextRetrievalExecutor().Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("unavailable text executor error = %v", err)
	}
	if result.Status != evaluation.VariantStatusUnavailable || result.Conclusion != evaluation.VariantConclusionNotEvaluated {
		t.Fatalf("unavailable result = %#v", result)
	}
	if result.OutputDigest != "" || len(result.EvidenceIDs) != 0 || len(result.ClaimIDs) != 0 || len(result.GapIDs) != 0 || len(result.Citations) != 0 {
		t.Fatalf("unavailable result exposed execution content = %#v", result)
	}
	if !reflect.DeepEqual(result.Limitations, []string{"text_retrieval_not_executed"}) {
		t.Fatalf("unavailable limitations = %#v", result.Limitations)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("unavailable result validation error = %v", err)
	}
}

func TestVariantEvaluationContextKeyIsDeterministicAndContentFree(t *testing.T) {
	t.Parallel()
	corpus := loadEvaluationSeedCorpus(t)
	first, err := variantEvaluationContextKey(corpus)
	if err != nil {
		t.Fatalf("first context key error = %v", err)
	}
	second, err := variantEvaluationContextKey(corpus)
	if err != nil {
		t.Fatalf("second context key error = %v", err)
	}
	if !reflect.DeepEqual(first, second) || len(first) != 32 {
		t.Fatalf("context key is not deterministic 32-byte material: %x/%x", first, second)
	}
	if strings.Contains(string(first), "public class") || strings.Contains(string(first), "secret=") {
		t.Fatal("context key contained source content")
	}
}

func loadEvaluationSeedCases(t *testing.T) evaluation.CaseSet {
	t.Helper()
	cases, err := evaluation.LoadCases("../../testdata/evaluation/context-efficiency.v1alpha2.json")
	if err != nil {
		t.Fatalf("load evaluation cases: %v", err)
	}
	return cases
}

func textVariantRequest(t *testing.T, item evaluation.EvaluationCase) evaluation.VariantExecutionRequest {
	t.Helper()
	var variant evaluation.EvaluationVariant
	for _, candidate := range item.Variants {
		if candidate.Kind == evaluation.VariantTextRetrieval {
			variant = candidate
			break
		}
	}
	if variant.ID == "" {
		t.Fatal("text variant not found")
	}
	var configuration evaluation.EvaluationConfiguration
	for _, candidate := range item.Configurations {
		if candidate.ID == variant.ConfigurationID {
			configuration = candidate
			break
		}
	}
	if configuration.ID == "" {
		t.Fatal("text configuration not found")
	}
	toolByID := make(map[string]evaluation.EvaluationTool, len(item.Tools))
	for _, tool := range item.Tools {
		toolByID[tool.ID] = tool
	}
	tools := make([]evaluation.EvaluationTool, 0, len(variant.ToolIDs))
	for _, id := range variant.ToolIDs {
		tool, ok := toolByID[id]
		if !ok {
			t.Fatalf("text tool %q not found", id)
		}
		tools = append(tools, tool)
	}
	request := evaluation.VariantExecutionRequest{
		Case: item, Task: item.Task,
		CorpusID: item.CorpusID, CorpusRevision: item.CorpusRevision,
		SourceID: item.SourceID, SourceRevision: item.SourceRevision,
		Policy: item.Policy, Variant: variant, Tools: tools, Configuration: configuration,
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("text request validation error = %v", err)
	}
	return request
}
