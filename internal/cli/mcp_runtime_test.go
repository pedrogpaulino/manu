package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
)

func TestComposeMCPContextServiceBuildsProductionServiceWithoutGenerator(t *testing.T) {
	configuration := config.Default()
	key := bytes.Repeat([]byte{0x42}, mcpContinuationKeyBytes)

	service, err := composeMCPContextService(configuration, &pgxpool.Pool{}, bytes.NewReader(key))
	if err != nil {
		t.Fatalf("composeMCPContextService() error = %v", err)
	}
	scoped, ok := service.(*mcpOrganizationScopedContextService)
	if !ok {
		t.Fatalf("MCP service type = %T, want *mcpOrganizationScopedContextService", service)
	}
	if _, ok := scoped.service.(*query.ProductionContextService); !ok {
		t.Fatalf("scoped service type = %T, want *query.ProductionContextService", scoped.service)
	}
	wantOrganizationID := identity.CanonicalUUID("organization", configuration.Organization.ID)
	if scoped.organizationID != wantOrganizationID {
		t.Fatalf("configured organization ID = %q, want %q", scoped.organizationID, wantOrganizationID)
	}
}

func TestComposeMCPContextServiceRejectsMissingRuntimeInputs(t *testing.T) {
	configuration := config.Default()
	key := bytes.Repeat([]byte{0x42}, mcpContinuationKeyBytes)
	tests := []struct {
		name string
		pool *pgxpool.Pool
		read io.Reader
	}{
		{name: "nil pool", read: bytes.NewReader(key)},
		{name: "nil key reader", pool: &pgxpool.Pool{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := composeMCPContextService(configuration, test.pool, test.read)
			if !errors.Is(err, ErrMCPRuntimeConfiguration) {
				t.Fatalf("composeMCPContextService() error = %v, want %v", err, ErrMCPRuntimeConfiguration)
			}
		})
	}
}

func TestComposeMCPContextServerOptionsDerivesConfiguredLimits(t *testing.T) {
	configuration := config.Default()
	resolver := &mcpRuntimeTestActiveScopeResolver{}
	options, err := composeMCPContextServerOptions(configuration, resolver)
	if err != nil {
		t.Fatalf("composeMCPContextServerOptions() error = %v", err)
	}
	want := query.ContextLimits{
		MaxTokens:     configuration.Retrieval.MaxPackageTokens,
		MaxItems:      configuration.Retrieval.MaxPackageUnits,
		MaxCharacters: configuration.Retrieval.MaxPackageBytes,
		MaxBytes:      configuration.Retrieval.MaxPackageBytes,
	}
	if options.ResourceLimits != want {
		t.Fatalf("resource limits = %#v, want %#v", options.ResourceLimits, want)
	}
	if options.ActiveSnapshotResolver != resolver {
		t.Fatal("active snapshot resolver was not retained in server options")
	}
}

