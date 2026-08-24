//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
	"github.com/pedrogpaulino/manu/internal/persistence/migrations"
)

var factualMigrationTables = []string{
	"frontend_manifests",
	"frontend_extension_schemas",
	"rule_versions",
	"canonical_facts",
	"canonical_fact_qualifiers",
	"canonical_fact_evidence",
	"canonical_fact_inputs",
}

func TestFactualMigrationConstraintsAndScope(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)

	for _, table := range factualMigrationTables {
		assertFactualMigrationTable(t, database.pool, database.schema, table)
	}
	for _, constraint := range []struct {
		table string
		name  string
	}{
		{table: "frontend_manifests", name: "frontend_manifests_frontend_identity_unique"},
		{table: "frontend_manifests", name: "frontend_manifests_scoped_id_frontend_unique"},
		{table: "frontend_extension_schemas", name: "frontend_extension_schemas_manifest_fk"},
		{table: "rule_versions", name: "rule_versions_identity_unique"},
		{table: "canonical_facts", name: "canonical_facts_frontend_fk"},
		{table: "canonical_facts", name: "canonical_facts_observed_frontend_without_rule"},
		{table: "canonical_facts", name: "canonical_facts_derived_rule_without_frontend"},
		{table: "canonical_fact_qualifiers", name: "canonical_fact_qualifiers_ordinal_nonnegative"},
		{table: "canonical_fact_evidence", name: "canonical_fact_evidence_evidence_fk"},
		{table: "canonical_fact_evidence", name: "canonical_fact_evidence_ordinal_nonnegative"},
		{table: "canonical_fact_inputs", name: "canonical_fact_inputs_derived_fact_fk"},
		{table: "canonical_fact_inputs", name: "canonical_fact_inputs_not_self_link"},
	} {
		assertFactualMigrationConstraint(t, database.pool, database.schema, constraint.table, constraint.name)
	}

	assertIntegrationCount(t, database.pool, "manu_schema_migrations", 5)
	data := setupFactualMigrationFixture(t, database)
	assertIntegrationCount(t, database.pool, "frontend_manifests", 1)
	assertIntegrationCount(t, database.pool, "frontend_extension_schemas", 1)
	assertIntegrationCount(t, database.pool, "rule_versions", 1)
	assertIntegrationCount(t, database.pool, "canonical_facts", 3)
	assertIntegrationCount(t, database.pool, "canonical_fact_qualifiers", 1)
	assertIntegrationCount(t, database.pool, "canonical_fact_evidence", 1)
	assertIntegrationCount(t, database.pool, "canonical_fact_inputs", 1)

	// A valid snapshot with a manifest from another snapshot exercises the
	// complete scope key rather than relying on a same-artifact identifier.
	expectFactualSQLState(t, database.pool, "23503", `
INSERT INTO frontend_extension_schemas (
    id, organization_id, source_id, snapshot_id, frontend_manifest_id,
    schema_id, version, digest
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8)
`, identity.CanonicalUUID("extension-schema", data.organizationExternalID, "cross-snapshot"), data.organizationID, data.sourceID, data.otherSnapshotID, data.manifestID, "java.core", "1", integrationDigest("schema-cross-snapshot"))
	assertIntegrationCount(t, database.pool, "frontend_extension_schemas", 1)

	// The fact scope is valid, but the evidence unit belongs to the first
	// snapshot. The evidence FK must reject the row without publishing it.
	expectFactualSQLState(t, database.pool, "23503", `
INSERT INTO canonical_fact_evidence (
    id, organization_id, source_id, snapshot_id, fact_id, evidence_unit_id, ordinal
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7)
`, identity.CanonicalUUID("fact-evidence", data.organizationExternalID, "cross-snapshot"), data.organizationID, data.sourceID, data.otherSnapshotID, data.otherDerivedFactID, data.evidenceUnitID, 0)
	assertIntegrationCount(t, database.pool, "canonical_fact_evidence", 1)

	// Likewise, the derived fact is in the second snapshot while the input
	// fact is from the first one.
	expectFactualSQLState(t, database.pool, "23503", `
INSERT INTO canonical_fact_inputs (
    id, organization_id, source_id, snapshot_id, fact_id, input_fact_id,
    rule_version_id, fact_kind, ordinal
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9)
`, identity.CanonicalUUID("fact-input", data.organizationExternalID, "cross-snapshot"), data.organizationID, data.sourceID, data.otherSnapshotID, data.otherDerivedFactID, data.observedFactID, data.ruleVersionID, "derived", 0)
	assertIntegrationCount(t, database.pool, "canonical_fact_inputs", 1)

	// Observed facts need the declared frontend manifest and cannot carry a
	// rule version.
	expectFactualSQLState(t, database.pool, "23514", canonicalFactInsertSQL, identity.CanonicalUUID("canonical-fact", data.organizationExternalID, "observed-without-manifest"), data.organizationID, data.sourceID, data.snapshotID, "fact:observed-without-manifest", "v1alpha1", "observed", "definition", "symbol", "symbol-without-manifest", nil, nil, nil, nil, "java", "1", "symbols", nil, time.Date(2026, 8, 20, 12, 0, 1, 0, time.UTC))
	assertIntegrationCount(t, database.pool, "canonical_facts", 3)

	// Derived facts need a rule version and must not claim an observed
	// frontend manifest.
	expectFactualSQLState(t, database.pool, "23514", canonicalFactInsertSQL, identity.CanonicalUUID("canonical-fact", data.organizationExternalID, "derived-without-rule"), data.organizationID, data.sourceID, data.snapshotID, "fact:derived-without-rule", "v1alpha1", "derived", "depends_on", "symbol", "symbol-without-rule", nil, nil, nil, nil, "rule-engine", "1", "derive", nil, time.Date(2026, 8, 20, 12, 0, 2, 0, time.UTC))
	assertIntegrationCount(t, database.pool, "canonical_facts", 3)

	// The parent and input facts are valid, but a lineage row cannot point to
	// itself.
	expectFactualSQLState(t, database.pool, "23514", `
INSERT INTO canonical_fact_inputs (
    id, organization_id, source_id, snapshot_id, fact_id, input_fact_id,
    rule_version_id, fact_kind, ordinal
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9)
`, identity.CanonicalUUID("fact-input", data.organizationExternalID, "self"), data.organizationID, data.sourceID, data.snapshotID, data.derivedFactID, data.derivedFactID, data.ruleVersionID, "derived", 0)
	assertIntegrationCount(t, database.pool, "canonical_fact_inputs", 1)

	// Ordinals are deterministic non-negative positions.
	expectFactualSQLState(t, database.pool, "23514", `
INSERT INTO canonical_fact_evidence (
    id, organization_id, source_id, snapshot_id, fact_id, evidence_unit_id, ordinal
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7)
`, identity.CanonicalUUID("fact-evidence", data.organizationExternalID, "negative-ordinal"), data.organizationID, data.sourceID, data.snapshotID, data.observedFactID, data.evidenceUnitID, -1)
	assertIntegrationCount(t, database.pool, "canonical_fact_evidence", 1)

}

