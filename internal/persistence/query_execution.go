package persistence

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
)

// QueryExecutionRepository is the PostgreSQL application adapter for the
// synchronous query boundary. It stores a terminal abstention until the full
// retrieval/generation pipeline is composed, so Create never returns an
// execution that was not durably committed.
type QueryExecutionRepository struct {
	repository *Repository
	now        func() time.Time
	newID      func() (string, error)
}

// NewQueryExecutionRepository composes the query execution adapter with a
// PostgreSQL pool. Construction does not open or ping the pool.
func NewQueryExecutionRepository(pool *pgxpool.Pool) *QueryExecutionRepository {
	var repository *Repository
	if pool != nil {
		repository = NewRepository(pool)
	}
	return &QueryExecutionRepository{
		repository: repository,
		now:        time.Now,
		newID:      randomQueryUUID,
	}
}

func newQueryExecutionRepositoryWithStarter(starter transactionStarter) *QueryExecutionRepository {
	return &QueryExecutionRepository{
		repository: newRepositoryWithStarter(starter),
		now:        time.Now,
		newID:      randomQueryUUID,
	}
}

var _ query.ExecutionService = (*QueryExecutionRepository)(nil)

const insertQueryOrganizationSQL = `
INSERT INTO organizations (id, external_id, name)
VALUES ($1::uuid, $2, $2)
ON CONFLICT (external_id) DO NOTHING`

const selectQueryOrganizationSQL = `
SELECT id::text
FROM organizations
WHERE external_id = $1`

const insertQueryExecutionSQL = `
INSERT INTO queries (
    id, organization_id, source_id, snapshot_id, question, question_digest,
    retrieval_configuration, retrieval_configuration_digest, state,
    diagnostic_code, created_at, started_at, finished_at
)
VALUES (
    $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6, $7::jsonb, $8, $9,
    $10, $11, $11, $11
)
RETURNING id::text, organization_id::text, source_id::text, snapshot_id::text,
          question, btrim(question_digest), state, diagnostic_code,
          created_at, started_at, finished_at`

const selectQueryExecutionSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       question, btrim(question_digest), state, diagnostic_code,
       created_at, started_at, finished_at
