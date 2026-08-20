package migrations

import (
	"embed"
	"strings"
	"testing"
)

// The migration tests intentionally inspect SQL text instead of connecting to
// PostgreSQL. Execution against PostgreSQL/pgvector belongs to the later
// persistence integration task. The migration runner owns the transaction
// boundary and version/checksum record; these files must not commit early.
//
//go:embed 0001_canonical_knowledge.up.sql 0002_query_projection.up.sql
var queryProjectionMigrations embed.FS

func TestQueryProjectionMigrationFollowsCanonicalMigration(t *testing.T) {
	entries, err := queryProjectionMigrations.ReadDir(".")
	if err != nil {
		t.Fatalf("read embedded migration directory: %v", err)
	}
	if len(entries) != 2 || entries[0].Name() != "0001_canonical_knowledge.up.sql" || entries[1].Name() != "0002_query_projection.up.sql" {
		t.Fatalf("migration order = %v, want 0001 before 0002", entries)
	}

	canonical, err := queryProjectionMigrations.ReadFile("0001_canonical_knowledge.up.sql")
	if err != nil {
		t.Fatalf("read canonical migration: %v", err)
	}
	extended, err := queryProjectionMigrations.ReadFile("0002_query_projection.up.sql")
	if err != nil {
		t.Fatalf("read query projection migration: %v", err)
	}
	canonicalSQL := strings.ToLower(string(canonical))
	extendedSQL := strings.ToLower(string(extended))

	if strings.Index(canonicalSQL, "create table organizations") < 0 {
		t.Fatal("canonical migration does not contain the 0001 schema")
	}
	if !strings.HasPrefix(strings.TrimSpace(extendedSQL), "--") {
		t.Fatal("query projection migration must begin with its explanatory header")
	}
	for _, statement := range []string{"begin;", "commit;"} {
		if strings.Contains(extendedSQL, statement) {
			t.Fatalf("query projection migration must leave %s to the migration runner", statement)
		}
	}
	if !strings.Contains(extendedSQL, "create extension if not exists vector;") {
		t.Fatal("query projection migration must enable the approved vector extension")
	}
	if strings.Contains(extendedSQL, "create extension if not exists vector") &&
		strings.Contains(canonicalSQL, "create extension if not exists vector") {
		t.Fatal("the vector extension must be introduced only by the 0002 projection migration")
	}

	for _, forbidden := range []string{
		"create trigger",
		"create function",
		"drop table",
		"rollback",
		" on delete cascade",
		" on update cascade",
		"hnsw",
		"ivfflat",
		"api_key",
		"authorization",
		"credential",
		"request_body",
		"response_body",
		"prompt",
	} {
		if strings.Contains(extendedSQL, forbidden) {
			t.Errorf("query projection migration contains forbidden fragment %q", forbidden)
		}
	}
}

