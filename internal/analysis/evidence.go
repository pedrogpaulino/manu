package analysis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

const (
	// DefaultEvidenceMaxUnitsPerArtifact keeps a single artifact from
	// dominating an evidence bundle.
	DefaultEvidenceMaxUnitsPerArtifact = 8
	// DefaultEvidenceMaxBytesPerUnit bounds one retained evidence excerpt.
	DefaultEvidenceMaxBytesPerUnit int64 = 4 << 10
	// DefaultEvidenceMaxCharactersPerUnit bounds one retained evidence excerpt
	// independently of its UTF-8 byte width.
	DefaultEvidenceMaxCharactersPerUnit int64 = 2 << 10
)

// EvidenceLimits bounds materialization for one evidence run. Zero values
// use conservative defaults; negative values fail before source work starts.
type EvidenceLimits struct {
	MaxUnitsPerArtifact  int   `json:"max_units_per_artifact,omitempty"`
	MaxBytesPerUnit      int64 `json:"max_bytes_per_unit,omitempty"`
	MaxCharactersPerUnit int64 `json:"max_characters_per_unit,omitempty"`
}

// DefaultEvidenceLimits returns the bounded defaults used by RunWithEvidence.
func DefaultEvidenceLimits() EvidenceLimits {
	return EvidenceLimits{
		MaxUnitsPerArtifact:  DefaultEvidenceMaxUnitsPerArtifact,
		MaxBytesPerUnit:      DefaultEvidenceMaxBytesPerUnit,
		MaxCharactersPerUnit: DefaultEvidenceMaxCharactersPerUnit,
	}
}

func (l EvidenceLimits) normalized() (EvidenceLimits, error) {
	if l.MaxUnitsPerArtifact < 0 || l.MaxBytesPerUnit < 0 || l.MaxCharactersPerUnit < 0 {
		return EvidenceLimits{}, fmt.Errorf("%w: limits must not be negative", ErrEvidenceLimitExceeded)
	}
	defaults := DefaultEvidenceLimits()
	if l.MaxUnitsPerArtifact == 0 {
		l.MaxUnitsPerArtifact = defaults.MaxUnitsPerArtifact
	}
	if l.MaxBytesPerUnit == 0 {
		l.MaxBytesPerUnit = defaults.MaxBytesPerUnit
	}
	if l.MaxCharactersPerUnit == 0 {
		l.MaxCharactersPerUnit = defaults.MaxCharactersPerUnit
	}
	return l, nil
}

// Validate checks configured evidence limits without starting a run.
func (l EvidenceLimits) Validate() error {
	_, err := l.normalized()
	return err
}

// EvidenceConfig explicitly scopes one evidence-producing execution.
// OrganizationID is never inferred from a path or source name.
type EvidenceConfig struct {
	OrganizationID string          `json:"organization_id"`
	Limits         EvidenceLimits  `json:"limits"`
	Policy         evidence.Policy `json:"policy,omitempty"`
}

// EvidenceOptions is a compatibility spelling for EvidenceConfig.
type EvidenceOptions = EvidenceConfig

func (c EvidenceConfig) normalized() (EvidenceConfig, error) {
	if err := validateEvidenceIdentifier("organization id", c.OrganizationID); err != nil {
		return EvidenceConfig{}, err
	}
	limits, err := c.Limits.normalized()
	if err != nil {
		return EvidenceConfig{}, err
	}
	if c.Policy.IsZero() {
		c.Policy = evidence.DefaultPolicy()
	} else if err := c.Policy.Validate(); err != nil {
		return EvidenceConfig{}, fmt.Errorf("%w: policy: %v", ErrInvalidEvidence, err)
	}
	c.OrganizationID = strings.TrimSpace(c.OrganizationID)
	c.Limits = limits
	return c, nil
}

// Validate checks the explicit evidence scope and its limits.
func (c EvidenceConfig) Validate() error {
	_, err := c.normalized()
	return err
}

// EvidenceInput is the analyzer-facing switch for an evidence run. It is
// intentionally separate from EvidenceConfig so analyzers never receive the
// organization boundary as an implicit path-derived value.
type EvidenceInput struct {
	Enabled bool
	Limits  EvidenceLimits
}

