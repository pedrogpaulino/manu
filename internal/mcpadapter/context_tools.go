package mcpadapter

import (
	"context"
	"errors"
	"reflect"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/buildinfo"
	"github.com/pedrogpaulino/manu/internal/query"
)

var (
	// ErrNilContextService reports an invalid context-service boundary before
	// the MCP server is created.
	ErrNilContextService = errors.New("mcpadapter: context service must not be nil")
	// ErrContextServiceFailure is the payload-free error exposed for service
	// failures. The underlying error never crosses the MCP boundary.
	ErrContextServiceFailure = errors.New("mcpadapter: context service request failed")
)

type contextQueryInput struct {
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	SnapshotID     string `json:"snapshot_id"`
	MaxTokens      int    `json:"max_tokens"`
	MaxItems       int    `json:"max_items"`
	MaxCharacters  int64  `json:"max_characters"`
	MaxBytes       int64  `json:"max_bytes"`
	Question       string `json:"question"`
	Continuation   string `json:"continuation,omitempty"`
}

type contextTargetInput struct {
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	SnapshotID     string `json:"snapshot_id"`
	MaxTokens      int    `json:"max_tokens"`
	MaxItems       int    `json:"max_items"`
	MaxCharacters  int64  `json:"max_characters"`
	MaxBytes       int64  `json:"max_bytes"`
	TargetKind     string `json:"target_kind"`
	TargetID       string `json:"target_id"`
	Continuation   string `json:"continuation,omitempty"`
}

type contextEvidenceInput struct {
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	SnapshotID     string `json:"snapshot_id"`
	MaxTokens      int    `json:"max_tokens"`
	MaxItems       int    `json:"max_items"`
	MaxCharacters  int64  `json:"max_characters"`
	MaxBytes       int64  `json:"max_bytes"`
	EvidenceID     string `json:"evidence_id"`
	Continuation   string `json:"continuation,omitempty"`
}

type contextToolOutput struct {
	Context          query.ContextPackage `json:"context"`
	LatestSnapshotID string               `json:"latest_snapshot_id,omitempty"`
}

// NewContextServer creates the MCP server with the query and context tools.
// The tools are registered in their wire-visible, deterministic order.
func NewContextServer(service query.ContextService) (*mcp.Server, error) {
	return NewContextServerWithOptions(service, ContextServerOptions{})
}

// NewContextServerWithOptions creates the context server with optional
// resource reads and active-snapshot indication.
func NewContextServerWithOptions(service query.ContextService, options ContextServerOptions) (*mcp.Server, error) {
	if nilContextService(service) {
		return nil, ErrNilContextService
	}
	if err := options.Validate(); err != nil {
		return nil, err
	}

	outputSchema, err := jsonschema.For[contextToolOutput](nil)
	if err != nil {
		return nil, ErrContextServiceFailure
	}
	server := newServer(buildinfo.Current().Version)
	mcp.AddTool[contextQueryInput, contextToolOutput](server, &mcp.Tool{
		Name:         "manu_query",
		Description:  "Query authorized context by question.",
		InputSchema:  contextQuerySchema(),
		OutputSchema: outputSchema,
		Annotations:  readOnlyToolAnnotations(),
	}, queryToolHandler(service, options))
	mcp.AddTool[contextTargetInput, contextToolOutput](server, &mcp.Tool{
		Name:         "manu_context",
		Description:  "Get authorized context for an entity or symbol.",
		InputSchema:  contextTargetSchema(),
		OutputSchema: outputSchema,
		Annotations:  readOnlyToolAnnotations(),
	}, contextToolHandler(service, options))
	mcp.AddTool[contextTargetInput, contextToolOutput](server, &mcp.Tool{
		Name:         "manu_impact",
		Description:  "Return possible impact context, never confirmed execution.",
		InputSchema:  contextTargetSchema(),
		OutputSchema: outputSchema,
		Annotations:  readOnlyToolAnnotations(),
	}, impactToolHandler(service, options))
	mcp.AddTool[contextEvidenceInput, contextToolOutput](server, &mcp.Tool{
		Name:         "manu_evidence",
		Description:  "Reinspect authorized evidence by identity.",
		InputSchema:  contextEvidenceSchema(),
		OutputSchema: outputSchema,
		Annotations:  readOnlyToolAnnotations(),
	}, evidenceToolHandler(service, options))
	if options.resourcesEnabled() {
		server.AddResourceTemplate(&mcp.ResourceTemplate{
			Name:        "manu_evidence",
			Description: "Authorized evidence context resource.",
			MIMEType:    "application/json",
			URITemplate: contextEvidenceResourceTemplate,
		}, contextEvidenceResourceHandler(service, options))
	}
	return server, nil
}

