package persistence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

// JobStore is the PostgreSQL implementation of ingestion.JobStore. Every
// mutation carries the organization and, where applicable, the lease owner;
// no job payload or driver diagnostic is copied into returned errors.
type JobStore struct {
	db jobDatabase
}

type jobDatabase interface {
	Begin(context.Context) (pgx.Tx, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type poolJobDatabase struct{ pool *pgxpool.Pool }

func (d poolJobDatabase) Begin(ctx context.Context) (pgx.Tx, error) {
	return d.pool.Begin(ctx)
}

func (d poolJobDatabase) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return d.pool.Exec(ctx, query, args...)
}

func (d poolJobDatabase) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return d.pool.QueryRow(ctx, query, args...)
}

type connJobDatabase struct{ conn *pgx.Conn }

// jobQueryer is the read-only portion shared by a pool/connection database,
// and by a transaction. Canonical source/snapshot rows are intentionally
// resolved through this narrow port so accepting an ingestion job never needs
// to depend on a source bundle having been persisted already.
type jobQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const jobStoreCleanupTimeout = 5 * time.Second

func (d connJobDatabase) Begin(ctx context.Context) (pgx.Tx, error) {
	return d.conn.Begin(ctx)
}

func (d connJobDatabase) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	return d.conn.Exec(ctx, query, args...)
}

func (d connJobDatabase) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	return d.conn.QueryRow(ctx, query, args...)
}

// NewJobStore creates a job store backed by a pool. It does not open, ping,
// or otherwise mutate the pool during construction.
func NewJobStore(pool *pgxpool.Pool) *JobStore {
	if pool == nil {
		return &JobStore{}
	}
	return &JobStore{db: poolJobDatabase{pool: pool}}
}

// NewConnJobStore creates a job store backed by one *pgx.Conn. Callers own
// the connection lifecycle; operations still use short explicit transactions
// where an atomic claim requires them.
func NewConnJobStore(conn *pgx.Conn) *JobStore {
	if conn == nil {
		return &JobStore{}
	}
	return &JobStore{db: connJobDatabase{conn: conn}}
}

var _ ingestion.JobStore = (*JobStore)(nil)

const jobColumns = `
    id::text,
    organization_id::text,
    source_id::text,
    snapshot_id::text,
    source_external_id,
    snapshot_external_id,
    factual_digest,
    analysis_configuration_id,
    state,
    stage,
    attempt_count,
    lease_owner,
    lease_expires_at,
    cancel_requested,
    diagnostic_code,
    diagnostic_message,
    artifact_count,
    observation_count,
    evidence_count,
    failure_count,
    created_at,
    started_at,
    finished_at`

const insertJobSQL = `INSERT INTO ingestion_jobs (
    id, organization_id, source_id, snapshot_id, source_external_id,
    snapshot_external_id, factual_digest, analysis_configuration_id
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (organization_id, source_external_id, snapshot_external_id, factual_digest)
DO NOTHING
RETURNING ` + jobColumns

const insertJobStageSQL = `INSERT INTO ingestion_job_stages (
    id, organization_id, job_id, stage, state, attempt_count, created_at
) VALUES ($1, $2, $3, $4, $5, 0, $6)
ON CONFLICT (organization_id, job_id, stage) DO NOTHING`

const startJobStageSQL = `UPDATE ingestion_job_stages
SET state = 'running',
    attempt_count = attempt_count + 1,
    lease_owner = $2,
    lease_expires_at = $3,
    started_at = COALESCE(started_at, $4),
    finished_at = NULL,
    diagnostic_code = NULL,
    diagnostic_message = NULL
WHERE organization_id = $1 AND job_id = $5 AND stage = $6`

const refreshJobStageSQL = `UPDATE ingestion_job_stages
SET state = 'running',
    item_count = $3,
    error_count = $4,
    lease_owner = $2,
    lease_expires_at = $5
WHERE organization_id = $1 AND job_id = $6 AND stage = $7`

const completeJobStageSQL = `UPDATE ingestion_job_stages
SET state = $4,
    item_count = $5,
    error_count = $6,
    lease_owner = NULL,
    lease_expires_at = NULL,
    diagnostic_code = $7,
    diagnostic_message = $8,
    finished_at = $9
WHERE organization_id = $1 AND job_id = $2 AND stage = $3`

const selectJobSQL = `SELECT ` + jobColumns + ` FROM ingestion_jobs WHERE organization_id = $1 AND id = $2`

const selectJobForUpdateSQL = selectJobSQL + ` FOR UPDATE`

const selectJobIdentitySQL = `SELECT ` + jobColumns + ` FROM ingestion_jobs
WHERE organization_id = $1 AND source_external_id = $2 AND snapshot_external_id = $3 AND factual_digest = $4`

const selectCanonicalJobScopeSQL = `
SELECT source.id::text, snapshot.id::text
FROM sources AS source
LEFT JOIN analysis_snapshots AS snapshot
  ON snapshot.organization_id = source.organization_id
 AND snapshot.source_id = source.id
 AND snapshot.external_id = $3
WHERE source.organization_id = $1
  AND source.external_id = $2`

