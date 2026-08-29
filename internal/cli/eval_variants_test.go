package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
)

const evalVariantFixturePath = "../../testdata/evaluation/context-efficiency.v1alpha2.json"

func TestVariantEvaluationRequested(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "long flag", args: []string{"--variants"}, want: true},
		{name: "explicit true", args: []string{"--variants=true"}, want: true},
		{name: "single dash", args: []string{"-variants"}, want: true},
		{name: "after terminator is positional", args: []string{"--", "--variants"}, want: false},
		{name: "legacy", args: []string{"--json"}, want: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := variantEvaluationRequested(test.args); got != test.want {
				t.Fatalf("variantEvaluationRequested(%v) = %t, want %t", test.args, got, test.want)
			}
		})
	}
}

func TestRunEvalVariantsRejectsInvalidAndMixedArguments(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	output := filepath.Join(root, "report.json")
	valid := []string{"--variants", "--root", root, "--cases", evalVariantFixturePath, "--output-raw", output, "--output-summary", filepath.Join(root, "summary.json")}
	tests := []struct {
		name string
		args []string
	}{
		{name: "root empty", args: replaceVariantArg(valid, "--root", "")},
		{name: "cases empty", args: replaceVariantArg(valid, "--cases", "")},
		{name: "raw empty", args: replaceVariantArg(valid, "--output-raw", "")},
		{name: "summary empty", args: replaceVariantArg(valid, "--output-summary", "")},
		{name: "same outputs", args: replaceVariantArg(valid, "--output-summary", output)},
		{name: "positional", args: append(append([]string(nil), valid...), "extra")},
		{name: "legacy json", args: append(append([]string(nil), valid...), "--json")},
		{name: "legacy top k", args: append(append([]string(nil), valid...), "--top-k", "5")},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			code := runEvalVariantsWithDependencies(context.Background(), test.args, &stdout, &stderr, func() (config.Config, error) {
				called = true
				return config.Default(), nil
			}, func(context.Context, string, evaluation.CaseSet, config.Config) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error) {
				called = true
				return evaluation.VariantRawReport{}, evaluation.VariantSummaryReport{}, nil
			})
			if code != ExitUsage {
				t.Fatalf("runEvalVariantsWithDependencies(%v) = %d, want %d; stderr=%q", test.args, code, ExitUsage, stderr.String())
			}
			if called {
				t.Fatal("invalid variant arguments reached a dependency")
			}
		})
	}
}

func TestRunEvalVariantsWritesCanonicalReportsAndContentFreeSummary(t *testing.T) {
	caseSet := loadVariantCLICases(t)
	raw, summary := validVariantCLIReports(t, caseSet)
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "raw.json")
	summaryPath := filepath.Join(directory, "summary.json")
	var stdout, stderr bytes.Buffer
	var gotRoot string
	var gotConfig config.Config
	code := runEvalVariantsWithDependencies(context.Background(), []string{
		"--variants", "--root", directory, "--cases", evalVariantFixturePath,
		"--output-raw", rawPath, "--output-summary", summaryPath,
	}, &stdout, &stderr, func() (config.Config, error) {
		configuration := config.Default()
		configuration.Retrieval.TopK = 3
		return configuration, nil
	}, func(_ context.Context, root string, cases evaluation.CaseSet, configuration config.Config) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error) {
		gotRoot = root
		gotConfig = configuration
		if cases.Version != evaluation.Version || len(cases.Cases) == 0 {
			t.Fatalf("runner received cases = %#v", cases)
		}
		return raw, summary, nil
	})
	if code != ExitSuccess {
		t.Fatalf("runEvalVariantsWithDependencies() = %d, want %d; stdout=%q stderr=%q", code, ExitSuccess, stdout.String(), stderr.String())
	}
	if gotRoot != directory || gotConfig.Retrieval.TopK != 3 {
		t.Fatalf("runner inputs root=%q config=%#v", gotRoot, gotConfig)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	for _, marker := range []string{"variant evaluation complete", "cases:", "raw digest:", "summary digest:"} {
		if !strings.Contains(stdout.String(), marker) {
			t.Fatalf("stdout = %q, missing %q", stdout.String(), marker)
		}
	}
	for _, forbidden := range []string{"competence_question", "BookingResource", "OrdersAPI", "source_revision"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("content-bearing marker %q leaked to stdout: %q", forbidden, stdout.String())
		}
	}
	rawData, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw report: %v", err)
	}
	summaryData, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary report: %v", err)
	}
	expectedRaw, err := evaluation.MarshalVariantRawReport(raw)
	if err != nil {
		t.Fatalf("marshal expected raw report: %v", err)
	}
	expectedSummary, err := evaluation.MarshalVariantSummaryReport(summary)
	if err != nil {
		t.Fatalf("marshal expected summary report: %v", err)
	}
	if !bytes.Equal(rawData, expectedRaw) || !bytes.Equal(summaryData, expectedSummary) {
		t.Fatalf("reports were not written in canonical form")
	}
}

