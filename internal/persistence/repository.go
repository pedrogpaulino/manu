package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

var (
	// ErrInvalidInput identifies a value that cannot safely cross the
	// persistence boundary.
	ErrInvalidInput = errors.New("persistence: invalid input")
	// ErrNotFound identifies a required organization-scoped row that is absent.
	ErrNotFound = errors.New("persistence: not found")
	// ErrConflict identifies an existing row with the same identity but a
	// different immutable value.
	ErrConflict = errors.New("persistence: conflict")
	// ErrIncompatibleSnapshot identifies a snapshot identity reused for a
	// different factual or analysis configuration.
	ErrIncompatibleSnapshot = errors.New("persistence: incompatible snapshot")
	// ErrInconsistent identifies impossible cardinality or state in the
	// canonical schema.
	ErrInconsistent = errors.New("persistence: inconsistent data")
	// ErrDatabase identifies a database failure without exposing driver
	// diagnostics that may contain row values or other sensitive details.
	ErrDatabase = errors.New("persistence: database operation failed")
)

const rollbackTimeout = 5 * time.Second

// transaction is the small portion of pgx.Tx used by this package. Keeping
// the interface private lets deterministic tests provide a fake without
// imposing a database abstraction on callers. The production adapter below
// still starts transactions from *pgxpool.Pool.
type transaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Commit(context.Context) error
	Rollback(context.Context) error
}

type transactionStarter interface {
	Begin(context.Context) (transaction, error)
}

type poolTransactionStarter struct {
	pool *pgxpool.Pool
}

func (s poolTransactionStarter) Begin(ctx context.Context) (transaction, error) {
	if s.pool == nil {
		return nil, fmt.Errorf("%w: nil postgres pool", ErrInvalidInput)
	}
	return s.pool.Begin(ctx)
}

// Repository is the PostgreSQL adapter for canonical persistence. It does not
// retain request context or mutable transaction state; every operation gets
// its context explicitly and starts its own transaction unless it is invoked
// on a UnitOfWork.
type Repository struct {
	starter transactionStarter
}

// NewRepository creates a repository backed by the supplied PostgreSQL pool.
// The pool is not opened, configured, or pinged here.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{starter: poolTransactionStarter{pool: pool}}
}

func newRepositoryWithStarter(starter transactionStarter) *Repository {
	return &Repository{starter: starter}
}

// UnitOfWork groups canonical writes in one caller-owned transaction. Use it
// through Repository.WithinTx so commit and rollback remain centralized.
type UnitOfWork struct {
	tx transaction
}

// WithinTx begins a transaction, invokes fn, and commits only when fn and the
// caller context both succeed. Errors from fn are returned without exposing
// SQL arguments or persisted content. Rollback uses a bounded cleanup context
// so cancellation does not leave a connection in an open transaction.
func (r *Repository) WithinTx(ctx context.Context, fn func(*UnitOfWork) error) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if r == nil || r.starter == nil {
		return fmt.Errorf("%w: repository is not configured", ErrInvalidInput)
	}
	if fn == nil {
		return fmt.Errorf("%w: transaction callback is nil", ErrInvalidInput)
	}

	tx, err := r.starter.Begin(ctx)
	if err != nil {
		return wrapPersistenceError(ctx, "begin transaction", err)
	}
	if tx == nil {
		return fmt.Errorf("%w: transaction is nil", ErrInconsistent)
	}

	if err := fn(&UnitOfWork{tx: tx}); err != nil {
		return rollbackAfterError(ctx, tx, wrapPersistenceError(ctx, "transaction callback", err))
	}
	if err := ctx.Err(); err != nil {
		return rollbackAfterError(ctx, tx, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return rollbackAfterError(ctx, tx, wrapPersistenceError(ctx, "commit transaction", err))
	}
	return nil
}

func rollbackAfterError(ctx context.Context, tx transaction, operationErr error) error {
	cleanupCtx, cancel := rollbackContext(ctx)
	defer cancel()
	if rollbackErr := tx.Rollback(cleanupCtx); rollbackErr != nil {
		return errors.Join(operationErr, wrapPersistenceError(cleanupCtx, "rollback transaction", rollbackErr))
	}
	return operationErr
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidInput)
	}
	return ctx.Err()
}

func rollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	cleanup := context.WithoutCancel(ctx)
	return context.WithTimeout(cleanup, rollbackTimeout)
}

