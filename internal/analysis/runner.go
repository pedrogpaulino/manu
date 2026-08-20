package analysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
	"github.com/pedrogpaulino/manu/internal/state"
)

const (
	failureCodeAnalyzer        = "analyzer_failed"
	failureCodeInvalidOutput   = "invalid_analyzer_output"
	failureCodeNoAnalyzer      = "analyzer_unavailable"
	failureCodeSource          = "source_failure"
	failureCodeSourceChanged   = "source_changed_during_analysis"
	failureCodeCancelled       = "cancelled"
	gapCodeAnalyzerUnavailable = "analyzer_unavailable"
)

// Config scopes one local analysis. Root, inclusions, exclusions and limits
// are passed to source.Discover; no analyzer receives a path outside this
// scope.
type Config struct {
	Source contract.Source
	Root   string
	// OrganizationID is required by RunWithEvidence. It is deliberately not
	// inferred from Root or Source so evidence scope remains explicit.
	OrganizationID string `json:"organization_id,omitempty"`
	// EvidenceLimits are used by the two-argument RunWithEvidence convenience
	// form. Run continues to ignore these fields.
	EvidenceLimits EvidenceLimits `json:"evidence_limits,omitempty"`
	// Output is an optional directory outside Root where reconstructible
	// state is stored. The runner never writes state into the source root.
	Output string
	// OutputDir is a compatibility spelling for callers that use directory
	// terminology. Output takes precedence when both are set.
	OutputDir         string
	Includes          []string
	Excludes          []string
	SensitivePatterns []string
	IncludeSensitive  bool
	Limits            source.Limits
	RunID             string
	ToolVersion       string
	// Metrics receives stage timings and bounded input counters when a caller
	// needs to report how this run was measured. It is optional so normal
	// analysis callers do not pay for an observer they do not use.
	Metrics *RunMetrics

	// evidenceCapture is populated only by RunWithEvidence. Keeping it
	// private prevents callers from smuggling source content through Config.
	evidenceCapture *evidenceCapture
}

// RunMetrics contains measurements collected at the analysis boundary. The
// runner records discovery and analyzer work separately; callers are
// responsible for timing any result serialization they perform afterwards.
// BytesRead is the source-discovery stream count, which includes hashing and
// classification reads but not additional analyzer previews. EffectiveConcurrency
// is the maximum number of artifact jobs observed concurrently in the analyzer
// pool for this run; it is not merely the configured worker ceiling.
type RunMetrics struct {
	DiscoveryDuration    time.Duration
	AnalysisDuration     time.Duration
	StateWriteDuration   time.Duration
	BytesRead            int64
	EffectiveConcurrency int
}

// RunConfig is an explicit alias for callers that prefer to distinguish
// execution configuration from an analyzer configuration.
type RunConfig = Config

// Runner executes discovery once and then applies all compatible analyzers to
// each discovered artifact. The registry is immutable during Run; callers
// should build a new runner when changing analyzer selection.
type Runner struct {
	registry *Registry
}

// AnalysisRunner is an explicit alias for Runner.
type AnalysisRunner = Runner

// NewRunner validates and returns a runner backed by registry.
func NewRunner(registry *Registry) (*Runner, error) {
	if registry == nil {
		return nil, fmt.Errorf("%w: nil registry", ErrInvalidRequest)
	}
	return &Runner{registry: registry}, nil
}

// NewAnalysisRunner is an explicit constructor alias.
func NewAnalysisRunner(registry *Registry) (*Runner, error) {
	return NewRunner(registry)
}

// Registry returns the runner's analyzer registry. The registry should be
// treated as immutable while a run is in progress.
func (r *Runner) Registry() *Registry {
	if r == nil {
		return nil
	}
	return r.registry
}