FROM queries
WHERE organization_id = $1::uuid AND id = $2::uuid`

// Create persists a terminal abstention in one transaction. A later query
// pipeline can replace this adapter while retaining the same durable port.
func (r *QueryExecutionRepository) Create(ctx context.Context, organizationExternal string, input query.ExecutionInput) (query.Execution, error) {
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
		return query.Execution{}, fmt.Errorf("%w: query execution repository is not configured", ErrDatabase)
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
	if input.SourceID != "" {
		input.SourceID = strings.ToLower(input.SourceID)
	}
	if input.SnapshotID != "" {
		input.SnapshotID = strings.ToLower(input.SnapshotID)
	}

	now := time.Now().UTC()
	if r.now != nil {
		now = r.now().UTC()
	}
	if now.IsZero() {
		return query.Execution{}, fmt.Errorf("%w: query execution clock is invalid", ErrInconsistent)
	}
	configuration := []byte("{}")
	configurationDigest := digestBytes(configuration)
	const state = query.ExecutionStateAbstained
	const diagnosticCode = "pipeline_not_configured"

	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "begin query execution", err)
	}
	if tx == nil {
		return query.Execution{}, fmt.Errorf("%w: query execution transaction is nil", ErrInconsistent)
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
	row := tx.QueryRow(ctx, insertQueryExecutionSQL,
		executionID, organizationID, queryNullableUUID(input.SourceID), queryNullableUUID(input.SnapshotID),
		input.Question, input.QuestionDigest, configuration, configurationDigest,
		string(state), diagnosticCode, now,
	)
	execution, err := scanQueryExecution(row, organizationID, input.QuestionDigest)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.Execution{}, fmt.Errorf("%w: inserted query execution", ErrInconsistent)
		}
		return query.Execution{}, wrapPersistenceError(ctx, "insert query execution", err)
	}
	if err := ctx.Err(); err != nil {
		return query.Execution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "commit query execution", err)
	}
	committed = true
	execution.OrganizationID = organizationExternal
	return execution, nil
}

// Get re-inspects only the query execution belonging to the supplied fixed
// organization. The question itself is selected only to verify its digest and
// is never copied into the returned public representation.
func (r *QueryExecutionRepository) Get(ctx context.Context, organizationExternal, executionID string) (query.Execution, error) {
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
		return query.Execution{}, fmt.Errorf("%w: query execution repository is not configured", ErrDatabase)
	}
	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "begin query execution read", err)
	}
	if tx == nil {
		return query.Execution{}, fmt.Errorf("%w: query execution read transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()

	execution, err := scanQueryExecution(tx.QueryRow(ctx, selectQueryExecutionSQL, organizationID, executionID), organizationID, "")
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.Execution{}, fmt.Errorf("%w: query execution", ErrNotFound)
		}
		return query.Execution{}, wrapPersistenceError(ctx, "read query execution", err)
	}
	if err := ctx.Err(); err != nil {
		return query.Execution{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return query.Execution{}, wrapPersistenceError(ctx, "commit query execution read", err)
	}
	committed = true
	execution.OrganizationID = organizationExternal
	return execution, nil
}

func normalizeQueryOrganization(value string) (string, string, error) {
	if err := validateText("organization external_id", value); err != nil {
		return "", "", err
	}
	value = strings.TrimSpace(value)
	if err := validateText("organization external_id", value); err != nil {
		return "", "", err
	}
	return value, identity.CanonicalUUID("organization", value), nil
}

func validateQueryExecutionInput(input query.ExecutionInput) error {
	if err := validateText("query question", input.Question); err != nil {
		return err
	}
	if strings.ContainsAny(input.Question, "\x00\r\n") {
		return fmt.Errorf("%w: query question contains unsupported control characters", ErrInvalidInput)
	}
	if len(input.Question) > 1<<20 {
		return fmt.Errorf("%w: query question exceeds persistence limit", ErrInvalidInput)
	}
	if err := validateDigest("query question_digest", input.QuestionDigest); err != nil {
		return err
	}
	if digestBytes([]byte(input.Question)) != input.QuestionDigest {
		return fmt.Errorf("%w: query question digest does not match question", ErrInvalidInput)
	}
	if input.SourceID != "" {
		if err := validateUUID("query source_id", input.SourceID); err != nil {
			return err
		}
	}
	if input.SnapshotID != "" {
		if input.SourceID == "" {
			return fmt.Errorf("%w: query snapshot requires source", ErrInvalidInput)
		}
		if err := validateUUID("query snapshot_id", input.SnapshotID); err != nil {
			return err
		}
	}
	return nil
}

func ensureQueryOrganization(ctx context.Context, tx transaction, organizationID, externalID string) error {
	if err := validateUUID("organization_id", organizationID); err != nil {
		return err
	}
	if err := validateText("organization external_id", externalID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, insertQueryOrganizationSQL, organizationID, externalID)
	if err != nil {
		return wrapPersistenceError(ctx, "insert query organization", err)
	}
	if tag.RowsAffected() > 1 {
		return fmt.Errorf("%w: query organization insert affected too many rows", ErrInconsistent)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var storedID string
	if err := tx.QueryRow(ctx, selectQueryOrganizationSQL, externalID).Scan(&storedID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: query organization identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read query organization", err)
	}
	if storedID != organizationID {
		return fmt.Errorf("%w: query organization scope mismatch", ErrConflict)
	}
	return nil
}

func scanQueryExecution(row pgx.Row, expectedOrganizationID, expectedQuestionDigest string) (query.Execution, error) {
	var (
		storedID, storedOrganizationID, question, digest, state string
		sourceID, snapshotID, diagnosticCode                    *string
		createdAt                                               time.Time
		startedAt, finishedAt                                   *time.Time
	)
	if err := row.Scan(
		&storedID, &storedOrganizationID, &sourceID, &snapshotID, &question,
		&digest, &state, &diagnosticCode, &createdAt, &startedAt, &finishedAt,
	); err != nil {
		return query.Execution{}, err
	}
	if storedOrganizationID != expectedOrganizationID {
		return query.Execution{}, fmt.Errorf("%w: query execution organization mismatch", ErrInconsistent)
	}
	if err := validateStoredQueryExecution(storedID, sourceID, snapshotID, question, digest, state, diagnosticCode, createdAt, startedAt, finishedAt); err != nil {
		return query.Execution{}, err
	}
	if expectedQuestionDigest != "" && digest != expectedQuestionDigest {
		return query.Execution{}, fmt.Errorf("%w: query execution question digest mismatch", ErrInconsistent)
	}
	execution := query.Execution{
		ID:             storedID,
		OrganizationID: storedOrganizationID,
		SourceID:       optionalString(sourceID),
		SnapshotID:     optionalString(snapshotID),
		State:          query.ExecutionState(state),
		QuestionDigest: digest,
		DiagnosticCode: optionalString(diagnosticCode),
		CreatedAt:      createdAt,
		StartedAt:      startedAt,
		FinishedAt:     finishedAt,
	}
	return execution, nil
}

func validateStoredQueryExecution(id string, sourceID, snapshotID *string, question, digest, state string, diagnosticCode *string, createdAt time.Time, startedAt, finishedAt *time.Time) error {
	if err := validateUUID("query id", id); err != nil {
		return fmt.Errorf("%w: stored query id", ErrInconsistent)
	}
	if err := validateText("stored query question", question); err != nil || strings.ContainsAny(question, "\x00\r\n") {
		return fmt.Errorf("%w: stored query question", ErrInconsistent)
	}
	if validateDigest("stored query question_digest", digest) != nil || digestBytes([]byte(question)) != digest {
		return fmt.Errorf("%w: stored query question digest", ErrInconsistent)
	}
	if !query.ExecutionState(state).Valid() {
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
	switch query.ExecutionState(state) {
	case query.ExecutionStatePending:
		if startedAt != nil || finishedAt != nil || hasDiagnostic {
			return fmt.Errorf("%w: pending query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateRunning:
		if startedAt == nil || finishedAt != nil || hasDiagnostic {
			return fmt.Errorf("%w: running query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateCompleted:
		// The current projection has no response/package columns. A completed
		// row without those durable identities would be indistinguishable from
		// an invented answer and is therefore rejected until the full pipeline
		// owns its persistence contract.
		return fmt.Errorf("%w: completed query has no persisted result", ErrInconsistent)
	case query.ExecutionStatePartial:
		if finishedAt == nil || !hasDiagnostic {
			return fmt.Errorf("%w: partial query lifecycle", ErrInconsistent)
		}
	case query.ExecutionStateFailed, query.ExecutionStateAbstained:
		if finishedAt == nil || !hasDiagnostic {
			return fmt.Errorf("%w: terminal query lifecycle", ErrInconsistent)
		}
	}
	return nil
}

func safeQueryDiagnostic(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' && r != ':' {
			return false
		}
	}
	return true
}

func randomQueryUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:]), nil
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// EvidenceInspectionRepository exposes only persisted evidence in the fixed
// organization scope. It never reads original source files or provider data.
type EvidenceInspectionRepository struct {
	repository *Repository
}

// NewEvidenceInspectionRepository composes the evidence reader with a pool.
func NewEvidenceInspectionRepository(pool *pgxpool.Pool) *EvidenceInspectionRepository {
	if pool == nil {
		return &EvidenceInspectionRepository{}
	}
	return &EvidenceInspectionRepository{repository: NewRepository(pool)}
}

func newEvidenceInspectionRepositoryWithStarter(starter transactionStarter) *EvidenceInspectionRepository {
	return &EvidenceInspectionRepository{repository: newRepositoryWithStarter(starter)}
}

var _ query.EvidenceReader = (*EvidenceInspectionRepository)(nil)

const selectEvidenceInspectionSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       artifact_id::text, observation_id::text, locator, content_state,
       content, btrim(content_hash), content_bytes, content_characters,
       truncated, classification, findings, persist_decision,
       external_transfer_decision, redaction_reason
FROM evidence_units
WHERE organization_id = $1::uuid AND id = $2::uuid`

