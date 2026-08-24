//go:build integration

package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestBatchV2IntegrationPersistsLegacyAndFactualSequencesIdempotently(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "batch-v2", "snapshot-1", "class BatchV2 {}")
	input := batchIntegrationV2Bundle(t, fixture)
	repository := persistence.NewRepository(database.pool)

	first, err := repository.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("first v1alpha2 batch persistence: %v", err)
	}
	if first.FrontendManifestIDs["batch-frontend"] == "" || first.CanonicalFactIDs[input.Facts[0].ID] == "" {
		t.Fatalf("first result factual IDs = %#v", first)
	}
	assertBatchV2Counts(t, database, input, batchV2Counts{
		artifacts: 1, observations: 1, evidenceUnits: 1, coverage: 0, gaps: 1, failures: 1,
		frontendManifests: 1, facts: 1, qualifiers: 0, factEvidence: 1, inputs: 0,
	})

	var observedAt string
	var frontendID string
	if err := database.pool.QueryRow(context.Background(), `
SELECT observed_at::text, frontend_manifest_id::text
FROM canonical_facts
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid`,
		first.OrganizationID, first.SourceID, first.SnapshotID,
	).Scan(&observedAt, &frontendID); err != nil {
		t.Fatalf("read persisted factual row: %v", err)
	}
	if !strings.Contains(observedAt, "2026-08-20 12:00:00") || frontendID != first.FrontendManifestIDs["batch-frontend"] {
		t.Fatalf("factual observed_at/frontend = %q/%q", observedAt, frontendID)
	}

	if _, err := repository.PersistBundle(context.Background(), input); err != nil {
		t.Fatalf("idempotent v1alpha2 retry: %v", err)
	}
	assertBatchV2Counts(t, database, input, batchV2Counts{
		artifacts: 1, observations: 1, evidenceUnits: 1, coverage: 0, gaps: 1, failures: 1,
		frontendManifests: 1, facts: 1, qualifiers: 0, factEvidence: 1, inputs: 0,
	})
}

func TestBatchV2IntegrationRejectsFactualConflictAndIncompatibleEvidenceAtomically(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "batch-v2-conflict", "snapshot-1", "class BatchV2Conflict {}")
	input := batchIntegrationV2Bundle(t, fixture)
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), input); err != nil {
		t.Fatalf("seed v1alpha2 batch: %v", err)
	}
	want := batchV2Counts{artifacts: 1, observations: 1, evidenceUnits: 1, coverage: 0, gaps: 1, failures: 1, frontendManifests: 1, facts: 1, qualifiers: 0, factEvidence: 1, inputs: 0}

	conflict := input
	conflict.FrontendManifests = append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	conflict.FrontendManifests[0].Limitations = []string{"changed-but-valid"}
	setBatchIntegrationV2Digest(t, &conflict)
	if _, err := repository.PersistBundle(context.Background(), conflict); !errors.Is(err, persistence.ErrIncompatibleSnapshot) {
		t.Fatalf("factual conflict error = %v, want incompatible snapshot", err)
	}
	assertBatchV2Counts(t, database, input, want)

	badFixture := integrationFixture(t, "batch-v2-bad-evidence", "snapshot-1", "class BatchV2BadEvidence {}")
	bad := batchIntegrationV2Bundle(t, badFixture)
	bad.Facts[0].Evidence[0].Locator.Path = "different.java"
	setBatchIntegrationV2Digest(t, &bad)
	if _, err := repository.PersistBundle(context.Background(), bad); !errors.Is(err, bundle.ErrInvalidReference) {
		t.Fatalf("incompatible fact/evidence error = %v, want invalid reference", err)
	}
	assertBatchV2Counts(t, database, bad, batchV2Counts{})
}

type batchV2Counts struct {
	artifacts, observations, evidenceUnits, coverage, gaps, failures int
	frontendManifests, facts, qualifiers, factEvidence, inputs       int
}

