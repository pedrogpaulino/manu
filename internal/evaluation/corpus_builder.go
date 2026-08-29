package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/analyzer/java"
	"github.com/pedrogpaulino/manu/internal/analyzer/python"
	"github.com/pedrogpaulino/manu/internal/analyzer/wso2"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	// FactualCorpusVersion identifies this in-memory evaluation corpus
	// envelope. It is independent from both the case-set and bundle versions.
	FactualCorpusVersion = "v1alpha1"

	defaultFactualCorpusOrganization  = defaultEvaluationOrganization
	defaultFactualCorpusToolVersion   = "evaluation-factual-corpus-v1"
	defaultFactualCorpusConfiguration = "evaluation-factual-corpus-v1"

	// SourceRevisionDigestVersion identifies the canonical source revision
	// encoding used by the evaluation corpus.
	SourceRevisionDigestVersion = "v1"
	sourceRevisionDigestDomain  = "manu:evaluation:source-revision:v1\x00"
)

var (
	// ErrInvalidFactualCorpus identifies a corpus binding that cannot be
	// safely executed or represented as a factual snapshot.
	ErrInvalidFactualCorpus = errors.New("evaluation: invalid factual corpus")
	// ErrFactualCorpusUnavailable identifies a bounded local analysis that did
	// not produce a complete snapshot.
	ErrFactualCorpusUnavailable = errors.New("evaluation: factual corpus unavailable")
	// ErrSourceRevisionMismatch identifies source content whose observed
	// revision differs from the case declaration.
	ErrSourceRevisionMismatch = errors.New("evaluation: source revision mismatch")
	// ErrInvalidSourceRevision identifies malformed source revision inputs.
	ErrInvalidSourceRevision = errors.New("evaluation: invalid source revision")
)

// FactualCorpusConfig binds the repository root and the explicit local
// execution policy used to build factual snapshots. Root is execution-only;
// it is never copied into a returned bundle or report.
type FactualCorpusConfig struct {
	Root            string                  `json:"-"`
	OrganizationID  string                  `json:"organization_id"`
	ToolVersion     string                  `json:"tool_version"`
	ConfigurationID string                  `json:"configuration_id"`
	Limits          source.Limits           `json:"limits"`
	EvidenceLimits  analysis.EvidenceLimits `json:"evidence_limits"`
	Policy          evidence.Policy         `json:"policy,omitempty"`
}

// FactualCorpusBuilder executes only the repository's safe-static analyzers
// and their explicit normalizers. It has no source-writing, network, build,
// import, or runtime-execution capability.
type FactualCorpusBuilder struct {
	config FactualCorpusConfig
}

// FactualCorpus is a deterministic collection of snapshots grouped by the
// corpus/source revision identity used by evaluation cases.
type FactualCorpus struct {
	Version   string                  `json:"version"`
	Snapshots []FactualCorpusSnapshot `json:"snapshots"`
}

// FactualCorpusSnapshot contains the complete v1alpha2 bundle needed by the
// factual repository boundary. SourceRevision identifies the input artifacts;
// FactualDigest identifies the resulting factual bundle. Case IDs are
// references only; claims, gaps, and reference answers are deliberately not
// copied into this factual output.
type FactualCorpusSnapshot struct {
	CorpusID       string        `json:"corpus_id"`
	CorpusRevision string        `json:"corpus_revision"`
	SourceID       string        `json:"source_id"`
	SourceRevision string        `json:"source_revision"`
	AnalyzerID     string        `json:"analyzer_id"`
	CaseIDs        []string      `json:"case_ids"`
	Scope          fact.Scope    `json:"scope"`
	Bundle         bundle.Bundle `json:"bundle"`
	FactualDigest  string        `json:"factual_digest"`
}

// SourceRevisionMismatchError reports only the expected and observed source
// revision digests. Its error text intentionally omits paths and content.
type SourceRevisionMismatchError struct {
	Expected string
	Observed string
}

func (e SourceRevisionMismatchError) Error() string {
	return ErrSourceRevisionMismatch.Error()
}

func (e SourceRevisionMismatchError) Unwrap() error { return ErrSourceRevisionMismatch }

// FactualCorpusUnavailableError reports safe source-revision diagnostics
// without returning any partially validated bundle.
type FactualCorpusUnavailableError struct {
	Mismatches []SourceRevisionMismatchError
}

