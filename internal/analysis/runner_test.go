package analysis_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/analyzer/generic"
	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
	"github.com/pedrogpaulino/manu/internal/state"
)

func TestRunnerFallbackAndSpecialization(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, root, "Sample.java")
	copyFixture(t, root, "Hostile.java")
	copyFixture(t, root, "sample.xml")
	copyFixture(t, root, "notes.txt")
	if err := os.WriteFile(filepath.Join(root, "payload.bin"), []byte{0, 1, 2, 3, 4, 5}, 0o600); err != nil {
		t.Fatal(err)
	}

	registry, err := analysis.NewRegistry(generic.New(), java.New(), wso2.New())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), analysis.Config{
		Source: contract.Source{ID: "fixture-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
		Limits: source.Limits{MaxConcurrency: 2},
	})
	if err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result validation error = %v", err)
	}
	if len(result.Artifacts) != 5 {
		t.Fatalf("artifacts = %d, want 5", len(result.Artifacts))
	}
	seenIDs := make(map[string]bool, len(result.Contributions))
	hasGeneric, hasJava, hasWSO2 := false, false, false
	for _, contribution := range result.Contributions {
		if seenIDs[contribution.ID] {
			t.Fatalf("duplicate contribution id %q", contribution.ID)
		}
		seenIDs[contribution.ID] = true
		switch contribution.AnalyzerID {
		case generic.AnalyzerID:
			hasGeneric = true
		case java.AnalyzerID:
			hasJava = true
		case wso2.AnalyzerID:
			hasWSO2 = true
		}
		if contribution.Locator.Path == "" {
			t.Fatalf("contribution %q has no locator path", contribution.ID)
		}
	}
	if !hasGeneric || !hasJava || !hasWSO2 {
		t.Fatalf("analyzers = generic:%v java:%v wso2:%v", hasGeneric, hasJava, hasWSO2)
	}
	if !result.IsPartial() {
		t.Fatal("specialized lexical gaps were not exposed as partial coverage")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "token=secret") || strings.Contains(string(encoded), "bounded text fixture") {
		t.Fatalf("result leaked source literal: %s", encoded)
	}
	for _, contribution := range result.Contributions {
		if contribution.Locator.Path != "Hostile.java" {
			continue
		}
		if strings.Contains(string(contribution.Value), "Fake") {
			t.Fatalf("hostile comment/string symbol was emitted: %s", contribution.Value)
		}
	}
}

func TestRunnerIsolatesAnalyzerFailure(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", "fixture\n")
	registry, err := analysis.NewRegistry(generic.New(), failingAnalyzer{})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), analysis.Config{
		Source: contract.Source{ID: "failure-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
	})
	if err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	if len(result.Contributions) == 0 {
		t.Fatal("fallback contributions were lost after specialized failure")
	}
	foundFailure := false
	for _, failure := range result.Manifest.Failures {
		if failure.AnalyzerID == "failing" {
			foundFailure = true
		}
	}
	if !foundFailure {
		t.Fatal("isolated analyzer failure was not recorded")
	}
	if !result.IsPartial() {
		t.Fatal("failure did not make result partial")
	}
}

func TestRunnerDiscardsOutputWhenSourceChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "change.txt")
	writeFixture(t, root, "change.txt", "before\n")
	registry, err := analysis.NewRegistry(mutatingAnalyzer{path: path})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), analysis.Config{
		Source: contract.Source{ID: "changed-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
	})
	if err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	if len(result.Contributions) != 0 {
		t.Fatalf("changed source retained %d contributions", len(result.Contributions))
	}
	found := false
	for _, failure := range result.Manifest.Failures {
		if failure.Code == "source_changed_during_analysis" {
			found = true
		}
	}
	if !found {
		t.Fatal("source_changed_during_analysis failure was not recorded")
	}
}

