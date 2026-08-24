package migrations

import (
	"embed"
	"strings"
	"testing"
)

// The derivation reverse indexes are inspected structurally here. PostgreSQL
// execution belongs to the persistence integration tests; these checks keep
// index direction, scope, and forward-only migration policy explicit.
//
//go:embed 0006_derivation_reverse_index.up.sql
var derivationReverseIndexMigration embed.FS

func TestDerivationReverseIndexMigrationUsesDeterministicScopedIndexes(t *testing.T) {
	sqlBytes, err := derivationReverseIndexMigration.ReadFile("0006_derivation_reverse_index.up.sql")
	if err != nil {
		t.Fatalf("read derivation reverse index migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	compactSQL := strings.Join(strings.Fields(sql), " ")
	compactSQL = strings.ReplaceAll(compactSQL, "( ", "(")
	compactSQL = strings.ReplaceAll(compactSQL, " )", ")")

	for _, index := range []struct {
		name       string
		table      string
		definition string
	}{
		{
			name:  "canonical_fact_inputs_input_fact_reverse_lookup",
			table: "canonical_fact_inputs",
			definition: "create index canonical_fact_inputs_input_fact_reverse_lookup on canonical_fact_inputs " +
				"(organization_id, source_id, snapshot_id, input_fact_id, fact_id, ordinal, rule_version_id)",
		},
		{
			name:  "canonical_facts_derived_rule_snapshot_lookup",
			table: "canonical_facts",
			definition: "create index canonical_facts_derived_rule_snapshot_lookup on canonical_facts " +
				"(organization_id, source_id, snapshot_id, rule_version_id, id) where fact_kind = 'derived'",
		},
	} {
		t.Run(index.name, func(t *testing.T) {
			if !strings.Contains(compactSQL, index.definition) {
				t.Fatalf("migration missing deterministic definition for %s", index.name)
			}
			if !strings.Contains(compactSQL, "comment on index "+index.name+" is") {
				t.Fatalf("migration does not document %s", index.name)
			}
			if !strings.Contains(compactSQL, "on "+index.table+" ") {
				t.Fatalf("%s does not target %s", index.name, index.table)
			}
		})
	}

	if strings.Contains(compactSQL, "create unique index") {
		t.Fatal("derivation lookup indexes must not duplicate uniqueness constraints")
	}
	if !strings.Contains(compactSQL, "input_fact_id, fact_id") {
		t.Fatal("reverse derivation index must lead with input_fact_id before fact_id")
	}
	if !strings.Contains(compactSQL, "where fact_kind = 'derived'") {
		t.Fatal("rule-version lookup must be restricted to derived facts")
	}
}

func TestDerivationReverseIndexMigrationIsForwardOnlyAndScoped(t *testing.T) {
	sqlBytes, err := derivationReverseIndexMigration.ReadFile("0006_derivation_reverse_index.up.sql")
	if err != nil {
		t.Fatalf("read derivation reverse index migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	compactSQL := strings.Join(strings.Fields(sql), " ")

	for _, forbidden := range []string{
		"begin;",
		"commit;",
		"rollback",
		"savepoint",
		"release savepoint",
		"drop ",
		"truncate ",
		"delete ",
		"create trigger",
		"create function",
		" on delete ",
		" on update ",
	} {
		if strings.Contains(compactSQL, forbidden) {
			t.Errorf("migration contains forbidden fragment %q", forbidden)
		}
	}
	if !strings.HasPrefix(strings.TrimSpace(sql), "--") {
		t.Fatal("migration should begin with its explanatory header")
	}
	if strings.Count(compactSQL, "create index ") != 2 {
		t.Fatalf("migration creates %d indexes, want 2", strings.Count(compactSQL, "create index "))
	}
	for _, scope := range []string{"organization_id", "source_id", "snapshot_id"} {
		if !strings.Contains(compactSQL, scope) {
			t.Fatalf("migration does not retain %s scope", scope)
		}
	}
}