func TestQueryProjectionMigrationContainsScopedTables(t *testing.T) {
	sqlBytes, err := queryProjectionMigrations.ReadFile("0002_query_projection.up.sql")
	if err != nil {
		t.Fatalf("read query projection migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	tables := []string{
		"ingestion_jobs",
		"ingestion_job_stages",
		"embedding_profiles",
		"embedding_items",
		"queries",
		"query_candidates",
		"evidence_packages",
		"evidence_package_items",
		"generated_claims",
		"citations",
		"provider_calls",
		"provider_call_attempts",
	}
	for _, table := range tables {
		t.Run("table/"+table, func(t *testing.T) {
			section, ok := migrationTableSection(sql, table)
			if !ok {
				t.Fatalf("migration does not create %s", table)
			}
			if !strings.Contains(section, "organization_id uuid not null") {
				t.Fatalf("%s is missing its organization scope", table)
			}
			if !strings.Contains(section, "constraint "+table+"_organization_id_unique unique (organization_id, id)") {
				t.Fatalf("%s is missing an organization-scoped identifier key", table)
			}
		})
	}

	compactSQL := strings.Join(strings.Fields(sql), " ")
	for _, needle := range []string{
		"foreign key (organization_id, source_id) references sources (organization_id, id)",
		"foreign key (organization_id, source_id, snapshot_id) references analysis_snapshots (organization_id, source_id, id)",
		"foreign key (organization_id, snapshot_id, source_id, evidence_id) references evidence_units (organization_id, snapshot_id, source_id, id)",
		"foreign key (organization_id, profile_id, profile_dimension) references embedding_profiles (organization_id, id, dimension)",
		"foreign key (organization_id, package_id, query_id) references evidence_packages (organization_id, id, query_id)",
		"foreign key (organization_id, query_id, candidate_id) references query_candidates (organization_id, query_id, id)",
		"foreign key (organization_id, provider_call_id) references provider_calls (organization_id, id)",
	} {
		if !strings.Contains(compactSQL, needle) {
			t.Errorf("migration missing scoped foreign key %q", needle)
		}
	}

	for offset := 0; ; {
		relative := strings.Index(compactSQL[offset:], "foreign key (")
		if relative < 0 {
			break
		}
		start := offset + relative + len("foreign key (")
		foreignKeyColumns := strings.TrimSpace(compactSQL[start:])
		if !strings.HasPrefix(foreignKeyColumns, "organization_id,") &&
			!strings.HasPrefix(foreignKeyColumns, "organization_id)") {
			t.Errorf("foreign key is not organization-scoped near %q", compactSQL[offset:])
		}
		offset = start
	}
}

func TestQueryProjectionMigrationDefinesJobsProfilesAndVectorProjection(t *testing.T) {
	sqlBytes, err := queryProjectionMigrations.ReadFile("0002_query_projection.up.sql")
	if err != nil {
		t.Fatalf("read query projection migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	cases := []struct {
		name string
		want []string
	}{
		{
			name: "job lifecycle and idempotency",
			want: []string{
				"state in ('pending', 'running', 'completed', 'partial', 'failed')",
				"stage in (",
				"lease_owner",
				"lease_expires_at",
				"cancel_requested boolean not null default false",
				"diagnostic_code",
				"ingestion_jobs_factual_identity_unique unique",
			},
		},
		{
			name: "immutable profile metadata",
			want: []string{
				"provider text not null",
				"model text not null",
				"dimension integer not null",
				"normalization text not null",
				"configuration_version text not null",
				"configuration_digest char(64) not null",
				"configuration jsonb not null default '{}'::jsonb",
				"embedding_profiles_configuration_object",
				"embedding_profiles_identity_unique unique",
			},
		},
		{
			name: "dimension-checked rebuildable vectors",
			want: []string{
				"embedding vector not null",
				"vector_dims(embedding) = profile_dimension",
				"embedding_items_profile_evidence_unique unique",
				"embedding_items_profile_hash_lookup",
				"embedding_items_dimension_matches_profile",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, fragment := range test.want {
				if !strings.Contains(sql, fragment) {
					t.Errorf("migration missing %q", fragment)
				}
			}
		})
	}
	if strings.Contains(sql, "embedding_items") && strings.Contains(sql, "updated_at") {
		t.Fatal("embedding profile/projection migration must not imply mutable profile rows with updated_at")
	}
}

func TestQueryProjectionMigrationAllowsPreCanonicalJobsAndSignedVectorScores(t *testing.T) {
	sqlBytes, err := queryProjectionMigrations.ReadFile("0002_query_projection.up.sql")
	if err != nil {
		t.Fatalf("read query projection migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	compactSQL := strings.Join(strings.Fields(sql), " ")

	jobSection, ok := migrationTableSection(sql, "ingestion_jobs")
	if !ok {
		t.Fatal("ingestion_jobs table not found")
	}
	for _, fragment := range []string{
		"source_id uuid,",
		"snapshot_id uuid,",
		"source_external_id text not null",
		"snapshot_external_id text not null",
		"ingestion_jobs_snapshot_scope",
		"ingestion_jobs_source_external_id_not_blank",
		"ingestion_jobs_snapshot_external_id_not_blank",
	} {
		if !strings.Contains(jobSection, fragment) {
			t.Errorf("pre-canonical job contract missing %q", fragment)
		}
	}
	if strings.Contains(jobSection, "source_id uuid not null") || strings.Contains(jobSection, "snapshot_id uuid not null") {
		t.Fatal("ingestion jobs must not require canonical source/snapshot UUIDs before validation")
	}
	if !strings.Contains(compactSQL, "unique ( organization_id, source_external_id, snapshot_external_id, factual_digest )") {
		t.Fatal("job idempotency must use external source/snapshot identities and factual digest")
	}

	candidateSection, ok := migrationTableSection(sql, "query_candidates")
	if !ok {
		t.Fatal("query_candidates table not found")
	}
	if strings.Contains(candidateSection, "vector_score is null or vector_score >= 0") {
		t.Fatal("vector_score must remain signed because cosine similarity can be negative")
	}
	if !strings.Contains(candidateSection, "query_candidates_known_scores_nonnegative") {
		t.Fatal("non-vector score checks must remain explicit")
	}

	if !strings.Contains(compactSQL, "foreign key ( organization_id, provider_call_id, capability, provider, configured_model ) references provider_calls ( organization_id, id, capability, provider, configured_model )") {
		t.Fatal("provider call attempts must preserve capability/provider/configured model context")
	}
}

func TestQueryProjectionMigrationDefinesReproducibleQueryPackageAndClaims(t *testing.T) {
	sqlBytes, err := queryProjectionMigrations.ReadFile("0002_query_projection.up.sql")
	if err != nil {
		t.Fatalf("read query projection migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))
	compactSQL := strings.Join(strings.Fields(sql), " ")

	for _, needle := range []string{
		"question_digest char(64) not null",
		"retrieval_configuration_digest char(64) not null",
		"ranking_configuration_digest char(64) not null",
		"candidate_digest char(64) not null",
		"decision text not null",
		"decision_reason text not null",
		"included boolean not null",
		"evidence_packages_query_digest_unique unique",
		"claim_type text not null",
		"claim_type in ('observed', 'generated', 'gap')",
		"citations_package_item_included check (package_item_included)",
		"citations_included_package_item_fk",
	} {
		if !strings.Contains(sql, needle) {
			t.Errorf("migration missing query/package/claim invariant %q", needle)
		}
	}
	if !strings.Contains(compactSQL, "references evidence_package_items ( organization_id, package_id, query_id, id, included, source_id, snapshot_id, evidence_id )") {
		t.Fatal("citations must reference the package item including its included flag and evidence identity")
	}
}

func TestQueryProjectionMigrationDefinesProviderAuditWithoutSecrets(t *testing.T) {
	sqlBytes, err := queryProjectionMigrations.ReadFile("0002_query_projection.up.sql")
	if err != nil {
		t.Fatalf("read query projection migration: %v", err)
	}
	sql := strings.ToLower(string(sqlBytes))

	cases := []struct {
		name string
		want []string
	}{
		{
			name: "provider call capability and models",
			want: []string{
				"capability text not null",
				"provider text not null",
				"configured_model text not null",
				"effective_model text",
				"provider_calls_capability check",
				"provider_calls_capability_context check",
			},
		},
		{
			name: "usage and normalized failures",
			want: []string{
				"input_tokens bigint not null default 0",
				"output_tokens bigint not null default 0",
				"total_tokens bigint not null default 0",
				"cost_micros bigint not null default 0",
				"latency_ms bigint not null default 0",
				"error_category",
				"provider_call_attempts_call_number_unique unique",
				"provider_calls_attempt_context_key unique",
				"provider_call_attempts_context_fk",
			},
		},
		{
			name: "transferred identity hashes",
			want: []string{
				"request_digest char(64) not null",
				"transferred_evidence_digest char(64)",
				"transferred_evidence_count bigint not null default 0",
				"response_digest char(64)",
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, fragment := range test.want {
				if !strings.Contains(sql, fragment) {
					t.Errorf("migration missing provider invariant %q", fragment)
				}
			}
		})
	}
}

func migrationTableSection(sql, table string) (string, bool) {
	start := strings.Index(sql, "create table "+table+" (")
	if start < 0 {
		return "", false
	}
	rest := sql[start+len("create table "+table+" ("):]
	end := strings.Index(rest, ");")
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}
