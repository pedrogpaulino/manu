// Package cli composes the commands exposed by the Manu executable.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/analyzer/generic"
	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/python"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/benchmark"
	"github.com/pedrogpaulino/manu/internal/buildinfo"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	// ExitSuccess means that an operation completed without partial coverage.
	ExitSuccess = 0
	// ExitTechnical means that an operation could not complete because of a
	// filesystem, serialization, or other technical failure.
	ExitTechnical = 1
	// ExitUsage means that flags or positional arguments were invalid.
	ExitUsage = 2
	// ExitPartial means that valid output was produced with gaps, unsupported
	// dimensions, or partial failures.
	ExitPartial = 3

	usage = "usage: manu <version|analyze|inspect|benchmark|migrate|serve|ready|eval|ingest|ingestion|ask|evidence>"
)

// Run executes a Manu command and returns a Unix-style exit code. Output is
// written to the supplied destinations so callers and tests can capture it.
func Run(args []string, stdout, stderr io.Writer) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return RunContext(ctx, nil, args, stdout, stderr)
}

// RunContext executes a command with caller-owned cancellation and an
// optional signal stream. The signal stream is folded into the context so the
// runner and bounded source reads observe the same cancellation.
func RunContext(ctx context.Context, signals <-chan os.Signal, args []string, stdout, stderr io.Writer) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 {
		writeUsage(stderr)
		return ExitUsage
	}

	switch args[0] {
	case "help", "--help", "-h":
		if len(args) > 1 {
			fmt.Fprintln(stderr, "manu: help does not accept arguments")
			return ExitUsage
		}
		writeHelp(stdout)
		return ExitSuccess
	case "version":
		return runVersion(args[1:], stdout, stderr)
	case "analyze":
		return runAnalyze(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "inspect":
		return runInspect(args[1:], stdout, stderr)
	case "benchmark":
		return runBenchmark(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "migrate":
		return runMigrate(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "serve":
		return runServe(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "ready":
		return runReady(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "eval":
		return runEval(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "ingest":
		return runIngest(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "ingestion", "ingestion-status":
		return runIngestionStatus(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "ask", "query":
		return runAsk(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	case "evidence":
		return runEvidence(analysis.NewRunContext(ctx, signals), args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "manu: unknown command %q\n", args[0])
		writeUsage(stderr)
		return ExitUsage
	}
}

// RunWithContext is an explicit compatibility alias for RunContext.
func RunWithContext(ctx context.Context, signals <-chan os.Signal, args []string, stdout, stderr io.Writer) int {
	return RunContext(ctx, signals, args, stdout, stderr)
}

func runVersion(args []string, stdout, stderr io.Writer) int {
	flagSet := newFlagSet("version", stderr)
	jsonOutput := flagSet.Bool("json", false, "emit version metadata as JSON")
	format := flagSet.String("format", "human", "output format: human or json")
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu version: unexpected positional argument")
		return ExitUsage
	}
	selectedFormat, err := outputFormat(*format, *jsonOutput)
	if err != nil {
		fmt.Fprintln(stderr, "manu version:", err)
		return ExitUsage
	}
	info := buildinfo.Current()
	if selectedFormat == "json" {
		if err := writeJSON(stdout, info); err != nil {
			fmt.Fprintln(stderr, "manu version:", err)
			return ExitTechnical
		}
		return ExitSuccess
	}
	if _, err := fmt.Fprintln(stdout, info); err != nil {
		return ExitTechnical
	}
	return ExitSuccess
}

func runAnalyze(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	flagSet := newFlagSet("analyze", stderr)
	root := flagSet.String("root", "", "authorized source root")
	output := flagSet.String("output", "", "result output directory")
	outputMode := flagSet.String("output-mode", "legacy", "result output mode: legacy or bundle")
	organizationID := flagSet.String("organization-id", "", "explicit organization boundary (required for bundle mode)")
	format := flagSet.String("format", "human", "output format: human or json")
	jsonOutput := flagSet.Bool("json", false, "emit the result summary as JSON")
	revision := flagSet.String("revision", "", "known source revision")
	sourceID := flagSet.String("source-id", "", "stable source identity")
	includeSensitive := flagSet.Bool("include-sensitive", false, "include paths excluded by default sensitive-file rules")
	var includes, excludes stringListFlag
	flagSet.Var(&includes, "include", "include relative path pattern (repeatable)")
	flagSet.Var(&excludes, "exclude", "exclude relative path pattern (repeatable)")
	maxFiles := flagSet.Int("max-files", 0, "maximum files to inspect")
	maxBytes := flagSet.Int64("max-bytes", 0, "maximum total source bytes")
	maxFileBytes := flagSet.Int64("max-file-bytes", 0, "maximum bytes per source file")
	maxDuration := flagSet.Duration("max-duration", 0, "maximum analysis duration")
	maxConcurrency := flagSet.Int("max-concurrency", 0, "maximum analyzer concurrency")
	maxProbeBytes := flagSet.Int64("max-probe-bytes", 0, "maximum classification probe bytes")
	maxExtractionBytes := flagSet.Int64("max-extraction-bytes", 0, "maximum text/member bytes")
	maxArchiveMembers := flagSet.Int("max-archive-members", 0, "maximum archive members")
	maxArchiveBytes := flagSet.Int64("max-archive-bytes", 0, "maximum expanded archive bytes")
	maxArchiveMemberBytes := flagSet.Int64("max-archive-member-bytes", 0, "maximum expanded bytes per archive member")
	maxArchiveCompressedBytes := flagSet.Int64("max-archive-compressed-bytes", 0, "maximum compressed archive bytes")
	maxExpansionRatio := flagSet.Float64("max-expansion-ratio", 0, "maximum archive expansion ratio")
	maxEvidenceUnitsPerArtifact := flagSet.Int("max-evidence-units-per-artifact", 0, "maximum retained evidence units per artifact")
	maxEvidenceBytesPerUnit := flagSet.Int64("max-evidence-bytes-per-unit", 0, "maximum retained evidence bytes per unit")
	maxEvidenceCharactersPerUnit := flagSet.Int64("max-evidence-characters-per-unit", 0, "maximum retained evidence characters per unit")
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *root == "" && flagSet.NArg() == 1 {
		*root = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 {
		fmt.Fprintln(stderr, "manu analyze: expected one root positional argument")
		return ExitUsage
	}
	if strings.TrimSpace(*root) == "" || strings.TrimSpace(*output) == "" {
		fmt.Fprintln(stderr, "manu analyze: --root and --output are required")
		return ExitUsage
	}
	mode, err := analyzeOutputMode(*outputMode)
	if err != nil {
		fmt.Fprintln(stderr, "manu analyze:", err)
		return ExitUsage
	}
	evidenceLimits := analysis.EvidenceLimits{
		MaxUnitsPerArtifact:  *maxEvidenceUnitsPerArtifact,
		MaxBytesPerUnit:      *maxEvidenceBytesPerUnit,
		MaxCharactersPerUnit: *maxEvidenceCharactersPerUnit,
	}
	evidenceConfig := analysis.EvidenceConfig{
		OrganizationID: strings.TrimSpace(*organizationID),
		Limits:         evidenceLimits,
		Policy:         evidence.DefaultPolicy(),
	}
	if mode == analyzeOutputBundle {
		if strings.TrimSpace(*organizationID) == "" {
			fmt.Fprintln(stderr, "manu analyze: --organization-id is required with --output-mode bundle")
			return ExitUsage
		}
		if err := evidenceConfig.Validate(); err != nil {
			fmt.Fprintln(stderr, "manu analyze:", err)
			return ExitUsage
		}
	}
	selectedFormat, err := outputFormat(*format, *jsonOutput)
	if err != nil {
		fmt.Fprintln(stderr, "manu analyze:", err)
		return ExitUsage
	}

	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(stderr, "manu analyze: resolving root: %v\n", err)
		return ExitTechnical
	}
	rootInfo, err := os.Stat(absoluteRoot)
	if err != nil {
		fmt.Fprintf(stderr, "manu analyze: inspecting root: %v\n", err)
		return ExitTechnical
	}
	if !rootInfo.IsDir() {
		fmt.Fprintln(stderr, "manu analyze: root must be a directory")
		return ExitUsage
	}

	now := time.Now().UTC()
	selectedSourceID := strings.TrimSpace(*sourceID)
	if selectedSourceID == "" {
		selectedSourceID = contract.SourceID(absoluteRoot)
	}
	sourceInfo := contract.Source{
		ID:       selectedSourceID,
		Name:     filepath.Base(absoluteRoot),
		Type:     "filesystem",
		Revision: strings.TrimSpace(*revision),
		Root:     absoluteRoot,
	}
	registry, err := analysis.NewRegistry(generic.New(), java.New(), python.New(), wso2.New())
	if err != nil {
		fmt.Fprintln(stderr, "manu analyze: creating analyzer registry:", err)
		return ExitTechnical
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		fmt.Fprintln(stderr, "manu analyze: creating analysis runner:", err)
		return ExitTechnical
	}
	analysisCtx, stop := contextWithSignals(runContext)
	defer stop()
	config := analysis.Config{
		Source:           sourceInfo,
		Root:             absoluteRoot,
		Output:           *output,
		OrganizationID:   strings.TrimSpace(*organizationID),
		EvidenceLimits:   evidenceLimits,
		Includes:         includes.Values(),
		Excludes:         excludes.Values(),
		IncludeSensitive: *includeSensitive,
		Limits: source.Limits{
			MaxFiles:                  *maxFiles,
			MaxBytes:                  *maxBytes,
			MaxFileBytes:              *maxFileBytes,
			MaxDuration:               *maxDuration,
			MaxConcurrency:            *maxConcurrency,
			MaxProbeBytes:             *maxProbeBytes,
			MaxExtractionBytes:        *maxExtractionBytes,
			MaxArchiveMembers:         *maxArchiveMembers,
			MaxArchiveBytes:           *maxArchiveBytes,
			MaxArchiveMemberBytes:     *maxArchiveMemberBytes,
			MaxArchiveCompressedBytes: *maxArchiveCompressedBytes,
			MaxExpansionRatio:         *maxExpansionRatio,
		},
		RunID:       fmt.Sprintf("run-%d", now.UnixNano()),
		ToolVersion: buildinfo.Current().Version,
	}
	var result contract.Result
	var evidenceResult analysis.AnalysisResult
	var runErr error
	if mode == analyzeOutputBundle {
		evidenceResult, runErr = runner.RunWithEvidence(analysisCtx, config, evidenceConfig)
		result = evidenceResult.Result
	} else {
		result, runErr = runner.Run(analysisCtx, config)
	}
	if err := result.Validate(); err != nil {
		if runErr != nil {
			fmt.Fprintln(stderr, "manu analyze:", runErr)
		}
		fmt.Fprintln(stderr, "manu analyze: invalid result:", err)
		return ExitTechnical
	}
	writeContext := context.WithoutCancel(analysisCtx)
	if mode == analyzeOutputBundle {
		if err := writeAnalysisBundle(writeContext, *output, evidenceResult, *organizationID); err != nil {
			fmt.Fprintln(stderr, "manu analyze:", err)
			return ExitTechnical
		}
	} else {
		if err := contract.WriteResult(writeContext, *output, result); err != nil {
			fmt.Fprintln(stderr, "manu analyze:", err)
			return ExitTechnical
		}
	}
	exitCode := resultExitCode(result)
	if runErr != nil && len(result.Artifacts) == 0 && len(result.Manifest.Failures) > 0 {
		exitCode = ExitTechnical
	}
	if mode == analyzeOutputBundle {
		// Bundle mode is a portable boundary: both human and machine-readable
		// summaries must not re-expose the Agent-local source root that was
		// removed from the persisted envelope above. Mutate the value used by
		// every output branch so future summary formats cannot accidentally
		// serialize the private root through the legacy result envelope.
		result.Manifest.Source.Root = ""
	}
	if selectedFormat == "json" {
		if err := writeJSON(stdout, cliResult{ContractVersion: contract.Version, Result: result}); err != nil {
			fmt.Fprintln(stderr, "manu analyze:", err)
			return ExitTechnical
		}
		return exitCode
	}
	if err := writeResultSummary(stdout, "analysis", *output, result); err != nil {
		return ExitTechnical
	}
	return exitCode
}

func runBenchmark(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	flagSet := newFlagSet("benchmark", stderr)
	root := flagSet.String("root", "", "authorized source root")
	output := flagSet.String("output", "", "benchmark output directory")
	format := flagSet.String("format", "human", "output format: human or json")
	jsonOutput := flagSet.Bool("json", false, "emit the benchmark report as JSON")
	revision := flagSet.String("revision", "", "known source revision")
	sourceID := flagSet.String("source-id", "", "stable source identity")
	updatePath := flagSet.String("update-path", "", "relative artifact path for the localized update")
	updateMarker := flagSet.String("update-marker", "", "marker appended in the temporary localized overlay")
	includeSensitive := flagSet.Bool("include-sensitive", false, "include paths excluded by default sensitive-file rules")
	var includes, excludes stringListFlag
	flagSet.Var(&includes, "include", "include relative path pattern (repeatable)")
	flagSet.Var(&excludes, "exclude", "exclude relative path pattern (repeatable)")
	maxFiles := flagSet.Int("max-files", 0, "maximum files to inspect")
	maxBytes := flagSet.Int64("max-bytes", 0, "maximum total source bytes")
	maxFileBytes := flagSet.Int64("max-file-bytes", 0, "maximum bytes per source file")
	maxDuration := flagSet.Duration("max-duration", 0, "maximum analysis duration")
	maxConcurrency := flagSet.Int("max-concurrency", 0, "maximum analyzer concurrency")
	maxProbeBytes := flagSet.Int64("max-probe-bytes", 0, "maximum classification probe bytes")
	maxExtractionBytes := flagSet.Int64("max-extraction-bytes", 0, "maximum text/member bytes")
	maxArchiveMembers := flagSet.Int("max-archive-members", 0, "maximum archive members")
	maxArchiveBytes := flagSet.Int64("max-archive-bytes", 0, "maximum expanded archive bytes")
	maxArchiveMemberBytes := flagSet.Int64("max-archive-member-bytes", 0, "maximum expanded bytes per archive member")
	maxArchiveCompressedBytes := flagSet.Int64("max-archive-compressed-bytes", 0, "maximum compressed archive bytes")
	maxExpansionRatio := flagSet.Float64("max-expansion-ratio", 0, "maximum archive expansion ratio")
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *root == "" && flagSet.NArg() == 1 {
		*root = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 {
		fmt.Fprintln(stderr, "manu benchmark: expected one root positional argument")
		return ExitUsage
	}
	if strings.TrimSpace(*root) == "" || strings.TrimSpace(*output) == "" {
		fmt.Fprintln(stderr, "manu benchmark: --root and --output are required")
		return ExitUsage
	}
	selectedFormat, err := outputFormat(*format, *jsonOutput)
	if err != nil {
		fmt.Fprintln(stderr, "manu benchmark:", err)
		return ExitUsage
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(stderr, "manu benchmark: resolving root: %v\n", err)
		return ExitTechnical
	}
	rootInfo, err := os.Stat(absoluteRoot)
	if err != nil {
		fmt.Fprintf(stderr, "manu benchmark: inspecting root: %v\n", err)
		return ExitTechnical
	}
	if !rootInfo.IsDir() {
		fmt.Fprintln(stderr, "manu benchmark: root must be a directory")
		return ExitUsage
	}
	selectedSourceID := strings.TrimSpace(*sourceID)
	if selectedSourceID == "" {
		selectedSourceID = contract.SourceID(absoluteRoot)
	}
	config := benchmark.Config{
		Source: contract.Source{
			ID:       selectedSourceID,
			Name:     filepath.Base(absoluteRoot),
			Type:     "filesystem",
			Revision: strings.TrimSpace(*revision),
			Root:     absoluteRoot,
		},
		Root:             absoluteRoot,
		Output:           *output,
		Includes:         includes.Values(),
		Excludes:         excludes.Values(),
		IncludeSensitive: *includeSensitive,
		Update: benchmark.UpdateConfig{
			Path:   strings.TrimSpace(*updatePath),
			Marker: strings.TrimSpace(*updateMarker),
		},
		Limits: source.Limits{
			MaxFiles:                  *maxFiles,
			MaxBytes:                  *maxBytes,
			MaxFileBytes:              *maxFileBytes,
			MaxDuration:               *maxDuration,
			MaxConcurrency:            *maxConcurrency,
			MaxProbeBytes:             *maxProbeBytes,
			MaxExtractionBytes:        *maxExtractionBytes,
			MaxArchiveMembers:         *maxArchiveMembers,
			MaxArchiveBytes:           *maxArchiveBytes,
			MaxArchiveMemberBytes:     *maxArchiveMemberBytes,
			MaxArchiveCompressedBytes: *maxArchiveCompressedBytes,
			MaxExpansionRatio:         *maxExpansionRatio,
		},
		ToolVersion: buildinfo.Current().Version,
	}
	analysisCtx, stop := contextWithSignals(runContext)
	defer stop()
	report, runErr := benchmark.Run(analysisCtx, config)
	if runErr != nil {
		fmt.Fprintln(stderr, "manu benchmark:", runErr)
		return ExitTechnical
	}
	if selectedFormat == "json" {
		if err := writeJSON(stdout, report); err != nil {
			fmt.Fprintln(stderr, "manu benchmark:", err)
			return ExitTechnical
		}
		return benchmarkExitCode(report)
	}
	if err := writeBenchmarkSummary(stdout, report); err != nil {
		return ExitTechnical
	}
	return benchmarkExitCode(report)
}

func runInspect(args []string, stdout, stderr io.Writer) int {
	flagSet := newFlagSet("inspect", stderr)
	input := flagSet.String("input", "", "result directory to inspect")
	format := flagSet.String("format", "human", "output format: human or json")
	jsonOutput := flagSet.Bool("json", false, "emit the complete result as JSON")
	if err := flagSet.Parse(args); err != nil {
		return ExitUsage
	}
	if *input == "" && flagSet.NArg() == 1 {
		*input = flagSet.Arg(0)
	}
	if flagSet.NArg() > 1 || strings.TrimSpace(*input) == "" {
		fmt.Fprintln(stderr, "manu inspect: a result directory is required")
		return ExitUsage
	}
	selectedFormat, err := outputFormat(*format, *jsonOutput)
	if err != nil {
		fmt.Fprintln(stderr, "manu inspect:", err)
		return ExitUsage
	}
	inputPath := *input
	if filepath.Base(inputPath) == contract.ManifestFileName {
		inputPath = filepath.Dir(inputPath)
	}
	result, err := contract.ReadResult(nil, inputPath)
	if err != nil {
		fmt.Fprintln(stderr, "manu inspect:", err)
		return ExitTechnical
	}
	if selectedFormat == "json" {
		if err := writeJSON(stdout, cliResult{ContractVersion: contract.Version, Result: result}); err != nil {
			fmt.Fprintln(stderr, "manu inspect:", err)
			return ExitTechnical
		}
		return resultExitCode(result)
	}
	if err := writeResultSummary(stdout, "inspection", inputPath, result); err != nil {
		return ExitTechnical
	}
	return resultExitCode(result)
}

type cliResult struct {
	ContractVersion string          `json:"contract_version"`
	Result          contract.Result `json:"result"`
}

type stringListFlag []string

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("value must not be empty")
	}
	*f = append(*f, value)
	return nil
}

func (f stringListFlag) Values() []string {
	return append([]string(nil), f...)
}

func contextWithSignals(runContext analysis.RunContext) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(runContext.Base())
	signals := runContext.Signal()
	if signals == nil {
		return ctx, cancel
	}
	go func() {
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	flagSet := flag.NewFlagSet(name, flag.ContinueOnError)
	flagSet.SetOutput(stderr)
	return flagSet
}

func outputFormat(format string, jsonOutput bool) (string, error) {
	if jsonOutput {
		return "json", nil
	}
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "human", "text", "":
		return "human", nil
	case "json":
		return "json", nil
	default:
		return "", fmt.Errorf("invalid output format %q", format)
	}
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeResultSummary(w io.Writer, operation, location string, result contract.Result) error {
	if _, err := fmt.Fprintf(w, "%s complete\n", operation); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "source: %s\n", result.Manifest.Source.Name); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "revision: %s\n", emptyAsUnknown(result.Manifest.Source.Revision)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "artifacts: %d\n", len(result.Artifacts)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "contributions: %d\n", len(result.Contributions)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "coverage: %d\n", len(result.Manifest.Coverage)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "gaps: %d\n", len(result.Manifest.Gaps)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "failures: %d\n", len(result.Manifest.Failures)); err != nil {
		return err
	}
	metrics := result.Manifest.Execution.Metrics
	if _, err := fmt.Fprintf(w, "metrics: discovered=%d reused=%d reprocessed=%d limited=%d failed=%d\n", metrics.Discovered, metrics.Reused, metrics.Reprocessed, metrics.Limited, metrics.Failed); err != nil {
		return err
	}
	for _, limitation := range metrics.Limitations {
		if _, err := fmt.Fprintf(w, "limitation: %s\n", limitation); err != nil {
			return err
		}
	}
	if location != "" {
		if _, err := fmt.Fprintf(w, "result: %s\n", location); err != nil {
			return err
		}
	}
	for _, coverage := range result.Manifest.Coverage {
		if _, err := fmt.Fprintf(w, "- %s: %s\n", coverage.Dimension, coverage.State); err != nil {
			return err
		}
	}
	for _, gap := range result.Manifest.Gaps {
		if _, err := fmt.Fprintf(w, "gap %s: %s\n", gap.Code, gap.Message); err != nil {
			return err
		}
	}
	for _, failure := range result.Manifest.Failures {
		if _, err := fmt.Fprintf(w, "failure %s: %s\n", failure.Code, failure.Message); err != nil {
			return err
		}
	}
	return nil
}

func writeBenchmarkSummary(w io.Writer, report benchmark.Report) error {
	if _, err := fmt.Fprintln(w, "benchmark complete"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "source: %s (%s)\n", report.Source.Name, report.Source.ID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "revision: %s\n", emptyAsUnknown(report.Source.Revision)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "repeat equivalent facts: %t\n", report.RepeatEquivalentFacts); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "source metadata unchanged: %t\n", report.Integrity.Unchanged); err != nil {
		return err
	}
	for _, scenario := range report.Scenarios {
		if _, err := fmt.Fprintf(
			w,
			"- %s: total=%s discovery=%s analysis=%s writing=%s bytes_read=%d persisted_volume_bytes=%d output_bytes=%d concurrency=%d reused=%d reprocessed=%d\n",
			scenario.Name,
			time.Duration(scenario.Metrics.Durations.TotalNanos),
			time.Duration(scenario.Metrics.Durations.DiscoveryNanos),
			time.Duration(scenario.Metrics.Durations.AnalysisNanos),
			time.Duration(scenario.Metrics.Durations.WritingNanos),
			scenario.Metrics.BytesRead,
			scenario.Metrics.PersistedVolumeBytes,
			scenario.Metrics.OutputBytes,
			scenario.Metrics.EffectiveConcurrency,
			scenario.Metrics.ArtifactsReused,
			scenario.Metrics.ArtifactsReprocessed,
		); err != nil {
			return err
		}
		for _, limitation := range scenario.Metrics.Limitations {
			if _, err := fmt.Fprintf(w, "  limitation: %s\n", limitation); err != nil {
				return err
			}
		}
		for _, unavailable := range scenario.Metrics.Unavailable {
			if _, err := fmt.Fprintf(w, "  unavailable: %s\n", unavailable); err != nil {
				return err
			}
		}
	}
	for _, limitation := range report.Limitations {
		if _, err := fmt.Fprintf(w, "limitation: %s\n", limitation); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "report: %s\n", filepath.Join(report.Configuration.Output, "benchmark.json")); err != nil {
		return err
	}
	return nil
}

func resultExitCode(result contract.Result) int {
	if result.IsPartial() {
		return ExitPartial
	}
	return ExitSuccess
}

func benchmarkExitCode(report benchmark.Report) int {
	if report.Partial {
		return ExitPartial
	}
	return ExitSuccess
}

func emptyAsUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func writeUsage(w io.Writer) {
	_, _ = fmt.Fprintln(w, usage)
}

func writeHelp(w io.Writer) {
	_, _ = fmt.Fprintln(w, usage)
	_, _ = fmt.Fprintln(w, "\ncommands:")
	_, _ = fmt.Fprintln(w, "  version  identify the binary and contract")
	_, _ = fmt.Fprintln(w, "  analyze  create a deterministic analysis snapshot")
	_, _ = fmt.Fprintln(w, "  inspect  summarize a stored result and its coverage")
	_, _ = fmt.Fprintln(w, "  benchmark run first, repeat, and localized-update measurements")
	_, _ = fmt.Fprintln(w, "  migrate  apply the embedded PostgreSQL schema migrations")
	_, _ = fmt.Fprintln(w, "  serve    start the local-only HTTP API")
	_, _ = fmt.Fprintln(w, "  ready    probe the local HTTP readiness contract")
	_, _ = fmt.Fprintln(w, "  eval     run the deterministic local evaluation")
	_, _ = fmt.Fprintln(w, "  ingest   send an Analysis Bundle to the local HTTP API")
	_, _ = fmt.Fprintln(w, "  ingestion query the status of an ingestion job")
	_, _ = fmt.Fprintln(w, "  ask      ask a question through the local HTTP API")
	_, _ = fmt.Fprintln(w, "  evidence inspect one persisted Evidence Unit")
}

// IsUsageError reports whether a CLI exit code represents invalid input.
func IsUsageError(code int) bool { return code == ExitUsage }

// IsPartial reports whether a CLI exit code represents valid partial output.
func IsPartial(code int) bool { return code == ExitPartial }

// IsTechnicalError reports whether a CLI exit code represents a technical
// failure.
func IsTechnicalError(code int) bool {
	return code == ExitTechnical
}

// IsIncompatibleVersion reports whether an error is caused by an older or
// newer result contract.
func IsIncompatibleVersion(err error) bool {
	return errors.Is(err, contract.ErrIncompatibleVersion)
}
