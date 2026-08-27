package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/mcpadapter"
	"github.com/pedrogpaulino/manu/internal/persistence"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const mcpContinuationKeyBytes = 32

var (
	// ErrMCPRuntimeConfiguration is the safe composition failure returned by
	// the local MCP runtime. Driver and provider details stay behind the CLI
	// diagnostic boundary.
	ErrMCPRuntimeConfiguration = errors.New("mcp: invalid runtime configuration")
	// ErrMCPOrganizationScope identifies a request outside the configured local
	// organization without revealing whether the requested resource exists.
	ErrMCPOrganizationScope = errors.New("mcp: request scope is not authorized")
	// ErrMCPRuntimeAudit is the opaque failure returned when the local MCP
	// runtime cannot validate or write its content-free audit record.
	ErrMCPRuntimeAudit = errors.New("mcp: audit unavailable")
)

// runMCPRuntime owns the PostgreSQL pool and the context-service composition
// for one local stdio session. The continuation key is process-local and is
// never loaded from configuration or persisted.
func runMCPRuntime(ctx context.Context, configuration config.Config, auditWriter io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := configuration.Validate(); err != nil {
		return ErrMCPRuntimeConfiguration
	}
	if err := validateMCPResourceLimits(configuration); err != nil {
		return err
	}
	auditSink, err := newMCPJSONLAuditSink(auditWriter)
	if err != nil {
		return err
	}
	pool, err := openServePool(ctx, configuration.Postgres)
	if err != nil {
		return err
	}
	defer pool.Close()

	service, err := composeMCPContextService(configuration, pool, rand.Reader)
	if err != nil {
		return err
	}
	activeSnapshotResolver := &mcpActiveSnapshotResolver{
		delegate:             persistence.NewPipelineQueryRepository(pool),
		organizationExternal: configuration.Organization.ID,
		organizationID:       identity.CanonicalUUID("organization", configuration.Organization.ID),
	}
	options, err := composeMCPContextServerOptions(configuration, activeSnapshotResolver, auditSink)
	if err != nil {
		return err
	}
	return mcpadapter.RunStdioWithContextServiceWithOptions(ctx, service, options)
}

// composeMCPContextService builds the production ContextService without a
// Generator. keyReader is injected so key generation remains deterministic in
// tests while production uses crypto/rand.Reader.
func composeMCPContextService(configuration config.Config, pool *pgxpool.Pool, keyReader io.Reader) (query.ContextService, error) {
	if err := configuration.Validate(); err != nil || pool == nil || nilMCPReader(keyReader) {
		return nil, ErrMCPRuntimeConfiguration
	}

	textProjection := retrieval.NewTextProjection(persistence.NewTextProjectionStore(pool))
	relationProjection := retrieval.NewRelationProjection(persistence.NewRelationProjectionStore(pool))
	factualRelationInputs := persistence.NewFactualRelationInputProvider(pool)
	evidenceResolver := persistence.NewQueryEvidenceUnitRepository(pool)

	var vectorProjection *retrieval.VectorProjection
	var embedder aigateway.Embedder
	var embeddingProfile aigateway.EmbeddingProfile
	var vectorProfile retrieval.EmbeddingProfile
	if configuration.Embedding.Enabled {
		var err error
		embedder, embeddingProfile, vectorProfile, err = composeEmbedding(configuration)
		if err != nil {
			return nil, ErrMCPRuntimeConfiguration
		}
		vectorProjection = retrieval.NewVectorProjection(persistence.NewEmbeddingProjectionStore(pool))
	}

	fusion, err := composeFusion(configuration.Retrieval)
	if err != nil {
		return nil, ErrMCPRuntimeConfiguration
	}
	policy := composeEvidencePolicy(configuration.Policy)
	codec, err := newMCPContinuationCodec(keyReader)
	if err != nil {
		return nil, ErrMCPRuntimeConfiguration
	}
	retriever := &query.HybridRetriever{
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
	}
	service, err := query.NewProductionContextService(
		persistence.NewRepository(pool),
		retriever,
		codec,
		query.ContextServiceConfig{
			TransferPolicy:   &policy,
			Utility:          query.DefaultContextUtilityConfiguration(),
			Estimator:        query.DefaultContextTokenEstimatorConfiguration(),
			EstimationLimits: query.ContextTokenEstimationLimits{},
			RetrievalLimit:   configuration.Retrieval.TopK,
		},
	)
	if err != nil {
		return nil, ErrMCPRuntimeConfiguration
	}

	organizationID := identity.CanonicalUUID("organization", configuration.Organization.ID)
	return &mcpOrganizationScopedContextService{
		service:        service,
		organizationID: organizationID,
	}, nil
}

func composeMCPContextServerOptions(configuration config.Config, resolver mcpadapter.ActiveSnapshotResolver, auditSink mcpadapter.ContextAuditSink) (mcpadapter.ContextServerOptions, error) {
	options := mcpadapter.ContextServerOptions{
		ActiveSnapshotResolver: resolver,
		ResourceLimits:         mcpResourceLimits(configuration),
		AuditSink:              auditSink,
	}
	if nilMCPAuditSink(auditSink) || options.ResourceLimits.Validate() != nil || options.Validate() != nil {
		return mcpadapter.ContextServerOptions{}, ErrMCPRuntimeConfiguration
	}
	return options, nil
}

func mcpResourceLimits(configuration config.Config) query.ContextLimits {
	return query.ContextLimits{
		MaxTokens:     configuration.Retrieval.MaxPackageTokens,
		MaxItems:      configuration.Retrieval.MaxPackageUnits,
		MaxCharacters: configuration.Retrieval.MaxPackageBytes,
		MaxBytes:      configuration.Retrieval.MaxPackageBytes,
	}
}

