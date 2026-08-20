package config

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValidAndContainsNoCredentials(t *testing.T) {
	configuration := Default()
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Default().Validate() error = %v", err)
	}
	if configuration.Server.ListenAddress != DefaultListenAddress {
		t.Fatalf("default listen address = %q, want %q", configuration.Server.ListenAddress, DefaultListenAddress)
	}
	if configuration.Organization.ID != DefaultOrganizationID {
		t.Fatalf("default organization ID = %q, want %q", configuration.Organization.ID, DefaultOrganizationID)
	}
	if configuration.Ingestion.StagingDirectory != DefaultIngestionStagingDirectory {
		t.Fatalf("default ingestion staging directory = %q, want %q", configuration.Ingestion.StagingDirectory, DefaultIngestionStagingDirectory)
	}
	if configuration.Postgres.DSN != "" || configuration.Postgres.Password != "" {
		t.Fatal("database defaults unexpectedly contain credentials")
	}
	if configuration.Embedding.APIKey != "" || configuration.Generation.APIKey != "" {
		t.Fatal("AI defaults unexpectedly contain credentials")
	}
	if configuration.Embedding.Budget != (BudgetConfig{}) ||
		configuration.Generation.Budget != (BudgetConfig{}) ||
		configuration.Evaluation.Budget != (BudgetConfig{}) {
		t.Fatal("budget defaults unexpectedly authorize consumption")
	}
}

func TestLoadEnvAppliesTypedValues(t *testing.T) {
	environment := map[string]string{
		"MANU_SERVER_LISTEN_ADDRESS":               "127.0.0.1:9090",
		"MANU_SERVER_READ_TIMEOUT":                 "2s",
		"MANU_ORGANIZATION_ID":                     "payments",
		"MANU_ORGANIZATION_NAME":                   "Payments",
		"MANU_INGESTION_STAGING_DIRECTORY":         "/var/lib/manu/ingestions",
		"MANU_POSTGRES_HOST":                       "db.internal",
		"MANU_POSTGRES_PORT":                       "5433",
		"MANU_POSTGRES_DATABASE":                   "knowledge",
		"MANU_POSTGRES_USER":                       "runtime",
		"MANU_POSTGRES_PASSWORD":                   "test-only-password",
		"MANU_LIMITS_MAX_BUNDLE_BYTES":             "1048576",
		"MANU_POLICY_EXTERNAL_TRANSFER":            "allow",
		"MANU_RETRIEVAL_TOP_K":                     "7",
		"MANU_EMBEDDING_ENABLED":                   "true",
		"MANU_EMBEDDING_PROVIDER":                  "simulated",
		"MANU_EMBEDDING_MODEL":                     "unit-embedder",
		"MANU_EMBEDDING_TIMEOUT":                   "1s",
		"MANU_EMBEDDING_MAX_BATCH_SIZE":            "4",
		"MANU_EMBEDDING_DIMENSION":                 "3",
		"MANU_EMBEDDING_BUDGET_MAX_REQUESTS":       "4",
		"MANU_GENERATION_ENABLED":                  "true",
		"MANU_GENERATION_PROVIDER":                 "simulated",
		"MANU_GENERATION_MODEL":                    "unit-generator",
		"MANU_GENERATION_PROTOCOL":                 "chat_completions",
		"MANU_GENERATION_TIMEOUT":                  "3s",
		"MANU_GENERATION_MAX_OUTPUT_TOKENS":        "128",
		"MANU_GENERATION_TEMPERATURE":              "0.25",
		"MANU_GENERATION_BUDGET_MAX_COST_USD":      "0.50",
		"MANU_EVALUATION_LIVE":                     "true",
		"MANU_EVALUATION_BUDGET_MAX_REQUESTS":      "2",
		"MANU_EVALUATION_BUDGET_MAX_INPUT_TOKENS":  "100",
		"MANU_EVALUATION_BUDGET_MAX_OUTPUT_TOKENS": "100",
		"MANU_EVALUATION_BUDGET_MAX_COST_USD":      "0.10",
		"MANU_UNDOCUMENTED_SETTING":                "ignored",
	}

	configuration, err := LoadEnv(environment)
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if got := configuration.Server.ListenAddress; got != "127.0.0.1:9090" {
		t.Errorf("listen address = %q, want 127.0.0.1:9090", got)
	}
	if got := configuration.Server.ReadTimeout; got != 2*time.Second {
		t.Errorf("read timeout = %s, want 2s", got)
	}
	if got := configuration.Organization.ID; got != "payments" {
		t.Errorf("organization ID = %q, want payments", got)
	}
	if got := configuration.Postgres.Host; got != "db.internal" {
		t.Errorf("database host = %q, want db.internal", got)
	}
	if got := configuration.Ingestion.StagingDirectory; got != "/var/lib/manu/ingestions" {
		t.Errorf("ingestion staging directory = %q, want /var/lib/manu/ingestions", got)
	}
	if got := configuration.Postgres.Port; got != 5433 {
		t.Errorf("database port = %d, want 5433", got)
	}
	if got := configuration.Postgres.Password; got != "test-only-password" {
		t.Errorf("database password was not loaded")
	}
	if got := configuration.Limits.MaxBundleBytes; got != 1<<20 {
		t.Errorf("max bundle bytes = %d, want %d", got, 1<<20)
	}
	if got := configuration.Policy.ExternalTransfer; got != DecisionAllow {
		t.Errorf("external transfer policy = %q, want allow", got)
	}
	if got := configuration.Retrieval.TopK; got != 7 {
		t.Errorf("retrieval top k = %d, want 7", got)
	}
	if !configuration.Embedding.Enabled || configuration.Embedding.Dimension != 3 {
		t.Errorf("embedding configuration = %#v", configuration.Embedding)
	}
	if configuration.Generation.Protocol != ProtocolChatCompletions || configuration.Generation.Temperature != 0.25 {
		t.Errorf("generation configuration = %#v", configuration.Generation)
	}
	if !configuration.Evaluation.Live || configuration.Evaluation.Budget.MaxRequests != 2 {
		t.Errorf("evaluation configuration = %#v", configuration.Evaluation)
	}
}

