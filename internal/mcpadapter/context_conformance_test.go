package mcpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/query"
)

func TestContextMCPConformanceNegotiationDiscoveryDelegationAndAudit(t *testing.T) {
	limits := query.ContextLimits{MaxTokens: 17, MaxItems: 3, MaxCharacters: 200, MaxBytes: 400}
	sink := &contextAuditTestSink{}
	service := contextToolTestService{
		resultFor: func(request query.ContextRequest) query.ContextPackage {
			return contextConformancePackage(t, request)
		},
	}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &service, ContextServerOptions{
		ResourceLimits: limits,
		AuditSink:      sink,
	})
	defer closeSession()

	initialize := clientSession.InitializeResult()
	if initialize == nil {
		t.Fatal("InitializeResult() = nil")
	}
	if initialize.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %q, want %q", initialize.ProtocolVersion, ProtocolVersion)
	}
	wantInfo := ServerImplementation("")
	if initialize.ServerInfo == nil || initialize.ServerInfo.Name != wantInfo.Name || initialize.ServerInfo.Title != wantInfo.Title || initialize.ServerInfo.Version != wantInfo.Version {
		t.Fatalf("server info = %#v, want %#v", initialize.ServerInfo, wantInfo)
	}
	if initialize.Capabilities == nil || initialize.Capabilities.Tools == nil || initialize.Capabilities.Resources == nil {
		t.Fatalf("capabilities = %#v, want tools and resources", initialize.Capabilities)
	}
	if initialize.Capabilities.Prompts != nil || initialize.Capabilities.Logging != nil || initialize.Capabilities.Completions != nil ||
		initialize.Capabilities.Experimental != nil || initialize.Capabilities.Extensions != nil {
		t.Fatalf("capabilities expose unsupported features: %#v", initialize.Capabilities)
	}

	tools, err := clientSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	wantNames := []string{"manu_context", "manu_evidence", "manu_impact", "manu_query"}
	if len(tools.Tools) != len(wantNames) {
		t.Fatalf("ListTools() returned %d tools, want %d", len(tools.Tools), len(wantNames))
	}
	for index, tool := range tools.Tools {
		if tool.Name != wantNames[index] {
			t.Fatalf("tool[%d].Name = %q, want %q", index, tool.Name, wantNames[index])
		}
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint ||
			tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q annotations = %#v, want read-only/idempotent/closed", tool.Name, tool.Annotations)
		}
		inputSchema, ok := tool.InputSchema.(map[string]any)
		if !ok {
			t.Fatalf("tool %q input schema type = %T, want object", tool.Name, tool.InputSchema)
		}
		assertConformanceStrictObjectSchema(t, tool.Name+" input", inputSchema)
		if _, ok := inputSchema["properties"].(map[string]any); !ok {
			t.Fatalf("tool %q input schema properties = %#v, want object", tool.Name, inputSchema["properties"])
		}
		if tool.OutputSchema == nil {
			t.Fatalf("tool %q output schema = nil", tool.Name)
		}
		outputSchema, ok := tool.OutputSchema.(map[string]any)
		if !ok {
			t.Fatalf("tool %q output schema type = %T, want object", tool.Name, tool.OutputSchema)
		}
		assertConformanceStrictObjectSchema(t, tool.Name+" output", outputSchema)
	}

	templates, err := clientSession.ListResourceTemplates(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListResourceTemplates() error = %v", err)
	}
	if len(templates.ResourceTemplates) != 1 || templates.ResourceTemplates[0].URITemplate != contextEvidenceResourceTemplate ||
		templates.ResourceTemplates[0].MIMEType != "application/json" {
		t.Fatalf("resource templates = %#v, want one application/json Manu URI template", templates.ResourceTemplates)
	}

	base := contextConformanceScopeArguments()
	continuation := "opaque-conformance-cursor"
	queryArguments := cloneJSONMap(base)
	queryArguments["question"] = "where is the entrypoint?"
	queryArguments["continuation"] = continuation
	contextArguments := cloneJSONMap(base)
	contextArguments["target_kind"] = "symbol"
	contextArguments["target_id"] = "symbol-1"
	contextArguments["continuation"] = continuation
	impactArguments := cloneJSONMap(base)
	impactArguments["target_kind"] = "entity"
	impactArguments["target_id"] = "entity-1"
	impactArguments["continuation"] = continuation
	evidenceArguments := cloneJSONMap(base)
	evidenceArguments["evidence_id"] = "evidence-conformance"
	evidenceArguments["continuation"] = continuation

	for _, call := range []struct {
		name string
		args map[string]any
		kind query.IntentKind
	}{
		{name: "manu_query", args: queryArguments, kind: query.IntentKindQuestion},
		{name: "manu_context", args: contextArguments, kind: query.IntentKindSymbol},
		{name: "manu_impact", args: impactArguments, kind: query.IntentKindPossibleImpact},
		{name: "manu_evidence", args: evidenceArguments, kind: query.IntentKindEvidenceInspection},
	} {
		result, callErr := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: call.name, Arguments: call.args})
		if callErr != nil || result == nil || result.IsError || result.StructuredContent == nil {
			t.Fatalf("%s result = %#v, error = %v, want valid structured content", call.name, result, callErr)
		}
		var output contextToolOutput
		encoded, marshalErr := json.Marshal(result.StructuredContent)
		if marshalErr != nil {
			t.Fatalf("%s structured content marshal: %v", call.name, marshalErr)
		}
		if err := json.Unmarshal(encoded, &output); err != nil {
			t.Fatalf("%s structured content unmarshal: %v", call.name, err)
		}
		if err := output.Context.Validate(); err != nil {
			t.Fatalf("%s structured context validation: %v", call.name, err)
		}
		if output.Context.Intent.Kind != call.kind {
			t.Fatalf("%s intent kind = %q, want %q", call.name, output.Context.Intent.Kind, call.kind)
		}
	}

	uri := fmtResourceURI(contextToolTestPackageScope, "evidence-conformance")
	resource, err := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if err != nil || resource == nil || len(resource.Contents) != 1 {
		t.Fatalf("ReadResource() = %#v, error = %v, want one content", resource, err)
	}
	if resource.Contents[0].URI != uri || resource.Contents[0].MIMEType != "application/json" {
		t.Fatalf("resource content = %#v, want requested URI/application/json", resource.Contents[0])
	}
	var resourceOutput contextResourceOutput
	if err := json.Unmarshal([]byte(resource.Contents[0].Text), &resourceOutput); err != nil {
		t.Fatalf("resource JSON unmarshal: %v", err)
	}
	if err := resourceOutput.ContextPackage.Validate(); err != nil {
		t.Fatalf("resource context validation: %v", err)
	}
	if resourceOutput.Scope != contextToolTestPackageScope || resourceOutput.Intent.Kind != query.IntentKindEvidenceInspection ||
		resourceOutput.Intent.Target.ID != "evidence-conformance" {
		t.Fatalf("resource context = %#v, want exact evidence intent/scope", resourceOutput.ContextPackage)
	}

	wantRequests := []query.ContextRequest{
		contextConformanceRequest(query.Intent{Version: query.ContextVersion, Kind: query.IntentKindQuestion, Question: "where is the entrypoint?"}, continuation, base),
		contextConformanceRequest(query.Intent{Version: query.ContextVersion, Kind: query.IntentKindSymbol, Target: query.IntentTarget{Kind: query.IntentTargetSymbol, ID: "symbol-1"}}, continuation, base),
		contextConformanceRequest(query.Intent{Version: query.ContextVersion, Kind: query.IntentKindPossibleImpact, Target: query.IntentTarget{Kind: query.IntentTargetEntity, ID: "entity-1"}}, continuation, base),
		contextConformanceRequest(query.Intent{Version: query.ContextVersion, Kind: query.IntentKindEvidenceInspection, Target: query.IntentTarget{Kind: query.IntentTargetEvidence, ID: "evidence-conformance"}}, continuation, base),
		{
			Version: query.ContextVersion,
			Scope:   contextToolTestPackageScope,
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindEvidenceInspection,
				Target:  query.IntentTarget{Kind: query.IntentTargetEvidence, ID: "evidence-conformance"},
			},
			Limits: limits,
		},
	}
	if got := service.Requests(); len(got) != len(wantRequests) || !reflect.DeepEqual(got, wantRequests) {
		t.Fatalf("service requests = %#v, want exact one-per-call sequence %#v", got, wantRequests)
	}

	records := sink.Records()
	if len(records) != 5 {
		t.Fatalf("audit records = %#v, want one per four tools/resource", records)
	}
	wantOperations := []ContextAuditOperation{
		ContextAuditOperationQuery,
		ContextAuditOperationContext,
		ContextAuditOperationImpact,
		ContextAuditOperationEvidence,
		ContextAuditOperationEvidenceResource,
	}
	for index, record := range records {
		if record.Operation != wantOperations[index] || record.Outcome != ContextAuditOutcomeSuccess || record.Duration < 0 {
			t.Fatalf("audit record[%d] = %#v, want successful %q", index, record, wantOperations[index])
		}
		if err := record.Validate(); err != nil {
			t.Fatalf("audit record[%d] validation: %v", index, err)
		}
	}
}