const claimJobSQL = `WITH candidate AS (
    SELECT id
    FROM ingestion_jobs
    WHERE organization_id = $1
      AND cancel_requested = false
      AND (
          state = 'pending'
          OR (state = 'running' AND (lease_expires_at IS NULL OR lease_expires_at <= $3))
      )
    ORDER BY created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT 1
)
UPDATE ingestion_jobs AS job
SET state = 'running',
    stage = CASE WHEN job.state = 'running' THEN job.stage ELSE 'validation' END,
    attempt_count = job.attempt_count + 1,
    lease_owner = $2,
    lease_expires_at = $4,
    started_at = COALESCE(job.started_at, $3),
    finished_at = NULL,
    diagnostic_code = NULL,
    diagnostic_message = NULL
FROM candidate
WHERE job.organization_id = $1 AND job.id = candidate.id
RETURNING job.id::text,
    job.organization_id::text,
    job.source_id::text,
    job.snapshot_id::text,
    job.source_external_id,
    job.snapshot_external_id,
    job.factual_digest,
    job.analysis_configuration_id,
    job.state,
    job.stage,
    job.attempt_count,
    job.lease_owner,
    job.lease_expires_at,
    job.cancel_requested,
    job.diagnostic_code,
    job.diagnostic_message,
    job.artifact_count,
    job.observation_count,
    job.evidence_count,
    job.failure_count,
    job.created_at,
    job.started_at,
    job.finished_at`

const heartbeatJobSQL = `UPDATE ingestion_jobs
SET lease_expires_at = $4
WHERE organization_id = $1 AND id = $2 AND state = 'running'
  AND lease_owner = $3 AND (lease_expires_at IS NULL OR lease_expires_at > $5)
RETURNING ` + jobColumns

const heartbeatJobStageSQL = `UPDATE ingestion_job_stages
SET lease_expires_at = $3
WHERE organization_id = $1 AND job_id = $2 AND stage = $4 AND lease_owner = $5`

const advanceStageSQL = `UPDATE ingestion_jobs
SET stage = $4,
	artifact_count = $5,
	observation_count = $6,
	evidence_count = $7,
	failure_count = $8
WHERE organization_id = $1 AND id = $2 AND state = 'running'
	AND lease_owner = $3 AND (lease_expires_at IS NULL OR lease_expires_at > $9)
	AND (
		stage = $4
		OR (stage = 'validation' AND $4 = 'canonical_persistence')
		OR (stage = 'canonical_persistence' AND $4 = 'textual_projection')
		OR (stage = 'textual_projection' AND $4 = 'relational_projection')
		OR (stage = 'relational_projection' AND $4 = 'embedding_projection')
		OR (stage = 'embedding_projection' AND $4 = 'activation')
	)
	AND artifact_count <= $5 AND observation_count <= $6
	AND evidence_count <= $7 AND failure_count <= $8
RETURNING ` + jobColumns

const completeJobSQL = `UPDATE ingestion_jobs
SET state = 'completed',
    stage = 'activation',
    artifact_count = $4,
    observation_count = $5,
    evidence_count = $6,
    failure_count = $7,
    lease_owner = NULL,
    lease_expires_at = NULL,
    finished_at = $8,
    diagnostic_code = NULL,
    diagnostic_message = NULL
WHERE organization_id = $1 AND id = $2 AND state = 'running'
  AND lease_owner = $3 AND (lease_expires_at IS NULL OR lease_expires_at > $9)
  AND stage = 'activation'
  AND artifact_count <= $4 AND observation_count <= $5
  AND evidence_count <= $6 AND failure_count <= $7
RETURNING ` + jobColumns

const partialJobSQL = `UPDATE ingestion_jobs
SET state = 'partial',
    stage = $4,
    artifact_count = $5,
    observation_count = $6,
    evidence_count = $7,
    failure_count = $8,
    diagnostic_code = $9,
    diagnostic_message = $10,
    lease_owner = NULL,
    lease_expires_at = NULL,
    finished_at = $11
WHERE organization_id = $1 AND id = $2 AND state = 'running'
  AND lease_owner = $3 AND (lease_expires_at IS NULL OR lease_expires_at > $12)
  AND (
      stage = $4
      OR (stage = 'validation' AND $4 = 'canonical_persistence')
      OR (stage = 'canonical_persistence' AND $4 = 'textual_projection')
      OR (stage = 'textual_projection' AND $4 = 'relational_projection')
      OR (stage = 'relational_projection' AND $4 = 'embedding_projection')
      OR (stage = 'embedding_projection' AND $4 = 'activation')
  )
  AND artifact_count <= $5 AND observation_count <= $6
  AND evidence_count <= $7 AND failure_count <= $8
RETURNING ` + jobColumns

const resumePartialJobSQL = `UPDATE ingestion_jobs
SET state = 'running',
    attempt_count = attempt_count + 1,
    lease_owner = $3,
    lease_expires_at = $4,
    finished_at = NULL,
    diagnostic_code = NULL,
    diagnostic_message = NULL
WHERE organization_id = $1 AND id = $2 AND state = 'partial'
  AND stage = 'embedding_projection' AND cancel_requested = false
RETURNING ` + jobColumns

const failJobSQL = `UPDATE ingestion_jobs
SET state = 'failed',
    diagnostic_code = $5,
    diagnostic_message = $6,
    lease_owner = NULL,
    lease_expires_at = NULL,
    finished_at = $7
WHERE organization_id = $1 AND id = $2 AND state = 'running'
  AND lease_owner = $3 AND (lease_expires_at IS NULL OR lease_expires_at > $4)
RETURNING ` + jobColumns

