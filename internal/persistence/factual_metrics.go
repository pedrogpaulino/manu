package persistence

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
)

// ErrInvalidFactualMetrics identifies a metric record that cannot cross the
// factual observability boundary. It intentionally contains no record values.
var ErrInvalidFactualMetrics = fmt.Errorf("%w: invalid factual metrics", ErrInvalidInput)

// FactualMetricsOperation identifies the persistence operation summarized by
// a factual metrics record. The zero value is intentionally invalid.
type FactualMetricsOperation string

const (
	FactualMetricsOperationUnknown                FactualMetricsOperation = ""
	FactualMetricsOperationPersistBundle          FactualMetricsOperation = "persist_bundle"
	FactualMetricsOperationPersistFactualSnapshot FactualMetricsOperation = "persist_factual_snapshot"
)

// FactualMetricsOutcome identifies whether the summarized operation committed
// or was rejected. The zero value is intentionally invalid.
type FactualMetricsOutcome string

const (
	FactualMetricsOutcomeUnknown   FactualMetricsOutcome = ""
	FactualMetricsOutcomeCommitted FactualMetricsOutcome = "committed"
	FactualMetricsOutcomeRejected  FactualMetricsOutcome = "rejected"
)

// FactualMetrics contains bounded-cardinality factual persistence counters.
// Counts are deliberately independent of identities, scopes, payloads, and
// error details.
type FactualMetrics struct {
	Accepted      int64 `json:"accepted"`
	Reused        int64 `json:"reused"`
	Rejected      int64 `json:"rejected"`
	Derived       int64 `json:"derived"`
	FanoutLimited int64 `json:"fanout_limited"`
}

// FactualMetricsRecord is the complete safe observability event. Its shape is
// intentionally limited to a bounded operation, outcome, and five counts.
type FactualMetricsRecord struct {
	Operation FactualMetricsOperation `json:"operation"`
	Outcome   FactualMetricsOutcome   `json:"outcome"`
	Metrics   FactualMetrics          `json:"metrics"`
}

// FactualMetricsRecorder receives safe factual metrics without influencing
// the outcome of the operation that produced them.
type FactualMetricsRecorder interface {
	RecordFactualMetrics(context.Context, FactualMetricsRecord)
}

// FactualMetricsRecorderFunc adapts a function to FactualMetricsRecorder.
type FactualMetricsRecorderFunc func(context.Context, FactualMetricsRecord)

// RecordFactualMetrics implements FactualMetricsRecorder.
func (f FactualMetricsRecorderFunc) RecordFactualMetrics(ctx context.Context, record FactualMetricsRecord) {
	if f != nil {
		f(ctx, record)
	}
}

// RepositoryOption configures a Repository at construction time.
type RepositoryOption func(*Repository)

// WithFactualMetricsRecorder configures an optional factual metrics recorder.
// A nil recorder leaves the repository without metrics collection.
func WithFactualMetricsRecorder(recorder FactualMetricsRecorder) RepositoryOption {
	return func(repository *Repository) {
		if repository != nil {
			repository.factualMetricsRecorder = recorder
		}
	}
}

// Validate checks that all factual counters are non-negative.
func (m FactualMetrics) Validate() error {
	if m.Accepted < 0 || m.Reused < 0 || m.Rejected < 0 || m.Derived < 0 || m.FanoutLimited < 0 {
		return ErrInvalidFactualMetrics
	}
	return nil
}

// Validate checks the bounded operation/outcome enums and their count
// invariants. It does not impose an arbitrary upper bound on int64 counters.
func (r FactualMetricsRecord) Validate() error {
	if !validFactualMetricsOperation(r.Operation) || !validFactualMetricsOutcome(r.Outcome) {
		return ErrInvalidFactualMetrics
	}
	if err := r.Metrics.Validate(); err != nil {
		return err
	}

	switch r.Outcome {
	case FactualMetricsOutcomeCommitted:
		if r.Metrics.Rejected != 0 || derivedExceedsAvailable(r.Metrics) {
			return ErrInvalidFactualMetrics
		}
	case FactualMetricsOutcomeRejected:
		if r.Metrics.Accepted != 0 || r.Metrics.Reused != 0 || r.Metrics.Derived != 0 || r.Metrics.FanoutLimited != 0 {
			return ErrInvalidFactualMetrics
		}
	default:
		return ErrInvalidFactualMetrics
	}
	return nil
}

// LogValue exposes only the stable, bounded factual metrics group. Invalid
// enum values are normalized rather than emitted as caller-controlled strings.
func (r FactualMetricsRecord) LogValue() slog.Value {
	operation := r.Operation
	if !validFactualMetricsOperation(operation) {
		operation = FactualMetricsOperationUnknown
	}
	outcome := r.Outcome
	if !validFactualMetricsOutcome(outcome) {
		outcome = FactualMetricsOutcomeUnknown
	}
	return slog.GroupValue(
		slog.String("operation", string(operation)),
		slog.String("outcome", string(outcome)),
		slog.Int64("accepted", r.Metrics.Accepted),
		slog.Int64("reused", r.Metrics.Reused),
		slog.Int64("rejected", r.Metrics.Rejected),
		slog.Int64("derived", r.Metrics.Derived),
		slog.Int64("fanout_limited", r.Metrics.FanoutLimited),
	)
}

func validFactualMetricsOperation(operation FactualMetricsOperation) bool {
	switch operation {
	case FactualMetricsOperationPersistBundle, FactualMetricsOperationPersistFactualSnapshot:
		return true
	default:
		return false
	}
}

func validFactualMetricsOutcome(outcome FactualMetricsOutcome) bool {
	switch outcome {
	case FactualMetricsOutcomeCommitted, FactualMetricsOutcomeRejected:
		return true
	default:
		return false
	}
}

func derivedExceedsAvailable(metrics FactualMetrics) bool {
	if metrics.Derived <= metrics.Accepted {
		return false
	}
	return metrics.Derived-metrics.Accepted > metrics.Reused
}

// recordFactualMetrics sends only valid, context-live records. Metrics are
// telemetry-only: a recorder error or panic cannot change the caller's
// operation result.
func (r *Repository) recordFactualMetrics(ctx context.Context, record FactualMetricsRecord) {
	if r == nil || factualMetricsRecorderIsNil(r.factualMetricsRecorder) {
		return
	}
	if validateContext(ctx) != nil || record.Validate() != nil {
		return
	}
	recorder := r.factualMetricsRecorder
	defer func() {
		_ = recover()
	}()
	recorder.RecordFactualMetrics(ctx, record)
}

func factualMetricsRecorderIsNil(recorder FactualMetricsRecorder) bool {
	if recorder == nil {
		return true
	}
	value := reflect.ValueOf(recorder)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
