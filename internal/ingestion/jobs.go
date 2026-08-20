package ingestion

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// JobState is the durable lifecycle state of an ingestion job. The database
// schema intentionally has no transient or cancellation-specific state;
// cancellation is represented as failed with DiagnosticCodeCancelled.
type JobState string

const (
	JobStatePending   JobState = "pending"
	JobStateRunning   JobState = "running"
	JobStateCompleted JobState = "completed"
	JobStatePartial   JobState = "partial"
	JobStateFailed    JobState = "failed"
)

// JobStage identifies the durable progress boundary at which a job stopped.
type JobStage string

const (
	JobStageValidation           JobStage = "validation"
	JobStageCanonicalPersistence JobStage = "canonical_persistence"
	JobStageTextualProjection    JobStage = "textual_projection"
	JobStageRelationalProjection JobStage = "relational_projection"
	JobStageEmbeddingProjection  JobStage = "embedding_projection"
	JobStageActivation           JobStage = "activation"
)

const (
	// Diagnostic codes are stable, low-cardinality values. Raw processor or
	// driver messages never become durable diagnostics.
	DiagnosticCodeCancelled            = "cancelled"
	DiagnosticCodeProcessing           = "processing_failed"
	DiagnosticCodeEmbeddingUnavailable = "embedding_unavailable"
	DiagnosticCodeEmbeddingForbidden   = "embedding_forbidden"
	DiagnosticCodeEmbeddingIncomplete  = "embedding_incomplete"
)

var (
	// ErrInvalidJob identifies invalid job input, state, stage, or counters.
	ErrInvalidJob = errors.New("ingestion: invalid job")
	// ErrJobNotFound identifies an organization-scoped job that is absent.
	ErrJobNotFound = errors.New("ingestion: job not found")
	// ErrJobConflict identifies a factual identity reused incompatibly.
	ErrJobConflict = errors.New("ingestion: job conflict")
	// ErrJobState identifies an operation that is not valid for the current
	// durable lifecycle state.
	ErrJobState = errors.New("ingestion: invalid job state")
	// ErrJobLeaseLost identifies a worker that no longer owns a job lease.
	ErrJobLeaseLost = errors.New("ingestion: job lease lost")
	// ErrJobCancelled identifies a job cancelled by its caller.
	ErrJobCancelled = errors.New("ingestion: job cancelled")
	// ErrJobStore identifies a safe, normalized persistence failure.
	ErrJobStore = errors.New("ingestion: job store failure")
	// ErrJobProcessing identifies a processor failure after the job was safely
	// marked failed. Its cause is deliberately not exposed.
	ErrJobProcessing = errors.New("ingestion: job processing failed")
	// ErrJobPartial identifies a projection limitation that was durably
	// recorded without marking the job failed. The canonical and non-vector
	// knowledge remains usable and can be resumed explicitly.
	ErrJobPartial = errors.New("ingestion: job is partial")
)

const (
	maxJobIdentifierLength     = 256
	maxDiagnosticCodeLength    = 64
	maxDiagnosticMessageLength = 512
)

// JobCounts are monotonic counters persisted with a job and its stages.
type JobCounts struct {
	ArtifactCount    int64 `json:"artifact_count"`
	ObservationCount int64 `json:"observation_count"`
	EvidenceCount    int64 `json:"evidence_count"`
	FailureCount     int64 `json:"failure_count"`
}

// Validate checks the non-negative database counter invariants.
func (c JobCounts) Validate() error {
	if c.ArtifactCount < 0 || c.ObservationCount < 0 || c.EvidenceCount < 0 || c.FailureCount < 0 {
		return fmt.Errorf("%w: counters must be non-negative", ErrInvalidJob)
	}
	return nil
}

// Diagnostic is the safe status detail stored for a terminal or partially
// completed job. Callers should use stable codes and avoid source content.
type Diagnostic struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Validate checks the safe diagnostic shape without interpreting its meaning.
func (d Diagnostic) Validate() error {
	if !validDiagnosticCode(d.Code) || len(d.Code) > maxDiagnosticCodeLength {
		return fmt.Errorf("%w: diagnostic code is invalid", ErrInvalidJob)
	}
	if len(d.Message) > maxDiagnosticMessageLength || strings.ContainsAny(d.Message, "\r\n\x00") {
		return fmt.Errorf("%w: diagnostic message is invalid", ErrInvalidJob)
	}
	return nil
}

