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

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/evaluation"
)

// VariantEvaluationRunner is the CLI seam for one complete v1alpha2
// evaluation. The runner receives case metadata and typed configuration only;
// it returns the content-free raw and summary artifacts.
type VariantEvaluationRunner func(
	context.Context,
	string,
	evaluation.CaseSet,
	config.Config,
) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error)

// variantEvaluationRun is the production seam. The runtime supplies its
// PostgreSQL-backed implementation; keeping the value as a function
// dependency makes CLI tests independent from PostgreSQL.
var variantEvaluationRun VariantEvaluationRunner = runVariantEvaluationRuntime

var errVariantEvaluationUnavailable = errors.New("variant evaluation runtime unavailable")

func unavailableVariantEvaluation(context.Context, string, evaluation.CaseSet, config.Config) (evaluation.VariantRawReport, evaluation.VariantSummaryReport, error) {
	return evaluation.VariantRawReport{}, evaluation.VariantSummaryReport{}, errVariantEvaluationUnavailable
}

var legacyEvalFlags = map[string]struct{}{
	"cases-path":             {},
	"top-k":                  {},
	"repeat":                 {},
	"output":                 {},
	"format":                 {},
	"json":                   {},
	"live":                   {},
	"confirm-policy":         {},
	"confirm-transfer":       {},
	"transfer-policy":        {},
	"provider":               {},
	"model":                  {},
	"max-requests":           {},
	"max-input-tokens":       {},
	"max-output-tokens":      {},
	"max-cost-usd":           {},
	"price-table-version":    {},
	"input-token-price-usd":  {},
	"output-token-price-usd": {},
}

// variantEvaluationRequested performs a small dispatch-only scan before the
// legacy flag set sees the arguments. Without it, legacy parsing would reject
// the variant-only flags before the new mode could handle them.
func variantEvaluationRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		name := strings.TrimLeft(strings.TrimSpace(arg), "-")
		if strings.HasPrefix(name, "variants=") {
			return true
		}
		if name == "variants" {
			return true
		}
	}
	return false
}

// runEvalVariantsWithDependencies runs the explicitly selected variant mode.
// It has no legacy flags, positional shorthand, or output-format switches so
// that a variant report cannot be mistaken for the legacy report contract.
func runEvalVariantsWithDependencies(ctx context.Context, args []string, stdout, stderr io.Writer, load EvalConfigLoader, run VariantEvaluationRunner) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if mixedLegacyEvalFlag(args) {
		fmt.Fprintln(stderr, "manu eval --variants: legacy evaluation flags cannot be combined")
		return ExitUsage
	}

	flagSet := newFlagSet("eval", io.Discard)
	variants := flagSet.Bool("variants", false, "execute the v1alpha2 variant evaluation")
	root := flagSet.String("root", "", "authorized repository root")
	casesPath := flagSet.String("cases", "", "versioned v1alpha2 evaluation cases JSON")
	outputRaw := flagSet.String("output-raw", "", "write the canonical raw report")
	outputSummary := flagSet.String("output-summary", "", "write the canonical summary report")
	flagSet.Usage = func() {
		_, _ = fmt.Fprintln(stdout, "usage: manu eval --variants --root path --cases path --output-raw file --output-summary file")
	}
	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		fmt.Fprintln(stderr, "manu eval --variants: invalid flags")
		return ExitUsage
	}
	if !*variants {
		fmt.Fprintln(stderr, "manu eval --variants: --variants must be enabled")
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu eval --variants: positional arguments are not supported")
		return ExitUsage
	}

	rootValue := strings.TrimSpace(*root)
	casesValue := strings.TrimSpace(*casesPath)
	rawValue := strings.TrimSpace(*outputRaw)
	summaryValue := strings.TrimSpace(*outputSummary)
	switch {
	case rootValue == "":
		fmt.Fprintln(stderr, "manu eval --variants: --root must not be empty")
		return ExitUsage
	case casesValue == "":
		fmt.Fprintln(stderr, "manu eval --variants: --cases must not be empty")
		return ExitUsage
	case rawValue == "":
		fmt.Fprintln(stderr, "manu eval --variants: --output-raw must not be empty")
		return ExitUsage
	case summaryValue == "":
		fmt.Fprintln(stderr, "manu eval --variants: --output-summary must not be empty")
		return ExitUsage
	case variantOutputPathsConflict(rawValue, summaryValue):
		fmt.Fprintln(stderr, "manu eval --variants: output paths must be different")
		return ExitUsage
	}
	absoluteRoot, err := filepath.Abs(rootValue)
	if err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	rootValue = absoluteRoot
	if err := ctx.Err(); err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}

	cases, err := evaluation.LoadCases(casesValue)
	if err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if load == nil {
		load = config.Load
	}
	configuration, err := load()
	if err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if run == nil {
		run = variantEvaluationRun
	}
	if run == nil {
		writeVariantEvaluationDiagnostic(stderr, errVariantEvaluationUnavailable)
		return ExitTechnical
	}
	raw, summary, err := run(ctx, rootValue, cases, configuration)
	if err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	rawData, err := evaluation.MarshalVariantRawReport(raw)
	if err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	summaryData, err := evaluation.MarshalVariantSummaryReport(summary)
	if err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if err := writeVariantReportsAtomic(rawValue, summaryValue, rawData, summaryData); err != nil {
		writeVariantEvaluationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if err := writeVariantEvaluationSummary(stdout, summary); err != nil {
		return ExitTechnical
	}
	return variantEvaluationExitCode(summary)
}

func mixedLegacyEvalFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		name := strings.TrimLeft(strings.TrimSpace(arg), "-")
		if separator := strings.IndexByte(name, '='); separator >= 0 {
			name = name[:separator]
		}
		if _, exists := legacyEvalFlags[name]; exists {
			return true
		}
	}
	return false
}

func variantOutputPathsConflict(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" {
		return false
	}
	leftPath, leftErr := filepath.Abs(filepath.Clean(left))
	rightPath, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return left == right
	}
	return leftPath == rightPath
}

// writeVariantReportsAtomic stages both complete documents before publishing
// either one. Filesystem rename is atomic per path, but there is no portable
// two-path transaction; if the second rename fails after the first succeeds,
// the caller receives a technical error and one new file may remain published.
func writeVariantReportsAtomic(rawPath, summaryPath string, rawData, summaryData []byte) error {
	rawTemporary, err := stageVariantReport(rawPath, rawData)
	if err != nil {
		return err
	}
	defer os.Remove(rawTemporary)
	summaryTemporary, err := stageVariantReport(summaryPath, summaryData)
	if err != nil {
		return err
	}
	defer os.Remove(summaryTemporary)

	if err := os.Rename(rawTemporary, rawPath); err != nil {
		return errors.New("variant evaluation: could not publish raw report")
	}
	if err := os.Rename(summaryTemporary, summaryPath); err != nil {
		return errors.New("variant evaluation: could not publish summary report")
	}
	return nil
}

func stageVariantReport(filePath string, data []byte) (string, error) {
	directory := filepath.Dir(filePath)
	temporary, err := os.CreateTemp(directory, ".manu-eval-variant-*")
	if err != nil {
		return "", errors.New("variant evaluation: could not create report temporary file")
	}
	temporaryPath := temporary.Name()
	cleanup := func() (string, error) {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		return "", errors.New("variant evaluation: could not write report")
	}
	if _, err := temporary.Write(data); err != nil {
		return cleanup()
	}
	if err := temporary.Sync(); err != nil {
		return cleanup()
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", errors.New("variant evaluation: could not close report")
	}
	return temporaryPath, nil
}

func writeVariantEvaluationSummary(w io.Writer, report evaluation.VariantSummaryReport) error {
	if _, err := fmt.Fprintln(w, "variant evaluation complete"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "cases: %d executions: %d\n", report.Samples.Cases, report.Samples.Executions); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "raw digest: %s\nsummary digest: %s\n", report.ArtifactDigest, report.SummaryDigest); err != nil {
		return err
	}
	return nil
}

func variantEvaluationExitCode(report evaluation.VariantSummaryReport) int {
	if report.Samples.Limited > 0 || report.Samples.Failed > 0 || report.Samples.Unavailable > 0 || report.Samples.Cancelled > 0 {
		return ExitPartial
	}
	return ExitSuccess
}

func writeVariantEvaluationDiagnostic(w io.Writer, err error) {
	message := "evaluation failed"
	switch {
	case errors.Is(err, context.Canceled):
		message = "operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		message = "operation timed out"
	}
	_, _ = fmt.Fprintln(w, "manu eval --variants:", message)
}