// RunWithContextService serves the context-enabled MCP server over one
// injected transport until the peer closes it or ctx is cancelled.
func RunWithContextService(ctx context.Context, transport mcp.Transport, service query.ContextService) error {
	return RunWithContextServiceWithOptions(ctx, transport, service, ContextServerOptions{})
}

// RunWithContextServiceWithOptions serves an optionally resource-enabled
// context MCP server over one injected transport.
func RunWithContextServiceWithOptions(ctx context.Context, transport mcp.Transport, service query.ContextService, options ContextServerOptions) error {
	if transport == nil {
		return ErrNilTransport
	}
	server, err := NewContextServerWithOptions(service, options)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return server.Run(ctx, transport)
}

// RunStdioWithContextService serves the context-enabled MCP server over stdio.
func RunStdioWithContextService(ctx context.Context, service query.ContextService) error {
	return RunStdioWithContextServiceWithOptions(ctx, service, ContextServerOptions{})
}

// RunStdioWithContextServiceWithOptions serves an optionally resource-enabled
// context MCP server over stdio.
func RunStdioWithContextServiceWithOptions(ctx context.Context, service query.ContextService, options ContextServerOptions) error {
	return RunWithContextServiceWithOptions(ctx, &mcp.StdioTransport{}, service, options)
}

func queryToolHandler(service query.ContextService, options ContextServerOptions) mcp.ToolHandlerFor[contextQueryInput, contextToolOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input contextQueryInput) (*mcp.CallToolResult, contextToolOutput, error) {
		started := time.Now()
		if err := contextError(ctx); err != nil {
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationQuery, query.ContextRequest{}, started, err, err)
		}
		request := query.ContextRequest{
			Version: query.ContextVersion,
			Scope: query.Scope{
				OrganizationID: input.OrganizationID,
				SourceID:       input.SourceID,
				SnapshotID:     input.SnapshotID,
			},
			Intent: query.Intent{
				Version:  query.ContextVersion,
				Kind:     query.IntentKindQuestion,
				Question: input.Question,
			},
			Limits: query.ContextLimits{
				MaxTokens:     input.MaxTokens,
				MaxItems:      input.MaxItems,
				MaxCharacters: input.MaxCharacters,
				MaxBytes:      input.MaxBytes,
			},
			Continuation: contextContinuation(input.Continuation),
		}
		packageContext, err := service.BuildContext(ctx, request)
		if err != nil {
			safeErr := sanitizeContextServiceError(ctx, err)
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationQuery, request, started, err, safeErr)
		}
		return contextToolSuccess(ctx, packageContext, options, ContextAuditOperationQuery, request, started)
	}
}

func contextToolHandler(service query.ContextService, options ContextServerOptions) mcp.ToolHandlerFor[contextTargetInput, contextToolOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input contextTargetInput) (*mcp.CallToolResult, contextToolOutput, error) {
		started := time.Now()
		if err := contextError(ctx); err != nil {
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationContext, query.ContextRequest{}, started, err, err)
		}
		var targetKind query.IntentTargetKind
		var intentKind query.IntentKind
		switch input.TargetKind {
		case string(query.IntentTargetEntity):
			targetKind = query.IntentTargetEntity
			intentKind = query.IntentKindEntity
		case string(query.IntentTargetSymbol):
			targetKind = query.IntentTargetSymbol
			intentKind = query.IntentKindSymbol
		default:
			// The typed SDK schema rejects this before invoking the handler. Keep
			// the guard for direct handler use and future schema changes.
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationContext, query.ContextRequest{}, started, query.ErrInvalidContextRequest, query.ErrInvalidContextRequest)
		}
		request := query.ContextRequest{
			Version: query.ContextVersion,
			Scope: query.Scope{
				OrganizationID: input.OrganizationID,
				SourceID:       input.SourceID,
				SnapshotID:     input.SnapshotID,
			},
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    intentKind,
				Target: query.IntentTarget{
					Kind: targetKind,
					ID:   input.TargetID,
				},
			},
			Limits: query.ContextLimits{
				MaxTokens:     input.MaxTokens,
				MaxItems:      input.MaxItems,
				MaxCharacters: input.MaxCharacters,
				MaxBytes:      input.MaxBytes,
			},
			Continuation: contextContinuation(input.Continuation),
		}
		packageContext, err := service.BuildContext(ctx, request)
		if err != nil {
			safeErr := sanitizeContextServiceError(ctx, err)
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationContext, request, started, err, safeErr)
		}
		return contextToolSuccess(ctx, packageContext, options, ContextAuditOperationContext, request, started)
	}
}

