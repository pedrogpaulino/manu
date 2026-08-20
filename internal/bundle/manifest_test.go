package bundle_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestManifestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*bundle.Bundle)
		wantErr error
	}{
		{
			name:   "valid bundle",
			mutate: func(*bundle.Bundle) {},
		},
		{
			name: "unsupported bundle version",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Version = "v0"
			},
			wantErr: bundle.ErrUnsupportedVersion,
		},
		{
			name: "missing organization",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Organization.ID = ""
			},
			wantErr: bundle.ErrInvalid,
		},
		{
			name: "snapshot source mismatch",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Snapshot.SourceID = "source-other"
			},
			wantErr: bundle.ErrScopeMismatch,
		},
		{
			name: "source and snapshot revision mismatch",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Snapshot.Revision = "revision-other"
			},
			wantErr: bundle.ErrScopeMismatch,
		},
		{
			name: "source hash is not sha256",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Source.Hash = "source-hash"
			},
			wantErr: bundle.ErrInvalidDigest,
		},
		{
			name: "snapshot hash is not sha256",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Snapshot.Hash = "snapshot-hash"
			},
			wantErr: bundle.ErrInvalidDigest,
		},
		{
			name: "missing source and snapshot revision or hash",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Source.Revision = ""
				input.Manifest.Source.Hash = ""
				input.Manifest.Snapshot.Revision = ""
				input.Manifest.Snapshot.Hash = ""
			},
			wantErr: bundle.ErrMissingRevision,
		},
		{
			name: "analysis configuration mismatch",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Analysis.ConfigurationID = "analysis-other"
			},
			wantErr: bundle.ErrScopeMismatch,
		},
		{
			name: "analysis hash is not sha256",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Analysis.Hash = "not-a-digest"
			},
			wantErr: bundle.ErrInvalidDigest,
		},
		{
			name: "invalid factual digest",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.FactualDigest = "not-a-digest"
			},
			wantErr: bundle.ErrInvalidDigest,
		},
		{
			name: "missing canonical sequence",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files = input.Manifest.Files[:1]
			},
			wantErr: bundle.ErrInvalidFile,
		},
		{
			name: "unknown sequence name",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files[0].Name = "other.ndjson"
			},
			wantErr: bundle.ErrInvalidFile,
		},
		{
			name: "duplicate sequence descriptor",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files = append(input.Manifest.Files, input.Manifest.Files[0])
			},
			wantErr: bundle.ErrDuplicate,
		},
		{
			name: "sequence has invalid digest",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files[0].Digest = "invalid"
			},
			wantErr: bundle.ErrInvalidDigest,
		},
		{
			name: "artifact hash is not sha256",
			mutate: func(input *bundle.Bundle) {
				input.Artifacts[0].Hash = "artifact-hash"
			},
			wantErr: bundle.ErrInvalidDigest,
		},
		{
			name: "sequence has negative bytes",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files[0].Bytes = -1
			},
			wantErr: bundle.ErrLimitExceeded,
		},
		{
			name: "sequence count disagrees with manifest",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Files[0].Count++
			},
			wantErr: bundle.ErrCountMismatch,
		},
		{
			name: "negative count",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Counts.EvidenceUnitCount = -1
			},
			wantErr: bundle.ErrLimitExceeded,
		},
		{
			name: "bundle byte limit exceeded",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Limits.MaxBundleBytes = 1
			},
			wantErr: bundle.ErrLimitExceeded,
		},
		{
			name: "manifest byte limit is transport metadata",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Limits.MaxManifestBytes = 1
			},
		},
		{
			name: "count limit exceeded",
			mutate: func(input *bundle.Bundle) {
				input.Manifest.Limits.MaxEvidenceUnits = 1
				input.Manifest.Counts.EvidenceUnitCount = 2
			},
			wantErr: bundle.ErrLimitExceeded,
		},
		{
			name: "artifact duplicate",
			mutate: func(input *bundle.Bundle) {
				input.Artifacts = append(input.Artifacts, input.Artifacts[0])
				input.Manifest.ArtifactCount++
				input.Manifest.Counts.ArtifactCount++
				input.Manifest.Files[0].Count++
			},
			wantErr: bundle.ErrDuplicate,
		},
		{
			name: "contribution duplicate",
			mutate: func(input *bundle.Bundle) {
				input.Contributions = append(input.Contributions, input.Contributions[0])
				input.Manifest.ContributionCount++
				input.Manifest.Counts.ContributionCount++
				input.Manifest.Files[1].Count++
			},
			wantErr: bundle.ErrDuplicate,
		},
		{
			name: "evidence outside organization scope",
			mutate: func(input *bundle.Bundle) {
				input.Evidence[0].OrganizationID = "organization-other"
			},
			wantErr: bundle.ErrScopeMismatch,
		},
		{
			name: "evidence references missing artifact",
			mutate: func(input *bundle.Bundle) {
				input.Evidence[0].ArtifactID = "artifact-other"
			},
			wantErr: bundle.ErrInvalidReference,
		},
		{
			name: "evidence references missing contribution",
			mutate: func(input *bundle.Bundle) {
				input.Evidence[0].Contribution.ID = "contribution-other"
			},
			wantErr: bundle.ErrInvalidReference,
		},
		{
			name: "evidence contribution metadata mismatch",
			mutate: func(input *bundle.Bundle) {
				input.Evidence[0].Contribution.Method = "other-method"
				input.Evidence[0].ID = evidence.EvidenceID(input.Evidence[0])
			},
			wantErr: bundle.ErrInvalidReference,
		},
		{
			name: "evidence duplicate",
			mutate: func(input *bundle.Bundle) {
				input.Evidence = append(input.Evidence, input.Evidence[0])
				input.Manifest.Counts.EvidenceUnitCount++
				input.Manifest.Files[2].Count++
			},
			wantErr: bundle.ErrDuplicate,
		},
		{
			name: "evidence fact changes digest",
			mutate: func(input *bundle.Bundle) {
				input.Evidence[0].ContentState = evidence.ContentStateRedacted
				input.Evidence[0].Content = evidence.RedactedContent
				input.Evidence[0].ContentBytes = int64(len(input.Evidence[0].Content))
				input.Evidence[0].ContentCharacters = int64(len([]rune(input.Evidence[0].Content)))
				input.Evidence[0].RedactionReason = "fixture policy"
			},
			wantErr: bundle.ErrDigestMismatch,
		},
		{
			name: "redaction metadata changes digest",
			mutate: func(input *bundle.Bundle) {
				input.Evidence[0].ContentState = evidence.ContentStateRedacted
				input.Evidence[0].Content = evidence.RedactedContent
				input.Evidence[0].ContentBytes = int64(len(input.Evidence[0].Content))
				input.Evidence[0].ContentCharacters = int64(len([]rune(input.Evidence[0].Content)))
				input.Evidence[0].RedactionReason = "fixture policy"
				digest, err := input.FactualDigest()
				if err != nil {
					panic(err)
				}
				input.Manifest.FactualDigest = digest
				input.Evidence[0].RedactionReason = "other fixture policy"
			},
			wantErr: bundle.ErrDigestMismatch,
		},
		{
			name: "factual digest mismatch",
			mutate: func(input *bundle.Bundle) {
				input.Artifacts[0].Hash = strings.Repeat("f", 64)
			},
			wantErr: bundle.ErrDigestMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := validBundle()
			tt.mutate(&input)
			err := input.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Bundle.Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Bundle.Validate() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
		})
	}
}

