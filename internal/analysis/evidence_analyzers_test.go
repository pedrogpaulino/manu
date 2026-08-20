package analysis_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/analyzer/generic"
	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestGenericEvidenceUsesBoundedTextWindow(t *testing.T) {
	root := t.TempDir()
	content := strings.Repeat("line for bounded generic evidence\n", 64)
	writeFixture(t, root, "large.txt", content)
	runner := newAnalyzerRunner(t, generic.New())
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "generic-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-generic",
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	for _, unit := range result.Evidence {
		if unit.Contribution.AnalyzerID != generic.AnalyzerID || unit.Contribution.Method != "text:large.txt" {
			continue
		}
		if unit.Content == "" || len(unit.Content) >= len(content) {
			t.Fatalf("generic evidence was not bounded: bytes=%d source=%d", len(unit.Content), len(content))
		}
		if unit.Locator.StartLine != 1 || unit.Locator.EndLine <= unit.Locator.StartLine {
			t.Fatalf("generic evidence locator = %#v, want bounded line window", unit.Locator)
		}
		return
	}
	t.Fatal("no bounded generic text evidence unit was materialized")
}

func TestJavaEvidenceIsLineBoundedAndTraceable(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, root, "Sample.java")
	runner := newAnalyzerRunner(t, generic.New(), java.New())
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "java-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-java",
		EvidenceLimits: analysis.EvidenceLimits{MaxUnitsPerArtifact: 64, MaxBytesPerUnit: 256, MaxCharactersPerUnit: 128},
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("AnalysisResult.Validate() error = %v", err)
	}
	found := 0
	for _, unit := range result.Evidence {
		if unit.Contribution.AnalyzerID != java.AnalyzerID {
			continue
		}
		found++
		if unit.Locator.StartLine <= 0 || unit.Locator.EndLine < unit.Locator.StartLine {
			t.Fatalf("Java locator = %#v, want line range", unit.Locator)
		}
		if strings.Contains(unit.Content, "package example.booking;\n\nimport") {
			t.Fatalf("Java evidence retained the complete source prefix: %q", unit.Content)
		}
		if len([]byte(unit.Content)) > 256 || unit.ContentCharacters > 128 {
			t.Fatalf("Java evidence exceeded limits: bytes=%d chars=%d", unit.ContentBytes, unit.ContentCharacters)
		}
	}
	if found == 0 {
		t.Fatal("no Java evidence units were materialized")
	}
}

func TestWSO2EvidenceKeepsStructuralLocatorWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, root, "sample.xml")
	runner := newAnalyzerRunner(t, generic.New(), wso2.New())
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "wso2-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-wso2",
		EvidenceLimits: analysis.EvidenceLimits{MaxUnitsPerArtifact: 64, MaxBytesPerUnit: 256, MaxCharactersPerUnit: 128},
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	found := 0
	for _, unit := range result.Evidence {
		if unit.Contribution.AnalyzerID != wso2.AnalyzerID {
			continue
		}
		found++
		if unit.Locator.ByteOffset < 0 {
			t.Fatalf("WSO2 locator byte offset = %d", unit.Locator.ByteOffset)
		}
		if strings.Contains(unit.Content, "<?xml") || strings.Contains(unit.Content, "should-not-leak") || strings.Contains(unit.Content, "token=") {
			t.Fatalf("WSO2 evidence leaked XML or secret material: %q", unit.Content)
		}
	}
	if found == 0 {
		t.Fatal("no WSO2 evidence units were materialized")
	}
}

func TestWSO2CAREvidenceRetainsMemberAndOffsetOnly(t *testing.T) {
	root := t.TempDir()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	member, err := writer.Create("synapse/proxy.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := member.Write([]byte(`<proxy name="InventoryProxy"><endpoint uri="https://example.test/api"/></proxy>`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "bundle.car", archive.String())
	runner := newAnalyzerRunner(t, generic.New(), wso2.New())
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "car-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-car",
		EvidenceLimits: analysis.EvidenceLimits{MaxUnitsPerArtifact: 64},
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	for _, unit := range result.Evidence {
		if unit.Contribution.AnalyzerID != wso2.AnalyzerID {
			continue
		}
		if unit.Locator.Member != "synapse/proxy.xml" {
			t.Fatalf("CAR evidence member = %q", unit.Locator.Member)
		}
		if strings.Contains(unit.Content, "<proxy") || strings.Contains(unit.Content, "<endpoint") {
			t.Fatalf("CAR evidence retained XML: %q", unit.Content)
		}
		return
	}
	t.Fatal("no WSO2 CAR evidence unit was materialized")
}

func TestBinaryEvidenceIsExplicitlyOmitted(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "payload.bin", "\x00\x01\x02\x03")
	runner := newAnalyzerRunner(t, generic.New())
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "binary-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-binary",
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	if len(result.Evidence) != 1 {
		t.Fatalf("binary evidence units = %d, want 1 omitted unit", len(result.Evidence))
	}
	unit := result.Evidence[0]
	if unit.ContentState != evidence.ContentStateOmitted || unit.Content != "" || unit.ContentBytes != 0 {
		t.Fatalf("binary evidence = %#v, want omitted without content", unit)
	}
}

func TestAnalyzerEvidenceAndContributionsDoNotRetainCredentialLiterals(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "Secrets.java", "package secrets;\nclass Secrets { String password = \"super-secret\"; }\n")
	writeFixture(t, root, "secret.xml", `<proxy><endpoint uri="https://example.test/api?token=secret-value"/></proxy>`)
	runner := newAnalyzerRunner(t, generic.New(), java.New(), wso2.New())
	result, err := runner.RunWithEvidence(context.Background(), analysis.Config{
		Source:         contract.Source{ID: "secret-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-secrets",
		EvidenceLimits: analysis.EvidenceLimits{MaxUnitsPerArtifact: 64},
	})
	if err != nil {
		t.Fatalf("RunWithEvidence() error = %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(encoded)
	for _, secret := range []string{"super-secret", "secret-value", "token=secret-value"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("analysis result retained sensitive literal %q: %s", secret, serialized)
		}
	}
}

func TestEvidenceUnitsAreDeterministicAcrossRuns(t *testing.T) {
	root := t.TempDir()
	copyFixture(t, root, "Sample.java")
	runner := newAnalyzerRunner(t, generic.New(), java.New())
	config := analysis.Config{
		Source:         contract.Source{ID: "deterministic-evidence-source", Name: "fixture", Type: "filesystem"},
		Root:           root,
		OrganizationID: "org-deterministic",
		EvidenceLimits: analysis.EvidenceLimits{MaxUnitsPerArtifact: 64},
	}
	first, err := runner.RunWithEvidence(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.RunWithEvidence(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("evidence changed across runs:\nfirst=%s\nsecond=%s", firstJSON, secondJSON)
	}
}

func newAnalyzerRunner(t *testing.T, analyzers ...analysis.Analyzer) *analysis.Runner {
	t.Helper()
	registry, err := analysis.NewRegistry(analyzers...)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		t.Fatal(err)
	}
	return runner
}
