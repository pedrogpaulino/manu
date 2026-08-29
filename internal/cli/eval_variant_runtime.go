package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
)

var (
	// ErrVariantEvaluationValidation is the safe boundary for malformed local
	// evaluation inputs. It never contains the rejected root, case data, or
	// configuration values.
	ErrVariantEvaluationValidation = errors.New("cli: invalid variant evaluation")
	// ErrVariantEvaluationRuntime is the safe boundary for failures while
	// composing or executing the local evaluation runtime. Provider, database,
	// filesystem, and bundle details remain behind this sentinel.
	ErrVariantEvaluationRuntime = errors.New("cli: variant evaluation runtime unavailable")
)

const variantEvaluationKeyDomain = "manu:evaluation:context-key:v1\x00"

// runVariantEvaluationRuntime builds the factual fixture, seeds the real local
// projections, executes the direct and Manu context variants, and returns the
// content-free report pair. Database ownership stays inside this function;
// no source or response content crosses the report boundary.
func runVariantEvaluationRuntime(ctx context.Context, root string, cases evaluation.CaseSet, configuration config.Config) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error) {
	var emptyRaw evaluation.VariantRawReport
	var emptySummary evaluation.VariantSummaryReport
	if err := validateVariantEvaluationInputs(ctx, root, cases, configuration); err != nil {
		return emptyRaw, emptySummary, err
	}

	normalizedCases, err := cases.Normalize()
	if err != nil || normalizedCases.Version != evaluation.Version {
		return emptyRaw, emptySummary, ErrVariantEvaluationValidation
	}
	if err := ctx.Err(); err != nil {
		return emptyRaw, emptySummary, err
	}

	// Keep the pool lifecycle at the CLI boundary. The factual builder is
	// intentionally run before any seed write, while the pool is already owned
	// by this invocation as required by the documented runtime flow.
	pool, err := openServePool(ctx, configuration.Postgres)
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}
	defer pool.Close()

	startedAt := time.Now().UTC()
	corpus, err := evaluation.BuildFactualCorpus(ctx, root, normalizedCases)
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}
	if err := corpus.Validate(); err != nil {
		return emptyRaw, emptySummary, ErrVariantEvaluationRuntime
	}

	// The MCP wrapper enforces the canonical organization boundary. The cases
	// use a synthetic fixture organization, so compose the service from this
	// detached configuration copy instead of the caller's local organization.
	evaluationConfiguration, err := configForEvaluationCorpus(configuration, corpus)
	if err != nil {
		return emptyRaw, emptySummary, err
	}
	resolver, err := seedEvaluationFactualCorpus(ctx, corpus, pool)
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}

	key, err := variantEvaluationContextKey(corpus)
	if err != nil {
		return emptyRaw, emptySummary, ErrVariantEvaluationRuntime
	}
	service, err := composeMCPContextService(evaluationConfiguration, pool, bytes.NewReader(key))
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}

	direct, err := evaluation.NewDirectSourceExecutor(evaluation.DirectSourceExecutorConfig{
		Root:   root,
		Limits: evaluation.DefaultDirectSourceLimits(),
	})
	if err != nil {
		return emptyRaw, emptySummary, ErrVariantEvaluationRuntime
	}
	manu, err := evaluation.NewManuContextExecutorWithOrganization(
		service,
		resolver,
		evaluationConfiguration.Organization.ID,
		mcpResourceLimits(evaluationConfiguration),
	)
	if err != nil {
		return emptyRaw, emptySummary, ErrVariantEvaluationRuntime
	}

	registry, err := evaluation.NewVariantExecutorRegistry(
		evaluation.VariantExecutorRegistration{Kind: evaluation.VariantDirectSource, Executor: direct},
		evaluation.VariantExecutorRegistration{Kind: evaluation.VariantTextRetrieval, Executor: unavailableTextRetrievalExecutor()},
		evaluation.VariantExecutorRegistration{Kind: evaluation.VariantManuContext, Executor: manu},
	)
	if err != nil {
		return emptyRaw, emptySummary, ErrVariantEvaluationRuntime
	}
	runner, err := evaluation.NewVariantRunner(registry)
	if err != nil {
		return emptyRaw, emptySummary, ErrVariantEvaluationRuntime
	}
	execution, err := runner.Run(ctx, normalizedCases)
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}
	finishedAt := time.Now().UTC()
	metadata, err := buildEvaluationVariantMetadata(evaluationVariantMetadataInput{
		Cases:         normalizedCases,
		Configuration: evaluationConfiguration,
		RunID:         variantEvaluationRunID(key),
		StartedAt:     startedAt,
		FinishedAt:    finishedAt,
	})
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}
	raw, summary, err := evaluation.BuildVariantReports(normalizedCases, execution, metadata)
	if err != nil {
		return emptyRaw, emptySummary, variantEvaluationRuntimeError(ctx, err)
	}
	return raw, summary, nil
}