// Organization is the non-secret identity stored for one organization. Its
// ID is supplied separately to every repository operation; ID is optional
// here and, when present, must match that explicit scope.
type Organization struct {
	ID         string
	ExternalID string
	Name       string
}

// Source is a canonical source identity. ExternalID is intentionally separate
// from the UUID used by relational references.
type Source struct {
	ID         string
	ExternalID string
	Name       string
	Type       string
	Root       string
}

// Snapshot is insert-only. Revision/hash, configuration and factual digest
// are compared when an existing snapshot ID is retried.
type Snapshot struct {
	ID                      string
	SourceID                string
	ExternalID              string
	Revision                string
	Hash                    string
	AnalysisConfigurationID string
	FactualDigest           string
	CapturedAt              time.Time
}

// Artifact contains identity and bounded metadata, never source contents.
type Artifact struct {
	ID          string
	SourceID    string
	SnapshotID  string
	ExternalID  string
	Path        string
	Type        string
	ContentHash string
	ContentSize int64
}

// Observation is a canonical contribution produced for one artifact.
type Observation struct {
	ID              string
	SourceID        string
	SnapshotID      string
	ArtifactID      string
	ExternalID      string
	AnalyzerID      string
	AnalyzerVersion string
	Method          string
	Type            string
	Locator         contract.Locator
	Value           json.RawMessage
	ObservedAt      time.Time
}

// Entity is a snapshot-scoped canonical entity. Attributes are structured
// JSON, not an unbounded source document.
type Entity struct {
	ID         string
	SourceID   string
	SnapshotID string
	ExternalID string
	Type       string
	Name       string
	Attributes json.RawMessage
}

// Relationship is a directed snapshot-scoped edge. Self-relations remain
// valid because recursion and self-calls are meaningful domain facts.
type Relationship struct {
	ID           string
	SourceID     string
	SnapshotID   string
	ExternalID   string
	FromEntityID string
	ToEntityID   string
	Type         string
	Attributes   json.RawMessage
}

// Evidence is the prepared representation persisted for one Evidence Unit.
// Provenance is structured metadata supplied by the analyzer; source files
// and credentials are never accepted as a separate persistence field.
type Evidence struct {
	// ID and the relational IDs are canonical UUIDs supplied by the caller.
	// The corresponding Unit fields remain external identities from the Agent
	// contract and are stored as external_id/provenance, never cast to UUID.
	ID                     string
	OrganizationID         string
	SourceID               string
	SnapshotID             string
	ArtifactID             string
	ObservationID          string
	OrganizationExternalID string
	SourceExternalID       string
	SnapshotExternalID     string
	ArtifactExternalID     string
	ObservationExternalID  string
	Unit                   evidence.EvidenceUnit
	Provenance             json.RawMessage
}

// Coverage is a canonical coverage row plus its external contract identity.
type Coverage struct {
	ID         string
	SourceID   string
	SnapshotID string
	Value      contract.Coverage
}

// Gap is a canonical gap row plus its external contract identity.
type Gap struct {
	ID         string
	SourceID   string
	SnapshotID string
	Value      contract.Gap
}

// Failure is a canonical failure row plus its external contract identity.
type Failure struct {
	ID                 string
	SourceID           string
	SnapshotID         string
	ArtifactID         string // optional canonical artifact UUID
	ArtifactExternalID string // optional contract artifact identity
	Value              contract.Failure
}

// FactualIdentity identifies a reusable fact in one source/snapshot.
type FactualIdentity struct {
	ID            string
	SourceID      string
	SnapshotID    string
	IdentityKey   string
	FactualDigest string
	State         string
	ObservedAt    time.Time
}

func (r *Repository) EnsureOrganization(ctx context.Context, organizationID string, organization Organization) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.EnsureOrganization(ctx, organizationID, organization)
	})
}

func (r *Repository) InsertSource(ctx context.Context, organizationID string, source Source) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertSource(ctx, organizationID, source)
	})
}

func (r *Repository) InsertSnapshot(ctx context.Context, organizationID string, snapshot Snapshot) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertSnapshot(ctx, organizationID, snapshot)
	})
}

func (r *Repository) InsertArtifact(ctx context.Context, organizationID string, artifact Artifact) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertArtifact(ctx, organizationID, artifact)
	})
}

func (r *Repository) InsertObservation(ctx context.Context, organizationID string, observation Observation) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertObservation(ctx, organizationID, observation)
	})
}

func (r *Repository) InsertEntity(ctx context.Context, organizationID string, entity Entity) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertEntity(ctx, organizationID, entity)
	})
}

