package migrations

import (
	"embed"
	"strings"
	"testing"
)

// The factual substrate migration is inspected structurally here. PostgreSQL
// execution belongs to the later integration task; these checks keep the
// forward-only SQL contract visible without requiring a database.
//
//go:embed 0005_factual_substrate.up.sql
var factualSubstrateMigration embed.FS

func TestFactualSubstrateMigrationDefinesRequiredTablesAndScope(t *testing.T) {
	sqlBytes, err := factualSubstrateMigration.ReadFile("0005_factual_substrate.up.sql")
	if err != nil {
		t.Fatalf("read factual substrate migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	tables := []string{
		"frontend_manifests",
		"frontend_extension_schemas",
		"rule_versions",
		"canonical_facts",
		"canonical_fact_qualifiers",
		"canonical_fact_evidence",
		"canonical_fact_inputs",
	}
	for _, table := range tables {
		t.Run("table/"+table, func(t *testing.T) {
			section, ok := migrationTableSection(sql, table)
			if !ok {
				t.Fatalf("migration does not create %s", table)
			}
			for _, fragment := range []string{
				"id uuid primary key",
				"organization_id uuid not null",
				"source_id uuid not null",
				"snapshot_id uuid not null",
				"created_at timestamptz not null",
			} {
				if table == "rule_versions" && (fragment == "source_id uuid not null" || fragment == "snapshot_id uuid not null") {
					continue
				}
				if !strings.Contains(section, fragment) {
					t.Errorf("%s is missing %q", table, fragment)
				}
			}
		})
	}

	compactSQL := strings.Join(strings.Fields(sql), " ")
	for _, fragment := range []string{
		"foreign key (organization_id, snapshot_id, source_id) references analysis_snapshots (organization_id, id, source_id)",
		"foreign key (organization_id, snapshot_id, source_id, frontend_manifest_id) references frontend_manifests (organization_id, snapshot_id, source_id, id)",
		"foreign key (organization_id, snapshot_id, source_id, fact_id) references canonical_facts (organization_id, snapshot_id, source_id, id)",
		"foreign key (organization_id, snapshot_id, source_id, evidence_unit_id) references evidence_units (organization_id, snapshot_id, source_id, id)",
		"foreign key (organization_id, snapshot_id, source_id, input_fact_id) references canonical_facts (organization_id, snapshot_id, source_id, id)",
	} {
		if !strings.Contains(compactSQL, fragment) {
			t.Errorf("migration missing scoped foreign key %q", fragment)
		}
	}
	if !strings.Contains(compactSQL, "foreign key (organization_id, rule_version_id) references rule_versions (organization_id, id)") {
		t.Fatal("migration missing organization-scoped rule foreign key")
	}

	for offset := 0; ; {
		relative := strings.Index(compactSQL[offset:], "foreign key (")
		if relative < 0 {
			break
		}
		start := offset + relative + len("foreign key (")
		foreignKeyColumns := strings.TrimLeft(compactSQL[start:], " ")
		if !strings.HasPrefix(foreignKeyColumns, "organization_id,") &&
			!strings.HasPrefix(foreignKeyColumns, "organization_id)") {
			t.Errorf("foreign key is not organization-scoped near %q", foreignKeyColumns)
		}
		offset = start
	}
}

func TestFactualSubstrateMigrationContainsFactAndManifestChecks(t *testing.T) {
	sqlBytes, err := factualSubstrateMigration.ReadFile("0005_factual_substrate.up.sql")
	if err != nil {
		t.Fatalf("read factual substrate migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	for _, fragment := range []string{
		"external_id text not null",
		"manifest_version text not null",
		"frontend_id text not null",
		"version text not null",
		"method text not null",
		"execution_profile text not null",
		"manifest jsonb not null",
		"frontend_manifests_manifest_object",
		"frontend_manifests_manifest_digest_sha256",
		"frontend_manifests_frontend_identity_unique unique",
		"frontend_manifests_scoped_id_frontend_unique unique",
		"schema_id text not null",
		"frontend_extension_schemas_manifest_fk",
		"frontend_extension_schemas_digest_sha256",
		"rule_id text not null",
		"implementation_digest char(64) not null",
		"configuration jsonb not null",
		"rule_versions_configuration_object",
		"identity_key text not null",
		"fact_kind text not null",
		"subject_kind text not null",
		"subject_id text not null",
		"object_kind text",
		"object_id text",
		"typed_value jsonb",
		"frontend_manifest_id uuid",
		"producer_id text not null",
		"producer_version text not null",
		"producer_method text not null",
		"canonical_facts_object_pair",
		"canonical_facts_object_value_exclusive",
		"canonical_facts_observed_frontend_without_rule",
		"canonical_facts_derived_rule_without_frontend",
		"canonical_facts_frontend_fk",
		"canonical_facts_rule_version_fk",
		"canonical_fact_qualifiers_ordinal_nonnegative",
		"canonical_fact_qualifiers_typed_value_object",
		"canonical_fact_qualifiers_fact_ordinal_unique unique",
		"canonical_fact_qualifiers_fact_name_unique unique",
		"canonical_fact_evidence_ordinal_nonnegative",
		"canonical_fact_evidence_fact_evidence_unique unique",
		"canonical_fact_evidence_fact_evidence_ordinal_unique unique",
		"canonical_fact_inputs_ordinal_nonnegative",
		"canonical_fact_inputs_fact_kind",
		"canonical_fact_inputs_not_self_link",
		"canonical_fact_inputs_fact_ordinal_unique unique",
		"canonical_fact_inputs_fact_input_unique unique",
		"canonical_fact_inputs_fact_input_ordinal_unique unique",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration missing contract fragment %q", fragment)
		}
	}
}

func TestCanonicalFactsUseManifestIdentityForObservedFrontend(t *testing.T) {
	sqlBytes, err := factualSubstrateMigration.ReadFile("0005_factual_substrate.up.sql")
	if err != nil {
		t.Fatalf("read factual substrate migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	compactSQL := strings.Join(strings.Fields(sql), " ")
	compactSQL = strings.ReplaceAll(compactSQL, "( ", "(")
	compactSQL = strings.ReplaceAll(compactSQL, " )", ")")

	manifestSection, ok := migrationTableSection(sql, "frontend_manifests")
	if !ok {
		t.Fatal("frontend_manifests table not found")
	}
	if !strings.Contains(
		strings.ReplaceAll(strings.ReplaceAll(strings.Join(strings.Fields(manifestSection), " "), "( ", "("), " )", ")"),
		"constraint frontend_manifests_scoped_id_frontend_unique unique (organization_id, snapshot_id, source_id, id, frontend_id, version, method)",
	) {
		t.Fatal("frontend manifest is missing the scoped id/frontend triplet unique target")
	}

	factSection, ok := migrationTableSection(sql, "canonical_facts")
	if !ok {
		t.Fatal("canonical_facts table not found")
	}
	if strings.Contains(factSection, "frontend_manifest_id uuid not null") {
		t.Fatal("canonical_facts frontend_manifest_id must remain nullable for derived facts")
	}
	for _, forbidden := range []string{
		"frontend_id text",
		"frontend_version text",
		"frontend_method text",
	} {
		if strings.Contains(factSection, forbidden) {
			t.Errorf("canonical_facts must not duplicate frontend field %q", forbidden)
		}
	}

	for _, fragment := range []string{
		"foreign key (organization_id, snapshot_id, source_id, frontend_manifest_id, producer_id, producer_version, producer_method) references frontend_manifests (organization_id, snapshot_id, source_id, id, frontend_id, version, method) match simple",
		"constraint canonical_facts_observed_frontend_without_rule check (fact_kind <> 'observed' or (frontend_manifest_id is not null and rule_version_id is null))",
		"constraint canonical_facts_derived_rule_without_frontend check (fact_kind <> 'derived' or (frontend_manifest_id is null and rule_version_id is not null))",
	} {
		if !strings.Contains(compactSQL, fragment) {
			t.Errorf("migration missing frontend provenance invariant %q", fragment)
		}
	}
}

func TestFactualSubstrateMigrationIsForwardOnlyAndDocumentsCrossRowRule(t *testing.T) {
	sqlBytes, err := factualSubstrateMigration.ReadFile("0005_factual_substrate.up.sql")
	if err != nil {
		t.Fatalf("read factual substrate migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	for _, forbidden := range []string{
		"create extension",
		"uuid_generate_v4",
		"gen_random_uuid",
		"begin;",
		"commit;",
		"rollback",
		"create trigger",
		"create function",
		"drop table",
		"truncate",
		"delete from",
		" on delete cascade",
		" on update cascade",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration contains forbidden fragment %q", forbidden)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(sql), "--") {
		t.Fatal("migration should begin with its explanatory header")
	}
	for _, fragment := range []string{
		"minimum cardinality",
		"task 3.2",
		"at least one input",
		"row-local check",
	} {
		if !strings.Contains(sql, fragment) {
			t.Errorf("migration does not document cross-row invariant %q", fragment)
		}
	}
}
