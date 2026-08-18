package benchmark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

func TestRunReportsThreeScenariosAndEquivalentFacts(t *testing.T) {
	root := t.TempDir()
	javaPath := filepath.Join(root, "Sample.java")
	if err := os.WriteFile(javaPath, []byte("package sample;\nclass Sample {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := metadataDigest(root)
	if err != nil {
		t.Fatal(err)
	}

	report, err := Run(context.Background(), Config{
		Source: contract.Source{
			ID:   "fixture-source",
			Name: "fixture",
			Type: "filesystem",
		},
		Root:   root,
		Output: filepath.Join(t.TempDir(), "benchmark"),
		Limits: sourceLimitsForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 3 {
		t.Fatalf("scenario count = %d, want 3", len(report.Scenarios))
	}
	if !report.RepeatEquivalentFacts {
		t.Fatal("repeat scenario did not preserve EquivalentFacts")
	}
	if !report.Integrity.Unchanged {
		t.Fatalf("source integrity = %#v, want unchanged", report.Integrity)
	}
	after, err := metadataDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("source metadata digest changed: before=%s after=%s", before, after)
	}
	for _, scenario := range report.Scenarios {
		if scenario.Metrics.Durations.TotalNanos <= 0 {
			t.Fatalf("%s total duration = %d, want positive", scenario.Name, scenario.Metrics.Durations.TotalNanos)
		}
		if scenario.Metrics.Durations.DiscoveryNanos <= 0 || scenario.Metrics.Durations.AnalysisNanos <= 0 {
			t.Fatalf("%s stage durations = %#v, want discovery and analysis", scenario.Name, scenario.Metrics.Durations)
		}
		if scenario.Metrics.OutputBytes <= 0 || scenario.Metrics.PersistedVolumeBytes <= 0 {
			t.Fatalf("%s output metrics = %#v, want positive", scenario.Name, scenario.Metrics)
		}
	}
	if !strings.Contains(report.Configuration.OverlayMethod, "temporary") {
		t.Fatalf("overlay method = %q, want temporary staging", report.Configuration.OverlayMethod)
	}
}

func TestRunRejectsOutputInsideSource(t *testing.T) {
	root := t.TempDir()
	if _, err := Run(context.Background(), Config{Root: root, Output: filepath.Join(root, "out")}); err == nil {
		t.Fatal("Run() accepted output inside source")
	}
}

func TestRunRejectsOutputSymlinkIntoSourceBeforeWriting(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkParent := t.TempDir()
	outputLink := filepath.Join(linkParent, "output")
	if err := os.Symlink(root, outputLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Run(context.Background(), Config{Root: root, Output: outputLink, Limits: sourceLimitsForTest()}); err == nil {
		t.Fatal("Run() accepted output symlink into source")
	}
	if _, err := os.Stat(filepath.Join(root, reportFileName)); !os.IsNotExist(err) {
		t.Fatalf("source received benchmark report, stat error = %v", err)
	}
}

func TestRunRequiresNewOrEmptyOutput(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := t.TempDir()
	sentinel := filepath.Join(output, "sentinel")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Config{Root: root, Output: output, Limits: sourceLimitsForTest()}); err == nil {
		t.Fatal("Run() accepted a non-empty output workspace")
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("non-empty workspace was modified: %q", data)
	}
}

func TestRunRejectsUpdatePathAbsentFromFirstArtifacts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), Config{
		Root:   root,
		Output: filepath.Join(t.TempDir(), "benchmark"),
		Limits: sourceLimitsForTest(),
		Update: UpdateConfig{Path: "missing.txt"},
	})
	if err == nil {
		t.Fatal("Run() accepted an update path absent from first artifacts")
	}
}

func TestMutateOverlayFileRespectsStreamingFileLimit(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.txt")
	stagedPath := filepath.Join(directory, "staged.txt")
	content := []byte("0123456789\n")
	if err := os.WriteFile(sourcePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mutateOverlayFile(sourcePath, stagedPath, "m", int64(len(content))); err == nil {
		t.Fatal("mutateOverlayFile accepted a marker beyond MaxFileBytes")
	}
	if err := mutateOverlayFile(sourcePath, stagedPath, "m", 32); err != nil {
		t.Fatalf("mutateOverlayFile() error = %v", err)
	}
	updated, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(updated) != "0123456789\nm\n" {
		t.Fatalf("overlay contents = %q", updated)
	}
}

func sourceLimitsForTest() source.Limits {
	return source.Limits{
		MaxFiles:                  16,
		MaxBytes:                  1 << 20,
		MaxFileBytes:              1 << 20,
		MaxConcurrency:            2,
		MaxProbeBytes:             8 << 10,
		MaxExtractionBytes:        1 << 20,
		MaxArchiveMembers:         64,
		MaxArchiveBytes:           1 << 20,
		MaxArchiveMemberBytes:     1 << 20,
		MaxArchiveCompressedBytes: 1 << 20,
		MaxExpansionRatio:         100,
	}
}
