package evaluation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/bundle"
)

func runnerFixture(t *testing.T) CaseSet {
	t.Helper()
	cases, err := LoadCases("../../testdata/evaluation/cases.json")
	if err != nil {
		t.Fatalf("LoadCases() error = %v", err)
	}
	return cases
}

func TestRunSimulatedMeasuresPipelineAndKeepsReportSafe(t *testing.T) {
	cases := runnerFixture(t)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	report, err := Run(context.Background(), Config{Cases: cases, RunID: "run-evaluation-test", Repeat: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.Version != ReportVersion || report.Mode != ModeSimulated || report.RunID != "run-evaluation-test" {
		t.Fatalf("report identity = %#v", report)
	}
	if len(report.Cases) != len(cases.Cases) {
		t.Fatalf("case count = %d, want %d", len(report.Cases), len(cases.Cases))
	}
	for index, item := range report.Cases {
		if item.CaseID != cases.Cases[index].CaseID {
			t.Fatalf("case order at %d = %q, want %q", index, item.CaseID, cases.Cases[index].CaseID)
		}
		if item.Extraction.Status != StageCompleted || item.Ingestion.Status != StageCompleted || item.Retrieval.Status != StageCompleted || item.Generation.Status != StageCompleted {
			t.Fatalf("case %s stage status = %#v", item.CaseID, item)
		}
		if item.RetrievalMetrics.EvidenceRecallAtK != 1 {
			t.Logf("case %s report = %#v", item.CaseID, item)
			t.Fatalf("case %s recall = %v, want 1", item.CaseID, item.RetrievalMetrics.EvidenceRecallAtK)
		}
		if item.Volume.ContentBytes == 0 || item.Volume.PackageUnits == 0 {
			t.Fatalf("case %s volume = %#v", item.CaseID, item.Volume)
		}
		if item.Reuse.EvidenceReused == 0 || item.Reuse.EmbeddingsReused == 0 {
			t.Fatalf("case %s reuse = %#v", item.CaseID, item.Reuse)
		}
	}
	if report.Summary.Cases != len(cases.Cases) || report.ReproducibilityDigest == "" {
		t.Fatalf("summary/digest = %#v/%q", report.Summary, report.ReproducibilityDigest)
	}
	repeated, err := Run(context.Background(), Config{Cases: cases, RunID: "run-evaluation-test", Repeat: true, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.ReproducibilityDigest != report.ReproducibilityDigest {
		t.Fatalf("reproducibility digest changed: %q != %q", repeated.ReproducibilityDigest, report.ReproducibilityDigest)
	}
	if _, err := MarshalReport(report); err != nil {
		t.Fatalf("MarshalReport() error = %v", err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"secret=", "api_key", "-----begin", "password=", "simulated evidence"} {
		if strings.Contains(strings.ToLower(string(encoded)), unsafe) {
			t.Fatalf("report contains unsafe/raw marker %q", unsafe)
		}
	}
}

func TestRunSimulatedAbstainsForExecutionCase(t *testing.T) {
	cases := runnerFixture(t)
	selected := CaseSet{Version: cases.Version, Cases: []EvaluationCase{cases.Cases[0]}}
	report, err := RunSimulated(context.Background(), Config{Cases: selected, RunID: "run-abstention", Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cases) != 1 {
		t.Fatalf("cases = %d", len(report.Cases))
	}
	metric := report.Cases[0].Generation
	if !metric.Abstained || !metric.ExpectedAbstention || !metric.AbstentionCorrect || metric.Provider != string(queryProviderNone()) {
		t.Fatalf("abstention metric = %#v", metric)
	}
}

func TestRunAttributesExtractionFailureWithoutLeakingError(t *testing.T) {
	cases := runnerFixture(t)
	extractor := extractorFunc(func(context.Context, EvaluationCase) (bundle.Bundle, map[string]string, error) {
		return bundle.Bundle{}, nil, errors.New("raw source secret")
	})
	report, err := Run(context.Background(), Config{Cases: CaseSet{Version: cases.Version, Cases: []EvaluationCase{cases.Cases[0]}}, Extractor: extractor, RunID: "run-failure", Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	if err != nil {
		t.Fatal(err)
	}
	item := report.Cases[0]
	if item.FailureStage != FailureStageExtraction || item.FailureCode != "extraction_failed" || item.Outcome != "failed" {
		t.Fatalf("failure attribution = %#v", item)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "raw source secret") {
		t.Fatal("raw extractor error leaked into report")
	}
}

func TestRunRejectsLiveMode(t *testing.T) {
	_, err := Run(context.Background(), Config{Mode: Mode("live"), Cases: CaseSet{Version: Version, Cases: runnerFixture(t).Cases}})
	if err == nil {
		t.Fatal("Run() error = nil, want unsupported mode")
	}
}

type extractorFunc func(context.Context, EvaluationCase) (bundle.Bundle, map[string]string, error)

func (f extractorFunc) Extract(ctx context.Context, item EvaluationCase) (bundle.Bundle, map[string]string, error) {
	return f(ctx, item)
}

// These tiny aliases keep runner tests focused on the report contract while
// avoiding provider-specific values in test assertions.
func queryProviderNone() string { return "none" }
