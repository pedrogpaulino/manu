package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestFactualMetricsValidate(t *testing.T) {
	tests := []struct {
		name  string
		value FactualMetrics
		valid bool
	}{
		{name: "zero counts", value: FactualMetrics{}, valid: true},
		{name: "large nonnegative counts", value: FactualMetrics{Accepted: 1 << 62, Reused: 1 << 61, Derived: 1 << 62, FanoutLimited: 1 << 62}, valid: true},
		{name: "negative accepted", value: FactualMetrics{Accepted: -1}},
		{name: "negative reused", value: FactualMetrics{Reused: -1}},
		{name: "negative rejected", value: FactualMetrics{Rejected: -1}},
		{name: "negative derived", value: FactualMetrics{Derived: -1}},
		{name: "negative fanout limited", value: FactualMetrics{FanoutLimited: -1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.value.Validate()
			if tt.valid {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidFactualMetrics) || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Validate() error = %v, want invalid factual metrics/input", err)
			}
		})
	}
}

func TestFactualMetricsRecordValidate(t *testing.T) {
	validCommitted := FactualMetricsRecord{
		Operation: FactualMetricsOperationPersistBundle,
		Outcome:   FactualMetricsOutcomeCommitted,
		Metrics:   FactualMetrics{Accepted: 2, Reused: 1, Derived: 3, FanoutLimited: 1},
	}
	validRejected := FactualMetricsRecord{
		Operation: FactualMetricsOperationPersistFactualSnapshot,
		Outcome:   FactualMetricsOutcomeRejected,
		Metrics:   FactualMetrics{Rejected: 1},
	}
	tests := []struct {
		name  string
		value FactualMetricsRecord
		valid bool
	}{
		{name: "committed", value: validCommitted, valid: true},
		{name: "rejected", value: validRejected, valid: true},
		{name: "rejected without facts", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeRejected}, valid: true},
		{name: "unknown operation", value: FactualMetricsRecord{Outcome: FactualMetricsOutcomeCommitted}},
		{name: "unsupported operation", value: FactualMetricsRecord{Operation: FactualMetricsOperation("persist_unknown"), Outcome: FactualMetricsOutcomeCommitted}},
		{name: "unknown outcome", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle}},
		{name: "unsupported outcome", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcome("failed")}},
		{name: "committed rejected count", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeCommitted, Metrics: FactualMetrics{Rejected: 1}}},
		{name: "committed derived exceeds available", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeCommitted, Metrics: FactualMetrics{Accepted: 1, Reused: 1, Derived: 3}}},
		{name: "rejected accepted count", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeRejected, Metrics: FactualMetrics{Accepted: 1}}},
		{name: "rejected reused count", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeRejected, Metrics: FactualMetrics{Reused: 1}}},
		{name: "rejected derived count", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeRejected, Metrics: FactualMetrics{Derived: 1}}},
		{name: "rejected fanout count", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeRejected, Metrics: FactualMetrics{FanoutLimited: 1}}},
		{name: "negative count", value: FactualMetricsRecord{Operation: FactualMetricsOperationPersistBundle, Outcome: FactualMetricsOutcomeCommitted, Metrics: FactualMetrics{Accepted: -1}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.value.Validate()
			if tt.valid {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidFactualMetrics) || !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Validate() error = %v, want invalid factual metrics/input", err)
			}
		})
	}
}

func TestFactualMetricsEnumCardinality(t *testing.T) {
	operations := []FactualMetricsOperation{
		FactualMetricsOperationUnknown,
		FactualMetricsOperationPersistBundle,
		FactualMetricsOperationPersistFactualSnapshot,
		FactualMetricsOperation("persist_unknown"),
	}
	validOperations := 0
	for _, operation := range operations {
		if validFactualMetricsOperation(operation) {
			validOperations++
		}
	}
	if validOperations != 2 {
		t.Fatalf("valid operation count = %d, want 2", validOperations)
	}

	outcomes := []FactualMetricsOutcome{
		FactualMetricsOutcomeUnknown,
		FactualMetricsOutcomeCommitted,
		FactualMetricsOutcomeRejected,
		FactualMetricsOutcome("failed"),
	}
	validOutcomes := 0
	for _, outcome := range outcomes {
		if validFactualMetricsOutcome(outcome) {
			validOutcomes++
		}
	}
	if validOutcomes != 2 {
		t.Fatalf("valid outcome count = %d, want 2", validOutcomes)
	}
}

