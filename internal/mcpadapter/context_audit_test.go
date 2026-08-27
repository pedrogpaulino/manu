package mcpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/query"
)

type contextAuditTestSink struct {
	mu       sync.Mutex
	records  []ContextAuditRecord
	contexts []context.Context
	err      error
}

func (s *contextAuditTestSink) RecordContextAudit(ctx context.Context, record ContextAuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contexts = append(s.contexts, ctx)
	s.records = append(s.records, record.Clone())
	return s.err
}

func (s *contextAuditTestSink) Records() []ContextAuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]ContextAuditRecord, len(s.records))
	for index, record := range s.records {
		result[index] = record.Clone()
	}
	return result
}

func (s *contextAuditTestSink) Contexts() []context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]context.Context(nil), s.contexts...)
}

func TestContextAuditRecordIsVersionedValidatedAndDefensive(t *testing.T) {
	record := ContextAuditRecord{
		Version:          ContextAuditVersion,
		Operation:        ContextAuditOperationQuery,
		Scope:            contextToolTestPackageScope,
		Budget:           query.ContextLimits{MaxTokens: 8, MaxItems: 2, MaxCharacters: 100, MaxBytes: 200},
		Outcome:          ContextAuditOutcomeSuccess,
		SnapshotRevision: "revision-1",
		ItemIDs:          []string{"item-1"},
		RelationIDs:      []string{"relation-1"},
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("ContextAuditRecord.Validate() error = %v", err)
	}
	clone := record.Clone()
	clone.ItemIDs[0] = "changed"
	clone.RelationIDs[0] = "changed"
	if record.ItemIDs[0] != "item-1" || record.RelationIDs[0] != "relation-1" {
		t.Fatalf("Clone mutated original IDs: original=%#v clone=%#v", record, clone)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal audit record: %v", err)
	}
	for _, forbidden := range []string{"question", "target", "continuation", "content", "locator", "path", "sql", "cypher", "password"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("audit JSON = %s, contains forbidden field/text %q", encoded, forbidden)
		}
	}
}

func TestContextAuditRecordsExactSuccessfulDelivery(t *testing.T) {
	packageContext, evidenceID := contextToolTestPackageWithEvidence(t)
	sink := &contextAuditTestSink{}
	service := contextToolTestService{result: packageContext}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{AuditSink: sink})
	defer closeSession()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "manu_query",
		Arguments: contextAuditQueryArguments("audit question"),
	})
	if err != nil || result == nil || result.IsError || result.StructuredContent == nil {
		t.Fatalf("successful audited query = %#v, error = %v", result, err)
	}
	records := sink.Records()
	if len(records) != 1 {
		t.Fatalf("audit records = %#v, want one", records)
	}
	record := records[0]
	wantBudget := query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001}
	if record.Version != ContextAuditVersion || record.Operation != ContextAuditOperationQuery || record.Scope != contextToolTestPackageScope ||
		record.Budget != wantBudget || record.Outcome != ContextAuditOutcomeSuccess || record.SnapshotRevision != packageContext.Revision || record.Truncated != packageContext.Truncated ||
		!reflect.DeepEqual(record.ItemIDs, []string{evidenceID}) || len(record.RelationIDs) != 0 || record.Duration < 0 {
		t.Fatalf("audit success record = %#v, want exact operation/scope/budget/revision/IDs", record)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("audited success record validation: %v", err)
	}
}

func TestContextAuditMapsKnownErrorsWithoutLeakingDetails(t *testing.T) {
	const secret = "postgres password=very-secret SELECT cypher"
	tests := []struct {
		name       string
		serviceErr error
		wantError  error
		wantAudit  ContextAuditOutcome
	}{
		{name: "cursor", serviceErr: query.ErrInvalidContextContinuation, wantError: ErrContextCursorRejected, wantAudit: ContextAuditOutcomeRejected},
		{name: "request", serviceErr: query.ErrInvalidContextRequest, wantError: ErrContextRequestRejected, wantAudit: ContextAuditOutcomeRejected},
		{name: "snapshot", serviceErr: query.ErrContextServiceSnapshot, wantError: ErrContextUnavailable, wantAudit: ContextAuditOutcomeFailed},
		{name: "retrieval", serviceErr: query.ErrContextServiceRetrieval, wantError: ErrContextUnavailable, wantAudit: ContextAuditOutcomeFailed},
		{name: "composition", serviceErr: query.ErrContextServiceComposition, wantError: ErrContextUnavailable, wantAudit: ContextAuditOutcomeFailed},
		{name: "service cancellation", serviceErr: context.Canceled, wantError: context.Canceled, wantAudit: ContextAuditOutcomeCancelled},
		{name: "unknown", serviceErr: errors.New(secret), wantError: ErrContextServiceFailure, wantAudit: ContextAuditOutcomeFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sink := &contextAuditTestSink{}
			service := contextToolTestService{err: test.serviceErr}
			clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{AuditSink: sink})
			defer closeSession()
			arguments := contextAuditQueryArguments("error mapping")
			if test.name == "cursor" {
				arguments["continuation"] = "cursor"
			}
			result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: arguments})
			if err != nil || result == nil || !result.IsError {
				t.Fatalf("error result = %#v, error = %v, want tool error", result, err)
			}
			encoded, marshalErr := json.Marshal(result.Content)
			if marshalErr != nil {
				t.Fatalf("marshal error content: %v", marshalErr)
			}
			if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), test.wantError.Error()) {
				t.Fatalf("error content = %s, want only safe sentinel %q", encoded, test.wantError)
			}
			records := sink.Records()
			if len(records) != 1 || records[0].Outcome != test.wantAudit || records[0].Operation != ContextAuditOperationQuery {
				t.Fatalf("audit records = %#v, want outcome %q", records, test.wantAudit)
			}
			if records[0].SnapshotRevision != "" || len(records[0].ItemIDs) != 0 || len(records[0].RelationIDs) != 0 {
				t.Fatalf("error audit record = %#v, must contain no delivery metadata", records[0])
			}
		})
	}
}

