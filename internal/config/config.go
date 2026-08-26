// Package config defines the typed configuration for the local Manu platform.
//
// Configuration is deliberately composed from standard-library types. The
// package does not read files, make network calls, or log credentials. Use
// Default to obtain safe local defaults, then Load to apply MANU_* variables.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// EnvPrefix is the prefix accepted by the environment loader.
	EnvPrefix = "MANU_"

	// DefaultListenAddress keeps the unauthenticated platform on IPv4 loopback.
	DefaultListenAddress = "127.0.0.1:8080"

	// DefaultOrganizationID identifies the single local organization used by
	// the unauthenticated platform mode.
	DefaultOrganizationID = "local"
)

var (
	// ErrInvalid identifies a configuration that cannot be used safely.
	ErrInvalid = errors.New("config: invalid configuration")
)

// ServerMode identifies the runtime mode that consumes the configuration.
type ServerMode string

const (
	// ModeUnknown is the zero value and is not valid for a loaded Config.
	ModeUnknown ServerMode = ""
	// ModePlatform is the unauthenticated local HTTP platform mode.
	ModePlatform ServerMode = "platform"
)

// Decision controls whether a content operation is allowed, redacted, or
// denied. Persistence and external transfer are evaluated independently.
type Decision string

const (
	DecisionUnknown Decision = ""
	DecisionAllow   Decision = "allow"
	DecisionRedact  Decision = "redact"
	DecisionDeny    Decision = "deny"
)

// Provider identifies an AI provider without exposing provider DTOs to the
// rest of the application.
type Provider string

const (
	ProviderUnknown          Provider = ""
	ProviderOpenAI           Provider = "openai"
	ProviderOpenRouter       Provider = "openrouter"
	ProviderOpenAICompatible Provider = "openai-compatible"
	ProviderSimulated        Provider = "simulated"
)

// Protocol identifies the explicitly selected generation protocol.
type Protocol string

const (
	ProtocolUnknown         Protocol = ""
	ProtocolResponses       Protocol = "responses"
	ProtocolChatCompletions Protocol = "chat_completions"
)

// Config is the complete non-secret and secret-bearing configuration for the
// local platform. API keys, database passwords, and DSNs are excluded from
// JSON serialization and from Config's String and LogValue representations.
type Config struct {
	Server       ServerConfig       `json:"server"`
	MCP          MCPConfig          `json:"mcp"`
	Organization OrganizationConfig `json:"organization"`
	Postgres     PostgresConfig     `json:"postgres"`
	Limits       LimitsConfig       `json:"limits"`
	Ingestion    IngestionConfig    `json:"ingestion"`
	Policy       PolicyConfig       `json:"policy"`
	Retrieval    RetrievalConfig    `json:"retrieval"`
	Embedding    EmbeddingConfig    `json:"embedding"`
	Generation   GenerationConfig   `json:"generation"`
	Evaluation   EvaluationConfig   `json:"evaluation"`
}

// MCPConfig controls the optional local MCP stdio adapter. MCP is disabled by
// default and is never enabled implicitly by another capability or server
// mode.
type MCPConfig struct {
	Enabled bool `json:"enabled"`
}

// ServerConfig controls the HTTP listener, request deadlines, and body and
// concurrency bounds. Platform mode is intentionally loopback-only until an
// authenticated mode exists.
type ServerConfig struct {
	Mode                  ServerMode    `json:"mode"`
	ListenAddress         string        `json:"listen_address"`
	ReadTimeout           time.Duration `json:"read_timeout"`
	WriteTimeout          time.Duration `json:"write_timeout"`
	IdleTimeout           time.Duration `json:"idle_timeout"`
	ShutdownTimeout       time.Duration `json:"shutdown_timeout"`
	MaxHeaderBytes        int           `json:"max_header_bytes"`
	MaxBodyBytes          int64         `json:"max_body_bytes"`
	MaxConcurrentRequests int           `json:"max_concurrent_requests"`
}

