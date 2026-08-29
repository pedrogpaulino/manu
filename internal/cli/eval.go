package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// EvaluationRunner is the seam used by manu eval. The production runner is
// always evaluation.RunSimulated; the seam keeps CLI tests local and avoids
// opening a socket or selecting a provider.
type EvaluationRunner func(context.Context, evaluation.Config) (evaluation.Report, error)

// LiveEvaluationRunner is the explicit seam for live evaluation. Production
// CLI wiring uses evaluation.RunLive after the typed configuration factory
// has built the selected external adapter; tests inject deterministic fakes.
type LiveEvaluationRunner func(context.Context, evaluation.LiveConfig) (evaluation.Report, error)

// runEval folds process signals into the evaluation context and delegates to
// the deterministic, no-network runner.
func runEval(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	return runEvalWithDependencies(ctx, args, stdout, stderr, evaluation.RunSimulated, evaluation.RunLive, config.Load, newProductionLiveGenerator)
}

func runEvalWith(ctx context.Context, args []string, stdout, stderr io.Writer, run EvaluationRunner) int {
	return runEvalWithDependencies(ctx, args, stdout, stderr, run, evaluation.RunLive, config.Load, newProductionLiveGenerator)
}

func runEvalWithRunners(ctx context.Context, args []string, stdout, stderr io.Writer, run EvaluationRunner, liveRun LiveEvaluationRunner) int {
	return runEvalWithDependencies(ctx, args, stdout, stderr, run, liveRun, config.Load, newProductionLiveGenerator)
}

