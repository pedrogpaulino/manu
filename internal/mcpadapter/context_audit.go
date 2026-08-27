package mcpadapter

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/query"
)

const ContextAuditVersion = "v1alpha1"

var (
	// ErrContextAuditFailure is returned when an audit record cannot be
	// accepted. Successful content is never delivered after this failure.
	ErrContextAuditFailure = errors.New("mcpadapter: context audit unavailable")
	// ErrContextCursorRejected is the opaque MCP result for an invalid or
	// incompatible continuation.
	ErrContextCursorRejected = errors.New("mcpadapter: cursor rejected")
	// ErrContextRequestRejected is the opaque MCP result for invalid request
	// scope, budget or identity material.
	ErrContextRequestRejected = errors.New("mcpadapter: request rejected")
	// ErrContextUnavailable is the opaque MCP result for unavailable context
	// stages such as snapshot, retrieval or composition.
	ErrContextUnavailable = errors.New("mcpadapter: context unavailable")
)

// ContextAuditOperation identifies the small, closed MCP audit surface.
type ContextAuditOperation string

const (
	ContextAuditOperationQuery            ContextAuditOperation = "manu_query"
	ContextAuditOperationContext          ContextAuditOperation = "manu_context"
	ContextAuditOperationImpact           ContextAuditOperation = "manu_impact"
	ContextAuditOperationEvidence         ContextAuditOperation = "manu_evidence"
	ContextAuditOperationEvidenceResource ContextAuditOperation = "manu_evidence_resource"
)

// ContextAuditOutcome identifies the terminal result without carrying
// diagnostics or service error text.
type ContextAuditOutcome string

const (
	ContextAuditOutcomeSuccess   ContextAuditOutcome = "success"
	ContextAuditOutcomeRejected  ContextAuditOutcome = "rejected"
	ContextAuditOutcomeFailed    ContextAuditOutcome = "failed"
	ContextAuditOutcomeCancelled ContextAuditOutcome = "cancelled"
)

// ContextAuditRecord is the bounded, content-free record emitted for one MCP
// operation. IDs are copied at construction and again before sink delivery.
type ContextAuditRecord struct {
	Version          string                `json:"version"`
	Operation        ContextAuditOperation `json:"operation"`
	Scope            query.Scope           `json:"scope"`
	Budget           query.ContextLimits   `json:"budget"`
	Outcome          ContextAuditOutcome   `json:"outcome"`
	Duration         time.Duration         `json:"duration"`
	SnapshotRevision string                `json:"snapshot_revision,omitempty"`
	Truncated        bool                  `json:"truncated"`
	ItemIDs          []string              `json:"item_ids,omitempty"`
	RelationIDs      []string              `json:"relation_ids,omitempty"`
}

// Validate checks the versioned shape and closed vocabularies. Rejected,
// failed and cancelled operations may carry zero scope or budget when parsing
// failed before either value could be trusted.
func (r ContextAuditRecord) Validate() error {
	if r.Version != ContextAuditVersion || !validContextAuditOperation(r.Operation) || !validContextAuditOutcome(r.Outcome) || r.Duration < 0 {
		return ErrContextAuditFailure
	}
	if err := r.Scope.Validate(); err != nil && r.Scope != (query.Scope{}) {
		return ErrContextAuditFailure
	}
	if err := r.Budget.Validate(); err != nil && r.Budget != (query.ContextLimits{}) {
		return ErrContextAuditFailure
	}
	if r.Outcome == ContextAuditOutcomeSuccess {
		if err := r.Scope.Validate(); err != nil {
			return ErrContextAuditFailure
		}
		if err := r.Budget.Validate(); err != nil {
			return ErrContextAuditFailure
		}
		if !validContextAuditString(r.SnapshotRevision) {
			return ErrContextAuditFailure
		}
	} else if r.SnapshotRevision != "" || r.Truncated || len(r.ItemIDs) != 0 || len(r.RelationIDs) != 0 {
		return ErrContextAuditFailure
	}
	if !validContextAuditIDs(r.ItemIDs) || !validContextAuditIDs(r.RelationIDs) {
		return ErrContextAuditFailure
	}
	return nil
}

// Clone returns a detached audit record suitable for sink delivery.
func (r ContextAuditRecord) Clone() ContextAuditRecord {
	r.ItemIDs = append([]string(nil), r.ItemIDs...)
	r.RelationIDs = append([]string(nil), r.RelationIDs...)
	return r
}

// ContextAuditSink receives one validated record synchronously. Implementors
// must not expect source payload, query text, continuation tokens or errors.
type ContextAuditSink interface {
	RecordContextAudit(context.Context, ContextAuditRecord) error
}

// ContextAuditSinkFunc adapts a function to ContextAuditSink.
type ContextAuditSinkFunc func(context.Context, ContextAuditRecord) error

// RecordContextAudit implements ContextAuditSink.
func (f ContextAuditSinkFunc) RecordContextAudit(ctx context.Context, record ContextAuditRecord) error {
	if f == nil {
		return nil
	}
	return f(ctx, record)
}