func (r *EvidenceInspectionRepository) GetEvidence(ctx context.Context, organizationExternal, evidenceID string) (query.EvidenceInspection, error) {
	if err := validateContext(ctx); err != nil {
		return query.EvidenceInspection{}, err
	}
	organizationExternal, organizationID, err := normalizeQueryOrganization(organizationExternal)
	if err != nil {
		return query.EvidenceInspection{}, err
	}
	if err := validateUUID("evidence id", evidenceID); err != nil {
		return query.EvidenceInspection{}, err
	}
	if r == nil || r.repository == nil || r.repository.starter == nil {
		return query.EvidenceInspection{}, fmt.Errorf("%w: evidence inspection repository is not configured", ErrDatabase)
	}
	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return query.EvidenceInspection{}, wrapPersistenceError(ctx, "begin evidence inspection", err)
	}
	if tx == nil {
		return query.EvidenceInspection{}, fmt.Errorf("%w: evidence inspection transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()

	inspection, err := scanEvidenceInspection(tx.QueryRow(ctx, selectEvidenceInspectionSQL, organizationID, evidenceID), organizationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return query.EvidenceInspection{}, fmt.Errorf("%w: evidence inspection", ErrNotFound)
		}
		return query.EvidenceInspection{}, wrapPersistenceError(ctx, "read evidence inspection", err)
	}
	if err := ctx.Err(); err != nil {
		return query.EvidenceInspection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return query.EvidenceInspection{}, wrapPersistenceError(ctx, "commit evidence inspection", err)
	}
	committed = true
	inspection.OrganizationID = organizationExternal
	return inspection, nil
}

