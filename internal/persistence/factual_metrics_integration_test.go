//go:build integration

package persistence_test

import (
	"context"
	"errors"
	"testing"

	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

type factualMetricsIntegrationRecorder struct {
	records []persistence.FactualMetricsRecord
}

func (r *factualMetricsIntegrationRecorder) RecordFactualMetrics(_ context.Context, record persistence.FactualMetricsRecord) {
	r.records = append(r.records, record)
}

func TestFactualMetricsIntegrationPersistsAndReusesFacts(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "factual-metrics", "snapshot-1", "class FactualMetrics {}")
	if _, err := persistence.NewRepository(database.pool).PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("seed canonical bundle: %v", err)
	}
	input := factualRepositoryInput(t, fixture, "evidence-present")
	recorder := &factualMetricsIntegrationRecorder{}
	repository := persistence.NewRepository(database.pool, persistence.WithFactualMetricsRecorder(recorder))

	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("first factual persistence: %v", err)
	}
	assertFactualMetricsRecord(t, recorder, 0, persistence.FactualMetricsOperationPersistFactualSnapshot, persistence.FactualMetricsOutcomeCommitted, persistence.FactualMetrics{Accepted: 2, Derived: 1})

	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("identical factual retry: %v", err)
	}
	assertFactualMetricsRecord(t, recorder, 1, persistence.FactualMetricsOperationPersistFactualSnapshot, persistence.FactualMetricsOutcomeCommitted, persistence.FactualMetrics{Reused: 2, Derived: 1})

	manifestConflict := input
	manifestConflict.FrontendManifests = append([]fact.FrontendManifest(nil), input.FrontendManifests...)
	manifestConflict.FrontendManifests[0].Limitations = []string{"changed-but-valid"}
	if err := repository.PersistFactualSnapshot(context.Background(), manifestConflict); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("factual conflict error = %v, want ErrConflict", err)
	}
	assertFactualMetricsRecord(t, recorder, 2, persistence.FactualMetricsOperationPersistFactualSnapshot, persistence.FactualMetricsOutcomeRejected, persistence.FactualMetrics{Rejected: 2})
	if len(recorder.records) != 3 {
		t.Fatalf("factual metrics records = %d, want exactly three", len(recorder.records))
	}
}

func TestFactualMetricsIntegrationBundleResultAndRecorder(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "bundle-metrics", "snapshot-1", "class BundleMetrics {}")
	input := batchIntegrationV2Bundle(t, fixture)
	recorder := &factualMetricsIntegrationRecorder{}
	repository := persistence.NewRepository(database.pool, persistence.WithFactualMetricsRecorder(recorder))

	first, err := repository.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("first v1alpha2 bundle persistence: %v", err)
	}
	if first.FactualMetrics != (persistence.FactualMetrics{Accepted: 1}) {
		t.Fatalf("first bundle result metrics = %#v, want one accepted fact", first.FactualMetrics)
	}
	assertFactualMetricsRecord(t, recorder, 0, persistence.FactualMetricsOperationPersistBundle, persistence.FactualMetricsOutcomeCommitted, first.FactualMetrics)

	second, err := repository.PersistBundle(context.Background(), input)
	if err != nil {
		t.Fatalf("v1alpha2 bundle retry: %v", err)
	}
	if second.FactualMetrics != (persistence.FactualMetrics{Reused: 1}) {
		t.Fatalf("bundle retry result metrics = %#v, want one reused fact", second.FactualMetrics)
	}
	assertFactualMetricsRecord(t, recorder, 1, persistence.FactualMetricsOperationPersistBundle, persistence.FactualMetricsOutcomeCommitted, second.FactualMetrics)

	legacyFixture := integrationFixture(t, "bundle-metrics-v1", "snapshot-1", "class BundleMetricsV1 {}")
	legacy, err := repository.PersistBundle(context.Background(), legacyFixture.input)
	if err != nil {
		t.Fatalf("v1alpha1 bundle persistence: %v", err)
	}
	if legacy.FactualMetrics != (persistence.FactualMetrics{}) {
		t.Fatalf("v1alpha1 result metrics = %#v, want zero", legacy.FactualMetrics)
	}
	assertFactualMetricsRecord(t, recorder, 2, persistence.FactualMetricsOperationPersistBundle, persistence.FactualMetricsOutcomeCommitted, persistence.FactualMetrics{})
	if len(recorder.records) != 3 {
		t.Fatalf("bundle metrics records = %d, want exactly three", len(recorder.records))
	}
}

func assertFactualMetricsRecord(t *testing.T, recorder *factualMetricsIntegrationRecorder, index int, operation persistence.FactualMetricsOperation, outcome persistence.FactualMetricsOutcome, metrics persistence.FactualMetrics) {
	t.Helper()
	if len(recorder.records) <= index {
		t.Fatalf("metrics records = %d, missing record %d", len(recorder.records), index)
	}
	record := recorder.records[index]
	if record.Operation != operation || record.Outcome != outcome || record.Metrics != metrics {
		t.Fatalf("metrics record %d = %#v, want operation=%q outcome=%q metrics=%#v", index, record, operation, outcome, metrics)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("metrics record %d invalid: %v", index, err)
	}
}
