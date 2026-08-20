package evaluation

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
	"sort"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/analyzer/generic"
	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	// RealCorpusReportVersion identifies the metadata-only measurement emitted
	// by MeasureCorpus. It is independent from the evaluation case format.
	RealCorpusReportVersion = "v1alpha1"

	defaultRealOrganization = defaultEvaluationOrganization
	defaultRealToolVersion  = "evaluation-real-extractor-v1"
	defaultRealConfigPrefix = "evaluation-real"
)

var (
	// ErrInvalidRealCorpus identifies a corpus configuration that cannot be
	// scoped without guessing a local path, source identity, or revision.
	ErrInvalidRealCorpus = errors.New("evaluation: invalid real corpus")
	// ErrRealCorpusUnavailable identifies a configured corpus that could not be
	// read by the bounded local analyzer pipeline.
	ErrRealCorpusUnavailable = errors.New("evaluation: real corpus unavailable")
)

// RealCorpusConfig explicitly binds one logical corpus to a local source for
// a read-only evaluation. Root is execution configuration and is never
// serialized into a report. The revision must be supplied by the caller; it
// is not inferred from the machine-local path.
type RealCorpusConfig struct {
	CorpusID         string                  `json:"corpus_id"`
	CorpusRevision   string                  `json:"corpus_revision"`
	SourceID         string                  `json:"source_id"`
	SourceRevision   string                  `json:"source_revision"`
	SourceName       string                  `json:"source_name"`
	SourceRole       string                  `json:"source_role"`
	OrganizationID   string                  `json:"organization_id"`
	Root             string                  `json:"root"`
	Includes         []string                `json:"includes,omitempty"`
	Excludes         []string                `json:"excludes,omitempty"`
	SensitivePattern []string                `json:"sensitive_patterns,omitempty"`
	Limits           source.Limits           `json:"limits"`
	EvidenceLimits   analysis.EvidenceLimits `json:"evidence_limits"`
	Policy           evidence.Policy         `json:"policy,omitempty"`
	Output           string                  `json:"output,omitempty"`
	ConfigurationID  string                  `json:"configuration_id,omitempty"`
}

// RealExtractor runs the repository's actual bounded analyzers and adapts
// their output to the deterministic evaluation pipeline. It has no network,
// provider, or source-writing capability.
type RealExtractor struct {
	corpora  map[string]RealCorpusConfig
	registry *analysis.Registry
}

// NewRealExtractor validates explicit corpus bindings and returns an
// extractor using the generic, Java, and WSO2 analyzers already used by
// manu analyze.
func NewRealExtractor(configs ...RealCorpusConfig) (*RealExtractor, error) {
	if len(configs) == 0 {
		return nil, fmt.Errorf("%w: at least one corpus is required", ErrInvalidRealCorpus)
	}
	registry, err := analysis.NewRegistry(generic.New(), java.New(), wso2.New())
	if err != nil {
		return nil, fmt.Errorf("%w: analyzer registry: %v", ErrInvalidRealCorpus, err)
	}
	corpora := make(map[string]RealCorpusConfig, len(configs))
	for _, config := range configs {
		normalized, err := normalizeRealCorpusConfig(config)
		if err != nil {
			return nil, err
		}
		if _, exists := corpora[normalized.SourceID]; exists {
			return nil, fmt.Errorf("%w: duplicate source id", ErrInvalidRealCorpus)
		}
		corpora[normalized.SourceID] = normalized
	}
	return &RealExtractor{corpora: corpora, registry: registry}, nil
}

// Corpus returns a defensive copy of one configured corpus. It is intended
// for local execution tooling and never exposes a corpus in a report.
func (e *RealExtractor) Corpus(sourceID string) (RealCorpusConfig, bool) {
	if e == nil {
		return RealCorpusConfig{}, false
	}
	config, ok := e.corpora[strings.TrimSpace(sourceID)]
	if !ok {
		return RealCorpusConfig{}, false
	}
	return cloneRealCorpusConfig(config), true
}