func TestFactualMigrationRollbackLeavesNoTables(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	catalog, err := migrations.EmbeddedCatalog()
	if err != nil {
		t.Fatalf("load embedded migration catalog: %v", err)
	}
	items := catalog.Migrations()
	if len(items) != 5 {
		t.Fatalf("embedded migration count = %d, want 5", len(items))
	}

	conn := database.migrationConn
	var originalSearchPath string
	if err := conn.QueryRow(ctx, "SHOW search_path").Scan(&originalSearchPath); err != nil {
		t.Fatalf("read original search_path: %v", err)
	}
	secondSchema := "manu_it_rollback_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if !safeIntegrationIdentifier(secondSchema) {
		t.Fatalf("generated rollback schema is unsafe")
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoteIntegrationIdentifier(secondSchema)); err != nil {
		t.Fatalf("create rollback schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cleanupCancel()
		if _, err := conn.Exec(cleanupCtx, `SELECT set_config('search_path', $1, false)`, originalSearchPath); err != nil {
			t.Errorf("restore search_path after rollback test: %v", err)
		}
		if _, err := conn.Exec(cleanupCtx, "DROP SCHEMA "+quoteIntegrationIdentifier(secondSchema)+" CASCADE"); err != nil {
			t.Errorf("drop rollback schema: %v", err)
		}
	})

	// Keep the vector type visible to migration 0002 when the extension was
	// installed in the first isolated schema by openPostgresIntegrationDatabase.
	if _, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, secondSchema+","+database.schema+",public"); err != nil {
		t.Fatalf("set rollback migration search_path: %v", err)
	}
	for _, migration := range items[:4] {
		if err := applyFactualMigrationInTransaction(ctx, conn, migration); err != nil {
			t.Fatalf("apply migration %s in rollback schema: %v", migration.Name, err)
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin migration 0005 rollback transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, string(items[4].SQL)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("execute migration 0005 in rollback transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT 1 / 0"); err == nil {
		_ = tx.Rollback(ctx)
		t.Fatal("forced rollback error unexpectedly succeeded")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback migration 0005 transaction: %v", err)
	}

	for _, table := range factualMigrationTables {
		var exists bool
		if err := conn.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", secondSchema+"."+table).Scan(&exists); err != nil {
			t.Fatalf("inspect rolled back table %s: %v", table, err)
		}
		if exists {
			t.Fatalf("rolled-back migration left table %s", table)
		}
	}
}