func TestComposeMCPContextServerOptionsRejectsInvalidResourceLimits(t *testing.T) {
	configuration := config.Default()
	configuration.Retrieval.MaxPackageBytes = 16<<20 + 1
	_, err := composeMCPContextServerOptions(configuration, &mcpRuntimeTestActiveScopeResolver{})
	if !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("composeMCPContextServerOptions() error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
}

func TestNewMCPContinuationCodecReadsOneProcessLocalKey(t *testing.T) {
	seed := bytes.Repeat([]byte{0x24}, mcpContinuationKeyBytes)
	reader := bytes.NewReader(append(append([]byte(nil), seed...), 0x99))
	got, err := newMCPContinuationCodec(reader)
	if err != nil {
		t.Fatalf("newMCPContinuationCodec() error = %v", err)
	}
	if reader.Len() != 1 {
		t.Fatalf("key reader remaining bytes = %d, want 1", reader.Len())
	}
	want, err := query.NewContextContinuationCodec(seed)
	if err != nil {
		t.Fatalf("NewContextContinuationCodec() error = %v", err)
	}
	binding := query.ContextContinuationBinding{
		Scope: query.Scope{
			OrganizationID: identity.CanonicalUUID("organization", "local"),
			SourceID:       identity.CanonicalUUID("source", "source-1"),
			SnapshotID:     identity.CanonicalUUID("snapshot", "snapshot-1"),
		},
		SnapshotRevision: "revision-1",
		IntentDigest:     "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		PolicyDigest:     "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		AlgorithmVersion: query.ContextServiceAssemblyVersion,
		Ordering:         query.ContextRetrievalPlanOrdering,
	}
	sequence := []string{"item-1", "item-2"}
	gotContinuation, err := got.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("generated codec Issue() error = %v", err)
	}
	wantContinuation, err := want.Issue(context.Background(), binding, sequence, 1)
	if err != nil {
		t.Fatalf("expected codec Issue() error = %v", err)
	}
	if !reflect.DeepEqual(gotContinuation, wantContinuation) {
		t.Fatalf("continuation = %#v, want %#v", gotContinuation, wantContinuation)
	}
}

