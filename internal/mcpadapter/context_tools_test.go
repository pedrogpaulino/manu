package mcpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/query"
)

type contextToolTestService struct {
	mu        sync.Mutex
	requests  []query.ContextRequest
	result    query.ContextPackage
	resultFor func(query.ContextRequest) query.ContextPackage
	err       error
	errFor    func(query.ContextRequest) error
}

type contextToolTestSnapshotResolver struct {
	mu      sync.Mutex
	results []query.Scope
	calls   []query.Scope
	index   int
}

func (r *contextToolTestSnapshotResolver) ResolveActiveSnapshot(_ context.Context, scope query.Scope) (query.Scope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, scope)
	if r.index >= len(r.results) {
		return scope, nil
	}
	result := r.results[r.index]
	r.index++
	return result, nil
}

func (r *contextToolTestSnapshotResolver) Calls() []query.Scope {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]query.Scope(nil), r.calls...)
}

func (s *contextToolTestService) BuildContext(_ context.Context, request query.ContextRequest) (query.ContextPackage, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request.Clone())
	s.mu.Unlock()
	if s.err != nil {
		return query.ContextPackage{}, s.err
	}
	if s.errFor != nil {
		if err := s.errFor(request); err != nil {
			return query.ContextPackage{}, err
		}
	}
	if s.resultFor != nil {
		return s.resultFor(request).Clone(), nil
	}
	return s.result.Clone(), nil
}

func (s *contextToolTestService) Requests() []query.ContextRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]query.ContextRequest(nil), s.requests...)
}

func TestNewContextServerRejectsNilService(t *testing.T) {
	if _, err := NewContextServer(nil); !errors.Is(err, ErrNilContextService) {
		t.Fatalf("NewContextServer(nil) error = %v, want %v", err, ErrNilContextService)
	}
	var typedNil *contextToolTestService
	if _, err := NewContextServer(typedNil); !errors.Is(err, ErrNilContextService) {
		t.Fatalf("NewContextServer(typed nil) error = %v, want %v", err, ErrNilContextService)
	}
}

func TestContextServerOptionsRejectInvalidResourceLimits(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	_, err := NewContextServerWithOptions(&service, ContextServerOptions{
		ResourceLimits: query.ContextLimits{MaxTokens: 0, MaxItems: 1, MaxCharacters: 1, MaxBytes: 1},
	})
	if !errors.Is(err, ErrInvalidContextServerOptions) || !errors.Is(err, query.ErrInvalidContextBudget) {
		t.Fatalf("invalid resource options error = %v, want options and budget sentinels", err)
	}
}

func TestContextToolsListSchemasAndAnnotations(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("ListTools() returned %d tools, want 4", len(tools.Tools))
	}
	wantNames := []string{"manu_context", "manu_evidence", "manu_impact", "manu_query"}
	for index, tool := range tools.Tools {
		if tool.Name != wantNames[index] {
			t.Fatalf("tool[%d].Name = %q, want %q", index, tool.Name, wantNames[index])
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
			tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations = %#v, want read-only/idempotent/closed", tool.Name, tool.Annotations)
		}
		if tool.Name == "manu_impact" &&
			(!strings.Contains(tool.Description, "possible impact") || !strings.Contains(tool.Description, "never confirmed")) {
			t.Errorf("manu_impact description = %q, want possible/non-confirmed qualification", tool.Description)
		}
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Fatalf("tool %q schemas = input %#v, output %#v; both are required", tool.Name, tool.InputSchema, tool.OutputSchema)
		}
		inputSchema := tool.InputSchema.(map[string]any)
		properties := inputSchema["properties"].(map[string]any)
		for _, field := range []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes"} {
			if _, ok := properties[field]; !ok {
				t.Errorf("tool %q input schema missing %q", tool.Name, field)
			}
		}
		if _, ok := properties["continuation"]; !ok {
			t.Errorf("tool %q input schema missing optional continuation", tool.Name)
		}
		switch tool.Name {
		case "manu_query":
			if _, ok := properties["question"]; !ok {
				t.Error("manu_query input schema missing question")
			}
		case "manu_context", "manu_impact":
			targetKind := properties["target_kind"].(map[string]any)
			if !reflect.DeepEqual(targetKind["enum"], []any{"entity", "symbol"}) {
				t.Errorf("target_kind enum = %#v, want entity/symbol", targetKind["enum"])
			}
		case "manu_evidence":
			if _, ok := properties["evidence_id"]; !ok {
				t.Error("manu_evidence input schema missing evidence_id")
			}
		}
		for _, field := range []string{"max_tokens", "max_items", "max_characters", "max_bytes"} {
			fieldSchema := properties[field].(map[string]any)
			if fieldSchema["minimum"] != float64(1) {
				t.Errorf("%s minimum = %#v, want 1", field, fieldSchema["minimum"])
			}
		}
		outputSchema := tool.OutputSchema.(map[string]any)
		outputProperties := outputSchema["properties"].(map[string]any)
		contextSchema := outputProperties["context"].(map[string]any)
		if contextSchema["type"] != "object" {
			t.Errorf("output context type = %#v, want object", contextSchema["type"])
		}
	}
}

