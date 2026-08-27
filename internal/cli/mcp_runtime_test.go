package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/mcpadapter"
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
	auditSink := mcpadapter.ContextAuditSinkFunc(func(context.Context, mcpadapter.ContextAuditRecord) error {
		return nil
	})
	options, err := composeMCPContextServerOptions(configuration, resolver, auditSink)
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
	if options.AuditSink == nil {
		t.Fatal("audit sink was not retained in server options")
	}
}

func TestComposeMCPContextServerOptionsRejectsInvalidResourceLimits(t *testing.T) {
	configuration := config.Default()
	configuration.Retrieval.MaxPackageBytes = 16<<20 + 1
	auditSink := mcpadapter.ContextAuditSinkFunc(func(context.Context, mcpadapter.ContextAuditRecord) error {
		return nil
	})
	_, err := composeMCPContextServerOptions(configuration, &mcpRuntimeTestActiveScopeResolver{}, auditSink)
	if !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("composeMCPContextServerOptions() error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
}

func TestComposeMCPContextServerOptionsRequiresAuditSink(t *testing.T) {
	configuration := config.Default()
	resolver := &mcpRuntimeTestActiveScopeResolver{}
	if _, err := composeMCPContextServerOptions(configuration, resolver, nil); !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("nil audit sink error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
	var typedNil *mcpJSONLAuditSink
	if _, err := composeMCPContextServerOptions(configuration, resolver, typedNil); !errors.Is(err, ErrMCPRuntimeConfiguration) {
		t.Fatalf("typed nil audit sink error = %v, want %v", err, ErrMCPRuntimeConfiguration)
	}
}

func TestMCPJSONLAuditSinkWritesExactValidatedRecord(t *testing.T) {
	var output bytes.Buffer
	sink, err := newMCPJSONLAuditSink(&output)
	if err != nil {
		t.Fatalf("newMCPJSONLAuditSink() error = %v", err)
	}
	record := mcpRuntimeAuditRecord()
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal expected record: %v", err)
	}
	expected := append(encoded, '\n')
	if err := sink.RecordContextAudit(context.Background(), record); err != nil {
		t.Fatalf("RecordContextAudit() error = %v", err)
	}
	if output.String() != string(expected) {
		t.Fatalf("audit JSONL = %q, want %q", output.String(), expected)
	}
	var decoded mcpadapter.ContextAuditRecord
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &decoded); err != nil {
		t.Fatalf("decode audit JSONL: %v", err)
	}
	if !reflect.DeepEqual(decoded, record) {
		t.Fatalf("decoded audit record = %#v, want %#v", decoded, record)
	}
}

func TestMCPJSONLAuditSinkWritesOneLinePerRecord(t *testing.T) {
	var output bytes.Buffer
	sink, err := newMCPJSONLAuditSink(&output)
	if err != nil {
		t.Fatalf("newMCPJSONLAuditSink() error = %v", err)
	}
	record := mcpRuntimeAuditRecord()
	for index := 0; index < 4; index++ {
		record.Duration = time.Duration(index) * time.Millisecond
		if err := sink.RecordContextAudit(context.Background(), record); err != nil {
			t.Fatalf("RecordContextAudit(%d) error = %v", index, err)
		}
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("audit lines = %d, want 4: %q", len(lines), output.String())
	}
	for index, line := range lines {
		var decoded mcpadapter.ContextAuditRecord
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode audit line %d: %v", index, err)
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("validate audit line %d: %v", index, err)
		}
	}
}

