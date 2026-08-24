package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/persistence"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const (
	serveDatabaseConnectTimeout = 10 * time.Second
	serveEmbeddingNormalization = "none"
	serveRuntimeVersion         = "runtime-v1"
)

var (
	// ErrServeRuntimeConfiguration is a safe category for a runtime wiring
	// error. The underlying provider/driver detail is intentionally not sent
	// to the CLI or HTTP surface.
	ErrServeRuntimeConfiguration = errors.New("serve: invalid runtime configuration")
	// ErrServeRuntimeDatabase is a safe category for pool startup failures.
	ErrServeRuntimeDatabase = errors.New("serve: database unavailable")
	// ErrServeRuntimeNotConfigured identifies an incomplete composition.
	ErrServeRuntimeNotConfigured = errors.New("serve: runtime is not configured")
)

type serveRuntime struct {
	server *api.Server
	close  func()
	run    func(context.Context) error
}

type serveRuntimeBuilder func(context.Context, config.Config) (serveRuntime, error)

// executeServe keeps resource ownership at the command boundary. The pool is
// opened by buildServeRuntime and is closed after the HTTP lifecycle returns,
// including cancellation and startup failures.
func executeServe(ctx context.Context, configuration config.Config) error {
	return executeServeWithBuilder(ctx, configuration, buildServeRuntime)
}

func executeServeWithBuilder(ctx context.Context, configuration config.Config, build serveRuntimeBuilder) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if build == nil {
		return ErrServeRuntimeNotConfigured
	}
	runtime, err := build(ctx, configuration)
	if err != nil {
		return err
	}
	if runtime.close != nil {
		defer runtime.close()
	}
	if runtime.server == nil {
		return ErrServeRuntimeNotConfigured
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- runtime.server.Serve(runCtx) }()
	if runtime.run == nil {
		return <-serverDone
	}
	runnerDone := make(chan error, 1)
	go func() { runnerDone <- runtime.run(runCtx) }()
	var serverErr, runnerErr error
	select {
	case serverErr = <-serverDone:
		cancel()
		runnerErr = <-runnerDone
	case runnerErr = <-runnerDone:
		cancel()
		serverErr = <-serverDone
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runnerErr != nil && !errors.Is(runnerErr, context.Canceled) && !errors.Is(runnerErr, context.DeadlineExceeded) {
		return runnerErr
	}
	if serverErr != nil && !errors.Is(serverErr, context.Canceled) && !errors.Is(serverErr, context.DeadlineExceeded) {
		return serverErr
	}
	if runnerErr != nil {
		return runnerErr
	}
	return serverErr
}

func buildServeRuntime(ctx context.Context, configuration config.Config) (serveRuntime, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := configuration.Validate(); err != nil {
		return serveRuntime{}, ErrServeRuntimeConfiguration
	}
	pool, err := openServePool(ctx, configuration.Postgres)
	if err != nil {
		return serveRuntime{}, err
	}
	closePool := func() { pool.Close() }

	queryService, evidenceService, err := composeServeServices(configuration, pool)
	if err != nil {
		closePool()
		return serveRuntime{}, err
	}
	ingestionService, executor, err := composeServeIngestion(configuration, pool)
	if err != nil {
		closePool()
		return serveRuntime{}, err
	}
	readiness := serveReadiness(pool)
	server, err := api.NewServerWithListenerAndReadinessAndServices(
		configuration,
		nil,
		readiness,
		ingestionService,
		queryService,
		evidenceService,
	)
	if err != nil {
		closePool()
		return serveRuntime{}, err
	}
	return serveRuntime{server: server, close: closePool, run: executor.Run}, nil
}

