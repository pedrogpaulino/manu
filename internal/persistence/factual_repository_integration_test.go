//go:build integration

package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestFactualRepositoryPersistsRetryAndValues(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-repository", "snapshot-1", "class FactualRepository {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}

	input := factualRepositoryInput(t, fixture, "evidence-present")
	prepared, err := persistence.PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("prepare factual input: %v", err)
	}
	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("first factual persistence: %v", err)
	}

	assertFactualRepositoryCounts(t, database, input, factualRepositoryCounts{
		manifests: 1, schemas: 1, rules: 1, facts: 2, qualifiers: 1, evidence: 2, inputs: 1,
	})
	assertFactualRepositoryFactRows(t, database, input, prepared)

	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("identical factual retry: %v", err)
	}
	assertFactualRepositoryCounts(t, database, input, factualRepositoryCounts{
		manifests: 1, schemas: 1, rules: 1, facts: 2, qualifiers: 1, evidence: 2, inputs: 1,
	})
}

func TestFactualRepositoryRejectsImmutableConflicts(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-conflict", "snapshot-1", "class FactualConflict {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	input := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("first factual persistence: %v", err)
	}
	wantCounts := factualRepositoryCounts{manifests: 1, schemas: 1, rules: 1, facts: 2, qualifiers: 1, evidence: 2, inputs: 1}

	manifestConflict := input
	manifestConflict.FrontendManifests = append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	manifestConflict.FrontendManifests[0].Limitations = []string{"changed-but-valid"}
	if err := repository.PersistFactualSnapshot(context.Background(), manifestConflict); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("manifest conflict error = %v, want ErrConflict", err)
	}
	assertFactualRepositoryCounts(t, database, input, wantCounts)

	ruleConflict := input
	ruleConflict.RuleVersions = append([]persistence.RuleVersion(nil), input.RuleVersions...)
	ruleConflict.RuleVersions[0].ImplementationDigest = strings.Repeat("f", 64)
	ruleConflict.RuleVersions[0].Configuration = json.RawMessage(`{"changed":true}`)
	if err := repository.PersistFactualSnapshot(context.Background(), ruleConflict); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("rule conflict error = %v, want ErrConflict", err)
	}
	assertFactualRepositoryCounts(t, database, input, wantCounts)
}

func TestFactualRepositoryLateEvidenceFailureRollsBack(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-late-failure", "snapshot-1", "class FactualLateFailure {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}

	input := factualRepositoryInput(t, fixture, "missing-evidence")
	err := repository.PersistFactualSnapshot(context.Background(), input)
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("missing evidence error = %v, want ErrNotFound", err)
	}
	assertFactualRepositoryCounts(t, database, input, factualRepositoryCounts{})
}

func TestFactualRepositoryScopesIdentitiesAndRejectsMismatchedRelationalIDs(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	first := integrationFixture(t, "factual-isolation-first", "snapshot-1", "class FactualIsolationFirst {}")
	second := integrationFixture(t, "factual-isolation-second", "snapshot-1", "class FactualIsolationSecond {}")
	repository := persistence.NewRepository(database.pool)
	firstInput := factualRepositoryInput(t, first, "evidence-present")
	secondInput := factualRepositoryInput(t, second, "evidence-present")
	for _, fixture := range []struct {
		data  integrationFixtureData
		input persistence.FactualSnapshotInput
	}{{first, firstInput}, {second, secondInput}} {
		if _, err := repository.PersistBundle(context.Background(), fixture.data.input); err != nil {
			t.Fatalf("seed %s canonical bundle: %v", fixture.data.organizationID, err)
		}
		if err := repository.PersistFactualSnapshot(context.Background(), fixture.input); err != nil {
			t.Fatalf("persist %s factual slice: %v", fixture.data.organizationID, err)
		}
	}
	assertFactualRepositoryCounts(t, database, firstInput, factualRepositoryCounts{manifests: 1, schemas: 1, rules: 1, facts: 2, qualifiers: 1, evidence: 2, inputs: 1})
	assertFactualRepositoryCounts(t, database, secondInput, factualRepositoryCounts{manifests: 1, schemas: 1, rules: 1, facts: 2, qualifiers: 1, evidence: 2, inputs: 1})

	bad := factualRepositoryInput(t, integrationFixture(t, "factual-prevalidation", "snapshot-1", "class FactualPrevalidation {}"), "evidence-present")
	bad.SourceID = identity.CanonicalUUID("source", bad.Scope.OrganizationID, "wrong-source")
	if err := repository.PersistFactualSnapshot(context.Background(), bad); !errors.Is(err, persistence.ErrInvalidFactualSnapshot) {
		t.Fatalf("relational scope mismatch error = %v, want ErrInvalidFactualSnapshot", err)
	}
}