const cancelJobSQL = `UPDATE ingestion_jobs
SET state = 'failed',
    cancel_requested = true,
    diagnostic_code = 'cancelled',
    diagnostic_message = 'job cancelled',
    lease_owner = NULL,
    lease_expires_at = NULL,
    finished_at = $3
WHERE organization_id = $1 AND id = $2 AND state IN ('pending', 'running')
RETURNING ` + jobColumns

func (s *JobStore) Create(ctx context.Context, job ingestion.Job) (ingestion.Job, error) {
	if err := validateJobContext(ctx); err != nil {
		return ingestion.Job{}, err
	}
	if err := normalizeNewJob(&job); err != nil {
		return ingestion.Job{}, err
	}
	if err := validateDatabaseJob(job); err != nil {
		return ingestion.Job{}, err
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "create job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	organizationExternalID := strings.TrimSpace(job.OrganizationExternalID)
	if organizationExternalID == "" {
		organizationExternalID = job.OrganizationID
	}
	organizationName := strings.TrimSpace(job.OrganizationName)
	if organizationName == "" {
		organizationName = organizationExternalID
	}
	if err := (&UnitOfWork{tx: tx}).EnsureOrganization(ctx, job.OrganizationID, Organization{
		ID:         job.OrganizationID,
		ExternalID: organizationExternalID,
		Name:       organizationName,
	}); err != nil {
		if errors.Is(err, ErrConflict) {
			return ingestion.Job{}, ingestion.ErrJobConflict
		}
		if errors.Is(err, ErrInvalidInput) {
			return ingestion.Job{}, ingestion.ErrInvalidJob
		}
		return ingestion.Job{}, ingestion.ErrJobStore
	}
	insertSourceID, insertSnapshotID, scopeErr := resolveCanonicalJobScope(ctx, tx, job)
	if scopeErr != nil {
		return ingestion.Job{}, scopeErr
	}
	row := tx.QueryRow(ctx, insertJobSQL, job.ID, job.OrganizationID, nullableUUID(insertSourceID), nullableUUID(insertSnapshotID),
		job.SourceExternalID, job.SnapshotExternalID, job.FactualDigest, job.AnalysisConfigurationID)
	created, scanErr := scanJob(row)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		created, scanErr = scanJob(tx.QueryRow(ctx, selectJobIdentitySQL, job.OrganizationID, job.SourceExternalID,
			job.SnapshotExternalID, job.FactualDigest))
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ingestion.Job{}, fmt.Errorf("%w: job identity disappeared", ingestion.ErrJobStore)
		}
		if scanErr != nil {
			return ingestion.Job{}, normalizeDatabaseError(ctx, "read job identity", scanErr)
		}
		if err := hydrateCanonicalJobScope(ctx, tx, &created); err != nil {
			return ingestion.Job{}, err
		}
		if !sameDatabaseIdentity(created, job) {
			return ingestion.Job{}, ingestion.ErrJobConflict
		}
	} else if scanErr != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "create job", scanErr)
	}
	if err := hydrateCanonicalJobScope(ctx, tx, &created); err != nil {
		return ingestion.Job{}, err
	}
	if err := ensureJobStageTx(ctx, tx, created.OrganizationID, created.ID, created.Stage, "pending", created.CreatedAt); err != nil {
		return ingestion.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "create job", err)
	}
	committed = true
	return created, nil
}