// Run performs one bounded analysis and returns partial results whenever
// discovery or an individual analyzer fails. A context cancellation or
// deadline is returned alongside the partial result so callers can distinguish
// cancellation from a successful complete run.
func (r *Runner) Run(ctx context.Context, config Config) (contract.Result, error) {
	started := time.Now().UTC()
	if ctx == nil {
		return contract.Result{}, fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	if r == nil || r.registry == nil {
		return contract.Result{}, fmt.Errorf("%w: nil registry", ErrInvalidRequest)
	}
	root := strings.TrimSpace(config.Root)
	if root == "" {
		root = strings.TrimSpace(config.Source.Root)
	}
	if root == "" {
		return contract.Result{}, fmt.Errorf("%w: source root is required", ErrInvalidRequest)
	}
	normalizedRoot, err := source.NormalizeRoot(root)
	if err != nil {
		return contract.Result{}, err
	}
	root = normalizedRoot
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return contract.Result{}, fmt.Errorf("opening source root: %w", err)
	}
	defer rootHandle.Close()
	limits, err := normalizeLimits(config.Limits)
	if err != nil {
		return contract.Result{}, err
	}
	if config.Source.Type == "" {
		config.Source.Type = "filesystem"
	}
	config.Source.Root = root
	if config.Source.Name == "" {
		config.Source.Name = filepath.Base(filepath.Clean(root))
	}
	if config.Source.ID == "" {
		config.Source.ID = contract.SourceIdentity(config.Source)
	}
	outputDirectory := strings.TrimSpace(config.Output)
	if outputDirectory == "" {
		outputDirectory = strings.TrimSpace(config.OutputDir)
	}
	if outputDirectory != "" {
		if err := validateOutputDirectory(root, outputDirectory); err != nil {
			return contract.Result{}, err
		}
	}
	var outputStore *state.Store
	var previousState state.Snapshot
	stateLimitations := make([]string, 0, 4)
	if config.evidenceCapture != nil && outputDirectory != "" {
		// Evidence drafts are intentionally reprocessed instead of restored from
		// legacy state entries, which do not contain drafts. Keep that choice
		// observable rather than silently returning an incomplete evidence set.
		stateLimitations = append(stateLimitations, "evidence_reprocessed_without_cache")
	}
	stateUsable := true
	if outputDirectory != "" {
		stateLimitations = append(stateLimitations,
			"cache_reconstructible_only",
			"only_known_direct_dependencies_invalidated",
		)
		outputStore, err = state.Open(outputDirectory)
		if err != nil {
			return contract.Result{}, fmt.Errorf("opening analysis state: %w", err)
		}
		previousState = outputStore.Snapshot()
		if loadErr := outputStore.LoadError(); loadErr != nil {
			stateLimitations = append(stateLimitations, "state_unavailable: "+stateReason(loadErr))
			stateUsable = false
		}
		if previousState.SourceID != "" && previousState.SourceID != config.Source.ID {
			stateLimitations = append(stateLimitations, "state_source_mismatch")
			previousState = state.Empty(config.Source.ID)
			stateUsable = false
		}
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if limits.MaxDuration > 0 {
		runCtx, cancel = context.WithTimeout(ctx, limits.MaxDuration)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	discoveryConfig := source.Config{
		Root:              root,
		Includes:          append([]string{}, config.Includes...),
		Excludes:          append([]string{}, config.Excludes...),
		SensitivePatterns: append([]string{}, config.SensitivePatterns...),
		IncludeSensitive:  config.IncludeSensitive,
		Limits:            limits,
	}
	discoveryStarted := time.Now()
	discovered, discoveryErr := source.Discover(runCtx, discoveryConfig)
	if config.Metrics != nil {
		config.Metrics.DiscoveryDuration = time.Since(discoveryStarted)
		config.Metrics.BytesRead = discovered.BytesRead
	}

	artifacts := make([]contract.Artifact, 0, len(discovered.Artifacts))
	inputs := make([]ArtifactInput, 0, len(discovered.Artifacts))
	for _, discoveredArtifact := range discovered.Artifacts {
		artifact := contract.Artifact{
			SourceID: config.Source.ID,
			Path:     discoveredArtifact.RelativePath,
			Type:     artifactTypeFromSource(discoveredArtifact),
			Hash:     discoveredArtifact.SHA256,
			Size:     discoveredArtifact.Size,
			Kind:     string(discoveredArtifact.Classification),
		}
		artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
		artifacts = append(artifacts, artifact)
		inputs = append(inputs, ArtifactInput{
			SourceID:       config.Source.ID,
			SourceType:     config.Source.Type,
			Root:           discovered.Root,
			Artifact:       artifact,
			SourceArtifact: discoveredArtifact,
			Limits:         limits,
			RootHandle:     rootHandle,
			Evidence: EvidenceInput{
				Enabled: config.evidenceCapture != nil,
				Limits:  evidenceLimits(config),
			},
		})
	}
	sort.SliceStable(inputs, func(i, j int) bool {
		return inputs[i].Artifact.Path < inputs[j].Artifact.Path
	})
	sort.SliceStable(artifacts, func(i, j int) bool {
		return artifacts[i].Path < artifacts[j].Path
	})
	previousIndex := buildStateIndex(previousState)
	invalidated := invalidatedPathsWithIndex(previousIndex, artifacts)
	cacheEnabled := outputStore != nil && stateUsable && previousState.SourceID == config.Source.ID

	contributions := make([]contract.Contribution, 0)
	coverage := make([]contract.Coverage, 0)
	gaps := make([]contract.Gap, 0)
	failures := make([]contract.Failure, 0)
	for _, failure := range discovered.Failures {
		failures = append(failures, sourceFailure(config.Source.ID, failure))
	}
	if discoveryErr != nil && !errors.Is(discoveryErr, context.Canceled) &&
		!errors.Is(discoveryErr, context.DeadlineExceeded) {
		failures = append(failures, contract.Failure{
			Code:      failureCodeSource,
			Operation: "discover",
			Message:   "source discovery failed",
			Partial:   len(artifacts) > 0,
		})
	}

	analysisStarted := time.Now()
	collected, runStats, runErr := r.runAnalyzers(runCtx, inputs, limits.MaxConcurrency, runOptions{
		stateIndex:     previousIndex,
		cache:          cacheEnabled,
		invalidated:    invalidated,
		forceReprocess: config.evidenceCapture != nil,
	})
	if config.Metrics != nil {
		config.Metrics.AnalysisDuration = time.Since(analysisStarted)
		config.Metrics.EffectiveConcurrency = runStats.maxConcurrent
	}
	contributions = append(contributions, collected.contributions...)
	coverage = append(coverage, collected.coverage...)
	gaps = append(gaps, collected.gaps...)
	if config.evidenceCapture != nil {
		config.evidenceCapture.drafts = append(config.evidenceCapture.drafts[:0], collected.evidence...)
	}
	failures = append(failures, collected.failures...)
	if runErr != nil && !errors.Is(runErr, context.Canceled) &&
		!errors.Is(runErr, context.DeadlineExceeded) {
		failures = append(failures, contract.Failure{
			Code:      failureCodeAnalyzer,
			Operation: "analyze",
			Message:   "analyzer execution failed",
			Partial:   len(artifacts) > 0,
		})
	}
	failures = uniqueFailures(failures)
	limitations := append([]string{}, stateLimitations...)
	if discovered.Limited {
		limitations = append(limitations, "source_limits_reached")
	}
	if discovered.Cancelled || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		limitations = append(limitations, "analysis_cancelled")
	}
	limited := 0
	if discovered.Limited || discovered.Cancelled || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		limited = 1
	}
	executionMetrics := contract.ExecutionMetrics{
		Discovered:  len(artifacts),
		Reused:      runStats.reusedArtifacts,
		Reprocessed: runStats.reprocessedArtifacts,
		Limited:     limited,
		Failed:      len(failures),
		Limitations: uniqueStrings(limitations),
	}

	if len(artifacts) > 0 && len(coverage) == 0 {
		coverage = append(coverage, contract.Coverage{
			Dimension: string(contract.DimensionLandscapeInventoryStructure),
			Scope:     "source",
			State:     contract.CoverageNotSupported,
			Message:   "no compatible analyzer was registered",
		})
		gaps = append(gaps, contract.Gap{
			Code:      gapCodeAnalyzerUnavailable,
			Dimension: string(contract.DimensionLandscapeInventoryStructure),
			Scope:     "source",
			Message:   "no compatible analyzer was registered",
		})
	}

	finished := time.Now().UTC()
	execution := contract.ExecutionMetadata{
		RunID:       config.RunID,
		StartedAt:   started,
		FinishedAt:  finished,
		ToolVersion: config.ToolVersion,
		GoVersion:   runtime.Version(),
		Cancelled:   errors.Is(runCtx.Err(), context.Canceled) || errors.Is(runCtx.Err(), context.DeadlineExceeded),
		Metrics:     executionMetrics,
	}
	if execution.RunID == "" {
		execution.RunID = fmt.Sprintf("run-%d", started.UnixNano())
	}
	snapshotHash := snapshotDigest(artifacts)
	manifest := contract.Manifest{
		ContractVersion: contract.Version,
		Source:          config.Source,
		Snapshot: contract.Snapshot{
			SourceID: config.Source.ID,
			Revision: config.Source.Revision,
			Hash:     snapshotHash,
		},
		Execution: execution,
		Coverage:  coverage,
		Gaps:      gaps,
		Failures:  failures,
	}
	result := contract.Result{
		Manifest:      manifest,
		Artifacts:     artifacts,
		Contributions: contributions,
	}
	if err := result.Normalize(); err != nil {
		return result, fmt.Errorf("%w: normalize result: %v", ErrInvalidRequest, err)
	}
	if outputStore != nil {
		stateWriteStarted := time.Now()
		if err := outputStore.Replace(context.WithoutCancel(ctx), buildStateSnapshot(config.Source.ID, artifacts, runStats.entries, contributions)); err != nil {
			result.Manifest.Execution.Metrics.Limitations = uniqueStrings(append(result.Manifest.Execution.Metrics.Limitations, "state_write_failed"))
			result.Manifest.Execution.Metrics.Failed++
			result.Manifest.Failures = append(result.Manifest.Failures, contract.Failure{
				Code:      "state_write_failed",
				Operation: "write_state",
				Message:   "incremental state could not be persisted",
				Partial:   true,
			})
			if normalizeErr := result.Normalize(); normalizeErr != nil {
				return result, fmt.Errorf("%w: normalize state failure: %v", ErrInvalidRequest, normalizeErr)
			}
		}
		if config.Metrics != nil {
			config.Metrics.StateWriteDuration = time.Since(stateWriteStarted)
		}
	}
	if discoveryErr != nil {
		return result, discoveryErr
	}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

// Analyze is the method spelling of Run for callers using domain vocabulary.
func (r *Runner) Analyze(ctx context.Context, config Config) (contract.Result, error) {
	return r.Run(ctx, config)
}

// Analyze is a convenience wrapper around NewRunner and Runner.Run.
func Analyze(ctx context.Context, registry *Registry, config Config) (contract.Result, error) {
	runner, err := NewRunner(registry)
	if err != nil {
		return contract.Result{}, err
	}
	return runner.Run(ctx, config)
}

type collectedOutput struct {
	contributions []contract.Contribution
	coverage      []contract.Coverage
	gaps          []contract.Gap
	failures      []contract.Failure
	evidence      []EvidenceDraft
}

type runOptions struct {
	stateIndex     stateIndex
	cache          bool
	invalidated    map[string]bool
	forceReprocess bool
}

// stateIndex is an immutable, per-run view of a validated state snapshot.
// The maps retain the complete state compatibility key and artifact path so
// cache lookups do not repeatedly scan or serialize the snapshot. The runner
// builds this once before dispatching artifact jobs and then only reads it
// from worker goroutines.
type stateIndex struct {
	artifactsByPath map[string]contract.Artifact
	entriesByKey    map[comparableStateKey]state.Entry
	dependencies    []state.Dependency
}

type comparableStateKey struct {
	sourceID        string
	artifactPath    string
	artifactHash    string
	contractVersion string
	analyzerID      string
	analyzerVersion string
	analyzerMethod  string
}

type artifactRun struct {
	collectedOutput
	reused      bool
	reprocessed bool
	entries     []state.Entry
}

type runStats struct {
	reusedArtifacts      int
	reprocessedArtifacts int
	maxConcurrent        int
	entries              []state.Entry
}

func (r *Runner) runAnalyzers(ctx context.Context, inputs []ArtifactInput, concurrency int, options runOptions) (collectedOutput, runStats, error) {
	if concurrency < 1 {
		concurrency = 1
	}
	jobs := make(chan ArtifactInput)
	results := make(chan artifactRun, len(inputs))
	var workers sync.WaitGroup
	var active atomic.Int32
	var maxActive atomic.Int32
	workers.Add(concurrency)
	for range concurrency {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case input, ok := <-jobs:
					if !ok {
						return
					}
					current := active.Add(1)
					observeMaximum(&maxActive, current)
					output := r.runArtifact(ctx, input, options)
					active.Add(-1)
					select {
					case results <- output:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, input := range inputs {
			select {
			case jobs <- input:
			case <-ctx.Done():
				return
			}
		}
	}()
	workers.Wait()
	close(results)

	combined := collectedOutput{
		contributions: make([]contract.Contribution, 0),
		coverage:      make([]contract.Coverage, 0),
		gaps:          make([]contract.Gap, 0),
		failures:      make([]contract.Failure, 0),
		evidence:      make([]EvidenceDraft, 0),
	}
	stats := runStats{entries: make([]state.Entry, 0), maxConcurrent: int(maxActive.Load())}
	for output := range results {
		combined.contributions = append(combined.contributions, output.contributions...)
		combined.coverage = append(combined.coverage, output.coverage...)
		combined.gaps = append(combined.gaps, output.gaps...)
		combined.failures = append(combined.failures, output.failures...)
		combined.evidence = append(combined.evidence, output.evidence...)
		if output.reused {
			stats.reusedArtifacts++
		}
		if output.reprocessed {
			stats.reprocessedArtifacts++
		}
		stats.entries = append(stats.entries, output.entries...)
	}
	return combined, stats, ctx.Err()
}

// observeMaximum publishes a monotonically increasing maximum without a data
// race. The caller owns the active counter and supplies one observed sample.
func observeMaximum(maximum *atomic.Int32, sample int32) {
	for {
		previous := maximum.Load()
		if sample <= previous || maximum.CompareAndSwap(previous, sample) {
			return
		}
	}
}

func (r *Runner) runArtifact(ctx context.Context, input ArtifactInput, options runOptions) artifactRun {
	output := artifactRun{collectedOutput: collectedOutput{
		contributions: make([]contract.Contribution, 0),
		coverage:      make([]contract.Coverage, 0),
		gaps:          make([]contract.Gap, 0),
		failures:      make([]contract.Failure, 0),
		evidence:      make([]EvidenceDraft, 0),
	}, entries: make([]state.Entry, 0)}
	selected := r.registry.Select(input)
	if len(selected) == 0 {
		output.coverage = append(output.coverage, contract.Coverage{
			Dimension: string(contract.DimensionLandscapeInventoryStructure),
			Scope:     input.Artifact.Path,
			State:     contract.CoverageNotSupported,
			Message:   "no compatible analyzer was registered",
		})
		output.gaps = append(output.gaps, contract.Gap{
			Code:      gapCodeAnalyzerUnavailable,
			Dimension: string(contract.DimensionLandscapeInventoryStructure),
			Scope:     input.Artifact.Path,
			Message:   "no compatible analyzer was registered",
		})
		return output
	}
	allReused := true
	anyReprocessed := false
	for _, analyzer := range selected {
		if err := ctx.Err(); err != nil {
			output.failures = append(output.failures, analyzerFailure(input, analyzer.Descriptor(), err, failureCodeCancelled))
			break
		}
		descriptor := analyzer.Descriptor()
		key := state.NewKey(input.SourceID, input.Artifact.Path, input.Artifact.Hash, descriptor.ContractVersion, descriptor.ID, descriptor.Version, descriptor.Method)
		if options.cache && !options.forceReprocess && !options.invalidated[input.Artifact.Path] {
			if entry, ok := lookupStateEntry(options.stateIndex, key, input); ok {
				output.contributions = append(output.contributions, entry.Contributions...)
				output.coverage = append(output.coverage, entry.Coverage...)
				output.gaps = append(output.gaps, entry.Gaps...)
				output.entries = append(output.entries, entry)
				continue
			}
		}
		allReused = false
		anyReprocessed = true
		analyzerOutput, err := invokeAnalyzer(ctx, analyzer, input)
		normalized := normalizeOutput(input, descriptor, analyzerOutput)
		output.contributions = append(output.contributions, normalized.contributions...)
		output.coverage = append(output.coverage, normalized.coverage...)
		output.gaps = append(output.gaps, normalized.gaps...)
		output.failures = append(output.failures, normalized.failures...)
		output.evidence = append(output.evidence, normalized.evidence...)
		if err != nil {
			output.failures = append(output.failures, analyzerFailure(input, descriptor, err, failureCodeAnalyzer))
			continue
		}
		if len(normalized.failures) == 0 {
			entry := state.Entry{
				Key:           key,
				ArtifactID:    input.Artifact.ID,
				Contributions: append([]contract.Contribution(nil), normalized.contributions...),
				Coverage:      append([]contract.Coverage(nil), normalized.coverage...),
				Gaps:          append([]contract.Gap(nil), normalized.gaps...),
			}
			if entryErr := entry.Validate(); entryErr == nil {
				output.entries = append(output.entries, entry)
			}
		}
	}
	if err := ctx.Err(); err == nil {
		changed, revalidationErr := sourceChanged(ctx, input)
		if revalidationErr != nil || changed {
			output.contributions = output.contributions[:0]
			output.coverage = output.coverage[:0]
			output.gaps = output.gaps[:0]
			output.evidence = output.evidence[:0]
			message := "source changed during analysis; analyzer observations were discarded"
			if revalidationErr != nil {
				message = "source could not be revalidated after analysis; analyzer observations were discarded"
			}
			output.failures = append(output.failures, contract.Failure{
				Code:       failureCodeSourceChanged,
				Operation:  "revalidate",
				Message:    message,
				ArtifactID: input.Artifact.ID,
				Partial:    true,
				Locator:    locatorForInput(input),
			})
			output.gaps = append(output.gaps, contract.Gap{
				Code:      failureCodeSourceChanged,
				Dimension: string(contract.DimensionEvidenceAndGaps),
				Scope:     input.Artifact.Path,
				Message:   message,
				Locator:   locatorForInput(input),
			})
			output.entries = output.entries[:0]
			allReused = false
		}
	}
	if allReused {
		output.reused = true
	} else if anyReprocessed {
		output.reprocessed = true
	}
	return output
}

func invokeAnalyzer(ctx context.Context, analyzer Analyzer, input ArtifactInput) (output Output, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("analyzer panic: %v", recovered)
			output = Output{}
		}
	}()
	return analyzer.Analyze(ctx, input)
}