func TestFactualRepositoryRejectsMissingLineageInputBeforeTransaction(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-lineage-invalid", "snapshot-1", "class FactualLineageInvalid {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	input := factualRepositoryInput(t, fixture, "evidence-present")
	input.Facts[1].Lineage = &fact.Lineage{RuleID: "dependency", RuleVersion: "1", InputFactIDs: []string{"fact-does-not-exist"}}
	input.Facts[1].ID = mustFactualRepositoryFactID(t, input.Facts[1])
	if err := repository.PersistFactualSnapshot(context.Background(), input); !errors.Is(err, persistence.ErrInvalidFactualSnapshot) {
		t.Fatalf("invalid lineage error = %v, want ErrInvalidFactualSnapshot", err)
	}
	assertFactualRepositoryCounts(t, database, input, factualRepositoryCounts{})
}

func factualRepositoryInput(t *testing.T, fixture integrationFixtureData, evidenceID string) persistence.FactualSnapshotInput {
	t.Helper()
	if evidenceID == "evidence-present" {
		evidenceID = fixture.input.Evidence[0].ID
	}
	scope := fact.Scope{
		OrganizationID: fixture.input.Manifest.Organization.ID,
		SourceID:       fixture.input.Manifest.Source.ID,
		SnapshotID:     fixture.input.Manifest.Snapshot.ID,
	}
	manifest := fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              "java-frontend",
		Version:         "1",
		Method:          "symbols",
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"java"},
		Versions:        []string{"17"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships},
		Limitations:     []string{"safe-static"},
		Predicates:      []fact.Predicate{fact.PredicateDefinition, fact.PredicateReference},
		Execution:       fact.ExecutionProfileSafeStatic,
		Extensions: []fact.ExtensionSchema{{
			ID: "java-schema", Version: "1", Digest: strings.Repeat("a", 64),
		}},
	}
	rule := persistence.RuleVersion{
		RuleID: "dependency", Version: "1", ImplementationDigest: strings.Repeat("b", 64),
		Configuration: json.RawMessage(`{"mode":"deterministic"}`),
	}
	locator := fixture.input.Evidence[0].Locator
	observed := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "IntegrationType"},
		Qualifiers: []fact.Qualifier{{
			Name: "origin", Value: fact.TypedValue{Kind: fact.ValueString, String: "observed"},
		}},
		Producer: fact.Producer{ID: manifest.ID, Version: manifest.Version, Method: manifest.Method},
		Evidence: []fact.EvidenceRef{{ID: evidenceID, Locator: locator}},
	}
	observed.ID = mustFactualRepositoryFactID(t, observed)
	derived := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDependency,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "IntegrationType"},
		Object:    &fact.Participant{Kind: fact.ParticipantSymbol, ID: "IntegrationDependency"},
		Producer:  fact.Producer{ID: "rule-engine", Version: "1", Method: "dependency"},
		Evidence:  []fact.EvidenceRef{{ID: evidenceID, Locator: locator}},
		Lineage:   &fact.Lineage{RuleID: rule.RuleID, RuleVersion: rule.Version, InputFactIDs: []string{observed.ID}},
	}
	derived.ID = mustFactualRepositoryFactID(t, derived)
	return persistence.FactualSnapshotInput{
		OrganizationID:    fixture.job.OrganizationID,
		SourceID:          fixture.job.SourceID,
		SnapshotID:        fixture.job.SnapshotID,
		Scope:             scope,
		FrontendManifests: []fact.FrontendManifest{manifest},
		RuleVersions:      []persistence.RuleVersion{rule},
		Facts:             []fact.CanonicalFact{observed, derived},
	}
}