func assertBatchV2Counts(t *testing.T, database *postgresIntegrationDatabase, input bundle.Bundle, want batchV2Counts) {
	t.Helper()
	organizationID := identity.CanonicalUUID("organization", input.Manifest.Organization.ID)
	sourceID := identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID)
	snapshotID := identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID)
	queries := []struct {
		name  string
		table string
		want  int
	}{
		{name: "artifacts", table: "artifacts", want: want.artifacts},
		{name: "observations", table: "observations", want: want.observations},
		{name: "evidence units", table: "evidence_units", want: want.evidenceUnits},
		{name: "coverage", table: "analysis_coverage", want: want.coverage},
		{name: "gaps", table: "explicit_gaps", want: want.gaps},
		{name: "failures", table: "analysis_failures", want: want.failures},
		{name: "frontend manifests", table: "frontend_manifests", want: want.frontendManifests},
		{name: "canonical facts", table: "canonical_facts", want: want.facts},
		{name: "qualifiers", table: "canonical_fact_qualifiers", want: want.qualifiers},
		{name: "fact evidence", table: "canonical_fact_evidence", want: want.factEvidence},
		{name: "lineage inputs", table: "canonical_fact_inputs", want: want.inputs},
	}
	for _, query := range queries {
		var got int
		if err := database.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+query.table+" WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid", organizationID, sourceID, snapshotID).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		if got != query.want {
			t.Fatalf("count %s = %d, want %d", query.name, got, query.want)
		}
	}
	var snapshots int
	if err := database.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM analysis_snapshots WHERE organization_id = $1::uuid AND source_id = $2::uuid AND id = $3::uuid", organizationID, sourceID, snapshotID).Scan(&snapshots); err != nil {
		t.Fatalf("count snapshots: %v", err)
	}
	wantSnapshots := 0
	if want.artifacts != 0 || want.observations != 0 || want.evidenceUnits != 0 || want.frontendManifests != 0 || want.facts != 0 || want.gaps != 0 || want.failures != 0 {
		wantSnapshots = 1
	}
	if snapshots != wantSnapshots {
		t.Fatalf("count snapshots = %d, want %d", snapshots, wantSnapshots)
	}
}

func batchIntegrationV2Bundle(t *testing.T, fixture integrationFixtureData) bundle.Bundle {
	t.Helper()
	input := fixture.input
	input.Manifest.Version = bundle.VersionV1Alpha2
	input.Manifest.Gaps = []contract.Gap{{
		ID:   contract.GapID("unsupported", "relationships", "batch", "not measured", "batch-frontend"),
		Code: "unsupported", Dimension: "relationships", Scope: "batch", Message: "not measured", AnalyzerID: "batch-frontend",
	}}
	input.Manifest.Failures = []contract.Failure{{
		ID:   contract.FailureID("partial", "optional", input.Artifacts[0].ID, "batch-frontend", "optional dimension failed"),
		Code: "partial", Operation: "optional", Message: "optional dimension failed", ArtifactID: input.Artifacts[0].ID, AnalyzerID: "batch-frontend", Partial: true,
	}}
	frontend := fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              "batch-frontend",
		Version:         "1",
		Method:          "symbols",
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"java"},
		Versions:        []string{"17"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships},
		Predicates:      []fact.Predicate{fact.PredicateDefinition},
		Execution:       fact.ExecutionProfileSafeStatic,
	}
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     fact.Scope{OrganizationID: input.Manifest.Organization.ID, SourceID: input.Manifest.Source.ID, SnapshotID: input.Manifest.Snapshot.ID},
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "IntegrationType"},
		Producer:  fact.Producer{ID: frontend.ID, Version: frontend.Version, Method: frontend.Method},
		Evidence:  []fact.EvidenceRef{{ID: input.Evidence[0].ID, Locator: input.Evidence[0].Locator}},
	}
	id, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("derive batch v2 fact ID: %v", err)
	}
	candidate.ID = id
	input.FrontendManifests = []fact.FrontendManifest{frontend}
	input.Facts = []fact.CanonicalFact{candidate}
	input.Extensions = []json.RawMessage{}
	input.Manifest.Counts.FrontendManifestCount = 1
	input.Manifest.Counts.CanonicalFactCount = 1
	input.Manifest.Counts.ExtensionCount = 0
	input.Manifest.Files = append(input.Manifest.Files,
		bundle.File{Name: bundle.FrontendManifestsFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("d", 64)},
		bundle.File{Name: bundle.CanonicalFactsFileName, Bytes: 1, Count: 1, Digest: strings.Repeat("d", 64)},
		bundle.File{Name: bundle.ExtensionsFileName, Bytes: 0, Count: 0, Digest: strings.Repeat("d", 64)},
	)
	input.Manifest.Limits.MaxFrontendManifests = 10
	input.Manifest.Limits.MaxCanonicalFacts = 10
	input.Manifest.Limits.MaxExtensions = 10
	setBatchIntegrationV2Digest(t, &input)
	if err := input.Validate(); err != nil {
		t.Fatalf("validate integration v1alpha2 bundle: %v", err)
	}
	return input
}

func setBatchIntegrationV2Digest(t *testing.T, input *bundle.Bundle) {
	t.Helper()
	digest, err := input.FactualDigest()
	if err != nil {
		t.Fatalf("compute integration v1alpha2 digest: %v", err)
	}
	input.Manifest.FactualDigest = digest
}