func TestContextToolsCallServiceWithExactRequestsAndStructuredOutput(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	arguments := map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
	}
	queryArguments := cloneJSONMap(arguments)
	queryArguments["question"] = "where is the entrypoint?"
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: queryArguments})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("manu_query result = %#v, error = %v", result, err)
	}
	assertContextToolOutput(t, result, service.result)

	contextArguments := cloneJSONMap(arguments)
	contextArguments["target_kind"] = "symbol"
	contextArguments["target_id"] = "symbol-1"
	result, err = clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_context", Arguments: contextArguments})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("manu_context result = %#v, error = %v", result, err)
	}
	assertContextToolOutput(t, result, service.result)

	want := []query.ContextRequest{
		{
			Version: query.ContextVersion,
			Scope: query.Scope{
				OrganizationID: contextToolTestPackageScope.OrganizationID,
				SourceID:       contextToolTestPackageScope.SourceID,
				SnapshotID:     contextToolTestPackageScope.SnapshotID,
			},
			Intent: query.Intent{
				Version:  query.ContextVersion,
				Kind:     query.IntentKindQuestion,
				Question: "where is the entrypoint?",
			},
			Limits: query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001},
		},
		{
			Version: query.ContextVersion,
			Scope: query.Scope{
				OrganizationID: contextToolTestPackageScope.OrganizationID,
				SourceID:       contextToolTestPackageScope.SourceID,
				SnapshotID:     contextToolTestPackageScope.SnapshotID,
			},
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindSymbol,
				Target:  query.IntentTarget{Kind: query.IntentTargetSymbol, ID: "symbol-1"},
			},
			Limits: query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001},
		},
	}
	if got := service.Requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("service requests = %#v, want %#v", got, want)
	}
}