func (e FactualCorpusUnavailableError) Error() string {
	return ErrFactualCorpusUnavailable.Error()
}

func (e FactualCorpusUnavailableError) Unwrap() []error {
	result := make([]error, 0, len(e.Mismatches)+1)
	result = append(result, ErrFactualCorpusUnavailable)
	for _, mismatch := range e.Mismatches {
		result = append(result, mismatch)
	}
	return result
}

// SourceRevisionArtifact is the content-free identity of one source
// artifact. Path must be a normalized repository-relative path.
type SourceRevisionArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// SourceRevisionDigest computes the deterministic v1 revision of an ordered
// set of source artifacts. It hashes only normalized paths, content SHA-256
// values, and sizes; source content never enters the returned value.
func SourceRevisionDigest(artifacts []SourceRevisionArtifact) (string, error) {
	if len(artifacts) == 0 {
		return "", ErrInvalidSourceRevision
	}
	ordered := append([]SourceRevisionArtifact(nil), artifacts...)
	seen := make(map[string]struct{}, len(ordered))
	for index := range ordered {
		candidate := &ordered[index]
		if !validSourceRevisionPath(candidate.Path) || !validSHA256(candidate.SHA256) || candidate.Size < 0 {
			return "", ErrInvalidSourceRevision
		}
		if _, exists := seen[candidate.Path]; exists {
			return "", ErrInvalidSourceRevision
		}
		seen[candidate.Path] = struct{}{}
	}
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].Path < ordered[right].Path })
	payload, err := json.Marshal(struct {
		Version   string                   `json:"version"`
		Artifacts []SourceRevisionArtifact `json:"artifacts"`
	}{Version: SourceRevisionDigestVersion, Artifacts: ordered})
	if err != nil {
		return "", ErrInvalidSourceRevision
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(sourceRevisionDigestDomain))
	_, _ = hash.Write(payload)
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// NewFactualCorpusBuilder validates an explicit repository binding and
// returns a builder with detached configuration. Relative roots are rejected
// so callers cannot accidentally evaluate a moving working-directory path.
func NewFactualCorpusBuilder(config FactualCorpusConfig) (*FactualCorpusBuilder, error) {
	config.Root = strings.TrimSpace(config.Root)
	if config.Root == "" || !filepath.IsAbs(config.Root) {
		return nil, ErrInvalidFactualCorpus
	}
	root, err := source.NormalizeRoot(config.Root)
	if err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	config.Root = root
	config.OrganizationID = strings.TrimSpace(config.OrganizationID)
	if config.OrganizationID == "" {
		config.OrganizationID = defaultFactualCorpusOrganization
	}
	config.ToolVersion = strings.TrimSpace(config.ToolVersion)
	if config.ToolVersion == "" {
		config.ToolVersion = defaultFactualCorpusToolVersion
	}
	config.ConfigurationID = strings.TrimSpace(config.ConfigurationID)
	if config.ConfigurationID == "" {
		config.ConfigurationID = defaultFactualCorpusConfiguration
	}
	if err := (fact.Scope{OrganizationID: config.OrganizationID, SourceID: "source", SnapshotID: "snapshot"}).Validate(); err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	if err := config.Limits.Validate(); err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	if config.EvidenceLimits.MaxUnitsPerArtifact == 0 && config.EvidenceLimits.MaxBytesPerUnit == 0 && config.EvidenceLimits.MaxCharactersPerUnit == 0 {
		config.EvidenceLimits = analysis.DefaultEvidenceLimits()
	}
	if err := config.EvidenceLimits.Validate(); err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	if config.Policy.IsZero() {
		config.Policy = evidence.DefaultPolicy()
	}
	if err := config.Policy.Validate(); err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	return &FactualCorpusBuilder{config: config}, nil
}

// BuildFactualCorpus is the convenience entry point for a repository root
// and a v1alpha2 case set.
func BuildFactualCorpus(ctx context.Context, root string, cases CaseSet) (FactualCorpus, error) {
	builder, err := NewFactualCorpusBuilder(FactualCorpusConfig{Root: root})
	if err != nil {
		return FactualCorpus{}, err
	}
	return builder.Build(ctx, cases)
}