func TestManifestNormalizeLegacyInput(t *testing.T) {
	t.Parallel()

	input := validBundle()
	input.Manifest.Evidence = bundle.EvidenceMetadata{}
	input.Manifest.Files = input.Manifest.Files[:2]
	input.Manifest.Counts.EvidenceUnitCount = 0
	input.Evidence = nil
	input.Manifest.Version = ""
	legacy := contract.Result{
		Manifest:      input.Manifest.Manifest,
		Artifacts:     input.Artifacts,
		Contributions: input.Contributions,
	}
	digest, err := bundle.FactualDigest(legacy, nil)
	if err != nil {
		t.Fatalf("bundle.FactualDigest() error = %v", err)
	}
	input.Manifest.FactualDigest = digest
	if err := input.Manifest.Normalize(); err != nil {
		t.Fatalf("Manifest.Normalize() error = %v", err)
	}
	if input.Manifest.Version != bundle.Version {
		t.Fatalf("normalized version = %q, want %q", input.Manifest.Version, bundle.Version)
	}
	if input.Manifest.Evidence.State != bundle.EvidenceStateLimited {
		t.Fatalf("normalized evidence state = %q, want limited", input.Manifest.Evidence.State)
	}
	if !input.Manifest.EvidenceLimited() {
		t.Fatal("EvidenceLimited() = false, want true")
	}
}

func TestManifestJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want := validBundle()
	encoded, err := json.Marshal(want.Manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var got bundle.Manifest
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want.Manifest) {
		t.Fatalf("manifest JSON round trip changed value:\n got %#v\nwant %#v", got, want.Manifest)
	}
	if !strings.Contains(string(encoded), `"evidence.ndjson"`) {
		t.Fatalf("manifest JSON omitted evidence sequence: %s", encoded)
	}
}