// EvidenceDraft is an in-memory analyzer observation awaiting final scope,
// sanitization, deterministic identity, and Evidence Unit validation.
// Drafts never cross the runner API or get serialized as analyzer output.
type EvidenceDraft struct {
	ContributionID  string
	Locator         contract.Locator
	Content         string
	State           evidence.ContentState
	Classification  evidence.Classification
	Findings        []string
	OriginalHash    string
	RedactionReason string
	Truncated       bool
}

// AnalysisResult keeps the established v1alpha1 result and the additive
// Evidence Units produced after the final snapshot identity is known.
type AnalysisResult struct {
	Result   contract.Result         `json:"result"`
	Evidence []evidence.EvidenceUnit `json:"evidence"`
}

// RunResult is a readable alias for AnalysisResult.
type RunResult = AnalysisResult

// Validate checks the final result and every Evidence Unit's scope and
// contribution reference without performing I/O.
func (r AnalysisResult) Validate() error {
	if err := r.Result.Validate(); err != nil {
		return fmt.Errorf("%w: result: %v", ErrInvalidEvidence, err)
	}
	artifacts := make(map[string]contract.Artifact, len(r.Result.Artifacts))
	for _, artifact := range r.Result.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	contributions := make(map[string]contract.Contribution, len(r.Result.Contributions))
	for _, contribution := range r.Result.Contributions {
		contributions[contribution.ID] = contribution
	}
	seen := make(map[string]struct{}, len(r.Evidence))
	organizationID := ""
	for index, unit := range r.Evidence {
		if err := unit.Validate(); err != nil {
			return fmt.Errorf("%w: unit %d: %v", ErrInvalidEvidence, index, err)
		}
		if unit.SourceID != r.Result.Manifest.Source.ID || unit.SnapshotID != r.Result.Manifest.Snapshot.ID {
			return fmt.Errorf("%w: unit %q is outside result scope", ErrInvalidEvidence, unit.ID)
		}
		if organizationID == "" {
			organizationID = unit.OrganizationID
		} else if organizationID != unit.OrganizationID {
			return fmt.Errorf("%w: unit %q has an inconsistent organization", ErrInvalidEvidence, unit.ID)
		}
		artifact, ok := artifacts[unit.ArtifactID]
		if !ok || artifact.SourceID != unit.SourceID {
			return fmt.Errorf("%w: unit %q references an unknown artifact", ErrInvalidEvidence, unit.ID)
		}
		contribution, ok := contributions[unit.Contribution.ID]
		if !ok || unit.ArtifactID != contribution.ArtifactID || contribution.ArtifactID != unit.Contribution.ArtifactID ||
			contribution.AnalyzerID != unit.Contribution.AnalyzerID ||
			contribution.AnalyzerVersion != unit.Contribution.AnalyzerVersion ||
			contribution.Method != unit.Contribution.Method {
			return fmt.Errorf("%w: unit %q references an inconsistent contribution", ErrInvalidEvidence, unit.ID)
		}
		if _, duplicate := seen[unit.ID]; duplicate {
			return fmt.Errorf("%w: duplicate unit %q", ErrInvalidEvidence, unit.ID)
		}
		seen[unit.ID] = struct{}{}
	}
	return nil
}

