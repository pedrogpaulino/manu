package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pedrogpaulino/manu/internal/persistence"
)

const (
	// HealthPath reports process liveness without checking external services.
	HealthPath = "/healthz"
	// ReadinessPath reports whether the configured persistence/schema boundary
	// is usable for API work.
	ReadinessPath = "/readyz"
)

var (
	// ErrReadinessNotConfigured is the safe default before a database/schema
	// checker is composed into the server.
	ErrReadinessNotConfigured = errors.New("api: readiness checker is not configured")
	// ErrReadinessDependency identifies an unavailable or failed dependency.
	ErrReadinessDependency = errors.New("api: readiness dependency unavailable")
	// ErrReadinessSchemaMissing identifies a database without the migration
	// history required by this binary.
	ErrReadinessSchemaMissing = errors.New("api: readiness schema is missing")
	// ErrReadinessSchemaIncomplete identifies a compatible database with
	// migrations that are not all applied.
	ErrReadinessSchemaIncomplete = errors.New("api: readiness schema is incomplete")
	// ErrReadinessSchemaIncompatible identifies an ahead, gapped, renamed, or
	// checksum-incompatible migration history.
	ErrReadinessSchemaIncompatible = errors.New("api: readiness schema is incompatible")
)

// ReadinessChecker is the only dependency required by /readyz. Implementations
// must inspect local state through ctx and must not call an AI or other remote
// provider. A nil error means ready; any error keeps the process not ready.
type ReadinessChecker interface {
	Check(context.Context) error
}

// ReadinessFunc adapts a function to ReadinessChecker for tests and small
// composition seams.
type ReadinessFunc func(context.Context) error

// Check implements ReadinessChecker.
func (f ReadinessFunc) Check(ctx context.Context) error {
	if f == nil {
		return ErrReadinessNotConfigured
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return f(ctx)
}

// MigrationStatusReader is the minimal status seam used by the optional
// migration adapter. persistence.Runner satisfies it through Status.
type MigrationStatusReader interface {
	Status(context.Context) (persistence.Status, error)
}

// MigrationReadiness adapts migration compatibility inspection to the API
// readiness boundary. It never applies migrations or opens a connection.
type MigrationReadiness struct {
	reader MigrationStatusReader
}

// NewMigrationReadiness returns a checker backed by a migration status reader.
// A nil reader intentionally remains not configured.
func NewMigrationReadiness(reader MigrationStatusReader) *MigrationReadiness {
	return &MigrationReadiness{reader: reader}
}

// Check inspects migration state and translates persistence details to safe,
// stable readiness categories.
func (m *MigrationReadiness) Check(ctx context.Context) error {
	if m == nil || m.reader == nil {
		return ErrReadinessNotConfigured
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	status, err := m.reader.Status(ctx)
	if err != nil {
		return classifyMigrationReadinessError(err)
	}
	if !status.Ready {
		return ErrReadinessSchemaIncomplete
	}
	return nil
}

// HealthResponse is the versioned JSON envelope used by liveness and
// readiness. Message and Code are fixed safe values; no dependency error is
// serialized.
type HealthResponse struct {
	Version   string `json:"version"`
	Status    string `json:"status"`
	Code      string `json:"code"`
	Message   string `json:"message,omitempty"`
	RequestID string `json:"request_id"`
}

func writeLiveness(w http.ResponseWriter, requestID string) {
	writeHealth(w, http.StatusOK, HealthResponse{
		Version:   Version,
		Status:    "ok",
		Code:      "alive",
		Message:   "process is alive",
		RequestID: requestID,
	})
}

func writeReadiness(w http.ResponseWriter, ctx context.Context, checker ReadinessChecker, requestID string) {
	err := ErrReadinessNotConfigured
	if checker != nil {
		err = checker.Check(ctx)
	}
	if err == nil && ctx != nil {
		err = ctx.Err()
	}
	if err == nil {
		writeHealth(w, http.StatusOK, HealthResponse{
			Version:   Version,
			Status:    "ready",
			Code:      "ready",
			Message:   "service is ready",
			RequestID: requestID,
		})
		return
	}

	code, message := readinessDiagnostic(err)
	writeHealth(w, http.StatusServiceUnavailable, HealthResponse{
		Version:   Version,
		Status:    "not_ready",
		Code:      code,
		Message:   message,
		RequestID: requestID,
	})
}

func writeHealth(w http.ResponseWriter, status int, response HealthResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func readinessDiagnostic(err error) (string, string) {
	switch {
	case err == nil:
		return "ready", "service is ready"
	case errors.Is(err, context.Canceled):
		return "readiness_canceled", "readiness check canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "readiness_timeout", "readiness check timed out"
	case errors.Is(err, ErrReadinessNotConfigured):
		return "readiness_not_configured", "readiness checker is not configured"
	case errors.Is(err, ErrReadinessSchemaMissing):
		return "schema_missing", "required schema is missing"
	case errors.Is(err, ErrReadinessSchemaIncomplete):
		return "schema_incomplete", "required schema is incomplete"
	case errors.Is(err, ErrReadinessSchemaIncompatible):
		return "schema_incompatible", "schema is incompatible with this binary"
	case errors.Is(err, ErrReadinessDependency):
		return "dependency_unavailable", "readiness dependency is unavailable"
	case errors.Is(err, persistence.ErrMigrationSchemaMissing):
		return "schema_missing", "required schema is missing"
	case errors.Is(err, persistence.ErrSchemaAhead),
		errors.Is(err, persistence.ErrMigrationGap),
		errors.Is(err, persistence.ErrMigrationNameMismatch),
		errors.Is(err, persistence.ErrMigrationChecksumMismatch),
		errors.Is(err, persistence.ErrInvalidMigrationCatalog):
		return "schema_incompatible", "schema is incompatible with this binary"
	case errors.Is(err, persistence.ErrMigrationDatabase),
		errors.Is(err, persistence.ErrMigrationLock):
		return "dependency_unavailable", "readiness dependency is unavailable"
	default:
		return "readiness_unavailable", "readiness could not be established"
	}
}

func classifyMigrationReadinessError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, persistence.ErrMigrationSchemaMissing):
		return ErrReadinessSchemaMissing
	case errors.Is(err, persistence.ErrSchemaAhead),
		errors.Is(err, persistence.ErrMigrationGap),
		errors.Is(err, persistence.ErrMigrationNameMismatch),
		errors.Is(err, persistence.ErrMigrationChecksumMismatch),
		errors.Is(err, persistence.ErrInvalidMigrationCatalog):
		return ErrReadinessSchemaIncompatible
	case errors.Is(err, persistence.ErrMigrationDatabase),
		errors.Is(err, persistence.ErrMigrationLock):
		return ErrReadinessDependency
	default:
		return ErrReadinessDependency
	}
}
