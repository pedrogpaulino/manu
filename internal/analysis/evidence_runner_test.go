package analysis_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestRunWithEvidenceMaterializesAfterSnapshotAndBoundsContent(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", strings.Repeat("bounded evidence line\n", 1024))
	counter := &atomic.Int32{}
	runner := newEvidenceTestRunner(t, &draftAnalyzer{counter: counter, content: strings.Repeat("retained\n", 1024)})

	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-explicit",
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("AnalysisResult.Validate() error = %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("evidence units = %d, want 1", len(result.Evidence))
	}
	unit := result.Evidence[0]
	if unit.OrganizationID != "org-explicit" {
		t.Fatalf("organization id = %q, want explicit input", unit.OrganizationID)
	}
	if unit.SnapshotID != result.Result.Manifest.Snapshot.ID {
		t.Fatalf("snapshot id = %q, result snapshot = %q", unit.SnapshotID, result.Result.Manifest.Snapshot.ID)
	}
	if unit.ContentState != evidence.ContentStatePresent || unit.Content == "" {
		t.Fatalf("content state/content = %q/%q, want bounded present content", unit.ContentState, unit.Content)
	}
	if unit.Persist != evidence.DecisionAllow || unit.ExternalTransfer != evidence.DecisionDeny {
		t.Fatalf("default evidence decisions = persist:%q transfer:%q", unit.Persist, unit.ExternalTransfer)
	}
	if len([]byte(unit.Content)) > int(analysis.DefaultEvidenceMaxBytesPerUnit) ||
		int64(len([]rune(unit.Content))) > analysis.DefaultEvidenceMaxCharactersPerUnit {
		t.Fatalf("content was not bounded: bytes=%d chars=%d", len([]byte(unit.Content)), len([]rune(unit.Content)))
	}
	if !unit.Truncated {
		t.Fatal("truncated evidence unit did not preserve its truncation marker")
	}
	if result.Result.Manifest.Execution.Metrics.Limited == 0 || !hasGapCode(result.Result.Manifest.Gaps, "evidence_limited") {
		t.Fatalf("truncation was not observable in manifest: %#v", result.Result.Manifest.Execution.Metrics)
	}
	if counter.Load() != 1 {
		t.Fatalf("analyzer calls = %d, want 1", counter.Load())
	}
}

func hasGapCode(gaps []contract.Gap, code string) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}

func TestRunWithEvidenceRequiresExplicitOrganizationAndRejectsNegativeLimits(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", "fixture\n")
	runner := newEvidenceTestRunner(t, &draftAnalyzer{content: "fixture"})

	_, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source: contract.Source{ID: "evidence-source", Name: "fixture", Type: "filesystem"},
		Root:   root,
	})
	if !errors.Is(err, analysis.ErrInvalidEvidence) {
		t.Fatalf("missing organization error = %v, want ErrInvalidEvidence", err)
	}

	_, err = runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-explicit",
		EvidenceLimits: analysis.EvidenceLimits{MaxBytesPerUnit: -1},
	})
	if !errors.Is(err, analysis.ErrEvidenceLimitExceeded) {
		t.Fatalf("negative evidence limit error = %v, want ErrEvidenceLimitExceeded", err)
	}
}

func TestRunWithEvidenceHandlesByteLimitInsideUTF8Rune(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", "fixture\n")
	runner := newEvidenceTestRunner(t, &draftAnalyzer{content: "é"})
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "utf8-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-utf8",
		EvidenceLimits: analysis.EvidenceLimits{MaxBytesPerUnit: 1, MaxCharactersPerUnit: 1},
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].ContentState != evidence.ContentStateOmitted {
		t.Fatalf("UTF-8 bounded evidence = %#v, want one omitted unit", result.Evidence)
	}
}

