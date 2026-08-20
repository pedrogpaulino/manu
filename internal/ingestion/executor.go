package ingestion

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// JobHandler performs the work for one claimed job. Implementations must
// honor cancellation and must not place raw source content in returned
// errors. Executor diagnostics remain stable even when a handler does not.
type JobHandler func(context.Context, Job) error

// ExecutorOptions bounds the in-process worker pool and its lease cadence.
// Zero values are filled by NewExecutor with conservative defaults.
type ExecutorOptions struct {
	OrganizationID    string
	Workers           int
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	PollInterval      time.Duration
	Owner             string
}

const (
	defaultExecutorWorkers   = 1
	defaultExecutorLease     = 30 * time.Second
	defaultExecutorHeartbeat = 10 * time.Second
	defaultExecutorPoll      = 100 * time.Millisecond
	minimumExecutorPoll      = time.Millisecond
	jobCleanupTimeout        = 5 * time.Second
)

// DefaultExecutorOptions returns a bounded single-worker configuration.
func DefaultExecutorOptions() ExecutorOptions {
	owner, err := newUUID()
	if err != nil {
		owner = "executor"
	}
	return ExecutorOptions{
		Workers:           defaultExecutorWorkers,
		LeaseDuration:     defaultExecutorLease,
		HeartbeatInterval: defaultExecutorHeartbeat,
		PollInterval:      defaultExecutorPoll,
		Owner:             owner,
	}
}

// Executor claims and processes jobs in the same process. It does not own a
// queue, a database connection, or a pipeline; those remain injected ports.
type Executor struct {
	store   JobStore
	handler JobHandler
	options ExecutorOptions
}

// NewExecutor validates and composes a bounded executor without starting any
// goroutines or opening external resources.
func NewExecutor(store JobStore, handler JobHandler, options ExecutorOptions) (*Executor, error) {
	if store == nil {
		return nil, fmt.Errorf("%w: job store is required", ErrInvalidJob)
	}
	if handler == nil {
		return nil, fmt.Errorf("%w: job handler is required", ErrInvalidJob)
	}
	if err := validateIdentifier("organization_id", options.OrganizationID); err != nil {
		return nil, err
	}
	defaults := DefaultExecutorOptions()
	if options.Workers == 0 {
		options.Workers = defaults.Workers
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = defaults.LeaseDuration
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = options.LeaseDuration / 3
		if options.HeartbeatInterval == 0 {
			options.HeartbeatInterval = defaults.HeartbeatInterval
		}
	}
	if options.PollInterval == 0 {
		options.PollInterval = defaults.PollInterval
	}
	if strings.TrimSpace(options.Owner) == "" {
		options.Owner = defaults.Owner
	}
	if options.Workers < 1 || options.LeaseDuration <= 0 || options.HeartbeatInterval <= 0 ||
		options.HeartbeatInterval >= options.LeaseDuration || options.PollInterval < minimumExecutorPoll {
		return nil, fmt.Errorf("%w: executor timing or worker limit is invalid", ErrInvalidJob)
	}
	if strings.ContainsAny(options.Owner, "\x00\r\n") || len(options.Owner) > maxJobIdentifierLength {
		return nil, fmt.Errorf("%w: executor owner is invalid", ErrInvalidJob)
	}
	return &Executor{store: store, handler: handler, options: options}, nil
}

// Run starts the configured workers and waits until cancellation or a store
// failure. Handler failures are recorded on their jobs and do not stop peers.
func (e *Executor) Run(ctx context.Context) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if e == nil || e.store == nil || e.handler == nil {
		return fmt.Errorf("%w: executor is not configured", ErrInvalidJob)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	workerErrors := make(chan error, 1)
	var workers sync.WaitGroup
	workers.Add(e.options.Workers)
	for worker := 0; worker < e.options.Workers; worker++ {
		owner := e.workerOwner(worker)
		go func() {
			defer workers.Done()
			if err := e.worker(runCtx, owner); err != nil && !errors.Is(err, context.Canceled) {
				select {
				case workerErrors <- err:
				default:
				}
				cancel()
			}
		}()
	}

	finished := make(chan struct{})
	go func() {
		workers.Wait()
		close(finished)
	}()
	select {
	case <-ctx.Done():
		cancel()
		<-finished
		return ctx.Err()
	case err := <-workerErrors:
		cancel()
		<-finished
		return err
	case <-finished:
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
}

// RunOnce claims at most one job for deterministic composition and tests.
// The boolean is false when no claimable job is available.
func (e *Executor) RunOnce(ctx context.Context) (bool, error) {
	if err := checkContext(ctx); err != nil {
		return false, err
	}
	if e == nil || e.store == nil || e.handler == nil {
		return false, fmt.Errorf("%w: executor is not configured", ErrInvalidJob)
	}
	return e.runOnce(ctx, e.options.Owner)
}

func (e *Executor) worker(ctx context.Context, owner string) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		claimed, err := e.runOnce(ctx, owner)
		if err != nil {
			if isJobLevelError(err) {
				if waitErr := waitForPoll(ctx, e.options.PollInterval); waitErr != nil {
					return waitErr
				}
				continue
			}
			return err
		}
		if claimed {
			continue
		}
		if err := waitForPoll(ctx, e.options.PollInterval); err != nil {
			return err
		}
	}
}

