package query

import (
	"context"
	"encoding/json"
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

// ExecutionState is the durable lifecycle of a query execution. A query
// service must persist the terminal or in-progress state before returning it
// to an HTTP caller.
type ExecutionState string

const (
	ExecutionStatePending   ExecutionState = "pending"
	ExecutionStateRunning   ExecutionState = "running"
	ExecutionStateCompleted ExecutionState = "completed"
	ExecutionStatePartial   ExecutionState = "partial"
	ExecutionStateFailed    ExecutionState = "failed"
	ExecutionStateAbstained ExecutionState = "abstained"
)

// Valid reports whether the state belongs to the persisted query vocabulary.
func (s ExecutionState) Valid() bool {
	switch s {
	case ExecutionStatePending, ExecutionStateRunning, ExecutionStateCompleted,
		ExecutionStatePartial, ExecutionStateFailed, ExecutionStateAbstained:
		return true
	default:
		return false
	}
}

// Terminal reports whether an execution has finished and can be returned by
// the synchronous create operation.
func (s ExecutionState) Terminal() bool {
	switch s {
	case ExecutionStateCompleted, ExecutionStatePartial, ExecutionStateFailed, ExecutionStateAbstained:
		return true
	default:
		return false
	}
}

// ExecutionInput is the provider-independent input to one query execution.
// Organization is intentionally supplied separately by the configured
// installation boundary.
type ExecutionInput struct {
	Question string
	// QuestionKind is supplied by the caller and is never inferred from the
	// natural-language question. The abstention gate uses this typed value to
	// keep inventory, flow, execution and business-intent conclusions distinct.
	QuestionKind   KnowledgeQuestionKind
	SourceID       string
	SnapshotID     string
	QuestionDigest string
}

// Execution is the persisted query status returned by the application port.
// Response is optional and must already be bounded by the complete query
// pipeline when a later implementation supplies one.
type Execution struct {
	ID             string
	OrganizationID string
	SourceID       string
	SnapshotID     string
	State          ExecutionState
	QuestionDigest string
	PackageDigest  string
	Response       json.RawMessage
	DiagnosticCode string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// ExecutionService is the small application port consumed by HTTP. Create
// MUST durably persist the execution before returning; Get MUST apply the
// supplied organization scope to every read.
type ExecutionService interface {
	Create(context.Context, string, ExecutionInput) (Execution, error)
	Get(context.Context, string, string) (Execution, error)
}

// EvidenceInspection is the bounded local inspection returned by the
// organization-scoped evidence reader. It deliberately carries no raw
// provider or database diagnostic.
type EvidenceInspection struct {
	ID                string
	OrganizationID    string
	SourceID          string
	SnapshotID        string
	ArtifactID        string
	ObservationID     string
	Locator           contract.Locator
	ContentState      evidence.ContentState
	Classification    evidence.Classification
	Persist           evidence.Decision
	ExternalTransfer  evidence.Decision
	Content           string
	ContentHash       string
	ContentBytes      int64
	ContentCharacters int64
	Truncated         bool
}

// EvidenceReader reads one persisted Evidence Unit in a fixed organization
// scope. It never grants access to the original source filesystem.
type EvidenceReader interface {
	GetEvidence(context.Context, string, string) (EvidenceInspection, error)
}