// Build runs one real analyzer/normalizer pipeline per distinct corpus and
// source revision. It returns a corpus only when every observed source
// revision agrees with its case revision; otherwise it returns
// ErrFactualCorpusUnavailable and no partially validated bundle.
func (b *FactualCorpusBuilder) Build(ctx context.Context, cases CaseSet) (FactualCorpus, error) {
	corpus := FactualCorpus{Version: FactualCorpusVersion, Snapshots: []FactualCorpusSnapshot{}}
	if ctx == nil {
		return corpus, ErrInvalidFactualCorpus
	}
	if err := ctx.Err(); err != nil {
		return corpus, err
	}
	if b == nil {
		return corpus, ErrInvalidFactualCorpus
	}
	normalized, err := cases.Normalize()
	if err != nil || normalized.Version != Version {
		return corpus, ErrInvalidFactualCorpus
	}
	groups, err := factualCorpusGroups(b.config.Root, normalized.Cases)
	if err != nil {
		return corpus, err
	}
	var buildErrors []error
	var mismatches []SourceRevisionMismatchError
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return corpus, err
		}
		snapshot, buildErr := b.buildGroup(ctx, group)
		if buildErr != nil {
			buildErrors = append(buildErrors, buildErr)
			var mismatch SourceRevisionMismatchError
			if errors.As(buildErr, &mismatch) {
				mismatches = append(mismatches, mismatch)
			}
			continue
		}
		corpus.Snapshots = append(corpus.Snapshots, *snapshot)
	}
	sort.SliceStable(corpus.Snapshots, func(left, right int) bool {
		return factualSnapshotKey(corpus.Snapshots[left]) < factualSnapshotKey(corpus.Snapshots[right])
	})
	if len(buildErrors) > 0 {
		if len(mismatches) > 0 {
			return FactualCorpus{}, FactualCorpusUnavailableError{Mismatches: append([]SourceRevisionMismatchError(nil), mismatches...)}
		}
		return FactualCorpus{}, errors.Join(append([]error{ErrFactualCorpusUnavailable}, buildErrors...)...)
	}
	if err := corpus.Validate(); err != nil {
		return FactualCorpus{}, ErrInvalidFactualCorpus
	}
	return corpus, nil
}

// Validate checks every returned bundle and keeps its two digest identities
// separate: SourceRevision identifies the input artifacts, while
// FactualDigest identifies the resulting bundle facts.
func (c FactualCorpus) Validate() error {
	if c.Version != FactualCorpusVersion || len(c.Snapshots) == 0 {
		return ErrInvalidFactualCorpus
	}
	previous := ""
	for index, snapshot := range c.Snapshots {
		if snapshot.CorpusID == "" || snapshot.CorpusRevision == "" || snapshot.SourceID == "" || snapshot.SourceRevision == "" || snapshot.AnalyzerID == "" {
			return ErrInvalidFactualCorpus
		}
		if err := snapshot.Scope.Validate(); err != nil {
			return ErrInvalidFactualCorpus
		}
		if snapshot.Scope.OrganizationID == "" || snapshot.Scope.SourceID != snapshot.SourceID {
			return ErrInvalidFactualCorpus
		}
		if err := snapshot.Bundle.Validate(); err != nil {
			return ErrInvalidFactualCorpus
		}
		if snapshot.Bundle.Manifest.Organization.ID != snapshot.Scope.OrganizationID || snapshot.Bundle.Manifest.Source.ID != snapshot.Scope.SourceID || snapshot.Bundle.Manifest.Snapshot.ID != snapshot.Scope.SnapshotID {
			return ErrInvalidFactualCorpus
		}
		if !validSHA256(snapshot.SourceRevision) || !validSHA256(snapshot.FactualDigest) || snapshot.FactualDigest != snapshot.Bundle.Manifest.FactualDigest || snapshot.SourceRevision != snapshot.Bundle.Manifest.Source.Revision || snapshot.SourceRevision != snapshot.Bundle.Manifest.Analysis.Revision {
			return ErrInvalidFactualCorpus
		}
		artifacts := make([]SourceRevisionArtifact, 0, len(snapshot.Bundle.Artifacts))
		for _, artifact := range snapshot.Bundle.Artifacts {
			if artifact.SourceID != snapshot.SourceID || !validSourceRevisionPath(artifact.Path) || !validSHA256(artifact.Hash) || artifact.Size < 0 {
				return ErrInvalidFactualCorpus
			}
			artifacts = append(artifacts, SourceRevisionArtifact{Path: artifact.Path, SHA256: artifact.Hash, Size: artifact.Size})
		}
		observedSourceRevision, err := SourceRevisionDigest(artifacts)
		if err != nil || observedSourceRevision != snapshot.SourceRevision {
			return ErrInvalidFactualCorpus
		}
		key := factualSnapshotKey(snapshot)
		if index > 0 && key <= previous {
			return ErrInvalidFactualCorpus
		}
		previous = key
	}
	return nil
}