func TestRunEvalVariantsDispatchesBeforeLegacyParserAndKeepsLegacyMode(t *testing.T) {
	caseSet := loadVariantCLICases(t)
	raw, summary := validVariantCLIReports(t, caseSet)
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "raw.json")
	summaryPath := filepath.Join(directory, "summary.json")
	previousVariantRunner := variantEvaluationRun
	t.Cleanup(func() { variantEvaluationRun = previousVariantRunner })
	variantCalled := false
	variantEvaluationRun = func(_ context.Context, root string, cases evaluation.CaseSet, _ config.Config) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error) {
		variantCalled = true
		if root != directory || cases.Version != evaluation.Version {
			t.Fatalf("dispatch inputs root=%q cases=%q", root, cases.Version)
		}
		return raw, summary, nil
	}
	var stdout, stderr bytes.Buffer
	code := runEvalWithDependencies(context.Background(), []string{
		"--variants", "--root", directory, "--cases", evalVariantFixturePath,
		"--output-raw", rawPath, "--output-summary", summaryPath,
	}, &stdout, &stderr, func(context.Context, evaluation.Config) (evaluation.Report, error) {
		t.Fatal("legacy runner was called for variant mode")
		return evaluation.Report{}, nil
	}, evaluation.RunLive, func() (config.Config, error) { return config.Default(), nil }, nil)
	if code != ExitSuccess || !variantCalled {
		t.Fatalf("dispatch code=%d variantCalled=%t stdout=%q stderr=%q", code, variantCalled, stdout.String(), stderr.String())
	}

	legacyCalled := false
	stdout.Reset()
	stderr.Reset()
	code = runEvalWithDependencies(context.Background(), []string{"--json"}, &stdout, &stderr, func(context.Context, evaluation.Config) (evaluation.Report, error) {
		legacyCalled = true
		return evaluation.Report{Version: evaluation.ReportVersion, Mode: evaluation.ModeSimulated, RunID: "legacy-test"}, nil
	}, evaluation.RunLive, config.Load, nil)
	if code != ExitSuccess || !legacyCalled {
		t.Fatalf("legacy compatibility code=%d legacyCalled=%t stdout=%q stderr=%q", code, legacyCalled, stdout.String(), stderr.String())
	}
}

func TestRunEvalVariantsSanitizesDependencyFailuresAndLeavesOutputsAbsent(t *testing.T) {
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "raw.json")
	summaryPath := filepath.Join(directory, "summary.json")
	args := []string{"--variants", "--root", directory, "--cases", evalVariantFixturePath, "--output-raw", rawPath, "--output-summary", summaryPath}
	secret := "secret=do-not-leak"
	var stdout, stderr bytes.Buffer
	code := runEvalVariantsWithDependencies(context.Background(), args, &stdout, &stderr, func() (config.Config, error) {
		return config.Config{}, errors.New(secret)
	}, nil)
	if code != ExitTechnical || strings.Contains(stderr.String(), secret) {
		t.Fatalf("loader failure code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw output after loader failure: %v", err)
	}
	if _, err := os.Stat(summaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("summary output after loader failure: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = runEvalVariantsWithDependencies(context.Background(), args, &stdout, &stderr, func() (config.Config, error) {
		return config.Default(), nil
	}, func(context.Context, string, evaluation.CaseSet, config.Config) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error) {
		return evaluation.VariantRawReport{}, evaluation.VariantSummaryReport{}, errors.New(secret)
	})
	if code != ExitTechnical || strings.Contains(stderr.String(), secret) {
		t.Fatalf("runner failure code=%d stderr=%q", code, stderr.String())
	}
	if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw output after runner failure: %v", err)
	}
	if _, err := os.Stat(summaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("summary output after runner failure: %v", err)
	}
}