func TestFactualDigestEvidenceOrderIsStable(t *testing.T) {
	t.Parallel()

	input := validBundle()
	second := input.Evidence[0]
	second.Locator.StartLine = 2
	second.Locator.EndLine = 2
	second.Content = "class B {}"
	second.ContentHash = evidence.ContentDigest(second.Content)
	second.ContentBytes = int64(len(second.Content))
	second.ContentCharacters = int64(len([]rune(second.Content)))
	second.ID = evidence.EvidenceID(second)
	result := contract.Result{
		Manifest:      input.Manifest.Manifest,
		Artifacts:     input.Artifacts,
		Contributions: input.Contributions,
	}
	left, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{input.Evidence[0], second})
	if err != nil {
		t.Fatalf("FactualDigest(left) error = %v", err)
	}
	right, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{second, input.Evidence[0]})
	if err != nil {
		t.Fatalf("FactualDigest(right) error = %v", err)
	}
	if left != right {
		t.Fatalf("FactualDigest depends on sequence order: left=%q right=%q", left, right)
	}
}

func TestFactualDigestIncludesEvidenceTruncation(t *testing.T) {
	t.Parallel()

	input := validBundle()
	result := contract.Result{
		Manifest:      input.Manifest.Manifest,
		Artifacts:     input.Artifacts,
		Contributions: input.Contributions,
	}
	withoutTruncation, err := bundle.FactualDigest(result, input.Evidence)
	if err != nil {
		t.Fatalf("FactualDigest() error = %v", err)
	}
	truncated := input.Evidence[0]
	truncated.Truncated = true
	truncated.ID = evidence.EvidenceID(truncated)
	withTruncation, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{truncated})
	if err != nil {
		t.Fatalf("FactualDigest(truncated) error = %v", err)
	}
	if withTruncation == withoutTruncation {
		t.Fatal("factual digest did not change when evidence truncation changed")
	}
}

func TestFactualDigestIncludesClassificationAndCanonicalFindings(t *testing.T) {
	t.Parallel()

	input := validBundle()
	result := contract.Result{
		Manifest:      input.Manifest.Manifest,
		Artifacts:     input.Artifacts,
		Contributions: input.Contributions,
	}
	annotated := input.Evidence[0]
	annotated.Classification = evidence.ClassificationSensitive
	annotated.Findings = []string{evidence.FindingSecretAssignment, evidence.FindingSecret}
	annotated.ContentState = evidence.ContentStateRedacted
	annotated.Content = evidence.RedactedContent
	annotated.ContentHash = evidence.ContentDigest("source secret")
	annotated.ContentBytes = int64(len(annotated.Content))
	annotated.ContentCharacters = int64(len([]rune(annotated.Content)))
	annotated.RedactionReason = "sensitive-content"
	annotated.Persist = evidence.DecisionRedact
	annotated.ExternalTransfer = evidence.DecisionDeny
	annotated.ID = evidence.EvidenceID(annotated)
	canonical, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{annotated})
	if err != nil {
		t.Fatalf("FactualDigest(canonical) error = %v", err)
	}
	reordered := annotated
	reordered.Findings = []string{evidence.FindingSecret, evidence.FindingSecretAssignment}
	reordered.ID = evidence.EvidenceID(reordered)
	reorderedDigest, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{reordered})
	if err != nil {
		t.Fatalf("FactualDigest(reordered) error = %v", err)
	}
	if reorderedDigest != canonical {
		t.Fatalf("factual digest depends on finding order: canonical=%q reordered=%q", canonical, reorderedDigest)
	}

	changedFinding := annotated
	changedFinding.Findings = []string{evidence.FindingSecret, evidence.FindingPEMPrivateKey}
	changedFinding.ID = evidence.EvidenceID(changedFinding)
	changedFindingDigest, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{changedFinding})
	if err != nil {
		t.Fatalf("FactualDigest(changed finding) error = %v", err)
	}
	if changedFindingDigest == canonical {
		t.Fatal("factual digest did not change when finding changed")
	}

	changedClassification := annotated
	changedClassification.Classification = evidence.ClassificationPromptInjection
	changedClassification.Findings = []string{evidence.FindingPromptInjection}
	changedClassification.RedactionReason = "prompt-injection"
	changedClassification.Persist = evidence.DecisionRedact
	changedClassification.ExternalTransfer = evidence.DecisionDeny
	changedClassification.ID = evidence.EvidenceID(changedClassification)
	changedClassificationDigest, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{changedClassification})
	if err != nil {
		t.Fatalf("FactualDigest(changed classification) error = %v", err)
	}
	if changedClassificationDigest == canonical {
		t.Fatal("factual digest did not change when classification changed")
	}
}