func TestContextImpactAndEvidenceCallServiceWithExactQualifiedIntents(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	impactResult, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "manu_impact",
		Arguments: map[string]any{
			"organization_id": contextToolTestPackageScope.OrganizationID,
			"source_id":       contextToolTestPackageScope.SourceID,
			"snapshot_id":     contextToolTestPackageScope.SnapshotID,
			"max_tokens":      101,
			"max_items":       11,
			"max_characters":  1001,
			"max_bytes":       2001,
			"target_kind":     "entity",
			"target_id":       "entity-1",
		},
	})
	if err != nil || impactResult == nil || impactResult.IsError {
		t.Fatalf("manu_impact result = %#v, error = %v", impactResult, err)
	}
	assertContextToolOutput(t, impactResult, service.result)

	evidenceResult, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "manu_evidence",
		Arguments: map[string]any{
			"organization_id": contextToolTestPackageScope.OrganizationID,
			"source_id":       contextToolTestPackageScope.SourceID,
			"snapshot_id":     contextToolTestPackageScope.SnapshotID,
			"max_tokens":      101,
			"max_items":       11,
			"max_characters":  1001,
			"max_bytes":       2001,
			"evidence_id":     "evidence-1",
		},
	})
	if err != nil || evidenceResult == nil || evidenceResult.IsError {
		t.Fatalf("manu_evidence result = %#v, error = %v", evidenceResult, err)
	}
	assertContextToolOutput(t, evidenceResult, service.result)

	want := []query.ContextRequest{
		{
			Version: query.ContextVersion,
			Scope:   contextToolTestPackageScope,
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindPossibleImpact,
				Target:  query.IntentTarget{Kind: query.IntentTargetEntity, ID: "entity-1"},
			},
			Limits: query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001},
		},
		{
			Version: query.ContextVersion,
			Scope:   contextToolTestPackageScope,
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindEvidenceInspection,
				Target:  query.IntentTarget{Kind: query.IntentTargetEvidence, ID: "evidence-1"},
			},
			Limits: query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001},
		},
	}
	if got := service.Requests(); !reflect.DeepEqual(got, want) {
		t.Fatalf("impact/evidence requests = %#v, want %#v", got, want)
	}
}

func TestContextToolsRejectMalformedInputWithoutCallingService(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	base := map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
		"question":        "valid question",
	}
	base["max_tokens"] = 0
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: base})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("malformed query result = %#v, error = %v, want tool error", result, err)
	}
	contextArguments := map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
		"target_kind":     "file",
		"target_id":       "file-1",
	}
	result, err = clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_context", Arguments: contextArguments})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("malformed context result = %#v, error = %v, want tool error", result, err)
	}
	impactArguments := cloneJSONMap(contextArguments)
	impactArguments["target_kind"] = "path"
	result, err = clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_impact", Arguments: impactArguments})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("malformed impact result = %#v, error = %v, want tool error", result, err)
	}
	evidenceArguments := map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
	}
	result, err = clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_evidence", Arguments: evidenceArguments})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("malformed evidence result = %#v, error = %v, want tool error", result, err)
	}
	if got := service.Requests(); len(got) != 0 {
		t.Fatalf("service requests after malformed calls = %#v, want none", got)
	}
}

func TestContextToolsSanitizeServiceError(t *testing.T) {
	const secret = "postgres password should not cross MCP"
	service := contextToolTestService{err: errors.New(secret)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "manu_query",
		Arguments: map[string]any{
			"organization_id": contextToolTestPackageScope.OrganizationID,
			"source_id":       contextToolTestPackageScope.SourceID,
			"snapshot_id":     contextToolTestPackageScope.SnapshotID,
			"max_tokens":      101,
			"max_items":       11,
			"max_characters":  1001,
			"max_bytes":       2001,
			"question":        "valid question",
		},
	})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("service failure result = %#v, error = %v, want tool error", result, err)
	}
	encoded, marshalErr := json.Marshal(result.Content)
	if marshalErr != nil {
		t.Fatalf("marshal error content: %v", marshalErr)
	}
	if strings.Contains(string(encoded), secret) || !strings.Contains(string(encoded), ErrContextServiceFailure.Error()) {
		t.Fatalf("service error content = %s, want only sanitized sentinel", encoded)
	}
	evidenceResult, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "manu_evidence",
		Arguments: map[string]any{
			"organization_id": contextToolTestPackageScope.OrganizationID,
			"source_id":       contextToolTestPackageScope.SourceID,
			"snapshot_id":     contextToolTestPackageScope.SnapshotID,
			"max_tokens":      101,
			"max_items":       11,
			"max_characters":  1001,
			"max_bytes":       2001,
			"evidence_id":     "protected-evidence",
		},
	})
	if err != nil || evidenceResult == nil || !evidenceResult.IsError {
		t.Fatalf("protected evidence result = %#v, error = %v, want tool error", evidenceResult, err)
	}
	evidenceEncoded, marshalErr := json.Marshal(evidenceResult.Content)
	if marshalErr != nil {
		t.Fatalf("marshal evidence error content: %v", marshalErr)
	}
	if strings.Contains(string(evidenceEncoded), secret) || !strings.Contains(string(evidenceEncoded), ErrContextServiceFailure.Error()) {
		t.Fatalf("evidence error content = %s, want only sanitized sentinel", evidenceEncoded)
	}
}

