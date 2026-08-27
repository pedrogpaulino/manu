//go:build integration

package persistence_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/mcpadapter"
	"github.com/pedrogpaulino/manu/internal/persistence"
	query "github.com/pedrogpaulino/manu/internal/query"
)

type mcpPersistenceContextOutput struct {
	Context          query.ContextPackage `json:"context"`
	LatestSnapshotID string               `json:"latest_snapshot_id,omitempty"`
}

type mcpPersistenceResourceOutput struct {
	query.ContextPackage
	LatestSnapshotID string `json:"latest_snapshot_id,omitempty"`
}

type mcpPersistenceContextServiceSpy struct {
	mu       sync.Mutex
	delegate query.ContextService
	requests []query.ContextRequest
}

func (s *mcpPersistenceContextServiceSpy) BuildContext(ctx context.Context, request query.ContextRequest) (query.ContextPackage, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request.Clone())
	s.mu.Unlock()
	return s.delegate.BuildContext(ctx, request)
}

func (s *mcpPersistenceContextServiceSpy) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *mcpPersistenceContextServiceSpy) Requests() []query.ContextRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests := make([]query.ContextRequest, len(s.requests))
	copy(requests, s.requests)
	return requests
}

type mcpPersistenceAuditSink struct {
	mu      sync.Mutex
	records []mcpadapter.ContextAuditRecord
}

func (s *mcpPersistenceAuditSink) RecordContextAudit(_ context.Context, record mcpadapter.ContextAuditRecord) error {
	s.mu.Lock()
	s.records = append(s.records, record.Clone())
	s.mu.Unlock()
	return nil
}

func (s *mcpPersistenceAuditSink) Records() []mcpadapter.ContextAuditRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := make([]mcpadapter.ContextAuditRecord, len(s.records))
	copy(records, s.records)
	return records
}

