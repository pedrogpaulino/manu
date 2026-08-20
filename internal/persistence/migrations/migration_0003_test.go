package migrations

import (
	"embed"
	"strings"
	"testing"
)

//go:embed 0003_textual_projection.up.sql
var textualProjectionMigrationFS embed.FS

func TestTextualProjectionMigrationDefinesBoundedSearchSurface(t *testing.T) {
	sqlBytes, err := textualProjectionMigrationFS.ReadFile("0003_textual_projection.up.sql")
	if err != nil {
		t.Fatalf("read textual projection migration: %v", err)
	}
	sql := string(sqlBytes)
	lower := strings.ToLower(sql)
	for _, forbidden := range []string{"begin;", "commit;", "rollback", "drop ", "truncate ", "delete ", "pgvector", "embedding", "hnsw", "ivfflat"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("textual projection migration contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{
		"create table textual_evidence_projection",
		"organization_id uuid not null",
		"content_state text not null",
		"content_hash char(64) not null",
		"content_characters bigint not null",
		"search_vector tsvector generated always as",
		"to_tsvector('simple'",
		"using gin (search_vector)",
		"using gin (exact_terms)",
		"foreign key (organization_id, snapshot_id, source_id, evidence_id)",
		"content_state in ('present', 'redacted')",
		"persist_decision in ('allow', 'redact')",
		"projection_kind in ('generic', 'symbol', 'configuration', 'exception')",
	} {
		if !strings.Contains(lower, required) {
			t.Errorf("textual projection migration missing %q", required)
		}
	}
}

func TestTextualProjectionMigrationUsesExplicitStableConfiguration(t *testing.T) {
	sqlBytes, err := textualProjectionMigrationFS.ReadFile("0003_textual_projection.up.sql")
	if err != nil {
		t.Fatalf("read textual projection migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	if !strings.Contains(sql, "search_configuration text not null default 'simple'") {
		t.Fatal("search configuration is not explicit and stable")
	}
	if !strings.Contains(sql, "constraint textual_evidence_projection_search_configuration check") {
		t.Fatal("search configuration has no stable-value check")
	}
	if !strings.Contains(sql, "content_length_bounded check") || !strings.Contains(sql, "char_length(content) <= 16384") || !strings.Contains(sql, "content_characters >= 0") {
		t.Fatal("bounded content metadata checks are missing")
	}
}