// RunWithEvidence executes the established runner and materializes additive
// Evidence Units only after the final artifact snapshot has been computed.
// The optional configuration is explicit; when omitted, OrganizationID and
// EvidenceLimits are read from Config for the convenience form.
func (r *Runner) RunWithEvidence(ctx context.Context, config Config, options ...EvidenceConfig) (AnalysisResult, error) {
	if len(options) > 1 {
		return AnalysisResult{}, fmt.Errorf("%w: at most one evidence configuration is allowed", ErrInvalidEvidence)
	}
	evidenceConfig := EvidenceConfig{
		OrganizationID: config.OrganizationID,
		Limits:         config.EvidenceLimits,
	}
	if len(options) == 1 {
		evidenceConfig = options[0]
	}
	normalized, err := evidenceConfig.normalized()
	if err != nil {
		return AnalysisResult{}, err
	}
	capture := &evidenceCapture{config: normalized}
	config.evidenceCapture = capture
	result, runErr := r.Run(ctx, config)
	if runErr != nil && result.Manifest.Source.ID == "" {
		return AnalysisResult{Result: result}, runErr
	}
	units, observations, materializeErr := materializeEvidence(result, capture.drafts, normalized)
	if materializeErr != nil {
		wrapped := fmt.Errorf("%w: materialize evidence: %v", ErrInvalidEvidence, materializeErr)
		if runErr != nil {
			wrapped = errors.Join(runErr, wrapped)
		}
		return AnalysisResult{Result: result}, wrapped
	}
	appendEvidenceObservability(&result, observations)
	if err := result.Normalize(); err != nil {
		wrapped := fmt.Errorf("%w: normalize evidence result: %v", ErrInvalidEvidence, err)
		if runErr != nil {
			wrapped = errors.Join(runErr, wrapped)
		}
		return AnalysisResult{Result: result, Evidence: units}, wrapped
	}
	analysisResult := AnalysisResult{Result: result, Evidence: units}
	if err := analysisResult.Validate(); err != nil {
		if runErr != nil {
			return analysisResult, errors.Join(runErr, err)
		}
		return analysisResult, err
	}
	if runErr != nil {
		return analysisResult, runErr
	}
	return analysisResult, nil
}

// AnalyzeWithEvidence is the domain-oriented spelling of RunWithEvidence.
func (r *Runner) AnalyzeWithEvidence(ctx context.Context, config Config, options ...EvidenceConfig) (AnalysisResult, error) {
	return r.RunWithEvidence(ctx, config, options...)
}

// RunWithEvidence is the package-level convenience wrapper corresponding to
// Runner.RunWithEvidence.
func RunWithEvidence(ctx context.Context, registry *Registry, config Config, options ...EvidenceConfig) (AnalysisResult, error) {
	runner, err := NewRunner(registry)
	if err != nil {
		return AnalysisResult{}, err
	}
	return runner.RunWithEvidence(ctx, config, options...)
}

func evidenceLimits(config Config) EvidenceLimits {
	if config.evidenceCapture == nil {
		return EvidenceLimits{}
	}
	return config.evidenceCapture.config.Limits
}

func appendEvidenceObservability(result *contract.Result, observations []evidenceObservation) {
	if result == nil || len(observations) == 0 {
		return
	}
	type artifactState struct {
		path      string
		truncated bool
		redacted  bool
		omitted   bool
	}
	states := make(map[string]artifactState)
	for _, observation := range observations {
		state := states[observation.ArtifactID]
		state.path = observation.Path
		state.truncated = state.truncated || observation.Truncated
		state.redacted = state.redacted || observation.Redacted
		state.omitted = state.omitted || observation.Omitted
		states[observation.ArtifactID] = state
	}
	limited := false
	redacted := false
	for artifactID, state := range states {
		scope := state.path + "::evidence"
		coverageState := contract.CoverageProduced
		if state.truncated || state.omitted {
			coverageState = contract.CoverageIncomplete
			limited = true
		}
		if state.redacted {
			redacted = true
		}
		result.Manifest.Coverage = append(result.Manifest.Coverage, contract.Coverage{
			Dimension: string(contract.DimensionEvidenceAndGaps),
			Scope:     scope,
			State:     coverageState,
			Message:   "bounded evidence materialization completed",
			Locator: &contract.Locator{
				SourceID:   result.Manifest.Source.ID,
				ArtifactID: artifactID,
				Path:       state.path,
			},
		})
		if state.truncated || state.omitted {
			result.Manifest.Gaps = append(result.Manifest.Gaps, contract.Gap{
				Code:      "evidence_limited",
				Dimension: string(contract.DimensionEvidenceAndGaps),
				Scope:     scope,
				Message:   "evidence was bounded or omitted by configured limits",
				Locator: &contract.Locator{
					SourceID:   result.Manifest.Source.ID,
					ArtifactID: artifactID,
					Path:       state.path,
				},
			})
		}
		if state.redacted {
			result.Manifest.Gaps = append(result.Manifest.Gaps, contract.Gap{
				Code:      "evidence_redacted",
				Dimension: string(contract.DimensionEvidenceAndGaps),
				Scope:     scope,
				Message:   "evidence content was conservatively redacted",
				Locator: &contract.Locator{
					SourceID:   result.Manifest.Source.ID,
					ArtifactID: artifactID,
					Path:       state.path,
				},
			})
		}
	}
	if limited {
		if result.Manifest.Execution.Metrics.Limited < 1 {
			result.Manifest.Execution.Metrics.Limited = 1
		}
		result.Manifest.Execution.Metrics.Limitations = append(result.Manifest.Execution.Metrics.Limitations, "evidence_limits_reached")
	}
	if redacted {
		result.Manifest.Execution.Metrics.Limitations = append(result.Manifest.Execution.Metrics.Limitations, "evidence_redacted")
	}
	result.Manifest.Execution.Metrics.Limitations = uniqueStrings(result.Manifest.Execution.Metrics.Limitations)
}