func TestMCPPostgreSQLContextConformance(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "mcp-context", "snapshot-1", "class IntegrationType {}")
	repository := persistence.NewRepository(database.pool)
	jobs := persistence.NewJobStore(database.pool)
	pipeline := newIntegrationPipeline(t, database, repository, jobs, fixture, ingestion.EmbeddingOptions{})
	runIntegrationJob(t, jobs, pipeline, fixture.job)

	factual := factualRepositoryInput(t, fixture, "evidence-present")
	if err := repository.PersistFactualSnapshot(context.Background(), factual); err != nil {
		t.Fatalf("persist factual snapshot: %v", err)
	}
	if _, err := repository.RebuildFactualProjection(context.Background(), factual.OrganizationID, factual.SourceID, factual.SnapshotID); err != nil {
		t.Fatalf("rebuild factual projection: %v", err)
	}

	scope := query.Scope{
		OrganizationID: fixture.job.OrganizationID,
		SourceID:       fixture.job.SourceID,
		SnapshotID:     fixture.job.SnapshotID,
	}
	wantRevision := "revision-snapshot-1"
	limits := productionContextIntegrationLimits()
	service := &mcpPersistenceContextServiceSpy{
		delegate: newPostgresContextService(t, database, repository),
	}
	audit := &mcpPersistenceAuditSink{}
	session, closeSession := connectMCPPersistenceClient(t, service, mcpadapter.ContextServerOptions{
		ResourceLimits: limits,
		AuditSink:      audit,
	})
	defer closeSession()

	initialize := session.InitializeResult()
	if initialize == nil || initialize.ProtocolVersion != mcpadapter.ProtocolVersion {
		t.Fatalf("MCP negotiation = %#v, want protocol %q", initialize, mcpadapter.ProtocolVersion)
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}
	assertMCPPersistenceTools(t, tools)
	templates, err := session.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error: %v", err)
	}
	if len(templates.ResourceTemplates) != 1 || templates.ResourceTemplates[0].URITemplate != "manu://organizations/{organization}/sources/{source}/snapshots/{snapshot}/evidence/{id}" || templates.ResourceTemplates[0].MIMEType != "application/json" {
		t.Fatalf("resource templates = %#v, want one JSON Manu evidence template", templates.ResourceTemplates)
	}
	if got := service.Count(); got != 0 {
		t.Fatalf("service calls during negotiation/discovery = %d, want 0", got)
	}

	base := mcpPersistenceScopeArguments(scope, limits)
	queryArguments := cloneMCPPersistenceMap(base)
	queryArguments["question"] = "IntegrationType"
	before := service.Count()
	queryResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: queryArguments})
	if err != nil || queryResult == nil || queryResult.IsError {
		t.Fatalf("manu_query result = %#v, error = %v", queryResult, err)
	}
	if got := service.Count(); got != before+1 {
		t.Fatalf("manu_query service calls = %d, want one", got-before)
	}
	queryPackage := decodeMCPPersistenceOutput(t, queryResult)
	assertMCPPersistencePackage(t, queryPackage.Context, scope, query.IntentKindQuestion, wantRevision)
	evidenceID, evidenceContent, evidencePath := mcpPersistenceEvidenceItem(t, queryPackage.Context)
	if evidenceContent != "class IntegrationType {}" || evidencePath != "src/mcp-context.java" {
		t.Fatalf("query evidence content/locator = %q/%q, want authorized PostgreSQL evidence", evidenceContent, evidencePath)
	}
	resourceURI := fmt.Sprintf("manu://organizations/%s/sources/%s/snapshots/%s/evidence/%s", scope.OrganizationID, scope.SourceID, scope.SnapshotID, evidenceID)
	if !mcpPersistenceHasResourceLink(queryResult, resourceURI) {
		t.Fatalf("manu_query resource links = %#v, want authorized URI %q", queryResult.Content, resourceURI)
	}

	pageArguments := cloneMCPPersistenceMap(queryArguments)
	pageArguments["max_items"] = 1
	before = service.Count()
	firstPageResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: pageArguments})
	if err != nil || firstPageResult == nil || firstPageResult.IsError {
		t.Fatalf("first budgeted manu_query result = %#v, error = %v", firstPageResult, err)
	}
	if got := service.Count(); got != before+1 {
		t.Fatalf("first budgeted manu_query service calls = %d, want one", got-before)
	}
	firstPage := decodeMCPPersistenceOutput(t, firstPageResult).Context
	assertMCPPersistencePackage(t, firstPage, scope, query.IntentKindQuestion, wantRevision)
	if len(firstPage.Items) != 1 || !firstPage.Truncated || firstPage.Continuation == nil {
		t.Fatalf("first budgeted page = %#v, want one item and continuation", firstPage)
	}

	seenPageItems := map[string]struct{}{firstPage.Items[0].ID: {}}
	nonEmptyPages := 1
	continuationCalls := 0
	continuation := firstPage.Continuation
	for pageNumber := 2; continuation != nil && nonEmptyPages < 2 && pageNumber <= 8; pageNumber++ {
		currentToken := continuation.Token
		continuedArguments := cloneMCPPersistenceMap(pageArguments)
		continuedArguments["continuation"] = currentToken
		before = service.Count()
		continuedResult, callErr := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: continuedArguments})
		if callErr != nil || continuedResult == nil || continuedResult.IsError {
			t.Fatalf("continued manu_query page %d result = %#v, error = %v", pageNumber, continuedResult, callErr)
		}
		if got := service.Count(); got != before+1 {
			t.Fatalf("continued manu_query page %d service calls = %d, want one", pageNumber, got-before)
		}
		continuationCalls++
		continued := decodeMCPPersistenceOutput(t, continuedResult).Context
		assertMCPPersistencePackage(t, continued, scope, query.IntentKindQuestion, wantRevision)
		if len(continued.Items) > 1 {
			t.Fatalf("continued page %d item count = %d, want at most one", pageNumber, len(continued.Items))
		}
		for _, item := range continued.Items {
			if _, exists := seenPageItems[item.ID]; exists {
				t.Fatalf("continued page %d repeated item %q", pageNumber, item.ID)
			}
			seenPageItems[item.ID] = struct{}{}
			nonEmptyPages++
		}
		if continued.Continuation != nil && continued.Continuation.Token == currentToken {
			t.Fatalf("continued page %d did not advance its cursor", pageNumber)
		}
		continuation = continued.Continuation
	}
	if nonEmptyPages < 2 {
		t.Fatalf("budgeted continuation produced %d non-empty pages after %d calls; want two distinct pages", nonEmptyPages, continuationCalls)
	}

	contextArguments := cloneMCPPersistenceMap(base)
	contextArguments["target_kind"] = "symbol"
	contextArguments["target_id"] = "IntegrationType"
	before = service.Count()
	contextResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_context", Arguments: contextArguments})
	if err != nil || contextResult == nil || contextResult.IsError {
		t.Fatalf("manu_context result = %#v, error = %v", contextResult, err)
	}
	if got := service.Count(); got != before+1 {
		t.Fatalf("manu_context service calls = %d, want one", got-before)
	}
	contextPackage := decodeMCPPersistenceOutput(t, contextResult).Context
	assertMCPPersistencePackage(t, contextPackage, scope, query.IntentKindSymbol, wantRevision)

	impactArguments := cloneMCPPersistenceMap(contextArguments)
	before = service.Count()
	impactResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_impact", Arguments: impactArguments})
	if err != nil || impactResult == nil || impactResult.IsError {
		t.Fatalf("manu_impact result = %#v, error = %v", impactResult, err)
	}
	if got := service.Count(); got != before+1 {
		t.Fatalf("manu_impact service calls = %d, want one", got-before)
	}
	impactPackage := decodeMCPPersistenceOutput(t, impactResult).Context
	assertMCPPersistencePackage(t, impactPackage, scope, query.IntentKindPossibleImpact, wantRevision)

	evidenceArguments := cloneMCPPersistenceMap(base)
	evidenceArguments["evidence_id"] = evidenceID
	before = service.Count()
	evidenceResult, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_evidence", Arguments: evidenceArguments})
	if err != nil || evidenceResult == nil || evidenceResult.IsError {
		t.Fatalf("manu_evidence result = %#v, error = %v", evidenceResult, err)
	}
	if got := service.Count(); got != before+1 {
		t.Fatalf("manu_evidence service calls = %d, want one", got-before)
	}
	evidencePackage := decodeMCPPersistenceOutput(t, evidenceResult).Context
	assertMCPPersistencePackage(t, evidencePackage, scope, query.IntentKindEvidenceInspection, wantRevision)
	if evidencePackage.Intent.Target.ID != evidenceID {
		t.Fatalf("manu_evidence target = %q, want real evidence ID %q", evidencePackage.Intent.Target.ID, evidenceID)
	}

	before = service.Count()
	resource, err := session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: resourceURI})
	if err != nil || resource == nil || len(resource.Contents) != 1 {
		t.Fatalf("ReadResource() = %#v, error = %v", resource, err)
	}
	if got := service.Count(); got != before+1 {
		t.Fatalf("ReadResource() service calls = %d, want one", got-before)
	}
	if resource.Contents[0].URI != resourceURI || resource.Contents[0].MIMEType != "application/json" {
		t.Fatalf("resource content = %#v, want requested URI/application/json", resource.Contents[0])
	}
	var resourceOutput mcpPersistenceResourceOutput
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &resourceOutput); err != nil {
		t.Fatalf("resource JSON unmarshal: %v", err)
	}
	assertMCPPersistencePackage(t, resourceOutput.ContextPackage, scope, query.IntentKindEvidenceInspection, wantRevision)
	if resourceOutput.Intent.Target.ID != evidenceID {
		t.Fatalf("resource evidence target = %q, want %q", resourceOutput.Intent.Target.ID, evidenceID)
	}
	if resourceOutput.Limits != limits {
		t.Fatalf("resource limits = %#v, want configured limits %#v", resourceOutput.Limits, limits)
	}

	malformed := cloneMCPPersistenceMap(queryArguments)
	malformed["sql"] = "SELECT secret FROM evidence_units"
	before = service.Count()
	malformedResult, malformedErr := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: malformed})
	assertMCPPersistenceOpaqueError(t, malformedResult, malformedErr, "SELECT secret", "evidence_units")
	if got := service.Count(); got != before {
		t.Fatalf("service calls after SQL argument rejection = %d, want zero", got-before)
	}
	malformedBudget := cloneMCPPersistenceMap(queryArguments)
	malformedBudget["max_items"] = 0
	before = service.Count()
	malformedBudgetResult, malformedBudgetErr := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: malformedBudget})
	assertMCPPersistenceOpaqueError(t, malformedBudgetResult, malformedBudgetErr)
	if got := service.Count(); got != before {
		t.Fatalf("service calls after budget rejection = %d, want zero", got-before)
	}

	wrongSnapshot := cloneMCPPersistenceMap(queryArguments)
	wrongSnapshot["snapshot_id"] = identity.CanonicalUUID("snapshot", fixture.organizationID, fixture.sourceID, "missing-snapshot")
	before = service.Count()
	wrongSnapshotResult, wrongSnapshotErr := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: wrongSnapshot})
	assertMCPPersistenceOpaqueError(t, wrongSnapshotResult, wrongSnapshotErr, "missing-snapshot")
	if got := service.Count(); got != before+1 {
		t.Fatalf("wrong snapshot service calls = %d, want one authorized-port call", got-before)
	}

	wrongScope := cloneMCPPersistenceMap(queryArguments)
	wrongScope["organization_id"] = identity.CanonicalUUID("organization", fixture.organizationID+"-wrong")
	before = service.Count()
	wrongScopeResult, wrongScopeErr := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: wrongScope})
	assertMCPPersistenceOpaqueError(t, wrongScopeResult, wrongScopeErr, "SELECT", "missing-snapshot")
	if got := service.Count(); got != before+1 {
		t.Fatalf("wrong scope service calls = %d, want one authorized-port call", got-before)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before = service.Count()
	canceledResult, canceledErr := session.CallTool(canceled, &mcp.CallToolParams{Name: "manu_query", Arguments: queryArguments})
	if canceledErr == nil && (canceledResult == nil || !canceledResult.IsError) {
		t.Fatalf("cancelled manu_query result = %#v, error = %v, want cancellation", canceledResult, canceledErr)
	}
	if canceledErr != nil && !errors.Is(canceledErr, context.Canceled) && !strings.Contains(canceledErr.Error(), context.Canceled.Error()) {
		t.Fatalf("cancelled manu_query error = %v, want context cancellation", canceledErr)
	}
	if got := service.Count(); got != before {
		t.Fatalf("service calls after pre-cancelled request = %d, want zero", got-before)
	}

	records := audit.Records()
	if len(records) < 8 {
		t.Fatalf("audit records = %d, want records for each valid MCP operation", len(records))
	}
	for _, record := range records {
		if err := record.Validate(); err != nil {
			t.Fatalf("audit record = %#v, validation error = %v", record, err)
		}
	}
	for _, operation := range []mcpadapter.ContextAuditOperation{
		mcpadapter.ContextAuditOperationQuery,
		mcpadapter.ContextAuditOperationContext,
		mcpadapter.ContextAuditOperationImpact,
		mcpadapter.ContextAuditOperationEvidence,
		mcpadapter.ContextAuditOperationEvidenceResource,
	} {
		found := false
		for _, record := range records {
			if record.Operation == operation && record.Outcome == mcpadapter.ContextAuditOutcomeSuccess {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("audit records missing successful operation %q: %#v", operation, records)
		}
	}
	encodedAudit, err := json.Marshal(records)
	if err != nil {
		t.Fatalf("marshal audit records: %v", err)
	}
	if strings.Contains(string(encodedAudit), "class IntegrationType {}") {
		t.Fatalf("audit records carried source content: %s", encodedAudit)
	}

	if requests := service.Requests(); len(requests) == 0 {
		t.Fatal("context service spy received no valid requests")
	}
}

func connectMCPPersistenceClient(t *testing.T, service query.ContextService, options mcpadapter.ContextServerOptions) (*mcp.ClientSession, func()) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := mcpadapter.NewContextServerWithOptions(service, options)
	if err != nil {
		t.Fatalf("NewContextServerWithOptions() error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "manu-persistence-conformance", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("MCP client Connect() error: %v", err)
	}
	return session, func() {
		_ = session.Close()
		cancel()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("MCP context server did not finish")
		}
	}
}