func TestContextMCPConformanceRejectsBudgetAndMalformedArgumentsBeforeService(t *testing.T) {
	t.Run("minimum before service", func(t *testing.T) {
		service := contextToolTestService{result: contextToolTestPackage(t)}
		clientSession, closeSession := connectContextToolClient(t, &service)
		defer closeSession()
		arguments := contextConformanceQueryArguments()
		arguments["max_tokens"] = 0
		result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: arguments})
		assertConformanceToolError(t, result, err, "")
		if got := service.Requests(); len(got) != 0 {
			t.Fatalf("service requests after minimum rejection = %#v, want none", got)
		}
	})

	t.Run("excessive through context port", func(t *testing.T) {
		service := contextToolTestService{
			result: contextToolTestPackage(t),
			errFor: func(request query.ContextRequest) error {
				if err := request.Limits.Validate(); err != nil {
					return err
				}
				return nil
			},
		}
		clientSession, closeSession := connectContextToolClient(t, &service)
		defer closeSession()
		arguments := contextConformanceQueryArguments()
		arguments["max_tokens"] = 1<<20 + 1
		result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: arguments})
		assertConformanceToolError(t, result, err, ErrContextRequestRejected.Error())
		if got := service.Requests(); len(got) != 1 {
			t.Fatalf("service requests after excessive rejection = %#v, want one application-port request", got)
		}
	})

	for _, test := range []struct {
		name  string
		field string
		value any
	}{
		{name: "missing required question", field: "question", value: nil},
		{name: "wrong budget type", field: "max_items", value: "11"},
		{name: "unknown property", field: "extra", value: "do-not-echo"},
		{name: "sql property", field: "sql", value: "SELECT secret"},
		{name: "cypher property", field: "cypher", value: "MATCH (secret) RETURN secret"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := contextToolTestService{result: contextToolTestPackage(t)}
			clientSession, closeSession := connectContextToolClient(t, &service)
			defer closeSession()
			arguments := contextConformanceQueryArguments()
			if test.value == nil {
				delete(arguments, test.field)
			} else {
				arguments[test.field] = test.value
			}
			result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_query", Arguments: arguments})
			assertConformanceToolError(t, result, err, "")
			if got := service.Requests(); len(got) != 0 {
				t.Fatalf("service requests after %s rejection = %#v, want none", test.name, got)
			}
		})
	}

	t.Run("invalid target enum", func(t *testing.T) {
		service := contextToolTestService{result: contextToolTestPackage(t)}
		clientSession, closeSession := connectContextToolClient(t, &service)
		defer closeSession()
		arguments := contextConformanceScopeArguments()
		arguments["target_kind"] = "file"
		arguments["target_id"] = "file-1"
		result, err := clientSession.CallTool(context.Background(), &mcp.CallToolParams{Name: "manu_context", Arguments: arguments})
		assertConformanceToolError(t, result, err, "")
		if got := service.Requests(); len(got) != 0 {
			t.Fatalf("service requests after target enum rejection = %#v, want none", got)
		}
	})
}