// OrganizationConfig identifies the only organization available in local
// unauthenticated platform mode.
type OrganizationConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// PostgresConfig describes PostgreSQL connectivity and pool bounds. DSN and
// Password are accepted from the environment but are never serialized.
type PostgresConfig struct {
	DSN             string        `json:"-"`
	Host            string        `json:"host"`
	Port            int           `json:"port"`
	Database        string        `json:"database"`
	User            string        `json:"user"`
	Password        string        `json:"-"`
	SSLMode         string        `json:"ssl_mode"`
	MaxOpenConns    int           `json:"max_open_conns"`
	MaxIdleConns    int           `json:"max_idle_conns"`
	ConnMaxLifetime time.Duration `json:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `json:"conn_max_idle_time"`
}

// LimitsConfig bounds bundle, evidence, query, and local concurrency work.
type LimitsConfig struct {
	MaxBundleBytes          int64 `json:"max_bundle_bytes"`
	MaxManifestBytes        int64 `json:"max_manifest_bytes"`
	MaxEvidenceUnits        int   `json:"max_evidence_units"`
	MaxEvidenceBytes        int64 `json:"max_evidence_bytes"`
	MaxEvidenceTextBytes    int64 `json:"max_evidence_text_bytes"`
	MaxQueryBytes           int64 `json:"max_query_bytes"`
	MaxConcurrentIngestions int   `json:"max_concurrent_ingestions"`
	MaxConcurrentQueries    int   `json:"max_concurrent_queries"`
}

// IngestionConfig controls the durable local staging directory used by the
// in-process ingestion executor. It is application state, not the Agent's
// source directory, and is never derived from an incoming request.
type IngestionConfig struct {
	StagingDirectory string `json:"staging_directory"`
}

// DefaultIngestionStagingDirectory is the safe local default. Compose
// overrides it with its named persistent volume path.
const DefaultIngestionStagingDirectory = "/tmp/manu/ingestions"

// PolicyConfig separates local persistence authorization from authorization
// to transfer content to an external provider.
type PolicyConfig struct {
	Persist          Decision `json:"persist"`
	ExternalTransfer Decision `json:"external_transfer"`
}

// RetrievalConfig bounds and weights deterministic hybrid retrieval.
type RetrievalConfig struct {
	TopK              int     `json:"top_k"`
	MaxCandidates     int     `json:"max_candidates"`
	MaxRelationHops   int     `json:"max_relation_hops"`
	MaxRelationFanOut int     `json:"max_relation_fan_out"`
	MaxPackageUnits   int     `json:"max_package_units"`
	MaxPackageBytes   int64   `json:"max_package_bytes"`
	MaxPackageTokens  int     `json:"max_package_tokens"`
	ExactWeight       float64 `json:"exact_weight"`
	TextWeight        float64 `json:"text_weight"`
	VectorWeight      float64 `json:"vector_weight"`
	RelationWeight    float64 `json:"relation_weight"`
}

// BudgetConfig bounds requests, tokens, and estimated provider cost. Zero is
// unset and does not authorize provider consumption; negative values are
// invalid. Active external capabilities and live evaluation require explicit
// positive limits.
type BudgetConfig struct {
	MaxRequests     int     `json:"max_requests"`
	MaxInputTokens  int     `json:"max_input_tokens"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	MaxCostUSD      float64 `json:"max_cost_usd"`
}

// EmbeddingConfig configures the embedding capability independently from
// generation. APIKey is intentionally omitted from serialized forms.
type EmbeddingConfig struct {
	Enabled      bool          `json:"enabled"`
	Provider     Provider      `json:"provider"`
	Model        string        `json:"model"`
	BaseURL      string        `json:"base_url"`
	APIKey       string        `json:"-"`
	Timeout      time.Duration `json:"timeout"`
	MaxBatchSize int           `json:"max_batch_size"`
	Dimension    int           `json:"dimension"`
	Budget       BudgetConfig  `json:"budget"`
}

// GenerationConfig configures the generation capability independently from
// embeddings. Protocol and provider are explicit; no silent fallback is
// implied by this configuration.
type GenerationConfig struct {
	Enabled         bool          `json:"enabled"`
	Provider        Provider      `json:"provider"`
	Model           string        `json:"model"`
	BaseURL         string        `json:"base_url"`
	APIKey          string        `json:"-"`
	Protocol        Protocol      `json:"protocol"`
	Timeout         time.Duration `json:"timeout"`
	MaxOutputTokens int           `json:"max_output_tokens"`
	Temperature     float64       `json:"temperature"`
	Budget          BudgetConfig  `json:"budget"`
}

// EvaluationConfig controls the opt-in live evaluation budget. Simulation is
// the default and does not need provider credentials.
type EvaluationConfig struct {
	Live   bool         `json:"live"`
	Budget BudgetConfig `json:"budget"`
}

