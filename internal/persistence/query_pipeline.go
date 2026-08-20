package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// PipelineQueryRepository is the durable adapter used by the complete query
// application. It extends the minimal 5.3 execution repository with one
// transaction that records candidates, package decisions, generated claims,
// citations and safe provider-call telemetry before returning a terminal
// execution.
type PipelineQueryRepository struct {
	repository *Repository
	now        func() time.Time
	newID      func() (string, error)
}

// NewPipelineQueryRepository constructs the complete PostgreSQL query store.
// It does not open or ping the supplied pool.
func NewPipelineQueryRepository(pool *pgxpool.Pool) *PipelineQueryRepository {
	var repository *Repository
	if pool != nil {
		repository = NewRepository(pool)
	}
	return &PipelineQueryRepository{repository: repository, now: time.Now, newID: randomQueryUUID}
}

func newPipelineQueryRepositoryWithStarter(starter transactionStarter) *PipelineQueryRepository {
	return &PipelineQueryRepository{repository: newRepositoryWithStarter(starter), now: time.Now, newID: randomQueryUUID}
}

var _ query.QueryRunStore = (*PipelineQueryRepository)(nil)
var _ query.ActiveScopeResolver = (*PipelineQueryRepository)(nil)

const (
	selectActiveSourceScopeSQL = `
SELECT id::text, active_snapshot_id::text
FROM sources
WHERE organization_id = $1::uuid AND active_snapshot_id IS NOT NULL
ORDER BY id ASC
LIMIT 1`
	selectActiveSourceByIDScopeSQL = `
SELECT id::text, active_snapshot_id::text
FROM sources
WHERE organization_id = $1::uuid AND id = $2::uuid AND active_snapshot_id IS NOT NULL`
)

// ResolveActiveScope resolves the active snapshot without ever crossing the
// configured organization. A caller may provide a source UUID to constrain
// the lookup; an empty source selects the deterministic first active source.
func (r *PipelineQueryRepository) ResolveActiveScope(ctx context.Context, organizationExternal, sourceID string) (query.Scope, error) {
	if err := validateContext(ctx); err != nil {
		return query.Scope{}, err
	}
	_, organizationID, err := normalizeQueryOrganization(organizationExternal)
	if err != nil {
		return query.Scope{}, err
	}
	if sourceID != "" {
		if err := validateUUID("active source_id", sourceID); err != nil {
			return query.Scope{}, err
		}
		sourceID = strings.ToLower(sourceID)
	}
	if r == nil || r.repository == nil || r.repository.starter == nil {
		return query.Scope{}, fmt.Errorf("%w: pipeline query repository is not configured", ErrDatabase)
	}
	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.Scope{}, wrapPersistenceError(ctx, "begin active query scope", err)
	}
	if tx == nil {
		return query.Scope{}, fmt.Errorf("%w: active query scope transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()
	var activeSourceID, activeSnapshotID string
	if sourceID == "" {
		err = tx.QueryRow(ctx, selectActiveSourceScopeSQL, organizationID).Scan(&activeSourceID, &activeSnapshotID)
	} else {
		err = tx.QueryRow(ctx, selectActiveSourceByIDScopeSQL, organizationID, sourceID).Scan(&activeSourceID, &activeSnapshotID)
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.Scope{}, query.ErrQueryScopeRequired
		}
		return query.Scope{}, wrapPersistenceError(ctx, "read active query scope", err)
	}
	if err := validateUUID("active source_id", activeSourceID); err != nil {
		return query.Scope{}, fmt.Errorf("%w: stored active source", ErrInconsistent)
	}
	if err := validateUUID("active snapshot_id", activeSnapshotID); err != nil {
		return query.Scope{}, fmt.Errorf("%w: stored active snapshot", ErrInconsistent)
	}
	if sourceID != "" && !strings.EqualFold(sourceID, activeSourceID) {
		return query.Scope{}, fmt.Errorf("%w: active source scope mismatch", ErrInconsistent)
	}
	if err := ctx.Err(); err != nil {
		return query.Scope{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return query.Scope{}, wrapPersistenceError(ctx, "commit active query scope", err)
	}
	committed = true
	scope := query.Scope{OrganizationID: organizationID, SourceID: activeSourceID, SnapshotID: activeSnapshotID}
	if err := scope.Validate(); err != nil {
		return query.Scope{}, fmt.Errorf("%w: active query scope", ErrInconsistent)
	}
	return scope, nil
}

const insertPipelineQuerySQL = `
INSERT INTO queries (
    id, organization_id, source_id, snapshot_id, question, question_digest,
    retrieval_configuration, retrieval_configuration_digest, state,
    created_at, started_at
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7::jsonb, $8, 'running', $9, $9)
RETURNING id::text, organization_id::text, source_id::text, snapshot_id::text,
          question, btrim(question_digest), state, diagnostic_code,
          created_at, started_at, finished_at`

const selectPipelineQuerySQL = `
SELECT q.id::text, q.organization_id::text, q.source_id::text, q.snapshot_id::text,
       q.question, btrim(q.question_digest), q.state, q.diagnostic_code,
       q.created_at, q.started_at, q.finished_at,
       r.package_digest, r.response
FROM queries q
LEFT JOIN query_results r
  ON r.organization_id = q.organization_id AND r.query_id = q.id
WHERE q.organization_id = $1::uuid AND q.id = $2::uuid`

const updatePipelineQuerySQL = `
UPDATE queries
SET state = $3,
    diagnostic_code = $4,
    candidate_count = $5,
    package_count = $6,
    latency_ms = $7,
    finished_at = $8
WHERE organization_id = $1::uuid AND id = $2::uuid AND state = 'running'`

// Start durably creates the running execution before retrieval or generation
// begins. This makes cancellation and terminal failure observable after a
// process restart.
func (r *PipelineQueryRepository) Start(ctx context.Context, organizationExternal string, input query.ExecutionInput) (query.Execution, error) {
	if err := validateContext(ctx); err != nil {
		return query.Execution{}, err
	}
	organizationExternal, organizationID, err := normalizeQueryOrganization(organizationExternal)
	if err != nil {
		return query.Execution{}, err
	}
	if err := validateQueryExecutionInput(input); err != nil {
		return query.Execution{}, err
	}
	if r == nil || r.repository == nil || r.repository.starter == nil {
		return query.Execution{}, fmt.Errorf("%w: pipeline query repository is not configured", ErrDatabase)
	}
	newID := r.newID
	if newID == nil {
		newID = randomQueryUUID
	}
	executionID, err := newID()
	if err != nil {
		return query.Execution{}, fmt.Errorf("%w: query execution identity", ErrDatabase)
	}
	if err := validateUUID("query id", executionID); err != nil {
		return query.Execution{}, err
	}
	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	if now.IsZero() {
		return query.Execution{}, fmt.Errorf("%w: query execution clock is invalid", ErrInconsistent)
	}
	configuration := []byte(`{"version":"hybrid-v1"}`)
	configurationDigest := digestPipeline(configuration)

	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "begin pipeline query", err)
	}
	if tx == nil {
		return query.Execution{}, fmt.Errorf("%w: pipeline query transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()
	if err := ensureQueryOrganization(ctx, tx, organizationID, organizationExternal); err != nil {
		return query.Execution{}, err
	}
	row := tx.QueryRow(ctx, insertPipelineQuerySQL,
		executionID, organizationID, queryNullableUUID(input.SourceID), queryNullableUUID(input.SnapshotID),
		input.Question, input.QuestionDigest, configuration, configurationDigest, now,
	)
	execution, err := scanQueryExecution(row, organizationID, input.QuestionDigest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.Execution{}, fmt.Errorf("%w: inserted pipeline query", ErrInconsistent)
		}
		return query.Execution{}, wrapPersistenceError(ctx, "insert pipeline query", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "commit pipeline query", err)
	}
	committed = true
	execution.OrganizationID = organizationExternal
	return execution, nil
}