func normalizeOutput(input ArtifactInput, descriptor Descriptor, output Output) collectedOutput {
	result := collectedOutput{
		contributions: make([]contract.Contribution, 0, len(output.Contributions)),
		coverage:      make([]contract.Coverage, 0, len(output.Coverage)),
		gaps:          make([]contract.Gap, 0, len(output.Gaps)),
		failures:      make([]contract.Failure, 0),
		evidence:      make([]EvidenceDraft, 0, len(output.Evidence)),
	}
	contributions := append([]contract.Contribution{}, output.Contributions...)
	sort.SliceStable(contributions, func(i, j int) bool {
		return contributionKey(contributions[i]) < contributionKey(contributions[j])
	})
	seenContributions := make(map[string]int, len(contributions))
	normalizedByReference := make(map[string]string, len(contributions)*2)
	for index, contribution := range contributions {
		if contribution.ArtifactID != "" && contribution.ArtifactID != input.Artifact.ID {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d references another artifact", index), failureCodeInvalidOutput))
			continue
		}
		if contribution.AnalyzerID != "" && contribution.AnalyzerID != descriptor.ID {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d references another analyzer", index), failureCodeInvalidOutput))
			continue
		}
		if contribution.AnalyzerVersion != "" && contribution.AnalyzerVersion != descriptor.Version {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d references another analyzer version", index), failureCodeInvalidOutput))
			continue
		}
		if contribution.Locator.SourceID != "" && contribution.Locator.SourceID != input.SourceID {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d locator references another source", index), failureCodeInvalidOutput))
			continue
		}
		if contribution.Locator.ArtifactID != "" && contribution.Locator.ArtifactID != input.Artifact.ID {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d locator references another artifact", index), failureCodeInvalidOutput))
			continue
		}
		if contribution.Locator.Path != "" && contribution.Locator.Path != input.Artifact.Path {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d locator references another path", index), failureCodeInvalidOutput))
			continue
		}
		contribution.ArtifactID = input.Artifact.ID
		contribution.AnalyzerID = descriptor.ID
		contribution.AnalyzerVersion = descriptor.Version
		if contribution.Method == "" {
			contribution.Method = descriptor.Method
		}
		if input.Evidence.Enabled {
			contribution.Method = evidenceSafeMethod(contribution.Method)
		}
		if contribution.Locator.SourceID == "" {
			contribution.Locator.SourceID = input.SourceID
		}
		if contribution.Locator.ArtifactID == "" {
			contribution.Locator.ArtifactID = input.Artifact.ID
		}
		if contribution.Locator.Path == "" {
			contribution.Locator.Path = input.Artifact.Path
		}
		if contribution.Type == "" {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d has no type", index), failureCodeInvalidOutput))
			continue
		}
		if contribution.Method == "" {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("contribution %d has no method", index), failureCodeInvalidOutput))
			continue
		}
		baseMethod := contribution.Method
		originalID := contribution.ID
		identity := contract.ContributionID(
			contribution.ArtifactID,
			contribution.AnalyzerID,
			contribution.AnalyzerVersion,
			contribution.Method,
		)
		ordinal := 1
		for hasContributionID(seenContributions, identity) {
			ordinal++
			contribution.Method = fmt.Sprintf("%s#%d", baseMethod, ordinal)
			identity = contract.ContributionID(
				contribution.ArtifactID,
				contribution.AnalyzerID,
				contribution.AnalyzerVersion,
				contribution.Method,
			)
		}
		contribution.ID = identity
		seenContributions[identity] = index
		if err := contribution.Validate(); err != nil {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, err, failureCodeInvalidOutput))
			continue
		}
		result.contributions = append(result.contributions, contribution)
		normalizedByReference[contribution.ID] = contribution.ID
		if originalID != "" {
			// Analyzer drafts are allowed to refer to the contribution identity
			// created before runner normalization. Preserve that reference when
			// duplicate methods receive a deterministic suffix.
			normalizedByReference[originalID] = contribution.ID
		}
	}

	for index, draft := range output.Evidence {
		if !input.Evidence.Enabled {
			continue
		}
		if draft.ContributionID == "" {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("evidence draft %d has no contribution reference", index), failureCodeInvalidOutput))
			continue
		}
		if normalizedID, ok := normalizedByReference[draft.ContributionID]; ok {
			draft.ContributionID = normalizedID
		} else {
			found := false
			for _, candidate := range result.contributions {
				if candidate.ID == draft.ContributionID {
					found = true
					break
				}
			}
			if !found {
				result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("evidence draft %d references an unknown contribution", index), failureCodeInvalidOutput))
				continue
			}
		}
		if !locatorMatchesInput(draft.Locator, input) {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("evidence draft %d locator is outside the artifact", index), failureCodeInvalidOutput))
			continue
		}
		draft.Locator = *completeLocator(&draft.Locator, input)
		if err := draft.Locator.Validate(); err != nil {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("evidence draft %d locator is invalid", index), failureCodeInvalidOutput))
			continue
		}
		result.evidence = append(result.evidence, draft)
	}

	for _, coverage := range output.Coverage {
		if coverage.AnalyzerID != "" && coverage.AnalyzerID != descriptor.ID {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("coverage references another analyzer"), failureCodeInvalidOutput))
			continue
		}
		if coverage.Locator != nil && !locatorMatchesInput(*coverage.Locator, input) {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("coverage locator is outside the artifact"), failureCodeInvalidOutput))
			continue
		}
		coverage.AnalyzerID = descriptor.ID
		coverage.Locator = completeLocator(coverage.Locator, input)
		if coverage.Scope == "" {
			coverage.Scope = input.Artifact.Path
		} else if coverage.Scope != input.Artifact.Path && !strings.HasPrefix(coverage.Scope, input.Artifact.Path+"::") {
			coverage.Scope = input.Artifact.Path + "::" + coverage.Scope
		}
		coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
		coverageOrdinal := 1
		for hasCoverageID(result.coverage, coverage.ID) {
			coverageOrdinal++
			coverage.Scope = fmt.Sprintf("%s#%d", coverage.Scope, coverageOrdinal)
			coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
		}
		if err := coverage.Validate(); err != nil {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, err, failureCodeInvalidOutput))
			continue
		}
		result.coverage = append(result.coverage, coverage)
	}
	for _, gap := range output.Gaps {
		if gap.AnalyzerID != "" && gap.AnalyzerID != descriptor.ID {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("gap references another analyzer"), failureCodeInvalidOutput))
			continue
		}
		if gap.Locator != nil && !locatorMatchesInput(*gap.Locator, input) {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, fmt.Errorf("gap locator is outside the artifact"), failureCodeInvalidOutput))
			continue
		}
		gap.AnalyzerID = descriptor.ID
		gap.Locator = completeLocator(gap.Locator, input)
		if gap.Scope == "" {
			gap.Scope = input.Artifact.Path
		} else if gap.Scope != input.Artifact.Path && !strings.HasPrefix(gap.Scope, input.Artifact.Path+"::") {
			gap.Scope = input.Artifact.Path + "::" + gap.Scope
		}
		gap.ID = contract.GapID(gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID)
		gapOrdinal := 1
		for hasGapID(result.gaps, gap.ID) {
			gapOrdinal++
			gap.Scope = fmt.Sprintf("%s#%d", gap.Scope, gapOrdinal)
			gap.ID = contract.GapID(gap.Code, gap.Dimension, gap.Scope, gap.Message, gap.AnalyzerID)
		}
		if err := gap.Validate(); err != nil {
			result.failures = append(result.failures, analyzerFailure(input, descriptor, err, failureCodeInvalidOutput))
			continue
		}
		result.gaps = append(result.gaps, gap)
	}
	return result
}