// Default returns a finite, local-only configuration. It never supplies a
// database password, DSN, API key, or other credential.
func Default() Config {
	budget := BudgetConfig{}

	return Config{
		Server: ServerConfig{
			Mode:                  ModePlatform,
			ListenAddress:         DefaultListenAddress,
			ReadTimeout:           15 * time.Second,
			WriteTimeout:          30 * time.Second,
			IdleTimeout:           60 * time.Second,
			ShutdownTimeout:       10 * time.Second,
			MaxHeaderBytes:        1 << 20,
			MaxBodyBytes:          64 << 20,
			MaxConcurrentRequests: 64,
		},
		MCP: MCPConfig{},
		Organization: OrganizationConfig{
			ID:   DefaultOrganizationID,
			Name: "Local organization",
		},
		Postgres: PostgresConfig{
			Host:            "127.0.0.1",
			Port:            5432,
			Database:        "manu",
			User:            "manu",
			SSLMode:         "disable",
			MaxOpenConns:    10,
			MaxIdleConns:    5,
			ConnMaxLifetime: 30 * time.Minute,
			ConnMaxIdleTime: 5 * time.Minute,
		},
		Limits: LimitsConfig{
			MaxBundleBytes:          64 << 20,
			MaxManifestBytes:        1 << 20,
			MaxEvidenceUnits:        10_000,
			MaxEvidenceBytes:        256 << 10,
			MaxEvidenceTextBytes:    64 << 10,
			MaxQueryBytes:           16 << 10,
			MaxConcurrentIngestions: 2,
			MaxConcurrentQueries:    16,
		},
		Ingestion: IngestionConfig{StagingDirectory: DefaultIngestionStagingDirectory},
		Policy: PolicyConfig{
			Persist:          DecisionAllow,
			ExternalTransfer: DecisionDeny,
		},
		Retrieval: RetrievalConfig{
			TopK:              20,
			MaxCandidates:     100,
			MaxRelationHops:   1,
			MaxRelationFanOut: 25,
			MaxPackageUnits:   16,
			MaxPackageBytes:   256 << 10,
			MaxPackageTokens:  16_000,
			ExactWeight:       1,
			TextWeight:        1,
			VectorWeight:      1,
			RelationWeight:    1,
		},
		Embedding: EmbeddingConfig{
			Timeout:      30 * time.Second,
			MaxBatchSize: 32,
			Budget:       budget,
		},
		Generation: GenerationConfig{
			Protocol:        ProtocolResponses,
			Timeout:         60 * time.Second,
			MaxOutputTokens: 2_048,
			Temperature:     0,
			Budget:          budget,
		},
		Evaluation: EvaluationConfig{
			Budget: budget,
		},
	}
}

// Load applies MANU_* environment variables from the process to the safe
// defaults and validates the resulting configuration.
func Load() (Config, error) {
	return LoadWithLookup(os.LookupEnv)
}

// LoadEnv applies a deterministic environment snapshot to the safe defaults.
// It does not read files, make network calls, or contact PostgreSQL or an AI
// provider. Unknown variables are ignored so unrelated MANU_* settings can be
// introduced without changing this package's behavior.
func LoadEnv(environment map[string]string) (Config, error) {
	return LoadWithLookup(func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	})
}