func (r *Repository) InsertRelationship(ctx context.Context, organizationID string, relationship Relationship) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertRelationship(ctx, organizationID, relationship)
	})
}

func (r *Repository) InsertEvidence(ctx context.Context, organizationID string, item Evidence) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertEvidence(ctx, organizationID, item)
	})
}

func (r *Repository) InsertCoverage(ctx context.Context, organizationID string, coverage Coverage) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertCoverage(ctx, organizationID, coverage)
	})
}

func (r *Repository) InsertGap(ctx context.Context, organizationID string, gap Gap) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertGap(ctx, organizationID, gap)
	})
}

func (r *Repository) InsertFailure(ctx context.Context, organizationID string, failure Failure) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertFailure(ctx, organizationID, failure)
	})
}

func (r *Repository) InsertFactualIdentity(ctx context.Context, organizationID string, identity FactualIdentity) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.InsertFactualIdentity(ctx, organizationID, identity)
	})
}

func (r *Repository) ActivateSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string) error {
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		return u.ActivateSnapshot(ctx, organizationID, sourceID, snapshotID)
	})
}

func validateUUID(field, value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return fmt.Errorf("%w: %s must be a uuid", ErrInvalidInput, field)
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !isHex(r) {
			return fmt.Errorf("%w: %s must be a uuid", ErrInvalidInput, field)
		}
	}
	return nil
}

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

func validateText(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidInput, field)
	}
	for _, r := range value {
		if r == '\x00' || (r < ' ' && r != '\t') {
			return fmt.Errorf("%w: %s contains control characters", ErrInvalidInput, field)
		}
	}
	return nil
}

func validateOptionalText(field, value string) error {
	if value == "" {
		return nil
	}
	return validateText(field, value)
}

func validateDigest(field, value string) error {
	if len(value) != 64 {
		return fmt.Errorf("%w: %s must be sha-256", ErrInvalidInput, field)
	}
	for _, r := range value {
		if !isHex(r) || (r >= 'A' && r <= 'F') {
			return fmt.Errorf("%w: %s must be lowercase sha-256", ErrInvalidInput, field)
		}
	}
	return nil
}

func validateOrganizationScope(organizationID, embeddedID string) error {
	if err := validateUUID("organization_id", organizationID); err != nil {
		return err
	}
	if embeddedID != "" {
		if err := validateUUID("organization_id", embeddedID); err != nil {
			return err
		}
		if embeddedID != organizationID {
			return fmt.Errorf("%w: organization scope mismatch", ErrInvalidInput)
		}
	}
	return nil
}

func validateJSONObject(field string, raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return []byte("{}"), nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' || !json.Valid([]byte(trimmed)) {
		return nil, fmt.Errorf("%w: %s must be a json object", ErrInvalidInput, field)
	}
	return []byte(trimmed), nil
}

func normalizedJSONObject(raw json.RawMessage) []byte {
	if len(raw) == 0 {
		return []byte("{}")
	}
	return []byte(strings.TrimSpace(string(raw)))
}

func jsonEqual(left, right []byte) bool {
	left = bytes.TrimSpace(left)
	right = bytes.TrimSpace(right)
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	canonicalLeft, err := json.Marshal(leftValue)
	if err != nil {
		return false
	}
	canonicalRight, err := json.Marshal(rightValue)
	if err != nil {
		return false
	}
	return bytes.Equal(canonicalLeft, canonicalRight)
}