// NewSafeDiagnostic creates a diagnostic after removing line breaks and
// bounding detail. It is intended for controlled messages, not raw errors.
func NewSafeDiagnostic(code, message string) (Diagnostic, error) {
	diagnostic := Diagnostic{
		Code:    strings.TrimSpace(code),
		Message: strings.TrimSpace(message),
	}
	if len(diagnostic.Message) > maxDiagnosticMessageLength {
		diagnostic.Message = diagnostic.Message[:maxDiagnosticMessageLength]
	}
	if err := diagnostic.Validate(); err != nil {
		return Diagnostic{}, err
	}
	return diagnostic, nil
}

// Job is the organization-scoped durable ingestion record. LeaseOwner is
// omitted from JSON because it is an internal coordination token.
type Job struct {
	ID                      string     `json:"id"`
	OrganizationID          string     `json:"organization_id"`
	OrganizationExternalID  string     `json:"-"`
	OrganizationName        string     `json:"-"`
	SourceID                string     `json:"source_id,omitempty"`
	SnapshotID              string     `json:"snapshot_id,omitempty"`
	SourceExternalID        string     `json:"source_external_id"`
	SnapshotExternalID      string     `json:"snapshot_external_id"`
	FactualDigest           string     `json:"factual_digest"`
	AnalysisConfigurationID string     `json:"analysis_configuration_id"`
	State                   JobState   `json:"state"`
	Stage                   JobStage   `json:"stage"`
	AttemptCount            int        `json:"attempt_count"`
	LeaseOwner              string     `json:"-"`
	LeaseExpiresAt          *time.Time `json:"lease_expires_at,omitempty"`
	CancelRequested         bool       `json:"cancel_requested"`
	DiagnosticCode          string     `json:"diagnostic_code,omitempty"`
	DiagnosticMessage       string     `json:"diagnostic_message,omitempty"`
	JobCounts
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Validate checks a job as read from or written to durable storage.
func (j Job) Validate() error {
	required := []struct {
		name  string
		value string
	}{
		{name: "id", value: j.ID},
		{name: "organization_id", value: j.OrganizationID},
		{name: "source_external_id", value: j.SourceExternalID},
		{name: "snapshot_external_id", value: j.SnapshotExternalID},
		{name: "analysis_configuration_id", value: j.AnalysisConfigurationID},
	}
	for _, field := range required {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return err
		}
	}
	if j.OrganizationExternalID != "" {
		if err := validateIdentifier("organization external id", j.OrganizationExternalID); err != nil {
			return err
		}
	}
	if strings.ContainsAny(j.OrganizationName, "\x00\r\n") {
		return fmt.Errorf("%w: organization name is invalid", ErrInvalidJob)
	}
	if j.SourceID == "" && j.SnapshotID != "" {
		return fmt.Errorf("%w: snapshot requires source", ErrInvalidJob)
	}
	if j.SourceID != "" && !isUUID(j.SourceID) {
		return fmt.Errorf("%w: source id is invalid", ErrInvalidJob)
	}
	if j.SnapshotID != "" && !isUUID(j.SnapshotID) {
		return fmt.Errorf("%w: snapshot id is invalid", ErrInvalidJob)
	}
	if j.FactualDigest == "" || !isLowerSHA256(j.FactualDigest) {
		return fmt.Errorf("%w: factual digest is invalid", ErrInvalidJob)
	}
	if !j.State.Valid() || !j.Stage.Valid() {
		return fmt.Errorf("%w: state or stage is invalid", ErrInvalidJob)
	}
	if j.CreatedAt.IsZero() {
		return fmt.Errorf("%w: created time is required", ErrInvalidJob)
	}
	if j.StartedAt != nil && j.StartedAt.Before(j.CreatedAt) {
		return fmt.Errorf("%w: started time is inconsistent", ErrInvalidJob)
	}
	if j.FinishedAt != nil && j.FinishedAt.Before(j.CreatedAt) {
		return fmt.Errorf("%w: finished time is inconsistent", ErrInvalidJob)
	}
	if j.StartedAt != nil && j.FinishedAt != nil && j.FinishedAt.Before(*j.StartedAt) {
		return fmt.Errorf("%w: finished time is inconsistent", ErrInvalidJob)
	}
	if j.LeaseOwner != "" && j.State != JobStateRunning {
		return fmt.Errorf("%w: lease owner is inconsistent", ErrInvalidJob)
	}
	if j.LeaseExpiresAt != nil && j.State != JobStateRunning {
		return fmt.Errorf("%w: lease expiry is inconsistent", ErrInvalidJob)
	}
	if j.LeaseExpiresAt != nil && j.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: lease expiry is invalid", ErrInvalidJob)
	}
	if j.LeaseExpiresAt != nil && j.LeaseOwner == "" {
		return fmt.Errorf("%w: lease owner is required", ErrInvalidJob)
	}
	switch j.State {
	case JobStatePending:
		if j.AttemptCount != 0 || j.StartedAt != nil || j.FinishedAt != nil || j.LeaseOwner != "" || j.LeaseExpiresAt != nil {
			return fmt.Errorf("%w: pending job is inconsistent", ErrInvalidJob)
		}
	case JobStateRunning:
		if j.AttemptCount <= 0 || j.FinishedAt != nil || j.CancelRequested || j.DiagnosticCode != "" || j.DiagnosticMessage != "" {
			return fmt.Errorf("%w: running job is inconsistent", ErrInvalidJob)
		}
	case JobStateCompleted:
		if j.FinishedAt == nil || j.LeaseOwner != "" || j.LeaseExpiresAt != nil || j.CancelRequested || j.DiagnosticCode != "" || j.DiagnosticMessage != "" {
			return fmt.Errorf("%w: completed job is inconsistent", ErrInvalidJob)
		}
	case JobStatePartial, JobStateFailed:
		if j.FinishedAt == nil || j.LeaseOwner != "" || j.LeaseExpiresAt != nil {
			return fmt.Errorf("%w: terminal job is inconsistent", ErrInvalidJob)
		}
		if j.DiagnosticCode == "" {
			return fmt.Errorf("%w: terminal diagnostic is required", ErrInvalidJob)
		}
	}
	if j.AttemptCount < 0 {
		return fmt.Errorf("%w: attempt count must be non-negative", ErrInvalidJob)
	}
	if err := j.JobCounts.Validate(); err != nil {
		return err
	}
	if j.DiagnosticCode != "" {
		if err := (Diagnostic{Code: j.DiagnosticCode, Message: j.DiagnosticMessage}).Validate(); err != nil {
			return err
		}
	} else if j.DiagnosticMessage != "" {
		return fmt.Errorf("%w: diagnostic code is required", ErrInvalidJob)
	}
	if j.CancelRequested && j.State != JobStateFailed {
		return fmt.Errorf("%w: cancellation state is inconsistent", ErrInvalidJob)
	}
	return nil
}