type factualCorpusGroup struct {
	corpusID       string
	corpusRevision string
	sourceID       string
	sourceRevision string
	analyzerID     string
	caseIDs        []string
	artifacts      []string
}

func factualCorpusGroups(root string, cases []EvaluationCase) ([]factualCorpusGroup, error) {
	type groupKey struct {
		corpusID       string
		corpusRevision string
		sourceID       string
		sourceRevision string
	}
	groupsByKey := make(map[groupKey]*factualCorpusGroup)
	for _, item := range cases {
		analyzerID, err := factualCaseAnalyzer(item)
		if err != nil {
			return nil, ErrInvalidFactualCorpus
		}
		key := groupKey{item.CorpusID, item.CorpusRevision, item.SourceID, item.SourceRevision}
		group := groupsByKey[key]
		if group == nil {
			group = &factualCorpusGroup{
				corpusID:       item.CorpusID,
				corpusRevision: item.CorpusRevision,
				sourceID:       item.SourceID,
				sourceRevision: item.SourceRevision,
				analyzerID:     analyzerID,
			}
			groupsByKey[key] = group
		} else if group.analyzerID != analyzerID {
			return nil, ErrInvalidFactualCorpus
		}
		group.caseIDs = append(group.caseIDs, item.CaseID)
		group.artifacts = append(group.artifacts, item.Scope.Artifacts...)
	}
	groups := make([]factualCorpusGroup, 0, len(groupsByKey))
	for _, group := range groupsByKey {
		artifacts, err := normalizeCorpusArtifacts(root, group.artifacts)
		if err != nil {
			return nil, err
		}
		group.artifacts = artifacts
		sort.Strings(group.caseIDs)
		groups = append(groups, *group)
	}
	sort.SliceStable(groups, func(left, right int) bool {
		return factualGroupKey(groups[left]) < factualGroupKey(groups[right])
	})
	return groups, nil
}

func factualCaseAnalyzer(item EvaluationCase) (string, error) {
	applicable := make([]AnalyzerApplicability, 0, len(item.ApplicableAnalyzers))
	for _, analyzer := range item.ApplicableAnalyzers {
		if analyzer.Status == AnalyzerApplicable {
			applicable = append(applicable, analyzer)
		}
	}
	if len(applicable) != 1 {
		return "", ErrInvalidFactualCorpus
	}
	switch applicable[0].ID {
	case java.AnalyzerID, python.AnalyzerID, wso2.AnalyzerID:
		return applicable[0].ID, nil
	default:
		return "", ErrInvalidFactualCorpus
	}
}

func normalizeCorpusArtifacts(root string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, ErrInvalidFactualCorpus
	}
	unique := make(map[string]struct{}, len(paths))
	for _, candidate := range paths {
		if candidate == "" || strings.ContainsAny(candidate, "*?[") {
			return nil, ErrInvalidFactualCorpus
		}
		normalized, err := source.NormalizeRelativePath(root, candidate)
		if err != nil || normalized == "." {
			return nil, ErrInvalidFactualCorpus
		}
		if _, exists := unique[normalized]; exists {
			continue
		}
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(normalized)))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalidFactualCorpus
		}
		unique[normalized] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for pathName := range unique {
		result = append(result, pathName)
	}
	sort.Strings(result)
	return result, nil
}

