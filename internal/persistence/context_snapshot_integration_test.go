//go:build integration

package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
)

func TestReadContextSnapshotRoundTripsCanonicalFactualState(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "context-snapshot", "snapshot-1", "class ContextSnapshot {}")
	coverageLocator := &contract.Locator{
		SourceID: fixture.input.Manifest.Source.ID, ArtifactID: fixture.input.Artifacts[0].ID,
		Path: fixture.input.Artifacts[0].Path, StartLine: 2, EndLine: 4,
	}
	coverage := contract.Coverage{
		Dimension: string(contract.DimensionEntitiesAndRelationships), Scope: "source",
		State: contract.CoverageProduced, AnalyzerID: "integration-java", Message: "entity coverage",
		Locator: coverageLocator,
	}
	coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
	gapLocator := &contract.Locator{
		SourceID: fixture.input.Manifest.Source.ID, ArtifactID: fixture.input.Artifacts[0].ID,
		Path: fixture.input.Artifacts[0].Path, StartLine: 8, EndLine: 10,
	}
	gap := contract.Gap{
		Code: "missing-dependency-analysis", Dimension: string(contract.DimensionFlowsAndDependencies),
		Scope: "source", Message: "dependency coverage is incomplete", AnalyzerID: "integration-java",
		Locator: gapLocator,
	}
	gap.ID = contract.GapID(gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID)
	fixture.input.Manifest.Coverage = []contract.Coverage{coverage}
	fixture.input.Manifest.Gaps = []contract.Gap{gap}
	factualDigest, err := bundle.FactualDigest(contract.Result{
		Manifest: fixture.input.Manifest.Manifest, Artifacts: fixture.input.Artifacts, Contributions: fixture.input.Contributions,
	}, fixture.input.Evidence)
	if err != nil {
		t.Fatalf("recalculate factual digest: %v", err)
	}
	fixture.input.Manifest.FactualDigest = factualDigest
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	input := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("persist factual snapshot: %v", err)
	}
	wantFactual, err := repository.ReadFactualSnapshot(context.Background(), input.OrganizationID, input.SourceID, input.SnapshotID)
	if err != nil {
		t.Fatalf("read expected factual snapshot: %v", err)
	}
	scope := domainquery.Scope{
		OrganizationID: input.OrganizationID,
		SourceID:       input.SourceID,
		SnapshotID:     input.SnapshotID,
	}

	got, err := repository.ReadContextSnapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("ReadContextSnapshot() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("ReadContextSnapshot() result validation error = %v", err)
	}
	if got.Scope != scope {
		t.Fatalf("context snapshot scope = %#v, want %#v", got.Scope, scope)
	}
	if got.Revision != "revision-snapshot-1" {
		t.Fatalf("context snapshot revision = %q, want %q", got.Revision, "revision-snapshot-1")
	}
	if !reflect.DeepEqual(got.Facts, wantFactual.Facts) {
		t.Fatalf("context snapshot facts differ from factual read:\n got %#v\nwant %#v", got.Facts, wantFactual.Facts)
	}
	expectedArtifactID := identity.CanonicalUUID("artifact", fixture.organizationID, fixture.sourceID, fixture.input.Manifest.Snapshot.ID, fixture.input.Artifacts[0].ID)
	expectedCoverage := coverage
	expectedCoverage.Locator = &contract.Locator{
		SourceID: scope.SourceID, ArtifactID: expectedArtifactID,
		Path: coverageLocator.Path, StartLine: coverageLocator.StartLine, EndLine: coverageLocator.EndLine,
	}
	expectedGap := gap
	expectedGap.Locator = &contract.Locator{
		SourceID: scope.SourceID, ArtifactID: expectedArtifactID,
		Path: gapLocator.Path, StartLine: gapLocator.StartLine, EndLine: gapLocator.EndLine,
	}
	if !reflect.DeepEqual(got.Coverage, []contract.Coverage{expectedCoverage}) {
		t.Fatalf("context snapshot coverage = %#v, want %#v", got.Coverage, []contract.Coverage{expectedCoverage})
	}
	if !reflect.DeepEqual(got.Gaps, []contract.Gap{expectedGap}) {
		t.Fatalf("context snapshot gaps = %#v, want %#v", got.Gaps, []contract.Gap{expectedGap})
	}

	got.Facts[0].Subject.ID = "mutated-context-snapshot-subject"
	if len(got.Facts[0].Qualifiers) > 0 {
		got.Facts[0].Qualifiers[0].Value.String = "mutated-context-snapshot-qualifier"
	}
	got.Coverage[0].Locator.SourceID = "mutated-context-snapshot-source"
	got.Coverage[0].Locator.Path = "mutated-context-snapshot-path"
	got.Gaps[0].Locator.ArtifactID = "mutated-context-snapshot-artifact"
	again, err := repository.ReadContextSnapshot(context.Background(), scope)
	if err != nil {
		t.Fatalf("ReadContextSnapshot() after mutation error = %v", err)
	}
	if !reflect.DeepEqual(again.Facts, wantFactual.Facts) {
		t.Fatalf("context snapshot read was affected by caller mutation:\n got %#v\nwant %#v", again.Facts, wantFactual.Facts)
	}
	if !reflect.DeepEqual(again.Coverage, []contract.Coverage{expectedCoverage}) || !reflect.DeepEqual(again.Gaps, []contract.Gap{expectedGap}) {
		t.Fatalf("context snapshot locators were affected by caller mutation: coverage=%#v gaps=%#v", again.Coverage, again.Gaps)
	}
}