func TestContextToolsContinuationRoundTripAndOpaqueRejection(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	const token = "opaque-cursor-7"
	base := map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
		"continuation":    token,
	}
	queryArguments := cloneJSONMap(base)
	queryArguments["question"] = "where is the entrypoint?"
	contextArguments := cloneJSONMap(base)
	contextArguments["target_kind"] = "symbol"
	contextArguments["target_id"] = "symbol-1"
	impactArguments := cloneJSONMap(base)
	impactArguments["target_kind"] = "entity"
	impactArguments["target_id"] = "entity-1"
	evidenceArguments := cloneJSONMap(base)
	evidenceArguments["evidence_id"] = "evidence-1"
	for _, arguments := range []struct {
		name string
		args map[string]any
	}{
		{name: "manu_query", args: queryArguments},
		{name: "manu_context", args: contextArguments},
		{name: "manu_impact", args: impactArguments},
		{name: "manu_evidence", args: evidenceArguments},
	} {
		result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: arguments.name, Arguments: arguments.args})
		if err != nil || result == nil || result.IsError {
			t.Fatalf("%s result = %#v, error = %v", arguments.name, result, err)
		}
	}
	requests := service.Requests()
	if len(requests) != 4 {
		t.Fatalf("service received %d requests, want 4", len(requests))
	}
	for index, request := range requests {
		if request.Continuation == nil || request.Continuation.Token != token {
			t.Fatalf("request[%d].Continuation = %#v, want opaque token %q", index, request.Continuation, token)
		}
	}

	service.err = errors.New("incompatible cursor contains backend secret")
	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: queryArguments})
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("incompatible cursor result = %#v, error = %v, want sanitized tool error", result, err)
	}
	encoded, marshalErr := json.Marshal(result.Content)
	if marshalErr != nil {
		t.Fatalf("marshal incompatible cursor error: %v", marshalErr)
	}
	if strings.Contains(string(encoded), "backend secret") || strings.Contains(string(encoded), token) ||
		!strings.Contains(string(encoded), ErrContextServiceFailure.Error()) {
		t.Fatalf("incompatible cursor error content = %s, want opaque sentinel", encoded)
	}
}

func TestContextToolEvidenceLinksPreserveStructuredContent(t *testing.T) {
	packageContext, evidenceID := contextToolTestPackageWithEvidence(t)
	service := contextToolTestService{result: packageContext}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "manu_query",
		Arguments: map[string]any{
			"organization_id": contextToolTestPackageScope.OrganizationID,
			"source_id":       contextToolTestPackageScope.SourceID,
			"snapshot_id":     contextToolTestPackageScope.SnapshotID,
			"max_tokens":      101,
			"max_items":       11,
			"max_characters":  1001,
			"max_bytes":       2001,
			"question":        "which evidence supports this?",
		},
	})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("manu_query with evidence result = %#v, error = %v", result, err)
	}
	if result.StructuredContent == nil || len(result.Content) != 1 {
		t.Fatalf("result content = %#v, structured = %#v, want one link and structured output", result.Content, result.StructuredContent)
	}
	link, ok := result.Content[0].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("result content[0] type = %T, want *mcp.ResourceLink", result.Content[0])
	}
	wantURI := fmt.Sprintf("manu://organizations/%s/sources/%s/snapshots/%s/evidence/%s", contextToolTestPackageScope.OrganizationID, contextToolTestPackageScope.SourceID, contextToolTestPackageScope.SnapshotID, evidenceID)
	if link.URI != wantURI || link.MIMEType != "application/json" {
		t.Fatalf("resource link = %#v, want URI %q and application/json", link, wantURI)
	}
	assertContextToolOutput(t, result, packageContext)
}

