package persistence

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
)

func TestQueryEvidenceUnitRepositoryResolveKeepsOmittedContentOmitted(t *testing.T) {
	scope := query.Scope{
		OrganizationID: identity.CanonicalUUID("organization", "local"),
		SourceID:       testUUID(201),
		SnapshotID:     testUUID(202),
	}
	artifactID := testUUID(203)
	observationID := testUUID(204)
	locator := contract.Locator{SourceID: scope.SourceID, ArtifactID: artifactID, Path: "src/secret.go", StartLine: 1, EndLine: 1}
	locatorJSON, err := json.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ArtifactID:     artifactID,
		Contribution: evidence.ContributionRef{
			ID:              "observation-secret",
			ArtifactID:      artifactID,
			AnalyzerID:      "generic",
			AnalyzerVersion: "v1",
			Method:          "paragraph",
		},
		Locator:          locator,
		ContentState:     evidence.ContentStateOmitted,
		ContentHash:      strings.Repeat("a", 64),
		Persist:          evidence.DecisionAllow,
		ExternalTransfer: evidence.DecisionDeny,
		Classification:   evidence.ClassificationProhibited,
		Findings:         []string{evidence.FindingProhibited},
	}
	unit.ID = evidence.EvidenceID(unit)
	findingsJSON, _ := json.Marshal(unit.Findings)
	provenanceJSON := []byte(`{}`)
	tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
		testUUID(205), scope.OrganizationID, scope.SourceID, scope.SnapshotID, artifactID, observationID,
		unit.ID, "secret.go", "source-secret", unit.Contribution.ID, unit.Contribution.AnalyzerID,
		unit.Contribution.AnalyzerVersion, unit.Contribution.Method, locatorJSON,
		string(unit.ContentState), nil, unit.ContentHash, int64(0), int64(0), false,
		string(unit.Classification), findingsJSON, string(unit.Persist), string(unit.ExternalTransfer), nil, provenanceJSON,
	}}}
	repository := newQueryEvidenceUnitRepositoryWithStarter(&queryExecutionStarter{tx: tx})

	got, err := repository.Resolve(context.Background(), scope, testUUID(205))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got.Content != "" || got.ContentState != evidence.ContentStateOmitted || got.ExternalTransfer != evidence.DecisionDeny {
		t.Fatalf("resolved omitted evidence = %#v", got)
	}
	if got.ContentHash != unit.ContentHash || got.Locator.Path != locator.Path || got.ID == testUUID(205) {
		t.Fatalf("resolved evidence identity = %#v", got)
	}
	if !tx.committed || tx.rollbackCalls != 0 {
		t.Fatalf("transaction state: committed=%v rollbacks=%d", tx.committed, tx.rollbackCalls)
	}
}

func TestQueryEvidenceUnitRepositoryResolveEvidenceUnitPreservesExternalID(t *testing.T) {
	scope := query.Scope{
		OrganizationID: identity.CanonicalUUID("organization", "local"),
		SourceID:       testUUID(211),
		SnapshotID:     testUUID(212),
	}
	artifactID := testUUID(213)
	observationID := testUUID(214)
	locator := contract.Locator{SourceID: scope.SourceID, ArtifactID: artifactID, Path: "src/identity.go", StartLine: 1, EndLine: 1}
	locatorJSON, err := json.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ArtifactID:     artifactID,
		Contribution: evidence.ContributionRef{
			ID:              "observation-identity",
			ArtifactID:      artifactID,
			AnalyzerID:      "generic",
			AnalyzerVersion: "v1",
			Method:          "paragraph",
		},
		Locator:          locator,
		ContentState:     evidence.ContentStateOmitted,
		ContentHash:      strings.Repeat("b", 64),
		Persist:          evidence.DecisionAllow,
		ExternalTransfer: evidence.DecisionDeny,
		Classification:   evidence.ClassificationProhibited,
		Findings:         []string{evidence.FindingProhibited},
	}
	unit.ID = evidence.EvidenceID(unit)
	const externalID = "bundle-evidence-identity"
	findingsJSON, _ := json.Marshal(unit.Findings)
	tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
		testUUID(215), scope.OrganizationID, scope.SourceID, scope.SnapshotID, artifactID, observationID,
		externalID, "identity.go", "source-identity", unit.Contribution.ID, unit.Contribution.AnalyzerID,
		unit.Contribution.AnalyzerVersion, unit.Contribution.Method, locatorJSON,
		string(unit.ContentState), nil, unit.ContentHash, int64(0), int64(0), false,
		string(unit.Classification), findingsJSON, string(unit.Persist), string(unit.ExternalTransfer), nil, []byte(`{}`),
	}}}
	repository := newQueryEvidenceUnitRepositoryWithStarter(&queryExecutionStarter{tx: tx})

	got, err := repository.ResolveEvidenceUnit(context.Background(), scope, testUUID(215))
	if err != nil {
		t.Fatalf("ResolveEvidenceUnit() error = %v", err)
	}
	if got.ExternalID != externalID || got.Unit.ID == externalID || got.Unit.ID != unit.ID {
		t.Fatalf("resolved identity = external %q, unit %q; want external %q, projected %q", got.ExternalID, got.Unit.ID, externalID, unit.ID)
	}
	if !tx.committed || tx.rollbackCalls != 0 {
		t.Fatalf("transaction state: committed=%v rollbacks=%d", tx.committed, tx.rollbackCalls)
	}
}