func hasCoverageID(values []contract.Coverage, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func hasContributionID(values map[string]int, id string) bool {
	_, exists := values[id]
	return exists
}

func locatorMatchesInput(locator contract.Locator, input ArtifactInput) bool {
	if locator.SourceID != "" && locator.SourceID != input.SourceID {
		return false
	}
	if locator.ArtifactID != "" && locator.ArtifactID != input.Artifact.ID {
		return false
	}
	return locator.Path == "" || locator.Path == input.Artifact.Path
}

func completeLocator(locator *contract.Locator, input ArtifactInput) *contract.Locator {
	if locator == nil {
		locator = &contract.Locator{}
	} else {
		copyOfLocator := *locator
		locator = &copyOfLocator
	}
	if locator.SourceID == "" {
		locator.SourceID = input.SourceID
	}
	if locator.ArtifactID == "" {
		locator.ArtifactID = input.Artifact.ID
	}
	if locator.Path == "" {
		locator.Path = input.Artifact.Path
	}
	return locator
}

func hasGapID(values []contract.Gap, id string) bool {
	for _, value := range values {
		if value.ID == id {
			return true
		}
	}
	return false
}

func contributionKey(contribution contract.Contribution) string {
	value, _ := json.Marshal(contribution.Value)
	return strings.Join([]string{
		contribution.Method,
		contribution.Type,
		contribution.Locator.Path,
		contribution.Locator.Member,
		fmt.Sprint(contribution.Locator.StartLine),
		string(value),
	}, "\x00")
}

func evidenceSafeMethod(value string) string {
	value = strings.TrimSpace(value)
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return '_'
		}
		return r
	}, value)
}

