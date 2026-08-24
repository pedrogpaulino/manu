package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestRunVersionPreservesHumanOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(version) = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "manu dev ") {
		t.Fatalf("version output = %q, want existing human prefix", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("version stderr = %q, want empty", stderr.String())
	}
}

func TestRunVersionJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version", "--json"}, &stdout, &stderr)
	if code != ExitSuccess {
		t.Fatalf("Run(version --json) = %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"contract_version": "v1alpha1"`) {
		t.Fatalf("version JSON = %q, missing contract version", stdout.String())
	}
}

func TestRunUsageErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "empty root", args: nil},
		{name: "unknown command", args: []string{"unknown"}},
		{name: "missing analyze output", args: []string{"analyze", "--root", t.TempDir()}},
		{name: "unknown inspect flag", args: []string{"inspect", "--wat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("Run(%v) = %d, want %d; stderr=%q", tt.args, code, ExitUsage, stderr.String())
			}
		})
	}
}

func TestRunAnalyzeAndInspect(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(t.TempDir(), "result")
	var analyzeOut, analyzeErr bytes.Buffer
	code := Run([]string{"analyze", "--root", root, "--output", output}, &analyzeOut, &analyzeErr)
	if code != ExitSuccess {
		t.Fatalf("Run(analyze) = %d, want %d; stdout=%q stderr=%q", code, ExitSuccess, analyzeOut.String(), analyzeErr.String())
	}
	for _, file := range []string{contract.ManifestFileName, contract.ArtifactsFileName, contract.ContributionsFileName} {
		if _, err := os.Stat(filepath.Join(output, file)); err != nil {
			t.Fatalf("analyze did not write %s: %v", file, err)
		}
	}
	if !strings.Contains(analyzeOut.String(), "analysis complete") {
		t.Fatalf("analyze output = %q, missing summary", analyzeOut.String())
	}

	var inspectOut, inspectErr bytes.Buffer
	code = Run([]string{"inspect", "--input", output, "--json"}, &inspectOut, &inspectErr)
	if code != ExitSuccess {
		t.Fatalf("Run(inspect) = %d, want %d; stdout=%q stderr=%q", code, ExitSuccess, inspectOut.String(), inspectErr.String())
	}
	if !strings.Contains(inspectOut.String(), `"contract_version": "v1alpha1"`) {
		t.Fatalf("inspect JSON = %q, missing contract version", inspectOut.String())
	}
}