func openServePool(ctx context.Context, postgres config.PostgresConfig) (*pgxpool.Pool, error) {
	connectionConfig, err := postgresConnectionConfig(postgres)
	if err != nil {
		return nil, ErrServeRuntimeConfiguration
	}
	poolConfig, err := pgxpool.ParseConfig(connectionConfig.ConnString())
	if err != nil {
		return nil, ErrServeRuntimeConfiguration
	}
	if postgres.MaxOpenConns > math.MaxInt32 {
		return nil, ErrServeRuntimeConfiguration
	}
	poolConfig.MaxConns = int32(postgres.MaxOpenConns)
	poolConfig.MinConns = 0
	poolConfig.MaxConnLifetime = postgres.ConnMaxLifetime
	poolConfig.MaxConnIdleTime = postgres.ConnMaxIdleTime
	connectCtx, cancel := context.WithTimeout(ctx, serveDatabaseConnectTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(connectCtx, poolConfig)
	if err != nil {
		if connectCtx.Err() != nil {
			return nil, connectCtx.Err()
		}
		return nil, ErrServeRuntimeDatabase
	}
	if err := pool.Ping(connectCtx); err != nil {
		pool.Close()
		if connectCtx.Err() != nil {
			return nil, connectCtx.Err()
		}
		return nil, ErrServeRuntimeDatabase
	}
	return pool, nil
}

func serveReadiness(pool *pgxpool.Pool) api.ReadinessChecker {
	return api.ReadinessFunc(func(ctx context.Context) error {
		if pool == nil {
			return api.ErrReadinessDependency
		}
		if ctx == nil {
			return context.Canceled
		}
		connection, err := pool.Acquire(ctx)
		if err != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return contextErr
			}
			return persistence.ErrMigrationDatabase
		}
		defer connection.Release()
		runner, err := persistence.NewEmbeddedPGXMigrationRunner(connection.Conn())
		if err != nil {
			return persistence.ErrInvalidMigrationCatalog
		}
		_, err = runner.Status(ctx)
		return err
	})
}

func composeServeServices(configuration config.Config, pool *pgxpool.Pool) (api.QueryService, api.EvidenceService, error) {
	if pool == nil {
		return nil, nil, ErrServeRuntimeNotConfigured
	}
	queryStore := persistence.NewPipelineQueryRepository(pool)
	evidenceResolver := persistence.NewQueryEvidenceUnitRepository(pool)
	evidenceReader := persistence.NewEvidenceInspectionRepository(pool)

	textProjection := retrieval.NewTextProjection(persistence.NewTextProjectionStore(pool))
	relationProjection := retrieval.NewRelationProjection(persistence.NewRelationProjectionStore(pool))
	factualRelationInputs := persistence.NewFactualRelationInputProvider(pool)
	var vectorProjection *retrieval.VectorProjection
	var embedder aigateway.Embedder
	var embeddingProfile aigateway.EmbeddingProfile
	var vectorProfile retrieval.EmbeddingProfile
	if configuration.Embedding.Enabled {
		var err error
		embedder, embeddingProfile, vectorProfile, err = composeEmbedding(configuration)
		if err != nil {
			return nil, nil, err
		}
		vectorProjection = retrieval.NewVectorProjection(persistence.NewEmbeddingProjectionStore(pool))
	}

	generator, generationProfile, err := composeGenerator(configuration)
	if err != nil {
		return nil, nil, err
	}
	fusion, err := composeFusion(configuration.Retrieval)
	if err != nil {
		return nil, nil, err
	}
	policy := composeEvidencePolicy(configuration.Policy)
	limits := query.PackageLimits{
		MaxUnits:      configuration.Retrieval.MaxPackageUnits,
		MaxCharacters: configuration.Retrieval.MaxPackageBytes,
		MaxBytes:      configuration.Retrieval.MaxPackageBytes,
		MaxTokens:     configuration.Retrieval.MaxPackageTokens,
	}
	limits, err = limits.Normalize()
	if err != nil {
		return nil, nil, ErrServeRuntimeConfiguration
	}
	orchestrator, err := query.NewQueryOrchestrator(queryStore, &query.HybridRetriever{
		Text:             textProjection,
		Vector:           vectorProjection,
		Embedder:         embedder,
		EmbeddingProfile: embeddingProfile,
		VectorProfile:    vectorProfile,
		UnitResolver:     evidenceResolver,
		Relations:        relationProjection,
		RelationInputs:   factualRelationInputs,
		Support:          query.ConservativeSupportAssessor{},
		Fusion:           fusion,
		Limit:            configuration.Retrieval.TopK,
	}, generator, query.OrchestratorConfig{
		PackageLimits:     limits,
		TransferPolicy:    &policy,
		GenerationProfile: generationProfile,
		ActiveScope:       queryStore,
		GenerationTimeout: configuration.Generation.Timeout,
	})
	if err != nil {
		return nil, nil, ErrServeRuntimeConfiguration
	}
	return orchestrator, evidenceReader, nil
}