func mcpPersistenceScopeArguments(scope query.Scope, limits query.ContextLimits) map[string]any {
	return map[string]any{
		"organization_id": scope.OrganizationID,
		"source_id":       scope.SourceID,
		"snapshot_id":     scope.SnapshotID,
		"max_tokens":      limits.MaxTokens,
		"max_items":       limits.MaxItems,
		"max_characters":  limits.MaxCharacters,
		"max_bytes":       limits.MaxBytes,
	}
}

func cloneMCPPersistenceMap(input map[string]any) map[string]any {
	clone := make(map[string]any, len(input))
	for key, value := range input {
		clone[key] = value
	}
	return clone
}

func decodeMCPPersistenceOutput(t *testing.T, result *mcp.CallToolResult) mcpPersistenceContextOutput {
	t.Helper()
	if result == nil || result.StructuredContent == nil {
		t.Fatalf("MCP result structured content = %#v, want ContextPackage", result)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal MCP structured content: %v", err)
	}
	var output mcpPersistenceContextOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("unmarshal MCP structured content: %v", err)
	}
	return output
}

func assertMCPPersistencePackage(t *testing.T, packageContext query.ContextPackage, scope query.Scope, intent query.IntentKind, revision string) {
	t.Helper()
	if err := packageContext.Validate(); err != nil {
		t.Fatalf("MCP ContextPackage.Validate() error: %v", err)
	}
	if packageContext.Scope != scope || packageContext.Intent.Kind != intent || packageContext.Revision != revision {
		t.Fatalf("MCP package scope/intent/revision = %#v/%q/%q, want %#v/%q/%q", packageContext.Scope, packageContext.Intent.Kind, packageContext.Revision, scope, intent, revision)
	}
}