func runEvalWithDependencies(ctx context.Context, args []string, stdout, stderr io.Writer, run EvaluationRunner, liveRun LiveEvaluationRunner, load EvalConfigLoader, factory LiveGeneratorFactory) int {
	if variantEvaluationRequested(args) {
		return runEvalVariantsWithDependencies(ctx, args, stdout, stderr, load, variantEvaluationRun)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	flagSet := newFlagSet("eval", stderr)
	casesPath := flagSet.String("cases", evaluation.DefaultCasesPath, "versioned evaluation cases JSON")
	// --cases-path is retained as a descriptive alias for scripts that prefer
	// to make the path semantics explicit.
	casesPathAlias := flagSet.String("cases-path", "", "alias for --cases")
	topK := flagSet.Int("top-k", 5, "bounded retrieval rank cutoff")
	repeat := flagSet.Bool("repeat", true, "repeat ingestion to measure reusable work (default true; use --repeat=false to disable)")
	output := flagSet.String("output", "", "write the canonical JSON report atomically to this file")
	format := flagSet.String("format", "human", "output format: human or json")
	jsonOutput := flagSet.Bool("json", false, "emit the versioned report as JSON")
	live := flagSet.Bool("live", false, "explicitly enable live evaluation; default is simulated")
	confirmPolicy := flagSet.Bool("confirm-policy", false, "confirm that the configured external-transfer policy is approved")
	confirmTransfer := flagSet.Bool("confirm-transfer", false, "confirm that evidence transfer to the provider is approved")
	transferPolicy := flagSet.String("transfer-policy", "deny", "external transfer policy: allow or deny")
	provider := flagSet.String("provider", "", "optional live provider consistency check; configuration is authoritative")
	model := flagSet.String("model", "", "optional live model consistency check; configuration is authoritative")
	maxRequests := flagSet.Int("max-requests", 0, "optional consistency check for configured live budget requests")
	maxInputTokens := flagSet.Int("max-input-tokens", 0, "optional consistency check for configured live input-token budget")
	maxOutputTokens := flagSet.Int("max-output-tokens", 0, "optional consistency check for configured live output-token budget")
	maxCostUSD := flagSet.Float64("max-cost-usd", 0, "optional consistency check for configured live cost budget")
	priceTableVersion := flagSet.String("price-table-version", "", "version of the explicit live token price table")
	inputTokenPriceUSD := flagSet.Float64("input-token-price-usd", 0, "explicit input token price in USD")
	outputTokenPriceUSD := flagSet.Float64("output-token-price-usd", 0, "explicit output token price in USD")
	flagSet.Usage = func() {
		_, _ = fmt.Fprintln(stdout, "usage: manu eval [--cases path] [--top-k n] [--repeat[=true|false]] [--output file] [--format human|json] [--json] [--live --confirm-policy --confirm-transfer ...]")
	}
	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu eval: positional arguments are not supported")
		return ExitUsage
	}
	if *casesPathAlias != "" {
		casesFlagProvided := false
		flagSet.Visit(func(flagValue *flag.Flag) {
			casesFlagProvided = casesFlagProvided || flagValue.Name == "cases"
		})
		if casesFlagProvided {
			fmt.Fprintln(stderr, "manu eval: use only one of --cases and --cases-path")
			return ExitUsage
		}
		*casesPath = *casesPathAlias
	}
	if strings.TrimSpace(*casesPath) == "" {
		fmt.Fprintln(stderr, "manu eval: --cases must not be empty")
		return ExitUsage
	}
	if *topK < 1 || *topK > retrieval.MaxFusionCandidates {
		fmt.Fprintf(stderr, "manu eval: --top-k must be between 1 and %d\n", retrieval.MaxFusionCandidates)
		return ExitUsage
	}
	if *jsonOutput {
		normalizedFormat := strings.ToLower(strings.TrimSpace(*format))
		if normalizedFormat != "" && normalizedFormat != "human" && normalizedFormat != "text" && normalizedFormat != "json" {
			fmt.Fprintln(stderr, "manu eval: invalid output format")
			return ExitUsage
		}
	}
	selectedFormat, err := outputFormat(*format, *jsonOutput)
	if err != nil {
		fmt.Fprintln(stderr, "manu eval:", err)
		return ExitUsage
	}
	if err := ctx.Err(); err != nil {
		writeEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	var report evaluation.Report
	if *live {
		if load == nil {
			load = config.Load
		}
		if factory == nil {
			factory = newProductionLiveGenerator
		}
		configuration, loadErr := load()
		if loadErr != nil {
			writeEvaluationDiagnostic(stderr, loadErr)
			return ExitTechnical
		}
		generation := configuration.Generation
		budget := configuration.Evaluation.Budget
		providerFlagSet := evalFlagSet(flagSet, "provider")
		modelFlagSet := evalFlagSet(flagSet, "model")
		budgetFlagMismatch := (evalFlagSet(flagSet, "max-requests") && *maxRequests != budget.MaxRequests) ||
			(evalFlagSet(flagSet, "max-input-tokens") && *maxInputTokens != budget.MaxInputTokens) ||
			(evalFlagSet(flagSet, "max-output-tokens") && *maxOutputTokens != budget.MaxOutputTokens) ||
			(evalFlagSet(flagSet, "max-cost-usd") && *maxCostUSD != budget.MaxCostUSD)
		transferFlagMismatch := evalFlagSet(flagSet, "transfer-policy") && *transferPolicy != string(configuration.Policy.ExternalTransfer)
		providerMismatch := providerFlagSet && strings.TrimSpace(*provider) != string(generation.Provider)
		modelMismatch := modelFlagSet && strings.TrimSpace(*model) != generation.Model
		if !configuration.Evaluation.Live || !configuration.Generation.Enabled || generation.Provider == config.ProviderSimulated ||
			configuration.Policy.ExternalTransfer != config.DecisionAllow || !*confirmPolicy || !*confirmTransfer ||
			*transferPolicy != evaluation.LiveTransferPolicyAllow || budgetFlagMismatch || transferFlagMismatch || providerMismatch || modelMismatch ||
			strings.TrimSpace(*priceTableVersion) == "" || *inputTokenPriceUSD <= 0 || *outputTokenPriceUSD <= 0 {
			fmt.Fprintln(stderr, "manu eval: live mode requires MANU_EVALUATION_LIVE, external generation, approved transfer policy, confirmations, configured budget, and versioned prices")
			return ExitUsage
		}
		profile, profileErr := liveGenerationProfile(configuration)
		if profileErr != nil {
			writeEvaluationDiagnostic(stderr, profileErr)
			return ExitUsage
		}
		generator, factoryErr := factory(configuration)
		if factoryErr != nil {
			writeEvaluationDiagnostic(stderr, factoryErr)
			return ExitTechnical
		}
		if liveRun == nil {
			liveRun = evaluation.RunLive
		}
		report, err = liveRun(ctx, evaluation.LiveConfig{
			CasesPath: casesPathValue(*casesPath), RunID: "", TopK: *topK, Repeat: *repeat,
			OptIn: true, ConfirmPolicy: *confirmPolicy, ConfirmTransfer: *confirmTransfer,
			TransferPolicy: *transferPolicy, Provider: string(generation.Provider), RequestedModel: generation.Model,
			MaxOutputTokens: generation.MaxOutputTokens, Generation: profile, Timeout: generation.Timeout,
			Budget:         evaluation.LiveBudgetConfig{MaxRequests: budget.MaxRequests, MaxInputTokens: budget.MaxInputTokens, MaxOutputTokens: budget.MaxOutputTokens, MaxCostUSD: budget.MaxCostUSD},
			PriceTable:     evaluation.LivePriceTable{Version: *priceTableVersion, Provider: string(generation.Provider), Model: generation.Model, InputTokenUSD: *inputTokenPriceUSD, OutputTokenUSD: *outputTokenPriceUSD},
			ProviderClient: generator,
		})
	} else {
		if run == nil {
			run = evaluation.RunSimulated
		}
		report, err = run(ctx, evaluation.Config{
			CasesPath: *casesPath,
			Mode:      evaluation.ModeSimulated,
			TopK:      *topK,
			Repeat:    *repeat,
		})
	}
	if err != nil {
		writeEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	reportData, err := evaluation.MarshalReport(report)
	if err != nil {
		writeEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if strings.TrimSpace(*output) != "" {
		if err := writeEvaluationReportAtomic(*output, reportData); err != nil {
			writeEvaluationDiagnostic(stderr, err)
			return ExitTechnical
		}
	}
	if selectedFormat == "json" {
		if _, err := stdout.Write(reportData); err != nil {
			return ExitTechnical
		}
	} else if err := writeEvaluationSummary(stdout, report, *output); err != nil {
		return ExitTechnical
	}
	return evaluationExitCode(report)
}

func casesPathValue(value string) string { return strings.TrimSpace(value) }

func evalFlagSet(flagSet *flag.FlagSet, name string) bool {
	if flagSet == nil {
		return false
	}
	set := false
	flagSet.Visit(func(value *flag.Flag) {
		set = set || value.Name == name
	})
	return set
}

// writeEvaluationReportAtomic publishes a complete report or leaves the
// existing destination untouched. The temporary file is created beside the
// destination so rename is atomic within one filesystem.
func writeEvaluationReportAtomic(filePath string, data []byte) error {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return errors.New("evaluation: output path is empty")
	}
	directory := filepath.Dir(filePath)
	temporary, err := os.CreateTemp(directory, ".manu-eval-*")
	if err != nil {
		return errors.New("evaluation: could not create report temporary file")
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return errors.New("evaluation: could not write report")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return errors.New("evaluation: could not sync report")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("evaluation: could not close report")
	}
	if err := os.Rename(temporaryPath, filePath); err != nil {
		return errors.New("evaluation: could not publish report")
	}
	return nil
}

func writeEvaluationSummary(w io.Writer, report evaluation.Report, output string) error {
	passed, partial, failed := evaluationOutcomeCounts(report)
	if _, err := fmt.Fprintln(w, "evaluation complete"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "mode: %s\n", report.Mode); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "cases: %d passed=%d partial=%d failed=%d\n", report.Summary.Cases, passed, partial, failed); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "evidence recall@%d: %.4f\n", firstK(report), report.Summary.EvidenceRecallAtKMean); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "evidence precision@%d: %.4f\n", firstK(report), report.Summary.EvidencePrecisionAtKMean); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "valid claims: %d\nvalid citations: %d\n", report.Summary.ValidClaims, report.Summary.ValidCitations); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "abstentions: %d/%d correct\n", report.Summary.CorrectAbstentions, report.Summary.ExpectedAbstentions); err != nil {
		return err
	}
	if output != "" {
		if _, err := fmt.Fprintf(w, "report: %s\n", output); err != nil {
			return err
		}
	}
	for _, limitation := range report.Limitations {
		if _, err := fmt.Fprintf(w, "limitation: %s\n", limitation); err != nil {
			return err
		}
	}
	return nil
}

func firstK(report evaluation.Report) int {
	for _, item := range report.Cases {
		return item.RetrievalMetrics.K
	}
	return 0
}

func evaluationOutcomeCounts(report evaluation.Report) (passed, partial, failed int) {
	for _, item := range report.Cases {
		switch item.Outcome {
		case "passed":
			passed++
		case "partial":
			partial++
		case "failed":
			failed++
		}
	}
	return passed, partial, failed
}

func evaluationExitCode(report evaluation.Report) int {
	_, partial, failed := evaluationOutcomeCounts(report)
	if partial > 0 || failed > 0 {
		return ExitPartial
	}
	return ExitSuccess
}

func writeEvaluationDiagnostic(w io.Writer, err error) {
	_, _ = fmt.Fprintln(w, "manu eval:", evaluationDiagnostic(err))
}

func evaluationDiagnostic(err error) string {
	switch {
	case err == nil:
		return "evaluation failed"
	case errors.Is(err, context.Canceled):
		return "operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	default:
		return "evaluation failed"
	}
}