func TestContextServerOptionsIndicateLatestWithoutReplacingHistoricalScope(t *testing.T) {
	historical := contextToolTestPackageScope
	latest := historical
	latest.SnapshotID = "00000000-0000-4000-8000-000000000004"
	resolver := &contextToolTestSnapshotResolver{results: []query.Scope{latest, historical}}
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{ActiveSnapshotResolver: resolver})
	defer closeSession()

	arguments := map[string]any{
		"organization_id": historical.OrganizationID,
		"source_id":       historical.SourceID,
		"snapshot_id":     historical.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  1001,
		"max_bytes":       2001,
		"question":        "historical question",
	}
	first, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: cloneJSONMap(arguments)})
	if err != nil || first == nil || first.IsError {
		t.Fatalf("historical query result = %#v, error = %v", first, err)
	}
	second, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: cloneJSONMap(arguments)})
	if err != nil || second == nil || second.IsError {
		t.Fatalf("current query result = %#v, error = %v", second, err)
	}
	var firstOutput, secondOutput contextToolOutput
	firstJSON, _ := json.Marshal(first.StructuredContent)
	secondJSON, _ := json.Marshal(second.StructuredContent)
	if err := json.Unmarshal(firstJSON, &firstOutput); err != nil {
		t.Fatalf("unmarshal historical output: %v", err)
	}
	if err := json.Unmarshal(secondJSON, &secondOutput); err != nil {
		t.Fatalf("unmarshal current output: %v", err)
	}
	if firstOutput.Context.Scope != historical || firstOutput.LatestSnapshotID != latest.SnapshotID {
		t.Fatalf("historical output = %#v, want historical scope and latest %q", firstOutput, latest.SnapshotID)
	}
	if secondOutput.Context.Scope != historical || secondOutput.LatestSnapshotID != "" {
		t.Fatalf("current output = %#v, want unchanged historical scope and no latest indication", secondOutput)
	}
	if got := resolver.Calls(); len(got) != 2 || got[0] != historical || got[1] != historical {
		t.Fatalf("resolver calls = %#v, want historical scope for both calls", got)
	}
}

func TestContextEvidenceResourceTemplateReauthorizesAndReturnsHistoricalJSON(t *testing.T) {
	limits := query.ContextLimits{MaxTokens: 17, MaxItems: 3, MaxCharacters: 200, MaxBytes: 400}
	const evidenceID = "evidence-resource"
	service := contextToolTestService{
		resultFor: func(request query.ContextRequest) query.ContextPackage {
			return contextToolTestEvidencePackage(t, request.Intent.Target.ID, query.ContextDegradationExactUnavailable)
		},
	}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{ResourceLimits: limits})
	defer closeSession()

	templates, err := clientSession.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	if len(templates.ResourceTemplates) != 1 || templates.ResourceTemplates[0].URITemplate != contextEvidenceResourceTemplate {
		t.Fatalf("resource templates = %#v, want exactly %q", templates.ResourceTemplates, contextEvidenceResourceTemplate)
	}
	uri := fmt.Sprintf("manu://organizations/%s/sources/%s/snapshots/%s/evidence/%s", contextToolTestPackageScope.OrganizationID, contextToolTestPackageScope.SourceID, contextToolTestPackageScope.SnapshotID, evidenceID)
	for call := 0; call < 2; call++ {
		resource, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err != nil || resource == nil || len(resource.Contents) != 1 {
			t.Fatalf("ReadResource() call %d = %#v, error = %v", call, resource, err)
		}
		content := resource.Contents[0]
		if content.URI != uri || content.MIMEType != "application/json" {
			t.Fatalf("resource content = %#v, want URI and application/json", content)
		}
		var output contextResourceOutput
		if err := json.Unmarshal([]byte(content.Text), &output); err != nil {
			t.Fatalf("unmarshal resource content: %v", err)
		}
		if err := output.ContextPackage.Validate(); err != nil {
			t.Fatalf("resource package validation: %v", err)
		}
		if output.Scope != contextToolTestPackageScope || output.Intent.Target.ID != evidenceID || len(output.Items) != 0 ||
			len(output.Degradations) != 1 || output.Degradations[0].Code != query.ContextDegradationExactUnavailable {
			t.Fatalf("resource output = %#v, want historical controlled unavailable state", output)
		}
	}
	wantRequest := query.ContextRequest{
		Version: query.ContextVersion,
		Scope:   contextToolTestPackageScope,
		Intent: query.Intent{
			Version: query.ContextVersion,
			Kind:    query.IntentKindEvidenceInspection,
			Target:  query.IntentTarget{Kind: query.IntentTargetEvidence, ID: evidenceID},
		},
		Limits: limits,
	}
	requests := service.Requests()
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], wantRequest) || !reflect.DeepEqual(requests[1], wantRequest) {
		t.Fatalf("resource service requests = %#v, want two exact reauthorized requests %#v", requests, wantRequest)
	}
}

