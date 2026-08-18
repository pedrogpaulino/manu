package contract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCoverageValidate(t *testing.T) {
	tests := []struct {
		name    string
		state   CoverageState
		wantErr bool
	}{
		{name: "produced", state: CoverageProduced},
		{name: "incomplete", state: CoverageIncomplete},
		{name: "not supported", state: CoverageNotSupported},
		{name: "not applicable", state: CoverageNotApplicable},
		{name: "failed", state: CoverageFailed},
		{name: "unknown", state: CoverageUnknown, wantErr: true},
		{name: "future state", state: CoverageState("future"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Coverage{
				Dimension: "inventory",
				State:     tt.state,
			}).Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Coverage.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLocatorValidate(t *testing.T) {
	tests := []struct {
		name    string
		locator Locator
		wantErr bool
	}{
		{name: "path", locator: Locator{Path: "src/main.java"}},
		{name: "uri", locator: Locator{URI: "file:///src/main.java"}},
		{name: "line range", locator: Locator{Path: "main.java", StartLine: 2, EndLine: 4}},
		{name: "empty", wantErr: true},
		{name: "negative line", locator: Locator{Path: "main.java", StartLine: -1}, wantErr: true},
		{name: "reversed range", locator: Locator{Path: "main.java", StartLine: 4, EndLine: 2}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.locator.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Locator.Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResultValidate(t *testing.T) {
	valid := fixtureResult()
	if err := valid.Validate(); err != nil {
		t.Fatalf("fixtureResult().Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{
			name: "missing result id",
			mutate: func(result *Result) {
				result.Manifest.ResultID = ""
			},
		},
		{
			name: "missing snapshot id",
			mutate: func(result *Result) {
				result.Manifest.Snapshot.ID = ""
			},
		},
		{
			name: "missing snapshot source id",
			mutate: func(result *Result) {
				result.Manifest.Snapshot.SourceID = ""
			},
		},
		{
			name: "wrong contract version",
			mutate: func(result *Result) {
				result.Manifest.ContractVersion = "v0"
			},
		},
		{
			name: "count mismatch",
			mutate: func(result *Result) {
				result.Manifest.ArtifactCount++
			},
		},
		{
			name: "artifact from another source",
			mutate: func(result *Result) {
				result.Artifacts[0].SourceID = "other-source"
			},
		},
		{
			name: "unknown contribution artifact",
			mutate: func(result *Result) {
				result.Contributions[0].ArtifactID = "missing-artifact"
			},
		},
		{
			name: "locator artifact mismatch",
			mutate: func(result *Result) {
				result.Contributions[0].Locator.ArtifactID = "artifact-b"
			},
		},
		{
			name: "locator source mismatch",
			mutate: func(result *Result) {
				result.Contributions[0].Locator.SourceID = "other-source"
			},
		},
		{
			name: "missing contribution locator",
			mutate: func(result *Result) {
				result.Contributions[0].Locator = Locator{}
			},
		},
		{
			name: "duplicate artifact",
			mutate: func(result *Result) {
				result.Artifacts = append(result.Artifacts, result.Artifacts[0])
				result.Manifest.ArtifactCount++
			},
		},
		{
			name: "duplicate gap",
			mutate: func(result *Result) {
				result.Manifest.Gaps = append(result.Manifest.Gaps, result.Manifest.Gaps[0])
			},
		},
		{
			name: "duplicate failure",
			mutate: func(result *Result) {
				failure := Failure{ID: "failure-duplicate", Code: "read", Operation: "analyze", Message: "could not read"}
				result.Manifest.Failures = []Failure{failure, failure}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := fixtureResult()
			tt.mutate(&result)
			if err := result.Validate(); err == nil {
				t.Fatal("Result.Validate() error = nil, want error")
			}
		})
	}
}

func TestManifestValidateRequiresSnapshotIdentity(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "result id", mutate: func(manifest *Manifest) { manifest.ResultID = "" }},
		{name: "snapshot id", mutate: func(manifest *Manifest) { manifest.Snapshot.ID = "" }},
		{name: "snapshot source id", mutate: func(manifest *Manifest) { manifest.Snapshot.SourceID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := fixtureResult().Manifest
			tt.mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatal("Manifest.Validate() error = nil, want error")
			}
		})
	}
}

func TestNormalizeDeterministicIDsAndOrdering(t *testing.T) {
	first := fixtureResult()
	first.Manifest.ResultID = ""
	first.Manifest.Snapshot.ID = ""
	first.Contributions[0].ID = ""
	first.Manifest.Coverage[0].ID = ""
	first.Manifest.Gaps[0].ID = ""
	if err := first.Normalize(); err != nil {
		t.Fatalf("first.Normalize() error = %v", err)
	}

	second := fixtureResult()
	second.Manifest.ResultID = ""
	second.Manifest.Snapshot.ID = ""
	second.Contributions[0].ID = ""
	second.Manifest.Coverage[0].ID = ""
	second.Manifest.Gaps[0].ID = ""
	second.Artifacts[0], second.Artifacts[1] = second.Artifacts[1], second.Artifacts[0]
	if err := second.Normalize(); err != nil {
		t.Fatalf("second.Normalize() error = %v", err)
	}

	if !EquivalentFacts(first, second) {
		t.Fatal("equivalent inputs produced different factual results")
	}
	if first.Artifacts[0].ID > first.Artifacts[1].ID {
		t.Fatal("artifacts were not sorted by deterministic identity")
	}
	if first.Artifacts[0].ID != second.Artifacts[0].ID || first.Contributions[0].ID != second.Contributions[0].ID {
		t.Fatal("deterministic identities changed with input order")
	}
}

func TestSourceIdentityDoesNotChangeAcrossRevisions(t *testing.T) {
	first := Source{
		Name:     "ticketmaster",
		Type:     "git",
		Root:     "/sources/ticketmaster",
		Revision: "revision-a",
		Hash:     strings.Repeat("a", 64),
	}
	second := first
	second.Revision = "revision-b"
	second.Hash = strings.Repeat("b", 64)
	if got, want := SourceIdentity(first), SourceIdentity(second); got != want {
		t.Fatalf("SourceIdentity changed across revisions: got %q and %q", got, want)
	}
	second.Name = "other-source"
	if SourceIdentity(first) == SourceIdentity(second) {
		t.Fatal("different logical sources received the same identity")
	}
}

func TestEquivalentFactsIgnoresExecutionMetadata(t *testing.T) {
	left := fixtureResult()
	right := fixtureResult()
	right.Manifest.Execution.RunID = "run-other"
	right.Manifest.Execution.StartedAt = right.Manifest.Execution.StartedAt.Add(time.Hour)
	right.Manifest.Execution.FinishedAt = right.Manifest.Execution.FinishedAt.Add(time.Hour)
	right.Manifest.Execution.Host = "other-host"
	right.Manifest.Execution.ToolVersion = "other-version"

	if !EquivalentFacts(left, right) {
		t.Fatal("execution metadata changed factual equivalence")
	}
	right.Artifacts[0].Hash = strings.Repeat("f", 64)
	if EquivalentFacts(left, right) {
		t.Fatal("factual artifact change was ignored")
	}
}

func TestSequenceStreamsValuesAndHonorsCancellation(t *testing.T) {
	values := []Artifact{fixtureResult().Artifacts[0], fixtureResult().Artifacts[1]}
	var encoded strings.Builder
	if err := WriteSequence(context.Background(), &encoded, values); err != nil {
		t.Fatalf("WriteSequence() error = %v", err)
	}
	got := make([]Artifact, 0, len(values))
	if err := ReadSequence(context.Background(), strings.NewReader(encoded.String()), func(artifact Artifact) error {
		got = append(got, artifact)
		return nil
	}); err != nil {
		t.Fatalf("ReadSequence() error = %v", err)
	}
	if len(got) != len(values) || got[0].ID != values[0].ID || got[1].ID != values[1].ID {
		t.Fatalf("ReadSequence() = %#v, want %#v", got, values)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WriteSequence(ctx, &strings.Builder{}, values); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteSequence() error = %v, want context canceled", err)
	}
}

func TestWriteResultAndReadResult(t *testing.T) {
	directory := t.TempDir()
	want := fixtureResult()
	if err := WriteResult(context.Background(), directory, want); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}
	for _, name := range []string{ManifestFileName, ArtifactsFileName, ContributionsFileName} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("result file %q: %v", name, err)
		}
	}
	got, err := ReadResult(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadResult() error = %v", err)
	}
	if !EquivalentFacts(want, got) {
		t.Fatal("round trip changed factual result")
	}
}

func TestReadManifestRejectsIncompatibleVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), ManifestFileName)
	data := `{"contract_version":"v0","source":{"id":"source","name":"fixture","type":"filesystem"},"execution":{"run_id":"run"}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadManifest(context.Background(), path)
	if !errors.Is(err, ErrIncompatibleVersion) {
		t.Fatalf("ReadManifest() error = %v, want incompatible version", err)
	}
}

func TestWriteJSONSequenceIsAtomicOnEncodingFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "values.ndjson")
	if err := WriteJSONSequence(context.Background(), path, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	values := []json.Marshaler{jsonValue(`{"value":"new"}`), failingJSON{}}
	if err := WriteJSONSequence(context.Background(), path, values); err == nil {
		t.Fatal("WriteJSONSequence() error = nil, want encoding error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\"old\"\n" {
		t.Fatalf("atomic write replaced old content with %q", got)
	}
}

type jsonValue string

func (j jsonValue) MarshalJSON() ([]byte, error) { return []byte(j), nil }

type failingJSON struct{}

func (failingJSON) MarshalJSON() ([]byte, error) {
	return nil, errors.New("intentional encoding failure")
}

func fixtureResult() Result {
	return Result{
		Manifest: Manifest{
			ContractVersion: Version,
			ResultID:        "result-fixed",
			Source: Source{
				ID:       "source-fixed",
				Name:     "fixture",
				Type:     "filesystem",
				Revision: "r1",
			},
			Snapshot: Snapshot{
				ID:       "snapshot-fixed",
				SourceID: "source-fixed",
				Revision: "r1",
				Hash:     "snapshot-hash",
			},
			Execution: ExecutionMetadata{
				RunID:       "run-fixed",
				StartedAt:   time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC),
				FinishedAt:  time.Date(2026, 8, 17, 12, 0, 1, 0, time.UTC),
				ToolVersion: "dev",
				GoVersion:   "go1.26.6",
			},
			ArtifactCount:     2,
			ContributionCount: 1,
			Coverage: []Coverage{
				{ID: "coverage-inventory", Dimension: "inventory", State: CoverageProduced},
				{ID: "coverage-relations", Dimension: "relations", State: CoverageNotSupported},
			},
			Gaps: []Gap{
				{ID: "gap-relations", Code: "not_supported", Dimension: "relations", Message: "relation analyzer is unavailable"},
			},
			Failures: []Failure{},
		},
		Artifacts: []Artifact{
			{ID: "artifact-a", SourceID: "source-fixed", Path: "src/A.java", Type: "java", Hash: strings.Repeat("a", 64), Size: 10},
			{ID: "artifact-b", SourceID: "source-fixed", Path: "src/B.java", Type: "java", Hash: strings.Repeat("b", 64), Size: 20},
		},
		Contributions: []Contribution{
			{
				ID:              "contribution-a",
				ArtifactID:      "artifact-a",
				AnalyzerID:      "generic",
				AnalyzerVersion: "1",
				Method:          "inventory",
				Type:            "symbol",
				Locator:         Locator{Path: "src/A.java", StartLine: 1, EndLine: 1},
				ObservedAt:      time.Date(2026, 8, 17, 12, 0, 1, 0, time.UTC),
				Value:           []byte(`{"name":"A"}`),
			},
		},
	}
}