func impactToolHandler(service query.ContextService, options ContextServerOptions) mcp.ToolHandlerFor[contextTargetInput, contextToolOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input contextTargetInput) (*mcp.CallToolResult, contextToolOutput, error) {
		started := time.Now()
		if err := contextError(ctx); err != nil {
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationImpact, query.ContextRequest{}, started, err, err)
		}
		var targetKind query.IntentTargetKind
		switch input.TargetKind {
		case string(query.IntentTargetEntity):
			targetKind = query.IntentTargetEntity
		case string(query.IntentTargetSymbol):
			targetKind = query.IntentTargetSymbol
		default:
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationImpact, query.ContextRequest{}, started, query.ErrInvalidContextRequest, query.ErrInvalidContextRequest)
		}
		request := query.ContextRequest{
			Version: query.ContextVersion,
			Scope: query.Scope{
				OrganizationID: input.OrganizationID,
				SourceID:       input.SourceID,
				SnapshotID:     input.SnapshotID,
			},
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindPossibleImpact,
				Target: query.IntentTarget{
					Kind: targetKind,
					ID:   input.TargetID,
				},
			},
			Limits: query.ContextLimits{
				MaxTokens:     input.MaxTokens,
				MaxItems:      input.MaxItems,
				MaxCharacters: input.MaxCharacters,
				MaxBytes:      input.MaxBytes,
			},
			Continuation: contextContinuation(input.Continuation),
		}
		packageContext, err := service.BuildContext(ctx, request)
		if err != nil {
			safeErr := sanitizeContextServiceError(ctx, err)
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationImpact, request, started, err, safeErr)
		}
		return contextToolSuccess(ctx, packageContext, options, ContextAuditOperationImpact, request, started)
	}
}

func evidenceToolHandler(service query.ContextService, options ContextServerOptions) mcp.ToolHandlerFor[contextEvidenceInput, contextToolOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, input contextEvidenceInput) (*mcp.CallToolResult, contextToolOutput, error) {
		started := time.Now()
		if err := contextError(ctx); err != nil {
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationEvidence, query.ContextRequest{}, started, err, err)
		}
		request := query.ContextRequest{
			Version: query.ContextVersion,
			Scope: query.Scope{
				OrganizationID: input.OrganizationID,
				SourceID:       input.SourceID,
				SnapshotID:     input.SnapshotID,
			},
			Intent: query.Intent{
				Version: query.ContextVersion,
				Kind:    query.IntentKindEvidenceInspection,
				Target: query.IntentTarget{
					Kind: query.IntentTargetEvidence,
					ID:   input.EvidenceID,
				},
			},
			Limits: query.ContextLimits{
				MaxTokens:     input.MaxTokens,
				MaxItems:      input.MaxItems,
				MaxCharacters: input.MaxCharacters,
				MaxBytes:      input.MaxBytes,
			},
			Continuation: contextContinuation(input.Continuation),
		}
		packageContext, err := service.BuildContext(ctx, request)
		if err != nil {
			safeErr := sanitizeContextServiceError(ctx, err)
			return nil, contextToolOutput{}, contextToolError(ctx, options, ContextAuditOperationEvidence, request, started, err, safeErr)
		}
		return contextToolSuccess(ctx, packageContext, options, ContextAuditOperationEvidence, request, started)
	}
}

func contextContinuation(token string) *query.ContextContinuation {
	if token == "" {
		return nil
	}
	return &query.ContextContinuation{Token: token}
}