// NewJobInput contains the immutable identity used to create a pending job.
// An empty ID is replaced with a cryptographically random UUID.
type NewJobInput struct {
	ID                      string
	OrganizationID          string
	OrganizationExternalID  string
	OrganizationName        string
	SourceID                string
	SnapshotID              string
	SourceExternalID        string
	SnapshotExternalID      string
	FactualDigest           string
	AnalysisConfigurationID string
	CreatedAt               time.Time
}

// NewJob creates a validated pending job without contacting a database.
func NewJob(input NewJobInput) (Job, error) {
	id := strings.TrimSpace(input.ID)
	if id == "" {
		var err error
		id, err = newUUID()
		if err != nil {
			return Job{}, fmt.Errorf("%w: cannot create job identity", ErrInvalidJob)
		}
	}
	createdAt := input.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	job := Job{
		ID:                      id,
		OrganizationID:          strings.TrimSpace(input.OrganizationID),
		OrganizationExternalID:  strings.TrimSpace(input.OrganizationExternalID),
		OrganizationName:        strings.TrimSpace(input.OrganizationName),
		SourceID:                strings.TrimSpace(input.SourceID),
		SnapshotID:              strings.TrimSpace(input.SnapshotID),
		SourceExternalID:        strings.TrimSpace(input.SourceExternalID),
		SnapshotExternalID:      strings.TrimSpace(input.SnapshotExternalID),
		FactualDigest:           input.FactualDigest,
		AnalysisConfigurationID: strings.TrimSpace(input.AnalysisConfigurationID),
		State:                   JobStatePending,
		Stage:                   JobStageValidation,
		CreatedAt:               createdAt.UTC(),
	}
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	return job, nil
}