func TestRunAnalyzeSelectsPythonWithCanonicalFallbackAndExistingAnalyzers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "module.py"), []byte("class Invoice:\n    def total(self):\n        return 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Sample.java"), []byte("package sample;\nclass Sample {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sample.xml"), []byte("<proxy name=\"InventoryProxy\"><endpoint uri=\"https://example.test/api\"/></proxy>\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "result")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"analyze", "--root", root, "--output", output}, &stdout, &stderr); code != ExitPartial {
		t.Fatalf("Run(analyze) = %d, want partial; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	result, err := contract.ReadResult(context.Background(), output)
	if err != nil {
		t.Fatalf("ReadResult() error = %v", err)
	}
	pythonArtifact := false
	for _, artifact := range result.Artifacts {
		if artifact.Path == "module.py" {
			pythonArtifact = artifact.Type == analysis.ArtifactTypePython
			break
		}
	}
	if !pythonArtifact {
		t.Fatalf("Python artifact type was not persisted: %#v", result.Artifacts)
	}
	seenAnalyzers := make(map[string]bool)
	for _, contribution := range result.Contributions {
		seenAnalyzers[contribution.AnalyzerID] = true
	}
	for _, analyzerID := range []string{"generic", "java", "python", "wso2"} {
		if !seenAnalyzers[analyzerID] {
			t.Fatalf("analyzer %q did not contribute; seen = %#v", analyzerID, seenAnalyzers)
		}
	}
}

func TestRunBenchmarkReportsScenariosAndMetrics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Sample.java"), []byte("package sample;\nclass Sample {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "benchmark")
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"benchmark",
		"--root", root,
		"--output", output,
		"--source-id", "cli-benchmark-source",
		"--json",
	}, &stdout, &stderr)
	if code != ExitPartial {
		t.Fatalf("Run(benchmark) = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	for _, name := range []string{"first_analysis", "repeat_unchanged", "localized_update"} {
		if !strings.Contains(stdout.String(), `"name": "`+name+`"`) {
			t.Fatalf("benchmark JSON = %q, missing scenario %s", stdout.String(), name)
		}
	}
	if !strings.Contains(stdout.String(), `"repeat_equivalent_facts": true`) ||
		!strings.Contains(stdout.String(), `"discovery_nanos"`) ||
		!strings.Contains(stdout.String(), `"max_rss_method"`) {
		t.Fatalf("benchmark JSON = %q, missing metrics/equivalence", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(output, "benchmark.json")); err != nil {
		t.Fatalf("benchmark report was not written: %v", err)
	}
}

func TestRunAnalyzeUsesRealAnalyzersAndIncrementalState(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Sample.java"), []byte("package sample;\nclass Sample {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	args := []string{"analyze", "--root", root, "--output", output, "--source-id", "cli-source"}
	var firstOut, firstErr bytes.Buffer
	if code := Run(args, &firstOut, &firstErr); code != ExitPartial {
		t.Fatalf("first analyze code = %d, want partial; stdout=%q stderr=%q", code, firstOut.String(), firstErr.String())
	}
	manifest, err := contract.ReadManifest(context.Background(), filepath.Join(output, contract.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Source.ID != "cli-source" {
		t.Fatalf("manifest source id = %q, want cli-source", manifest.Source.ID)
	}
	if manifest.Execution.Metrics.Discovered != 1 || manifest.Execution.Metrics.Reprocessed != 1 || manifest.Execution.Metrics.Reused != 0 {
		t.Fatalf("first metrics = %#v", manifest.Execution.Metrics)
	}
	if _, err := os.Stat(filepath.Join(output, "state.json")); err != nil {
		t.Fatalf("incremental state was not written: %v", err)
	}
	if !strings.Contains(firstOut.String(), "metrics: discovered=1 reused=0 reprocessed=1") {
		t.Fatalf("first summary = %q, missing metrics", firstOut.String())
	}

	var secondOut, secondErr bytes.Buffer
	if code := Run(args, &secondOut, &secondErr); code != ExitPartial {
		t.Fatalf("second analyze code = %d, want partial; stdout=%q stderr=%q", code, secondOut.String(), secondErr.String())
	}
	secondManifest, err := contract.ReadManifest(context.Background(), filepath.Join(output, contract.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if secondManifest.Execution.Metrics.Reused != 1 || secondManifest.Execution.Metrics.Reprocessed != 0 {
		t.Fatalf("second metrics = %#v", secondManifest.Execution.Metrics)
	}

	var inspectOut, inspectErr bytes.Buffer
	if code := Run([]string{"inspect", "--input", output}, &inspectOut, &inspectErr); code != ExitPartial {
		t.Fatalf("inspect code = %d, want partial; stdout=%q stderr=%q", code, inspectOut.String(), inspectErr.String())
	}
	if !strings.Contains(inspectOut.String(), "metrics: discovered=1 reused=1 reprocessed=0") {
		t.Fatalf("inspect summary = %q, missing reuse counters", inspectOut.String())
	}
	if !strings.Contains(inspectOut.String(), "limitation: cache_reconstructible_only") ||
		!strings.Contains(inspectOut.String(), "limitation: only_known_direct_dependencies_invalidated") {
		t.Fatalf("inspect summary = %q, missing incremental limitations", inspectOut.String())
	}
}

func TestRunContextCancellationProducesPartialResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := RunContext(ctx, nil, []string{"analyze", "--root", root, "--output", output}, &stdout, &stderr)
	if code != ExitPartial {
		t.Fatalf("RunContext(cancelled) = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	manifest, err := contract.ReadManifest(context.Background(), filepath.Join(output, contract.ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.Execution.Cancelled || manifest.Execution.Metrics.Limited == 0 {
		t.Fatalf("cancelled execution metadata = %#v", manifest.Execution)
	}
}

func TestRunInspectReturnsPartialCode(t *testing.T) {
	output := t.TempDir()
	result := contract.Result{
		Manifest: contract.Manifest{
			ContractVersion: contract.Version,
			ResultID:        "result-partial",
			Source: contract.Source{
				ID:   "source-partial",
				Name: "partial",
				Type: "filesystem",
			},
			Snapshot:  contract.Snapshot{ID: "snapshot-partial", SourceID: "source-partial"},
			Execution: contract.ExecutionMetadata{RunID: "run-partial"},
			Coverage: []contract.Coverage{
				{ID: "coverage-gap", Dimension: "relations", State: contract.CoverageNotSupported},
			},
			Gaps:     []contract.Gap{{ID: "gap", Code: "not_supported", Message: "not implemented"}},
			Failures: []contract.Failure{},
		},
		Artifacts:     []contract.Artifact{},
		Contributions: []contract.Contribution{},
	}
	if err := contract.WriteResult(context.Background(), output, result); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run([]string{"inspect", output}, &stdout, &stderr)
	if code != ExitPartial {
		t.Fatalf("Run(inspect partial) = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "gaps: 1") {
		t.Fatalf("inspect output = %q, missing gap count", stdout.String())
	}
}

func TestRunInspectTechnicalError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"inspect", t.TempDir()}, &stdout, &stderr)
	if code != ExitTechnical {
		t.Fatalf("Run(inspect missing result) = %d, want %d", code, ExitTechnical)
	}
	if stderr.Len() == 0 {
		t.Fatal("inspect technical error did not write stderr")
	}
}
