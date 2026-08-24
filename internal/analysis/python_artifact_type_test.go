package analysis

import (
	"context"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

func TestArtifactTypeClassifiesPythonAndPreservesTextFallback(t *testing.T) {
	tests := []struct {
		name           string
		artifact       contract.Artifact
		sourceArtifact source.Artifact
		fromSource     bool
		want           string
	}{
		{
			name:     "explicit type",
			artifact: contract.Artifact{Type: ArtifactTypePython, Path: "module.txt"},
			want:     ArtifactTypePython,
		},
		{
			name:     "python extension",
			artifact: contract.Artifact{Path: "module.py"},
			want:     ArtifactTypePython,
		},
		{
			name:           "python extension from source",
			sourceArtifact: source.Artifact{RelativePath: "module.PY"},
			fromSource:     true,
			want:           ArtifactTypePython,
		},
		{
			name: "text fallback",
			artifact: contract.Artifact{
				Path: "notes.txt",
				Type: ArtifactTypeText,
			},
			want: ArtifactTypeText,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got string
			if test.fromSource {
				got = artifactTypeFromSource(test.sourceArtifact)
			} else {
				got = artifactType(test.artifact, test.sourceArtifact)
			}
			if got != test.want {
				t.Fatalf("artifact type = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRegistrySelectsPythonAnalyzerWithFallback(t *testing.T) {
	registry, err := NewRegistry(
		testSelectionAnalyzer{descriptor: Descriptor{
			ID:              "python-analyzer",
			Version:         "v1",
			Method:          "python",
			ContractVersion: contract.Version,
			ArtifactTypes:   []string{ArtifactTypePython},
		}},
		testSelectionAnalyzer{descriptor: Descriptor{
			ID:              "fallback-analyzer",
			Version:         "v1",
			Method:          "fallback",
			ContractVersion: contract.Version,
			ArtifactTypes:   []string{ArtifactTypeAny},
			Fallback:        true,
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	pythonSelected := registry.Select(ArtifactInput{
		Artifact: contract.Artifact{Path: "module.py"},
	})
	if len(pythonSelected) != 2 {
		t.Fatalf("Python analyzers = %d, want 2", len(pythonSelected))
	}
	if !containsAnalyzer(pythonSelected, "python-analyzer") || !containsAnalyzer(pythonSelected, "fallback-analyzer") {
		t.Fatalf("Python selection = %v, want specialized and fallback analyzers", analyzerIDs(pythonSelected))
	}

	textSelected := registry.Select(ArtifactInput{
		Artifact: contract.Artifact{Path: "notes.txt"},
	})
	if len(textSelected) != 1 || !containsAnalyzer(textSelected, "fallback-analyzer") {
		t.Fatalf("text selection = %v, want fallback only", analyzerIDs(textSelected))
	}
}

type testSelectionAnalyzer struct {
	descriptor Descriptor
}

func (a testSelectionAnalyzer) Descriptor() Descriptor { return a.descriptor }

func (testSelectionAnalyzer) Analyze(context.Context, ArtifactInput) (Output, error) {
	return Output{}, nil
}

func containsAnalyzer(analyzers []Analyzer, id string) bool {
	for _, analyzer := range analyzers {
		if analyzer.Descriptor().ID == id {
			return true
		}
	}
	return false
}

func analyzerIDs(analyzers []Analyzer) []string {
	ids := make([]string, 0, len(analyzers))
	for _, analyzer := range analyzers {
		ids = append(ids, analyzer.Descriptor().ID)
	}
	return ids
}