type evidenceCapture struct {
	config EvidenceConfig
	drafts []EvidenceDraft
}

type evidenceObservation struct {
	ArtifactID string
	Path       string
	Truncated  bool
	Redacted   bool
	Omitted    bool
}

// materializeEvidence converts normalized drafts into valid Evidence Units.
// It is called only after Runner.Run has computed and normalized the final
// artifact snapshot, so no analyzer can guess a snapshot identity.
func materializeEvidence(result contract.Result, drafts []EvidenceDraft, config EvidenceConfig) ([]evidence.EvidenceUnit, []evidenceObservation, error) {
	limits, err := config.Limits.normalized()
	if err != nil {
		return nil, nil, err
	}
	contributions := make(map[string]contract.Contribution, len(result.Contributions))
	artifacts := make(map[string]contract.Artifact, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, contribution := range result.Contributions {
		contributions[contribution.ID] = contribution
	}
	sortedDrafts := append([]EvidenceDraft(nil), drafts...)
	sort.SliceStable(sortedDrafts, func(i, j int) bool {
		left, right := sortedDrafts[i], sortedDrafts[j]
		if left.Locator.ArtifactID != right.Locator.ArtifactID {
			return left.Locator.ArtifactID < right.Locator.ArtifactID
		}
		if left.ContributionID != right.ContributionID {
			return left.ContributionID < right.ContributionID
		}
		if left.Locator.Member != right.Locator.Member {
			return left.Locator.Member < right.Locator.Member
		}
		if left.Locator.StartLine != right.Locator.StartLine {
			return left.Locator.StartLine < right.Locator.StartLine
		}
		return left.Content < right.Content
	})

	units := make([]evidence.EvidenceUnit, 0, len(sortedDrafts))
	observations := make([]evidenceObservation, 0, len(sortedDrafts))
	counts := make(map[string]int)
	for index, draft := range sortedDrafts {
		contribution, ok := contributions[draft.ContributionID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: draft %d references unknown contribution", ErrInvalidEvidence, index)
		}
		artifact, ok := artifacts[contribution.ArtifactID]
		if !ok {
			return nil, nil, fmt.Errorf("%w: draft %d references unknown artifact", ErrInvalidEvidence, index)
		}
		if counts[artifact.ID] >= limits.MaxUnitsPerArtifact {
			observations = append(observations, evidenceObservation{ArtifactID: artifact.ID, Path: artifact.Path, Omitted: true})
			continue
		}
		unit, observation, err := materializeDraft(result, artifact, contribution, draft, limits, config.OrganizationID, config.Policy)
		if err != nil {
			return nil, nil, err
		}
		counts[artifact.ID]++
		units = append(units, unit)
		observations = append(observations, observation)
	}
	sort.SliceStable(units, func(i, j int) bool { return units[i].ID < units[j].ID })
	return units, observations, nil
}

func materializeDraft(result contract.Result, artifact contract.Artifact, contribution contract.Contribution, draft EvidenceDraft, limits EvidenceLimits, organizationID string, policy evidence.Policy) (evidence.EvidenceUnit, evidenceObservation, error) {
	locator := draft.Locator
	if locator.SourceID == "" {
		locator.SourceID = result.Manifest.Source.ID
	}
	if locator.ArtifactID == "" {
		locator.ArtifactID = artifact.ID
	}
	if locator.Path == "" {
		locator.Path = artifact.Path
	}
	if locator.SourceID != result.Manifest.Source.ID || locator.ArtifactID != artifact.ID || locator.Path != artifact.Path {
		return evidence.EvidenceUnit{}, evidenceObservation{}, fmt.Errorf("%w: draft locator is outside artifact scope", ErrInvalidEvidence)
	}
	if err := locator.Validate(); err != nil {
		return evidence.EvidenceUnit{}, evidenceObservation{}, fmt.Errorf("%w: draft locator: %v", ErrInvalidEvidence, err)
	}

	originalContent := draft.Content
	inspection := evidence.InspectContent(originalContent)
	originalHash := draft.OriginalHash
	if originalHash == "" && originalContent != "" {
		originalHash = inspection.OriginalHash
	}
	content := inspection.Content
	state := draft.State
	reason := draft.RedactionReason
	classification := draft.Classification
	findings := append([]string(nil), draft.Findings...)
	if classification == evidence.ClassificationUnknown {
		classification = inspection.Classification
	}
	if inspection.Classification != evidence.ClassificationSafeText && inspection.Classification != evidence.ClassificationUnknown {
		classification = inspection.Classification
	}
	findings = append(findings, inspection.Findings...)
	if state == evidence.ContentStateUnknown {
		state = inspection.State
	}
	redacted := inspection.Redacted || state == evidence.ContentStateRedacted
	if state == evidence.ContentStateRedacted {
		if content == "" {
			content = evidence.RedactedContent
		}
		if reason == "" {
			reason = inspection.RedactionReason
		}
		if reason == "" {
			reason = "redacted-content"
		}
		if originalHash == "" {
			originalHash = artifact.Hash
		}
		redacted = true
	}
	if state == evidence.ContentStateOmitted && classification == evidence.ClassificationSafeText {
		if strings.EqualFold(artifact.Kind, "binary") || strings.EqualFold(artifact.Type, "binary") {
			classification = evidence.ClassificationBinary
			findings = append(findings, evidence.FindingBinary)
		}
	}
	if classification == evidence.ClassificationUnknown {
		classification = evidence.ClassificationSafeText
	}
	if originalHash == "" {
		originalHash = artifact.Hash
	}
	truncated := draft.Truncated
	if state == evidence.ContentStatePresent || state == evidence.ContentStateRedacted {
		var limited bool
		content, limited = truncateEvidenceText(content, limits.MaxBytesPerUnit, limits.MaxCharactersPerUnit)
		truncated = truncated || limited
	}
	omitted := state == evidence.ContentStateOmitted
	if state == evidence.ContentStatePresent && content == "" {
		// A byte limit can fall inside a multi-byte rune. Retaining an empty
		// present value would violate the Evidence Unit invariant, so record a
		// bounded omission while preserving the origin hash.
		state = evidence.ContentStateOmitted
		omitted = true
	}
	if omitted {
		content = ""
	}
	contentHash := originalHash
	if state == evidence.ContentStatePresent {
		contentHash = evidence.ContentDigest(content)
	}
	if contentHash == "" {
		contentHash = artifact.Hash
	}
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: organizationID,
		SourceID:       result.Manifest.Source.ID,
		SnapshotID:     result.Manifest.Snapshot.ID,
		ArtifactID:     artifact.ID,
		Contribution: evidence.ContributionRef{
			ID:              contribution.ID,
			ArtifactID:      contribution.ArtifactID,
			AnalyzerID:      contribution.AnalyzerID,
			AnalyzerVersion: contribution.AnalyzerVersion,
			Method:          contribution.Method,
		},
		Locator:           locator,
		ContentState:      state,
		Content:           content,
		ContentHash:       contentHash,
		Truncated:         truncated,
		RedactionReason:   reason,
		ContentBytes:      int64(len([]byte(content))),
		ContentCharacters: int64(utf8.RuneCountInString(content)),
		Persist:           evidence.DecisionAllow,
		ExternalTransfer:  evidence.DecisionDeny,
		Classification:    classification,
		Findings:          uniqueEvidenceFindings(findings),
	}
	prepared, prepareErr := evidence.PrepareForPersistence(unit, policy)
	if prepareErr != nil {
		return evidence.EvidenceUnit{}, evidenceObservation{}, fmt.Errorf("%w: prepare policy: %w", ErrInvalidEvidence, prepareErr)
	}
	unit = prepared
	if unit.ContentState == evidence.ContentStateRedacted && exceedsEvidenceLimits(unit.Content, limits) {
		unit.ContentState = evidence.ContentStateOmitted
		unit.Content = ""
		unit.ContentBytes = 0
		unit.ContentCharacters = 0
		unit.RedactionReason = "policy-deny"
		unit.ID = evidence.EvidenceID(unit)
	}
	unitLimits := evidence.UnitLimits{MaxBytes: limits.MaxBytesPerUnit, MaxCharacters: limits.MaxCharactersPerUnit}
	if err := unit.ValidateWithLimits(unitLimits); err != nil {
		return evidence.EvidenceUnit{}, evidenceObservation{}, fmt.Errorf("%w: materialized unit: %v", ErrInvalidEvidence, err)
	}
	return unit, evidenceObservation{
		ArtifactID: artifact.ID,
		Path:       artifact.Path,
		Truncated:  unit.Truncated,
		Redacted:   redacted || unit.ContentState == evidence.ContentStateRedacted,
		Omitted:    unit.ContentState == evidence.ContentStateOmitted,
	}, nil
}