// Valid reports whether the state is part of the durable vocabulary.
func (s JobState) Valid() bool {
	switch s {
	case JobStatePending, JobStateRunning, JobStateCompleted, JobStatePartial, JobStateFailed:
		return true
	default:
		return false
	}
}

// Valid reports whether the stage is part of the durable vocabulary.
func (s JobStage) Valid() bool {
	switch s {
	case JobStageValidation, JobStageCanonicalPersistence, JobStageTextualProjection,
		JobStageRelationalProjection, JobStageEmbeddingProjection, JobStageActivation:
		return true
	default:
		return false
	}
}

// JobStore is the durable boundary consumed by the executor. Implementations
// must make Claim atomic and must scope every mutation by organization, job,
// and lease owner where applicable.
type JobStore interface {
	Create(context.Context, Job) (Job, error)
	Get(context.Context, string, string) (Job, error)
	Claim(context.Context, string, string, time.Duration) (Job, bool, error)
	Heartbeat(context.Context, string, string, string, time.Duration) (Job, error)
	AdvanceStage(context.Context, string, string, string, JobStage, JobCounts) (Job, error)
	Complete(context.Context, string, string, string, JobCounts) (Job, error)
	Partial(context.Context, string, string, string, JobStage, Diagnostic, JobCounts) (Job, error)
	ResumePartial(context.Context, string, string, string, time.Duration) (Job, error)
	Fail(context.Context, string, string, string, Diagnostic) (Job, error)
	Cancel(context.Context, string, string) (Job, error)
}

// Store is a short alias for callers that do not need to distinguish job
// storage from other ingestion boundaries.
type Store = JobStore

// MemoryStore is a deterministic, organization-scoped store for unit tests
// and local composition without PostgreSQL. Production uses persistence.JobStore.
type MemoryStore struct {
	mu    sync.Mutex
	jobs  map[string]Job
	byKey map[string]string
	clock func() time.Time
}

// MemoryStoreOptions controls deterministic time in a MemoryStore.
type MemoryStoreOptions struct {
	Now func() time.Time
}

// NewMemoryStore creates an empty in-memory job store.
func NewMemoryStore(options ...MemoryStoreOptions) *MemoryStore {
	var now func() time.Time
	if len(options) > 0 {
		now = options[0].Now
	}
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		jobs:  make(map[string]Job),
		byKey: make(map[string]string),
		clock: now,
	}
}

// Create inserts a pending job or returns the existing job for the same
// factual identity and organization. Incompatible reuse is a conflict.
func (s *MemoryStore) Create(ctx context.Context, job Job) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if s == nil {
		return Job{}, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	if job.State == "" {
		job.State = JobStatePending
	}
	if job.Stage == "" {
		job.Stage = JobStageValidation
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = s.now()
	}
	if job.State != JobStatePending || job.Stage != JobStageValidation || job.AttemptCount != 0 {
		return Job{}, fmt.Errorf("%w: new job must be pending at validation", ErrInvalidJob)
	}
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.jobs[job.ID]; ok {
		if sameIdentity(existing, job) {
			return cloneJob(existing), nil
		}
		return Job{}, ErrJobConflict
	}
	key := identityKey(job)
	if existingID, ok := s.byKey[key]; ok {
		existing := s.jobs[existingID]
		if sameIdentity(existing, job) {
			return cloneJob(existing), nil
		}
		return Job{}, ErrJobConflict
	}
	s.jobs[job.ID] = cloneJob(job)
	s.byKey[key] = job.ID
	return cloneJob(job), nil
}

// Get returns a defensive copy of one organization-scoped job.
func (s *MemoryStore) Get(ctx context.Context, organizationID, jobID string) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if s == nil {
		return Job{}, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.OrganizationID != organizationID {
		return Job{}, ErrJobNotFound
	}
	return cloneJob(job), nil
}