func TestContextEvidenceResourceRejectsMalformedCrossScopeAndOpaqueErrors(t *testing.T) {
	const secret = "authorization details and postgres password"
	service := contextToolTestService{
		resultFor: func(request query.ContextRequest) query.ContextPackage {
			return contextToolTestEvidencePackage(t, request.Intent.Target.ID, query.ContextDegradationExactUnavailable)
		},
	}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{
		ResourceLimits: query.ContextLimits{MaxTokens: 17, MaxItems: 3, MaxCharacters: 200, MaxBytes: 400},
	})
	defer closeSession()

	validURI := fmt.Sprintf("manu://organizations/%s/sources/%s/snapshots/%s/evidence/evidence-resource", contextToolTestPackageScope.OrganizationID, contextToolTestPackageScope.SourceID, contextToolTestPackageScope.SnapshotID)
	malformed := []string{
		"manu://organizations/not-a-uuid/sources/" + contextToolTestPackageScope.SourceID + "/snapshots/" + contextToolTestPackageScope.SnapshotID + "/evidence/evidence-resource",
		validURI + "?secret=1",
		validURI + "/extra",
		strings.Replace(validURI, "/evidence/", "/evidence/%2F", 1),
	}
	for _, uri := range malformed {
		_, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err == nil || strings.Contains(err.Error(), uri) {
			t.Fatalf("malformed resource URI %q error = %v, want opaque rejection", uri, err)
		}
	}
	if got := len(service.Requests()); got != 0 {
		t.Fatalf("service calls after malformed resources = %d, want 0", got)
	}

	crossScope := contextToolTestPackageScope
	crossScope.OrganizationID = "00000000-0000-4000-8000-000000000099"
	crossURI := fmt.Sprintf("manu://organizations/%s/sources/%s/snapshots/%s/evidence/evidence-resource", crossScope.OrganizationID, crossScope.SourceID, crossScope.SnapshotID)
	service.resultFor = func(_ query.ContextRequest) query.ContextPackage { return contextToolTestPackage(t) }
	_, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: crossURI})
	if err == nil || strings.Contains(err.Error(), crossURI) || strings.Contains(err.Error(), secret) {
		t.Fatalf("cross-scope resource error = %v, want opaque failure", err)
	}
}

