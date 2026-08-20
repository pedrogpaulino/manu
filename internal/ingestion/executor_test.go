package ingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func executorTestOptions(organizationID, owner string) ExecutorOptions {
	return ExecutorOptions{
		OrganizationID:    organizationID,
		Workers:           1,
		LeaseDuration:     time.Second,
		HeartbeatInterval: 100 * time.Millisecond,
		PollInterval:      time.Millisecond,
		Owner:             owner,
	}
}

func advanceToActivation(ctx context.Context, store JobStore, job Job, counts JobCounts) error {
	for _, stage := range []JobStage{
		JobStageCanonicalPersistence,
		JobStageTextualProjection,
		JobStageRelationalProjection,
		JobStageEmbeddingProjection,
		JobStageActivation,
	} {
		if _, err := store.AdvanceStage(ctx, job.OrganizationID, job.ID, job.LeaseOwner, stage, counts); err != nil {
			return err
		}
	}
	return nil
}

func TestExecutorRecordsSafeHandlerFailure(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, func(context.Context, Job) error {
		return errors.New("raw secret source contents")
	}, executorTestOptions(testOrganizationID, "executor"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(err, ErrJobProcessing) {
		t.Fatalf("RunOnce() = claimed %v, err %v", claimed, err)
	}
	if strings.Contains(err.Error(), "raw secret") {
		t.Fatalf("handler detail leaked in error: %v", err)
	}
	stored, err := store.Get(context.Background(), testOrganizationID, testJobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStateFailed || stored.DiagnosticCode != DiagnosticCodeProcessing || stored.DiagnosticMessage != "job processing failed" {
		t.Fatalf("stored failure = %#v", stored)
	}
}

func TestExecutorLeavesDurablePartialWithoutFailOrComplete(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, func(ctx context.Context, claimed Job) error {
		if _, err := store.AdvanceStage(ctx, claimed.OrganizationID, claimed.ID, claimed.LeaseOwner, JobStageCanonicalPersistence, JobCounts{}); err != nil {
			return err
		}
		if _, err := store.Partial(ctx, claimed.OrganizationID, claimed.ID, claimed.LeaseOwner, JobStageCanonicalPersistence,
			Diagnostic{Code: DiagnosticCodeEmbeddingUnavailable, Message: "embedding projection unavailable"}, JobCounts{}); err != nil {
			return err
		}
		return ErrJobPartial
	}, executorTestOptions(testOrganizationID, "executor"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, runErr := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(runErr, ErrJobPartial) {
		t.Fatalf("RunOnce() = claimed %v, err %v, want ErrJobPartial", claimed, runErr)
	}
	stored, err := store.Get(context.Background(), testOrganizationID, testJobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStatePartial || stored.DiagnosticCode != DiagnosticCodeEmbeddingUnavailable {
		t.Fatalf("stored partial = %#v", stored)
	}
}

func TestExecutorCompletesWithCountsAdvancedByHandler(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, func(ctx context.Context, claimed Job) error {
		return advanceToActivation(ctx, store, claimed, JobCounts{ArtifactCount: 2, EvidenceCount: 1})
	}, executorTestOptions(testOrganizationID, "executor"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := executor.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("RunOnce() = claimed %v, err %v", claimed, err)
	}
	stored, err := store.Get(context.Background(), testOrganizationID, testJobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStateCompleted || stored.ArtifactCount != 2 || stored.EvidenceCount != 1 {
		t.Fatalf("completed job = %#v", stored)
	}
}

func TestExecutorCancellationLeavesLeaseForRestart(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := testJob(t, testJobID, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	first, err := NewExecutor(store, func(ctx context.Context, _ Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, executorTestOptions(testOrganizationID, "first"))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, runErr := first.RunOnce(ctx)
		result <- runErr
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled RunOnce() error = %v, want context.Canceled", err)
	}
	stored, err := store.Get(context.Background(), testOrganizationID, testJobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStateRunning {
		t.Fatalf("cancelled job state = %s, want running for reclaim", stored.State)
	}

	now = now.Add(2 * time.Second)
	second, err := NewExecutor(store, func(ctx context.Context, claimed Job) error {
		return advanceToActivation(ctx, store, claimed, JobCounts{})
	}, executorTestOptions(testOrganizationID, "second"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := second.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("restart RunOnce() = claimed %v, err %v", claimed, err)
	}
	stored, err = store.Get(context.Background(), testOrganizationID, testJobID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStateCompleted || stored.AttemptCount != 2 {
		t.Fatalf("reclaimed job = %#v", stored)
	}
}

func TestExecutorBoundsConcurrentProcessingAndDoesNotDuplicate(t *testing.T) {
	const jobCount = 8
	const workers = 3
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	for index := 0; index < jobCount; index++ {
		jobID := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+10)
		job := testJob(t, jobID, now.Add(time.Duration(index)*time.Nanosecond))
		job.SourceExternalID = fmt.Sprintf("source-%d", index)
		job.SnapshotExternalID = fmt.Sprintf("snapshot-%d", index)
		job.FactualDigest = fmt.Sprintf("%064x", index+1)
		if _, err := store.Create(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	active, maximum, completed := 0, 0, 0
	seen := make(map[string]int)
	allDone := make(chan struct{})
	var allDoneOnce sync.Once
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := func(ctx context.Context, job Job) error {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		seen[job.ID]++
		mu.Unlock()
		if err := advanceToActivation(ctx, store, job, JobCounts{}); err != nil {
			mu.Lock()
			active--
			mu.Unlock()
			return err
		}
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
		mu.Lock()
		active--
		completed++
		if completed == jobCount {
			allDoneOnce.Do(func() { close(allDone) })
		}
		mu.Unlock()
		return nil
	}
	options := executorTestOptions(testOrganizationID, "bounded")
	options.Workers = workers
	executor, err := NewExecutor(store, handler, options)
	if err != nil {
		t.Fatal(err)
	}
	runResult := make(chan error, 1)
	go func() { runResult <- executor.Run(ctx) }()
	select {
	case <-allDone:
		deadline := time.Now().Add(2 * time.Second)
		for {
			allCompleted := true
			for _, job := range store.Snapshot() {
				if job.State != JobStateCompleted {
					allCompleted = false
					break
				}
			}
			if allCompleted || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for all jobs")
	}
	if err := <-runResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum > workers {
		t.Fatalf("maximum active handlers = %d, want <= %d", maximum, workers)
	}
	if len(seen) != jobCount || completed != jobCount {
		t.Fatalf("seen %d/completed %d, want %d", len(seen), completed, jobCount)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("job %s processed %d times", id, count)
		}
	}
}

func TestNewExecutorRequiresOrganizationAndValidBounds(t *testing.T) {
	store := NewMemoryStore()
	handler := func(context.Context, Job) error { return nil }
	cases := []struct {
		name    string
		options ExecutorOptions
	}{
		{name: "missing organization", options: executorTestOptions("", "owner")},
		{name: "zero workers", options: func() ExecutorOptions {
			value := executorTestOptions(testOrganizationID, "owner")
			value.Workers = -1
			return value
		}()},
		{name: "heartbeat beyond lease", options: func() ExecutorOptions {
			value := executorTestOptions(testOrganizationID, "owner")
			value.HeartbeatInterval = time.Second
			return value
		}()},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewExecutor(store, handler, test.options); !errors.Is(err, ErrInvalidJob) {
				t.Fatalf("NewExecutor() error = %v, want ErrInvalidJob", err)
			}
		})
	}
}