// Claim atomically claims the oldest pending job or an expired running job.
// A false result means no work is currently available.
func (s *MemoryStore) Claim(ctx context.Context, organizationID, owner string, lease time.Duration) (Job, bool, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, false, err
	}
	if err := validateIdentifier("organization_id", organizationID); err != nil {
		return Job{}, false, err
	}
	if err := validateLease(owner, lease); err != nil {
		return Job{}, false, err
	}
	if s == nil {
		return Job{}, false, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var selected *Job
	for id := range s.jobs {
		candidate := s.jobs[id]
		if candidate.OrganizationID != organizationID || candidate.CancelRequested || !claimable(candidate, now) {
			continue
		}
		if selected == nil || before(candidate, *selected) {
			copy := candidate
			selected = &copy
		}
	}
	if selected == nil {
		return Job{}, false, nil
	}
	expires := now.Add(lease).UTC()
	selected.State = JobStateRunning
	if selected.Stage == "" {
		selected.Stage = JobStageValidation
	}
	selected.AttemptCount++
	selected.LeaseOwner = owner
	selected.LeaseExpiresAt = &expires
	if selected.StartedAt == nil {
		started := now.UTC()
		selected.StartedAt = &started
	}
	selected.FinishedAt = nil
	selected.DiagnosticCode = ""
	selected.DiagnosticMessage = ""
	s.jobs[selected.ID] = cloneJob(*selected)
	return cloneJob(*selected), true, nil
}

// Heartbeat extends an active lease and returns the updated job.
func (s *MemoryStore) Heartbeat(ctx context.Context, organizationID, jobID, owner string, lease time.Duration) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if err := validateLease(owner, lease); err != nil {
		return Job{}, err
	}
	if s == nil {
		return Job{}, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.OrganizationID != organizationID {
		return Job{}, ErrJobNotFound
	}
	if err := requireLease(job, owner, now); err != nil {
		return Job{}, err
	}
	expires := now.Add(lease).UTC()
	job.LeaseExpiresAt = &expires
	s.jobs[job.ID] = cloneJob(job)
	return cloneJob(job), nil
}

// AdvanceStage records monotonic progress and updated counters.
func (s *MemoryStore) AdvanceStage(ctx context.Context, organizationID, jobID, owner string, stage JobStage, counts JobCounts) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if !stage.Valid() {
		return Job{}, fmt.Errorf("%w: stage is invalid", ErrInvalidJob)
	}
	if err := counts.Validate(); err != nil {
		return Job{}, err
	}
	return s.mutateRunning(organizationID, jobID, owner, func(job *Job, now time.Time) error {
		if !stageTransitionAllowed(job.Stage, stage) {
			return fmt.Errorf("%w: stage transition is invalid", ErrJobState)
		}
		if err := monotonicCounts(*job, counts); err != nil {
			return err
		}
		job.Stage = stage
		job.JobCounts = counts
		_ = now
		return nil
	})
}

// Complete marks a claimed job completed and records activation as its final
// stage. Only the current lease owner may complete it.
func (s *MemoryStore) Complete(ctx context.Context, organizationID, jobID, owner string, counts JobCounts) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if err := counts.Validate(); err != nil {
		return Job{}, err
	}
	return s.mutateRunning(organizationID, jobID, owner, func(job *Job, now time.Time) error {
		if job.Stage != JobStageActivation {
			return fmt.Errorf("%w: activation stage is required", ErrJobState)
		}
		if err := monotonicCounts(*job, counts); err != nil {
			return err
		}
		job.State = JobStateCompleted
		job.Stage = JobStageActivation
		job.JobCounts = counts
		job.LeaseOwner = ""
		job.LeaseExpiresAt = nil
		finished := now.UTC()
		job.FinishedAt = &finished
		return nil
	})
}

