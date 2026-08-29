package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/pedrogpaulino/manu/internal/analyzer"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	evaluationMetadataAgentID      = "manu-evaluation-harness"
	evaluationMetadataAgentVersion = "v1"
	evaluationMetadataModelID      = "model-not-executed"
	evaluationMetadataModelVersion = "v1"
	evaluationMetadataContextID    = "manu-context"

	evaluationMetadataRetrievalID      = "hybrid-retrieval"
	evaluationMetadataRetrievalVersion = "v1"
	evaluationMetadataRulesID          = "rules-disabled"
	evaluationMetadataRulesVersion     = "v1"

	// The evaluation metadata reader hashes the complete executable, while the
	// limit prevents an accidental unbounded read if the runtime path is
	// replaced by an unexpected file.
	evaluationMetadataMaxExecutableBytes int64 = 128 << 20
)

var ErrEvaluationVariantMetadata = errors.New("cli: evaluation variant metadata unavailable")

var evaluationVariantMetadataLimitations = []string{
	"generator_not_executed",
	"model_not_executed",
	"text_variant_unavailable",
	"impact_uses_question_fallback_without_typed_target",
	"tokens_and_cost_unavailable",
	"savings_only_defined_for_correct_supported_results",
}

// evaluationVariantMetadataInput contains only the fixed, non-secret inputs
// needed to identify a local variant run. OpenExecutable is a test seam; a
// nil value hashes the executable of the current process.
type evaluationVariantMetadataInput struct {
	Cases          evaluation.CaseSet
	Configuration  config.Config
	RunID          string
	StartedAt      time.Time
	FinishedAt     time.Time
	OpenExecutable func() (io.ReadCloser, error)
}

// buildEvaluationVariantMetadata builds the content-free metadata shared by
// every variant report in one local evaluation run.
func buildEvaluationVariantMetadata(input evaluationVariantMetadataInput) (evaluation.VariantReportMetadata, error) {
	var empty evaluation.VariantReportMetadata

	cases, err := input.Cases.Normalize()
	if err != nil || len(cases.Cases) == 0 {
		return empty, ErrEvaluationVariantMetadata
	}
	if err := input.Configuration.Retrieval.Validate(); err != nil {
		return empty, ErrEvaluationVariantMetadata
	}

	contextDigest, err := evaluationExecutableDigest(input.OpenExecutable)
	if err != nil {
		return empty, ErrEvaluationVariantMetadata
	}
	frontendComponents, err := evaluationFrontendComponents()
	if err != nil {
		return empty, ErrEvaluationVariantMetadata
	}
	tools, err := evaluationToolComponents(cases)
	if err != nil {
		return empty, ErrEvaluationVariantMetadata
	}
	retrieval := evaluationRetrievalConfiguration(input.Configuration.Retrieval)

	metadata := evaluation.VariantReportMetadata{
		RunID:      input.RunID,
		StartedAt:  input.StartedAt,
		FinishedAt: input.FinishedAt,
		Agent: evaluation.EvaluationComponent{
			ID:      evaluationMetadataAgentID,
			Version: evaluationMetadataAgentVersion,
		},
		Model: evaluation.EvaluationComponent{
			ID:      evaluationMetadataModelID,
			Version: evaluationMetadataModelVersion,
		},
		ContextServer: evaluation.EvaluationComponent{
			ID:      evaluationMetadataContextID,
			Version: serveRuntimeVersion,
			Digest:  contextDigest,
		},
		Frontends: frontendComponents,
		Rules: []evaluation.EvaluationComponent{{
			ID:      evaluationMetadataRulesID,
			Version: evaluationMetadataRulesVersion,
			Digest:  evaluationDisabledRulesDigest(),
		}},
		Retrieval:   retrieval,
		Tools:       tools,
		Limitations: append([]string(nil), evaluationVariantMetadataLimitations...),
	}
	normalized, err := metadata.Normalize()
	if err != nil {
		return empty, ErrEvaluationVariantMetadata
	}
	return normalized, nil
}

// BuildEvaluationVariantMetadata is the exported spelling for callers in
// adjacent internal packages. It keeps the executable reader injectable for
// deterministic tests while preserving the same safe default.
func BuildEvaluationVariantMetadata(input evaluationVariantMetadataInput) (evaluation.VariantReportMetadata, error) {
	return buildEvaluationVariantMetadata(input)
}