func TestContextMCPConformanceCancellationAndResourceReauthorization(t *testing.T) {
	limits := query.ContextLimits{MaxTokens: 17, MaxItems: 3, MaxCharacters: 200, MaxBytes: 400}
	sink := &contextAuditTestSink{}
	service := contextToolTestService{errFor: func(query.ContextRequest) error { return context.Canceled }}
	handler := queryToolHandler(&service, ContextServerOptions{AuditSink: sink})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := handler(ctx, nil, contextQueryInput{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled handler error = %v, want context.Canceled", err)
	}
	if len(service.Requests()) != 0 {
		t.Fatalf("service requests for pre-cancelled call = %#v, want none", service.Requests())
	}
	if records := sink.Records(); len(records) != 1 || records[0].Outcome != ContextAuditOutcomeCancelled {
		t.Fatalf("cancelled audit records = %#v, want one cancelled record", records)
	}

	resourceService := contextToolTestService{
		resultFor: func(request query.ContextRequest) query.ContextPackage {
			return contextToolTestEvidencePackage(t, request.Intent.Target.ID, query.ContextDegradationExactUnavailable)
		},
	}
	clientSession, closeSession := connectContextToolClientWithOptions(t, &resourceService, ContextServerOptions{ResourceLimits: limits})
	defer closeSession()
	uri := fmtResourceURI(contextToolTestPackageScope, "evidence-reauthorized")
	resource, readErr := clientSession.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if readErr != nil || resource == nil || len(resource.Contents) != 1 {
		t.Fatalf("resource read = %#v, error = %v, want valid content", resource, readErr)
	}
	requests := resourceService.Requests()
	if len(requests) != 1 || requests[0].Scope != contextToolTestPackageScope || requests[0].Limits != limits ||
		requests[0].Intent.Kind != query.IntentKindEvidenceInspection || requests[0].Intent.Target.Kind != query.IntentTargetEvidence ||
		requests[0].Intent.Target.ID != "evidence-reauthorized" || requests[0].Continuation != nil {
		t.Fatalf("resource service request = %#v, want exact reauthorized evidence request", requests)
	}
}

func TestContextMCPAdapterProductionFilesHaveNoBackendBypass(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() failed")
	}
	directory := filepath.Dir(filename)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", directory, err)
	}
	const queryImport = "github.com/pedrogpaulino/manu/internal/query"
	const buildInfoImport = "github.com/pedrogpaulino/manu/internal/buildinfo"
	foundContextServicePort := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("ParseFile(%q): %v", path, parseErr)
		}
		for _, importSpec := range file.Imports {
			importPath, unquoteErr := strconv.Unquote(importSpec.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("Unquote(%q): %v", importSpec.Path.Value, unquoteErr)
			}
			if strings.Contains(importPath, "/internal/persistence") || strings.Contains(importPath, "/internal/retrieval") ||
				strings.HasPrefix(importPath, "github.com/jackc/pgx") || importPath == "database/sql" || importPath == "database/sql/driver" {
				t.Errorf("%s imports backend implementation %q", entry.Name(), importPath)
			}
			if strings.HasPrefix(importPath, "github.com/pedrogpaulino/manu/internal/") && importPath != queryImport && importPath != buildInfoImport {
				t.Errorf("%s imports non-port internal package %q", entry.Name(), importPath)
			}
			if importPath == queryImport {
				ast.Inspect(file, func(node ast.Node) bool {
					selector, ok := node.(*ast.SelectorExpr)
					if !ok || selector.Sel.Name != "ContextService" {
						return true
					}
					identifier, ok := selector.X.(*ast.Ident)
					if ok && identifier.Name == "query" {
						foundContextServicePort = true
					}
					return true
				})
			}
		}
	}
	if !foundContextServicePort {
		t.Fatal("production adapter files do not reference query.ContextService")
	}
}

