package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/analyzer/generic"
	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/buildinfo"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

const reportFileName = "benchmark.json"

// ErrInvalidConfig identifies a benchmark that cannot be safely scoped.
var ErrInvalidConfig = errors.New("benchmark: invalid configuration")

// Run executes first analysis, unchanged repetition, and one localized update
// in that order. It never mutates config.Root. Result snapshots and state are
// written below config.Output, which must be outside the source root.
func Run(ctx context.Context, config Config) (Report, error) {
	if ctx == nil {
		return Report{}, fmt.Errorf("%w: nil context", ErrInvalidConfig)
	}
	normalized, err := normalizeConfig(config)
	if err != nil {
		return Report{}, err
	}
	if err := ensureFreshWorkspace(normalized.Output); err != nil {
		return Report{}, err
	}
	if err := os.MkdirAll(normalized.Output, 0o755); err != nil {
		return Report{}, fmt.Errorf("creating benchmark output: %w", err)
	}

	started := time.Now().UTC()
	report := Report{
		ContractVersion:  contract.Version,
		BenchmarkVersion: Version,
		RunID:            fmt.Sprintf("benchmark-%d", started.UnixNano()),
		StartedAt:        started,
		Source:           normalized.Source,
		Configuration:    configurationRecord(normalized),
		Environment:      currentEnvironment(),
		Integrity: IntegrityReport{
			Method: "sha256 de metadados relativos: caminho, tipo, modo, tamanho, mtime e alvo de symlink",
		},
		Scenarios: make([]ScenarioReport, 0, 3),
		Limitations: []string{
			"benchmark_local_experimental_not_sla",
			"bytes_read_covers_discovery_hash_stream_only",
			"persisted_volume_is_logical_file_size_not_syscall_io_bytes",
			"heap_go_is_end_sample_not_continuous_peak",
			"max_rss_is_process_cumulative_high_water_mark_per_scenario",
			"localized_update_root_is_ephemeral_staging_and_is_removed_after_run",
		},
	}
	report.Integrity.Before, err = metadataDigest(normalized.Root)
	if err != nil {
		report.Integrity.Error = "pre-source-metadata: " + err.Error()
		report.Limitations = append(report.Limitations, "source_metadata_precheck_unavailable")
	}

	registry, err := analysis.NewRegistry(generic.New(), java.New(), wso2.New())
	if err != nil {
		return report, fmt.Errorf("creating analyzer registry: %w", err)
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		return report, fmt.Errorf("creating analysis runner: %w", err)
	}
	stateDir := filepath.Join(normalized.Output, ".state")
	firstDir := filepath.Join(normalized.Output, string(ScenarioFirstAnalysis))
	repeatDir := filepath.Join(normalized.Output, string(ScenarioRepeatUnchanged))
	updateDir := filepath.Join(normalized.Output, string(ScenarioLocalizedUpdate))
	baseSource := normalized.Source

	firstScenario, firstResult, err := runScenario(
		ctx,
		runner,
		normalized,
		baseSource,
		stateDir,
		firstDir,
		ScenarioFirstAnalysis,
		report.RunID+"-first",
	)
	if err != nil {
		return report, err
	}
	report.Scenarios = append(report.Scenarios, firstScenario)

	repeatScenario, repeatResult, err := runScenario(
		ctx,
		runner,
		normalized,
		baseSource,
		stateDir,
		repeatDir,
		ScenarioRepeatUnchanged,
		report.RunID+"-repeat",
	)
	if err != nil {
		return report, err
	}
	repeatEquivalent := contract.EquivalentFacts(firstResult, repeatResult)
	repeatScenario.EquivalentFactsToFirst = &repeatEquivalent
	report.RepeatEquivalentFacts = repeatEquivalent
	if !repeatEquivalent {
		repeatScenario.Error = "EquivalentFacts retornou falso na repetição sem mudança"
		repeatScenario.Partial = true
		report.Limitations = append(report.Limitations, "repeat_facts_not_equivalent")
	}
	report.Scenarios = append(report.Scenarios, repeatScenario)

	updatePath, err := chooseUpdatePath(normalized.Update.Path, firstResult.Artifacts)
	if err != nil {
		return report, err
	}
	stagedRoot, overlay, cleanup, err := stageOverlay(ctx, normalized.Root, updatePath, normalized.Update.Marker, normalized.Limits.MaxFileBytes)
	if err != nil {
		return report, err
	}
	defer cleanup()
	report.Configuration.UpdatePath = updatePath
	report.Configuration.OverlayMethod = overlay.Method
	report.Limitations = append(report.Limitations, overlay.Limitations...)
	updateSource := baseSource
	updateSource.Root = stagedRoot
	updateScenario, _, err := runScenario(
		ctx,
		runner,
		normalized,
		updateSource,
		stateDir,
		updateDir,
		ScenarioLocalizedUpdate,
		report.RunID+"-update",
	)
	if err != nil {
		return report, err
	}
	report.Scenarios = append(report.Scenarios, updateScenario)

	report.Integrity.After, err = metadataDigest(normalized.Root)
	if err != nil {
		report.Integrity.Error = "post-source-metadata: " + err.Error()
		report.Limitations = append(report.Limitations, "source_metadata_postcheck_unavailable")
	} else if report.Integrity.Before != "" {
		report.Integrity.Unchanged = report.Integrity.Before == report.Integrity.After
		if !report.Integrity.Unchanged {
			report.Partial = true
			report.Limitations = append(report.Limitations, "source_metadata_changed")
		}
	}
	for _, scenario := range report.Scenarios {
		if scenario.Partial || scenario.Error != "" {
			report.Partial = true
		}
		report.Unavailable = append(report.Unavailable, scenario.Metrics.Unavailable...)
	}
	report.Limitations = uniqueStrings(report.Limitations)
	report.Unavailable = uniqueStrings(report.Unavailable)
	report.FinishedAt = time.Now().UTC()
	if err := writeReport(filepath.Join(normalized.Output, reportFileName), report); err != nil {
		return report, err
	}
	return report, nil
}