func TestContextEvidencePassesThroughControlledProtectedAndUnavailableStates(t *testing.T) {
	const secret = "denied evidence payload must not cross MCP"
	protected := contextToolTestEvidencePackage(t, "protected-evidence", query.ContextDegradationPolicyFiltered)
	unavailable := contextToolTestEvidencePackage(t, "missing-evidence", query.ContextDegradationExactUnavailable)
	states := map[string]query.ContextPackage{
		"protected-evidence": protected,
		"missing-evidence":   unavailable,
	}
	service := contextToolTestService{
		resultFor: func(request query.ContextRequest) query.ContextPackage {
			return states[request.Intent.Target.ID]
		},
	}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	for _, evidenceID := range []string{"protected-evidence", "missing-evidence"} {
		result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "manu_evidence",
			Arguments: map[string]any{
				"organization_id": contextToolTestPackageScope.OrganizationID,
				"source_id":       contextToolTestPackageScope.SourceID,
				"snapshot_id":     contextToolTestPackageScope.SnapshotID,
				"max_tokens":      101,
				"max_items":       11,
				"max_characters":  1001,
				"max_bytes":       2001,
				"evidence_id":     evidenceID,
			},
		})
		if err != nil || result == nil || result.IsError {
			t.Fatalf("manu_evidence(%q) result = %#v, error = %v, want structured state", evidenceID, result, err)
		}
		assertContextToolOutput(t, result, states[evidenceID])
		encoded, marshalErr := json.Marshal(result.StructuredContent)
		if marshalErr != nil {
			t.Fatalf("marshal %q structured state: %v", evidenceID, marshalErr)
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("%q structured state contains denied content: %s", evidenceID, encoded)
		}
		var output contextToolOutput
		if err := json.Unmarshal(encoded, &output); err != nil {
			t.Fatalf("unmarshal %q structured state: %v", evidenceID, err)
		}
		if output.Context.Intent.Kind != query.IntentKindEvidenceInspection || output.Context.Intent.Target.ID != evidenceID {
			t.Fatalf("%q state intent = %#v, want evidence inspection for requested identity", evidenceID, output.Context.Intent)
		}
		if len(output.Context.Items) != 0 {
			t.Fatalf("%q state exposed %d evidence items, want zero protected/absent content", evidenceID, len(output.Context.Items))
		}
	}
}

func TestRunWithContextServiceHonorsCancellation(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	serverTransport, _ := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunWithContextService(ctx, serverTransport, &service) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunWithContextService() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunWithContextService() did not finish after cancellation")
	}
}

var contextToolTestPackageScope = query.Scope{
	OrganizationID: "00000000-0000-4000-8000-000000000001",
	SourceID:       "00000000-0000-4000-8000-000000000002",
	SnapshotID:     "00000000-0000-4000-8000-000000000003",
}

func contextToolTestPackage(t *testing.T) query.ContextPackage {
	t.Helper()
	limits := query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001}
	material := query.ContextPackage{
		Version:  query.ContextVersion,
		Revision: "revision-1",
		Scope:    contextToolTestPackageScope,
		Intent: query.Intent{
			Version:  query.ContextVersion,
			Kind:     query.IntentKindQuestion,
			Question: "fixture question",
		},
		Limits: limits,
	}
	packageContext, err := query.FinalizeContextPackage(context.Background(), material, query.ContextPackageIdentityBinding{
		PolicyDigest: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage() error = %v", err)
	}
	return packageContext
}

func contextToolTestPackageWithEvidence(t *testing.T) (query.ContextPackage, string) {
	t.Helper()
	unit := contextToolTestEvidenceUnit(t)
	material := query.ContextPackage{
		Version:  query.ContextVersion,
		Revision: "revision-1",
		Scope:    contextToolTestPackageScope,
		Intent: query.Intent{
			Version:  query.ContextVersion,
			Kind:     query.IntentKindQuestion,
			Question: "which evidence supports this?",
		},
		Limits: query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001},
		Items: []query.ContextItem{{
			ID:       unit.ID,
			Kind:     query.ContextItemEvidence,
			Origin:   query.ContextKnowledgeObserved,
			Scope:    contextToolTestPackageScope,
			Evidence: &unit,
			Locator:  unit.Locator,
		}},
		Audit: []query.ContextSelectionAudit{{
			ItemID:   unit.ID,
			Included: true,
			Reason:   query.ContextSelectionIncluded,
		}},
	}
	if err := material.Items[0].Validate(); err != nil {
		t.Fatalf("evidence context item invalid: %v", err)
	}
	packageContext, err := query.FinalizeContextPackage(context.Background(), material, query.ContextPackageIdentityBinding{
		PolicyDigest: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage() evidence error = %v", err)
	}
	return packageContext, unit.ID
}