const canonicalFactInsertSQL = `
INSERT INTO canonical_facts (
    id, organization_id, source_id, snapshot_id, identity_key, fact_version,
    fact_kind, predicate, subject_kind, subject_id, object_kind, object_id,
    typed_value, frontend_manifest_id, producer_id, producer_version,
    producer_method, rule_version_id, observed_at
)
VALUES (
    $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10,
    $11, $12, $13::jsonb, $14::uuid, $15, $16, $17, $18::uuid, $19
)`

type factualMigrationFixture struct {
	organizationExternalID string
	organizationID         string
	sourceID               string
	snapshotID             string
	otherSnapshotID        string
	manifestID             string
	ruleVersionID          string
	observedFactID         string
	derivedFactID          string
	otherDerivedFactID     string
	evidenceUnitID         string
}

func setupFactualMigrationFixture(t *testing.T, database *postgresIntegrationDatabase) factualMigrationFixture {
	t.Helper()
	base := integrationFixture(t, "factual", "snapshot-1", "class Factual {}")
	repository := persistence.NewRepository(database.pool)
	if _, err := repository.PersistBundle(context.Background(), base.input); err != nil {
		t.Fatalf("persist factual migration base bundle: %v", err)
	}

	data := factualMigrationFixture{
		organizationExternalID: base.organizationID,
		organizationID:         identity.CanonicalUUID("organization", base.organizationID),
		sourceID:               base.job.SourceID,
		snapshotID:             base.job.SnapshotID,
		otherSnapshotID:        identity.CanonicalUUID("snapshot", base.organizationID, base.sourceID, "snapshot-2"),
		manifestID:             identity.CanonicalUUID("frontend-manifest", base.organizationID, base.sourceID, "java", "1", "symbols"),
		ruleVersionID:          identity.CanonicalUUID("rule-version", base.organizationID, "dependency", "1"),
		observedFactID:         identity.CanonicalUUID("canonical-fact", base.organizationID, base.sourceID, "snapshot-1", "observed"),
		derivedFactID:          identity.CanonicalUUID("canonical-fact", base.organizationID, base.sourceID, "snapshot-1", "derived"),
		otherDerivedFactID:     identity.CanonicalUUID("canonical-fact", base.organizationID, base.sourceID, "snapshot-2", "derived"),
		evidenceUnitID: identity.CanonicalUUID(
			"evidence", base.organizationID, base.sourceID, "snapshot-1", base.input.Evidence[0].ID,
		),
	}

	insertFactualSnapshot(t, database.pool, data.organizationID, data.sourceID, data.otherSnapshotID, "snapshot-2")
	insertFactualManifest(t, database.pool, data.organizationID, data.sourceID, data.snapshotID, data.manifestID)
	insertFactualExtensionSchema(t, database.pool, data.organizationID, data.sourceID, data.snapshotID, data.manifestID, identity.CanonicalUUID("extension-schema", data.organizationExternalID, "valid"))
	insertFactualRuleVersion(t, database.pool, data.organizationID, data.ruleVersionID)
	insertFactualFact(t, database.pool, data.observedFactID, data.organizationID, data.sourceID, data.snapshotID, "fact:observed", "observed", data.manifestID, "", "java", "1", "symbols")
	insertFactualFact(t, database.pool, data.derivedFactID, data.organizationID, data.sourceID, data.snapshotID, "fact:derived", "derived", "", data.ruleVersionID, "rule-engine", "1", "derive")
	insertFactualFact(t, database.pool, data.otherDerivedFactID, data.organizationID, data.sourceID, data.otherSnapshotID, "fact:derived-other", "derived", "", data.ruleVersionID, "rule-engine", "1", "derive")
	insertFactualQualifier(t, database.pool, data.organizationID, data.sourceID, data.snapshotID, data.observedFactID, identity.CanonicalUUID("fact-qualifier", data.organizationExternalID, "origin"))
	insertFactualEvidenceLink(t, database.pool, data.organizationID, data.sourceID, data.snapshotID, data.observedFactID, data.evidenceUnitID, identity.CanonicalUUID("fact-evidence", data.organizationExternalID, "valid"))
	insertFactualInput(t, database.pool, data.organizationID, data.sourceID, data.snapshotID, data.derivedFactID, data.observedFactID, data.ruleVersionID, identity.CanonicalUUID("fact-input", data.organizationExternalID, "valid"))
	return data
}

