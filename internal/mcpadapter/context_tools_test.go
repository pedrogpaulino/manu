package mcpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/query"
)

type contextToolTestService struct {
	mu       sync.Mutex
	requests []query.ContextRequest
	result   query.ContextPackage
	err      error
}

func (s *contextToolTestService) BuildContext(_ context.Context, request query.ContextRequest) (query.ContextPackage, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request.Clone())
	s.mu.Unlock()
	if s.err != nil {
		return query.ContextPackage{}, s.err
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

func TestContextToolsListSchemasAndAnnotations(t *testing.T) {
	service := contextToolTestService{result: contextToolTestPackage(t)}
	clientSession, closeSession := connectContextToolClient(t, &service)
	defer closeSession()

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("ListTools() returned %d tools, want 2", len(tools.Tools))
	}
	wantNames := []string{"manu_context", "manu_query"}
	for index, tool := range tools.Tools {
		if tool.Name != wantNames[index] {
			t.Fatalf("tool[%d].Name = %q, want %q", index, tool.Name, wantNames[index])
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
			tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations = %#v, want read-only/idempotent/closed", tool.Name, tool.Annotations)
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
		if tool.Name == "manu_query" {
			if _, ok := properties["question"]; !ok {
				t.Error("manu_query input schema missing question")
			}
		} else {
			targetKind := properties["target_kind"].(map[string]any)
			if !reflect.DeepEqual(targetKind["enum"], []any{"entity", "symbol"}) {
				t.Errorf("target_kind enum = %#v, want entity/symbol", targetKind["enum"])
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

func connectContextToolClient(t *testing.T, service query.ContextService) (*mcp.ClientSession, func()) {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	server, err := NewContextServer(service)
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