type normalizedConfig struct {
	Config
	Root   string
	Output string
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	root := strings.TrimSpace(config.Root)
	if root == "" {
		root = strings.TrimSpace(config.Source.Root)
	}
	if root == "" {
		return normalizedConfig{}, fmt.Errorf("%w: root is required", ErrInvalidConfig)
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: resolving root: %v", ErrInvalidConfig, err)
	}
	root = filepath.Clean(root)
	rootInfo, err := os.Stat(root)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: inspecting root: %v", ErrInvalidConfig, err)
	}
	if !rootInfo.IsDir() {
		return normalizedConfig{}, fmt.Errorf("%w: root is not a directory", ErrInvalidConfig)
	}
	output := strings.TrimSpace(config.Output)
	if output == "" {
		return normalizedConfig{}, fmt.Errorf("%w: output is required", ErrInvalidConfig)
	}
	output, err = filepath.Abs(output)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: resolving output: %v", ErrInvalidConfig, err)
	}
	output = filepath.Clean(output)
	output, err = resolveOutputPath(output)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: resolving output symlinks: %v", ErrInvalidConfig, err)
	}
	if sameOrWithin(root, output) {
		return normalizedConfig{}, fmt.Errorf("%w: output must be outside root", ErrInvalidConfig)
	}
	if err := config.Limits.Validate(); err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: limits: %v", ErrInvalidConfig, err)
	}
	limits := config.Limits
	defaults := source.DefaultLimits()
	if limits.MaxFiles == 0 {
		limits.MaxFiles = defaults.MaxFiles
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxConcurrency == 0 {
		limits.MaxConcurrency = defaults.MaxConcurrency
	}
	if limits.MaxProbeBytes == 0 {
		limits.MaxProbeBytes = defaults.MaxProbeBytes
	}
	if limits.MaxExtractionBytes == 0 {
		limits.MaxExtractionBytes = defaults.MaxExtractionBytes
	}
	if limits.MaxArchiveMembers == 0 {
		limits.MaxArchiveMembers = defaults.MaxArchiveMembers
	}
	if limits.MaxArchiveBytes == 0 {
		limits.MaxArchiveBytes = defaults.MaxArchiveBytes
	}
	if limits.MaxArchiveMemberBytes == 0 {
		limits.MaxArchiveMemberBytes = defaults.MaxArchiveMemberBytes
	}
	if limits.MaxArchiveCompressedBytes == 0 {
		limits.MaxArchiveCompressedBytes = defaults.MaxArchiveCompressedBytes
	}
	if limits.MaxExpansionRatio == 0 {
		limits.MaxExpansionRatio = defaults.MaxExpansionRatio
	}

	config.Source.Root = root
	if strings.TrimSpace(config.Source.Type) == "" {
		config.Source.Type = "filesystem"
	}
	if strings.TrimSpace(config.Source.Name) == "" {
		config.Source.Name = filepath.Base(root)
	}
	if strings.TrimSpace(config.Source.ID) == "" {
		config.Source.ID = contract.SourceID(root)
	}
	config.Root = root
	config.Output = output
	config.Limits = limits
	config.Includes = append([]string(nil), config.Includes...)
	config.Excludes = append([]string(nil), config.Excludes...)
	config.SensitivePatterns = append([]string(nil), config.SensitivePatterns...)
	if config.ToolVersion == "" {
		config.ToolVersion = buildinfo.Current().Version
	}
	return normalizedConfig{Config: config, Root: root, Output: output}, nil
}

