package migrations

import (
	"embed"
	"strings"
	"testing"
)

// Keep the migration under test as a repository artifact. These tests are
// deliberately textual: PostgreSQL integration and execution belong to the
// later persistence/integration tasks. The migration runner owns the
// transaction boundary and version/checksum record, so migration SQL files
// must not open or commit transactions themselves.
//
//go:embed 0001_canonical_knowledge.up.sql
var canonicalMigration embed.FS

func TestCanonicalMigrationContainsRequiredTablesAndScopeGuards(t *testing.T) {
	sqlBytes, err := canonicalMigration.ReadFile("0001_canonical_knowledge.up.sql")
	if err != nil {
		t.Fatalf("read canonical migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	tables := []string{
		"organizations",
		"sources",
		"analysis_snapshots",
		"artifacts",
		"observations",
		"entities",
		"relationships",
		"evidence_units",
		"analysis_coverage",
		"explicit_gaps",
		"analysis_failures",
		"factual_identities",
	}
	for _, table := range tables {
		t.Run("table/"+table, func(t *testing.T) {
			needle := "create table " + table + " ("
			if !strings.Contains(sql, needle) {
				t.Fatalf("migration does not create %s", table)
			}
		})
	}

	for _, needle := range []string{
		"organization_id uuid not null",
		"timestamptz not null",
		"foreign key (organization_id, source_id)",
		"foreign key (organization_id, snapshot_id, source_id)",
		"foreign key (organization_id, snapshot_id, source_id, artifact_id)",
		"foreign key (organization_id, snapshot_id, source_id, from_entity_id)",
		"foreign key (organization_id, snapshot_id, source_id, to_entity_id)",
		"constraint sources_active_snapshot_fk",
		"references analysis_snapshots (organization_id, source_id, id)",
		"where state = 'active'",
		"comment on table observations",
		"content_state text not null",
		"content_hash char(64) not null",
		"classification text not null",
		"findings jsonb not null",
		"truncated boolean not null default false",
		"persist_decision text not null",
		"external_transfer_decision text not null",
		"provenance jsonb not null",
		"evidence_units_content_state_consistent",
		"evidence_units_present_reason_check",
		"evidence_units_restricted_classification_check",
		"evidence_units_persist_consistent",
		"evidence_units_redaction_consistent",
		"evidence_units_classification_consistent",
		"evidence_units_transfer_consistent",
	} {
		if !strings.Contains(sql, needle) {
			t.Errorf("migration missing invariant fragment %q", needle)
		}
	}
}

func TestCanonicalMigrationIsForwardOnlyAndDoesNotRequireExtensions(t *testing.T) {
	sqlBytes, err := canonicalMigration.ReadFile("0001_canonical_knowledge.up.sql")
	if err != nil {
		t.Fatalf("read canonical migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	for _, forbidden := range []string{
		"create extension",
		"uuid_generate_v4",
		"gen_random_uuid",
		"begin;",
		"commit;",
		" on delete cascade",
		"create trigger",
		"create function",
		"drop table",
		"rollback",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration contains forbidden fragment %q", forbidden)
		}
	}
	if strings.Contains(sql, "down.sql") {
		t.Fatal("migration must not advertise a destructive down migration")
	}
	if !strings.HasPrefix(strings.TrimSpace(sql), "--") {
		t.Fatal("migration should begin with its explanatory header")
	}
}

func TestCanonicalMigrationLeavesTransactionBoundaryToRunner(t *testing.T) {
	sqlBytes, err := canonicalMigration.ReadFile("0001_canonical_knowledge.up.sql")
	if err != nil {
		t.Fatalf("read canonical migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	for _, statement := range []string{"begin;", "commit;"} {
		if strings.Contains(sql, statement) {
			t.Fatalf("canonical migration must leave %s to the migration runner", statement)
		}
	}
}

func TestCanonicalMigrationKeepsSnapshotsHistorical(t *testing.T) {
	sqlBytes, err := canonicalMigration.ReadFile("0001_canonical_knowledge.up.sql")
	if err != nil {
		t.Fatalf("read canonical migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	compactSQL := strings.Join(strings.Fields(sql), " ")

	snapshotStart := strings.Index(sql, "create table analysis_snapshots")
	if snapshotStart < 0 {
		t.Fatal("analysis_snapshots table not found")
	}
	snapshotEnd := strings.Index(sql[snapshotStart:], "create table artifacts")
	if snapshotEnd < 0 {
		t.Fatal("analysis_snapshots section boundary not found")
	}
	snapshotSQL := sql[snapshotStart : snapshotStart+snapshotEnd]
	if !strings.Contains(snapshotSQL, "captured_at timestamptz not null") {
		t.Fatal("snapshots must carry a non-null capture timestamp")
	}
	if strings.Contains(snapshotSQL, "on delete") || strings.Contains(snapshotSQL, "on update") {
		t.Fatal("snapshot references must not cascade or mutate historical rows")
	}
	if !strings.Contains(sql, "comment on table analysis_snapshots") {
		t.Fatal("snapshot immutability intent must be documented in migration")
	}
	if !strings.Contains(sql, "state in ('active', 'historical')") {
		t.Fatal("factual identities must distinguish active and historical rows")
	}
	if strings.Contains(sql, "relationships_not_self_referential") {
		t.Fatal("self-referential relationships must remain representable")
	}
	if strings.Contains(compactSQL, "from_entity_id <> to_entity_id") {
		t.Fatal("self-referential relationships must not be rejected by a check")
	}
	if strings.Contains(sql, "factual_identities_digest_unique") {
		t.Fatal("the same factual digest must be reusable in later historical snapshots")
	}
	if strings.Contains(compactSQL, "unique (organization_id, source_id, identity_key, factual_digest)") {
		t.Fatal("factual digest uniqueness must remain scoped to each snapshot identity")
	}
	if !strings.Contains(sql, "factual_identities_digest_lookup") {
		t.Fatal("factual digest lookup index is missing")
	}
	if !strings.Contains(sql, "on factual_identities (organization_id, source_id, factual_digest, identity_key)") {
		t.Fatal("factual digest lookup must be keyed by digest before identity details")
	}
	if strings.Contains(sql, "revoke update, delete on analysis_snapshots") {
		t.Fatal("migration must not claim owner-independent immutability via REVOKE")
	}
}

func TestCanonicalMigrationEvidenceRepresentationInvariants(t *testing.T) {
	sqlBytes, err := canonicalMigration.ReadFile("0001_canonical_knowledge.up.sql")
	if err != nil {
		t.Fatalf("read canonical migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	start := strings.Index(sql, "create table evidence_units")
	if start < 0 {
		t.Fatal("evidence_units table not found")
	}
	end := strings.Index(sql[start:], "comment on table evidence_units")
	if end < 0 {
		t.Fatal("evidence_units section boundary not found")
	}
	evidenceSQL := sql[start : start+end]

	tests := []struct {
		name string
		want []string
	}{
		{
			name: "redacted fixed representation and reason",
			want: []string{
				"content_state = 'redacted'",
				"content = '[redacted]'",
				"redaction_reason is not null",
				"btrim(redaction_reason) <> ''",
			},
		},
		{
			name: "omitted has no content or counts",
			want: []string{
				"content_state = 'omitted'",
				"content is null",
				"content_bytes = 0",
				"content_characters = 0",
			},
		},
		{
			name: "present has no redaction reason",
			want: []string{
				"content_state <> 'present'",
				"redaction_reason is null",
			},
		},
		{
			name: "restricted classifications are omitted",
			want: []string{
				"classification not in ('binary', 'invalid', 'prohibited')",
				"or content_state = 'omitted'",
			},
		},
		{
			name: "external allow requires safe text",
			want: []string{
				"external_transfer_decision <> 'allow'",
				"or classification = 'safe_text'",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, fragment := range test.want {
				if !strings.Contains(evidenceSQL, fragment) {
					t.Errorf("evidence constraints missing %q", fragment)
				}
			}
		})
	}
	if !strings.Contains(evidenceSQL, "empty classification is retained only for explicitly authorized") {
		t.Fatal("unknown classification compatibility decision is not documented")
	}
}