func validContextAuditOperation(operation ContextAuditOperation) bool {
	switch operation {
	case ContextAuditOperationQuery, ContextAuditOperationContext, ContextAuditOperationImpact,
		ContextAuditOperationEvidence, ContextAuditOperationEvidenceResource:
		return true
	default:
		return false
	}
}

func validContextAuditOutcome(outcome ContextAuditOutcome) bool {
	switch outcome {
	case ContextAuditOutcomeSuccess, ContextAuditOutcomeRejected, ContextAuditOutcomeFailed, ContextAuditOutcomeCancelled:
		return true
	default:
		return false
	}
}

func validContextAuditString(value string) bool {
	return value != "" && int64(len([]byte(value))) <= 256 && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\t")
}

func validContextAuditIDs(values []string) bool {
	if len(values) > 10_000 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validContextAuditString(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		for _, character := range value {
			if unicode.IsSpace(character) || unicode.IsControl(character) {
				return false
			}
		}
		seen[value] = struct{}{}
	}
	return true
}

func recordContextAudit(ctx context.Context, sink ContextAuditSink, record ContextAuditRecord) error {
	if nilContextAuditSink(sink) {
		return nil
	}
	if err := record.Validate(); err != nil {
		return ErrContextAuditFailure
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	if err := sink.RecordContextAudit(ctx, record.Clone()); err != nil {
		return ErrContextAuditFailure
	}
	return nil
}

func nilContextAuditSink(sink ContextAuditSink) bool {
	if sink == nil {
		return true
	}
	value := reflect.ValueOf(sink)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func contextAuditRecordFor(
	operation ContextAuditOperation,
	scope query.Scope,
	budget query.ContextLimits,
	outcome ContextAuditOutcome,
	started time.Time,
	packageContext *query.ContextPackage,
) ContextAuditRecord {
	record := ContextAuditRecord{
		Version:   ContextAuditVersion,
		Operation: operation,
		Scope:     normalizedContextAuditScope(scope),
		Budget:    normalizedContextAuditBudget(budget),
		Outcome:   outcome,
		Duration:  time.Since(started),
	}
	if outcome == ContextAuditOutcomeSuccess && packageContext != nil {
		record.SnapshotRevision = packageContext.Revision
		record.Truncated = packageContext.Truncated
		record.ItemIDs = make([]string, 0, len(packageContext.Items))
		for _, item := range packageContext.Items {
			record.ItemIDs = append(record.ItemIDs, item.ID)
		}
		record.RelationIDs = make([]string, 0, len(packageContext.Relations))
		for _, relation := range packageContext.Relations {
			record.RelationIDs = append(record.RelationIDs, relation.ID)
		}
	}
	return record
}

func contextToolError(
	ctx context.Context,
	options ContextServerOptions,
	operation ContextAuditOperation,
	request query.ContextRequest,
	started time.Time,
	originalErr error,
	safeErr error,
) error {
	record := contextAuditRecordFor(operation, request.Scope, request.Limits, contextAuditOutcomeForError(ctx, originalErr), started, nil)
	if err := recordContextAudit(ctx, options.AuditSink, record); err != nil {
		return err
	}
	return safeErr
}

func normalizedContextAuditScope(scope query.Scope) query.Scope {
	if scope.Validate() != nil {
		return query.Scope{}
	}
	return scope
}

func normalizedContextAuditBudget(budget query.ContextLimits) query.ContextLimits {
	if budget.Validate() != nil {
		return query.ContextLimits{}
	}
	return budget
}

func contextAuditOutcomeForError(ctx context.Context, err error) ContextAuditOutcome {
	if ctx != nil {
		if ctx.Err() != nil {
			return ContextAuditOutcomeCancelled
		}
	}
	if err == nil {
		return ContextAuditOutcomeSuccess
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ContextAuditOutcomeCancelled
	}
	if errors.Is(err, query.ErrInvalidContextContinuation) || errors.Is(err, query.ErrInvalidContextContinuationKey) || errors.Is(err, ErrContextCursorRejected) || errors.Is(err, ErrInvalidContextResource) {
		return ContextAuditOutcomeRejected
	}
	if contextRequestRejectedError(err) {
		return ContextAuditOutcomeRejected
	}
	return ContextAuditOutcomeFailed
}

func contextRequestRejectedError(err error) bool {
	return errors.Is(err, query.ErrInvalidContext) ||
		errors.Is(err, query.ErrUnsupportedContextVersion) ||
		errors.Is(err, query.ErrInvalidContextRequest) ||
		errors.Is(err, query.ErrInvalidContextScope) ||
		errors.Is(err, query.ErrInvalidContextBudget) ||
		errors.Is(err, query.ErrInvalidContextReference) ||
		errors.Is(err, query.ErrInvalidContextPackage) ||
		errors.Is(err, query.ErrContextSnapshotScopeMismatch) ||
		errors.Is(err, ErrContextRequestRejected)
}

func contextAuditUnavailableError(err error) bool {
	return errors.Is(err, query.ErrInvalidContextSnapshot) ||
		errors.Is(err, query.ErrContextServiceSnapshot) ||
		errors.Is(err, query.ErrContextServiceRetrieval) ||
		errors.Is(err, query.ErrContextServiceComposition) ||
		errors.Is(err, ErrContextUnavailable)
}