func TestMCPJSONLAuditSinkSerializesConcurrentRecords(t *testing.T) {
	var output bytes.Buffer
	sink, err := newMCPJSONLAuditSink(&output)
	if err != nil {
		t.Fatalf("newMCPJSONLAuditSink() error = %v", err)
	}
	const records = 64
	record := mcpRuntimeAuditRecord()
	var group sync.WaitGroup
	group.Add(records)
	for index := 0; index < records; index++ {
		go func() {
			defer group.Done()
			if err := sink.RecordContextAudit(context.Background(), record); err != nil {
				t.Errorf("RecordContextAudit() error = %v", err)
			}
		}()
	}
	group.Wait()
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != records {
		t.Fatalf("audit lines = %d, want %d", len(lines), records)
	}
	for index, line := range lines {
		var decoded mcpadapter.ContextAuditRecord
		if err := json.Unmarshal([]byte(line), &decoded); err != nil {
			t.Fatalf("decode concurrent audit line %d: %v", index, err)
		}
	}
}

func TestMCPJSONLAuditSinkRejectsNilAndWriteFailuresSafely(t *testing.T) {
	if _, err := newMCPJSONLAuditSink(nil); !errors.Is(err, ErrMCPRuntimeAudit) {
		t.Fatalf("nil writer error = %v, want %v", err, ErrMCPRuntimeAudit)
	}
	var typedNil *bytes.Buffer
	if _, err := newMCPJSONLAuditSink(typedNil); !errors.Is(err, ErrMCPRuntimeAudit) {
		t.Fatalf("typed nil writer error = %v, want %v", err, ErrMCPRuntimeAudit)
	}

	sink, err := newMCPJSONLAuditSink(mcpRuntimeFailingWriter{})
	if err != nil {
		t.Fatalf("newMCPJSONLAuditSink() error = %v", err)
	}
	err = sink.RecordContextAudit(context.Background(), mcpRuntimeAuditRecord())
	if !errors.Is(err, ErrMCPRuntimeAudit) {
		t.Fatalf("write failure error = %v, want %v", err, ErrMCPRuntimeAudit)
	}
	if strings.Contains(err.Error(), "postgres password=secret") {
		t.Fatal("write failure leaked internal writer error")
	}

	var output bytes.Buffer
	sink, err = newMCPJSONLAuditSink(&output)
	if err != nil {
		t.Fatalf("newMCPJSONLAuditSink() error = %v", err)
	}
	invalid := mcpRuntimeAuditRecord()
	invalid.Version = "internal secret query"
	if err := sink.RecordContextAudit(context.Background(), invalid); !errors.Is(err, ErrMCPRuntimeAudit) {
		t.Fatalf("invalid record error = %v, want %v", err, ErrMCPRuntimeAudit)
	}
	if output.Len() != 0 {
		t.Fatalf("invalid record wrote %q, want empty output", output.String())
	}
}

func TestRunMCPRuntimeRejectsUnavailableAuditBeforeDatabase(t *testing.T) {
	configuration := config.Default()
	configuration.MCP.Enabled = true
	if err := runMCPRuntime(context.Background(), configuration, nil); !errors.Is(err, ErrMCPRuntimeAudit) {
		t.Fatalf("runMCPRuntime() error = %v, want %v", err, ErrMCPRuntimeAudit)
	}
}

func mcpRuntimeAuditRecord() mcpadapter.ContextAuditRecord {
	return mcpadapter.ContextAuditRecord{
		Version:   mcpadapter.ContextAuditVersion,
		Operation: mcpadapter.ContextAuditOperationQuery,
		Scope: query.Scope{
			OrganizationID: identity.CanonicalUUID("organization", "local"),
			SourceID:       identity.CanonicalUUID("source", "source-1"),
			SnapshotID:     identity.CanonicalUUID("snapshot", "snapshot-1"),
		},
		Budget: query.ContextLimits{
			MaxTokens:     100,
			MaxItems:      4,
			MaxCharacters: 1 << 10,
			MaxBytes:      1 << 10,
		},
		Outcome:          mcpadapter.ContextAuditOutcomeSuccess,
		Duration:         time.Millisecond,
		SnapshotRevision: "revision-1",
		Truncated:        false,
		ItemIDs:          []string{"item-1"},
		RelationIDs:      []string{"relation-1"},
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

type mcpRuntimeFailingWriter struct{}

func (mcpRuntimeFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("postgres password=secret")
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
