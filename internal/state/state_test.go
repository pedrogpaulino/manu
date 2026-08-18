package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestStoreWritesAndLooksUpExactCompatibilityKey(t *testing.T) {
	destination := t.TempDir()
	artifact := contract.Artifact{
		ID:       contract.ArtifactID("source-1", "src/Main.java", "hash-1"),
		SourceID: "source-1",
		Path:     "src/Main.java",
		Type:     "java",
		Hash:     "hash-1",
		Size:     12,
	}
	key := NewKey("source-1", artifact.Path, artifact.Hash, contract.Version, "java", "1", "lexical-java-v1")
	contribution := contract.Contribution{
		ID:              contract.ContributionID(artifact.ID, key.AnalyzerID, key.AnalyzerVersion, "type:Main"),
		ArtifactID:      artifact.ID,
		AnalyzerID:      key.AnalyzerID,
		AnalyzerVersion: key.AnalyzerVersion,
		Method:          "type:Main",
		Type:            "java.type",
		Locator:         contract.Locator{SourceID: "source-1", ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1},
	}
	snapshot := Snapshot{
		Version:         Version,
		ContractVersion: contract.Version,
		SourceID:        "source-1",
		Artifacts:       []contract.Artifact{artifact},
		Entries: []Entry{{
			Key:           key,
			ArtifactID:    artifact.ID,
			Contributions: []contract.Contribution{contribution},
			Coverage:      []contract.Coverage{},
			Gaps:          []contract.Gap{},
		}},
		Dependencies: []Dependency{},
	}

	if err := Write(context.Background(), destination, snapshot); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	statePath := filepath.Join(destination, StateFileName)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("state file was not written: %v", err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != StateFileName {
		t.Fatalf("temporary state files remain: %#v", entries)
	}

	store, err := Open(destination)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if store.LoadError() != nil {
		t.Fatalf("LoadError() = %v, want nil", store.LoadError())
	}
	got, ok := store.Lookup(key)
	if !ok {
		t.Fatal("Lookup() did not find exact key")
	}
	if got.ArtifactID != artifact.ID || len(got.Contributions) != 1 {
		t.Fatalf("Lookup() = %#v", got)
	}
	wrongHash := key
	wrongHash.ArtifactHash = "another-hash"
	if _, ok := store.Lookup(wrongHash); ok {
		t.Fatal("Lookup() reused an entry with a different artifact hash")
	}
}

func TestOpenTreatsCorruptAndIncompatibleStateAsCacheMiss(t *testing.T) {
	tests := []struct {
		name       string
		contents   string
		wantReason error
	}{
		{name: "malformed json", contents: "{", wantReason: ErrCorrupt},
		{name: "wrong state version", contents: `{"version":"v0","contract_version":"v1alpha1","source_id":"source-1","artifacts":[],"entries":[],"dependencies":[]}`, wantReason: ErrIncompatibleVersion},
		{name: "wrong contract version", contents: `{"version":"v1alpha1","contract_version":"v0","source_id":"source-1","artifacts":[],"entries":[],"dependencies":[]}`, wantReason: ErrIncompatibleVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := t.TempDir()
			if err := os.WriteFile(filepath.Join(destination, StateFileName), []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := Open(destination)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			if !errors.Is(store.LoadError(), test.wantReason) {
				t.Fatalf("LoadError() = %v, want %v", store.LoadError(), test.wantReason)
			}
			if _, ok := store.Lookup(NewKey("source-1", "src/Main.java", "hash-1", contract.Version, "java", "1", "lexical-java-v1")); ok {
				t.Fatal("corrupt or incompatible state produced a cache hit")
			}
		})
	}
}

func TestKeyRejectsPathsOutsideTheSourceNamespace(t *testing.T) {
	for _, path := range []string{"/absolute/file.java", "../outside.java", ".."} {
		t.Run(path, func(t *testing.T) {
			key := NewKey("source-1", path, "hash", contract.Version, "generic", "1", "inventory-text-v1")
			if err := key.Validate(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Key.Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestSnapshotRejectsIncoherentEntriesAndUnknownDependencies(t *testing.T) {
	artifact := contract.Artifact{
		ID:       contract.ArtifactID("source-1", "Main.java", "hash-1"),
		SourceID: "source-1",
		Path:     "Main.java",
		Type:     "java",
		Hash:     "hash-1",
	}
	key := NewKey("source-1", artifact.Path, artifact.Hash, contract.Version, "java", "1", "lexical-java-v1")
	base := Snapshot{
		Version:         Version,
		ContractVersion: contract.Version,
		SourceID:        "source-1",
		Artifacts:       []contract.Artifact{artifact},
		Entries:         []Entry{{Key: key, ArtifactID: artifact.ID, Contributions: []contract.Contribution{}, Coverage: []contract.Coverage{}, Gaps: []contract.Gap{}}},
		Dependencies:    []Dependency{},
	}
	wrongHash := base
	wrongHash.Entries = append([]Entry(nil), base.Entries...)
	wrongHash.Entries[0].Key.ArtifactHash = "other-hash"
	if err := wrongHash.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong hash validation error = %v, want ErrCorrupt", err)
	}

	wrongCoverage := base
	wrongCoverage.Entries = append([]Entry(nil), base.Entries...)
	wrongCoverage.Entries[0].Coverage = []contract.Coverage{{Dimension: "entities", State: contract.CoverageProduced, AnalyzerID: "other-analyzer"}}
	if err := wrongCoverage.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong coverage analyzer validation error = %v, want ErrCorrupt", err)
	}

	unknownDependency := base
	unknownDependency.Dependencies = []Dependency{{FromPath: "Main.java", ToPath: "Missing.java", Kind: "java-import"}}
	if err := unknownDependency.Validate(); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("unknown dependency validation error = %v, want ErrCorrupt", err)
	}
}