func mcpPersistenceEvidenceItem(t *testing.T, packageContext query.ContextPackage) (string, string, string) {
	t.Helper()
	for _, item := range packageContext.Items {
		if item.Kind == query.ContextItemEvidence && item.Evidence != nil {
			return item.ID, item.Evidence.Content, item.Evidence.Locator.Path
		}
	}
	t.Fatalf("MCP query package has no evidence item: %#v", packageContext.Items)
	return "", "", ""
}

func mcpPersistenceHasResourceLink(result *mcp.CallToolResult, wantURI string) bool {
	if result == nil {
		return false
	}
	for _, content := range result.Content {
		link, ok := content.(*mcp.ResourceLink)
		if ok && link.URI == wantURI {
			return true
		}
	}
	return false
}

func assertMCPPersistenceTools(t *testing.T, tools *mcp.ListToolsResult) {
	t.Helper()
	wantNames := []string{"manu_context", "manu_evidence", "manu_impact", "manu_query"}
	if tools == nil || len(tools.Tools) != len(wantNames) {
		t.Fatalf("MCP tools = %#v, want %d tools", tools, len(wantNames))
	}
	for index, tool := range tools.Tools {
		if tool.Name != wantNames[index] || tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("MCP tool[%d] = %#v, want %q and both schemas", index, tool, wantNames[index])
		}
		inputSchema, ok := tool.InputSchema.(map[string]any)
		if !ok || inputSchema["additionalProperties"] != false {
			t.Fatalf("MCP tool %q input schema = %#v, want strict object", tool.Name, tool.InputSchema)
		}
		properties, ok := inputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("MCP tool %q properties = %#v, want object", tool.Name, inputSchema["properties"])
		}
		for _, field := range []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "continuation"} {
			if _, exists := properties[field]; !exists {
				t.Fatalf("MCP tool %q schema missing %q", tool.Name, field)
			}
		}
		switch tool.Name {
		case "manu_query":
			if _, exists := properties["question"]; !exists {
				t.Fatalf("manu_query schema missing question")
			}
		case "manu_context", "manu_impact":
			if _, exists := properties["target_kind"]; !exists {
				t.Fatalf("%s schema missing target_kind", tool.Name)
			}
		case "manu_evidence":
			if _, exists := properties["evidence_id"]; !exists {
				t.Fatalf("manu_evidence schema missing evidence_id")
			}
		}
	}
}

func assertMCPPersistenceOpaqueError(t *testing.T, result *mcp.CallToolResult, callErr error, forbidden ...string) {
	t.Helper()
	if callErr != nil && result == nil {
		for _, value := range forbidden {
			if strings.Contains(callErr.Error(), value) {
				t.Fatalf("MCP opaque error %q leaked %q", callErr, value)
			}
		}
		return
	}
	if result == nil || !result.IsError {
		t.Fatalf("MCP error result = %#v, error = %v, want opaque tool error", result, callErr)
	}
	encoded, err := json.Marshal(result.Content)
	if err != nil {
		t.Fatalf("marshal MCP error content: %v", err)
	}
	for _, value := range forbidden {
		if strings.Contains(string(encoded), value) {
			t.Fatalf("MCP opaque error content leaked %q: %s", value, encoded)
		}
	}
}
