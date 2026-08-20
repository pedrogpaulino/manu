package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	testOrganizationID  = "00000000-0000-4000-8000-000000000001"
	otherOrganizationID = "00000000-0000-4000-8000-000000000011"
	testSourceID        = "00000000-0000-4000-8000-000000000002"
	testSnapshotID      = "00000000-0000-4000-8000-000000000003"
	testJobID           = "00000000-0000-4000-8000-000000000004"
)

var testDigest = strings.Repeat("a", 64)

func testJob(t *testing.T, id string, createdAt time.Time) Job {
	t.Helper()
	job, err := NewJob(NewJobInput{
		ID:                      id,
		OrganizationID:          testOrganizationID,
		SourceID:                testSourceID,
		SnapshotID:              testSnapshotID,
		SourceExternalID:        "source-1",
		SnapshotExternalID:      "snapshot-1",
		FactualDigest:           testDigest,
		AnalysisConfigurationID: "config-1",
		CreatedAt:               createdAt,
	})
	if err != nil {
		t.Fatalf("create test job: %v", err)
	}
	return job
}

func TestJobValidateRejectsInvalidLifecycleAndReferences(t *testing.T) {
	createdAt := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	base := testJob(t, testJobID, createdAt)
	cases := []struct {
		name string
		job  Job
	}{
		{name: "unknown state", job: func() Job { value := base; value.State = JobState("unknown"); return value }()},
		{name: "unknown stage", job: func() Job { value := base; value.Stage = JobStage("unknown"); return value }()},
		{name: "snapshot without uuid source", job: func() Job { value := base; value.SourceID = "source"; return value }()},
		{name: "pending attempt", job: func() Job { value := base; value.AttemptCount = 1; return value }()},
		{name: "running without attempt", job: func() Job { value := base; value.State = JobStateRunning; return value }()},
		{name: "terminal without finish", job: func() Job { value := base; value.State = JobStateFailed; return value }()},
		{name: "finished before created", job: func() Job {
			value := base
			value.State = JobStateFailed
			value.FinishedAt = ptrTime(createdAt.Add(-time.Second))
			return value
		}()},
		{name: "lease on pending", job: func() Job { value := base; value.LeaseOwner = "worker"; return value }()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := test.job.Validate(); !errors.Is(err, ErrInvalidJob) {
				t.Fatalf("Validate() error = %v, want ErrInvalidJob", err)
			}
		})
	}
}

func TestMemoryStoreCreateIsIdempotentAndConfigurationScoped(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	created, err := store.Create(context.Background(), job)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	retried, err := store.Create(context.Background(), job)
	if err != nil {
		t.Fatalf("idempotent Create() error = %v", err)
	}
	if retried.ID != created.ID || len(store.Snapshot()) != 1 {
		t.Fatalf("idempotent Create() = %#v, jobs = %d", retried, len(store.Snapshot()))
	}

	byID := job
	byID.AnalysisConfigurationID = "config-2"
	if _, err := store.Create(context.Background(), byID); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("same ID with changed config error = %v, want ErrJobConflict", err)
	}
	byNaturalKey := job
	byNaturalKey.ID = "00000000-0000-4000-8000-000000000005"
	byNaturalKey.AnalysisConfigurationID = "config-2"
	if _, err := store.Create(context.Background(), byNaturalKey); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("same natural identity with changed config error = %v, want ErrJobConflict", err)
	}
}

func TestMemoryStoreClaimReclaimsExpiredLeaseWithoutDuplicate(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	other := testJob(t, "00000000-0000-4000-8000-000000000005", now.Add(time.Nanosecond))
	other.OrganizationID = otherOrganizationID
	other.SourceExternalID = "source-other"
	other.SnapshotExternalID = "snapshot-other"
	other.FactualDigest = strings.Repeat("b", 64)
	if _, err := store.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), testOrganizationID, "worker-a", time.Second)
	if err != nil || !ok {
		t.Fatalf("first Claim() = %#v, %v", claimed, err)
	}
	if _, ok, err := store.Claim(context.Background(), testOrganizationID, "worker-b", time.Second); err != nil || ok {
		t.Fatalf("duplicate Claim() = ok %v, err %v", ok, err)
	}
	if scoped, ok, err := store.Claim(context.Background(), otherOrganizationID, "worker-other", time.Second); err != nil || !ok || scoped.OrganizationID != otherOrganizationID {
		t.Fatalf("cross-organization Claim() = %#v, ok %v, err %v", scoped, ok, err)
	}
	now = now.Add(2 * time.Second)
	reclaimed, ok, err := store.Claim(context.Background(), testOrganizationID, "worker-b", time.Second)
	if err != nil || !ok || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaim Claim() = %#v, ok %v, err %v", reclaimed, ok, err)
	}
	if _, err := store.Heartbeat(context.Background(), job.OrganizationID, job.ID, "worker-a", time.Second); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("old heartbeat error = %v, want ErrJobLeaseLost", err)
	}
	if _, err := store.Complete(context.Background(), job.OrganizationID, job.ID, "worker-a", JobCounts{}); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("old completion error = %v, want ErrJobLeaseLost", err)
	}
}