// Extract implements the evaluation extraction port with real local
// analysis. Expected evidence is matched only by metadata locators and never
// by an answer rubric or fabricated content.
func (e *RealExtractor) Extract(ctx context.Context, item EvaluationCase) (bundle.Bundle, map[string]string, error) {
	if ctx == nil {
		return bundle.Bundle{}, nil, fmt.Errorf("%w: nil context", ErrRealCorpusUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return bundle.Bundle{}, nil, err
	}
	if e == nil || e.registry == nil {
		return bundle.Bundle{}, nil, fmt.Errorf("%w: extractor is not configured", ErrRealCorpusUnavailable)
	}
	config, ok := e.corpora[strings.TrimSpace(item.SourceID)]
	if !ok {
		return bundle.Bundle{}, nil, fmt.Errorf("%w: source is not configured", ErrRealCorpusUnavailable)
	}
	if item.SourceRevision != config.SourceRevision || item.CorpusRevision != config.CorpusRevision || item.CorpusID != config.CorpusID {
		return bundle.Bundle{}, nil, fmt.Errorf("%w: case revision or corpus does not match configured source", ErrRealCorpusUnavailable)
	}
	analysisResult, err := e.run(ctx, config, "case-"+item.CaseID)
	if err != nil {
		return bundle.Bundle{}, nil, err
	}
	input, err := realBundle(analysisResult, config, item.CaseID)
	if err != nil {
		return bundle.Bundle{}, nil, err
	}
	return input, matchExpectedEvidence(item, input), nil
}

// RealCorpusMeasurement is a content-free extraction and scale observation.
// Retrieval and generation are intentionally absent when no competency case
// is supplied for a corpus; callers must not interpret this as coverage of
// those stages.
type RealCorpusMeasurement struct {
	Version              string         `json:"version"`
	CorpusID             string         `json:"corpus_id"`
	CorpusRevision       string         `json:"corpus_revision"`
	SourceID             string         `json:"source_id"`
	SourceRevision       string         `json:"source_revision"`
	SourceRole           string         `json:"source_role"`
	Extraction           StageMetric    `json:"extraction"`
	Volume               VolumeMetric   `json:"volume"`
	Coverage             map[string]int `json:"coverage"`
	GapCount             int            `json:"gap_count"`
	FailureCount         int            `json:"failure_count"`
	BytesRead            int64          `json:"bytes_read"`
	EffectiveConcurrency int            `json:"effective_concurrency"`
	FactualDigest        string         `json:"factual_digest"`
	ExecutionLimitations []string       `json:"execution_limitations,omitempty"`
	NonApplicableStages  []string       `json:"non_applicable_stages"`
}

// MeasureCorpus executes the same real analyzer pipeline without requiring a
// case rubric. It is used for WSO2/ERPNext heterogeneity and scale evidence;
// it never invokes an embedding or generation provider.
func (e *RealExtractor) MeasureCorpus(ctx context.Context, sourceID string) (RealCorpusMeasurement, error) {
	var measurement RealCorpusMeasurement
	if ctx == nil {
		return measurement, fmt.Errorf("%w: nil context", ErrRealCorpusUnavailable)
	}
	if e == nil || e.registry == nil {
		return measurement, fmt.Errorf("%w: extractor is not configured", ErrRealCorpusUnavailable)
	}
	config, ok := e.corpora[strings.TrimSpace(sourceID)]
	if !ok {
		return measurement, fmt.Errorf("%w: source is not configured", ErrRealCorpusUnavailable)
	}
	started := time.Now()
	var metrics analysis.RunMetrics
	result, runErr := e.runWithMetrics(ctx, config, "corpus-"+config.SourceID, &metrics)
	measurement = measurementFromResult(result, config, time.Since(started), metrics)
	if runErr != nil {
		return measurement, fmt.Errorf("%w: %s", ErrRealCorpusUnavailable, safeRealError(runErr))
	}
	if digest, err := bundle.FactualDigest(result.Result, result.Evidence); err == nil {
		measurement.FactualDigest = digest
	}
	return measurement, nil
}

func normalizeRealCorpusConfig(config RealCorpusConfig) (RealCorpusConfig, error) {
	config.CorpusID = strings.TrimSpace(config.CorpusID)
	config.CorpusRevision = strings.TrimSpace(config.CorpusRevision)
	config.SourceID = strings.TrimSpace(config.SourceID)
	config.SourceRevision = strings.TrimSpace(config.SourceRevision)
	config.SourceName = strings.TrimSpace(config.SourceName)
	config.SourceRole = strings.TrimSpace(config.SourceRole)
	config.OrganizationID = strings.TrimSpace(config.OrganizationID)
	config.Root = strings.TrimSpace(config.Root)
	config.Output = strings.TrimSpace(config.Output)
	config.ConfigurationID = strings.TrimSpace(config.ConfigurationID)
	for name, value := range map[string]string{
		"corpus_id":       config.CorpusID,
		"corpus_revision": config.CorpusRevision,
		"source_id":       config.SourceID,
		"source_revision": config.SourceRevision,
		"organization_id": config.OrganizationID,
		"root":            config.Root,
	} {
		if value == "" {
			return RealCorpusConfig{}, fmt.Errorf("%w: %s is required", ErrInvalidRealCorpus, name)
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return RealCorpusConfig{}, fmt.Errorf("%w: %s contains control characters", ErrInvalidRealCorpus, name)
		}
	}
	if !filepath.IsAbs(config.Root) {
		return RealCorpusConfig{}, fmt.Errorf("%w: root must be absolute", ErrInvalidRealCorpus)
	}
	if info, err := os.Stat(config.Root); err != nil || !info.IsDir() {
		return RealCorpusConfig{}, fmt.Errorf("%w: root is not a readable directory", ErrInvalidRealCorpus)
	}
	if config.SourceName == "" {
		config.SourceName = config.SourceID
	}
	if config.SourceRole == "" {
		config.SourceRole = "local-corpus"
	}
	if config.ConfigurationID == "" {
		config.ConfigurationID = defaultRealConfigPrefix + "-" + config.SourceID
	}
	if config.Limits.MaxConcurrency == 0 {
		config.Limits.MaxConcurrency = 4
	}
	if err := config.Limits.Validate(); err != nil {
		return RealCorpusConfig{}, fmt.Errorf("%w: source limits: %v", ErrInvalidRealCorpus, err)
	}
	if err := config.EvidenceLimits.Validate(); err != nil {
		return RealCorpusConfig{}, fmt.Errorf("%w: evidence limits: %v", ErrInvalidRealCorpus, err)
	}
	policy := config.Policy
	if policy.IsZero() {
		policy = evidence.DefaultPolicy()
	}
	if err := policy.Validate(); err != nil {
		return RealCorpusConfig{}, fmt.Errorf("%w: policy: %v", ErrInvalidRealCorpus, err)
	}
	config.Policy = policy
	config.Includes = append([]string(nil), config.Includes...)
	config.Excludes = append([]string(nil), config.Excludes...)
	config.SensitivePattern = append([]string(nil), config.SensitivePattern...)
	return config, nil
}

func cloneRealCorpusConfig(config RealCorpusConfig) RealCorpusConfig {
	config.Includes = append([]string(nil), config.Includes...)
	config.Excludes = append([]string(nil), config.Excludes...)
	config.SensitivePattern = append([]string(nil), config.SensitivePattern...)
	return config
}

func (e *RealExtractor) run(ctx context.Context, config RealCorpusConfig, runSuffix string) (analysis.AnalysisResult, error) {
	result, runErr := e.runWithMetrics(ctx, config, runSuffix, nil)
	if runErr != nil {
		return analysis.AnalysisResult{}, fmt.Errorf("%w: %s", ErrRealCorpusUnavailable, safeRealError(runErr))
	}
	return result, nil
}

func (e *RealExtractor) runWithMetrics(ctx context.Context, config RealCorpusConfig, runSuffix string, metrics *analysis.RunMetrics) (analysis.AnalysisResult, error) {
	if err := ctx.Err(); err != nil {
		return analysis.AnalysisResult{}, err
	}
	runID := "real-" + digestString(config.SourceID + "\x00" + config.SourceRevision + "\x00" + runSuffix)[:16]
	runner, err := analysis.NewRunner(e.registry)
	if err != nil {
		return analysis.AnalysisResult{}, err
	}
	result, runErr := runner.RunWithEvidence(ctx, analysis.Config{
		Source: contract.Source{
			ID:       config.SourceID,
			Name:     config.SourceName,
			Type:     "filesystem",
			Revision: config.SourceRevision,
			Root:     config.Root,
		},
		Root:              config.Root,
		Output:            config.Output,
		Includes:          append([]string(nil), config.Includes...),
		Excludes:          append([]string(nil), config.Excludes...),
		SensitivePatterns: append([]string(nil), config.SensitivePattern...),
		IncludeSensitive:  false,
		Limits:            config.Limits,
		RunID:             runID,
		ToolVersion:       defaultRealToolVersion,
		Metrics:           metrics,
		OrganizationID:    config.OrganizationID,
		EvidenceLimits:    config.EvidenceLimits,
	}, analysis.EvidenceConfig{
		OrganizationID: config.OrganizationID,
		Limits:         config.EvidenceLimits,
		Policy:         config.Policy,
	})
	if runErr != nil {
		return result, runErr
	}
	result.Result.Manifest.Execution.ConfigurationID = config.ConfigurationID
	if err := result.Result.Normalize(); err != nil {
		return result, err
	}
	if err := result.Validate(); err != nil {
		return result, err
	}
	return result, nil
}

func realBundle(result analysis.AnalysisResult, config RealCorpusConfig, caseID string) (bundle.Bundle, error) {
	legacy := result.Result
	if err := legacy.Validate(); err != nil {
		return bundle.Bundle{}, fmt.Errorf("%w: result validation failed", ErrRealCorpusUnavailable)
	}
	if legacy.Manifest.Execution.ConfigurationID != config.ConfigurationID {
		return bundle.Bundle{}, fmt.Errorf("%w: analysis configuration mismatch", ErrRealCorpusUnavailable)
	}
	factualDigest, err := bundle.FactualDigest(legacy, result.Evidence)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("%w: factual digest failed", ErrRealCorpusUnavailable)
	}
	artifactFile, err := realSequenceFile(bundle.ArtifactsFileName, legacy.Artifacts)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("%w: artifact sequence failed", ErrRealCorpusUnavailable)
	}
	contributionFile, err := realSequenceFile(bundle.ContributionsFileName, legacy.Contributions)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("%w: contribution sequence failed", ErrRealCorpusUnavailable)
	}
	evidenceFile, err := realSequenceFile(bundle.EvidenceFileName, result.Evidence)
	if err != nil {
		return bundle.Bundle{}, fmt.Errorf("%w: evidence sequence failed", ErrRealCorpusUnavailable)
	}
	manifest := bundle.Manifest{
		Version: bundle.Version,
		Organization: bundle.Organization{
			ID:   config.OrganizationID,
			Name: "local evaluation",
		},
		Manifest: legacy.Manifest,
		Analysis: bundle.Analysis{
			ID:              "real-analysis-" + digestString(config.SourceID + "\x00" + caseID)[:16],
			ConfigurationID: config.ConfigurationID,
			Revision:        config.SourceRevision,
		},
		FactualDigest: factualDigest,
		Files:         []bundle.File{artifactFile, contributionFile, evidenceFile},
		Counts: bundle.Counts{
			ArtifactCount:     int64(len(legacy.Artifacts)),
			ContributionCount: int64(len(legacy.Contributions)),
			EvidenceUnitCount: int64(len(result.Evidence)),
		},
		Limits: bundle.Limits{
			MaxBundleBytes:   512 << 20,
			MaxManifestBytes: 4 << 20,
			MaxEvidenceBytes: 256 << 20,
			MaxArtifacts:     100_000,
			MaxContributions: 100_000,
			MaxEvidenceUnits: 100_000,
		},
		Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
	}
	input := bundle.Bundle{Manifest: manifest, Artifacts: legacy.Artifacts, Contributions: legacy.Contributions, Evidence: result.Evidence}
	if err := input.Validate(); err != nil {
		return bundle.Bundle{}, fmt.Errorf("%w: bundle validation failed", ErrRealCorpusUnavailable)
	}
	return input, nil
}