func contextToolSuccess(ctx context.Context, packageContext query.ContextPackage, options ContextServerOptions, operation ContextAuditOperation, request query.ContextRequest, started time.Time) (*mcp.CallToolResult, contextToolOutput, error) {
	output := contextToolOutput{
		Context:          packageContext,
		LatestSnapshotID: activeSnapshotID(ctx, options.ActiveSnapshotResolver, packageContext.Scope),
	}
	links, err := contextEvidenceResourceLinks(packageContext)
	if err != nil {
		return nil, contextToolOutput{}, contextToolError(ctx, options, operation, request, started, ErrContextServiceFailure, ErrContextServiceFailure)
	}
	if err := recordContextAudit(ctx, options.AuditSink, contextAuditRecordFor(operation, request.Scope, request.Limits, ContextAuditOutcomeSuccess, started, &packageContext)); err != nil {
		return nil, contextToolOutput{}, err
	}
	if len(links) == 0 {
		return nil, output, nil
	}
	return &mcp.CallToolResult{Content: links}, output, nil
}

func sanitizeContextServiceError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, query.ErrInvalidContextContinuation) || errors.Is(err, query.ErrInvalidContextContinuationKey) || errors.Is(err, ErrContextCursorRejected) {
		return ErrContextCursorRejected
	}
	if contextRequestRejectedError(err) {
		return ErrContextRequestRejected
	}
	if contextAuditUnavailableError(err) {
		return ErrContextUnavailable
	}
	return ErrContextServiceFailure
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func nilContextService(service query.ContextService) bool {
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

func readOnlyToolAnnotations() *mcp.ToolAnnotations {
	openWorld := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint:   true,
		IdempotentHint: true,
		OpenWorldHint:  &openWorld,
	}
}

func contextQuerySchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           contextQueryProperties(),
		Required:             []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "question"},
		AdditionalProperties: falseSchema(),
		PropertyOrder:        []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "question", "continuation"},
	}
}

func contextTargetSchema() *jsonschema.Schema {
	properties := contextBudgetProperties()
	properties["target_kind"] = &jsonschema.Schema{
		Type:        "string",
		Enum:        []any{"entity", "symbol"},
		MinLength:   jsonschema.Ptr(1),
		Description: "target kind",
	}
	properties["target_id"] = &jsonschema.Schema{Type: "string", MinLength: jsonschema.Ptr(1), Description: "target identity"}
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           properties,
		Required:             []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "target_kind", "target_id"},
		AdditionalProperties: falseSchema(),
		PropertyOrder:        []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "target_kind", "target_id", "continuation"},
	}
}

func contextEvidenceSchema() *jsonschema.Schema {
	properties := contextBudgetProperties()
	properties["evidence_id"] = &jsonschema.Schema{Type: "string", MinLength: jsonschema.Ptr(1), Description: "evidence identity"}
	return &jsonschema.Schema{
		Type:                 "object",
		Properties:           properties,
		Required:             []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "evidence_id"},
		AdditionalProperties: falseSchema(),
		PropertyOrder:        []string{"organization_id", "source_id", "snapshot_id", "max_tokens", "max_items", "max_characters", "max_bytes", "evidence_id", "continuation"},
	}
}

func contextQueryProperties() map[string]*jsonschema.Schema {
	properties := contextBudgetProperties()
	properties["question"] = &jsonschema.Schema{Type: "string", MinLength: jsonschema.Ptr(1), Description: "question"}
	properties["continuation"] = contextContinuationSchema()
	return properties
}

func contextBudgetProperties() map[string]*jsonschema.Schema {
	return map[string]*jsonschema.Schema{
		"organization_id": {Type: "string", MinLength: jsonschema.Ptr(1), Description: "organization scope"},
		"source_id":       {Type: "string", MinLength: jsonschema.Ptr(1), Description: "source scope"},
		"snapshot_id":     {Type: "string", MinLength: jsonschema.Ptr(1), Description: "snapshot scope"},
		"max_tokens":      positiveIntegerSchema("maximum estimated tokens"),
		"max_items":       positiveIntegerSchema("maximum selected items"),
		"max_characters":  positiveIntegerSchema("maximum returned characters"),
		"max_bytes":       positiveIntegerSchema("maximum returned bytes"),
		"continuation":    contextContinuationSchema(),
	}
}

func contextContinuationSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		MinLength:   jsonschema.Ptr(1),
		MaxLength:   jsonschema.Ptr(4 << 10),
		Description: "opaque continuation",
	}
}

func positiveIntegerSchema(description string) *jsonschema.Schema {
	return &jsonschema.Schema{Type: "integer", Minimum: jsonschema.Ptr(1.0), Description: description}
}

func falseSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Not: &jsonschema.Schema{}}
}