func contextToolTestEvidenceUnit(t *testing.T) evidence.EvidenceUnit {
	t.Helper()
	const content = "safe evidence for the MCP resource link"
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: contextToolTestPackageScope.OrganizationID,
		SourceID:       contextToolTestPackageScope.SourceID,
		SnapshotID:     contextToolTestPackageScope.SnapshotID,
		ArtifactID:     "artifact-resource",
		Contribution: evidence.ContributionRef{
			ID:              "contribution-artifact-resource",
			ArtifactID:      "artifact-resource",
			AnalyzerID:      "generic",
			AnalyzerVersion: "v1",
			Method:          "paragraph",
		},
		Locator: contract.Locator{
			SourceID:   contextToolTestPackageScope.SourceID,
			ArtifactID: "artifact-resource",
			Path:       "src/resource.go",
			StartLine:  1,
			EndLine:    1,
		},
		ContentState:      evidence.ContentStatePresent,
		Content:           content,
		ContentHash:       evidence.ContentDigest(content),
		ContentBytes:      int64(len([]byte(content))),
		ContentCharacters: int64(len([]rune(content))),
		Persist:           evidence.DecisionAllow,
		ExternalTransfer:  evidence.DecisionAllow,
		Classification:    evidence.ClassificationSafeText,
	}
	unit.ID = evidence.EvidenceID(unit)
	if err := unit.Validate(); err != nil {
		t.Fatalf("evidence fixture invalid: %v", err)
	}
	return unit
}

func contextToolTestEvidencePackage(t *testing.T, evidenceID string, degradation query.ContextDegradationCode) query.ContextPackage {
	t.Helper()
	material := query.ContextPackage{
		Version:  query.ContextVersion,
		Revision: "revision-1",
		Scope:    contextToolTestPackageScope,
		Intent: query.Intent{
			Version: query.ContextVersion,
			Kind:    query.IntentKindEvidenceInspection,
			Target:  query.IntentTarget{Kind: query.IntentTargetEvidence, ID: evidenceID},
		},
		Limits: query.ContextLimits{MaxTokens: 101, MaxItems: 11, MaxCharacters: 1001, MaxBytes: 2001},
		Degradations: []query.ContextDegradation{{
			Code: degradation,
		}},
	}
	packageContext, err := query.FinalizeContextPackage(context.Background(), material, query.ContextPackageIdentityBinding{
		PolicyDigest: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage(%q) error = %v", evidenceID, err)
	}
	return packageContext
}

func connectContextToolClient(t *testing.T, service query.ContextService) (*mcp.ClientSession, func()) {
	return connectContextToolClientWithOptions(t, service, ContextServerOptions{})
}

func connectContextToolClientWithOptions(t *testing.T, service query.ContextService, options ContextServerOptions) (*mcp.ClientSession, func()) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := NewContextServerWithOptions(service, options)
	if err != nil {
		t.Fatalf("NewContextServer() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "context-tool-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect() error = %v", err)
	}
	return session, func() {
		_ = session.Close()
		cancel()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("context tool server did not finish")
		}
	}
}

func assertContextToolOutput(t *testing.T, result *mcp.CallToolResult, want query.ContextPackage) {
	t.Helper()
	if result.StructuredContent == nil {
		t.Fatal("StructuredContent = nil")
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal StructuredContent: %v", err)
	}
	var output contextToolOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatalf("unmarshal StructuredContent: %v", err)
	}
	wantEncoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal expected context: %v", err)
	}
	gotEncoded, err := json.Marshal(output.Context)
	if err != nil {
		t.Fatalf("marshal decoded context: %v", err)
	}
	if string(gotEncoded) != string(wantEncoded) {
		t.Fatalf("structured context JSON = %s, want %s", gotEncoded, wantEncoded)
	}
	if err := output.Context.Validate(); err != nil {
		t.Fatalf("structured context validation: %v", err)
	}
}

func cloneJSONMap(input map[string]any) map[string]any {
	clone := make(map[string]any, len(input))
	for key, value := range input {
		clone[key] = value
	}
	return clone
}