func insertFactualSnapshot(t *testing.T, pool *pgxpool.Pool, organizationID, sourceID, snapshotID, externalID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO analysis_snapshots (
    id, organization_id, source_id, external_id, source_revision, source_hash,
    analysis_configuration_id, factual_digest, captured_at
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9)
`, snapshotID, organizationID, sourceID, externalID, "revision-"+externalID, integrationDigest(externalID+":source"), "integration-config", integrationDigest(externalID+":facts"), time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("insert factual migration snapshot: %v", err)
	}
}

func insertFactualManifest(t *testing.T, pool *pgxpool.Pool, organizationID, sourceID, snapshotID, manifestID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO frontend_manifests (
    id, organization_id, source_id, snapshot_id, external_id, manifest_version,
    frontend_id, version, method, execution_profile, manifest, manifest_digest
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11::jsonb, $12)
`, manifestID, organizationID, sourceID, snapshotID, "frontend-java", "v1alpha1", "java", "1", "symbols", "safe-static", `{}`, integrationDigest("frontend-manifest"))
	if err != nil {
		t.Fatalf("insert factual migration frontend manifest: %v", err)
	}
}

func insertFactualExtensionSchema(t *testing.T, pool *pgxpool.Pool, organizationID, sourceID, snapshotID, manifestID, schemaID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO frontend_extension_schemas (
    id, organization_id, source_id, snapshot_id, frontend_manifest_id,
    schema_id, version, digest
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8)
`, schemaID, organizationID, sourceID, snapshotID, manifestID, "java.core", "1", integrationDigest("extension-schema"))
	if err != nil {
		t.Fatalf("insert factual migration extension schema: %v", err)
	}
}

func insertFactualRuleVersion(t *testing.T, pool *pgxpool.Pool, organizationID, ruleVersionID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO rule_versions (
    id, organization_id, rule_id, version, implementation_digest, configuration
)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb)
`, ruleVersionID, organizationID, "dependency", "1", integrationDigest("rule-dependency-v1"), `{}`)
	if err != nil {
		t.Fatalf("insert factual migration rule version: %v", err)
	}
}