func validateMCPResourceLimits(configuration config.Config) error {
	if err := mcpResourceLimits(configuration).Validate(); err != nil {
		return ErrMCPRuntimeConfiguration
	}
	return nil
}

// mcpJSONLAuditSink writes one validated, content-free audit record per JSON
// line. The mutex covers the complete writer call so concurrent MCP requests
// cannot interleave lines on the diagnostic stream.
type mcpJSONLAuditSink struct {
	mu     sync.Mutex
	writer io.Writer
}

var _ mcpadapter.ContextAuditSink = (*mcpJSONLAuditSink)(nil)

func newMCPJSONLAuditSink(writer io.Writer) (mcpadapter.ContextAuditSink, error) {
	if nilMCPWriter(writer) {
		return nil, ErrMCPRuntimeAudit
	}
	return &mcpJSONLAuditSink{writer: writer}, nil
}

func (s *mcpJSONLAuditSink) RecordContextAudit(_ context.Context, record mcpadapter.ContextAuditRecord) error {
	if s == nil {
		return ErrMCPRuntimeAudit
	}
	if err := record.Validate(); err != nil {
		return ErrMCPRuntimeAudit
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return ErrMCPRuntimeAudit
	}
	line := make([]byte, len(encoded)+1)
	copy(line, encoded)
	line[len(encoded)] = '\n'

	s.mu.Lock()
	defer s.mu.Unlock()
	if nilMCPWriter(s.writer) {
		return ErrMCPRuntimeAudit
	}
	written, err := s.writer.Write(line)
	if err != nil || written != len(line) {
		return ErrMCPRuntimeAudit
	}
	return nil
}

// newMCPContinuationCodec reads exactly one process-local key. It does not
// retain the reader or expose the generated bytes to callers.
func newMCPContinuationCodec(keyReader io.Reader) (*query.ContextContinuationCodec, error) {
	if nilMCPReader(keyReader) {
		return nil, ErrMCPRuntimeConfiguration
	}
	key := make([]byte, mcpContinuationKeyBytes)
	if _, err := io.ReadFull(keyReader, key); err != nil {
		return nil, ErrMCPRuntimeConfiguration
	}
	codec, err := query.NewContextContinuationCodec(key)
	if err != nil {
		return nil, ErrMCPRuntimeConfiguration
	}
	return codec, nil
}

func nilMCPReader(reader io.Reader) bool {
	if reader == nil {
		return true
	}
	value := reflect.ValueOf(reader)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilMCPWriter(writer io.Writer) bool {
	if writer == nil {
		return true
	}
	value := reflect.ValueOf(writer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func nilMCPAuditSink(sink mcpadapter.ContextAuditSink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// mcpOrganizationScopedContextService keeps local unauthenticated MCP bound
// to the one configured organization. Source and snapshot are still resolved
// by the production ContextService for each request.
type mcpOrganizationScopedContextService struct {
	service        query.ContextService
	organizationID string
}

var _ query.ContextService = (*mcpOrganizationScopedContextService)(nil)

func (s *mcpOrganizationScopedContextService) BuildContext(ctx context.Context, request query.ContextRequest) (query.ContextPackage, error) {
	if s == nil || nilMCPContextService(s.service) || s.organizationID == "" {
		return query.ContextPackage{}, ErrMCPRuntimeConfiguration
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return query.ContextPackage{}, err
		}
	}
	if !strings.EqualFold(request.Scope.OrganizationID, s.organizationID) {
		return query.ContextPackage{}, ErrMCPOrganizationScope
	}
	return s.service.BuildContext(ctx, request)
}

func nilMCPContextService(service query.ContextService) bool {
	if service == nil {
		return true
	}
	value := reflect.ValueOf(service)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// mcpActiveSnapshotResolver adapts the query application's organization-aware
// active-scope port to the MCP resource indication. It accepts a historical
// canonical scope but never replaces that scope in the returned package.
type mcpActiveSnapshotResolver struct {
	delegate             query.ActiveScopeResolver
	organizationExternal string
	organizationID       string
}

var _ mcpadapter.ActiveSnapshotResolver = (*mcpActiveSnapshotResolver)(nil)

func (r *mcpActiveSnapshotResolver) ResolveActiveSnapshot(ctx context.Context, historical query.Scope) (query.Scope, error) {
	if r == nil || nilMCPActiveScopeResolver(r.delegate) || r.organizationExternal == "" || r.organizationID == "" {
		return query.Scope{}, ErrMCPRuntimeConfiguration
	}
	if !strings.EqualFold(historical.OrganizationID, r.organizationID) {
		return query.Scope{}, ErrMCPOrganizationScope
	}
	if err := historical.Validate(); err != nil {
		return query.Scope{}, ErrMCPRuntimeConfiguration
	}
	latest, err := r.delegate.ResolveActiveScope(ctx, r.organizationExternal, historical.SourceID)
	if err != nil {
		if ctx != nil {
			if contextErr := ctx.Err(); contextErr != nil {
				return query.Scope{}, contextErr
			}
		}
		return query.Scope{}, ErrMCPRuntimeConfiguration
	}
	if err := latest.Validate(); err != nil ||
		!strings.EqualFold(latest.OrganizationID, r.organizationID) ||
		!strings.EqualFold(latest.SourceID, historical.SourceID) {
		return query.Scope{}, ErrMCPRuntimeConfiguration
	}
	return latest, nil
}

func nilMCPActiveScopeResolver(resolver query.ActiveScopeResolver) bool {
	if resolver == nil {
		return true
	}
	value := reflect.ValueOf(resolver)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