func mustFactualRepositoryFactID(t *testing.T, candidate fact.CanonicalFact) string {
	t.Helper()
	id, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("derive factual fact ID: %v", err)
	}
	return id
}

type factualRepositoryCounts struct {
	manifests  int
	schemas    int
	rules      int
	facts      int
	qualifiers int
	evidence   int
	inputs     int
}

func assertFactualRepositoryCounts(t *testing.T, database *postgresIntegrationDatabase, input persistence.FactualSnapshotInput, expected factualRepositoryCounts) {
	t.Helper()
	queries := []struct {
		name  string
		table string
		want  int
	}{
		{name: "frontend manifests", table: "frontend_manifests", want: expected.manifests},
		{name: "extension schemas", table: "frontend_extension_schemas", want: expected.schemas},
		{name: "canonical facts", table: "canonical_facts", want: expected.facts},
		{name: "qualifiers", table: "canonical_fact_qualifiers", want: expected.qualifiers},
		{name: "evidence links", table: "canonical_fact_evidence", want: expected.evidence},
		{name: "lineage inputs", table: "canonical_fact_inputs", want: expected.inputs},
	}
	for _, query := range queries {
		var got int
		if err := database.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+query.table+" WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid", input.OrganizationID, input.SourceID, input.SnapshotID).Scan(&got); err != nil {
			t.Fatalf("count %s: %v", query.name, err)
		}
		if got != query.want {
			t.Fatalf("count %s = %d, want %d", query.name, got, query.want)
		}
	}
	var gotRules int
	if err := database.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM rule_versions WHERE organization_id = $1::uuid", input.OrganizationID).Scan(&gotRules); err != nil {
		t.Fatalf("count rule versions: %v", err)
	}
	if gotRules != expected.rules {
		t.Fatalf("count rule versions = %d, want %d", gotRules, expected.rules)
	}
}