func TestReadContextSnapshotRejectsCorruptCoverageLocator(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "context-snapshot-corrupt", "snapshot-1", "class ContextSnapshotCorrupt {}")
	coverageLocator := &contract.Locator{
		SourceID: fixture.input.Manifest.Source.ID, ArtifactID: fixture.input.Artifacts[0].ID,
		Path: fixture.input.Artifacts[0].Path, StartLine: 3, EndLine: 5,
	}
	coverage := contract.Coverage{
		Dimension: string(contract.DimensionEntitiesAndRelationships), Scope: "source",
		State: contract.CoverageProduced, AnalyzerID: "integration-java", Message: "entity coverage",
		Locator: coverageLocator,
	}
	coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
	fixture.input.Manifest.Coverage = []contract.Coverage{coverage}
	factualDigest, err := bundle.FactualDigest(contract.Result{
		Manifest: fixture.input.Manifest.Manifest, Artifacts: fixture.input.Artifacts, Contributions: fixture.input.Contributions,
	}, fixture.input.Evidence)
	if err != nil {
		t.Fatalf("recalculate factual digest: %v", err)
	}
	fixture.input.Manifest.FactualDigest = factualDigest
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle with coverage: %v", err)
	}
	corruptLocator, err := json.Marshal(map[string]any{
		"source_id":   "wrong-source",
		"artifact_id": fixture.input.Artifacts[0].ID,
		"path":        fixture.input.Artifacts[0].Path,
		"start_line":  3,
		"end_line":    5,
	})
	if err != nil {
		t.Fatalf("marshal corrupt locator: %v", err)
	}
	if _, err := database.pool.Exec(context.Background(), `
UPDATE analysis_coverage
SET locator = $1::jsonb
WHERE organization_id = $2::uuid AND source_id = $3::uuid AND snapshot_id = $4::uuid AND external_id = $5`,
		corruptLocator, fixture.job.OrganizationID, fixture.job.SourceID, fixture.job.SnapshotID, coverage.ID); err != nil {
		t.Fatalf("corrupt coverage locator: %v", err)
	}

	scope := domainquery.Scope{
		OrganizationID: fixture.job.OrganizationID,
		SourceID:       fixture.job.SourceID,
		SnapshotID:     fixture.job.SnapshotID,
	}
	got, err := repository.ReadContextSnapshot(context.Background(), scope)
	if !errors.Is(err, persistence.ErrInconsistent) {
		t.Fatalf("ReadContextSnapshot() error = %v, want ErrInconsistent", err)
	}
	if !reflect.DeepEqual(got, domainquery.ContextSnapshot{}) {
		t.Fatalf("corrupt context snapshot = %#v, want zero value", got)
	}
	if strings.Contains(err.Error(), "wrong-source") || strings.Contains(err.Error(), coverage.ID) || len(err.Error()) > 160 {
		t.Fatalf("corrupt coverage error leaked details: %v", err)
	}
}

func TestReadContextSnapshotReturnsNotFoundWithoutScopeLeak(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "context-snapshot-missing", "snapshot-1", "class ContextSnapshotMissing {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	missingSnapshotID := identity.CanonicalUUID("snapshot", fixture.organizationID, fixture.sourceID, "missing-snapshot")
	scope := domainquery.Scope{
		OrganizationID: fixture.job.OrganizationID,
		SourceID:       fixture.job.SourceID,
		SnapshotID:     missingSnapshotID,
	}

	got, err := repository.ReadContextSnapshot(context.Background(), scope)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("ReadContextSnapshot() error = %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(got, domainquery.ContextSnapshot{}) {
		t.Fatalf("missing context snapshot = %#v, want zero value", got)
	}
	for _, secret := range []string{missingSnapshotID, fixture.organizationID, fixture.sourceID} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("not-found error leaked scope value %q: %v", secret, err)
		}
	}
}
