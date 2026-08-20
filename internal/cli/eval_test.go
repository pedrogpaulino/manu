package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
)

const evalFixturePath = "../../testdata/evaluation/cases.json"

func TestEvalIsRoutedAndAdvertisedByHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("Run(help) = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "eval") {
		t.Fatalf("help = %q, missing eval command", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"eval", "--help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("Run(eval --help) = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: manu eval") || stderr.Len() != 0 {
		t.Fatalf("eval help = stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestEvalSimulatedWritesCanonicalJSONAtomically(t *testing.T) {
	output := filepath.Join(t.TempDir(), "evaluation.json")
	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"eval",
		"--cases", evalFixturePath,
		"--top-k", "5",
		"--output", output,
		"--json",
	}, &stdout, &stderr)
	if code != ExitPartial {
		t.Fatalf("Run(eval) = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("stdout is not JSON: %q", stdout.String())
	}
	reportData, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	if !bytes.Equal(reportData, stdout.Bytes()) {
		t.Fatalf("file report differs from stdout JSON")
	}
	var report evaluation.Report
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("decode report: %v", err)
	}
	if report.Mode != evaluation.ModeSimulated || report.Version != evaluation.ReportVersion || len(report.Cases) == 0 {
		t.Fatalf("report identity = %#v", report)
	}
	if report.Summary.EvidenceReused == 0 {
		t.Fatalf("default evaluation did not measure evidence reuse: %#v", report.Summary)
	}
	reusedEmbeddings := 0
	for _, item := range report.Cases {
		reusedEmbeddings += item.Reuse.EmbeddingsReused
	}
	if reusedEmbeddings == 0 {
		t.Fatalf("default evaluation did not measure embedding reuse")
	}
	if strings.Contains(strings.ToLower(string(reportData)), "secret=") || strings.Contains(strings.ToLower(string(reportData)), "api_key") {
		t.Fatalf("report contains unsafe marker")
	}
}

func TestEvalHumanOutputAndBoundedFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"eval", "--cases", evalFixturePath, "--repeat"}, &stdout, &stderr); code != ExitPartial {
		t.Fatalf("human eval code = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	for _, want := range []string{"evaluation complete", "mode: simulated", "evidence recall@5:", "abstentions:"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("human output = %q, missing %q", stdout.String(), want)
		}
	}

	for _, args := range [][]string{
		{"eval", "--top-k", "0"},
		{"eval", "--top-k", "1001"},
		{"eval", "--cases", ""},
		{"eval", "--format", "yaml"},
		{"eval", "--json", "--format", "yaml"},
		{"eval", "unexpected"},
	} {
		stdout.Reset()
		stderr.Reset()
		if code := Run(args, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("Run(%v) = %d, want %d; stdout=%q stderr=%q", args, code, ExitUsage, stdout.String(), stderr.String())
		}
	}
}