func assertFactualRepositoryFactRows(t *testing.T, database *postgresIntegrationDatabase, input persistence.FactualSnapshotInput, prepared persistence.PreparedFactualSnapshot) {
	t.Helper()
	var observedKind, observedFrontend string
	var observedRule *string
	var observedAt time.Time
	observed := preparedFactByExternalID(t, prepared, input.Facts[0].ID)
	if err := database.pool.QueryRow(context.Background(), `
SELECT fact_kind, frontend_manifest_id::text, rule_version_id::text, observed_at
FROM canonical_facts
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid AND id = $4::uuid`,
		input.OrganizationID, input.SourceID, input.SnapshotID, observed.ID,
	).Scan(&observedKind, &observedFrontend, &observedRule, &observedAt); err != nil {
		t.Fatalf("read observed canonical fact: %v", err)
	}
	if observedKind != "observed" || observedFrontend != prepared.FrontendManifests[0].ID || observedRule != nil || !observedAt.Equal(inputSnapshotCapturedAt(t, database, input)) {
		t.Fatalf("observed row kind/frontend/rule/time = %s/%s/%v/%s", observedKind, observedFrontend, observedRule, observedAt)
	}

	derived := preparedFactByKind(t, prepared, "derived")
	var derivedKind string
	var derivedFrontend *string
	var derivedRule *string
	if err := database.pool.QueryRow(context.Background(), `
SELECT fact_kind, frontend_manifest_id::text, rule_version_id::text
FROM canonical_facts
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid AND id = $4::uuid`,
		input.OrganizationID, input.SourceID, input.SnapshotID, derived.ID,
	).Scan(&derivedKind, &derivedFrontend, &derivedRule); err != nil {
		t.Fatalf("read derived canonical fact: %v", err)
	}
	wantRuleID := identity.CanonicalUUID("rule-version", input.Scope.OrganizationID, "dependency", "1")
	if derivedKind != "derived" || derivedFrontend != nil || derivedRule == nil || *derivedRule != wantRuleID {
		t.Fatalf("derived row kind/frontend/rule = %s/%v/%v", derivedKind, derivedFrontend, derivedRule)
	}

	var inputFactID, inputRuleID, inputKind string
	if err := database.pool.QueryRow(context.Background(), `
SELECT input_fact_id::text, rule_version_id::text, fact_kind
FROM canonical_fact_inputs
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid AND fact_id = $4::uuid AND ordinal = 0`,
		input.OrganizationID, input.SourceID, input.SnapshotID, derived.ID,
	).Scan(&inputFactID, &inputRuleID, &inputKind); err != nil {
		t.Fatalf("read derived lineage input: %v", err)
	}
	if inputFactID != observed.ID || inputRuleID != wantRuleID || inputKind != "derived" {
		t.Fatalf("lineage input = %s/%s/%s, want %s/%s/derived", inputFactID, inputRuleID, inputKind, observed.ID, wantRuleID)
	}

	var evidenceUnitID, qualifierName, qualifierValue string
	if err := database.pool.QueryRow(context.Background(), `
SELECT e.evidence_unit_id::text, q.name, q.typed_value::text
FROM canonical_fact_evidence e
JOIN canonical_fact_qualifiers q
  ON q.organization_id = e.organization_id AND q.source_id = e.source_id
 AND q.snapshot_id = e.snapshot_id AND q.fact_id = e.fact_id
WHERE e.organization_id = $1::uuid AND e.source_id = $2::uuid AND e.snapshot_id = $3::uuid AND e.fact_id = $4::uuid AND e.ordinal = 0 AND q.ordinal = 0`,
		input.OrganizationID, input.SourceID, input.SnapshotID, observed.ID,
	).Scan(&evidenceUnitID, &qualifierName, &qualifierValue); err != nil {
		t.Fatalf("read factual support rows: %v", err)
	}
	wantEvidenceID := identity.CanonicalUUID("evidence", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, input.Facts[0].Evidence[0].ID)
	if evidenceUnitID != wantEvidenceID || qualifierName != "origin" || !strings.Contains(qualifierValue, `"observed"`) {
		t.Fatalf("support rows = %s/%s/%s", evidenceUnitID, qualifierName, qualifierValue)
	}
}

func inputSnapshotCapturedAt(t *testing.T, database *postgresIntegrationDatabase, input persistence.FactualSnapshotInput) time.Time {
	t.Helper()
	var capturedAt time.Time
	if err := database.pool.QueryRow(context.Background(), "SELECT captured_at FROM analysis_snapshots WHERE organization_id = $1::uuid AND source_id = $2::uuid AND id = $3::uuid", input.OrganizationID, input.SourceID, input.SnapshotID).Scan(&capturedAt); err != nil {
		t.Fatalf("read snapshot captured_at: %v", err)
	}
	return capturedAt
}

func preparedFactByExternalID(t *testing.T, prepared persistence.PreparedFactualSnapshot, externalID string) persistence.PreparedCanonicalFact {
	t.Helper()
	for _, candidate := range prepared.Facts {
		if candidate.ExternalID == externalID {
			return candidate
		}
	}
	t.Fatalf("prepared fact %q not found", externalID)
	return persistence.PreparedCanonicalFact{}
}

func preparedFactByKind(t *testing.T, prepared persistence.PreparedFactualSnapshot, kind string) persistence.PreparedCanonicalFact {
	t.Helper()
	for _, candidate := range prepared.Facts {
		if candidate.Kind == kind {
			return candidate
		}
	}
	t.Fatalf("prepared fact kind %q not found", kind)
	return persistence.PreparedCanonicalFact{}
}