// SanitizeEvidenceContent applies a conservative allow-by-default check to
// retained text. Suspected credentials become a fixed placeholder and never
// appear in a contribution, Evidence Unit, or diagnostic.
func SanitizeEvidenceContent(content string) SanitizedEvidence {
	sanitized := evidence.SanitizeEvidenceContent(content)
	return SanitizedEvidence{Content: sanitized.Content, Redacted: sanitized.Redacted, Reason: sanitized.RedactionReason}
}

// SanitizedEvidence is the safe result of SanitizeEvidenceContent.
type SanitizedEvidence struct {
	Content  string
	Redacted bool
	Reason   string
}

func uniqueEvidenceFindings(findings []string) []string {
	if len(findings) == 0 {
		return nil
	}
	ordered := append([]string(nil), findings...)
	sort.Strings(ordered)
	result := ordered[:0]
	for _, finding := range ordered {
		if finding == "" || (len(result) > 0 && result[len(result)-1] == finding) {
			continue
		}
		result = append(result, finding)
	}
	return result
}

func exceedsEvidenceLimits(content string, limits EvidenceLimits) bool {
	if limits.MaxBytesPerUnit > 0 && int64(len([]byte(content))) > limits.MaxBytesPerUnit {
		return true
	}
	return limits.MaxCharactersPerUnit > 0 && int64(utf8.RuneCountInString(content)) > limits.MaxCharactersPerUnit
}

func truncateEvidenceText(content string, maxBytes, maxCharacters int64) (string, bool) {
	if content == "" {
		return content, false
	}
	limit := len([]byte(content))
	if maxBytes > 0 && int64(limit) > maxBytes {
		limit = int(maxBytes)
	}
	if maxCharacters > 0 {
		characters := utf8.RuneCountInString(content)
		if int64(characters) > maxCharacters {
			byteLimit := 0
			seen := int64(0)
			for index := range content {
				if seen == maxCharacters {
					byteLimit = index
					break
				}
				seen++
			}
			if byteLimit == 0 {
				byteLimit = len(content)
			}
			if byteLimit < limit {
				limit = byteLimit
			}
		}
	}
	for limit > 0 && !utf8.ValidString(content[:limit]) {
		limit--
	}
	if limit == len([]byte(content)) {
		return content, false
	}
	return content[:limit], true
}

func validateEvidenceIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidEvidence, name)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains whitespace or control characters", ErrInvalidEvidence, name)
		}
	}
	return nil
}