func TestEvalExitCodesAndDiagnostics(t *testing.T) {
	partial := evaluation.Report{Version: evaluation.ReportVersion, Mode: evaluation.ModeSimulated, RunID: "partial", Cases: []evaluation.CaseReport{{Outcome: "partial"}}}
	var stdout, stderr bytes.Buffer
	if code := runEvalWith(context.Background(), []string{"--json"}, &stdout, &stderr, func(context.Context, evaluation.Config) (evaluation.Report, error) {
		return partial, nil
	}); code != ExitPartial {
		t.Fatalf("partial eval code = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	if !json.Valid(stdout.Bytes()) {
		t.Fatalf("partial JSON is invalid: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := runEvalWith(context.Background(), nil, &stdout, &stderr, func(context.Context, evaluation.Config) (evaluation.Report, error) {
		return evaluation.Report{}, errors.New("secret=must-not-leak")
	}); code != ExitTechnical {
		t.Fatalf("technical eval code = %d, want %d", code, ExitTechnical)
	}
	if strings.Contains(stderr.String(), "must-not-leak") || strings.Contains(stderr.String(), "secret=") {
		t.Fatalf("diagnostic leaked runner error: %q", stderr.String())
	}
}

func TestEvalLiveRequiresExplicitGates(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"eval", "--live"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("live without gates code = %d, want %d; stdout=%q stderr=%q", code, ExitUsage, stdout.String(), stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "api_key") || strings.Contains(strings.ToLower(stderr.String()), "secret") {
		t.Fatalf("live gate diagnostic leaked sensitive material: %q", stderr.String())
	}
}

func TestEvalLiveUsesOnlyInjectedProviderPort(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received evaluation.LiveConfig
	configuration := config.Default()
	configuration.Policy.ExternalTransfer = config.DecisionAllow
	configuration.Generation.Enabled = true
	configuration.Generation.Provider = config.ProviderOpenRouter
	configuration.Generation.Model = "openai/gpt-4o-mini"
	configuration.Generation.BaseURL = "http://127.0.0.1:1"
	configuration.Generation.APIKey = "test-key"
	configuration.Generation.Protocol = config.ProtocolChatCompletions
	configuration.Generation.Budget = config.BudgetConfig{MaxRequests: 16, MaxInputTokens: 100_000, MaxOutputTokens: 1_000, MaxCostUSD: 100_000}
	configuration.Evaluation.Live = true
	configuration.Evaluation.Budget = configuration.Generation.Budget
	args := []string{
		"--live", "--confirm-policy", "--confirm-transfer", "--transfer-policy", "allow",
		"--provider", string(configuration.Generation.Provider), "--model", configuration.Generation.Model, "--cases", evalFixturePath,
		"--max-requests", "16", "--max-input-tokens", "100000", "--max-output-tokens", "1000", "--max-cost-usd", "100000",
		"--price-table-version", "prices-2026-08-20", "--input-token-price-usd", "0.01", "--output-token-price-usd", "0.02", "--json",
	}
	fake := liveProviderForCLI{}
	load := func() (config.Config, error) { return configuration, nil }
	factory := func(config.Config) (aigateway.Generator, error) { return fake, nil }
	liveRun := func(ctx context.Context, liveConfig evaluation.LiveConfig) (evaluation.Report, error) {
		received = liveConfig
		return evaluation.RunLive(ctx, liveConfig)
	}
	if code := runEvalWithDependencies(context.Background(), args, &stdout, &stderr, evaluation.RunSimulated, liveRun, load, factory); code != ExitPartial {
		t.Fatalf("live eval code = %d, want %d; stdout=%q stderr=%q", code, ExitPartial, stdout.String(), stderr.String())
	}
	var report evaluation.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("live JSON: %v; output=%q", err, stdout.String())
	}
	if report.Mode != evaluation.ModeLive || received.ProviderClient == nil || received.Provider != string(configuration.Generation.Provider) || received.RequestedModel != configuration.Generation.Model || received.Generation.Provider != aigateway.ProviderOpenRouter {
		t.Fatalf("live configuration/report = %#v/%#v", received, report)
	}
}

type liveProviderForCLI struct{}

func (liveProviderForCLI) Generate(_ context.Context, request aigateway.GenerationRequest) (aigateway.GenerationResult, error) {
	ids := make([]string, 0, len(request.Package.Evidence))
	for _, item := range request.Package.Evidence {
		ids = append(ids, item.ID)
	}
	return aigateway.GenerationResult{
		ExecutionID: request.ExecutionID,
		RequestID:   request.RequestID,
		Provider:    request.Profile.Provider,
		Model:       "model-effective",
		Output:      aigateway.GenerationEnvelope{Version: aigateway.GenerationEnvelopeVersion, Text: "live cli response", PackageDigest: request.Package.Digest, EvidenceIDs: ids},
		Usage:       aigateway.Usage{InputItems: 1, OutputItems: 1, InputTokens: 1, OutputTokens: 1},
		Termination: aigateway.TerminationCompleted,
	}, nil
}

func TestWriteEvaluationReportAtomicReplacesOnlyAfterCompleteWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeEvaluationReportAtomic(path, []byte("new")); err != nil {
		t.Fatalf("writeEvaluationReportAtomic() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("report contents = %q, want new", data)
	}
}