func composeFusion(configuration config.RetrievalConfig) (retrieval.FusionConfiguration, error) {
	return (retrieval.FusionConfiguration{
		Version:           serveRuntimeVersion,
		ExactWeight:       configuration.ExactWeight,
		TextualWeight:     configuration.TextWeight,
		VectorWeight:      configuration.VectorWeight,
		RelationWeight:    configuration.RelationWeight,
		MaxCandidates:     configuration.MaxCandidates,
		RelationFanOut:    configuration.MaxRelationFanOut,
		RelationMaxHops:   configuration.MaxRelationHops,
		RelationDirection: retrieval.RelationDirectionBoth,
	}).Normalize()
}

func composeEvidencePolicy(configuration config.PolicyConfig) evidence.Policy {
	policy := evidence.DefaultPolicy()
	policy.Installation.Persist = evidence.Decision(configuration.Persist)
	policy.Installation.ExternalTransfer = evidence.Decision(configuration.ExternalTransfer)
	return policy
}

func composeEmbedding(configuration config.Config) (aigateway.Embedder, aigateway.EmbeddingProfile, retrieval.EmbeddingProfile, error) {
	settings := configuration.Embedding
	provider, ok := gatewayProvider(settings.Provider)
	if !ok || provider == aigateway.ProviderUnknown {
		return nil, aigateway.EmbeddingProfile{}, retrieval.EmbeddingProfile{}, ErrServeRuntimeConfiguration
	}
	profile := aigateway.EmbeddingProfile{
		Provider:     provider,
		Model:        settings.Model,
		Version:      aigateway.EmbeddingProfileVersion,
		Dimension:    settings.Dimension,
		Normalize:    false,
		MaxBatchSize: settings.MaxBatchSize,
	}
	if err := profile.Validate(); err != nil {
		return nil, aigateway.EmbeddingProfile{}, retrieval.EmbeddingProfile{}, ErrServeRuntimeConfiguration
	}
	retrievalProfile, err := runtimeEmbeddingProfile(configuration.Organization.ID, profile)
	if err != nil {
		return nil, aigateway.EmbeddingProfile{}, retrieval.EmbeddingProfile{}, ErrServeRuntimeConfiguration
	}
	backend, err := composeEmbeddingBackend(settings, profile)
	if err != nil {
		return nil, aigateway.EmbeddingProfile{}, retrieval.EmbeddingProfile{}, err
	}
	orchestrated, err := aigateway.NewEmbeddingOrchestrator(backend, aigateway.OrchestrationConfig{
		Budget:         gatewayBudget(settings.Budget),
		MaxConcurrency: 1,
		Retry:          aigateway.RetryPolicy{MaxAttempts: 1},
	})
	if err != nil {
		return nil, aigateway.EmbeddingProfile{}, retrieval.EmbeddingProfile{}, ErrServeRuntimeConfiguration
	}
	return orchestrated, profile, retrievalProfile, nil
}

func composeEmbeddingBackend(settings config.EmbeddingConfig, profile aigateway.EmbeddingProfile) (aigateway.Embedder, error) {
	httpClient := &http.Client{Timeout: settings.Timeout}
	switch profile.Provider {
	case aigateway.ProviderSimulated:
		backend, err := aigateway.NewSimulatedEmbedder(aigateway.SimulatedEmbedderConfig{Profile: profile})
		if err != nil {
			return nil, ErrServeRuntimeConfiguration
		}
		return backend, nil
	case aigateway.ProviderOpenAI:
		backend, err := aigateway.NewOpenAIAdapter(aigateway.OpenAIConfig{
			BaseURL: settings.BaseURL, APIKey: settings.APIKey, HTTPClient: httpClient,
		})
		if err != nil {
			return nil, ErrServeRuntimeConfiguration
		}
		return backend, nil
	case aigateway.ProviderOpenRouter:
		backend, err := aigateway.NewOpenAICompatibleAdapter(aigateway.OpenAICompatibleConfig{
			Dialect: aigateway.OpenRouterDialect, BaseURL: settings.BaseURL, APIKey: settings.APIKey,
			HTTPClient: httpClient, StructuredOutputs: true,
		})
		if err != nil {
			return nil, ErrServeRuntimeConfiguration
		}
		return backend, nil
	case aigateway.ProviderOpenAICompatible:
		// The current compatible adapter has only the explicitly supported
		// OpenRouter dialect. Do not reinterpret this provider as OpenRouter.
		return nil, ErrServeRuntimeConfiguration
	default:
		return nil, ErrServeRuntimeConfiguration
	}
}