func evaluationExecutableDigest(openExecutable func() (io.ReadCloser, error)) (string, error) {
	if openExecutable == nil {
		openExecutable = openCurrentEvaluationExecutable
	}
	reader, err := openExecutable()
	if err != nil || reader == nil {
		return "", ErrEvaluationVariantMetadata
	}
	defer reader.Close()

	hasher := sha256.New()
	read := io.LimitReader(reader, evaluationMetadataMaxExecutableBytes+1)
	count, err := io.Copy(hasher, read)
	if err != nil || count <= 0 || count > evaluationMetadataMaxExecutableBytes {
		return "", ErrEvaluationVariantMetadata
	}
	digest := hasher.Sum(nil)
	return hex.EncodeToString(digest), nil
}

func openCurrentEvaluationExecutable() (io.ReadCloser, error) {
	path, err := os.Executable()
	if err != nil || path == "" {
		return nil, ErrEvaluationVariantMetadata
	}
	return os.Open(path)
}

func evaluationFrontendComponents() ([]evaluation.EvaluationComponent, error) {
	manifests, err := analyzer.FrontendManifests()
	if err != nil || len(manifests) == 0 {
		return nil, ErrEvaluationVariantMetadata
	}
	components := make([]evaluation.EvaluationComponent, 0, len(manifests))
	for _, manifest := range manifests {
		digest, err := fact.FrontendManifestDigest(manifest)
		if err != nil {
			return nil, ErrEvaluationVariantMetadata
		}
		components = append(components, evaluation.EvaluationComponent{
			ID:      manifest.ID,
			Version: manifest.Version,
			Digest:  digest,
		})
	}
	return components, nil
}

func evaluationToolComponents(cases evaluation.CaseSet) ([]evaluation.EvaluationComponent, error) {
	byKey := make(map[string]evaluation.EvaluationComponent)
	for _, item := range cases.Cases {
		for _, tool := range item.Tools {
			component := evaluation.EvaluationComponent{ID: tool.ID, Version: tool.Version}
			if err := component.Validate(); err != nil {
				return nil, ErrEvaluationVariantMetadata
			}
			key := tool.ID + "\x00" + tool.Version
			if prior, exists := byKey[key]; exists && prior != component {
				return nil, ErrEvaluationVariantMetadata
			}
			byKey[key] = component
		}
	}
	if len(byKey) == 0 {
		return nil, ErrEvaluationVariantMetadata
	}
	tools := make([]evaluation.EvaluationComponent, 0, len(byKey))
	for _, tool := range byKey {
		tools = append(tools, tool)
	}
	sort.SliceStable(tools, func(left, right int) bool {
		if tools[left].ID != tools[right].ID {
			return tools[left].ID < tools[right].ID
		}
		return tools[left].Version < tools[right].Version
	})
	return tools, nil
}

func evaluationRetrievalConfiguration(retrieval config.RetrievalConfig) evaluation.EvaluationConfiguration {
	return evaluation.EvaluationConfiguration{
		ID:      evaluationMetadataRetrievalID,
		Version: evaluationMetadataRetrievalVersion,
		Settings: map[string]string{
			"strategy":             "hybrid",
			"top_k":                strconv.Itoa(retrieval.TopK),
			"max_candidates":       strconv.Itoa(retrieval.MaxCandidates),
			"max_relation_hops":    strconv.Itoa(retrieval.MaxRelationHops),
			"max_relation_fan_out": strconv.Itoa(retrieval.MaxRelationFanOut),
			"max_package_units":    strconv.Itoa(retrieval.MaxPackageUnits),
			"max_package_bytes":    strconv.FormatInt(retrieval.MaxPackageBytes, 10),
			// EvaluationConfiguration rejects keys containing "token" because
			// they can otherwise be mistaken for credentials. Preserve this
			// retrieval limit with a neutral key and an explicit unit instead.
			"max_package_budget":      strconv.Itoa(retrieval.MaxPackageTokens),
			"max_package_budget_unit": "tokens",
			"exact_weight":            strconv.FormatFloat(retrieval.ExactWeight, 'g', -1, 64),
			"text_weight":             strconv.FormatFloat(retrieval.TextWeight, 'g', -1, 64),
			"vector_weight":           strconv.FormatFloat(retrieval.VectorWeight, 'g', -1, 64),
			"relation_weight":         strconv.FormatFloat(retrieval.RelationWeight, 'g', -1, 64),
		},
	}
}

type evaluationDisabledRulesPayload struct {
	Enabled bool     `json:"enabled"`
	Rules   []string `json:"rules"`
	Version string   `json:"version"`
}

func evaluationDisabledRulesDigest() string {
	payload, err := json.Marshal(evaluationDisabledRulesPayload{
		Enabled: false,
		Rules:   []string{},
		Version: evaluationMetadataRulesVersion,
	})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