func contextConformancePackage(t *testing.T, request query.ContextRequest) query.ContextPackage {
	t.Helper()
	if request.Intent.Kind == query.IntentKindEvidenceInspection {
		return contextToolTestEvidencePackage(t, request.Intent.Target.ID, query.ContextDegradationExactUnavailable)
	}
	packageContext := contextToolTestPackage(t).Clone()
	packageContext.ID = ""
	packageContext.Digest = ""
	packageContext.Intent = request.Intent
	packageContext.Limits = request.Limits
	finalized, err := query.FinalizeContextPackage(context.Background(), packageContext, query.ContextPackageIdentityBinding{
		PolicyDigest: strings.Repeat("0", 64),
	})
	if err != nil {
		t.Fatalf("FinalizeContextPackage(%q): %v", request.Intent.Kind, err)
	}
	return finalized
}

func contextConformanceRequest(intent query.Intent, continuation string, arguments map[string]any) query.ContextRequest {
	return query.ContextRequest{
		Version: query.ContextVersion,
		Scope:   contextToolTestPackageScope,
		Intent:  intent,
		Limits: query.ContextLimits{
			MaxTokens:     arguments["max_tokens"].(int),
			MaxItems:      arguments["max_items"].(int),
			MaxCharacters: arguments["max_characters"].(int64),
			MaxBytes:      arguments["max_bytes"].(int64),
		},
		Continuation: &query.ContextContinuation{Token: continuation},
	}
}

