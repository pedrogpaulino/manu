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
	mu        sync.Mutex
	requests  []query.ContextRequest
	result    query.ContextPackage
	resultFor func(query.ContextRequest) query.ContextPackage
	err       error
}

func (s *contextToolTestService) BuildContext(_ context.Context, request query.ContextRequest) (query.ContextPackage, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request.Clone())
	s.mu.Unlock()
	if s.err != nil {
		return query.ContextPackage{}, s.err
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