func scanEvidenceInspection(row pgx.Row, expectedOrganizationID string) (query.EvidenceInspection, error) {
	var (
		id, storedOrganizationID, sourceID, snapshotID, artifactID string
		observationID, contentState, contentHash, classification   *string
		content                                                    *string
		locatorJSON, findingsJSON                                  []byte
		contentBytes, contentCharacters                            int64
		truncated                                                  bool
		persistDecision, transferDecision                          string
		redactionReason                                            *string
	)
	if err := row.Scan(
		&id, &storedOrganizationID, &sourceID, &snapshotID, &artifactID,
		&observationID, &locatorJSON, &contentState, &content, &contentHash,
		&contentBytes, &contentCharacters, &truncated, &classification,
		&findingsJSON, &persistDecision, &transferDecision, &redactionReason,
	); err != nil {
		return query.EvidenceInspection{}, err
	}
	if storedOrganizationID != expectedOrganizationID {
		return query.EvidenceInspection{}, fmt.Errorf("%w: evidence organization mismatch", ErrInconsistent)
	}
	if contentState == nil {
		return query.EvidenceInspection{}, fmt.Errorf("%w: stored evidence content state", ErrInconsistent)
	}
	if err := validateStoredEvidence(id, sourceID, snapshotID, artifactID, observationID, locatorJSON, *contentState, content, contentHash, contentBytes, contentCharacters, classification, findingsJSON, persistDecision, transferDecision, redactionReason); err != nil {
		return query.EvidenceInspection{}, err
	}
	var locator contract.Locator
	if err := json.Unmarshal(locatorJSON, &locator); err != nil {
		return query.EvidenceInspection{}, fmt.Errorf("%w: stored evidence locator", ErrInconsistent)
	}
	return query.EvidenceInspection{
		ID:                id,
		OrganizationID:    storedOrganizationID,
		SourceID:          sourceID,
		SnapshotID:        snapshotID,
		ArtifactID:        artifactID,
		ObservationID:     optionalString(observationID),
		Locator:           locator,
		ContentState:      evidence.ContentState(*contentState),
		Classification:    evidence.Classification(optionalString(classification)),
		Persist:           evidence.Decision(persistDecision),
		ExternalTransfer:  evidence.Decision(transferDecision),
		Content:           optionalString(content),
		ContentHash:       *contentHash,
		ContentBytes:      contentBytes,
		ContentCharacters: contentCharacters,
		Truncated:         truncated,
	}, nil
}