// Finish records the complete terminal audit in one transaction. All SQL is
// parameterized and only validated response/package representations cross the
// persistence boundary.
func (r *PipelineQueryRepository) Finish(ctx context.Context, organizationExternal string, outcome query.QueryOutcome) (query.Execution, error) {
	if err := validateContext(ctx); err != nil {
		return query.Execution{}, err
	}
	organizationExternal, organizationID, err := normalizeQueryOrganization(organizationExternal)
	if err != nil {
		return query.Execution{}, err
	}
	if outcome.HasResponse {
		outcome.Response = ensurePipelineClaimIDs(outcome.Response, organizationID, outcome.ExecutionID, outcome.PackageDigest)
	}
	if err := validatePipelineOutcome(outcome); err != nil {
		return query.Execution{}, err
	}
	if r == nil || r.repository == nil || r.repository.starter == nil {
		return query.Execution{}, fmt.Errorf("%w: pipeline query repository is not configured", ErrDatabase)
	}
	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "begin pipeline query finish", err)
	}
	if tx == nil {
		return query.Execution{}, fmt.Errorf("%w: pipeline query finish transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()
	current, err := scanQueryExecution(tx.QueryRow(ctx, selectPipelineRunningSQL, organizationID, outcome.ExecutionID), organizationID, "")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.Execution{}, fmt.Errorf("%w: pipeline query", ErrNotFound)
		}
		return query.Execution{}, wrapPersistenceError(ctx, "lock pipeline query", err)
	}
	if current.State != query.ExecutionStateRunning {
		return query.Execution{}, fmt.Errorf("%w: pipeline query is not running", ErrConflict)
	}
	if current.QuestionDigest != outcome.QuestionDigest ||
		!strings.EqualFold(current.SourceID, outcome.Input.SourceID) ||
		!strings.EqualFold(current.SnapshotID, outcome.Input.SnapshotID) {
		return query.Execution{}, fmt.Errorf("%w: pipeline query input mismatch", ErrConflict)
	}
	if current.StartedAt == nil || current.StartedAt.IsZero() || !current.StartedAt.Equal(outcome.StartedAt) {
		return query.Execution{}, fmt.Errorf("%w: pipeline query lifecycle mismatch", ErrConflict)
	}
	packageID, packageDigest, itemIDs, err := persistPipelinePackage(ctx, tx, organizationID, outcome)
	if err != nil {
		return query.Execution{}, err
	}
	if err := persistPipelineClaimsAndCitations(ctx, tx, organizationID, outcome, packageID, itemIDs); err != nil {
		return query.Execution{}, err
	}
	if err := persistPipelineProviderCall(ctx, tx, organizationID, outcome, packageID); err != nil {
		return query.Execution{}, err
	}
	if outcome.HasResponse {
		encoded, marshalErr := json.Marshal(outcome.Response)
		if marshalErr != nil {
			return query.Execution{}, fmt.Errorf("%w: query response encoding", ErrInvalidInput)
		}
		resultID, idErr := r.nextID()
		if idErr != nil {
			return query.Execution{}, fmt.Errorf("%w: query result identity", ErrDatabase)
		}
		responseDigest := digestPipeline(encoded)
		if _, execErr := tx.Exec(ctx, insertPipelineResultSQL,
			resultID, organizationID, outcome.ExecutionID, packageID,
			outcome.Composition.GatewayPackage.ID, packageDigest, encoded, responseDigest,
		); execErr != nil {
			return query.Execution{}, wrapPersistenceError(ctx, "persist query response", execErr)
		}
	}
	finishedAt := outcome.FinishedAt.UTC()
	latency := finishedAt.Sub(current.CreatedAt)
	if current.StartedAt != nil {
		latency = finishedAt.Sub(*current.StartedAt)
	}
	if latency < 0 {
		return query.Execution{}, fmt.Errorf("%w: pipeline query latency", ErrInvalidInput)
	}
	diagnostic := nullableText(outcome.DiagnosticCode)
	tag, err := tx.Exec(ctx, updatePipelineQuerySQL,
		organizationID, outcome.ExecutionID, string(outcome.State), diagnostic,
		len(outcome.Retrieval.Fusion.Candidates), outcome.Composition.UnitCount,
		latency.Milliseconds(), finishedAt,
	)
	if err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "finish pipeline query", err)
	}
	if tag.RowsAffected() != 1 {
		return query.Execution{}, fmt.Errorf("%w: pipeline query terminal update affected %d rows", ErrConflict, tag.RowsAffected())
	}
	if err := tx.Commit(ctx); err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "commit pipeline query finish", err)
	}
	committed = true
	result := query.Execution{
		ID:             outcome.ExecutionID,
		OrganizationID: organizationExternal,
		SourceID:       outcome.Input.SourceID,
		SnapshotID:     outcome.Input.SnapshotID,
		State:          outcome.State,
		QuestionDigest: outcome.QuestionDigest,
		PackageDigest:  outcome.PackageDigest,
		DiagnosticCode: outcome.DiagnosticCode,
		Response:       nil,
		CreatedAt:      current.CreatedAt,
		StartedAt:      current.StartedAt,
		FinishedAt:     &finishedAt,
	}
	if outcome.HasResponse {
		result.Response, _ = json.Marshal(outcome.Response)
	}
	return result, nil
}