func configurationRecord(config normalizedConfig) Configuration {
	record := Configuration{
		Root:              config.Root,
		Output:            config.Output,
		SourceID:          config.Source.ID,
		Revision:          config.Source.Revision,
		Includes:          append([]string(nil), config.Includes...),
		Excludes:          append([]string(nil), config.Excludes...),
		SensitivePatterns: append([]string(nil), config.SensitivePatterns...),
		IncludeSensitive:  config.IncludeSensitive,
		Limits:            config.Limits,
		UpdatePath:        config.Update.Path,
		OverlayMethod:     "temporary_regular_file_staging",
	}
	canonical := struct {
		SourceID          string
		Revision          string
		Includes          []string
		Excludes          []string
		SensitivePatterns []string
		IncludeSensitive  bool
		Limits            source.Limits
		Update            UpdateConfig
	}{
		SourceID:          config.Source.ID,
		Revision:          config.Source.Revision,
		Includes:          append([]string(nil), config.Includes...),
		Excludes:          append([]string(nil), config.Excludes...),
		SensitivePatterns: append([]string(nil), config.SensitivePatterns...),
		IncludeSensitive:  config.IncludeSensitive,
		Limits:            config.Limits,
		Update:            config.Update,
	}
	encoded, _ := json.Marshal(canonical)
	digest := sha256.Sum256(encoded)
	record.ID = "config-" + hex.EncodeToString(digest[:])
	return record
}

// resolveOutputPath evaluates the existing portion of an output path before
// the benchmark creates anything. This prevents a symlinked output or parent
// from redirecting the first state/result write into the authorized source.
func resolveOutputPath(output string) (string, error) {
	missing := make([]string, 0, 2)
	candidate := filepath.Clean(output)
	for {
		_, err := os.Lstat(candidate)
		if err == nil {
			resolved, evalErr := filepath.EvalSymlinks(candidate)
			if evalErr != nil {
				return "", evalErr
			}
			resolved = filepath.Clean(resolved)
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return "", fmt.Errorf("output has no existing parent")
		}
		missing = append(missing, filepath.Base(candidate))
		candidate = parent
	}
}

func ensureFreshWorkspace(output string) error {
	info, err := os.Lstat(output)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: checking benchmark output: %v", ErrInvalidConfig, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: benchmark output is not a directory", ErrInvalidConfig)
	}
	entries, err := os.ReadDir(output)
	if err != nil {
		return fmt.Errorf("%w: reading benchmark output: %v", ErrInvalidConfig, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: benchmark output must be new or empty", ErrInvalidConfig)
	}
	return nil
}

