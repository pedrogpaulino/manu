package docs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestEnvironmentExampleIsSafeAndKeepsAIProfilesIndependent(t *testing.T) {
	content, err := os.ReadFile("../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	text := string(content)

	for _, forbidden := range []string{"sk-", "bearer ", "BEGIN PRIVATE KEY", "OPENAI_API_KEY=", "OPENROUTER_API_KEY="} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf(".env.example contains secret-like material %q", forbidden)
		}
	}

	for _, key := range []string{
		"MANU_POSTGRES_PASSWORD",
		"MANU_EMBEDDING_API_KEY",
		"MANU_GENERATION_API_KEY",
	} {
		value, ok := environmentValue(text, key)
		if !ok {
			t.Fatalf(".env.example does not declare %s", key)
		}
		if value != "" {
			t.Fatalf(".env.example contains a value for secret %s", key)
		}
	}

	for _, expected := range []string{
		"MANU_EMBEDDING_ENABLED=false",
		"MANU_EMBEDDING_PROVIDER=",
		"MANU_EMBEDDING_MODEL=",
		"MANU_EMBEDDING_BASE_URL=",
		"MANU_EMBEDDING_TIMEOUT=30s",
		"MANU_EMBEDDING_MAX_BATCH_SIZE=32",
		"MANU_EMBEDDING_DIMENSION=0",
		"MANU_EMBEDDING_BUDGET_MAX_REQUESTS=0",
		"MANU_EMBEDDING_BUDGET_MAX_INPUT_TOKENS=0",
		"MANU_EMBEDDING_BUDGET_MAX_OUTPUT_TOKENS=0",
		"MANU_EMBEDDING_BUDGET_MAX_COST_USD=0",
		"MANU_GENERATION_ENABLED=false",
		"MANU_GENERATION_PROVIDER=",
		"MANU_GENERATION_MODEL=",
		"MANU_GENERATION_BASE_URL=",
		"MANU_GENERATION_PROTOCOL=responses",
		"MANU_GENERATION_TIMEOUT=1m",
		"MANU_GENERATION_MAX_OUTPUT_TOKENS=2048",
		"MANU_GENERATION_TEMPERATURE=0",
		"MANU_GENERATION_BUDGET_MAX_REQUESTS=0",
		"MANU_GENERATION_BUDGET_MAX_INPUT_TOKENS=0",
		"MANU_GENERATION_BUDGET_MAX_OUTPUT_TOKENS=0",
		"MANU_GENERATION_BUDGET_MAX_COST_USD=0",
		"MANU_EVALUATION_LIVE=false",
		"MANU_POLICY_EXTERNAL_TRANSFER=deny",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf(".env.example is missing %s", expected)
		}
	}
}

func TestEnvironmentExampleKeysBelongToConfigLoader(t *testing.T) {
	example, err := os.ReadFile("../.env.example")
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	loader, err := os.ReadFile("../internal/config/config.go")
	if err != nil {
		t.Fatalf("read config loader: %v", err)
	}

	known := make(map[string]struct{})
	keyPattern := regexp.MustCompile(`\[\]string\{"(MANU_[A-Z0-9_]+)"\}`)
	for _, match := range keyPattern.FindAllSubmatch(loader, -1) {
		known[string(match[1])] = struct{}{}
	}
	if len(known) == 0 {
		t.Fatal("could not discover MANU_* keys in config loader")
	}

	for _, line := range strings.Split(string(example), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf(".env.example contains a line without '=': %q", line)
		}
		if !strings.HasPrefix(key, "MANU_") {
			t.Fatalf(".env.example declares a non-MANU key: %q", key)
		}
		if _, ok := known[key]; !ok {
			t.Errorf(".env.example key %s is not accepted by config loader", key)
		}
	}
}

func environmentValue(content, key string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		prefix := key + "="
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}