func TestRunnerRejectsOutputSymlinkIntoSource(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "input.txt", "fixture\n")
	outside := t.TempDir()
	outputLink := filepath.Join(outside, "output")
	if err := os.Symlink(root, outputLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	registry, err := analysis.NewRegistry(generic.New())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), analysis.Config{
		Source: contract.Source{ID: "symlink-output-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
		Output: outputLink,
	})
	if !errors.Is(err, analysis.ErrInvalidRequest) {
		t.Fatalf("Runner.Run() error = %v, want ErrInvalidRequest", err)
	}
}

func TestRunnerAnalyzesCARMembersWithoutExtraction(t *testing.T) {
	root := t.TempDir()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	header := &zip.FileHeader{Name: "synapse/proxy.xml", Method: zip.Deflate}
	member, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(`<proxy name="InventoryProxy" targetEndpoint="inventoryEndpoint"/>`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bundle.car"), archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := analysis.NewRegistry(generic.New(), wso2.New())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), analysis.Config{
		Source: contract.Source{ID: "car-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
	})
	if err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	foundMember := false
	for _, contribution := range result.Contributions {
		if contribution.AnalyzerID == wso2.AnalyzerID && contribution.Locator.Member == "synapse/proxy.xml" {
			foundMember = true
		}
	}
	if !foundMember {
		t.Fatal("CAR member contribution with a member locator was not found")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "bundle.car" {
		t.Fatalf("CAR analysis created extracted files: %#v", entries)
	}
}

func TestRegistryRejectsIncompatibleDescriptor(t *testing.T) {
	for _, analyzer := range []incompatibleAnalyzer{{version: "v0"}, {version: ""}} {
		registry := &analysis.Registry{}
		err := registry.Register(analyzer)
		if !errors.Is(err, analysis.ErrInvalidAnalyzer) {
			t.Fatalf("Register() error = %v, want invalid analyzer", err)
		}
	}
}

func TestRunnerReusesUnchangedArtifactsAndInvalidatesDirectDependents(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, filepath.Join(root, "pkg"), "A.java", "package pkg;\nimport pkg.B;\nclass A extends B {}\n")
	writeFixture(t, filepath.Join(root, "pkg"), "B.java", "package pkg;\nclass B {}\n")
	writeFixture(t, root, "C.txt", "independent\n")
	output := t.TempDir()
	counter := &atomic.Int32{}
	registry, err := analysis.NewRegistry(generic.New(), java.New(), countingAnalyzer{counter: counter})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	config := analysis.Config{
		Source: contract.Source{ID: "incremental-source", Name: "incremental", Type: "filesystem"},
		Root:   root,
		Output: output,
	}
	first, err := runner.Run(context.Background(), config)
	if err != nil {
		t.Fatalf("first run error = %v", err)
	}
	if first.Manifest.Execution.Metrics.Discovered != 3 || first.Manifest.Execution.Metrics.Reprocessed != 3 {
		t.Fatalf("first metrics = %#v, want discovered/reprocessed 3", first.Manifest.Execution.Metrics)
	}
	if counter.Load() != 3 {
		t.Fatalf("first counter = %d, want 3", counter.Load())
	}
	stored, err := state.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	foundDependency := false
	for _, dependency := range stored.Snapshot().Dependencies {
		if dependency.FromPath == "pkg/A.java" && dependency.ToPath == "pkg/B.java" {
			foundDependency = true
		}
	}
	if !foundDependency {
		t.Fatalf("stored dependencies = %#v, want pkg/A.java -> pkg/B.java", stored.Snapshot().Dependencies)
	}

	second, err := runner.Run(context.Background(), config)
	if err != nil {
		t.Fatalf("second run error = %v", err)
	}
	if second.Manifest.Execution.Metrics.Reused != 3 || second.Manifest.Execution.Metrics.Reprocessed != 0 {
		t.Fatalf("second metrics = %#v, want reused 3 and reprocessed 0", second.Manifest.Execution.Metrics)
	}
	if !contract.EquivalentFacts(first, second) {
		t.Fatal("unchanged run changed factual contributions, coverage, or gaps")
	}
	if counter.Load() != 3 {
		t.Fatalf("second counter = %d, unchanged cache invoked analyzer", counter.Load())
	}

	writeFixture(t, filepath.Join(root, "pkg"), "B.java", "package pkg;\nclass B { int changed; }\n")
	third, err := runner.Run(context.Background(), config)
	if err != nil {
		t.Fatalf("third run error = %v", err)
	}
	if third.Manifest.Execution.Metrics.Reused != 1 || third.Manifest.Execution.Metrics.Reprocessed != 2 {
		t.Fatalf("third metrics = %#v, want reused independent C and reprocessed A/B", third.Manifest.Execution.Metrics)
	}
	if counter.Load() != 5 {
		t.Fatalf("third counter = %d, want two invalidated artifacts", counter.Load())
	}
	if err := os.Remove(filepath.Join(root, "pkg", "B.java")); err != nil {
		t.Fatal(err)
	}
	fourth, err := runner.Run(context.Background(), config)
	if err != nil {
		t.Fatalf("fourth run error = %v", err)
	}
	if fourth.Manifest.Execution.Metrics.Reused != 1 || fourth.Manifest.Execution.Metrics.Reprocessed != 1 {
		t.Fatalf("fourth metrics = %#v, want only dependent A reprocessed", fourth.Manifest.Execution.Metrics)
	}
	if counter.Load() != 6 {
		t.Fatalf("fourth counter = %d, want only dependent A reprocessed", counter.Load())
	}
}