func composeGenerator(configuration config.Config) (aigateway.Generator, aigateway.GenerationProfile, error) {
	settings := configuration.Generation
	if !settings.Enabled {
		return nil, aigateway.GenerationProfile{}, nil
	}
	provider, ok := gatewayProvider(settings.Provider)
	if !ok || provider == aigateway.ProviderUnknown {
		return nil, aigateway.GenerationProfile{}, ErrServeRuntimeConfiguration
	}
	protocol, ok := gatewayProtocol(settings.Protocol)
	if !ok {
		return nil, aigateway.GenerationProfile{}, ErrServeRuntimeConfiguration
	}
	maxOutputBytes := settings.MaxOutputTokens * 4
	if maxOutputBytes < settings.MaxOutputTokens || maxOutputBytes <= 0 {
		return nil, aigateway.GenerationProfile{}, ErrServeRuntimeConfiguration
	}
	profile := aigateway.GenerationProfile{
		Provider:       provider,
		Model:          settings.Model,
		Version:        aigateway.GenerationProfileVersion,
		Protocol:       protocol,
		MaxOutputBytes: maxOutputBytes,
	}
	if err := profile.Validate(); err != nil {
		return nil, aigateway.GenerationProfile{}, ErrServeRuntimeConfiguration
	}
	backend, err := composeGenerationBackend(settings, profile)
	if err != nil {
		return nil, aigateway.GenerationProfile{}, err
	}
	orchestrated, err := aigateway.NewGenerationOrchestrator(backend, aigateway.OrchestrationConfig{
		Budget:         gatewayBudget(settings.Budget),
		MaxConcurrency: configuration.Limits.MaxConcurrentQueries,
		Retry:          aigateway.RetryPolicy{MaxAttempts: 1},
	})
	if err != nil {
		return nil, aigateway.GenerationProfile{}, ErrServeRuntimeConfiguration
	}
	return orchestrated, profile, nil
}

func composeGenerationBackend(settings config.GenerationConfig, profile aigateway.GenerationProfile) (aigateway.Generator, error) {
	httpClient := &http.Client{Timeout: settings.Timeout}
	switch profile.Provider {
	case aigateway.ProviderSimulated:
		return &runtimeSimulatedGenerator{profile: profile}, nil
	case aigateway.ProviderOpenAI:
		if profile.Protocol != aigateway.ProtocolResponses {
			return nil, ErrServeRuntimeConfiguration
		}
		backend, err := aigateway.NewOpenAIAdapter(aigateway.OpenAIConfig{
			BaseURL: settings.BaseURL, APIKey: settings.APIKey, HTTPClient: httpClient,
			MaxOutputTokens: settings.MaxOutputTokens,
		})
		if err != nil {
			return nil, ErrServeRuntimeConfiguration
		}
		return backend, nil
	case aigateway.ProviderOpenRouter:
		if profile.Protocol != aigateway.ProtocolChatCompletions {
			return nil, ErrServeRuntimeConfiguration
		}
		backend, err := aigateway.NewOpenAICompatibleAdapter(aigateway.OpenAICompatibleConfig{
			Dialect: aigateway.OpenRouterDialect, BaseURL: settings.BaseURL, APIKey: settings.APIKey,
			HTTPClient: httpClient, MaxOutputTokens: settings.MaxOutputTokens, StructuredOutputs: true,
		})
		if err != nil {
			return nil, ErrServeRuntimeConfiguration
		}
		return backend, nil
	case aigateway.ProviderOpenAICompatible:
		return nil, ErrServeRuntimeConfiguration
	default:
		return nil, ErrServeRuntimeConfiguration
	}
}