func TestRunWithEvidenceRedactionKeepsOriginalSnippetHash(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", "fixture\n")
	const secret = "password=super-secret"
	runner := newEvidenceTestRunner(t, &draftAnalyzer{content: secret})
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "redaction-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-redaction",
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("evidence units = %d, want 1", len(result.Evidence))
	}
	unit := result.Evidence[0]
	if unit.ContentState != evidence.ContentStateRedacted || strings.Contains(unit.Content, "super-secret") {
		t.Fatalf("redacted unit = %#v", unit)
	}
	if unit.ContentHash != evidence.ContentDigest(secret) {
		t.Fatalf("redacted hash = %q, want original snippet hash %q", unit.ContentHash, evidence.ContentDigest(secret))
	}
}

func TestRunWithEvidenceReprocessesLegacyCacheForDrafts(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "notes.txt", "fixture\n")
	output := t.TempDir()
	counter := &atomic.Int32{}
	runner := newEvidenceTestRunner(t, &draftAnalyzer{counter: counter, content: "fixture"})
	config := analysis.Config{
		Source:         contract.Source{ID: "cache-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		Output:         output,
		OrganizationID: "org-explicit",
	}

	first, err := runner.RunWithEvidence(context.Background(), config)
	if err != nil {
		t.Fatalf("first RunWithEvidence() error = %v", err)
	}
	second, err := runner.RunWithEvidence(context.Background(), config)
	if err != nil {
		t.Fatalf("second RunWithEvidence() error = %v", err)
	}
	if len(first.Evidence) != 1 || len(second.Evidence) != 1 {
		t.Fatalf("evidence units = %d/%d, want one on each run", len(first.Evidence), len(second.Evidence))
	}
	if counter.Load() != 2 {
		t.Fatalf("analyzer calls = %d, want cache reprocessing on second evidence run", counter.Load())
	}
	if second.Result.Manifest.Execution.Metrics.Reused != 0 || second.Result.Manifest.Execution.Metrics.Reprocessed != 1 {
		t.Fatalf("second metrics = %#v, want reprocessed evidence", second.Result.Manifest.Execution.Metrics)
	}
	if !strings.Contains(strings.Join(second.Result.Manifest.Execution.Metrics.Limitations, "\n"), "evidence_reprocessed_without_cache") {
		t.Fatalf("limitations = %#v, missing evidence cache limitation", second.Result.Manifest.Execution.Metrics.Limitations)
	}
}

func newEvidenceTestRunner(t *testing.T, analyzer analysis.Analyzer) *analysis.Runner {
	t.Helper()
	registry, err := analysis.NewRegistry(analyzer)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

type draftAnalyzer struct {
	counter      *atomic.Int32
	content      string
	originalHash string
}

func (a *draftAnalyzer) Descriptor() analysis.Descriptor {
	return analysis.Descriptor{
		ID:              "draft-test",
		Version:         "1",
		Method:          "draft-v1",
		ContractVersion: contract.Version,
		ArtifactTypes:   []string{analysis.ArtifactTypeAny},
	}
}

func (a *draftAnalyzer) Analyze(ctx context.Context, input analysis.ArtifactInput) (analysis.Output, error) {
	if err := ctx.Err(); err != nil {
		return analysis.Output{}, err
	}
	if a.counter != nil {
		a.counter.Add(1)
	}
	contribution, err := analysis.NewContribution(
		input,
		a.Descriptor(),
		"draft:"+input.Artifact.Path,
		"test.evidence",
		contract.Locator{Path: input.Artifact.Path, StartLine: 1, EndLine: 1},
		map[string]string{"path": input.Artifact.Path},
	)
	if err != nil {
		return analysis.Output{}, err
	}
	return analysis.Output{
		Contributions: []contract.Contribution{contribution},
		Evidence: []analysis.EvidenceDraft{{
			ContributionID: contribution.ID,
			Locator:        contribution.Locator,
			Content:        a.content,
			OriginalHash:   a.originalHash,
		}},
	}, nil
}