func TestNewMCPContinuationCodecHidesReaderFailure(t *testing.T) {
	_, err := newMCPContinuationCodec(mcpFailingReader{})
	if !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("newMCPContinuationCodec() error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
	if err.Error() == "reader secret" {
		t.Fatal("reader failure escaped runtime configuration boundary")
	}
}

func TestMCPOrganizationScopeRejectsForeignOrganizationBeforeService(t *testing.T) {
	delegate := &mcpRuntimeTestContextService{}
	service := &mcpOrganizationScopedContextService{
		service:        delegate,
		organizationID: identity.CanonicalUUID("organization", "local"),
	}
	foreign := query.ContextRequest{Scope: query.Scope{OrganizationID: identity.CanonicalUUID("organization", "other")}}
	if _, err := service.BuildContext(context.Background(), foreign); !errors.Is(err, ErrMCPOrganizationScope) {
		t.Fatalf("foreign scope error = %v, want %v", err, ErrMCPOrganizationScope)
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate calls after foreign scope = %d, want 0", delegate.calls)
	}

	local := query.ContextRequest{Scope: query.Scope{OrganizationID: service.organizationID}}
	if _, err := service.BuildContext(context.Background(), local); err != nil {
		t.Fatalf("local scope error = %v", err)
	}
	if delegate.calls != 1 {
		t.Fatalf("delegate calls after local scope = %d, want 1", delegate.calls)
	}
}

func TestMCPOrganizationScopeRejectsTypedNilDelegate(t *testing.T) {
	var delegate *mcpRuntimeTestContextService
	service := &mcpOrganizationScopedContextService{
		service:        delegate,
		organizationID: identity.CanonicalUUID("organization", "local"),
	}
	_, err := service.BuildContext(context.Background(), query.ContextRequest{})
	if !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("typed nil delegate error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
}

func TestMCPActiveSnapshotResolverPassesConfiguredExternalOrganizationAndSource(t *testing.T) {
	configuredOrganization := identity.CanonicalUUID("organization", "local")
	sourceID := identity.CanonicalUUID("source", "payments")
	historical := query.Scope{
		OrganizationID: configuredOrganization,
		SourceID:       sourceID,
		SnapshotID:     identity.CanonicalUUID("snapshot", "old"),
	}
	delegate := &mcpRuntimeTestActiveScopeResolver{
		result: query.Scope{
			OrganizationID: configuredOrganization,
			SourceID:       sourceID,
			SnapshotID:     identity.CanonicalUUID("snapshot", "latest"),
		},
	}
	resolver := &mcpActiveSnapshotResolver{
		delegate:             delegate,
		organizationExternal: "local",
		organizationID:       configuredOrganization,
	}
	got, err := resolver.ResolveActiveSnapshot(context.Background(), historical)
	if err != nil {
		t.Fatalf("ResolveActiveSnapshot() error = %v", err)
	}
	if !reflect.DeepEqual(got, delegate.result) {
		t.Fatalf("active scope = %#v, want %#v", got, delegate.result)
	}
	if delegate.calls != 1 || delegate.organizationExternal != "local" || delegate.sourceID != sourceID {
		t.Fatalf("delegate call = %#v, want one call with configured external org/source", delegate)
	}
}

func TestMCPActiveSnapshotResolverRejectsForeignOrganizationBeforeDelegate(t *testing.T) {
	delegate := &mcpRuntimeTestActiveScopeResolver{}
	resolver := &mcpActiveSnapshotResolver{
		delegate:             delegate,
		organizationExternal: "local",
		organizationID:       identity.CanonicalUUID("organization", "local"),
	}
	historical := query.Scope{
		OrganizationID: identity.CanonicalUUID("organization", "other"),
		SourceID:       identity.CanonicalUUID("source", "payments"),
		SnapshotID:     identity.CanonicalUUID("snapshot", "old"),
	}
	_, err := resolver.ResolveActiveSnapshot(context.Background(), historical)
	if !errors.Is(err, ErrMCPOrganizationScope) {
		t.Fatalf("foreign scope error = %v, want %v", err, ErrMCPOrganizationScope)
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate calls after foreign scope = %d, want 0", delegate.calls)
	}
}

func TestMCPActiveSnapshotResolverRejectsMismatchedDelegateResult(t *testing.T) {
	configuredOrganization := identity.CanonicalUUID("organization", "local")
	sourceID := identity.CanonicalUUID("source", "payments")
	resolver := &mcpActiveSnapshotResolver{
		delegate: &mcpRuntimeTestActiveScopeResolver{result: query.Scope{
			OrganizationID: identity.CanonicalUUID("organization", "other"),
			SourceID:       sourceID,
			SnapshotID:     identity.CanonicalUUID("snapshot", "latest"),
		}},
		organizationExternal: "local",
		organizationID:       configuredOrganization,
	}
	historical := query.Scope{
		OrganizationID: configuredOrganization,
		SourceID:       sourceID,
		SnapshotID:     identity.CanonicalUUID("snapshot", "old"),
	}
	_, err := resolver.ResolveActiveSnapshot(context.Background(), historical)
	if !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("mismatched result error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
}

func TestMCPActiveSnapshotResolverRejectsTypedNilResolver(t *testing.T) {
	var delegate *mcpRuntimeTestActiveScopeResolver
	resolver := &mcpActiveSnapshotResolver{
		delegate:             delegate,
		organizationExternal: "local",
		organizationID:       identity.CanonicalUUID("organization", "local"),
	}
	_, err := resolver.ResolveActiveSnapshot(context.Background(), query.Scope{})
	if !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("typed nil resolver error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
}

type mcpFailingReader struct{}

func (mcpFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("reader secret")
}

type mcpRuntimeTestContextService struct {
	calls int
}

func (s *mcpRuntimeTestContextService) BuildContext(context.Context, query.ContextRequest) (query.ContextPackage, error) {
	s.calls++
	return query.ContextPackage{}, nil
}

type mcpRuntimeTestActiveScopeResolver struct {
	calls                int
	organizationExternal string
	sourceID             string
	result               query.Scope
}

func (r *mcpRuntimeTestActiveScopeResolver) ResolveActiveScope(_ context.Context, organizationExternal, sourceID string) (query.Scope, error) {
	r.calls++
	r.organizationExternal = organizationExternal
	r.sourceID = sourceID
	return r.result, nil
}

func (r *mcpRuntimeTestActiveScopeResolver) ResolveActiveSnapshot(ctx context.Context, historical query.Scope) (query.Scope, error) {
	return r.ResolveActiveScope(ctx, "local", historical.SourceID)
}
