package analysis

import (
	"fmt"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/state"
)

var (
	benchmarkStateHits        int
	benchmarkInvalidatedCount int
)

func BenchmarkStateIndexLookup(b *testing.B) {
	for _, size := range []int{64, 1024, 16384} {
		b.Run(fmt.Sprintf("entries=%d", size), func(b *testing.B) {
			snapshot := benchmarkSnapshot(size)
			index := buildStateIndex(snapshot)
			keys := make([]state.Key, size)
			inputs := make([]ArtifactInput, size)
			for i, entry := range snapshot.Entries {
				keys[i] = entry.Key
				inputs[i].Artifact = snapshot.Artifacts[i]
			}

			b.ReportAllocs()
			b.ResetTimer()
			hits := 0
			for i := 0; b.Loop(); i++ {
				if _, ok := lookupStateEntry(index, keys[i%size], inputs[i%size]); ok {
					hits++
				}
			}
			benchmarkStateHits = hits
		})
	}
}

func BenchmarkInvalidatedPathsWithIndex(b *testing.B) {
	for _, size := range []int{64, 1024, 16384} {
		b.Run(fmt.Sprintf("artifacts=%d", size), func(b *testing.B) {
			snapshot := benchmarkSnapshot(size)
			index := buildStateIndex(snapshot)
			current := append([]contract.Artifact(nil), snapshot.Artifacts...)

			b.ReportAllocs()
			b.ResetTimer()
			invalidatedCount := 0
			for b.Loop() {
				invalidatedCount += len(invalidatedPathsWithIndex(index, current))
			}
			benchmarkInvalidatedCount = invalidatedCount
		})
	}
}

func benchmarkSnapshot(size int) state.Snapshot {
	const sourceID = "benchmark-source"
	const analyzerID = "benchmark-analyzer"
	const analyzerVersion = "v1"
	const analyzerMethod = "indexed-lookup"

	snapshot := state.Snapshot{
		Version:         state.Version,
		ContractVersion: contract.Version,
		SourceID:        sourceID,
		Artifacts:       make([]contract.Artifact, size),
		Entries:         make([]state.Entry, size),
		Dependencies:    []state.Dependency{},
	}
	for i := range size {
		artifactPath := fmt.Sprintf("pkg/%06d.java", i)
		artifactHash := fmt.Sprintf("hash-%06d", i)
		artifact := contract.Artifact{
			ID:       contract.ArtifactID(sourceID, artifactPath, artifactHash),
			SourceID: sourceID,
			Path:     artifactPath,
			Type:     "source",
			Hash:     artifactHash,
		}
		snapshot.Artifacts[i] = artifact
		snapshot.Entries[i] = state.Entry{
			Key:        state.NewKey(sourceID, artifactPath, artifactHash, contract.Version, analyzerID, analyzerVersion, analyzerMethod),
			ArtifactID: artifact.ID,
			Coverage:   []contract.Coverage{},
			Gaps:       []contract.Gap{},
		}
	}
	return snapshot
}
