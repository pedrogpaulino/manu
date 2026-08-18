package analysis

import (
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/state"
)

func TestStateIndexLookupRequiresCompleteKey(t *testing.T) {
	snapshot := benchmarkSnapshot(1)
	index := buildStateIndex(snapshot)
	entry := snapshot.Entries[0]
	input := ArtifactInput{Artifact: snapshot.Artifacts[0]}

	if _, ok := lookupStateEntry(index, entry.Key, input); !ok {
		t.Fatal("indexed lookup missed an exact key")
	}
	for name, mutate := range map[string]func(*state.Key){
		"source":           func(key *state.Key) { key.SourceID = "another-source" },
		"path":             func(key *state.Key) { key.ArtifactPath = "other.java" },
		"hash":             func(key *state.Key) { key.ArtifactHash = "another-hash" },
		"contract":         func(key *state.Key) { key.ContractVersion = "another-contract" },
		"analyzer":         func(key *state.Key) { key.AnalyzerID = "another-analyzer" },
		"analyzer-version": func(key *state.Key) { key.AnalyzerVersion = "another-version" },
		"method":           func(key *state.Key) { key.AnalyzerMethod = "another-method" },
	} {
		t.Run(name, func(t *testing.T) {
			key := entry.Key
			mutate(&key)
			if _, ok := lookupStateEntry(index, key, input); ok {
				t.Fatalf("lookup reused entry after changing %s", name)
			}
		})
	}
}

func TestStateIndexInvalidSnapshotIsSafeMiss(t *testing.T) {
	snapshot := benchmarkSnapshot(1)
	snapshot.Entries[0].Key.AnalyzerMethod = ""
	index := buildStateIndex(snapshot)
	if len(index.entriesByKey) != 0 {
		t.Fatal("invalid snapshot populated the state index")
	}
	if _, ok := lookupStateEntry(index, snapshot.Entries[0].Key, ArtifactInput{Artifact: snapshot.Artifacts[0]}); ok {
		t.Fatal("invalid snapshot produced a cache hit")
	}
}

func TestInvalidatedPathsWithIndexPreservesDirectScope(t *testing.T) {
	snapshot := benchmarkSnapshot(3)
	snapshot.Dependencies = []state.Dependency{{
		FromPath: snapshot.Artifacts[0].Path,
		ToPath:   snapshot.Artifacts[1].Path,
		Kind:     "java_import",
	}}
	index := buildStateIndex(snapshot)
	current := append([]contract.Artifact(nil), snapshot.Artifacts...)
	current[1].Hash = "changed-hash"

	invalidated := invalidatedPathsWithIndex(index, current)
	if !invalidated[current[0].Path] || !invalidated[current[1].Path] {
		t.Fatalf("direct dependent was not invalidated: %v", invalidated)
	}
	if invalidated[current[2].Path] {
		t.Fatalf("independent artifact was invalidated: %v", invalidated)
	}
}