// Partial records a resumable projection limitation without claiming the
// knowledge is fully complete. Partial jobs are not automatically reclaimed
// by task 4.1; later projection work may explicitly resume them.
func (s *MemoryStore) Partial(ctx context.Context, organizationID, jobID, owner string, stage JobStage, diagnostic Diagnostic, counts JobCounts) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if !stage.Valid() {
		return Job{}, fmt.Errorf("%w: stage is invalid", ErrInvalidJob)
	}
	if err := diagnostic.Validate(); err != nil {
		return Job{}, err
	}
	if err := counts.Validate(); err != nil {
		return Job{}, err
	}
	return s.mutateRunning(organizationID, jobID, owner, func(job *Job, now time.Time) error {
		if !stageTransitionAllowed(job.Stage, stage) {
			return fmt.Errorf("%w: stage transition is invalid", ErrJobState)
		}
		if err := monotonicCounts(*job, counts); err != nil {
			return err
		}
		job.State = JobStatePartial
		job.Stage = stage
		job.JobCounts = counts
		job.DiagnosticCode = diagnostic.Code
		job.DiagnosticMessage = diagnostic.Message
		job.LeaseOwner = ""
		job.LeaseExpiresAt = nil
		finished := now.UTC()
		job.FinishedAt = &finished
		return nil
	})
}

// ResumePartial claims only an embedding-stage partial job for an explicit
// projection retry. It never reclaims partial jobs through Claim, so a caller
// must choose the scope and provide a fresh lease owner deliberately.
func (s *MemoryStore) ResumePartial(ctx context.Context, organizationID, jobID, owner string, lease time.Duration) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if err := validateIdentifier("organization_id", organizationID); err != nil {
		return Job{}, err
	}
	if err := validateIdentifier("job_id", jobID); err != nil {
		return Job{}, err
	}
	if err := validateLease(owner, lease); err != nil {
		return Job{}, err
	}
	if s == nil {
		return Job{}, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.OrganizationID != organizationID {
		return Job{}, ErrJobNotFound
	}
	if job.State != JobStatePartial || job.Stage != JobStageEmbeddingProjection || job.CancelRequested {
		return Job{}, ErrJobState
	}
	expires := now.Add(lease).UTC()
	job.State = JobStateRunning
	job.AttemptCount++
	job.LeaseOwner = owner
	job.LeaseExpiresAt = &expires
	job.FinishedAt = nil
	job.DiagnosticCode = ""
	job.DiagnosticMessage = ""
	s.jobs[job.ID] = cloneJob(job)
	return cloneJob(job), nil
}

// Fail marks an owned running job failed with a safe diagnostic.
func (s *MemoryStore) Fail(ctx context.Context, organizationID, jobID, owner string, diagnostic Diagnostic) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if err := diagnostic.Validate(); err != nil {
		return Job{}, err
	}
	return s.mutateRunning(organizationID, jobID, owner, func(job *Job, now time.Time) error {
		job.State = JobStateFailed
		job.DiagnosticCode = diagnostic.Code
		job.DiagnosticMessage = diagnostic.Message
		job.LeaseOwner = ""
		job.LeaseExpiresAt = nil
		finished := now.UTC()
		job.FinishedAt = &finished
		return nil
	})
}

// Cancel transitions pending or running work to terminal failed/cancelled.
// It is idempotent for a job already cancelled by the same operation.
func (s *MemoryStore) Cancel(ctx context.Context, organizationID, jobID string) (Job, error) {
	if err := checkContext(ctx); err != nil {
		return Job{}, err
	}
	if s == nil {
		return Job{}, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.OrganizationID != organizationID {
		return Job{}, ErrJobNotFound
	}
	if job.State == JobStateFailed && job.CancelRequested {
		return cloneJob(job), nil
	}
	if job.State != JobStatePending && job.State != JobStateRunning {
		return Job{}, ErrJobState
	}
	job.State = JobStateFailed
	job.CancelRequested = true
	job.DiagnosticCode = DiagnosticCodeCancelled
	job.DiagnosticMessage = "job cancelled"
	job.LeaseOwner = ""
	job.LeaseExpiresAt = nil
	finished := now.UTC()
	job.FinishedAt = &finished
	s.jobs[job.ID] = cloneJob(job)
	return cloneJob(job), nil
}

// Snapshot returns all jobs in stable creation order for deterministic tests.
func (s *MemoryStore) Snapshot() []Job {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, cloneJob(job))
	}
	sort.Slice(jobs, func(i, j int) bool { return before(jobs[i], jobs[j]) })
	return jobs
}

