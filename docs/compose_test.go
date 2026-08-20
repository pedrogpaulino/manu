package docs

import (
	"os"
	"strings"
	"testing"
)

func TestComposeDefinesTheLocalCellWithoutSecrets(t *testing.T) {
	content, err := os.ReadFile("../compose.yaml")
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}
	text := string(content)

	for _, expected := range []string{
		"pgvector/pgvector:0.8.6-pg18-bookworm",
		"  postgres:",
		"  migrate:",
		"  api:",
		"condition: service_completed_successfully",
		"network_mode: host",
		"manu-postgres-data:/var/lib/postgresql",
		"manu-postgres-socket:/var/run/postgresql",
		"pg_isready",
		"test: [\"CMD\", \"/manu\", \"ready\"]",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("compose.yaml is missing %q", expected)
		}
	}

	if !strings.Contains(text, "image: manu:local") || strings.Count(text, "<<: *manu-image") != 2 {
		t.Fatalf("compose.yaml must reuse manu:local for migration and API")
	}
	if strings.Contains(text, "latest") {
		t.Fatal("compose.yaml must not use a floating latest image tag")
	}
	for _, forbidden := range []string{"sk-", "BEGIN PRIVATE KEY", "Bearer ", "OPENAI_API_KEY="} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("compose.yaml contains secret-like material %q", forbidden)
		}
	}

	postgres := section(text, "  postgres:\n", "  migrate:\n")
	if strings.Contains(postgres, "ports:") {
		t.Fatal("PostgreSQL must not publish a TCP port")
	}
	migrate := section(text, "  migrate:\n", "  api:\n")
	if !strings.Contains(migrate, "command: [\"migrate\", \"--json\"]") {
		t.Fatal("migration service must run manu migrate")
	}
	api := section(text, "  api:\n", "volumes:\n")
	if strings.Contains(api, "ports:") {
		t.Fatal("the API must publish only through its loopback host bind")
	}
	if !strings.Contains(api, "MANU_POSTGRES_HOST: /var/run/postgresql") {
		t.Fatal("the API must use the shared PostgreSQL Unix socket")
	}
	if !strings.Contains(api, "MANU_INGESTION_STAGING_DIRECTORY: /var/lib/manu/ingestions") ||
		!strings.Contains(text, "manu-ingestion-data:/var/lib/manu/ingestions") {
		t.Fatal("the API must mount durable ingestion staging")
	}

	for _, expected := range []string{
		"MANU_EMBEDDING_ENABLED: ${MANU_EMBEDDING_ENABLED:-false}",
		"MANU_EMBEDDING_PROVIDER: ${MANU_EMBEDDING_PROVIDER:-}",
		"MANU_EMBEDDING_MODEL: ${MANU_EMBEDDING_MODEL:-}",
		"MANU_EMBEDDING_BASE_URL: ${MANU_EMBEDDING_BASE_URL:-}",
		"MANU_EMBEDDING_API_KEY: ${MANU_EMBEDDING_API_KEY:-}",
		"MANU_EMBEDDING_TIMEOUT: ${MANU_EMBEDDING_TIMEOUT:-30s}",
		"MANU_EMBEDDING_MAX_BATCH_SIZE: ${MANU_EMBEDDING_MAX_BATCH_SIZE:-32}",
		"MANU_EMBEDDING_DIMENSION: ${MANU_EMBEDDING_DIMENSION:-0}",
		"MANU_EMBEDDING_BUDGET_MAX_REQUESTS: ${MANU_EMBEDDING_BUDGET_MAX_REQUESTS:-0}",
		"MANU_EMBEDDING_BUDGET_MAX_INPUT_TOKENS: ${MANU_EMBEDDING_BUDGET_MAX_INPUT_TOKENS:-0}",
		"MANU_EMBEDDING_BUDGET_MAX_OUTPUT_TOKENS: ${MANU_EMBEDDING_BUDGET_MAX_OUTPUT_TOKENS:-0}",
		"MANU_EMBEDDING_BUDGET_MAX_COST_USD: ${MANU_EMBEDDING_BUDGET_MAX_COST_USD:-0}",
		"MANU_GENERATION_ENABLED: ${MANU_GENERATION_ENABLED:-false}",
		"MANU_GENERATION_PROVIDER: ${MANU_GENERATION_PROVIDER:-}",
		"MANU_GENERATION_MODEL: ${MANU_GENERATION_MODEL:-}",
		"MANU_GENERATION_BASE_URL: ${MANU_GENERATION_BASE_URL:-}",
		"MANU_GENERATION_API_KEY: ${MANU_GENERATION_API_KEY:-}",
		"MANU_GENERATION_PROTOCOL: ${MANU_GENERATION_PROTOCOL:-responses}",
		"MANU_GENERATION_TIMEOUT: ${MANU_GENERATION_TIMEOUT:-1m}",
		"MANU_GENERATION_MAX_OUTPUT_TOKENS: ${MANU_GENERATION_MAX_OUTPUT_TOKENS:-2048}",
		"MANU_GENERATION_TEMPERATURE: ${MANU_GENERATION_TEMPERATURE:-0}",
		"MANU_GENERATION_BUDGET_MAX_REQUESTS: ${MANU_GENERATION_BUDGET_MAX_REQUESTS:-0}",
		"MANU_GENERATION_BUDGET_MAX_INPUT_TOKENS: ${MANU_GENERATION_BUDGET_MAX_INPUT_TOKENS:-0}",
		"MANU_GENERATION_BUDGET_MAX_OUTPUT_TOKENS: ${MANU_GENERATION_BUDGET_MAX_OUTPUT_TOKENS:-0}",
		"MANU_GENERATION_BUDGET_MAX_COST_USD: ${MANU_GENERATION_BUDGET_MAX_COST_USD:-0}",
		"MANU_POLICY_EXTERNAL_TRANSFER: ${MANU_POLICY_EXTERNAL_TRANSFER:-deny}",
	} {
		if !strings.Contains(api, expected) {
			t.Fatalf("api service does not forward %q", expected)
		}
	}
}

func section(content, start, end string) string {
	begin := strings.Index(content, start)
	if begin < 0 {
		return ""
	}
	begin += len(start)
	finish := strings.Index(content[begin:], end)
	if finish < 0 {
		return content[begin:]
	}
	return content[begin : begin+finish]
}