func realSequenceFile[T any](name string, values []T) (bundle.File, error) {
	hash := sha256.New()
	var bytes int64
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return bundle.File{}, err
		}
		if _, err := hash.Write(encoded); err != nil {
			return bundle.File{}, err
		}
		if _, err := hash.Write([]byte{'\n'}); err != nil {
			return bundle.File{}, err
		}
		bytes += int64(len(encoded) + 1)
	}
	return bundle.File{Name: name, Bytes: bytes, Count: int64(len(values)), Digest: hex.EncodeToString(hash.Sum(nil))}, nil
}

func measurementFromResult(result analysis.AnalysisResult, config RealCorpusConfig, duration time.Duration, metrics analysis.RunMetrics) RealCorpusMeasurement {
	measurement := RealCorpusMeasurement{
		Version:              RealCorpusReportVersion,
		CorpusID:             config.CorpusID,
		CorpusRevision:       config.CorpusRevision,
		SourceID:             config.SourceID,
		SourceRevision:       config.SourceRevision,
		SourceRole:           config.SourceRole,
		Extraction:           StageMetric{Status: StageCompleted, Duration: duration, Items: len(result.Evidence), Reprocessed: len(result.Evidence)},
		Volume:               VolumeMetric{Artifacts: len(result.Result.Artifacts), Contributions: len(result.Result.Contributions), EvidenceUnits: len(result.Evidence), ContentBytes: evidenceBytes(result.Evidence)},
		Coverage:             map[string]int{},
		GapCount:             len(result.Result.Manifest.Gaps),
		FailureCount:         len(result.Result.Manifest.Failures),
		BytesRead:            metrics.BytesRead,
		EffectiveConcurrency: metrics.EffectiveConcurrency,
		NonApplicableStages:  []string{"ingestion", "retrieval", "generation", "policy"},
	}
	if digest, err := bundle.FactualDigest(result.Result, result.Evidence); err == nil {
		measurement.FactualDigest = digest
	}
	for _, coverage := range result.Result.Manifest.Coverage {
		measurement.Coverage[string(coverage.State)]++
	}
	measurement.ExecutionLimitations = append(measurement.ExecutionLimitations, result.Result.Manifest.Execution.Metrics.Limitations...)
	measurement.ExecutionLimitations = uniqueSortedStrings(measurement.ExecutionLimitations)
	if result.Result.Manifest.Execution.Metrics.Limited > 0 {
		measurement.Extraction.Status = StageLimited
		measurement.Extraction.FailureStage = FailureStageExtraction
		measurement.Extraction.ErrorCode = "source_limits_reached"
	}
	if len(result.Result.Manifest.Failures) > 0 {
		measurement.Extraction.Status = StageLimited
		measurement.Extraction.FailureStage = FailureStageExtraction
		measurement.Extraction.ErrorCode = "analysis_failures"
	}
	return measurement
}