func ensurePipelineClaimIDs(response query.Response, organizationID, executionID, packageDigest string) query.Response {
	for index := range response.Claims {
		if response.Claims[index].ID != "" {
			continue
		}
		response.Claims[index].ID = identity.CanonicalUUID(
			"claim", organizationID, executionID, packageDigest, fmt.Sprint(response.Claims[index].Ordinal),
		)
	}
	return response
}

func (r *PipelineQueryRepository) Get(ctx context.Context, organizationExternal, executionID string) (query.Execution, error) {
	if err := validateContext(ctx); err != nil {
		return query.Execution{}, err
	}
	organizationExternal, organizationID, err := normalizeQueryOrganization(organizationExternal)
	if err != nil {
		return query.Execution{}, err
	}
	if err := validateUUID("query id", executionID); err != nil {
		return query.Execution{}, err
	}
	if r == nil || r.repository == nil || r.repository.starter == nil {
		return query.Execution{}, fmt.Errorf("%w: pipeline query repository is not configured", ErrDatabase)
	}
	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "begin pipeline query read", err)
	}
	if tx == nil {
		return query.Execution{}, fmt.Errorf("%w: pipeline query read transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()
	execution, err := scanPipelineExecution(tx.QueryRow(ctx, selectPipelineQuerySQL, organizationID, executionID), organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.Execution{}, fmt.Errorf("%w: pipeline query", ErrNotFound)
		}
		return query.Execution{}, wrapPersistenceError(ctx, "read pipeline query", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "commit pipeline query read", err)
	}
	committed = true
	execution.OrganizationID = organizationExternal
	return execution, nil
}