func validateStoredEvidence(id string, sourceID, snapshotID, artifactID string, observationID *string, locatorJSON []byte, contentState string, content *string, contentHash *string, contentBytes, contentCharacters int64, classification *string, findingsJSON []byte, persistDecision, transferDecision string, redactionReason *string) error {
	for name, value := range map[string]string{
		"evidence id": id, "evidence source_id": sourceID,
		"evidence snapshot_id": snapshotID, "evidence artifact_id": artifactID,
	} {
		if err := validateUUID(name, value); err != nil {
			return fmt.Errorf("%w: stored %s", ErrInconsistent, name)
		}
	}
	if observationID != nil {
		if err := validateUUID("evidence observation_id", *observationID); err != nil {
			return fmt.Errorf("%w: stored evidence observation", ErrInconsistent)
		}
	}
	trimmedLocator := strings.TrimSpace(string(locatorJSON))
	if trimmedLocator == "" || trimmedLocator[0] != '{' {
		return fmt.Errorf("%w: stored evidence locator", ErrInconsistent)
	}
	var locator contract.Locator
	if err := json.Unmarshal(locatorJSON, &locator); err != nil || locator.Validate() != nil {
		return fmt.Errorf("%w: stored evidence locator", ErrInconsistent)
	}
	if locator.SourceID != "" && locator.SourceID != sourceID || locator.ArtifactID != "" && locator.ArtifactID != artifactID {
		return fmt.Errorf("%w: stored evidence locator scope", ErrInconsistent)
	}
	state := evidence.ContentState(contentState)
	if state.Validate() != nil || contentHash == nil || validateDigest("evidence content_hash", *contentHash) != nil {
		return fmt.Errorf("%w: stored evidence content metadata", ErrInconsistent)
	}
	class := evidence.Classification("")
	if classification != nil {
		class = evidence.Classification(*classification)
	}
	if classification == nil || class.Validate() != nil || evidence.Decision(persistDecision).Validate() != nil || evidence.Decision(transferDecision).Validate() != nil {
		return fmt.Errorf("%w: stored evidence policy metadata", ErrInconsistent)
	}
	if contentBytes < 0 || contentCharacters < 0 {
		return fmt.Errorf("%w: stored evidence counts", ErrInconsistent)
	}
	if len(findingsJSON) == 0 || !json.Valid(findingsJSON) {
		return fmt.Errorf("%w: stored evidence findings", ErrInconsistent)
	}
	var findings []string
	if err := json.Unmarshal(findingsJSON, &findings); err != nil || findings == nil {
		return fmt.Errorf("%w: stored evidence findings", ErrInconsistent)
	}
	if err := validateStoredFindings(class, findings); err != nil {
		return err
	}
	value := optionalString(content)
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: stored evidence content encoding", ErrInconsistent)
	}
	if state == evidence.ContentStateOmitted {
		if content != nil || contentBytes != 0 || contentCharacters != 0 {
			return fmt.Errorf("%w: omitted evidence carries content", ErrInconsistent)
		}
	} else {
		if content == nil || value == "" || int64(len([]byte(value))) != contentBytes || int64(utf8.RuneCountInString(value)) != contentCharacters {
			return fmt.Errorf("%w: stored evidence content counts", ErrInconsistent)
		}
		if state == evidence.ContentStatePresent && evidence.ContentDigest(value) != *contentHash {
			return fmt.Errorf("%w: stored evidence content digest", ErrInconsistent)
		}
		if state == evidence.ContentStateRedacted && (value != evidence.RedactedContent || redactionReason == nil || strings.TrimSpace(*redactionReason) == "") {
			return fmt.Errorf("%w: stored evidence redaction", ErrInconsistent)
		}
	}
	if state == evidence.ContentStatePresent && class != evidence.ClassificationUnknown && class != evidence.ClassificationSafeText {
		return fmt.Errorf("%w: unsafe stored evidence content", ErrInconsistent)
	}
	if evidence.Decision(persistDecision) == evidence.DecisionDeny && (state != evidence.ContentStateOmitted || content != nil || contentBytes != 0 || contentCharacters != 0) {
		return fmt.Errorf("%w: denied evidence carries content", ErrInconsistent)
	}
	if evidence.Decision(persistDecision) == evidence.DecisionRedact && state == evidence.ContentStatePresent {
		return fmt.Errorf("%w: redacted evidence is present", ErrInconsistent)
	}
	if (class == evidence.ClassificationBinary || class == evidence.ClassificationInvalid || class == evidence.ClassificationProhibited) && state != evidence.ContentStateOmitted {
		return fmt.Errorf("%w: restricted evidence is not omitted", ErrInconsistent)
	}
	if evidence.Decision(transferDecision) == evidence.DecisionAllow && class != evidence.ClassificationSafeText {
		return fmt.Errorf("%w: unsafe evidence transfer", ErrInconsistent)
	}
	return nil
}

func validateStoredFindings(classification evidence.Classification, findings []string) error {
	known := map[string]struct{}{
		evidence.FindingSecret: {}, evidence.FindingSecretAssignment: {}, evidence.FindingPEMPrivateKey: {},
		evidence.FindingAuthorization: {}, evidence.FindingBearer: {}, evidence.FindingPromptInjection: {},
		evidence.FindingBinary: {}, evidence.FindingInvalidUTF8: {}, evidence.FindingProhibited: {},
	}
	seen := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		if _, ok := known[finding]; !ok || finding == "" {
			return fmt.Errorf("%w: stored evidence finding", ErrInconsistent)
		}
		if _, duplicate := seen[finding]; duplicate {
			return fmt.Errorf("%w: duplicate stored evidence finding", ErrInconsistent)
		}
		seen[finding] = struct{}{}
	}
	if classification == evidence.ClassificationUnknown && len(findings) != 0 {
		return fmt.Errorf("%w: unknown evidence classification has findings", ErrInconsistent)
	}
	return nil
}

func queryNullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