func insertFactualFact(t *testing.T, pool *pgxpool.Pool, factID, organizationID, sourceID, snapshotID, identityKey, kind, manifestID, ruleVersionID, producerID, producerVersion, producerMethod string) {
	t.Helper()
	var frontendValue any
	if manifestID != "" {
		frontendValue = manifestID
	}
	var ruleValue any
	if ruleVersionID != "" {
		ruleValue = ruleVersionID
	}
	_, err := pool.Exec(context.Background(), canonicalFactInsertSQL,
		factID, organizationID, sourceID, snapshotID, identityKey, "v1alpha1", kind,
		map[string]string{"observed": "definition", "derived": "depends_on"}[kind], "symbol", identityKey,
		nil, nil, nil, frontendValue, producerID, producerVersion, producerMethod, ruleValue,
		time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("insert factual migration %s fact: %v", kind, err)
	}
}

func insertFactualQualifier(t *testing.T, pool *pgxpool.Pool, organizationID, sourceID, snapshotID, factID, qualifierID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO canonical_fact_qualifiers (
    id, organization_id, source_id, snapshot_id, fact_id, ordinal, name, typed_value
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, $7, $8::jsonb)
`, qualifierID, organizationID, sourceID, snapshotID, factID, 0, "origin", `{"kind":"string","string":"observed"}`)
	if err != nil {
		t.Fatalf("insert factual migration qualifier: %v", err)
	}
}

func insertFactualEvidenceLink(t *testing.T, pool *pgxpool.Pool, organizationID, sourceID, snapshotID, factID, evidenceUnitID, linkID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO canonical_fact_evidence (
    id, organization_id, source_id, snapshot_id, fact_id, evidence_unit_id, ordinal
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7)
`, linkID, organizationID, sourceID, snapshotID, factID, evidenceUnitID, 0)
	if err != nil {
		t.Fatalf("insert factual migration evidence link: %v", err)
	}
}

func insertFactualInput(t *testing.T, pool *pgxpool.Pool, organizationID, sourceID, snapshotID, factID, inputFactID, ruleVersionID, inputID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
INSERT INTO canonical_fact_inputs (
    id, organization_id, source_id, snapshot_id, fact_id, input_fact_id,
    rule_version_id, fact_kind, ordinal
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid, $8, $9)
`, inputID, organizationID, sourceID, snapshotID, factID, inputFactID, ruleVersionID, "derived", 0)
	if err != nil {
		t.Fatalf("insert factual migration input: %v", err)
	}
}

func assertFactualMigrationTable(t *testing.T, pool *pgxpool.Pool, schema, table string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `SELECT to_regclass($1) IS NOT NULL`, schema+"."+table).Scan(&exists); err != nil {
		t.Fatalf("inspect factual migration table %s: %v", table, err)
	}
	if !exists {
		t.Fatalf("factual migration table %s is missing", table)
	}
}

func assertFactualMigrationConstraint(t *testing.T, pool *pgxpool.Pool, schema, table, constraint string) {
	t.Helper()
	var exists bool
	if err := pool.QueryRow(context.Background(), `
SELECT EXISTS (
    SELECT 1
    FROM pg_constraint c
    JOIN pg_class r ON r.oid = c.conrelid
    JOIN pg_namespace n ON n.oid = r.relnamespace
    WHERE n.nspname = $1 AND r.relname = $2 AND c.conname = $3
)
`, schema, table, constraint).Scan(&exists); err != nil {
		t.Fatalf("inspect factual migration constraint %s.%s: %v", table, constraint, err)
	}
	if !exists {
		t.Fatalf("factual migration constraint %s.%s is missing", table, constraint)
	}
}

func expectFactualSQLState(t *testing.T, pool *pgxpool.Pool, wantCode, query string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), query, args...)
	if err == nil {
		t.Fatalf("statement unexpectedly succeeded; want SQLSTATE %s", wantCode)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("statement error type = %T, want PostgreSQL error SQLSTATE %s", err, wantCode)
	}
	if pgErr.Code != wantCode {
		t.Fatalf("statement SQLSTATE = %s, want %s", pgErr.Code, wantCode)
	}
}

func applyFactualMigrationInTransaction(ctx context.Context, conn *pgx.Conn, migration migrations.Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, string(migration.SQL)); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return nil
}
