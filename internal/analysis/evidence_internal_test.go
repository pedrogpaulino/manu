package analysis

import (
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestAppendEvidenceObservabilityPreservesExistingLimitedMetric(t *testing.T) {
	result := contract.Result{
		Manifest: contract.Manifest{
			Source:    contract.Source{ID: "source-1"},
			Execution: contract.ExecutionMetadata{Metrics: contract.ExecutionMetrics{Limited: 7}},
		},
	}
	appendEvidenceObservability(&result, []evidenceObservation{{
		ArtifactID: "artifact-1",
		Path:       "src/A.java",
		Truncated:  true,
	}})
	if got := result.Manifest.Execution.Metrics.Limited; got != 7 {
		t.Fatalf("Limited metric = %d, want existing value 7", got)
	}
}