func (s *MemoryStore) mutateRunning(organizationID, jobID, owner string, update func(*Job, time.Time) error) (Job, error) {
	if s == nil {
		return Job{}, fmt.Errorf("%w: store is not configured", ErrJobStore)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok || job.OrganizationID != organizationID {
		return Job{}, ErrJobNotFound
	}
	if err := requireLease(job, owner, now); err != nil {
		return Job{}, err
	}
	updated := cloneJob(job)
	if err := update(&updated, now); err != nil {
		return Job{}, err
	}
	s.jobs[job.ID] = cloneJob(updated)
	return cloneJob(updated), nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidJob)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func validateLease(owner string, lease time.Duration) error {
	if strings.TrimSpace(owner) == "" {
		return fmt.Errorf("%w: lease owner is required", ErrInvalidJob)
	}
	if lease <= 0 {
		return fmt.Errorf("%w: lease duration must be positive", ErrInvalidJob)
	}
	return nil
}

func requireLease(job Job, owner string, now time.Time) error {
	if job.State == JobStateFailed && job.CancelRequested {
		return ErrJobCancelled
	}
	if job.State != JobStateRunning {
		return ErrJobState
	}
	if job.LeaseOwner == "" || job.LeaseOwner != owner || job.LeaseExpiresAt == nil || !now.Before(*job.LeaseExpiresAt) {
		return ErrJobLeaseLost
	}
	return nil
}

func claimable(job Job, now time.Time) bool {
	if job.State == JobStatePending {
		return true
	}
	return job.State == JobStateRunning && (job.LeaseExpiresAt == nil || !now.Before(*job.LeaseExpiresAt))
}

func before(left, right Job) bool {
	if left.CreatedAt.Equal(right.CreatedAt) {
		return left.ID < right.ID
	}
	return left.CreatedAt.Before(right.CreatedAt)
}

func stageTransitionAllowed(from, to JobStage) bool {
	if from == to {
		return true
	}
	return stageIndex(to) == stageIndex(from)+1
}

func stageIndex(stage JobStage) int {
	switch stage {
	case JobStageValidation:
		return 0
	case JobStageCanonicalPersistence:
		return 1
	case JobStageTextualProjection:
		return 2
	case JobStageRelationalProjection:
		return 3
	case JobStageEmbeddingProjection:
		return 4
	case JobStageActivation:
		return 5
	default:
		return -1
	}
}

func monotonicCounts(job Job, next JobCounts) error {
	if next.ArtifactCount < job.ArtifactCount || next.ObservationCount < job.ObservationCount ||
		next.EvidenceCount < job.EvidenceCount || next.FailureCount < job.FailureCount {
		return fmt.Errorf("%w: counters cannot decrease", ErrJobState)
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidJob, name)
	}
	if len(value) > maxJobIdentifierLength || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%w: %s is invalid", ErrInvalidJob, name)
	}
	return nil
}

func validDiagnosticCode(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func identityKey(job Job) string {
	return strings.Join([]string{job.OrganizationID, job.SourceExternalID, job.SnapshotExternalID, job.FactualDigest}, "\x00")
}

func sameIdentity(left, right Job) bool {
	return left.OrganizationID == right.OrganizationID && left.SourceExternalID == right.SourceExternalID &&
		left.SnapshotExternalID == right.SnapshotExternalID && left.FactualDigest == right.FactualDigest &&
		left.AnalysisConfigurationID == right.AnalysisConfigurationID && left.SourceID == right.SourceID &&
		left.SnapshotID == right.SnapshotID && left.OrganizationExternalID == right.OrganizationExternalID &&
		left.OrganizationName == right.OrganizationName
}

func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= '0' && character <= '9') && !(character >= 'a' && character <= 'f') && !(character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}

func cloneJob(job Job) Job {
	if job.LeaseExpiresAt != nil {
		value := *job.LeaseExpiresAt
		job.LeaseExpiresAt = &value
	}
	if job.StartedAt != nil {
		value := *job.StartedAt
		job.StartedAt = &value
	}
	if job.FinishedAt != nil {
		value := *job.FinishedAt
		job.FinishedAt = &value
	}
	return job
}

func (s *MemoryStore) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock().UTC()
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