func validBundle() bundle.Bundle {
	const (
		organizationID  = "organization-1"
		sourceID        = "source-1"
		revision        = "revision-1"
		configurationID = "configuration-1"
	)

	source := contract.Source{
		ID:       sourceID,
		Name:     "fixture",
		Type:     "filesystem",
		Revision: revision,
	}
	snapshot := contract.Snapshot{
		ID:       contract.SnapshotID(sourceID, revision, strings.Repeat("b", 64)),
		SourceID: sourceID,
		Revision: revision,
		Hash:     strings.Repeat("b", 64),
	}
	artifact := contract.Artifact{
		SourceID: sourceID,
		Path:     "src/A.java",
		Type:     "java",
		Hash:     strings.Repeat("a", 64),
		Size:     12,
	}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	contribution := contract.Contribution{
		ArtifactID:      artifact.ID,
		AnalyzerID:      "java",
		AnalyzerVersion: "1",
		Method:          "symbols",
		Type:            "symbol",
		Locator: contract.Locator{
			SourceID:   sourceID,
			ArtifactID: artifact.ID,
			Path:       artifact.Path,
			StartLine:  1,
			EndLine:    1,
		},
	}
	contribution.ID = contract.ContributionID(
		contribution.ArtifactID,
		contribution.AnalyzerID,
		contribution.AnalyzerVersion,
		contribution.Method,
	)
	legacy := contract.Manifest{
		ContractVersion: contract.Version,
		ResultID:        "result-1",
		Source:          source,
		Snapshot:        snapshot,
		Execution: contract.ExecutionMetadata{
			RunID:           "run-1",
			ConfigurationID: configurationID,
		},
		ArtifactCount:     1,
		ContributionCount: 1,
		Coverage:          []contract.Coverage{},
		Gaps:              []contract.Gap{},
		Failures:          []contract.Failure{},
	}
	result := contract.Result{
		Manifest:      legacy,
		Artifacts:     []contract.Artifact{artifact},
		Contributions: []contract.Contribution{contribution},
	}

	content := "class A {}"
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: organizationID,
		SourceID:       sourceID,
		SnapshotID:     snapshot.ID,
		ArtifactID:     artifact.ID,
		Contribution: evidence.ContributionRef{
			ID:              contribution.ID,
			ArtifactID:      artifact.ID,
			AnalyzerID:      contribution.AnalyzerID,
			AnalyzerVersion: contribution.AnalyzerVersion,
			Method:          contribution.Method,
		},
		Locator:           contribution.Locator,
		ContentState:      evidence.ContentStatePresent,
		Content:           content,
		ContentHash:       evidence.ContentDigest(content),
		ContentBytes:      int64(len(content)),
		ContentCharacters: int64(len(content)),
		Persist:           evidence.DecisionAllow,
		ExternalTransfer:  evidence.DecisionAllow,
	}
	unit.ID = evidence.EvidenceID(unit)
	unitDigest, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{unit})
	if err != nil {
		panic(err)
	}

	fileDigest := strings.Repeat("c", 64)
	return bundle.Bundle{
		Manifest: bundle.Manifest{
			Version:      bundle.Version,
			Organization: bundle.Organization{ID: organizationID, Name: "Fixture organization"},
			Manifest:     legacy,
			Analysis: bundle.Analysis{
				ID:              "analysis-1",
				ConfigurationID: configurationID,
				Revision:        "analysis-revision-1",
			},
			FactualDigest: unitDigest,
			Files: []bundle.File{
				{Name: bundle.ArtifactsFileName, Bytes: 256, Count: 1, Digest: fileDigest},
				{Name: bundle.ContributionsFileName, Bytes: 256, Count: 1, Digest: fileDigest},
				{Name: bundle.EvidenceFileName, Bytes: 256, Count: 1, Digest: fileDigest},
			},
			Counts: bundle.Counts{
				ArtifactCount:     1,
				ContributionCount: 1,
				EvidenceUnitCount: 1,
			},
			Limits: bundle.Limits{
				MaxBundleBytes:   1 << 20,
				MaxManifestBytes: 1 << 16,
				MaxEvidenceBytes: 1 << 16,
				MaxArtifacts:     10,
				MaxContributions: 10,
				MaxEvidenceUnits: 10,
			},
			Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
		},
		Artifacts:     []contract.Artifact{artifact},
		Contributions: []contract.Contribution{contribution},
		Evidence:      []evidence.EvidenceUnit{unit},
	}
}