func (r *PipelineQueryRepository) nextID() (string, error) {
	newID := r.newID
	if newID == nil {
		newID = randomQueryUUID
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	if err := validateUUID("pipeline identity", id); err != nil {
		return "", err
	}
	return id, nil
}

func scanPipelineExecution(row pgx.Row, expectedOrganizationID string) (query.Execution, error) {
	var (
		id, storedOrganizationID, question, digest, state   string
		sourceID, snapshotID, diagnosticCode, packageDigest *string
		response                                            []byte
		createdAt                                           time.Time
		startedAt, finishedAt                               *time.Time
	)
	if err := row.Scan(&id, &storedOrganizationID, &sourceID, &snapshotID, &question, &digest, &state, &diagnosticCode, &createdAt, &startedAt, &finishedAt, &packageDigest, &response); err != nil {
		return query.Execution{}, err
	}
	if storedOrganizationID != expectedOrganizationID {
		return query.Execution{}, fmt.Errorf("%w: pipeline query organization mismatch", ErrInconsistent)
	}
	if err := validateStoredPipelineExecution(id, sourceID, snapshotID, question, digest, state, diagnosticCode, createdAt, startedAt, finishedAt, len(response) > 0); err != nil {
		return query.Execution{}, err
	}
	result := query.Execution{
		ID:             id,
		OrganizationID: storedOrganizationID,
		SourceID:       optionalString(sourceID),
		SnapshotID:     optionalString(snapshotID),
		State:          query.ExecutionState(state),
		QuestionDigest: digest,
		PackageDigest:  optionalString(packageDigest),
		DiagnosticCode: optionalString(diagnosticCode),
		Response:       append([]byte(nil), response...),
		CreatedAt:      createdAt,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
	}
	if len(result.Response) > 0 {
		if !json.Valid(result.Response) {
			return query.Execution{}, fmt.Errorf("%w: stored pipeline response", ErrInconsistent)
		}
		var responseValue query.Response
		if err := json.Unmarshal(result.Response, &responseValue); err != nil || responseValue.Validate() != nil {
			return query.Execution{}, fmt.Errorf("%w: stored pipeline response validation", ErrInconsistent)
		}
		if result.PackageDigest == "" || result.PackageDigest != responseValue.Generation.PackageDigest {
			return query.Execution{}, fmt.Errorf("%w: stored pipeline package digest", ErrInconsistent)
		}
	}
	return result, nil
}

// validateStoredPipelineExecution is the lifecycle variant used after the
// complete result projection exists. The original 5.3 validator correctly
// rejects completed rows because that projection had no result columns; this
// adapter validates the same base invariants while requiring the joined
// response for successful terminal states.
func validateStoredPipelineExecution(id string, sourceID, snapshotID *string, question, digest, state string, diagnosticCode *string, createdAt time.Time, startedAt, finishedAt *time.Time, hasResponse bool) error {
	if err := validateUUID("query id", id); err != nil {
		return fmt.Errorf("%w: stored query id", ErrInconsistent)
	}
	if err := validateText("stored query question", question); err != nil || strings.ContainsAny(question, "\x00\r\n") {
		return fmt.Errorf("%w: stored query question", ErrInconsistent)
	}
	if validateDigest("stored query question_digest", digest) != nil || digestBytes([]byte(question)) != digest {
		return fmt.Errorf("%w: stored query question digest", ErrInconsistent)
	}
	storedState := query.ExecutionState(state)
	if !storedState.Valid() {
		return fmt.Errorf("%w: stored query state", ErrInconsistent)
	}
	if sourceID != nil {
		if err := validateUUID("stored query source_id", *sourceID); err != nil {
			return fmt.Errorf("%w: stored query source", ErrInconsistent)
		}
	}
	if snapshotID != nil {
		if sourceID == nil {
			return fmt.Errorf("%w: stored query snapshot scope", ErrInconsistent)
		}
		if err := validateUUID("stored query snapshot_id", *snapshotID); err != nil {
			return fmt.Errorf("%w: stored query snapshot", ErrInconsistent)
		}
	}
	hasDiagnostic := diagnosticCode != nil && *diagnosticCode != ""
	if diagnosticCode != nil && !safeQueryDiagnostic(*diagnosticCode) {
		return fmt.Errorf("%w: stored query diagnostic", ErrInconsistent)
	}
	if createdAt.IsZero() || (startedAt != nil && startedAt.Before(createdAt)) || (finishedAt != nil && finishedAt.Before(createdAt)) ||
		(startedAt != nil && finishedAt != nil && finishedAt.Before(*startedAt)) {
		return fmt.Errorf("%w: stored query timestamps", ErrInconsistent)
	}
	switch storedState {
	case query.ExecutionStatePending:
		if startedAt != nil || finishedAt != nil || hasDiagnostic || hasResponse {
			return fmt.Errorf("%w: pending query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateRunning:
		if startedAt == nil || finishedAt != nil || hasDiagnostic || hasResponse {
			return fmt.Errorf("%w: running query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateCompleted:
		if startedAt == nil || finishedAt == nil || hasDiagnostic || !hasResponse {
			return fmt.Errorf("%w: completed query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStatePartial:
		if startedAt == nil || finishedAt == nil || !hasResponse {
			return fmt.Errorf("%w: partial query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateFailed:
		if startedAt == nil || finishedAt == nil || !hasDiagnostic || hasResponse {
			return fmt.Errorf("%w: failed query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateAbstained:
		if startedAt == nil || finishedAt == nil || (!hasResponse && (diagnosticCode == nil || *diagnosticCode != "pipeline_not_configured")) {
			return fmt.Errorf("%w: abstained query lifecycle", ErrInconsistent)
		}
	}
	return nil
}

func validatePipelineOutcome(outcome query.QueryOutcome) error {
	if err := validateUUID("pipeline execution id", outcome.ExecutionID); err != nil {
		return err
	}
	if !outcome.State.Terminal() || outcome.FinishedAt.IsZero() || outcome.StartedAt.IsZero() || outcome.FinishedAt.Before(outcome.StartedAt) {
		return fmt.Errorf("%w: pipeline lifecycle", ErrInvalidInput)
	}
	if err := validateDigest("pipeline question digest", outcome.QuestionDigest); err != nil {
		return err
	}
	if outcome.DiagnosticCode != "" && !safeQueryDiagnostic(outcome.DiagnosticCode) {
		return fmt.Errorf("%w: pipeline diagnostic", ErrInvalidInput)
	}
	if outcome.HasResponse {
		if err := outcome.Response.Validate(); err != nil {
			return fmt.Errorf("%w: pipeline response", ErrInvalidInput)
		}
		if outcome.PackageDigest == "" || outcome.Response.Generation.PackageDigest != outcome.PackageDigest {
			return fmt.Errorf("%w: pipeline package digest", ErrInvalidInput)
		}
		if outcome.Composition.ValidationPackage.Digest == "" || outcome.PackageDigest != outcome.Composition.ValidationPackage.Digest {
			return fmt.Errorf("%w: pipeline composition digest", ErrInvalidInput)
		}
	}
	if outcome.State == query.ExecutionStateAbstained && !outcome.HasResponse && outcome.DiagnosticCode != "pipeline_not_configured" {
		return fmt.Errorf("%w: abstained pipeline response is missing", ErrInvalidInput)
	}
	if outcome.State == query.ExecutionStateFailed && outcome.HasResponse {
		return fmt.Errorf("%w: failed pipeline carries a response", ErrInvalidInput)
	}
	if outcome.State != query.ExecutionStateFailed && !outcome.HasResponse {
		return fmt.Errorf("%w: terminal pipeline response is missing", ErrInvalidInput)
	}
	return nil
}

// selectPipelineRunningSQL serializes terminalization. The older 5.3
// execution adapter intentionally remains unlocked because it never performs
// a separate running-to-terminal transition.
const selectPipelineRunningSQL = selectQueryExecutionSQL + " FOR UPDATE"

const (
	insertPipelineCandidateSQL = `
INSERT INTO query_candidates (
    id, organization_id, query_id, source_id, snapshot_id, evidence_id,
    rank_position, score, exact_score, lexical_score, vector_score, relation_score,
    signals, ranking_configuration, ranking_configuration_digest, candidate_digest,
    decision, decision_reason
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid,
        $7, $8, $9, $10, $11, $12, $13::jsonb, $14::jsonb, $15, $16, $17, $18)`
	insertPipelinePackageSQL = `
INSERT INTO evidence_packages (
    id, organization_id, query_id, package_digest, selection_configuration,
    selection_configuration_digest, state, max_items, max_characters, max_tokens,
    candidate_count, included_count, excluded_count, total_characters,
    estimated_tokens, latency_ms, abstention_reason, created_at, finalized_at
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::jsonb, $6, $7, $8, $9, $10,
        $11, $12, $13, $14, $15, $16, $17, $18, $18)`
	insertPipelinePackageItemSQL = `
INSERT INTO evidence_package_items (
    id, organization_id, package_id, query_id, candidate_id, source_id, snapshot_id,
    evidence_id, ordinal, included, decision_reason, content_hash,
    content_characters, estimated_tokens, external_transfer_decision
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, $7::uuid,
        $8::uuid, $9, $10, $11, $12, $13, $14, $15)`
	insertPipelineClaimSQL = `
INSERT INTO generated_claims (
    id, organization_id, query_id, package_id, ordinal, claim_type, claim_text,
    claim_digest, support_state, citation_count
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10)`
	insertPipelineCitationSQL = `
INSERT INTO citations (
    id, organization_id, query_id, package_id, claim_id, package_item_id,
    package_item_included, source_id, snapshot_id, evidence_id, ordinal,
    citation_role, locator
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6::uuid, true,
        $7::uuid, $8::uuid, $9::uuid, $10, $11, $12::jsonb)`
	insertPipelineProviderCallSQL = `
INSERT INTO provider_calls (
    id, organization_id, capability, provider, configured_model, effective_model,
    query_id, package_id, request_digest, response_digest, transferred_evidence_digest,
    transferred_evidence_count, state, error_category, error_code, attempt_count,
    input_tokens, output_tokens, total_tokens, latency_ms, started_at, finished_at
)
VALUES ($1::uuid, $2::uuid, 'generation', $3, $4, $5, $6::uuid, $7::uuid,
        $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)`
	insertPipelineProviderAttemptSQL = `
INSERT INTO provider_call_attempts (
    id, organization_id, provider_call_id, attempt_number, provider, capability,
    configured_model, effective_model, request_digest, response_digest,
    transferred_evidence_digest, transferred_evidence_count, state, error_category,
    error_code, input_tokens, output_tokens, total_tokens, latency_ms, started_at,
    finished_at
)
VALUES ($1::uuid, $2::uuid, $3::uuid, 1, $4, 'generation', $5, $6, $7, $8,
        $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`
	insertPipelineResultSQL = `
INSERT INTO query_results (
    id, organization_id, query_id, package_id, package_identity, package_digest,
    response, response_digest
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7::jsonb, $8)`
)

func persistPipelinePackage(ctx context.Context, tx transaction, organizationID string, outcome query.QueryOutcome) (string, string, map[string]string, error) {
	composition := outcome.Composition
	if composition.ValidationPackage.Digest == "" {
		return "", "", nil, nil
	}
	if err := composition.Validate(); err != nil {
		return "", "", nil, fmt.Errorf("%w: pipeline package", ErrInvalidInput)
	}
	packageID, err := randomQueryUUID()
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: package identity", ErrDatabase)
	}
	configuration, err := json.Marshal(composition.Configuration)
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: package configuration", ErrInvalidInput)
	}
	configurationDigest := digestPipeline(configuration)
	if composition.Configuration.Digest != "" {
		configurationDigest = composition.Configuration.Digest
	}
	state := "ready"
	if outcome.State == query.ExecutionStateAbstained {
		state = "abstained"
	} else if outcome.State == query.ExecutionStatePartial {
		state = "partial"
	} else if outcome.State == query.ExecutionStateFailed {
		state = "failed"
	}
	maxItems, maxCharacters, maxTokens := composition.Configuration.Limits.MaxUnits, composition.Configuration.Limits.MaxCharacters, composition.Configuration.Limits.MaxTokens
	if _, err := tx.Exec(ctx, insertPipelinePackageSQL,
		packageID, organizationID, outcome.ExecutionID, composition.ValidationPackage.Digest,
		configuration, configurationDigest, state, maxItems, maxCharacters, maxTokens,
		len(outcome.Retrieval.Fusion.Candidates), composition.UnitCount, len(composition.Audits)-composition.UnitCount,
		composition.CharacterCount, composition.TokenEstimate, int64(outcome.FinishedAt.Sub(outcome.StartedAt).Milliseconds()),
		pipelineAbstentionReason(outcome.Response), outcome.FinishedAt,
	); err != nil {
		return "", "", nil, wrapPersistenceError(ctx, "persist evidence package", err)
	}
	candidateIDs := make(map[string]string, len(outcome.Retrieval.Candidates))
	auditByEvidence := make(map[string]query.CandidateAudit, len(composition.Audits))
	for _, audit := range composition.Audits {
		auditByEvidence[audit.EvidenceID] = audit
	}
	for _, candidate := range outcome.Retrieval.Candidates {
		id, idErr := randomQueryUUID()
		if idErr != nil {
			return "", "", nil, fmt.Errorf("%w: candidate identity", ErrDatabase)
		}
		fusion := candidate.Fusion
		if err := validateUUID("candidate source_id", fusion.SourceID); err != nil {
			return "", "", nil, err
		}
		if err := validateUUID("candidate snapshot_id", fusion.SnapshotID); err != nil {
			return "", "", nil, err
		}
		if err := validateUUID("candidate evidence_id", fusion.EvidenceID); err != nil {
			return "", "", nil, err
		}
		if !finitePipelineFloat(fusion.Score) || fusion.Score < 0 {
			return "", "", nil, fmt.Errorf("%w: candidate score", ErrInvalidInput)
		}
		signals, _ := json.Marshal(fusion.Signals)
		ranking, _ := json.Marshal(outcome.Retrieval.Fusion.Configuration)
		rankingDigest := digestPipeline(ranking)
		decision := "excluded"
		reason := string(query.PackageReasonInvalidCandidate)
		if audit, ok := auditByEvidence[fusion.EvidenceID]; ok {
			if audit.Included {
				decision = "included"
			}
			reason = string(audit.Reason)
		}
		var exactScore, lexicalScore, vectorScore, relationScore any
		for _, signal := range fusion.Signals {
			if !finitePipelineFloat(signal.Contribution) || signal.Contribution < 0 {
				return "", "", nil, fmt.Errorf("%w: candidate signal", ErrInvalidInput)
			}
			switch signal.Kind {
			case retrieval.FusionSignalExact:
				exactScore = signal.Contribution
			case retrieval.FusionSignalTextual:
				lexicalScore = signal.Contribution
			case retrieval.FusionSignalVector:
				vectorScore = signal.Contribution
			case retrieval.FusionSignalRelation:
				relationScore = signal.Contribution
			}
		}
		candidateDigest := digestPipelineJSON(struct {
			EvidenceID string
			SourceID   string
			SnapshotID string
			Rank       int
			Score      float64
			Signals    json.RawMessage
		}{fusion.EvidenceID, fusion.SourceID, fusion.SnapshotID, fusion.Rank, fusion.Score, signals})
		if _, err := tx.Exec(ctx, insertPipelineCandidateSQL,
			id, organizationID, outcome.ExecutionID, fusion.SourceID, fusion.SnapshotID, fusion.EvidenceID,
			fusion.Rank, fusion.Score, exactScore, lexicalScore, vectorScore, relationScore,
			signals, ranking, rankingDigest, candidateDigest, decision, reason,
		); err != nil {
			return "", "", nil, wrapPersistenceError(ctx, "persist query candidate", err)
		}
		candidateIDs[fusion.EvidenceID] = id
	}
	itemIDs := make(map[string]string, len(composition.Audits))
	gatewayByID := make(map[string]string, len(composition.GatewayPackage.Evidence))
	for _, item := range composition.GatewayPackage.Evidence {
		gatewayByID[item.ID] = item.Content
	}
	candidateByUnitID := make(map[string]string, len(outcome.Retrieval.Candidates))
	for _, candidate := range outcome.Retrieval.Candidates {
		candidateByUnitID[candidate.Unit.ID] = candidate.Fusion.EvidenceID
	}
	ordinal := 0
	for _, audit := range composition.Audits {
		candidateEvidenceID := audit.EvidenceID
		if candidateEvidenceID == "" {
			continue
		}
		candidateID := candidateIDs[candidateEvidenceID]
		if candidateID == "" {
			if canonicalID, ok := candidateByUnitID[candidateEvidenceID]; ok {
				candidateID = candidateIDs[canonicalID]
			}
		}
		if candidateID == "" {
			return "", "", nil, fmt.Errorf("%w: package candidate reference", ErrInconsistent)
		}
		candidate := findPipelineCandidate(outcome.Retrieval.Candidates, candidateEvidenceID)
		if candidate.Fusion.EvidenceID == "" {
			if canonicalID, ok := candidateByUnitID[candidateEvidenceID]; ok {
				candidate = findPipelineCandidate(outcome.Retrieval.Candidates, canonicalID)
			}
		}
		if err := validateUUID("package item source_id", candidate.Fusion.SourceID); err != nil {
			return "", "", nil, err
		}
		if err := validateUUID("package item snapshot_id", candidate.Fusion.SnapshotID); err != nil {
			return "", "", nil, err
		}
		if err := validateUUID("package item evidence_id", candidate.Fusion.EvidenceID); err != nil {
			return "", "", nil, err
		}
		itemID, idErr := randomQueryUUID()
		if idErr != nil {
			return "", "", nil, fmt.Errorf("%w: package item identity", ErrDatabase)
		}
		ordinal++
		content := gatewayByID[audit.EvidenceID]
		transfer := string(evidence.DecisionDeny)
		characters, tokens := int64(0), 0
		if audit.Included {
			if content == evidence.RedactedContent {
				transfer = string(evidence.DecisionRedact)
			} else {
				transfer = string(evidence.DecisionAllow)
			}
			characters = int64(utf8.RuneCountInString(content))
			tokens = query.EstimatePackageTokens(content, composition.Configuration.Limits.CharactersPerToken)
		}
		if err := validateDigest("package item content_hash", audit.ContentHash); err != nil {
			return "", "", nil, err
		}
		if _, err := tx.Exec(ctx, insertPipelinePackageItemSQL,
			itemID, organizationID, packageID, outcome.ExecutionID, candidateID,
			candidate.Fusion.SourceID, candidate.Fusion.SnapshotID, candidate.Fusion.EvidenceID,
			ordinal, audit.Included, string(audit.Reason), audit.ContentHash, characters, tokens, transfer,
		); err != nil {
			return "", "", nil, wrapPersistenceError(ctx, "persist evidence package item", err)
		}
		itemIDs[audit.EvidenceID] = itemID
		itemIDs[candidate.Fusion.EvidenceID] = itemID
	}
	return packageID, composition.ValidationPackage.Digest, itemIDs, nil
}

func persistPipelineClaimsAndCitations(ctx context.Context, tx transaction, organizationID string, outcome query.QueryOutcome, packageID string, itemIDs map[string]string) error {
	if !outcome.HasResponse || packageID == "" {
		return nil
	}
	claimIDs := make(map[int]string, len(outcome.Response.Claims))
	for _, claim := range outcome.Response.Claims {
		claimID := claim.ID
		if claimID == "" {
			claimID = identity.CanonicalUUID("claim", organizationID, outcome.ExecutionID, outcome.PackageDigest, fmt.Sprint(claim.Ordinal))
		} else if err := validateUUID("claim id", claimID); err != nil {
			return fmt.Errorf("%w: claim identity", ErrInvalidInput)
		}
		claimDigest := digestPipeline([]byte(claim.Text))
		if _, err := tx.Exec(ctx, insertPipelineClaimSQL,
			claimID, organizationID, outcome.ExecutionID, packageID, claim.Ordinal,
			string(claim.Kind), claim.Text, claimDigest, string(claim.Support), len(claim.CitationOrdinals),
		); err != nil {
			return wrapPersistenceError(ctx, "persist generated claim", err)
		}
		claimIDs[claim.Ordinal] = claimID
	}
	for _, citation := range outcome.Response.Citations {
		claimID := ""
		for _, claim := range outcome.Response.Claims {
			for _, ordinal := range claim.CitationOrdinals {
				if ordinal == citation.Ordinal {
					claimID = claimIDs[claim.Ordinal]
				}
			}
		}
		itemID := itemIDs[citation.EvidenceID]
		if claimID == "" || itemID == "" {
			return fmt.Errorf("%w: citation references unknown claim or package item", ErrInconsistent)
		}
		citationID, err := randomQueryUUID()
		if err != nil {
			return fmt.Errorf("%w: citation identity", ErrDatabase)
		}
		locator, err := json.Marshal(citation.Locator)
		if err != nil {
			return fmt.Errorf("%w: citation locator", ErrInvalidInput)
		}
		if err := validateUUID("citation source_id", citation.SourceID); err != nil {
			return err
		}
		if err := validateUUID("citation snapshot_id", citation.SnapshotID); err != nil {
			return err
		}
		if err := validateUUID("citation evidence_id", canonicalCitationEvidenceID(outcome, citation.EvidenceID)); err != nil {
			return err
		}
		canonicalEvidenceID := canonicalCitationEvidenceID(outcome, citation.EvidenceID)
		if _, err := tx.Exec(ctx, insertPipelineCitationSQL,
			citationID, organizationID, outcome.ExecutionID, packageID, claimID, itemID,
			citation.SourceID, citation.SnapshotID, canonicalEvidenceID, citation.Ordinal,
			string(citation.Role), locator,
		); err != nil {
			return wrapPersistenceError(ctx, "persist citation", err)
		}
	}
	return nil
}

func persistPipelineProviderCall(ctx context.Context, tx transaction, organizationID string, outcome query.QueryOutcome, packageID string) error {
	audit := outcome.Generation
	if audit == nil {
		return nil
	}
	if packageID == "" {
		return fmt.Errorf("%w: provider call package", ErrInconsistent)
	}
	if err := validateDigest("provider request digest", audit.RequestDigest); err != nil {
		return err
	}
	if audit.ResponseDigest != "" {
		if err := validateDigest("provider response digest", audit.ResponseDigest); err != nil {
			return err
		}
	}
	if err := validateDigest("provider transfer digest", audit.TransferredEvidenceDigest); err != nil {
		return err
	}
	if audit.Provider == "" || strings.TrimSpace(audit.ConfiguredModel) == "" || audit.AttemptCount < 1 {
		return fmt.Errorf("%w: provider call identity", ErrInvalidInput)
	}
	callID, err := randomQueryUUID()
	if err != nil {
		return fmt.Errorf("%w: provider call identity", ErrDatabase)
	}
	errorCategory, errorCode := any(nil), any(nil)
	if audit.State != "succeeded" {
		errorCategory = "unknown"
		errorCode = nullableText(audit.ErrorCode)
		if audit.ErrorCode == "" {
			errorCode = "provider_failed"
		}
	}
	totalTokens := audit.Usage.InputTokens + audit.Usage.OutputTokens
	if _, err := tx.Exec(ctx, insertPipelineProviderCallSQL,
		callID, organizationID, string(audit.Provider), audit.ConfiguredModel, nullableText(audit.EffectiveModel),
		outcome.ExecutionID, packageID, audit.RequestDigest, nullableText(audit.ResponseDigest), audit.TransferredEvidenceDigest,
		audit.TransferredEvidenceCount, audit.State, errorCategory, errorCode, audit.AttemptCount,
		audit.Usage.InputTokens, audit.Usage.OutputTokens, totalTokens, audit.Latency.Milliseconds(), audit.StartedAt, audit.FinishedAt,
	); err != nil {
		return wrapPersistenceError(ctx, "persist provider call", err)
	}
	attemptID, err := randomQueryUUID()
	if err != nil {
		return fmt.Errorf("%w: provider attempt identity", ErrDatabase)
	}
	if _, err := tx.Exec(ctx, insertPipelineProviderAttemptSQL,
		attemptID, organizationID, callID, string(audit.Provider), audit.ConfiguredModel, nullableText(audit.EffectiveModel),
		audit.RequestDigest, nullableText(audit.ResponseDigest), audit.TransferredEvidenceDigest, audit.TransferredEvidenceCount,
		audit.State, errorCategory, errorCode, audit.Usage.InputTokens, audit.Usage.OutputTokens, totalTokens,
		audit.Latency.Milliseconds(), audit.StartedAt, audit.FinishedAt,
	); err != nil {
		return wrapPersistenceError(ctx, "persist provider attempt", err)
	}
	return nil
}

func canonicalCitationEvidenceID(outcome query.QueryOutcome, id string) string {
	for _, candidate := range outcome.Retrieval.Candidates {
		if candidate.Unit.ID == id {
			return candidate.Fusion.EvidenceID
		}
	}
	return id
}

func findPipelineCandidate(candidates []query.PackageCandidate, evidenceID string) query.PackageCandidate {
	for _, candidate := range candidates {
		if candidate.Fusion.EvidenceID == evidenceID || candidate.Unit.ID == evidenceID {
			return candidate
		}
	}
	return query.PackageCandidate{}
}

func pipelineAbstentionReason(response query.Response) any {
	if response.Generation.Termination != query.TerminationAbstained || len(response.Gaps) == 0 {
		return nil
	}
	return nullableText(response.Gaps[0].Code)
}

func finitePipelineFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func digestPipeline(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func digestPipelineJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return digestPipeline(encoded)
}