func TestFactualMetricsRecordLogValueAllowlist(t *testing.T) {
	tests := []struct {
		name  string
		value FactualMetricsRecord
	}{
		{
			name: "valid record",
			value: FactualMetricsRecord{
				Operation: FactualMetricsOperationPersistFactualSnapshot,
				Outcome:   FactualMetricsOutcomeCommitted,
				Metrics:   FactualMetrics{Accepted: 4, Reused: 2, Rejected: 0, Derived: 1, FanoutLimited: 3},
			},
		},
		{
			name: "invalid enum is normalized",
			value: FactualMetricsRecord{
				Operation: FactualMetricsOperation("secret-organization-id"),
				Outcome:   FactualMetricsOutcome("secret-database-error"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&output, nil))
			logger.Info("factual metrics", "factual_metrics", tt.value)

			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(output.Bytes(), &envelope); err != nil {
				t.Fatalf("decode log: %v", err)
			}
			var group map[string]json.RawMessage
			if err := json.Unmarshal(envelope["factual_metrics"], &group); err != nil {
				t.Fatalf("decode factual metrics group: %v", err)
			}
			keys := make([]string, 0, len(group))
			for key := range group {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			wantKeys := []string{"accepted", "derived", "fanout_limited", "operation", "outcome", "rejected", "reused"}
			if !reflect.DeepEqual(keys, wantKeys) {
				t.Fatalf("log group keys = %#v, want %#v", keys, wantKeys)
			}
			if strings.Contains(output.String(), "secret-organization-id") || strings.Contains(output.String(), "secret-database-error") {
				t.Fatalf("log exposed unbounded sentinel: %s", output.String())
			}
		})
	}
}

func TestFactualMetricsRecorderOptionAndHelper(t *testing.T) {
	var captured []FactualMetricsRecord
	recorder := FactualMetricsRecorderFunc(func(_ context.Context, record FactualMetricsRecord) {
		captured = append(captured, record)
	})
	repository := NewRepository(nil, WithFactualMetricsRecorder(recorder), nil)
	valid := FactualMetricsRecord{
		Operation: FactualMetricsOperationPersistBundle,
		Outcome:   FactualMetricsOutcomeCommitted,
		Metrics:   FactualMetrics{Accepted: 1},
	}
	repository.recordFactualMetrics(context.Background(), valid)
	if !reflect.DeepEqual(captured, []FactualMetricsRecord{valid}) {
		t.Fatalf("captured records = %#v, want %#v", captured, []FactualMetricsRecord{valid})
	}

	repository.recordFactualMetrics(context.Background(), FactualMetricsRecord{})
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	repository.recordFactualMetrics(canceled, valid)
	if len(captured) != 1 {
		t.Fatalf("captured records after invalid/canceled calls = %d, want 1", len(captured))
	}

	NewRepository(nil).recordFactualMetrics(context.Background(), valid)
	NewRepository(nil, WithFactualMetricsRecorder(nil)).recordFactualMetrics(context.Background(), valid)
	var nilFunction FactualMetricsRecorderFunc
	NewRepository(nil, WithFactualMetricsRecorder(nilFunction)).recordFactualMetrics(context.Background(), valid)
}

func TestFactualMetricsRecorderCannotPanicIntoCaller(t *testing.T) {
	repository := NewRepository(nil, WithFactualMetricsRecorder(FactualMetricsRecorderFunc(func(context.Context, FactualMetricsRecord) {
		panic("secret factual recorder failure")
	})))
	valid := FactualMetricsRecord{
		Operation: FactualMetricsOperationPersistBundle,
		Outcome:   FactualMetricsOutcomeRejected,
		Metrics:   FactualMetrics{Rejected: 1},
	}
	repository.recordFactualMetrics(context.Background(), valid)
}

func TestFactualMetricsTypesContainOnlySafeFields(t *testing.T) {
	recordType := reflect.TypeOf(FactualMetricsRecord{})
	if recordType.NumField() != 3 {
		t.Fatalf("FactualMetricsRecord fields = %d, want 3", recordType.NumField())
	}
	metricsType := reflect.TypeOf(FactualMetrics{})
	if metricsType.NumField() != 5 {
		t.Fatalf("FactualMetrics fields = %d, want 5", metricsType.NumField())
	}
	for index := 0; index < recordType.NumField(); index++ {
		if recordType.Field(index).Tag.Get("json") == "" {
			t.Fatalf("record field %s has no JSON tag", recordType.Field(index).Name)
		}
	}
	for index := 0; index < metricsType.NumField(); index++ {
		if metricsType.Field(index).Tag.Get("json") == "" {
			t.Fatalf("metrics field %s has no JSON tag", metricsType.Field(index).Name)
		}
	}
}