func matchExpectedEvidence(item EvaluationCase, input bundle.Bundle) map[string]string {
	type candidate struct {
		unit  evidence.EvidenceUnit
		score int
	}
	matched := make(map[string]string, len(item.ExpectedEvidence))
	used := make(map[string]struct{}, len(item.ExpectedEvidence))
	for index, expected := range item.ExpectedEvidence {
		key := expected.EvidenceID
		if key == "" {
			key = fmt.Sprintf("expected-%03d", index+1)
		}
		candidates := make([]candidate, 0)
		for _, unit := range input.Evidence {
			score, ok := expectedEvidenceScore(expected, unit)
			if ok {
				candidates = append(candidates, candidate{unit: unit, score: score})
			}
		}
		sort.SliceStable(candidates, func(left, right int) bool {
			if candidates[left].score != candidates[right].score {
				return candidates[left].score > candidates[right].score
			}
			return candidates[left].unit.ID < candidates[right].unit.ID
		})
		selected := ""
		for _, option := range candidates {
			if _, alreadyUsed := used[option.unit.ID]; alreadyUsed {
				continue
			}
			selected = option.unit.ID
			break
		}
		if selected == "" && len(candidates) > 0 {
			selected = candidates[0].unit.ID
		}
		if selected != "" {
			matched[key] = selected
			used[selected] = struct{}{}
		}
	}
	return matched
}

