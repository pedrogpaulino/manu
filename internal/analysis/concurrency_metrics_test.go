package analysis

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

func TestRunnerReportsObservedAnalyzerConcurrency(t *testing.T) {
	root := t.TempDir()
	for index := range 4 {
		pathName := filepath.Join(root, "input-"+string(rune('a'+index))+".txt")
		if err := os.WriteFile(pathName, []byte("fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	analyzer := &blockingMetricsAnalyzer{
		started: make(chan struct{}, 4),
		release: make(chan struct{}),
	}
	registry, err := NewRegistry(analyzer)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	metrics := &RunMetrics{}
	done := make(chan struct{})
	var result contract.Result
	var runErr error
	go func() {
		result, runErr = runner.Run(context.Background(), Config{
			Source:  contract.Source{ID: "metrics-source", Name: "fixture", Type: "filesystem"},
			Root:    root,
			Limits:  source.Limits{MaxConcurrency: 3},
			Metrics: metrics,
		})
		close(done)
	}()
	for range 3 {
		select {
		case <-analyzer.started:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for analyzer workers")
		}
	}
	close(analyzer.release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for runner")
	}
	if runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result validation = %v", err)
	}
	if metrics.EffectiveConcurrency != 3 {
		t.Fatalf("observed concurrency = %d, want 3", metrics.EffectiveConcurrency)
	}
}

type blockingMetricsAnalyzer struct {
	started chan struct{}
	release chan struct{}
}

func (a *blockingMetricsAnalyzer) Descriptor() Descriptor {
	return Descriptor{
		ID:              "blocking-metrics",
		Version:         "1",
		Method:          "blocking-v1",
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{ArtifactTypeAny},
	}
}

func (a *blockingMetricsAnalyzer) Analyze(ctx context.Context, input ArtifactInput) (Output, error) {
	select {
	case a.started <- struct{}{}:
	case <-ctx.Done():
		return Output{}, ctx.Err()
	}
	select {
	case <-a.release:
	case <-ctx.Done():
		return Output{}, ctx.Err()
	}
	contribution, err := NewContribution(
		input,
		a.Descriptor(),
		"observed:"+input.Artifact.Path,
		"test.observed_concurrency",
		contract.Locator{Path: input.Artifact.Path},
		map[string]string{"path": input.Artifact.Path},
	)
	if err != nil {
		return Output{}, err
	}
	return Output{Contributions: []contract.Contribution{contribution}}, nil
}
