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