func validSourceRevisionPath(value string) bool {
	if value == "" || value == "." || strings.IndexByte(value, 0) >= 0 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") || filepath.IsAbs(value) {
		return false
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return false
	}
	return !(len(clean) >= 2 && ((clean[0] >= 'a' && clean[0] <= 'z') || (clean[0] >= 'A' && clean[0] <= 'Z')) && clean[1] == ':')
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (b *FactualCorpusBuilder) observeSourceRevision(ctx context.Context, group factualCorpusGroup) (string, error) {
	discovered, err := source.Discover(ctx, source.Config{
		Root:     b.config.Root,
		Includes: append([]string(nil), group.artifacts...),
		Limits:   b.config.Limits,
	})
	if err != nil || discovered.Limited || discovered.Cancelled || len(discovered.Artifacts) != len(group.artifacts) {
		return "", ErrFactualCorpusUnavailable
	}
	artifacts := make([]SourceRevisionArtifact, 0, len(discovered.Artifacts))
	for index, candidate := range discovered.Artifacts {
		if index >= len(group.artifacts) || candidate.RelativePath != group.artifacts[index] {
			return "", ErrFactualCorpusUnavailable
		}
		artifacts = append(artifacts, SourceRevisionArtifact{
			Path:   candidate.RelativePath,
			SHA256: candidate.SHA256,
			Size:   candidate.Size,
		})
	}
	digest, err := SourceRevisionDigest(artifacts)
	if err != nil {
		return "", ErrFactualCorpusUnavailable
	}
	return digest, nil
}

func (b *FactualCorpusBuilder) buildGroup(ctx context.Context, group factualCorpusGroup) (*FactualCorpusSnapshot, error) {
	observedSourceRevision, err := b.observeSourceRevision(ctx, group)
	if err != nil {
		return nil, err
	}
	if observedSourceRevision != group.sourceRevision {
		return nil, SourceRevisionMismatchError{Expected: group.sourceRevision, Observed: observedSourceRevision}
	}
	frontend, ok := factualFrontendFor(group.analyzerID)
	if !ok {
		return nil, ErrInvalidFactualCorpus
	}
	registry, err := analysis.NewRegistry(frontend.analyzer)
	if err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	runner, err := analysis.NewRunner(registry)
	if err != nil {
		return nil, ErrInvalidFactualCorpus
	}
	runID := "factual-" + digestString(factualGroupKey(group))[:16]
	result, runErr := runner.RunWithEvidence(ctx, analysis.Config{
		Source: contract.Source{
			ID:       group.sourceID,
			Name:     group.corpusID,
			Type:     "filesystem",
			Revision: group.sourceRevision,
			Root:     b.config.Root,
		},
		Root:           b.config.Root,
		Includes:       append([]string(nil), group.artifacts...),
		Limits:         b.config.Limits,
		RunID:          runID,
		ToolVersion:    b.config.ToolVersion,
		OrganizationID: b.config.OrganizationID,
		EvidenceLimits: b.config.EvidenceLimits,
	}, analysis.EvidenceConfig{
		OrganizationID: b.config.OrganizationID,
		Limits:         b.config.EvidenceLimits,
		Policy:         b.config.Policy,
	})
	if runErr != nil {
		return nil, ErrFactualCorpusUnavailable
	}
	if err := result.Validate(); err != nil {
		return nil, ErrFactualCorpusUnavailable
	}
	scope := fact.Scope{
		OrganizationID: b.config.OrganizationID,
		SourceID:       group.sourceID,
		SnapshotID:     result.Result.Manifest.Snapshot.ID,
	}
	normalized, err := normalizeCorpusFacts(ctx, result, scope, frontend)
	if err != nil {
		return nil, ErrFactualCorpusUnavailable
	}
	input, observedDigest, err := factualBundle(result, normalized, scope, group, frontend.manifest, b.config)
	if err != nil {
		return nil, ErrFactualCorpusUnavailable
	}
	snapshot := &FactualCorpusSnapshot{
		CorpusID:       group.corpusID,
		CorpusRevision: group.corpusRevision,
		SourceID:       group.sourceID,
		SourceRevision: group.sourceRevision,
		AnalyzerID:     group.analyzerID,
		CaseIDs:        append([]string(nil), group.caseIDs...),
		Scope:          scope,
		Bundle:         input,
		FactualDigest:  observedDigest,
	}
	return snapshot, nil
}