func (e *Executor) runOnce(ctx context.Context, owner string) (bool, error) {
	job, claimed, err := e.store.Claim(ctx, e.options.OrganizationID, owner, e.options.LeaseDuration)
	if err != nil || !claimed {
		return claimed, err
	}
	return true, e.process(ctx, owner, job)
}

func (e *Executor) process(ctx context.Context, owner string, job Job) error {
	workCtx, stopWork := context.WithCancel(ctx)
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		e.heartbeat(workCtx, owner, job, heartbeatErrors, stopWork)
	}()

	processingErr := e.handler(workCtx, job)
	stopWork()
	<-heartbeatDone
	var heartbeatErr error
	select {
	case heartbeatErr = <-heartbeatErrors:
	default:
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if heartbeatErr != nil && !errors.Is(heartbeatErr, context.Canceled) {
		return heartbeatErr
	}
	if processingErr != nil {
		if errors.Is(processingErr, ErrJobPartial) {
			// The handler already persisted the safe partial diagnostic. Do not
			// call Fail or Complete: both would erase the resumable state or
			// incorrectly publish an incomplete snapshot.
			return ErrJobPartial
		}
		diagnostic := Diagnostic{Code: DiagnosticCodeProcessing, Message: "job processing failed"}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), jobCleanupTimeout)
		_, failErr := e.store.Fail(cleanupCtx, job.OrganizationID, job.ID, owner, diagnostic)
		cancel()
		if failErr != nil {
			return normalizeJobError(failErr)
		}
		return ErrJobProcessing
	}
	latest, err := e.store.Get(ctx, job.OrganizationID, job.ID)
	if err != nil {
		return normalizeJobError(err)
	}
	if latest.State != JobStateRunning {
		if latest.State == JobStatePartial {
			return ErrJobPartial
		}
		if latest.State == JobStateFailed && latest.CancelRequested {
			return ErrJobCancelled
		}
		return ErrJobState
	}
	_, err = e.store.Complete(ctx, latest.OrganizationID, latest.ID, owner, latest.JobCounts)
	return normalizeJobError(err)
}

func (e *Executor) heartbeat(ctx context.Context, owner string, job Job, errorsOut chan<- error, cancelWork context.CancelFunc) {
	ticker := time.NewTicker(e.options.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := e.store.Heartbeat(ctx, job.OrganizationID, job.ID, owner, e.options.LeaseDuration); err != nil {
				cancelWork()
				select {
				case errorsOut <- normalizeJobError(err):
				default:
				}
				return
			}
		}
	}
}

func (e *Executor) workerOwner(worker int) string {
	return fmt.Sprintf("%s/%d", e.options.Owner, worker)
}

func waitForPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isJobLevelError(err error) bool {
	return errors.Is(err, ErrJobProcessing) || errors.Is(err, ErrJobLeaseLost) ||
		errors.Is(err, ErrJobCancelled) || errors.Is(err, ErrJobPartial) ||
		errors.Is(err, ErrJobState) || errors.Is(err, ErrJobNotFound)
}

func normalizeJobError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrJobProcessing) || errors.Is(err, ErrJobLeaseLost) ||
		errors.Is(err, ErrJobCancelled) || errors.Is(err, ErrJobPartial) ||
		errors.Is(err, ErrJobState) || errors.Is(err, ErrJobNotFound) {
		return err
	}
	if errors.Is(err, ErrInvalidJob) || errors.Is(err, ErrJobConflict) || errors.Is(err, ErrJobStore) {
		return err
	}
	return ErrJobStore
}