func TestMemoryStoreTransitionsAndCancellation(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), testOrganizationID, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	if _, err := store.AdvanceStage(context.Background(), job.OrganizationID, job.ID, "worker", JobStageCanonicalPersistence, JobCounts{ArtifactCount: 1}); err != nil {
		t.Fatalf("AdvanceStage() error = %v", err)
	}
	if _, err := store.AdvanceStage(context.Background(), job.OrganizationID, job.ID, "worker", JobStageValidation, JobCounts{ArtifactCount: 1}); !errors.Is(err, ErrJobState) {
		t.Fatalf("backward stage error = %v, want ErrJobState", err)
	}
	if _, err := store.AdvanceStage(context.Background(), job.OrganizationID, job.ID, "worker", JobStageTextualProjection, JobCounts{}); !errors.Is(err, ErrJobState) {
		t.Fatalf("decreasing counters error = %v, want ErrJobState", err)
	}
	if _, err := store.Complete(context.Background(), job.OrganizationID, job.ID, "worker", JobCounts{ArtifactCount: 1}); !errors.Is(err, ErrJobState) {
		t.Fatalf("completion before activation error = %v, want ErrJobState", err)
	}
	if _, err := store.Cancel(context.Background(), job.OrganizationID, job.ID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	cancelled, err := store.Cancel(context.Background(), job.OrganizationID, job.ID)
	if err != nil || cancelled.DiagnosticCode != DiagnosticCodeCancelled {
		t.Fatalf("idempotent Cancel() = %#v, %v", cancelled, err)
	}
	if _, err := store.Complete(context.Background(), job.OrganizationID, job.ID, "worker", JobCounts{ArtifactCount: 1}); !errors.Is(err, ErrJobCancelled) {
		t.Fatalf("completion after cancellation error = %v, want ErrJobCancelled", err)
	}
}

func TestMemoryStoreCompletesAfterActivationSequence(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), testOrganizationID, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	if _, err := store.Complete(context.Background(), testOrganizationID, testJobID, "worker", JobCounts{}); !errors.Is(err, ErrJobState) {
		t.Fatalf("completion before activation error = %v, want ErrJobState", err)
	}
	for _, stage := range []JobStage{
		JobStageCanonicalPersistence,
		JobStageTextualProjection,
		JobStageRelationalProjection,
		JobStageEmbeddingProjection,
		JobStageActivation,
	} {
		if _, err := store.AdvanceStage(context.Background(), testOrganizationID, testJobID, "worker", stage, JobCounts{}); err != nil {
			t.Fatalf("AdvanceStage(%s) error = %v", stage, err)
		}
	}
	completed, err := store.Complete(context.Background(), testOrganizationID, testJobID, "worker", JobCounts{})
	if err != nil || completed.State != JobStateCompleted || completed.Stage != JobStageActivation {
		t.Fatalf("Complete() = %#v, %v", completed, err)
	}
}

func TestMemoryStoreResumesOnlyEmbeddingPartialWithFreshLease(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), job.OrganizationID, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim() = %#v, %v, %v", claimed, ok, err)
	}
	for _, stage := range []JobStage{
		JobStageCanonicalPersistence,
		JobStageTextualProjection,
		JobStageRelationalProjection,
		JobStageEmbeddingProjection,
	} {
		if _, err := store.AdvanceStage(context.Background(), job.OrganizationID, job.ID, claimed.LeaseOwner, stage, JobCounts{EvidenceCount: 2}); err != nil {
			t.Fatalf("AdvanceStage(%s) error = %v", stage, err)
		}
	}
	partial, err := store.Partial(context.Background(), job.OrganizationID, job.ID, claimed.LeaseOwner, JobStageEmbeddingProjection,
		Diagnostic{Code: DiagnosticCodeEmbeddingUnavailable, Message: "embedding projection unavailable"}, JobCounts{EvidenceCount: 2})
	if err != nil || partial.State != JobStatePartial {
		t.Fatalf("Partial() = %#v, %v", partial, err)
	}
	resumed, err := store.ResumePartial(context.Background(), job.OrganizationID, job.ID, "resume-worker", time.Minute)
	if err != nil {
		t.Fatalf("ResumePartial() error = %v", err)
	}
	if resumed.State != JobStateRunning || resumed.Stage != JobStageEmbeddingProjection || resumed.LeaseOwner != "resume-worker" || resumed.AttemptCount != 2 || resumed.EvidenceCount != 2 {
		t.Fatalf("resumed job = %#v", resumed)
	}
	if _, err := store.ResumePartial(context.Background(), job.OrganizationID, job.ID, "other-worker", time.Minute); !errors.Is(err, ErrJobState) {
		t.Fatalf("concurrent ResumePartial() error = %v, want ErrJobState", err)
	}
	if _, err := store.AdvanceStage(context.Background(), job.OrganizationID, job.ID, "other-worker", JobStageActivation, JobCounts{EvidenceCount: 2}); !errors.Is(err, ErrJobLeaseLost) {
		t.Fatalf("old owner AdvanceStage() error = %v, want ErrJobLeaseLost", err)
	}
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