// LoadWithLookup applies variables returned by lookup to the safe defaults.
// The lookup function is called only for documented MANU_* keys and must not
// perform I/O beyond reading the caller's environment source.
func LoadWithLookup(lookup func(string) (string, bool)) (Config, error) {
	if lookup == nil {
		return Config{}, invalid("environment", "lookup must not be nil")
	}

	config := Default()
	if err := applyEnvironment(&config, lookup); err != nil {
		return Config{}, err
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func applyEnvironment(config *Config, lookup func(string) (string, bool)) error {
	setters := []environmentSetter{
		{keys: []string{"MANU_SERVER_MODE"}, set: func(value string) error {
			config.Server.Mode = ServerMode(strings.ToLower(strings.TrimSpace(value)))
			return nil
		}},
		{keys: []string{"MANU_SERVER_LISTEN_ADDRESS"}, set: func(value string) error {
			config.Server.ListenAddress = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_SERVER_READ_TIMEOUT"}, set: func(value string) error {
			return setDuration(&config.Server.ReadTimeout, value)
		}},
		{keys: []string{"MANU_SERVER_WRITE_TIMEOUT"}, set: func(value string) error {
			return setDuration(&config.Server.WriteTimeout, value)
		}},
		{keys: []string{"MANU_SERVER_IDLE_TIMEOUT"}, set: func(value string) error {
			return setDuration(&config.Server.IdleTimeout, value)
		}},
		{keys: []string{"MANU_SERVER_SHUTDOWN_TIMEOUT"}, set: func(value string) error {
			return setDuration(&config.Server.ShutdownTimeout, value)
		}},
		{keys: []string{"MANU_SERVER_MAX_HEADER_BYTES"}, set: func(value string) error {
			return setInt(&config.Server.MaxHeaderBytes, value)
		}},
		{keys: []string{"MANU_SERVER_MAX_BODY_BYTES"}, set: func(value string) error {
			return setInt64(&config.Server.MaxBodyBytes, value)
		}},
		{keys: []string{"MANU_SERVER_MAX_CONCURRENT_REQUESTS"}, set: func(value string) error {
			return setInt(&config.Server.MaxConcurrentRequests, value)
		}},
		{keys: []string{"MANU_MCP_ENABLED"}, set: func(value string) error {
			return setBool(&config.MCP.Enabled, value)
		}},
		{keys: []string{"MANU_ORGANIZATION_ID"}, set: func(value string) error {
			config.Organization.ID = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_ORGANIZATION_NAME"}, set: func(value string) error {
			config.Organization.Name = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_DSN"}, set: func(value string) error {
			config.Postgres.DSN = value
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_HOST"}, set: func(value string) error {
			config.Postgres.Host = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_PORT"}, set: func(value string) error {
			return setInt(&config.Postgres.Port, value)
		}},
		{keys: []string{"MANU_POSTGRES_DATABASE"}, set: func(value string) error {
			config.Postgres.Database = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_USER"}, set: func(value string) error {
			config.Postgres.User = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_PASSWORD"}, set: func(value string) error {
			config.Postgres.Password = value
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_SSL_MODE"}, set: func(value string) error {
			config.Postgres.SSLMode = strings.ToLower(strings.TrimSpace(value))
			return nil
		}},
		{keys: []string{"MANU_POSTGRES_MAX_OPEN_CONNS"}, set: func(value string) error {
			return setInt(&config.Postgres.MaxOpenConns, value)
		}},
		{keys: []string{"MANU_POSTGRES_MAX_IDLE_CONNS"}, set: func(value string) error {
			return setInt(&config.Postgres.MaxIdleConns, value)
		}},
		{keys: []string{"MANU_POSTGRES_CONN_MAX_LIFETIME"}, set: func(value string) error {
			return setDuration(&config.Postgres.ConnMaxLifetime, value)
		}},
		{keys: []string{"MANU_POSTGRES_CONN_MAX_IDLE_TIME"}, set: func(value string) error {
			return setDuration(&config.Postgres.ConnMaxIdleTime, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_BUNDLE_BYTES"}, set: func(value string) error {
			return setInt64(&config.Limits.MaxBundleBytes, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_MANIFEST_BYTES"}, set: func(value string) error {
			return setInt64(&config.Limits.MaxManifestBytes, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_EVIDENCE_UNITS"}, set: func(value string) error {
			return setInt(&config.Limits.MaxEvidenceUnits, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_EVIDENCE_BYTES"}, set: func(value string) error {
			return setInt64(&config.Limits.MaxEvidenceBytes, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_EVIDENCE_TEXT_BYTES"}, set: func(value string) error {
			return setInt64(&config.Limits.MaxEvidenceTextBytes, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_QUERY_BYTES"}, set: func(value string) error {
			return setInt64(&config.Limits.MaxQueryBytes, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_CONCURRENT_INGESTIONS"}, set: func(value string) error {
			return setInt(&config.Limits.MaxConcurrentIngestions, value)
		}},
		{keys: []string{"MANU_LIMITS_MAX_CONCURRENT_QUERIES"}, set: func(value string) error {
			return setInt(&config.Limits.MaxConcurrentQueries, value)
		}},
		{keys: []string{"MANU_INGESTION_STAGING_DIRECTORY"}, set: func(value string) error {
			config.Ingestion.StagingDirectory = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_POLICY_PERSIST"}, set: func(value string) error {
			config.Policy.Persist = Decision(strings.ToLower(strings.TrimSpace(value)))
			return nil
		}},
		{keys: []string{"MANU_POLICY_EXTERNAL_TRANSFER"}, set: func(value string) error {
			config.Policy.ExternalTransfer = Decision(strings.ToLower(strings.TrimSpace(value)))
			return nil
		}},
		{keys: []string{"MANU_RETRIEVAL_TOP_K"}, set: func(value string) error {
			return setInt(&config.Retrieval.TopK, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_MAX_CANDIDATES"}, set: func(value string) error {
			return setInt(&config.Retrieval.MaxCandidates, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_MAX_RELATION_HOPS"}, set: func(value string) error {
			return setInt(&config.Retrieval.MaxRelationHops, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_MAX_RELATION_FAN_OUT"}, set: func(value string) error {
			return setInt(&config.Retrieval.MaxRelationFanOut, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_MAX_PACKAGE_UNITS"}, set: func(value string) error {
			return setInt(&config.Retrieval.MaxPackageUnits, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_MAX_PACKAGE_BYTES"}, set: func(value string) error {
			return setInt64(&config.Retrieval.MaxPackageBytes, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_MAX_PACKAGE_TOKENS"}, set: func(value string) error {
			return setInt(&config.Retrieval.MaxPackageTokens, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_EXACT_WEIGHT"}, set: func(value string) error {
			return setFloat64(&config.Retrieval.ExactWeight, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_TEXT_WEIGHT"}, set: func(value string) error {
			return setFloat64(&config.Retrieval.TextWeight, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_VECTOR_WEIGHT"}, set: func(value string) error {
			return setFloat64(&config.Retrieval.VectorWeight, value)
		}},
		{keys: []string{"MANU_RETRIEVAL_RELATION_WEIGHT"}, set: func(value string) error {
			return setFloat64(&config.Retrieval.RelationWeight, value)
		}},
		{keys: []string{"MANU_EMBEDDING_ENABLED"}, set: func(value string) error {
			return setBool(&config.Embedding.Enabled, value)
		}},
		{keys: []string{"MANU_EMBEDDING_PROVIDER"}, set: func(value string) error {
			config.Embedding.Provider = Provider(strings.ToLower(strings.TrimSpace(value)))
			return nil
		}},
		{keys: []string{"MANU_EMBEDDING_MODEL"}, set: func(value string) error {
			config.Embedding.Model = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_EMBEDDING_BASE_URL"}, set: func(value string) error {
			config.Embedding.BaseURL = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_EMBEDDING_API_KEY"}, set: func(value string) error {
			config.Embedding.APIKey = value
			return nil
		}},
		{keys: []string{"MANU_EMBEDDING_TIMEOUT"}, set: func(value string) error {
			return setDuration(&config.Embedding.Timeout, value)
		}},
		{keys: []string{"MANU_EMBEDDING_MAX_BATCH_SIZE"}, set: func(value string) error {
			return setInt(&config.Embedding.MaxBatchSize, value)
		}},
		{keys: []string{"MANU_EMBEDDING_DIMENSION"}, set: func(value string) error {
			return setInt(&config.Embedding.Dimension, value)
		}},
		{keys: []string{"MANU_EMBEDDING_BUDGET_MAX_REQUESTS"}, set: func(value string) error {
			return setInt(&config.Embedding.Budget.MaxRequests, value)
		}},
		{keys: []string{"MANU_EMBEDDING_BUDGET_MAX_INPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Embedding.Budget.MaxInputTokens, value)
		}},
		{keys: []string{"MANU_EMBEDDING_BUDGET_MAX_OUTPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Embedding.Budget.MaxOutputTokens, value)
		}},
		{keys: []string{"MANU_EMBEDDING_BUDGET_MAX_COST_USD"}, set: func(value string) error {
			return setFloat64(&config.Embedding.Budget.MaxCostUSD, value)
		}},
		{keys: []string{"MANU_GENERATION_ENABLED"}, set: func(value string) error {
			return setBool(&config.Generation.Enabled, value)
		}},
		{keys: []string{"MANU_GENERATION_PROVIDER"}, set: func(value string) error {
			config.Generation.Provider = Provider(strings.ToLower(strings.TrimSpace(value)))
			return nil
		}},
		{keys: []string{"MANU_GENERATION_MODEL"}, set: func(value string) error {
			config.Generation.Model = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_GENERATION_BASE_URL"}, set: func(value string) error {
			config.Generation.BaseURL = strings.TrimSpace(value)
			return nil
		}},
		{keys: []string{"MANU_GENERATION_API_KEY"}, set: func(value string) error {
			config.Generation.APIKey = value
			return nil
		}},
		{keys: []string{"MANU_GENERATION_PROTOCOL"}, set: func(value string) error {
			config.Generation.Protocol = Protocol(strings.ToLower(strings.TrimSpace(value)))
			return nil
		}},
		{keys: []string{"MANU_GENERATION_TIMEOUT"}, set: func(value string) error {
			return setDuration(&config.Generation.Timeout, value)
		}},
		{keys: []string{"MANU_GENERATION_MAX_OUTPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Generation.MaxOutputTokens, value)
		}},
		{keys: []string{"MANU_GENERATION_TEMPERATURE"}, set: func(value string) error {
			return setFloat64(&config.Generation.Temperature, value)
		}},
		{keys: []string{"MANU_GENERATION_BUDGET_MAX_REQUESTS"}, set: func(value string) error {
			return setInt(&config.Generation.Budget.MaxRequests, value)
		}},
		{keys: []string{"MANU_GENERATION_BUDGET_MAX_INPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Generation.Budget.MaxInputTokens, value)
		}},
		{keys: []string{"MANU_GENERATION_BUDGET_MAX_OUTPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Generation.Budget.MaxOutputTokens, value)
		}},
		{keys: []string{"MANU_GENERATION_BUDGET_MAX_COST_USD"}, set: func(value string) error {
			return setFloat64(&config.Generation.Budget.MaxCostUSD, value)
		}},
		{keys: []string{"MANU_EVALUATION_LIVE"}, set: func(value string) error {
			return setBool(&config.Evaluation.Live, value)
		}},
		{keys: []string{"MANU_EVALUATION_BUDGET_MAX_REQUESTS"}, set: func(value string) error {
			return setInt(&config.Evaluation.Budget.MaxRequests, value)
		}},
		{keys: []string{"MANU_EVALUATION_BUDGET_MAX_INPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Evaluation.Budget.MaxInputTokens, value)
		}},
		{keys: []string{"MANU_EVALUATION_BUDGET_MAX_OUTPUT_TOKENS"}, set: func(value string) error {
			return setInt(&config.Evaluation.Budget.MaxOutputTokens, value)
		}},
		{keys: []string{"MANU_EVALUATION_BUDGET_MAX_COST_USD"}, set: func(value string) error {
			return setFloat64(&config.Evaluation.Budget.MaxCostUSD, value)
		}},
	}

	for _, setter := range setters {
		key, value, ok := lookupEnvironmentValue(lookup, setter.keys)
		if !ok {
			continue
		}
		if err := setter.set(value); err != nil {
			return fmt.Errorf("%w: %s: %s", ErrInvalid, key, err)
		}
	}
	return nil
}

type environmentSetter struct {
	keys []string
	set  func(string) error
}

func lookupEnvironmentValue(lookup func(string) (string, bool), keys []string) (string, string, bool) {
	for _, key := range keys {
		if value, ok := lookup(key); ok {
			return key, value, true
		}
	}
	return "", "", false
}

func setBool(destination *bool, value string) error {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return errors.New("must be a boolean")
	}
	*destination = parsed
	return nil
}

func setInt(destination *int, value string) error {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return errors.New("must be an integer")
	}
	*destination = parsed
	return nil
}

func setInt64(destination *int64, value string) error {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return errors.New("must be an integer")
	}
	*destination = parsed
	return nil
}

func setFloat64(destination *float64, value string) error {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return errors.New("must be a number")
	}
	*destination = parsed
	return nil
}

func setDuration(destination *time.Duration, value string) error {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil {
		return errors.New("must be a duration such as 5s or 1m")
	}
	*destination = parsed
	return nil
}

// Validate checks all boundaries without opening a socket, connecting to
// PostgreSQL, or contacting an AI provider. Errors identify fields but never
// include their values, which keeps diagnostics safe for secret-bearing
// fields.
func (c Config) Validate() error {
	checks := []struct {
		name string
		fn   func() error
	}{
		{"server", c.Server.Validate},
		{"mcp", c.MCP.Validate},
		{"organization", c.Organization.Validate},
		{"postgres", c.Postgres.Validate},
		{"limits", c.Limits.Validate},
		{"ingestion", c.Ingestion.Validate},
		{"policy", c.Policy.Validate},
		{"retrieval", c.Retrieval.Validate},
		{"embedding", c.Embedding.Validate},
		{"generation", c.Generation.Validate},
		{"evaluation", c.Evaluation.Validate},
	}
	for _, check := range checks {
		if err := check.fn(); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
	}
	if c.Server.MaxBodyBytes < c.Limits.MaxBundleBytes {
		return invalid("relationships", "server max body bytes must cover max bundle bytes")
	}
	if c.Retrieval.MaxPackageUnits > c.Limits.MaxEvidenceUnits {
		return invalid("relationships", "retrieval package units exceed evidence units")
	}
	if c.Retrieval.MaxPackageBytes > c.Limits.MaxBundleBytes {
		return invalid("relationships", "retrieval package bytes exceed bundle bytes")
	}
	return nil
}

// Validate checks the MCP feature flag. The flag is a typed boolean, so every
// in-memory value is valid; textual values are validated by the environment
// loader before they reach this type.
func (m MCPConfig) Validate() error {
	return nil
}

// String returns a JSON representation with credentials omitted. It is safe
// for ordinary diagnostic output; callers should still avoid logging source
// content alongside configuration.
func (c Config) String() string {
	return safeJSON(c)
}

// LogValue makes slog use the redacted representation instead of reflecting
// over exported secret-bearing fields.
func (c Config) LogValue() slog.Value {
	return slog.StringValue(c.String())
}

// Redacted returns a copy with all credential-bearing fields cleared. The
// returned value is useful for callers that need to pass configuration to a
// generic logger or report serializer.
func (c Config) Redacted() Config {
	c.Postgres.DSN = ""
	c.Postgres.Password = ""
	c.Embedding.APIKey = ""
	c.Generation.APIKey = ""
	return c
}

// String returns a JSON representation without the DSN or password.
func (p PostgresConfig) String() string {
	return safeJSON(p)
}

// LogValue makes slog omit PostgreSQL credentials when the subsection is
// logged directly.
func (p PostgresConfig) LogValue() slog.Value {
	return slog.StringValue(p.String())
}

// String returns a JSON representation without the API key.
func (e EmbeddingConfig) String() string {
	return safeJSON(e)
}

// LogValue makes slog omit the embedding API key when the subsection is
// logged directly.
func (e EmbeddingConfig) LogValue() slog.Value {
	return slog.StringValue(e.String())
}

// String returns a JSON representation without the API key.
func (g GenerationConfig) String() string {
	return safeJSON(g)
}

// LogValue makes slog omit the generation API key when the subsection is
// logged directly.
func (g GenerationConfig) LogValue() slog.Value {
	return slog.StringValue(g.String())
}

func safeJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// Validate checks server address and resource bounds. The platform mode has
// no authentication in this slice, so only loopback addresses are accepted.
func (s ServerConfig) Validate() error {
	if s.Mode != ModePlatform {
		return invalid("mode", "unsupported server mode")
	}
	if err := validateLoopbackAddress(s.ListenAddress); err != nil {
		return err
	}
	if s.ReadTimeout <= 0 || s.WriteTimeout <= 0 || s.IdleTimeout <= 0 || s.ShutdownTimeout <= 0 {
		return invalid("timeouts", "must be positive")
	}
	if s.MaxHeaderBytes <= 0 || s.MaxBodyBytes <= 0 || s.MaxConcurrentRequests <= 0 {
		return invalid("limits", "must be positive")
	}
	return nil
}

// Validate checks the local organization identity without contacting an
// identity service.
func (o OrganizationConfig) Validate() error {
	if !validIdentifier(o.ID) {
		return invalid("id", "must be a non-empty identifier")
	}
	if len(o.ID) > 128 || len(o.Name) > 256 {
		return invalid("identity", "is too long")
	}
	return nil
}

// Validate checks PostgreSQL address, pool, and optional DSN syntax. It does
// not connect to the database and never requires a password in defaults.
func (p PostgresConfig) Validate() error {
	if p.DSN != "" {
		if err := validatePostgresDSN(p.DSN); err != nil {
			return err
		}
	} else {
		host := strings.TrimSpace(p.Host)
		if host == "" || strings.TrimSpace(p.Database) == "" || strings.TrimSpace(p.User) == "" {
			return invalid("host/database/user", "must be configured")
		}
		if strings.ContainsAny(host, "\x00\r\n") {
			return invalid("host", "contains an invalid character")
		}
		if filepath.IsAbs(host) {
			if filepath.Clean(host) == string(filepath.Separator) {
				return invalid("host", "socket directory cannot be the filesystem root")
			}
		} else if strings.ContainsAny(host, "@/?#") {
			return invalid("host", "contains an invalid character")
		}
	}
	if p.Port < 1 || p.Port > 65_535 {
		return invalid("port", "must be between 1 and 65535")
	}
	if p.MaxOpenConns <= 0 || p.MaxIdleConns < 0 || p.MaxIdleConns > p.MaxOpenConns {
		return invalid("pool", "has invalid connection bounds")
	}
	if p.ConnMaxLifetime < 0 || p.ConnMaxIdleTime < 0 {
		return invalid("pool_timeouts", "must not be negative")
	}
	switch p.SSLMode {
	case "disable", "allow", "prefer", "require", "verify-ca", "verify-full":
		return nil
	default:
		return invalid("ssl_mode", "is unsupported")
	}
}

// Validate checks all ingestion and query resource bounds.
func (l LimitsConfig) Validate() error {
	if l.MaxBundleBytes <= 0 || l.MaxManifestBytes <= 0 || l.MaxEvidenceUnits <= 0 ||
		l.MaxEvidenceBytes <= 0 || l.MaxEvidenceTextBytes <= 0 || l.MaxQueryBytes <= 0 ||
		l.MaxConcurrentIngestions <= 0 || l.MaxConcurrentQueries <= 0 {
		return invalid("values", "must be positive")
	}
	if l.MaxManifestBytes > l.MaxBundleBytes || l.MaxEvidenceTextBytes > l.MaxEvidenceBytes {
		return invalid("relationships", "exceed their enclosing limit")
	}
	return nil
}

// Validate checks that the durable ingestion root is an absolute, bounded
// local path. The runtime additionally rejects symlinked roots and files.
func (i IngestionConfig) Validate() error {
	path := strings.TrimSpace(i.StagingDirectory)
	if path == "" || strings.ContainsAny(path, "\x00\r\n") || !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return invalid("staging_directory", "must be an absolute non-root path")
	}
	return nil
}

// Validate checks independent persistence and transfer decisions.
func (p PolicyConfig) Validate() error {
	if !validDecision(p.Persist) || !validDecision(p.ExternalTransfer) {
		return invalid("decisions", "must be allow, redact, or deny")
	}
	return nil
}

// Validate checks deterministic retrieval bounds and finite ranking weights.
func (r RetrievalConfig) Validate() error {
	if r.TopK <= 0 || r.MaxCandidates <= 0 || r.MaxCandidates < r.TopK ||
		r.MaxRelationFanOut <= 0 || r.MaxRelationHops < 0 || r.MaxRelationHops > 1 ||
		r.MaxPackageUnits <= 0 || r.MaxPackageBytes <= 0 || r.MaxPackageTokens <= 0 {
		return invalid("limits", "are inconsistent")
	}
	weights := []float64{r.ExactWeight, r.TextWeight, r.VectorWeight, r.RelationWeight}
	hasSignal := false
	for _, weight := range weights {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
			return invalid("weights", "must be finite and non-negative")
		}
		if weight > 0 {
			hasSignal = true
		}
	}
	if !hasSignal {
		return invalid("weights", "must enable at least one retrieval signal")
	}
	return nil
}

// Validate checks a provider budget without requiring it to be spent.
func (b BudgetConfig) Validate() error {
	if b.MaxRequests < 0 || b.MaxInputTokens < 0 || b.MaxOutputTokens < 0 ||
		math.IsNaN(b.MaxCostUSD) || math.IsInf(b.MaxCostUSD, 0) || b.MaxCostUSD < 0 {
		return invalid("values", "must be non-negative and finite")
	}
	return nil
}

// Validate checks the embedding capability independently from generation.
func (e EmbeddingConfig) Validate() error {
	if e.Timeout < 0 || e.MaxBatchSize < 0 || e.Dimension < 0 {
		return invalid("limits", "must not be negative")
	}
	if err := e.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if !e.Enabled {
		return nil
	}
	if e.Timeout == 0 || e.MaxBatchSize == 0 || e.Dimension == 0 {
		return invalid("limits", "must be positive when embedding is enabled")
	}
	if err := validateCapability(e.Enabled, e.Provider, e.Model, e.BaseURL, e.APIKey); err != nil {
		return err
	}
	if e.Provider != ProviderSimulated {
		if err := e.Budget.validateActive(); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
	}
	return nil
}

// Validate checks the generation capability independently from embeddings.
func (g GenerationConfig) Validate() error {
	if g.Timeout < 0 || g.MaxOutputTokens < 0 || math.IsNaN(g.Temperature) ||
		math.IsInf(g.Temperature, 0) || g.Temperature < 0 || g.Temperature > 2 {
		return invalid("limits", "are invalid")
	}
	if err := g.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if !g.Enabled {
		return nil
	}
	if g.Timeout == 0 || g.MaxOutputTokens == 0 {
		return invalid("limits", "must be positive when generation is enabled")
	}
	if g.Protocol != ProtocolResponses && g.Protocol != ProtocolChatCompletions {
		return invalid("protocol", "must be responses or chat_completions")
	}
	if err := validateGenerationProtocol(g.Provider, g.Protocol); err != nil {
		return err
	}
	if err := validateCapability(g.Enabled, g.Provider, g.Model, g.BaseURL, g.APIKey); err != nil {
		return err
	}
	if g.Provider != ProviderSimulated {
		if err := g.Budget.validateActive(); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
	}
	return nil
}

func validateGenerationProtocol(provider Provider, protocol Protocol) error {
	switch provider {
	case ProviderOpenAI:
		if protocol != ProtocolResponses {
			return invalid("protocol", "openai requires responses")
		}
	case ProviderOpenRouter:
		if protocol != ProtocolChatCompletions {
			return invalid("protocol", "openrouter requires chat_completions")
		}
	case ProviderOpenAICompatible:
		// The compatible adapter currently implements the chat-completions
		// contract. Configuration must select it explicitly; there is no
		// fallback to another protocol.
		if protocol != ProtocolChatCompletions {
			return invalid("protocol", "openai-compatible requires chat_completions")
		}
	case ProviderSimulated:
		// Simulation supports both contracts, but only when the caller has
		// explicitly selected one of them.
		if protocol != ProtocolResponses && protocol != ProtocolChatCompletions {
			return invalid("protocol", "simulated protocol is unsupported")
		}
	default:
		return invalid("protocol", "provider has no supported generation protocol")
	}
	return nil
}

// Validate checks the opt-in evaluation budget without making provider calls.
func (e EvaluationConfig) Validate() error {
	if err := e.Budget.Validate(); err != nil {
		return fmt.Errorf("budget: %w", err)
	}
	if e.Live {
		if err := e.Budget.validateActive(); err != nil {
			return fmt.Errorf("budget: %w", err)
		}
	}
	return nil
}

func (b BudgetConfig) validateActive() error {
	if b.MaxRequests <= 0 || b.MaxInputTokens <= 0 || b.MaxOutputTokens <= 0 || b.MaxCostUSD <= 0 {
		return invalid("values", "must be positive for an active external operation")
	}
	return nil
}

func validateCapability(enabled bool, provider Provider, model, baseURL, apiKey string) error {
	if !enabled {
		return nil
	}
	if provider == ProviderUnknown || strings.TrimSpace(model) == "" {
		return invalid("provider/model", "must be configured when enabled")
	}
	switch provider {
	case ProviderOpenAI, ProviderOpenRouter, ProviderOpenAICompatible, ProviderSimulated:
	default:
		return invalid("provider", "is unsupported")
	}
	if provider != ProviderSimulated && strings.TrimSpace(apiKey) == "" {
		return invalid("api_key", "must be supplied externally when enabled")
	}
	if (provider == ProviderOpenAICompatible || provider == ProviderOpenRouter) && strings.TrimSpace(baseURL) == "" {
		return invalid("base_url", "must be configured for compatible providers")
	}
	if baseURL != "" {
		if err := validateHTTPURL(baseURL); err != nil {
			return err
		}
	}
	return nil
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil || host == "" {
		return invalid("listen_address", "must include a loopback host and port")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65_535 {
		return invalid("listen_address", "has an invalid port")
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return invalid("listen_address", "must be loopback while authentication is unavailable")
	}
	return nil
}

func validatePostgresDSN(dsn string) error {
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
		return invalid("dsn", "must be a postgres or postgresql URL")
	}
	return nil
}

func validateHTTPURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return invalid("base_url", "must be an http(s) URL without credentials, query, or fragment")
	}
	return nil
}

func validIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && !strings.ContainsAny(value, "\r\n")
}

func validDecision(value Decision) bool {
	return value == DecisionAllow || value == DecisionRedact || value == DecisionDeny
}

func invalid(field, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalid, field, reason)
}