func validateJSON(field string, raw json.RawMessage) error {
	if len(raw) != 0 && !json.Valid(raw) {
		return fmt.Errorf("%w: %s must be valid json", ErrInvalidInput, field)
	}
	return nil
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func marshalLocator(locator contract.Locator) ([]byte, error) {
	if err := locator.Validate(); err != nil {
		return nil, fmt.Errorf("%w: locator is invalid", ErrInvalidInput)
	}
	value, err := json.Marshal(locator)
	if err != nil {
		return nil, fmt.Errorf("%w: locator cannot be encoded", ErrInvalidInput)
	}
	return value, nil
}

func marshalOptionalLocator(locator *contract.Locator) ([]byte, error) {
	if locator == nil {
		return nil, nil
	}
	return marshalLocator(*locator)
}

func checkedRowsAffected(tag pgconn.CommandTag, want int64, operation string) error {
	if tag.RowsAffected() != want {
		return fmt.Errorf("%w: %s affected %d rows", ErrInconsistent, operation, tag.RowsAffected())
	}
	return nil
}

func (u *UnitOfWork) execInsert(ctx context.Context, query, operation string, args ...any) error {
	_, err := u.execInsertTag(ctx, query, operation, args...)
	return err
}

func (u *UnitOfWork) execInsertTag(ctx context.Context, query, operation string, args ...any) (pgconn.CommandTag, error) {
	if u == nil || u.tx == nil {
		return pgconn.CommandTag{}, fmt.Errorf("%w: unit of work is not configured", ErrInvalidInput)
	}
	if err := validateContext(ctx); err != nil {
		return pgconn.CommandTag{}, err
	}
	tag, err := u.tx.Exec(ctx, query, args...)
	if err != nil {
		return pgconn.CommandTag{}, wrapPersistenceError(ctx, operation, err)
	}
	// ON CONFLICT DO NOTHING is deliberate for safe retries. The canonical
	// identity remains scoped by organization and database constraints reject
	// conflicting non-identity uniqueness keys.
	if tag.RowsAffected() > 1 {
		return pgconn.CommandTag{}, fmt.Errorf("%w: %s affected too many rows", ErrInconsistent, operation)
	}
	return tag, nil
}

func wrapPersistenceError(ctx context.Context, operation string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return &redactedPersistenceError{
			operation: operation,
			category:  persistenceErrorCategory(err),
			cause:     err,
		}
	}
	if pgErr.Code == "57014" && ctx != nil && ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	switch pgErr.Code {
	case "23505":
		return fmt.Errorf("%w: %s", ErrConflict, operation)
	case "23502", "23503", "23514":
		if pgErr.Code == "23503" {
			return fmt.Errorf("%w: %s", ErrNotFound, operation)
		}
		return fmt.Errorf("%w: %s", ErrInvalidInput, operation)
	default:
		return fmt.Errorf("%w: %s", ErrDatabase, operation)
	}
}

// redactedPersistenceError preserves a safe category and programmatic
// errors.Is matching without exposing a driver, source, or callback message
// through Error or errors.Unwrap. The original cause is retained only for the
// private Is method; callers and loggers cannot traverse it.
type redactedPersistenceError struct {
	operation string
	category  error
	cause     error
}

func (e *redactedPersistenceError) Error() string {
	if e == nil {
		return ErrDatabase.Error()
	}
	category := e.category
	if category == nil {
		category = ErrDatabase
	}
	return fmt.Sprintf("%s: %s", category, e.operation)
}

func (e *redactedPersistenceError) Unwrap() error {
	if e == nil || e.category == nil {
		return ErrDatabase
	}
	return e.category
}

func (e *redactedPersistenceError) Is(target error) bool {
	if e == nil {
		return false
	}
	if e.category != nil && errors.Is(e.category, target) {
		return true
	}
	// Preserve programmatic matching for callers that supplied a typed or
	// sentinel callback error, while deliberately avoiding Unwrap(cause).
	return e.cause != nil && errors.Is(e.cause, target)
}

func persistenceErrorCategory(err error) error {
	switch {
	case errors.Is(err, ErrInvalidInput):
		return ErrInvalidInput
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	case errors.Is(err, ErrConflict):
		return ErrConflict
	case errors.Is(err, ErrIncompatibleSnapshot):
		return ErrIncompatibleSnapshot
	case errors.Is(err, ErrInconsistent):
		return ErrInconsistent
	case errors.Is(err, ErrDatabase):
		return ErrDatabase
	default:
		return ErrDatabase
	}
}

func (u *UnitOfWork) existingSnapshotMatches(ctx context.Context, organizationID string, snapshot Snapshot) error {
	var (
		externalID, configurationID, factualDigest string
		revision, sourceHash                       *string
		capturedAt                                 time.Time
	)
	err := u.tx.QueryRow(ctx, selectSnapshotIdentitySQL, organizationID, snapshot.SourceID, snapshot.ID).Scan(
		&externalID, &revision, &sourceHash, &configurationID, &factualDigest, &capturedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: snapshot identity", ErrIncompatibleSnapshot)
		}
		return wrapPersistenceError(ctx, "read snapshot identity", err)
	}
	if externalID != snapshot.ExternalID || optionalString(revision) != snapshot.Revision ||
		optionalString(sourceHash) != snapshot.Hash || configurationID != snapshot.AnalysisConfigurationID ||
		factualDigest != snapshot.FactualDigest || !capturedAt.Equal(snapshot.CapturedAt) {
		return ErrIncompatibleSnapshot
	}
	return nil
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