func expectedEvidenceScore(expected ExpectedEvidence, unit evidence.EvidenceUnit) (int, bool) {
	pathName := unit.Locator.Path
	member := ""
	pathPattern := ""
	if expected.Locator != nil {
		pathPattern = expected.Locator.Path
		member = expected.Locator.Member
		if pathPattern != "" && !portablePathMatch(pathPattern, pathName) {
			return 0, false
		}
		if expected.Locator.StartLine > 0 || expected.Locator.EndLine > 0 {
			if !lineRangesOverlap(expected.Locator.StartLine, expected.Locator.EndLine, unit.Locator.StartLine, unit.Locator.EndLine) {
				return 0, false
			}
		}
	} else if expected.Pattern != nil {
		pathPattern = expected.Pattern.PathPattern
		member = expected.Pattern.Member
		if member == "" {
			member = expected.Pattern.Symbol
		}
		if pathPattern != "" && !portablePathMatch(pathPattern, pathName) {
			return 0, false
		}
	}
	if member != "" && !unitSemanticMemberMatch(member, unit) {
		return 0, false
	}
	if expected.Pattern != nil && expected.Pattern.Attribute != "" && !strings.Contains(strings.ToLower(unit.Content), strings.ToLower(expected.Pattern.Attribute)) {
		return 0, false
	}
	if expected.Pattern != nil && expected.Pattern.XPath != "" && !strings.Contains(strings.ToLower(unit.Contribution.Method), strings.ToLower(expected.Pattern.XPath)) && !strings.Contains(strings.ToLower(unit.Content), strings.ToLower(expected.Pattern.XPath)) {
		return 0, false
	}
	score := 10
	if pathPattern != "" && pathPattern == pathName {
		score += 100
	} else if pathPattern != "" {
		score += 50
	}
	if member != "" {
		score += 20
	}
	if expected.Locator != nil && (expected.Locator.StartLine > 0 || expected.Locator.EndLine > 0) {
		score += 10
	}
	return score, true
}