func TestContextAuditUsesNonCancellableContextForCancellation(t *testing.T) {
	sink := &contextAuditTestSink{}
	service := contextToolTestService{result: contextToolTestPackage(t)}
	handler := queryToolHandler(&service, ContextServerOptions{AuditSink: sink})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := handler(ctx, nil, contextQueryInput{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled handler error = %v, want context.Canceled", err)
	}
	records := sink.Records()
	if len(records) != 1 || records[0].Outcome != ContextAuditOutcomeCancelled {
		t.Fatalf("cancelled audit records = %#v, want one cancelled record", records)
	}
	contexts := sink.Contexts()
	if len(contexts) != 1 || contexts[0].Err() != nil {
		t.Fatalf("audit context = %#v, want non-cancellable context", contexts)
	}
}

func TestContextAuditFailureBlocksStructuredDelivery(t *testing.T) {
	const secret = "audit sink backend password"
	sink := &contextAuditTestSink{err: errors.New(secret)}
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{AuditSink: sink})
	defer closeSession()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "manu_query",
		Arguments: contextAuditQueryArguments("must not be delivered"),
	})
	if err != nil || result == nil || !result.IsError || result.StructuredContent != nil {
		t.Fatalf("audit failure result = %#v, error = %v, want opaque error without structured delivery", result, err)
	}
	encoded, marshalErr := json.Marshal(result.Content)
	if marshalErr != nil {
		t.Fatalf("marshal audit failure content: %v", marshalErr)
	}
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), ErrContextAuditFailure.Error()) {
		t.Fatalf("audit failure content = %s, want opaque audit sentinel", encoded)
	}
}

func TestContextAuditResourceSuccessAndFailureAreContentFree(t *testing.T) {
	limits := query.ContextLimits{MaxTokens: 17, MaxItems: 3, MaxCharacters: 200, MaxBytes: 400}
	const evidenceID = "evidence-audit-resource"
	sink := &contextAuditTestSink{}
	service := contextToolTestService{resultFor: func(request query.ContextRequest) query.ContextPackage {
		return contextToolTestEvidencePackage(t, request.Intent.Target.ID, query.ContextDegradationExactUnavailable)
	}}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{ResourceLimits: limits, AuditSink: sink})
	defer closeSession()
	uri := fmtResourceURI(contextToolTestPackageScope, evidenceID)
	resource, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || resource == nil || len(resource.Contents) != 1 {
		t.Fatalf("audited resource = %#v, error = %v", resource, err)
	}
	records := sink.Records()
	if len(records) != 1 || records[0].Operation != ContextAuditOperationEvidenceResource || records[0].Outcome != ContextAuditOutcomeSuccess || records[0].SnapshotRevision == "" {
		t.Fatalf("resource success audit records = %#v", records)
	}
	service.err = errors.New("resource secret password SELECT")
	_, err = clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err == nil || strings.Contains(err.Error(), "resource secret") || strings.Contains(err.Error(), uri) {
		t.Fatalf("resource failure error = %v, want opaque", err)
	}
	records = sink.Records()
	if len(records) != 2 || records[1].Operation != ContextAuditOperationEvidenceResource || records[1].Outcome != ContextAuditOutcomeFailed || records[1].SnapshotRevision != "" {
		t.Fatalf("resource failure audit records = %#v", records)
	}
	encoded, marshalErr := json.Marshal(records[0])
	if marshalErr != nil {
		t.Fatalf("marshal resource audit record: %v", marshalErr)
	}
	for _, forbidden := range []string{"password", "SELECT", "cypher", evidenceID, "src/"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("resource audit JSON = %s, contains forbidden %q", encoded, forbidden)
		}
	}
}

func TestContextAuditRejectsArbitrarySQLArgumentsBeforeService(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	arguments := contextAuditQueryArguments("safe")
	arguments["sql"] = "SELECT * FROM secrets"
	arguments["cypher"] = "MATCH (n) RETURN n"
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: arguments})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("arbitrary query arguments result = %#v, error = %v, want schema rejection", result, err)
	}
	if got := service.Requests(); len(got) != 0 {
		t.Fatalf("service requests after arbitrary query rejection = %#v, want none", got)
	}
}

func contextAuditQueryArguments(question string) map[string]any {
	return map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
		"question":        question,
	}
}

func fmtResourceURI(scope query.Scope, evidenceID string) string {
	return "manu://organizations/" + scope.OrganizationID + "/sources/" + scope.SourceID + "/snapshots/" + scope.SnapshotID + "/evidence/" + evidenceID
}