func TestRunnerCacheKeyIncludesAnalyzerVersion(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "input.txt", "fixture\n")
	output := t.TempDir()
	counter := &atomic.Int32{}
	run := func(version string) contract.Result {
		registry, err := analysis.NewRegistry(countingAnalyzer{counter: counter, version: version})
		if err != nil {
			t.Fatal(err)
		}
		runner, err := analysis.NewRunner(registry)
		if err != nil {
			t.Fatal(err)
		}
		result, err := runner.Run(context.Background(), analysis.Config{
			Source: contract.Source{ID: "versioned-source", Name: "fixture", Type: "filesystem"},
			Root:   root,
			Output: output,
		})
		if err != nil {
			t.Fatalf("Run(%s) error = %v", version, err)
		}
		return result
	}
	first := run("1")
	if first.Manifest.Execution.Metrics.Reprocessed != 1 || counter.Load() != 1 {
		t.Fatalf("first metrics/counter = %#v/%d", first.Manifest.Execution.Metrics, counter.Load())
	}
	second := run("2")
	if second.Manifest.Execution.Metrics.Reused != 0 || second.Manifest.Execution.Metrics.Reprocessed != 1 || counter.Load() != 2 {
		t.Fatalf("version change reused incompatible state: metrics=%#v counter=%d", second.Manifest.Execution.Metrics, counter.Load())
	}
}