func portablePathMatch(pattern, value string) bool {
	pattern = strings.ReplaceAll(pattern, "\\", "/")
	value = strings.ReplaceAll(value, "\\", "/")
	if pattern == value {
		return true
	}
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}

func lineRangesOverlap(expectedStart, expectedEnd, actualStart, actualEnd int) bool {
	if expectedStart == 0 {
		expectedStart = 1
	}
	if expectedEnd < expectedStart {
		expectedEnd = expectedStart
	}
	if actualStart == 0 {
		return true
	}
	if actualEnd < actualStart {
		actualEnd = actualStart
	}
	return expectedStart <= actualEnd && actualStart <= expectedEnd
}

func unitSemanticMemberMatch(member string, unit evidence.EvidenceUnit) bool {
	wanted := strings.ToLower(strings.TrimSpace(member))
	if wanted == "" {
		return true
	}
	values := []string{unit.Locator.Member, unit.Contribution.Method, unit.Contribution.AnalyzerID, unit.Content}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), wanted) {
			return true
		}
	}
	return false
}

func safeRealError(err error) string {
	if err == nil {
		return ""
	}
	// Do not copy analyzer/path/provider details into a report. The caller
	// receives only a stable stage code; this intentionally discards the
	// wrapped diagnostic rather than attempting to sanitize arbitrary text.
	if errors.Is(err, context.Canceled) {
		return "analysis_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "analysis_deadline_exceeded"
	}
	return "local_pipeline_error"
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	result := ordered[:0]
	for _, value := range ordered {
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}