func runScenario(
	ctx context.Context,
	runner *analysis.Runner,
	config normalizedConfig,
	sourceInfo contract.Source,
	stateDir string,
	resultDir string,
	scenario Scenario,
	runID string,
) (ScenarioReport, contract.Result, error) {
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		return ScenarioReport{}, contract.Result{}, fmt.Errorf("creating %s result directory: %w", scenario, err)
	}
	started := time.Now()
	metrics := &analysis.RunMetrics{}
	result, runErr := runner.Run(ctx, analysis.Config{
		Source:            sourceInfo,
		Root:              sourceInfo.Root,
		Output:            stateDir,
		Includes:          config.Includes,
		Excludes:          config.Excludes,
		SensitivePatterns: config.SensitivePatterns,
		IncludeSensitive:  config.IncludeSensitive,
		Limits:            config.Limits,
		RunID:             runID,
		ToolVersion:       config.ToolVersion,
		Metrics:           metrics,
	})
	if err := result.Validate(); err != nil {
		if runErr != nil {
			return ScenarioReport{}, result, fmt.Errorf("%s analysis: %v; invalid result: %w", scenario, runErr, err)
		}
		return ScenarioReport{}, result, fmt.Errorf("%s invalid result: %w", scenario, err)
	}
	writingStarted := time.Now()
	writeErr := contract.WriteResult(context.WithoutCancel(ctx), resultDir, result)
	writingDuration := time.Since(writingStarted)
	if writeErr != nil {
		return ScenarioReport{}, result, fmt.Errorf("writing %s result: %w", scenario, writeErr)
	}
	factualDigest, err := contract.FactualDigest(result)
	if err != nil {
		return ScenarioReport{}, result, fmt.Errorf("digesting %s facts: %w", scenario, err)
	}
	outputBytes, err := directoryBytes(resultDir)
	if err != nil {
		return ScenarioReport{}, result, fmt.Errorf("measuring %s output: %w", scenario, err)
	}
	stateBytes, err := directoryBytes(stateDir)
	if err != nil {
		return ScenarioReport{}, result, fmt.Errorf("measuring %s state: %w", scenario, err)
	}
	heap := sampleHeap()
	unavailable := make([]string, 0, 1)
	if heap.MaxRSSMethod == "unavailable" {
		unavailable = append(unavailable, "max_rss_linux")
	}
	scenarioMetrics := Metrics{
		Durations: StageDurations{
			DiscoveryNanos:  metrics.DiscoveryDuration.Nanoseconds(),
			AnalysisNanos:   metrics.AnalysisDuration.Nanoseconds(),
			StateWriteNanos: metrics.StateWriteDuration.Nanoseconds(),
			WritingNanos:    writingDuration.Nanoseconds(),
			TotalNanos:      time.Since(started).Nanoseconds(),
		},
		BytesRead:            metrics.BytesRead,
		PersistedVolumeBytes: outputBytes + stateBytes,
		OutputBytes:          outputBytes,
		EffectiveConcurrency: metrics.EffectiveConcurrency,
		ArtifactsDiscovered:  result.Manifest.Execution.Metrics.Discovered,
		ArtifactsReused:      result.Manifest.Execution.Metrics.Reused,
		ArtifactsReprocessed: result.Manifest.Execution.Metrics.Reprocessed,
		Failures:             result.Manifest.Execution.Metrics.Failed,
		Limited:              result.Manifest.Execution.Metrics.Limited,
		Heap:                 heap,
		Unavailable:          unavailable,
		Limitations: []string{
			"bytes_read_is_discovery_hash_and_classification_stream",
		},
	}
	scenarioReport := ScenarioReport{
		Name:          scenario,
		ResultPath:    filepath.ToSlash(relativeResultPath(config.Output, resultDir)),
		RunID:         result.Manifest.Execution.RunID,
		Partial:       result.IsPartial() || runErr != nil,
		FactualDigest: factualDigest,
		Metrics:       scenarioMetrics,
	}
	if runErr != nil {
		scenarioReport.Error = runErr.Error()
	}
	return scenarioReport, result, nil
}

func relativeResultPath(output, resultDir string) string {
	relative, err := filepath.Rel(output, resultDir)
	if err != nil {
		return filepath.Base(resultDir)
	}
	return relative
}

func sameOrWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func directoryBytes(directory string) (int64, error) {
	var total int64
	err := filepath.WalkDir(directory, func(pathName string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() < 0 || total > (1<<63-1)-info.Size() {
			return fmt.Errorf("output size overflow")
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func writeReport(pathName string, report Report) error {
	directory := filepath.Dir(pathName)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating report directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".benchmark.json.tmp-")
	if err != nil {
		return fmt.Errorf("creating report temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporaryName)
		}
	}()
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("encoding benchmark report: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("syncing benchmark report: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("closing benchmark report: %w", err)
	}
	if err := os.Rename(temporaryName, pathName); err != nil {
		return fmt.Errorf("renaming benchmark report: %w", err)
	}
	removeTemporary = false
	return nil
}

func currentEnvironment() Environment {
	hostname, _ := os.Hostname()
	return Environment{
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		GoVersion:  runtime.Version(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		CPUCount:   runtime.NumCPU(),
		Hostname:   hostname,
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