// runtimeSimulatedGenerator binds every deterministic fixture response to
// the package selected for the current request. This keeps an explicitly
// enabled simulator useful in the composed server without manufacturing
// citations or consulting any external provider.
type runtimeSimulatedGenerator struct {
	profile aigateway.GenerationProfile
}

func (g *runtimeSimulatedGenerator) Generate(ctx context.Context, request aigateway.GenerationRequest) (aigateway.GenerationResult, error) {
	if g == nil {
		return aigateway.GenerationResult{}, ErrServeRuntimeNotConfigured
	}
	ids := make([]string, 0, len(request.Package.Evidence))
	for _, reference := range request.Package.Evidence {
		ids = append(ids, reference.ID)
	}
	backend, err := aigateway.NewSimulatedGenerator(aigateway.SimulatedGeneratorConfig{
		Profile: g.profile,
		Fixture: aigateway.GenerationEnvelope{
			Version:     aigateway.GenerationEnvelopeVersion,
			Text:        "Resposta determinística baseada nas evidências autorizadas.",
			EvidenceIDs: ids,
		},
	})
	if err != nil {
		return aigateway.GenerationResult{}, ErrServeRuntimeConfiguration
	}
	return backend.Generate(ctx, request)
}

func runtimeEmbeddingProfile(organization string, profile aigateway.EmbeddingProfile) (retrieval.EmbeddingProfile, error) {
	organizationID := identity.CanonicalUUID("organization", organization)
	normalization := serveEmbeddingNormalization
	configuration := map[string]any{
		"dimension":     profile.Dimension,
		"model":         profile.Model,
		"normalization": normalization,
		"provider":      string(profile.Provider),
		"version":       profile.Version,
	}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	digest := sha256.Sum256(encoded)
	profileID := identity.CanonicalUUID("embedding-profile", organizationID, profile.Model, string(profile.Provider), profile.Version, fmt.Sprint(profile.Dimension), normalization)
	result := retrieval.EmbeddingProfile{
		ID:                   profileID,
		OrganizationID:       organizationID,
		Provider:             string(profile.Provider),
		Model:                profile.Model,
		Dimension:            profile.Dimension,
		Normalization:        normalization,
		ConfigurationVersion: profile.Version,
		ConfigurationDigest:  fmt.Sprintf("%x", digest[:]),
		Configuration:        encoded,
	}
	if err := result.Validate(); err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	return result, nil
}

func gatewayProvider(provider config.Provider) (aigateway.Provider, bool) {
	switch provider {
	case config.ProviderSimulated:
		return aigateway.ProviderSimulated, true
	case config.ProviderOpenAI:
		return aigateway.ProviderOpenAI, true
	case config.ProviderOpenRouter:
		return aigateway.ProviderOpenRouter, true
	case config.ProviderOpenAICompatible:
		return aigateway.ProviderOpenAICompatible, true
	default:
		return aigateway.ProviderUnknown, false
	}
}

func gatewayProtocol(protocol config.Protocol) (aigateway.Protocol, bool) {
	switch protocol {
	case config.ProtocolResponses:
		return aigateway.ProtocolResponses, true
	case config.ProtocolChatCompletions:
		return aigateway.ProtocolChatCompletions, true
	default:
		return aigateway.ProtocolUnknown, false
	}
}

func gatewayBudget(budget config.BudgetConfig) aigateway.OrchestrationBudget {
	return aigateway.OrchestrationBudget{
		MaxRequests:     budget.MaxRequests,
		MaxInputTokens:  budget.MaxInputTokens,
		MaxOutputTokens: budget.MaxOutputTokens,
		MaxCostUSD:      budget.MaxCostUSD,
	}
}

var _ aigateway.Embedder = (*aigateway.EmbeddingOrchestrator)(nil)
var _ aigateway.Generator = (*aigateway.GenerationOrchestrator)(nil)
var _ api.QueryService = (*query.QueryOrchestrator)(nil)
var _ api.EvidenceService = (*persistence.EvidenceInspectionRepository)(nil)