func (s *JobStore) Get(ctx context.Context, organizationID, jobID string) (ingestion.Job, error) {
	if err := validateJobContext(ctx); err != nil {
		return ingestion.Job{}, err
	}
	if err := validateUUIDText(organizationID); err != nil {
		return ingestion.Job{}, fmt.Errorf("%w: organization id is invalid", ingestion.ErrInvalidJob)
	}
	if err := validateUUIDText(jobID); err != nil {
		return ingestion.Job{}, fmt.Errorf("%w: job id is invalid", ingestion.ErrInvalidJob)
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	job, err := scanJob(s.db.QueryRow(ctx, selectJobSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "read job", err)
	}
	if err := hydrateCanonicalJobScope(ctx, s.db, &job); err != nil {
		return ingestion.Job{}, err
	}
	return job, nil
}

func (s *JobStore) Claim(ctx context.Context, organizationID, owner string, lease time.Duration) (ingestion.Job, bool, error) {
	if err := validateJobContext(ctx); err != nil {
		return ingestion.Job{}, false, err
	}
	if err := validateUUIDText(organizationID); err != nil {
		return ingestion.Job{}, false, fmt.Errorf("%w: organization id is invalid", ingestion.ErrInvalidJob)
	}
	if err := validateLeaseInput(owner, lease); err != nil {
		return ingestion.Job{}, false, err
	}
	if !s.configured() {
		return ingestion.Job{}, false, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	expires := now.Add(lease)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, false, normalizeDatabaseError(ctx, "claim job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	job, err := scanJob(tx.QueryRow(ctx, claimJobSQL, organizationID, owner, now, expires))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, false, nil
	}
	if err != nil {
		return ingestion.Job{}, false, normalizeDatabaseError(ctx, "claim job", err)
	}
	if err := hydrateCanonicalJobScope(ctx, tx, &job); err != nil {
		return ingestion.Job{}, false, err
	}
	if err := ensureJobStageTx(ctx, tx, job.OrganizationID, job.ID, job.Stage, "pending", job.CreatedAt); err != nil {
		return ingestion.Job{}, false, err
	}
	if err := startJobStageTx(ctx, tx, job, owner, &expires, now); err != nil {
		return ingestion.Job{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, false, normalizeDatabaseError(ctx, "claim job", err)
	}
	committed = true
	return job, true, nil
}

func (s *JobStore) Heartbeat(ctx context.Context, organizationID, jobID, owner string, lease time.Duration) (ingestion.Job, error) {
	if err := validateMutation(ctx, organizationID, jobID, owner, lease); err != nil {
		return ingestion.Job{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(lease)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "heartbeat job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	job, err := scanJob(tx.QueryRow(ctx, heartbeatJobSQL, organizationID, jobID, owner, expires, now))
	if errors.Is(err, pgx.ErrNoRows) {
		current, lookupErr := scanJob(tx.QueryRow(ctx, selectJobSQL, organizationID, jobID))
		if errors.Is(lookupErr, pgx.ErrNoRows) {
			return ingestion.Job{}, ingestion.ErrJobNotFound
		}
		if lookupErr != nil {
			return ingestion.Job{}, normalizeDatabaseError(ctx, "read heartbeat job", lookupErr)
		}
		return classifyLockedMutation(current)
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "heartbeat job", err)
	}
	tag, err := tx.Exec(ctx, heartbeatJobStageSQL, organizationID, jobID, expires, job.Stage, owner)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "heartbeat job stage", err)
	}
	if tag.RowsAffected() != 1 {
		return ingestion.Job{}, fmt.Errorf("%w: heartbeat job stage affected no rows", ingestion.ErrJobStore)
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "heartbeat job", err)
	}
	committed = true
	return job, nil
}

func (s *JobStore) AdvanceStage(ctx context.Context, organizationID, jobID, owner string, stage ingestion.JobStage, counts ingestion.JobCounts) (ingestion.Job, error) {
	if err := validateMutationContext(ctx, organizationID, jobID, owner); err != nil {
		return ingestion.Job{}, err
	}
	if !stage.Valid() {
		return ingestion.Job{}, fmt.Errorf("%w: stage is invalid", ingestion.ErrInvalidJob)
	}
	if err := counts.Validate(); err != nil {
		return ingestion.Job{}, err
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "advance job stage", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	current, err := scanJob(tx.QueryRow(ctx, selectJobForUpdateSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "advance job stage", err)
	}
	if err := activeJobLease(current, owner, now); err != nil {
		return ingestion.Job{}, err
	}
	if !databaseStageTransitionAllowed(current.Stage, stage) {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	if !databaseCountsMonotonic(current.JobCounts, counts) {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	job, err := scanJob(tx.QueryRow(ctx, advanceStageSQL, organizationID, jobID, owner, stage,
		counts.ArtifactCount, counts.ObservationCount, counts.EvidenceCount, counts.FailureCount, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "advance job stage", err)
	}
	if err := ensureJobStageTx(ctx, tx, job.OrganizationID, job.ID, stage, "pending", job.CreatedAt); err != nil {
		return ingestion.Job{}, err
	}
	if stage == current.Stage {
		if err := refreshJobStageTx(ctx, tx, job, owner, counts); err != nil {
			return ingestion.Job{}, err
		}
	} else {
		if err := completeJobStageTx(ctx, tx, current, current.Stage, counts, ingestion.Diagnostic{}, now); err != nil {
			return ingestion.Job{}, err
		}
		if err := startJobStageTx(ctx, tx, job, owner, job.LeaseExpiresAt, now); err != nil {
			return ingestion.Job{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "advance job stage", err)
	}
	committed = true
	return job, nil
}

func (s *JobStore) Complete(ctx context.Context, organizationID, jobID, owner string, counts ingestion.JobCounts) (ingestion.Job, error) {
	if err := validateMutationContext(ctx, organizationID, jobID, owner); err != nil {
		return ingestion.Job{}, err
	}
	if err := counts.Validate(); err != nil {
		return ingestion.Job{}, err
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "complete job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	current, err := scanJob(tx.QueryRow(ctx, selectJobForUpdateSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "complete job", err)
	}
	if err := activeJobLease(current, owner, now); err != nil {
		return ingestion.Job{}, err
	}
	if current.Stage != ingestion.JobStageActivation {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	job, err := scanJob(tx.QueryRow(ctx, completeJobSQL, organizationID, jobID, owner,
		counts.ArtifactCount, counts.ObservationCount, counts.EvidenceCount, counts.FailureCount, now, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyLockedMutation(current)
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "complete job", err)
	}
	if err := completeJobStageTx(ctx, tx, current, current.Stage, counts, ingestion.Diagnostic{}, now); err != nil {
		return ingestion.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "complete job", err)
	}
	committed = true
	return job, nil
}

func (s *JobStore) Partial(ctx context.Context, organizationID, jobID, owner string, stage ingestion.JobStage, diagnostic ingestion.Diagnostic, counts ingestion.JobCounts) (ingestion.Job, error) {
	if err := validateMutationContext(ctx, organizationID, jobID, owner); err != nil {
		return ingestion.Job{}, err
	}
	if !stage.Valid() {
		return ingestion.Job{}, fmt.Errorf("%w: stage is invalid", ingestion.ErrInvalidJob)
	}
	if err := diagnostic.Validate(); err != nil {
		return ingestion.Job{}, err
	}
	if err := counts.Validate(); err != nil {
		return ingestion.Job{}, err
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "mark partial job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	current, err := scanJob(tx.QueryRow(ctx, selectJobForUpdateSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "mark partial job", err)
	}
	if err := activeJobLease(current, owner, now); err != nil {
		return ingestion.Job{}, err
	}
	if !databaseStageTransitionAllowed(current.Stage, stage) || !databaseCountsMonotonic(current.JobCounts, counts) {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	job, err := scanJob(tx.QueryRow(ctx, partialJobSQL, organizationID, jobID, owner, stage,
		counts.ArtifactCount, counts.ObservationCount, counts.EvidenceCount, counts.FailureCount,
		diagnostic.Code, diagnostic.Message, now, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyLockedMutation(current)
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "mark partial job", err)
	}
	if current.Stage != stage {
		if err := completeJobStageTx(ctx, tx, current, current.Stage, counts, ingestion.Diagnostic{}, now); err != nil {
			return ingestion.Job{}, err
		}
	}
	if err := finishJobStageTx(ctx, tx, job, stage, "partial", counts, diagnostic, now); err != nil {
		return ingestion.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "mark partial job", err)
	}
	committed = true
	return job, nil
}

// ResumePartial claims only a partial embedding projection. Canonical rows
// and non-vector projections remain untouched; the caller can retry the
// embedding stage from durable evidence without the original bundle.
func (s *JobStore) ResumePartial(ctx context.Context, organizationID, jobID, owner string, lease time.Duration) (ingestion.Job, error) {
	if err := validateMutationContext(ctx, organizationID, jobID, owner); err != nil {
		return ingestion.Job{}, err
	}
	if lease <= 0 {
		return ingestion.Job{}, fmt.Errorf("%w: lease duration must be positive", ingestion.ErrInvalidJob)
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	expires := now.Add(lease)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "resume partial job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	current, err := scanJob(tx.QueryRow(ctx, selectJobForUpdateSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "read partial job", err)
	}
	if err := hydrateCanonicalJobScope(ctx, tx, &current); err != nil {
		return ingestion.Job{}, err
	}
	if current.State != ingestion.JobStatePartial || current.Stage != ingestion.JobStageEmbeddingProjection || current.CancelRequested {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	job, err := scanJob(tx.QueryRow(ctx, resumePartialJobSQL, organizationID, jobID, owner, expires))
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyLockedMutation(current)
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "resume partial job", err)
	}
	if err := hydrateCanonicalJobScope(ctx, tx, &job); err != nil {
		return ingestion.Job{}, err
	}
	if err := startJobStageTx(ctx, tx, job, owner, job.LeaseExpiresAt, now); err != nil {
		return ingestion.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "resume partial job", err)
	}
	committed = true
	return job, nil
}

func (s *JobStore) Fail(ctx context.Context, organizationID, jobID, owner string, diagnostic ingestion.Diagnostic) (ingestion.Job, error) {
	if err := validateMutationContext(ctx, organizationID, jobID, owner); err != nil {
		return ingestion.Job{}, err
	}
	if err := diagnostic.Validate(); err != nil {
		return ingestion.Job{}, err
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "fail job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	current, err := scanJob(tx.QueryRow(ctx, selectJobForUpdateSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "fail job", err)
	}
	if err := activeJobLease(current, owner, now); err != nil {
		return ingestion.Job{}, err
	}
	job, err := scanJob(tx.QueryRow(ctx, failJobSQL, organizationID, jobID, owner, now,
		diagnostic.Code, diagnostic.Message, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyLockedMutation(current)
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "fail job", err)
	}
	if err := finishJobStageTx(ctx, tx, job, current.Stage, "failed", current.JobCounts, diagnostic, now); err != nil {
		return ingestion.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "fail job", err)
	}
	committed = true
	return job, nil
}

func (s *JobStore) Cancel(ctx context.Context, organizationID, jobID string) (ingestion.Job, error) {
	if err := validateJobContext(ctx); err != nil {
		return ingestion.Job{}, err
	}
	if err := validateUUIDText(organizationID); err != nil {
		return ingestion.Job{}, fmt.Errorf("%w: organization id is invalid", ingestion.ErrInvalidJob)
	}
	if err := validateUUIDText(jobID); err != nil {
		return ingestion.Job{}, fmt.Errorf("%w: job id is invalid", ingestion.ErrInvalidJob)
	}
	if !s.configured() {
		return ingestion.Job{}, fmt.Errorf("%w: job store is not configured", ingestion.ErrJobStore)
	}
	now := time.Now().UTC()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "cancel job", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackJobTx(tx, ctx)
		}
	}()
	current, err := scanJob(tx.QueryRow(ctx, selectJobForUpdateSQL, organizationID, jobID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestion.Job{}, ingestion.ErrJobNotFound
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "cancel job", err)
	}
	if current.State == ingestion.JobStateFailed && current.CancelRequested {
		if err := tx.Commit(ctx); err != nil {
			return ingestion.Job{}, normalizeDatabaseError(ctx, "cancel job", err)
		}
		committed = true
		return current, nil
	}
	if current.State != ingestion.JobStatePending && current.State != ingestion.JobStateRunning {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	job, err := scanJob(tx.QueryRow(ctx, cancelJobSQL, organizationID, jobID, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return classifyLockedMutation(current)
	}
	if err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "cancel job", err)
	}
	if err := finishJobStageTx(ctx, tx, job, current.Stage, "failed", current.JobCounts,
		ingestion.Diagnostic{Code: ingestion.DiagnosticCodeCancelled, Message: "job cancelled"}, now); err != nil {
		return ingestion.Job{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ingestion.Job{}, normalizeDatabaseError(ctx, "cancel job", err)
	}
	committed = true
	return job, nil
}

func classifyLockedMutation(job ingestion.Job) (ingestion.Job, error) {
	if job.State == ingestion.JobStateFailed && job.CancelRequested {
		return ingestion.Job{}, ingestion.ErrJobCancelled
	}
	if job.State != ingestion.JobStateRunning {
		return ingestion.Job{}, ingestion.ErrJobState
	}
	return ingestion.Job{}, ingestion.ErrJobLeaseLost
}

func activeJobLease(job ingestion.Job, owner string, now time.Time) error {
	if job.State == ingestion.JobStateFailed && job.CancelRequested {
		return ingestion.ErrJobCancelled
	}
	if job.State != ingestion.JobStateRunning {
		return ingestion.ErrJobState
	}
	if job.LeaseOwner != owner || job.LeaseExpiresAt == nil || !now.Before(*job.LeaseExpiresAt) {
		return ingestion.ErrJobLeaseLost
	}
	return nil
}

func databaseStageTransitionAllowed(from, to ingestion.JobStage) bool {
	if from == to {
		return true
	}
	transitions := map[ingestion.JobStage]ingestion.JobStage{
		ingestion.JobStageValidation:           ingestion.JobStageCanonicalPersistence,
		ingestion.JobStageCanonicalPersistence: ingestion.JobStageTextualProjection,
		ingestion.JobStageTextualProjection:    ingestion.JobStageRelationalProjection,
		ingestion.JobStageRelationalProjection: ingestion.JobStageEmbeddingProjection,
		ingestion.JobStageEmbeddingProjection:  ingestion.JobStageActivation,
	}
	return transitions[from] == to
}

func databaseCountsMonotonic(previous, next ingestion.JobCounts) bool {
	return next.ArtifactCount >= previous.ArtifactCount && next.ObservationCount >= previous.ObservationCount &&
		next.EvidenceCount >= previous.EvidenceCount && next.FailureCount >= previous.FailureCount
}

func (s *JobStore) configured() bool {
	return s != nil && s.db != nil
}

func scanJob(row pgx.Row) (ingestion.Job, error) {
	var (
		job                                   ingestion.Job
		sourceID, snapshotID, leaseOwner      *string
		diagnosticCode, diagnosticMessage     *string
		leaseExpiresAt, startedAt, finishedAt *time.Time
		state, stage                          string
	)
	err := row.Scan(
		&job.ID, &job.OrganizationID, &sourceID, &snapshotID,
		&job.SourceExternalID, &job.SnapshotExternalID, &job.FactualDigest,
		&job.AnalysisConfigurationID, &state, &stage, &job.AttemptCount,
		&leaseOwner, &leaseExpiresAt, &job.CancelRequested, &diagnosticCode,
		&diagnosticMessage, &job.ArtifactCount, &job.ObservationCount,
		&job.EvidenceCount, &job.FailureCount, &job.CreatedAt, &startedAt, &finishedAt,
	)
	if err != nil {
		return ingestion.Job{}, err
	}
	job.SourceID = optionalJobString(sourceID)
	job.SnapshotID = optionalJobString(snapshotID)
	job.LeaseOwner = optionalJobString(leaseOwner)
	job.LeaseExpiresAt = leaseExpiresAt
	job.DiagnosticCode = optionalJobString(diagnosticCode)
	job.DiagnosticMessage = optionalJobString(diagnosticMessage)
	job.StartedAt = startedAt
	job.FinishedAt = finishedAt
	job.State = ingestion.JobState(state)
	job.Stage = ingestion.JobStage(stage)
	if err := job.Validate(); err != nil {
		return ingestion.Job{}, ingestion.ErrJobStore
	}
	return job, nil
}

func ensureJobStageTx(ctx context.Context, tx pgx.Tx, organizationID, jobID string, stage ingestion.JobStage, state string, createdAt time.Time) error {
	if !stage.Valid() || !validStageState(state) {
		return fmt.Errorf("%w: stage state is invalid", ingestion.ErrInvalidJob)
	}
	_, err := tx.Exec(ctx, insertJobStageSQL, jobStageID(organizationID, jobID, stage), organizationID, jobID, stage, state, createdAt)
	if err != nil {
		return normalizeDatabaseError(ctx, "ensure job stage", err)
	}
	return nil
}

func startJobStageTx(ctx context.Context, tx pgx.Tx, job ingestion.Job, owner string, expires *time.Time, now time.Time) error {
	var leaseExpires any
	if expires != nil {
		leaseExpires = *expires
	}
	tag, err := tx.Exec(ctx, startJobStageSQL, job.OrganizationID, owner, leaseExpires, now, job.ID, job.Stage)
	if err != nil {
		return normalizeDatabaseError(ctx, "start job stage", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: start job stage affected no rows", ingestion.ErrJobStore)
	}
	return nil
}

func refreshJobStageTx(ctx context.Context, tx pgx.Tx, job ingestion.Job, owner string, counts ingestion.JobCounts) error {
	var leaseExpires any
	if job.LeaseExpiresAt != nil {
		leaseExpires = *job.LeaseExpiresAt
	}
	tag, err := tx.Exec(ctx, refreshJobStageSQL, job.OrganizationID, owner, totalJobItems(counts), counts.FailureCount,
		leaseExpires, job.ID, job.Stage)
	if err != nil {
		return normalizeDatabaseError(ctx, "refresh job stage", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: refresh job stage affected no rows", ingestion.ErrJobStore)
	}
	return nil
}

func completeJobStageTx(ctx context.Context, tx pgx.Tx, job ingestion.Job, stage ingestion.JobStage, counts ingestion.JobCounts, diagnostic ingestion.Diagnostic, finishedAt time.Time) error {
	if !stage.Valid() || !validStageState("completed") {
		return fmt.Errorf("%w: stage state is invalid", ingestion.ErrInvalidJob)
	}
	if err := ensureJobStageTx(ctx, tx, job.OrganizationID, job.ID, stage, "pending", job.CreatedAt); err != nil {
		return err
	}
	code, message := nullableDiagnostic(diagnostic)
	tag, err := tx.Exec(ctx, completeJobStageSQL, job.OrganizationID, job.ID, stage, "completed",
		totalJobItems(counts), counts.FailureCount, code, message, finishedAt)
	if err != nil {
		return normalizeDatabaseError(ctx, "complete job stage", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: complete job stage affected no rows", ingestion.ErrJobStore)
	}
	return nil
}

func finishJobStageTx(ctx context.Context, tx pgx.Tx, job ingestion.Job, stage ingestion.JobStage, state string, counts ingestion.JobCounts, diagnostic ingestion.Diagnostic, finishedAt time.Time) error {
	if !stage.Valid() || !validStageState(state) {
		return fmt.Errorf("%w: stage state is invalid", ingestion.ErrInvalidJob)
	}
	if err := ensureJobStageTx(ctx, tx, job.OrganizationID, job.ID, stage, "pending", job.CreatedAt); err != nil {
		return err
	}
	code, message := nullableDiagnostic(diagnostic)
	tag, err := tx.Exec(ctx, completeJobStageSQL, job.OrganizationID, job.ID, stage, state,
		totalJobItems(counts), counts.FailureCount, code, message, finishedAt)
	if err != nil {
		return normalizeDatabaseError(ctx, "finish job stage", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: finish job stage affected no rows", ingestion.ErrJobStore)
	}
	return nil
}

func validStageState(state string) bool {
	switch state {
	case "pending", "running", "completed", "partial", "failed":
		return true
	default:
		return false
	}
}

func nullableDiagnostic(diagnostic ingestion.Diagnostic) (any, any) {
	if diagnostic.Code == "" {
		return nil, nil
	}
	return diagnostic.Code, nullableJobMessage(diagnostic.Message)
}

func nullableJobMessage(message string) any {
	if message == "" {
		return nil
	}
	return message
}

func totalJobItems(counts ingestion.JobCounts) int64 {
	items := counts.ArtifactCount
	if counts.ObservationCount > 0 && items > (int64(^uint64(0)>>1)-counts.ObservationCount) {
		return int64(^uint64(0) >> 1)
	}
	items += counts.ObservationCount
	if counts.EvidenceCount > 0 && items > (int64(^uint64(0)>>1)-counts.EvidenceCount) {
		return int64(^uint64(0) >> 1)
	}
	return items + counts.EvidenceCount
}

func jobStageID(organizationID, jobID string, stage ingestion.JobStage) string {
	digest := sha256.Sum256([]byte("ingestion-stage:" + organizationID + ":" + jobID + ":" + string(stage)))
	value := digest[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func normalizeNewJob(job *ingestion.Job) error {
	if job == nil {
		return fmt.Errorf("%w: job is nil", ingestion.ErrInvalidJob)
	}
	if job.State == "" {
		job.State = ingestion.JobStatePending
	}
	if job.Stage == "" {
		job.Stage = ingestion.JobStageValidation
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	if job.State != ingestion.JobStatePending || job.Stage != ingestion.JobStageValidation || job.AttemptCount != 0 {
		return fmt.Errorf("%w: new job must be pending at validation", ingestion.ErrInvalidJob)
	}
	return job.Validate()
}

func validateDatabaseJob(job ingestion.Job) error {
	if err := validateUUIDText(job.ID); err != nil {
		return fmt.Errorf("%w: job id is invalid", ingestion.ErrInvalidJob)
	}
	if err := validateUUIDText(job.OrganizationID); err != nil {
		return fmt.Errorf("%w: organization id is invalid", ingestion.ErrInvalidJob)
	}
	return nil
}

// readCanonicalJobScope observes the canonical rows, when they already
// exist, without making them a prerequisite for accepting an ingestion job.
// The job's external source/snapshot identities remain the durable idempotency
// boundary while the UUID columns are filled opportunistically later.
func readCanonicalJobScope(ctx context.Context, queryer jobQueryer, job ingestion.Job) (string, string, bool, error) {
	if queryer == nil {
		return "", "", false, fmt.Errorf("%w: job scope reader is not configured", ingestion.ErrJobStore)
	}
	var sourceID string
	var snapshotID *string
	err := queryer.QueryRow(ctx, selectCanonicalJobScopeSQL, job.OrganizationID, job.SourceExternalID, job.SnapshotExternalID).Scan(&sourceID, &snapshotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, normalizeDatabaseError(ctx, "read canonical job scope", err)
	}
	if err := validateUUIDText(sourceID); err != nil {
		return "", "", false, fmt.Errorf("%w: canonical source id is invalid", ingestion.ErrJobStore)
	}
	if snapshotID != nil {
		if err := validateUUIDText(*snapshotID); err != nil {
			return "", "", false, fmt.Errorf("%w: canonical snapshot id is invalid", ingestion.ErrJobStore)
		}
	}
	return sourceID, optionalJobString(snapshotID), true, nil
}

// resolveCanonicalJobScope selects only UUIDs that satisfy the canonical
// foreign keys. A bundle accepted before canonical persistence therefore
// stores NULL scope columns, while a retry after persistence keeps the known
// UUIDs and remains idempotent.
func resolveCanonicalJobScope(ctx context.Context, queryer jobQueryer, job ingestion.Job) (string, string, error) {
	sourceID, snapshotID, exists, err := readCanonicalJobScope(ctx, queryer, job)
	if err != nil || !exists {
		return sourceID, snapshotID, err
	}
	if job.SourceID != "" && job.SourceID != sourceID {
		return "", "", ingestion.ErrJobConflict
	}
	if job.SnapshotID != "" && snapshotID != "" && job.SnapshotID != snapshotID {
		return "", "", ingestion.ErrJobConflict
	}
	return sourceID, snapshotID, nil
}

// hydrateCanonicalJobScope makes the UUIDs available to a resumed or
// inspected job after its canonical bundle has been persisted. It never
// clears a scope merely because the canonical rows are not ready yet.
func hydrateCanonicalJobScope(ctx context.Context, queryer jobQueryer, job *ingestion.Job) error {
	if job == nil {
		return fmt.Errorf("%w: job scope target is nil", ingestion.ErrInvalidJob)
	}
	sourceID, snapshotID, exists, err := readCanonicalJobScope(ctx, queryer, *job)
	if err != nil || !exists {
		return err
	}
	if job.SourceID != "" && job.SourceID != sourceID {
		return ingestion.ErrJobConflict
	}
	if job.SnapshotID != "" && snapshotID != "" && job.SnapshotID != snapshotID {
		return ingestion.ErrJobConflict
	}
	if job.SourceID == "" {
		job.SourceID = sourceID
	}
	if job.SnapshotID == "" {
		job.SnapshotID = snapshotID
	}
	return nil
}

func validateMutation(ctx context.Context, organizationID, jobID, owner string, lease time.Duration) error {
	if err := validateMutationContext(ctx, organizationID, jobID, owner); err != nil {
		return err
	}
	if lease <= 0 {
		return fmt.Errorf("%w: lease duration must be positive", ingestion.ErrInvalidJob)
	}
	return nil
}

func validateMutationContext(ctx context.Context, organizationID, jobID, owner string) error {
	if err := validateJobContext(ctx); err != nil {
		return err
	}
	if err := validateUUIDText(organizationID); err != nil {
		return fmt.Errorf("%w: organization id is invalid", ingestion.ErrInvalidJob)
	}
	if err := validateUUIDText(jobID); err != nil {
		return fmt.Errorf("%w: job id is invalid", ingestion.ErrInvalidJob)
	}
	if strings.TrimSpace(owner) == "" || len(owner) > 256 || strings.ContainsAny(owner, "\x00\r\n") {
		return fmt.Errorf("%w: lease owner is invalid", ingestion.ErrInvalidJob)
	}
	return nil
}

func validateLeaseInput(owner string, lease time.Duration) error {
	if strings.TrimSpace(owner) == "" || len(owner) > 256 || strings.ContainsAny(owner, "\x00\r\n") {
		return fmt.Errorf("%w: lease owner is invalid", ingestion.ErrInvalidJob)
	}
	if lease <= 0 {
		return fmt.Errorf("%w: lease duration must be positive", ingestion.ErrInvalidJob)
	}
	return nil
}

func validateJobContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ingestion.ErrInvalidJob)
	}
	return ctx.Err()
}

func validateUUIDText(value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return ingestion.ErrInvalidJob
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') &&
			!(character >= 'A' && character <= 'F') {
			return ingestion.ErrInvalidJob
		}
	}
	return nil
}

func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalJobString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sameDatabaseIdentity(existing, requested ingestion.Job) bool {
	return existing.OrganizationID == requested.OrganizationID &&
		(existing.SourceID == "" || requested.SourceID == "" || existing.SourceID == requested.SourceID) &&
		(existing.SnapshotID == "" || requested.SnapshotID == "" || existing.SnapshotID == requested.SnapshotID) && existing.SourceExternalID == requested.SourceExternalID &&
		existing.SnapshotExternalID == requested.SnapshotExternalID && existing.FactualDigest == requested.FactualDigest &&
		existing.AnalysisConfigurationID == requested.AnalysisConfigurationID
}

func rollbackJobTx(tx pgx.Tx, ctx context.Context) {
	cleanup := context.WithoutCancel(ctx)
	cleanupCtx, cancel := context.WithTimeout(cleanup, jobStoreCleanupTimeout)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}

func normalizeDatabaseError(ctx context.Context, operation string, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ingestion.ErrJobConflict, operation)
		case "23502", "23514", "22P02":
			return fmt.Errorf("%w: %s", ingestion.ErrInvalidJob, operation)
		case "23503":
			return fmt.Errorf("%w: %s", ingestion.ErrJobNotFound, operation)
		}
	}
	return fmt.Errorf("%w: %s", ingestion.ErrJobStore, operation)
}