type factualFrontend struct {
	analyzer      analysis.Analyzer
	manifest      fact.FrontendManifest
	registrations func(fact.FrontendManifest) ([]normalization.Registration, error)
}

func factualFrontendFor(id string) (factualFrontend, bool) {
	switch id {
	case java.AnalyzerID:
		return factualFrontend{analyzer: java.New(), manifest: java.Manifest(), registrations: java.NormalizerRegistrations}, true
	case python.AnalyzerID:
		return factualFrontend{analyzer: python.New(), manifest: python.Manifest(), registrations: python.NormalizerRegistrations}, true
	case wso2.AnalyzerID:
		return factualFrontend{analyzer: wso2.New(), manifest: wso2.Manifest(), registrations: wso2.NormalizerRegistrations}, true
	default:
		return factualFrontend{}, false
	}
}

func normalizeCorpusFacts(ctx context.Context, result analysis.AnalysisResult, scope fact.Scope, frontend factualFrontend) (normalization.Output, error) {
	registrations, err := frontend.registrations(frontend.manifest)
	if err != nil {
		return normalization.Output{}, err
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		return normalization.Output{}, err
	}
	evidenceByContribution := make(map[string][]fact.EvidenceRef, len(result.Evidence))
	for _, unit := range result.Evidence {
		if unit.SourceID != scope.SourceID || unit.SnapshotID != scope.SnapshotID {
			return normalization.Output{}, ErrInvalidFactualCorpus
		}
		evidenceByContribution[unit.Contribution.ID] = append(evidenceByContribution[unit.Contribution.ID], fact.EvidenceRef{
			ID:      unit.ID,
			Locator: unit.Locator,
		})
	}
	inputs := make([]normalization.Input, 0, len(result.Result.Contributions))
	for _, contribution := range result.Result.Contributions {
		refs := append([]fact.EvidenceRef(nil), evidenceByContribution[contribution.ID]...)
		sort.SliceStable(refs, func(left, right int) bool { return refs[left].ID < refs[right].ID })
		inputs = append(inputs, normalization.Input{
			Scope:        scope,
			Manifest:     frontend.manifest,
			Contribution: contribution,
			Evidence:     refs,
		})
	}
	return registry.NormalizeAll(ctx, inputs)
}

func factualBundle(
	result analysis.AnalysisResult,
	normalized normalization.Output,
	scope fact.Scope,
	group factualCorpusGroup,
	frontend fact.FrontendManifest,
	config FactualCorpusConfig,
) (bundle.Bundle, string, error) {
	legacy := result.Result
	legacy.Manifest.Source.Root = ""
	legacy.Manifest.Execution.ConfigurationID = config.ConfigurationID
	legacy.Manifest.Coverage = mergeCorpusCoverage(legacy.Manifest.Coverage, normalized.Coverage)
	if err := legacy.Normalize(); err != nil {
		return bundle.Bundle{}, "", err
	}
	frontendManifests := []fact.FrontendManifest{frontend}
	extensions := make([]json.RawMessage, 0, len(normalized.Extensions))
	for _, extension := range normalized.Extensions {
		encoded, err := json.Marshal(extension)
		if err != nil {
			return bundle.Bundle{}, "", err
		}
		extensions = append(extensions, encoded)
	}
	observedDigest, err := bundle.FactualDigestV2(legacy, result.Evidence, frontendManifests, normalized.Facts, extensions)
	if err != nil {
		return bundle.Bundle{}, "", err
	}
	limits := bundle.Limits{
		MaxBundleBytes:       512 << 20,
		MaxManifestBytes:     4 << 20,
		MaxEvidenceBytes:     256 << 20,
		MaxArtifacts:         bundle.DefaultMaxArtifacts,
		MaxContributions:     bundle.DefaultMaxContributions,
		MaxEvidenceUnits:     bundle.DefaultMaxEvidenceUnits,
		MaxFrontendManifests: bundle.DefaultMaxFrontendManifests,
		MaxCanonicalFacts:    bundle.DefaultMaxCanonicalFacts,
		MaxExtensions:        bundle.DefaultMaxExtensions,
	}
	files, err := factualSequenceFiles(legacy, result.Evidence, frontendManifests, normalized.Facts, extensions)
	if err != nil {
		return bundle.Bundle{}, "", err
	}
	manifest := bundle.Manifest{
		Version: bundle.VersionV1Alpha2,
		Organization: bundle.Organization{
			ID:   config.OrganizationID,
			Name: "local evaluation",
		},
		Manifest:      legacy.Manifest,
		Analysis:      bundle.Analysis{ID: "factual-analysis-" + digestString(factualGroupKey(group))[:16], ConfigurationID: config.ConfigurationID, Revision: group.sourceRevision},
		FactualDigest: observedDigest,
		Files:         files,
		Counts: bundle.Counts{
			ArtifactCount:         int64(len(legacy.Artifacts)),
			ContributionCount:     int64(len(legacy.Contributions)),
			EvidenceUnitCount:     int64(len(result.Evidence)),
			FrontendManifestCount: int64(len(frontendManifests)),
			CanonicalFactCount:    int64(len(normalized.Facts)),
			ExtensionCount:        int64(len(extensions)),
		},
		Limits:   limits,
		Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
	}
	if len(result.Evidence) == 0 {
		manifest.Evidence = bundle.EvidenceMetadata{State: bundle.EvidenceStateLimited}
	}
	input := bundle.Bundle{
		Manifest:          manifest,
		Artifacts:         append([]contract.Artifact(nil), legacy.Artifacts...),
		Contributions:     append([]contract.Contribution(nil), legacy.Contributions...),
		Evidence:          append([]evidence.EvidenceUnit(nil), result.Evidence...),
		FrontendManifests: frontendManifests,
		Facts:             append([]fact.CanonicalFact(nil), normalized.Facts...),
		Extensions:        extensions,
	}
	if err := input.Validate(); err != nil {
		return bundle.Bundle{}, "", err
	}
	return input, observedDigest, nil
}