func analyzerFailure(input ArtifactInput, descriptor Descriptor, err error, code string) contract.Failure {
	message := "analyzer failed"
	if code == failureCodeInvalidOutput {
		message = "analyzer output was invalid"
	}
	if code == failureCodeCancelled {
		message = "analyzer work was cancelled"
	}
	locator := contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       input.Artifact.Path,
	}
	return contract.Failure{
		Code:       code,
		Operation:  "analyze",
		Message:    message,
		ArtifactID: input.Artifact.ID,
		AnalyzerID: descriptor.ID,
		Partial:    true,
		Locator:    &locator,
	}
}

func sourceChanged(ctx context.Context, input ArtifactInput) (bool, error) {
	maxBytes := input.Limits.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = source.DefaultMaxFileBytes
	}
	hash, err := source.HashFileInRoot(ctx, input.RootHandle, input.Artifact.Path, maxBytes)
	if err != nil {
		return false, err
	}
	return hash.SHA256 != input.Artifact.Hash, nil
}

func locatorForInput(input ArtifactInput) *contract.Locator {
	return &contract.Locator{
		SourceID:   input.SourceID,
		ArtifactID: input.Artifact.ID,
		Path:       input.Artifact.Path,
	}
}

func sourceFailure(sourceID string, failure source.Failure) contract.Failure {
	locator := contract.Locator{SourceID: sourceID, Path: failure.Path}
	artifactID := "source"
	if failure.Path != "" {
		artifactID += ":" + failure.Path
	}
	return contract.Failure{
		Code:       failure.Code,
		Operation:  "discover",
		Message:    failure.Message,
		ArtifactID: artifactID,
		Partial:    true,
		Locator:    &locator,
	}
}