// validateVariantEvaluationInputs performs all cheap validation before the
// database is opened. The root remains execution-only and is never included
// in an error returned by this package.
func validateVariantEvaluationInputs(ctx context.Context, root string, cases evaluation.CaseSet, configuration config.Config) error {
	if ctx == nil {
		return ErrVariantEvaluationValidation
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == "" || strings.TrimSpace(root) != root || !utf8.ValidString(root) || !filepath.IsAbs(root) {
		return ErrVariantEvaluationValidation
	}
	if normalized, err := cases.Normalize(); err != nil || normalized.Version != evaluation.Version {
		return ErrVariantEvaluationValidation
	}
	if err := configuration.Validate(); err != nil {
		return ErrVariantEvaluationValidation
	}
	return nil
}

// configForEvaluationCorpus returns a detached configuration bound to the
// one organization represented by every factual snapshot. The caller's
// organization is not trusted for this local fixture runtime.
func configForEvaluationCorpus(configuration config.Config, corpus evaluation.FactualCorpus) (config.Config, error) {
	if err := corpus.Validate(); err != nil {
		return config.Config{}, ErrVariantEvaluationValidation
	}
	organizationID := ""
	organizationName := ""
	for _, snapshot := range corpus.Snapshots {
		candidateID := snapshot.Bundle.Manifest.Organization.ID
		candidateName := snapshot.Bundle.Manifest.Organization.Name
		if organizationID == "" {
			organizationID = candidateID
			organizationName = candidateName
			continue
		}
		if candidateID != organizationID || candidateName != organizationName {
			return config.Config{}, ErrVariantEvaluationValidation
		}
	}
	if organizationID == "" {
		return config.Config{}, ErrVariantEvaluationValidation
	}
	derived := configuration
	derived.Organization.ID = organizationID
	derived.Organization.Name = organizationName
	if err := derived.Validate(); err != nil {
		return config.Config{}, ErrVariantEvaluationValidation
	}
	return derived, nil
}

func variantEvaluationContextKey(corpus evaluation.FactualCorpus) ([]byte, error) {
	if err := corpus.Validate(); err != nil {
		return nil, ErrVariantEvaluationValidation
	}
	type identity struct {
		CorpusID       string `json:"corpus_id"`
		CorpusRevision string `json:"corpus_revision"`
		SourceID       string `json:"source_id"`
		SourceRevision string `json:"source_revision"`
		FactualDigest  string `json:"factual_digest"`
	}
	identities := make([]identity, 0, len(corpus.Snapshots))
	for _, snapshot := range corpus.Snapshots {
		identities = append(identities, identity{
			CorpusID: snapshot.CorpusID, CorpusRevision: snapshot.CorpusRevision,
			SourceID: snapshot.SourceID, SourceRevision: snapshot.SourceRevision,
			FactualDigest: snapshot.FactualDigest,
		})
	}
	encoded, err := json.Marshal(identities)
	if err != nil {
		return nil, ErrVariantEvaluationRuntime
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(variantEvaluationKeyDomain))
	_, _ = hash.Write(encoded)
	return hash.Sum(nil), nil
}

func variantEvaluationRunID(key []byte) string {
	digest := sha256.Sum256(key)
	return "evaluation-variant-" + hex.EncodeToString(digest[:])[:16]
}

func unavailableTextRetrievalExecutor() evaluation.VariantExecutor {
	return evaluation.VariantExecutorFunc(func(ctx context.Context, request evaluation.VariantExecutionRequest) (evaluation.VariantExecutionResult, error) {
		if ctx == nil {
			return evaluation.VariantExecutionResult{}, ErrVariantEvaluationValidation
		}
		if err := ctx.Err(); err != nil {
			return evaluation.VariantExecutionResult{}, err
		}
		if err := request.Validate(); err != nil || request.Variant.Kind != evaluation.VariantTextRetrieval {
			return evaluation.VariantExecutionResult{}, ErrVariantEvaluationValidation
		}
		return evaluation.VariantExecutionResult{
			Version:     evaluation.VariantExecutionVersion,
			Status:      evaluation.VariantStatusUnavailable,
			Conclusion:  evaluation.VariantConclusionNotEvaluated,
			Limitations: []string{"text_retrieval_not_executed"},
		}, nil
	})
}

func variantEvaluationRuntimeError(ctx context.Context, err error) error {
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrVariantEvaluationRuntime
}