func mergeCorpusCoverage(existing, added []contract.Coverage) []contract.Coverage {
	result := append([]contract.Coverage(nil), existing...)
	seen := make(map[string]struct{}, len(result)+len(added))
	for _, coverage := range result {
		seen[coverage.ID] = struct{}{}
	}
	for _, coverage := range added {
		if _, exists := seen[coverage.ID]; exists {
			continue
		}
		result = append(result, coverage)
		seen[coverage.ID] = struct{}{}
	}
	sort.SliceStable(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result
}

func factualSequenceFiles(
	result contract.Result,
	evidenceUnits []evidence.EvidenceUnit,
	frontendManifests []fact.FrontendManifest,
	facts []fact.CanonicalFact,
	extensions []json.RawMessage,
) ([]bundle.File, error) {
	artifactFile, err := realSequenceFile(bundle.ArtifactsFileName, result.Artifacts)
	if err != nil {
		return nil, err
	}
	contributionFile, err := realSequenceFile(bundle.ContributionsFileName, result.Contributions)
	if err != nil {
		return nil, err
	}
	frontendFile, err := realSequenceFile(bundle.FrontendManifestsFileName, frontendManifests)
	if err != nil {
		return nil, err
	}
	factFile, err := realSequenceFile(bundle.CanonicalFactsFileName, facts)
	if err != nil {
		return nil, err
	}
	extensionFile, err := realSequenceFile(bundle.ExtensionsFileName, extensions)
	if err != nil {
		return nil, err
	}
	files := []bundle.File{artifactFile, contributionFile, frontendFile, factFile, extensionFile}
	if len(evidenceUnits) > 0 {
		evidenceFile, evidenceErr := realSequenceFile(bundle.EvidenceFileName, evidenceUnits)
		if evidenceErr != nil {
			return nil, evidenceErr
		}
		files = append(files, evidenceFile)
	}
	sort.SliceStable(files, func(left, right int) bool { return files[left].Name < files[right].Name })
	return files, nil
}

func factualGroupKey(group factualCorpusGroup) string {
	return strings.Join([]string{group.corpusID, group.corpusRevision, group.sourceID, group.sourceRevision, group.analyzerID, strings.Join(group.artifacts, "\x00")}, "\x00")
}

func factualSnapshotKey(snapshot FactualCorpusSnapshot) string {
	return strings.Join([]string{snapshot.CorpusID, snapshot.CorpusRevision, snapshot.SourceID, snapshot.SourceRevision, snapshot.AnalyzerID}, "\x00")
}