func uniqueFailures(failures []contract.Failure) []contract.Failure {
	result := make([]contract.Failure, 0, len(failures))
	seen := make(map[string]int, len(failures))
	for _, failure := range failures {
		baseMessage := failure.Message
		id := contract.FailureID(failure.Code, failure.Operation, failure.ArtifactID, failure.AnalyzerID, baseMessage)
		if seen[id] > 0 {
			ordinal := seen[id] + 1
			for {
				failure.Message = fmt.Sprintf("%s (occurrence %d)", baseMessage, ordinal)
				id = contract.FailureID(failure.Code, failure.Operation, failure.ArtifactID, failure.AnalyzerID, failure.Message)
				if seen[id] == 0 {
					break
				}
				ordinal++
			}
		}
		seen[id]++
		failure.ID = id
		result = append(result, failure)
	}
	return result
}

func artifactTypeFromSource(artifact source.Artifact) string {
	if artifact.Format == source.FormatCAR {
		return ArtifactTypeCAR
	}
	if artifact.Format == source.FormatZIP {
		return ArtifactTypeZIP
	}
	return artifactType(contract.Artifact{Path: artifact.RelativePath}, artifact)
}

func snapshotDigest(artifacts []contract.Artifact) string {
	hash := sha256.New()
	for _, artifact := range artifacts {
		fmt.Fprintf(hash, "%d:%s%d:%s", len(artifact.Path), artifact.Path, len(artifact.Hash), artifact.Hash)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func normalizeLimits(limits source.Limits) (source.Limits, error) {
	if err := limits.Validate(); err != nil {
		return source.Limits{}, err
	}
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
	return limits, nil
}

func validateOutputDirectory(root, output string) error {
	resolvedRoot, err := resolveExistingPath(root)
	if err != nil {
		return fmt.Errorf("%w: resolving source directory: %v", ErrInvalidRequest, err)
	}
	resolvedOutput, err := resolveExistingPath(output)
	if err != nil {
		return fmt.Errorf("%w: resolving output directory: %v", ErrInvalidRequest, err)
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedOutput)
	if err != nil {
		return fmt.Errorf("%w: comparing source and output directories: %v", ErrInvalidRequest, err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return fmt.Errorf("%w: output directory must be outside source root", ErrInvalidRequest)
	}
	return nil
}

func resolveExistingPath(value string) (string, error) {
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	missing := make([]string, 0, 2)
	probe := absolute
	for {
		_, statErr := os.Lstat(probe)
		if statErr == nil {
			resolved, evalErr := filepath.EvalSymlinks(probe)
			if evalErr != nil {
				return "", evalErr
			}
			resolved, err = filepath.Abs(resolved)
			if err != nil {
				return "", err
			}
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return absolute, nil
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
}

func stateReason(err error) string {
	switch {
	case errors.Is(err, state.ErrIncompatibleVersion):
		return "incompatible_version"
	case errors.Is(err, state.ErrCorrupt):
		return "corrupt"
	default:
		return "unavailable"
	}
}

func invalidatedPaths(previous state.Snapshot, artifacts []contract.Artifact) map[string]bool {
	return invalidatedPathsWithIndex(buildStateIndex(previous), artifacts)
}

func buildStateIndex(snapshot state.Snapshot) stateIndex {
	if err := snapshot.Validate(); err != nil {
		return stateIndex{}
	}
	index := stateIndex{
		artifactsByPath: make(map[string]contract.Artifact, len(snapshot.Artifacts)),
		entriesByKey:    make(map[comparableStateKey]state.Entry, len(snapshot.Entries)),
		dependencies:    append([]state.Dependency(nil), snapshot.Dependencies...),
	}
	for _, artifact := range snapshot.Artifacts {
		index.artifactsByPath[artifact.Path] = artifact
	}
	for _, entry := range snapshot.Entries {
		index.entriesByKey[makeComparableStateKey(entry.Key)] = entry
	}
	return index
}

func makeComparableStateKey(key state.Key) comparableStateKey {
	return comparableStateKey{
		sourceID:        key.SourceID,
		artifactPath:    key.ArtifactPath,
		artifactHash:    key.ArtifactHash,
		contractVersion: key.ContractVersion,
		analyzerID:      key.AnalyzerID,
		analyzerVersion: key.AnalyzerVersion,
		analyzerMethod:  key.AnalyzerMethod,
	}
}

func invalidatedPathsWithIndex(previous stateIndex, artifacts []contract.Artifact) map[string]bool {
	invalidated := make(map[string]bool)
	current := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		current[artifact.Path] = struct{}{}
		old, ok := previous.artifactsByPath[artifact.Path]
		if !ok || old.Hash != artifact.Hash {
			invalidated[artifact.Path] = true
		}
	}
	for path := range previous.artifactsByPath {
		if _, ok := current[path]; !ok {
			invalidated[path] = true
		}
	}
	for _, dependency := range previous.dependencies {
		if invalidated[dependency.ToPath] {
			if _, exists := current[dependency.FromPath]; exists {
				invalidated[dependency.FromPath] = true
			}
		}
	}
	return invalidated
}

func lookupStateEntry(index stateIndex, key state.Key, input ArtifactInput) (state.Entry, bool) {
	entry, ok := index.entriesByKey[makeComparableStateKey(key)]
	if !ok || entry.ArtifactID != input.Artifact.ID {
		return state.Entry{}, false
	}
	if err := entry.Validate(); err != nil {
		return state.Entry{}, false
	}
	return entry, true
}

func buildStateSnapshot(sourceID string, artifacts []contract.Artifact, entries []state.Entry, contributions []contract.Contribution) state.Snapshot {
	snapshot := state.Snapshot{
		Version:         state.Version,
		ContractVersion: contract.Version,
		SourceID:        sourceID,
		Artifacts:       append([]contract.Artifact(nil), artifacts...),
		Entries:         append([]state.Entry(nil), entries...),
		Dependencies:    deriveDependencies(artifacts, contributions),
	}
	sort.Slice(snapshot.Artifacts, func(i, j int) bool { return snapshot.Artifacts[i].Path < snapshot.Artifacts[j].Path })
	sort.Slice(snapshot.Entries, func(i, j int) bool { return snapshot.Entries[i].Key.Canonical() < snapshot.Entries[j].Key.Canonical() })
	sort.Slice(snapshot.Dependencies, func(i, j int) bool {
		left, right := snapshot.Dependencies[i], snapshot.Dependencies[j]
		if left.FromPath != right.FromPath {
			return left.FromPath < right.FromPath
		}
		if left.ToPath != right.ToPath {
			return left.ToPath < right.ToPath
		}
		return left.Kind < right.Kind
	})
	return snapshot
}

func deriveDependencies(artifacts []contract.Artifact, contributions []contract.Contribution) []state.Dependency {
	paths := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		paths[artifact.Path] = struct{}{}
	}
	seen := make(map[string]struct{})
	dependencies := make([]state.Dependency, 0)
	for _, contribution := range contributions {
		from := contribution.Locator.Path
		if from == "" {
			continue
		}
		value := make(map[string]any)
		if len(contribution.Value) == 0 || json.Unmarshal(contribution.Value, &value) != nil {
			continue
		}
		target, _ := value["name"].(string)
		kind := ""
		switch contribution.Type {
		case "java.import":
			kind = "java-import"
		case "java.relation":
			kind = "java-type-relation"
			if target == "" {
				target, _ = value["to"].(string)
			}
		case "wso2.include":
			kind = "path-include"
			target, _ = value["target"].(string)
		default:
			continue
		}
		if target == "" {
			continue
		}
		candidates := dependencyCandidates(contribution.Type, target, paths)
		for _, to := range candidates {
			if to == from {
				continue
			}
			dependency := state.Dependency{FromPath: from, ToPath: to, Kind: kind}
			key := dependency.FromPath + "\x00" + dependency.ToPath + "\x00" + dependency.Kind
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			dependencies = append(dependencies, dependency)
		}
	}
	return dependencies
}

func dependencyCandidates(contributionType, target string, paths map[string]struct{}) []string {
	target = strings.TrimSpace(target)
	if target == "" || target == "[redacted]" {
		return nil
	}
	if contributionType == "java.import" || contributionType == "java.relation" {
		target = strings.TrimPrefix(target, "static ")
		if strings.HasSuffix(target, ".*") {
			return nil
		}
		candidate := strings.TrimSuffix(strings.ReplaceAll(target, ".", "/"), ".java") + ".java"
		if _, exists := paths[candidate]; exists {
			return []string{candidate}
		}
		base := path.Base(candidate)
		matches := make([]string, 0, 1)
		for pathName := range paths {
			if path.Base(pathName) == base {
				matches = append(matches, pathName)
			}
		}
		if len(matches) == 1 {
			return matches
		}
		return nil
	}
	if strings.Contains(target, "://") {
		return nil
	}
	if index := strings.IndexAny(target, "?#"); index >= 0 {
		target = target[:index]
	}
	target = strings.TrimPrefix(strings.ReplaceAll(target, "\\", "/"), "./")
	if target == "" || strings.HasPrefix(target, "/") {
		return nil
	}
	target = path.Clean(target)
	if target == "." || strings.HasPrefix(target, "../") {
		return nil
	}
	if _, exists := paths[target]; exists {
		return []string{target}
	}
	return nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