func TestQueryEvidenceUnitRepositoryResolveCanonicalizesPersistedExternalLocator(t *testing.T) {
	scope := query.Scope{
		OrganizationID: identity.CanonicalUUID("organization", "external-locator"),
		SourceID:       testUUID(221),
		SnapshotID:     testUUID(222),
	}
	artifactID := testUUID(223)
	observationID := testUUID(224)
	const (
		sourceExternalID      = "source-external-locator"
		artifactExternalID    = "artifact-external-locator"
		observationExternalID = "observation-external-locator"
	)
	storedLocator := contract.Locator{
		SourceID: sourceExternalID, ArtifactID: artifactExternalID,
		Path: "src/external-locator.go", StartLine: 1, EndLine: 1,
	}
	locatorJSON, err := json.Marshal(storedLocator)
	if err != nil {
		t.Fatal(err)
	}
	content := "safe external locator excerpt"
	contentHash := evidence.ContentDigest(content)
	findingsJSON := []byte("[]")
	const externalID = "bundle-evidence-external-locator"
	tx := &queryExecutionTransaction{row: queryExecutionRow{values: []any{
		testUUID(225), scope.OrganizationID, scope.SourceID, scope.SnapshotID, artifactID, observationID,
		externalID, artifactExternalID, sourceExternalID, observationExternalID, "generic", "v1", "paragraph", locatorJSON,
		string(evidence.ContentStatePresent), content, contentHash, int64(len(content)), int64(len(content)), false,
		string(evidence.ClassificationSafeText), findingsJSON, string(evidence.DecisionAllow), string(evidence.DecisionAllow), nil, []byte(`{}`),
	}}}
	repository := newQueryEvidenceUnitRepositoryWithStarter(&queryExecutionStarter{tx: tx})

	got, err := repository.ResolveEvidenceUnit(context.Background(), scope, testUUID(225))
	if err != nil {
		t.Fatalf("ResolveEvidenceUnit() error = %v", err)
	}
	if got.ExternalID != externalID || got.Unit.ID == externalID {
		t.Fatalf("resolved identity = external %q, unit %q", got.ExternalID, got.Unit.ID)
	}
	if got.Unit.Locator.SourceID != scope.SourceID || got.Unit.Locator.ArtifactID != artifactID || got.Unit.Locator.Path != storedLocator.Path {
		t.Fatalf("resolved locator = %#v, want canonical scope with preserved path", got.Unit.Locator)
	}
	if err := got.Unit.ValidatePrepared(); err != nil {
		t.Fatalf("resolved unit validation error = %v", err)
	}
	if !tx.committed || tx.rollbackCalls != 0 {
		t.Fatalf("transaction state: committed=%v rollbacks=%d", tx.committed, tx.rollbackCalls)
	}
}