func TestRunnerTreatsCorruptStateAsSafeMiss(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "input.txt", "fixture\n")
	output := t.TempDir()
	counter := &atomic.Int32{}
	registry, err := analysis.NewRegistry(countingAnalyzer{counter: counter})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	config := analysis.Config{
		Source: contract.Source{ID: "corrupt-state-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
		Output: output,
	}
	if _, err := runner.Run(context.Background(), config); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(output, state.StateFileName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(context.Background(), config)
	if err != nil {
		t.Fatalf("safe-miss run error = %v", err)
	}
	if result.Manifest.Execution.Metrics.Reused != 0 || result.Manifest.Execution.Metrics.Reprocessed != 1 || counter.Load() != 2 {
		t.Fatalf("corrupt state was reused: metrics=%#v counter=%d", result.Manifest.Execution.Metrics, counter.Load())
	}
	if !strings.Contains(strings.Join(result.Manifest.Execution.Metrics.Limitations, "\n"), "state_unavailable: corrupt") {
		t.Fatalf("limitations = %#v, missing corrupt-state limitation", result.Manifest.Execution.Metrics.Limitations)
	}
	if err := os.WriteFile(filepath.Join(output, state.StateFileName), []byte(`{"version":"v0","contract_version":"v1alpha1","source_id":"corrupt-state-source","artifacts":[],"entries":[],"dependencies":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err = runner.Run(context.Background(), config)
	if err != nil {
		t.Fatalf("incompatible safe-miss run error = %v", err)
	}
	if result.Manifest.Execution.Metrics.Reused != 0 || result.Manifest.Execution.Metrics.Reprocessed != 1 || counter.Load() != 3 {
		t.Fatalf("incompatible state was reused: metrics=%#v counter=%d", result.Manifest.Execution.Metrics, counter.Load())
	}
	if !strings.Contains(strings.Join(result.Manifest.Execution.Metrics.Limitations, "\n"), "state_unavailable: incompatible_version") {
		t.Fatalf("limitations = %#v, missing incompatible-state limitation", result.Manifest.Execution.Metrics.Limitations)
	}
}

func writeFixture(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

type countingAnalyzer struct {
	counter *atomic.Int32
	version string
}

func (c countingAnalyzer) Descriptor() analysis.Descriptor {
	version := c.version
	if version == "" {
		version = "1"
	}
	return analysis.Descriptor{
		ID:              "counting",
		Version:         version,
		Method:          "counter-v1",
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeAny},
	}
}

func (c countingAnalyzer) Analyze(ctx context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if err := ctx.Err(); err != nil {
		return analysis.Output{}, err
	}
	c.counter.Add(1)
	contribution, err := analysis.NewContribution(
		input,
		c.Descriptor(),
		"count:"+input.Artifact.Path,
		"test.counted",
		contract.Locator{Path: input.Artifact.Path},
		map[string]string{"path": input.Artifact.Path},
	)
	if err != nil {
		return analysis.Output{}, err
	}
	return analysis.Output{Contributions: []contract.Contribution{contribution}}, nil
}

func copyFixture(t *testing.T, root, name string) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	fixturePath := filepath.Join(filepath.Dir(currentFile), "..", "..", "testdata", "analyzers", name)
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type failingAnalyzer struct{}

func (failingAnalyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              "failing",
		Version:         "1",
		Method:          "failure-v1",
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeAny},
	}
}

func (failingAnalyzer) Analyze(context.Context, analysis.ArtifactInput) (analysis.Output, error) {
	return analysis.Output{}, errors.New("fixture analyzer failed")
}

type mutatingAnalyzer struct {
	path string
}

func (m mutatingAnalyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              "mutating",
		Version:         "1",
		Method:          "mutating-v1",
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeAny},
	}
}

func (m mutatingAnalyzer) Analyze(_ context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if input.SourceArtifact.Path == m.path {
		if err := os.WriteFile(m.path, []byte("after\n"), 0o600); err != nil {
			return analysis.Output{}, err
		}
	}
	contribution, err := analysis.NewContribution(
		input,
		m.Descriptor(),
		"mutating:"+input.Artifact.Path,
		"fixture.observation",
		contract.Locator{Path: input.Artifact.Path},
		map[string]any{"value": "before"},
	)
	if err != nil {
		return analysis.Output{}, err
	}
	return analysis.Output{Contributions: []contract.Contribution{contribution}}, nil
}

type incompatibleAnalyzer struct {
	version string
}

func (in incompatibleAnalyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{ID: "incompatible", Version: "1", Method: "old", ContractVersion: in.version}
}

func (incompatibleAnalyzer) Analyze(context.Context, analysis.ArtifactInput) (analysis.Output, error) {
	return analysis.Output{}, nil
}