func TestWriteVariantReportsAtomicStagesBothDocumentsBeforePublish(t *testing.T) {
	directory := t.TempDir()
	rawPath := filepath.Join(directory, "nested", "raw.json")
	summaryPath := filepath.Join(directory, "nested", "summary.json")
	if err := writeVariantReportsAtomic(rawPath, summaryPath, []byte("raw"), []byte("summary")); err == nil {
		t.Fatal("writeVariantReportsAtomic() unexpectedly created a missing output directory")
	}
	if _, err := os.Stat(rawPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("raw output after staging failure: %v", err)
	}
	if _, err := os.Stat(summaryPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("summary output after staging failure: %v", err)
	}

	rawPath = filepath.Join(directory, "raw.json")
	summaryPath = filepath.Join(directory, "summary.json")
	if err := writeVariantReportsAtomic(rawPath, summaryPath, []byte("raw"), []byte("summary")); err != nil {
		t.Fatalf("writeVariantReportsAtomic() error = %v", err)
	}
	rawData, _ := os.ReadFile(rawPath)
	summaryData, _ := os.ReadFile(summaryPath)
	if string(rawData) != "raw" || string(summaryData) != "summary" {
		t.Fatalf("published outputs raw=%q summary=%q", rawData, summaryData)
	}
}

func loadVariantCLICases(t *testing.T) evaluation.CaseSet {
	t.Helper()
	cases, err := evaluation.LoadCases(evalVariantFixturePath)
	if err != nil {
		t.Fatalf("evaluation.LoadCases() error = %v", err)
	}
	return cases
}

func validVariantCLIReports(t *testing.T, cases evaluation.CaseSet) (evaluation.VariantRawReport, evaluation.VariantSummaryReport) {
	t.Helper()
	executor := evaluation.VariantExecutorFunc(func(context.Context, evaluation.VariantExecutionRequest) (evaluation.VariantExecutionResult, error) {
		return evaluation.VariantExecutionResult{
			Version:      evaluation.VariantExecutionVersion,
			Status:       evaluation.VariantStatusCompleted,
			Conclusion:   evaluation.VariantConclusionPassed,
			OutputDigest: strings.Repeat("a", 64),
		}, nil
	})
	registrations := []evaluation.VariantExecutorRegistration{
		{Kind: evaluation.VariantDirectSource, Executor: executor},
		{Kind: evaluation.VariantTextRetrieval, Executor: executor},
		{Kind: evaluation.VariantManuContext, Executor: executor},
	}
	for _, item := range cases.Cases {
		for _, variant := range item.Variants {
			if variant.Kind == evaluation.VariantExternalContext {
				registrations = append(registrations, evaluation.VariantExecutorRegistration{VariantID: variant.ID, Kind: variant.Kind, Executor: executor})
			}
		}
	}
	registry, err := evaluation.NewVariantExecutorRegistry(registrations...)
	if err != nil {
		t.Fatalf("evaluation.NewVariantExecutorRegistry() error = %v", err)
	}
	runner, err := evaluation.NewVariantRunner(registry)
	if err != nil {
		t.Fatalf("evaluation.NewVariantRunner() error = %v", err)
	}
	execution, err := runner.Run(context.Background(), cases)
	if err != nil {
		t.Fatalf("VariantRunner.Run() error = %v", err)
	}
	tools := make(map[string]evaluation.EvaluationComponent)
	for _, item := range cases.Cases {
		for _, tool := range item.Tools {
			tools[tool.ID] = evaluation.EvaluationComponent{ID: tool.ID, Version: tool.Version}
		}
	}
	toolComponents := make([]evaluation.EvaluationComponent, 0, len(tools))
	for _, tool := range tools {
		toolComponents = append(toolComponents, tool)
	}
	metadata := evaluation.VariantReportMetadata{
		RunID:         "cli-test-run",
		Agent:         evaluation.EvaluationComponent{ID: "cli-test-agent", Version: "v1"},
		Model:         evaluation.EvaluationComponent{ID: "cli-test-model", Version: "v1"},
		ContextServer: evaluation.EvaluationComponent{ID: "cli-test-context", Version: "v1", Digest: testVariantCLIDigest("context")},
		Frontends:     []evaluation.EvaluationComponent{{ID: "cli-test-frontend", Version: "v1", Digest: testVariantCLIDigest("frontend")}},
		Rules:         []evaluation.EvaluationComponent{{ID: "cli-test-rules", Version: "v1", Digest: testVariantCLIDigest("rules")}},
		Retrieval:     evaluation.EvaluationConfiguration{ID: "cli-test-retrieval", Version: "v1", Settings: map[string]string{"top_k": "5"}},
		Tools:         toolComponents,
		Limitations:   []string{"test-only report"},
	}
	raw, summary, err := evaluation.BuildVariantReports(cases, execution, metadata)
	if err != nil {
		t.Fatalf("evaluation.BuildVariantReports() error = %v", err)
	}
	return raw, summary
}

func testVariantCLIDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func replaceVariantArg(args []string, flagName, value string) []string {
	result := append([]string(nil), args...)
	for index := range result {
		if result[index] == flagName && index+1 < len(result) {
			result[index+1] = value
			return result
		}
	}
	return result
}