func contextConformanceScopeArguments() map[string]any {
	return map[string]any{
		"organization_id": contextToolTestPackageScope.OrganizationID,
		"source_id":       contextToolTestPackageScope.SourceID,
		"snapshot_id":     contextToolTestPackageScope.SnapshotID,
		"max_tokens":      101,
		"max_items":       11,
		"max_characters":  int64(1001),
		"max_bytes":       int64(2001),
	}
}

func contextConformanceQueryArguments() map[string]any {
	arguments := contextConformanceScopeArguments()
	arguments["question"] = "conformance question"
	return arguments
}

func assertConformanceStrictObjectSchema(t *testing.T, name string, schema map[string]any) {
	t.Helper()
	if schema["type"] != "object" {
		t.Errorf("%s schema type = %#v, want object", name, schema["type"])
	}
	additional, ok := schema["additionalProperties"]
	if !ok {
		t.Errorf("%s schema omits additionalProperties", name)
		return
	}
	switch value := additional.(type) {
	case bool:
		if value {
			t.Errorf("%s schema additionalProperties = true, want false", name)
		}
	case map[string]any:
		// jsonschema-go represents a false schema as {"not": {}}.
		notSchema, validFalse := value["not"].(map[string]any)
		if !validFalse || len(notSchema) != 0 || len(value) != 1 {
			t.Errorf("%s schema additionalProperties = %#v, want false schema", name, additional)
		}
	default:
		t.Errorf("%s schema additionalProperties type = %T, want false schema", name, additional)
	}
}

func assertConformanceToolError(t *testing.T, result *mcp.CallToolResult, err error, want string) {
	t.Helper()
	if err != nil || result == nil || !result.IsError {
		t.Fatalf("tool error result = %#v, error = %v, want IsError", result, err)
	}
	encoded, marshalErr := json.Marshal(result.Content)
	if marshalErr != nil {
		t.Fatalf("marshal tool error content: %v", marshalErr)
	}
	if strings.Contains(string(encoded), "SELECT secret") || strings.Contains(string(encoded), "MATCH (secret)") {
		t.Fatalf("tool error content = %s, leaked rejected input", encoded)
	}
	if want != "" && !strings.Contains(string(encoded), want) {
		t.Fatalf("tool error content = %s, want opaque sentinel %q", encoded, want)
	}
}