func TestLoadEnvironmentUsesCanonicalNames(t *testing.T) {
	environment := map[string]string{
		"MANU_POSTGRES_HOST": "canonical-host",
		"MANU_POSTGRES_PORT": "5544",
	}

	configuration, err := LoadEnv(environment)
	if err != nil {
		t.Fatalf("LoadEnv() error = %v", err)
	}
	if configuration.Postgres.Host != "canonical-host" {
		t.Fatalf("canonical host = %q, want canonical-host", configuration.Postgres.Host)
	}
	if configuration.Postgres.Port != 5544 {
		t.Fatalf("database port = %d, want 5544", configuration.Postgres.Port)
	}
}

func TestPostgresSocketHostIsSupportedAndUnsafeHostsAreRejected(t *testing.T) {
	socket, err := LoadEnv(map[string]string{"MANU_POSTGRES_HOST": "/var/run/postgresql"})
	if err != nil {
		t.Fatalf("LoadEnv(socket host) error = %v", err)
	}
	if socket.Postgres.Host != "/var/run/postgresql" {
		t.Fatalf("socket host = %q", socket.Postgres.Host)
	}
	for _, host := range []string{"/", "db/user", "user@db"} {
		if _, err := LoadEnv(map[string]string{"MANU_POSTGRES_HOST": host}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("LoadEnv(%q) error = %v, want ErrInvalid", host, err)
		}
	}
}

func TestLoadRejectsMalformedValuesWithoutEchoingValue(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "boolean", env: map[string]string{"MANU_EMBEDDING_ENABLED": "maybe"}},
		{name: "integer", env: map[string]string{"MANU_SERVER_MAX_BODY_BYTES": "large"}},
		{name: "duration", env: map[string]string{"MANU_SERVER_READ_TIMEOUT": "soon"}},
		{name: "number", env: map[string]string{"MANU_RETRIEVAL_TEXT_WEIGHT": "many"}},
		{name: "secret-adjacent", env: map[string]string{
			"MANU_POSTGRES_PASSWORD": "test-only-secret-value",
			"MANU_POSTGRES_PORT":     "invalid-port",
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := LoadEnv(test.env)
			if err == nil {
				t.Fatal("LoadEnv() error = nil, want error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("LoadEnv() error = %v, want ErrInvalid", err)
			}
			if strings.Contains(err.Error(), "test-only-secret-value") {
				t.Fatalf("error echoed a credential: %v", err)
			}
		})
	}
}

func TestLoopbackValidation(t *testing.T) {
	tests := []struct {
		name    string
		address string
		valid   bool
	}{
		{name: "ipv4", address: "127.0.0.1:8080", valid: true},
		{name: "ipv6", address: "[::1]:8080", valid: true},
		{name: "localhost", address: "localhost:8080", valid: true},
		{name: "wildcard ipv4", address: "0.0.0.0:8080"},
		{name: "wildcard ipv6", address: "[::]:8080"},
		{name: "remote", address: "192.0.2.10:8080"},
		{name: "missing port", address: "127.0.0.1"},
		{name: "invalid port", address: "127.0.0.1:0"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := Default().Server
			server.ListenAddress = test.address
			err := server.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() error = nil, want loopback error")
			}
		})
	}
}

func TestValidationRejectsIncoherentConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "unsupported server mode", mutate: func(configuration *Config) {
			configuration.Server.Mode = ModeUnknown
		}},
		{name: "empty organization", mutate: func(configuration *Config) {
			configuration.Organization.ID = ""
		}},
		{name: "empty database user", mutate: func(configuration *Config) {
			configuration.Postgres.User = ""
		}},
		{name: "manifest exceeds bundle", mutate: func(configuration *Config) {
			configuration.Limits.MaxManifestBytes = configuration.Limits.MaxBundleBytes + 1
		}},
		{name: "retrieval candidate bound", mutate: func(configuration *Config) {
			configuration.Retrieval.MaxCandidates = configuration.Retrieval.TopK - 1
		}},
		{name: "server body bound", mutate: func(configuration *Config) {
			configuration.Server.MaxBodyBytes = configuration.Limits.MaxBundleBytes - 1
		}},
		{name: "retrieval package units", mutate: func(configuration *Config) {
			configuration.Retrieval.MaxPackageUnits = configuration.Limits.MaxEvidenceUnits + 1
		}},
		{name: "negative budget", mutate: func(configuration *Config) {
			configuration.Generation.Budget.MaxRequests = -1
		}},
		{name: "enabled embedding without capability", mutate: func(configuration *Config) {
			configuration.Embedding.Enabled = true
			configuration.Embedding.Dimension = 3
		}},
		{name: "enabled generation without credential", mutate: func(configuration *Config) {
			configuration.Generation.Enabled = true
			configuration.Generation.Provider = ProviderOpenAI
			configuration.Generation.Model = "unit-model"
		}},
		{name: "live evaluation without budget", mutate: func(configuration *Config) {
			configuration.Evaluation.Live = true
			configuration.Evaluation.Budget = BudgetConfig{}
		}},
		{name: "external embedding without budget", mutate: func(configuration *Config) {
			configuration.Embedding.Enabled = true
			configuration.Embedding.Provider = ProviderOpenAI
			configuration.Embedding.Model = "unit-model"
			configuration.Embedding.APIKey = "synthetic-key"
			configuration.Embedding.Dimension = 3
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := Default()
			test.mutate(&configuration)
			err := configuration.Validate()
			if err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Validate() error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestDisabledCapabilitiesMayUseZeroOptionalFields(t *testing.T) {
	configuration := Default()
	configuration.Embedding = EmbeddingConfig{}
	configuration.Generation = GenerationConfig{}
	if err := configuration.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want disabled capabilities to be valid", err)
	}
}

func TestSecretFieldsAreExcludedFromJSONAndString(t *testing.T) {
	configuration := Default()
	values := []string{
		"postgres://runtime:test-only-password@localhost/manu",
		"test-only-password",
		"test-only-embedding-key",
		"test-only-generation-key",
	}
	configuration.Postgres.DSN = values[0]
	configuration.Postgres.Password = values[1]
	configuration.Embedding.APIKey = values[2]
	configuration.Generation.APIKey = values[3]

	encoded, err := json.Marshal(configuration)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, value := range values {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("JSON contains credential %q: %s", value, encoded)
		}
	}
	for _, key := range []string{`"dsn"`, `"password"`, `"api_key"`} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("JSON contains secret field %s: %s", key, encoded)
		}
	}

	redacted := configuration.Redacted()
	if redacted.Postgres.DSN != "" || redacted.Postgres.Password != "" ||
		redacted.Embedding.APIKey != "" || redacted.Generation.APIKey != "" {
		t.Fatalf("Redacted() retained a credential: %#v", redacted)
	}
	if strings.Contains(configuration.String(), values[1]) || strings.Contains(configuration.String(), values[2]) {
		t.Fatal("String() contains a credential")
	}
}

func TestBudgetValidation(t *testing.T) {
	tests := []struct {
		name  string
		value BudgetConfig
		valid bool
	}{
		{name: "unset budget", value: BudgetConfig{}, valid: true},
		{name: "finite positive", value: BudgetConfig{MaxRequests: 1, MaxCostUSD: 0.5}, valid: true},
		{name: "negative requests", value: BudgetConfig{MaxRequests: -1}},
		{name: "negative tokens", value: BudgetConfig{MaxInputTokens: -1}},
		{name: "negative cost", value: BudgetConfig{MaxCostUSD: -0.1}},
		{name: "nan cost", value: BudgetConfig{MaxCostUSD: math.NaN()}},
		{name: "infinite cost", value: BudgetConfig{MaxCostUSD: math.Inf(1)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.value.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func TestActiveBudgetRequiresEveryPositiveLimit(t *testing.T) {
	active := BudgetConfig{
		MaxRequests:     1,
		MaxInputTokens:  1,
		MaxOutputTokens: 1,
		MaxCostUSD:      0.01,
	}
	tests := []struct {
		name  string
		value BudgetConfig
		valid bool
	}{
		{name: "all limits", value: active, valid: true},
		{name: "requests missing", value: BudgetConfig{MaxInputTokens: 1, MaxOutputTokens: 1, MaxCostUSD: 0.01}},
		{name: "input tokens missing", value: BudgetConfig{MaxRequests: 1, MaxOutputTokens: 1, MaxCostUSD: 0.01}},
		{name: "output tokens missing", value: BudgetConfig{MaxRequests: 1, MaxInputTokens: 1, MaxCostUSD: 0.01}},
		{name: "cost missing", value: BudgetConfig{MaxRequests: 1, MaxInputTokens: 1, MaxOutputTokens: 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.value.validateActive()
			if test.valid && err != nil {
				t.Fatalf("validateActive() error = %v, want nil", err)
			}
			if !test.valid && err == nil {
				t.Fatal("validateActive() error = nil, want error")
			}
		})
	}
}
