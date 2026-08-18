// Package contract contains the versioned result model shared by the Manu
// runtime and its command-line interface.
package contract

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	// Version is the version of the structured result contract.
	Version = "v1alpha1"

	// ContractVersion is kept as an explicit alias for callers that use the
	// terminology from the result envelope.
	ContractVersion = Version
)

// CoverageState describes the result of attempting one understanding
// dimension. The zero value is invalid and must not be serialized as a real
// state.
type CoverageState string

const (
	// CoverageUnknown is the zero value and is not a valid result state.
	CoverageUnknown CoverageState = ""
	// CoverageProduced means that the attempted dimension produced a result.
	CoverageProduced CoverageState = "produced"
	// CoverageIncomplete means that only part of the attempted dimension was
	// reconstructed.
	CoverageIncomplete CoverageState = "incomplete"
	// CoverageNotSupported means that the implementation does not support the
	// attempted dimension.
	CoverageNotSupported CoverageState = "not_supported"
	// CoverageNotApplicable means that the dimension does not apply to the
	// artifact or source in this execution.
	CoverageNotApplicable CoverageState = "not_applicable"
	// CoverageFailed means that the attempt failed after work was started.
	CoverageFailed CoverageState = "failed"

	// CoverageUnsupported is a readable compatibility alias.
	CoverageUnsupported = CoverageNotSupported
)

// CoverageStatus is an alias retained for callers that use status terminology.
type CoverageStatus = CoverageState

// Dimension names the semantic area covered by an analysis. Dimensions are
// intentionally strings so a future analyzer can add a value without a
// contract migration; the standard values below cover the first microcut.
type Dimension string

const (
	DimensionLandscapeInventoryStructure Dimension = "landscape_inventory_structure"
	DimensionEntitiesAndRelationships    Dimension = "entities_and_relationships"
	DimensionFlowsAndDependencies        Dimension = "flows_and_dependencies"
	DimensionDecisionsAndDataOrigins     Dimension = "decisions_conditions_data_origins"
	DimensionConfigurationVariations     Dimension = "configuration_variations"
	DimensionCapabilities                Dimension = "capabilities"
	DimensionErrorsAndPossibleFlows      Dimension = "errors_and_possible_flows"
	DimensionEvolution                   Dimension = "evolution"
	DimensionDocumentation               Dimension = "documentation"
	DimensionEvidenceAndGaps             Dimension = "evidence_provenance_uncertainty_gaps"
)

// Source identifies an authorized origin of artifacts.
type Source struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Revision string `json:"revision,omitempty"`
	Hash     string `json:"hash,omitempty"`
	Root     string `json:"root,omitempty"`
}

// Snapshot identifies the source state observed by an analysis.
type Snapshot struct {
	ID         string    `json:"id"`
	SourceID   string    `json:"source_id"`
	Revision   string    `json:"revision,omitempty"`
	Hash       string    `json:"hash,omitempty"`
	CapturedAt time.Time `json:"captured_at,omitempty"`
}

// MarshalJSON omits an unset capture time. encoding/json does not apply
// omitempty to time.Time because it is a struct.
func (s Snapshot) MarshalJSON() ([]byte, error) {
	type snapshotJSON struct {
		ID         string     `json:"id"`
		SourceID   string     `json:"source_id"`
		Revision   string     `json:"revision,omitempty"`
		Hash       string     `json:"hash,omitempty"`
		CapturedAt *time.Time `json:"captured_at,omitempty"`
	}
	var capturedAt *time.Time
	if !s.CapturedAt.IsZero() {
		captured := s.CapturedAt
		capturedAt = &captured
	}
	return json.Marshal(snapshotJSON{
		ID:         s.ID,
		SourceID:   s.SourceID,
		Revision:   s.Revision,
		Hash:       s.Hash,
		CapturedAt: capturedAt,
	})
}

// UnmarshalJSON reads a snapshot while preserving an absent capture time as
// the zero value.
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	type snapshotJSON struct {
		ID         string     `json:"id"`
		SourceID   string     `json:"source_id"`
		Revision   string     `json:"revision,omitempty"`
		Hash       string     `json:"hash,omitempty"`
		CapturedAt *time.Time `json:"captured_at,omitempty"`
	}
	var decoded snapshotJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	s.ID = decoded.ID
	s.SourceID = decoded.SourceID
	s.Revision = decoded.Revision
	s.Hash = decoded.Hash
	s.CapturedAt = time.Time{}
	if decoded.CapturedAt != nil {
		s.CapturedAt = *decoded.CapturedAt
	}
	return nil
}

// AnalysisSnapshot is the domain spelling used by the architecture documents.
type AnalysisSnapshot = Snapshot

// Artifact is a concrete item discovered in a Source.
type Artifact struct {
	ID       string `json:"id"`
	SourceID string `json:"source_id"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Hash     string `json:"hash"`
	Size     int64  `json:"size,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// Analyzer identifies the implementation and method that produced a
// contribution.
type Analyzer struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Method  string `json:"method"`
}

// Contribution is an observable fact produced for an Artifact. Value holds
// analyzer-specific structured data while the surrounding fields preserve its
// common provenance.
type Contribution struct {
	ID              string          `json:"id"`
	ArtifactID      string          `json:"artifact_id"`
	AnalyzerID      string          `json:"analyzer_id"`
	AnalyzerVersion string          `json:"analyzer_version"`
	Method          string          `json:"method"`
	Type            string          `json:"type"`
	Locator         Locator         `json:"locator"`
	ObservedAt      time.Time       `json:"observed_at,omitempty"`
	Value           json.RawMessage `json:"value,omitempty"`
}

// MarshalJSON omits an unset observation time while preserving the public
// time.Time field used by analyzers.
func (c Contribution) MarshalJSON() ([]byte, error) {
	type contributionJSON struct {
		ID              string          `json:"id"`
		ArtifactID      string          `json:"artifact_id"`
		AnalyzerID      string          `json:"analyzer_id"`
		AnalyzerVersion string          `json:"analyzer_version"`
		Method          string          `json:"method"`
		Type            string          `json:"type"`
		Locator         Locator         `json:"locator"`
		ObservedAt      *time.Time      `json:"observed_at,omitempty"`
		Value           json.RawMessage `json:"value,omitempty"`
	}
	var observedAt *time.Time
	if !c.ObservedAt.IsZero() {
		observed := c.ObservedAt
		observedAt = &observed
	}
	return json.Marshal(contributionJSON{
		ID:              c.ID,
		ArtifactID:      c.ArtifactID,
		AnalyzerID:      c.AnalyzerID,
		AnalyzerVersion: c.AnalyzerVersion,
		Method:          c.Method,
		Type:            c.Type,
		Locator:         c.Locator,
		ObservedAt:      observedAt,
		Value:           c.Value,
	})
}

// UnmarshalJSON reads a contribution while preserving an absent observation
// time as the zero value.
func (c *Contribution) UnmarshalJSON(data []byte) error {
	type contributionJSON struct {
		ID              string          `json:"id"`
		ArtifactID      string          `json:"artifact_id"`
		AnalyzerID      string          `json:"analyzer_id"`
		AnalyzerVersion string          `json:"analyzer_version"`
		Method          string          `json:"method"`
		Type            string          `json:"type"`
		Locator         Locator         `json:"locator"`
		ObservedAt      *time.Time      `json:"observed_at,omitempty"`
		Value           json.RawMessage `json:"value,omitempty"`
	}
	var decoded contributionJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	c.ID = decoded.ID
	c.ArtifactID = decoded.ArtifactID
	c.AnalyzerID = decoded.AnalyzerID
	c.AnalyzerVersion = decoded.AnalyzerVersion
	c.Method = decoded.Method
	c.Type = decoded.Type
	c.Locator = decoded.Locator
	c.ObservedAt = time.Time{}
	if decoded.ObservedAt != nil {
		c.ObservedAt = *decoded.ObservedAt
	}
	c.Value = decoded.Value
	return nil
}

// Evidence points to material that supports or contests a contribution or
// another result. A locator is retained even when the evidence text is not.
type Evidence struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Locator    Locator `json:"locator"`
	Excerpt    string  `json:"excerpt,omitempty"`
	Provenance string  `json:"provenance,omitempty"`
}

// Locator identifies a source position without requiring source contents to
// be copied into the result.
type Locator struct {
	URI         string `json:"uri,omitempty"`
	SourceID    string `json:"source_id,omitempty"`
	ArtifactID  string `json:"artifact_id,omitempty"`
	Path        string `json:"path,omitempty"`
	Member      string `json:"member,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	StartColumn int    `json:"start_column,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	EndColumn   int    `json:"end_column,omitempty"`
	ByteOffset  int64  `json:"byte_offset,omitempty"`
	ByteLength  int64  `json:"byte_length,omitempty"`
}

// Coverage records the state of one understanding dimension for one scope.
type Coverage struct {
	ID         string        `json:"id"`
	Dimension  string        `json:"dimension"`
	Scope      string        `json:"scope,omitempty"`
	State      CoverageState `json:"state"`
	AnalyzerID string        `json:"analyzer_id,omitempty"`
	Message    string        `json:"message,omitempty"`
	Locator    *Locator      `json:"locator,omitempty"`
}

// AnalysisCoverage is an alias for callers using the domain term.
type AnalysisCoverage = Coverage

// Gap records a material absence of support or knowledge.
type Gap struct {
	ID         string   `json:"id"`
	Code       string   `json:"code"`
	Dimension  string   `json:"dimension,omitempty"`
	Scope      string   `json:"scope,omitempty"`
	Message    string   `json:"message"`
	AnalyzerID string   `json:"analyzer_id,omitempty"`
	Locator    *Locator `json:"locator,omitempty"`
}

// ExplicitGap is an alias for the domain term.
type ExplicitGap = Gap

// Failure records a technical or analyzer failure while preserving valid
// contributions produced before the failure.
type Failure struct {
	ID         string   `json:"id"`
	Code       string   `json:"code"`
	Operation  string   `json:"operation"`
	Message    string   `json:"message"`
	ArtifactID string   `json:"artifact_id,omitempty"`
	AnalyzerID string   `json:"analyzer_id,omitempty"`
	Partial    bool     `json:"partial,omitempty"`
	Locator    *Locator `json:"locator,omitempty"`
}

// ExecutionMetrics contains run-specific counters. These fields are
// deliberately kept separate from factual result identity and are ignored by
// EquivalentFacts.
type ExecutionMetrics struct {
	Discovered  int      `json:"discovered,omitempty"`
	Reused      int      `json:"reused,omitempty"`
	Reprocessed int      `json:"reprocessed,omitempty"`
	Limited     int      `json:"limited,omitempty"`
	Failed      int      `json:"failed,omitempty"`
	Limitations []string `json:"limitations,omitempty"`
}

// IsZero reports whether no execution counters or limitations were recorded.
func (m ExecutionMetrics) IsZero() bool {
	return m.Discovered == 0 && m.Reused == 0 && m.Reprocessed == 0 &&
		m.Limited == 0 && m.Failed == 0 && len(m.Limitations) == 0
}

// ExecutionMetadata contains run-specific information. These fields are
// deliberately kept separate from factual result identity and are ignored by
// EquivalentFacts.
type ExecutionMetadata struct {
	RunID           string           `json:"run_id"`
	StartedAt       time.Time        `json:"started_at,omitempty"`
	FinishedAt      time.Time        `json:"finished_at,omitempty"`
	ToolVersion     string           `json:"tool_version,omitempty"`
	GoVersion       string           `json:"go_version,omitempty"`
	Host            string           `json:"host,omitempty"`
	ConfigurationID string           `json:"configuration_id,omitempty"`
	Cancelled       bool             `json:"cancelled,omitempty"`
	Metrics         ExecutionMetrics `json:"metrics,omitempty"`
}

// MarshalJSON omits unset execution timestamps from compact manifests.
func (e ExecutionMetadata) MarshalJSON() ([]byte, error) {
	type executionJSON struct {
		RunID           string            `json:"run_id"`
		StartedAt       *time.Time        `json:"started_at,omitempty"`
		FinishedAt      *time.Time        `json:"finished_at,omitempty"`
		ToolVersion     string            `json:"tool_version,omitempty"`
		GoVersion       string            `json:"go_version,omitempty"`
		Host            string            `json:"host,omitempty"`
		ConfigurationID string            `json:"configuration_id,omitempty"`
		Cancelled       bool              `json:"cancelled,omitempty"`
		Metrics         *ExecutionMetrics `json:"metrics,omitempty"`
	}
	var startedAt, finishedAt *time.Time
	if !e.StartedAt.IsZero() {
		started := e.StartedAt
		startedAt = &started
	}
	if !e.FinishedAt.IsZero() {
		finished := e.FinishedAt
		finishedAt = &finished
	}
	var metrics *ExecutionMetrics
	if !e.Metrics.IsZero() {
		copyOfMetrics := e.Metrics
		copyOfMetrics.Limitations = append([]string(nil), e.Metrics.Limitations...)
		metrics = &copyOfMetrics
	}
	return json.Marshal(executionJSON{
		RunID:           e.RunID,
		StartedAt:       startedAt,
		FinishedAt:      finishedAt,
		ToolVersion:     e.ToolVersion,
		GoVersion:       e.GoVersion,
		Host:            e.Host,
		ConfigurationID: e.ConfigurationID,
		Cancelled:       e.Cancelled,
		Metrics:         metrics,
	})
}

// UnmarshalJSON reads execution metadata while preserving absent timestamps.
func (e *ExecutionMetadata) UnmarshalJSON(data []byte) error {
	type executionJSON struct {
		RunID           string            `json:"run_id"`
		StartedAt       *time.Time        `json:"started_at,omitempty"`
		FinishedAt      *time.Time        `json:"finished_at,omitempty"`
		ToolVersion     string            `json:"tool_version,omitempty"`
		GoVersion       string            `json:"go_version,omitempty"`
		Host            string            `json:"host,omitempty"`
		ConfigurationID string            `json:"configuration_id,omitempty"`
		Cancelled       bool              `json:"cancelled,omitempty"`
		Metrics         *ExecutionMetrics `json:"metrics,omitempty"`
	}
	var decoded executionJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	e.RunID = decoded.RunID
	e.StartedAt = time.Time{}
	e.FinishedAt = time.Time{}
	if decoded.StartedAt != nil {
		e.StartedAt = *decoded.StartedAt
	}
	if decoded.FinishedAt != nil {
		e.FinishedAt = *decoded.FinishedAt
	}
	e.ToolVersion = decoded.ToolVersion
	e.GoVersion = decoded.GoVersion
	e.Host = decoded.Host
	e.ConfigurationID = decoded.ConfigurationID
	e.Cancelled = decoded.Cancelled
	e.Metrics = ExecutionMetrics{}
	if decoded.Metrics != nil {
		e.Metrics = *decoded.Metrics
		e.Metrics.Limitations = append([]string(nil), decoded.Metrics.Limitations...)
	}
	return nil
}

// Manifest is the small, independently readable part of a result. Artifacts
// and contributions are stored in sequences beside it.
type Manifest struct {
	ContractVersion   string            `json:"contract_version"`
	ResultID          string            `json:"result_id"`
	Source            Source            `json:"source"`
	Snapshot          Snapshot          `json:"snapshot"`
	Execution         ExecutionMetadata `json:"execution"`
	ArtifactCount     int               `json:"artifact_count"`
	ContributionCount int               `json:"contribution_count"`
	Coverage          []Coverage        `json:"coverage"`
	Gaps              []Gap             `json:"gaps"`
	Failures          []Failure         `json:"failures"`
}

// Result is the in-process representation of one analysis output.
type Result struct {
	Manifest      Manifest       `json:"manifest"`
	Artifacts     []Artifact     `json:"artifacts"`
	Contributions []Contribution `json:"contributions"`
}

// AnalysisResult is an alias for callers that prefer the explicit name.
type AnalysisResult = Result

// IsPartial reports whether the result contains an incomplete dimension,
// unsupported dimension, explicit gap, or failure.
func (r Result) IsPartial() bool {
	if len(r.Manifest.Gaps) > 0 || len(r.Manifest.Failures) > 0 {
		return true
	}
	if r.Manifest.Execution.Cancelled || r.Manifest.Execution.Metrics.Limited > 0 || r.Manifest.Execution.Metrics.Failed > 0 {
		return true
	}
	for _, coverage := range r.Manifest.Coverage {
		switch coverage.State {
		case CoverageProduced, CoverageNotApplicable:
			continue
		default:
			return true
		}
	}
	return false
}

// Partial reports whether the result has a partial status.
func (r Result) Partial() bool { return r.IsPartial() }

// Validate checks the structural invariants of a result without changing it.
func (r Result) Validate() error {
	if err := validateVersion(r.Manifest.ContractVersion); err != nil {
		return err
	}
	if err := r.Manifest.validate(); err != nil {
		return fmt.Errorf("validating manifest: %w", err)
	}
	if r.Manifest.ArtifactCount != len(r.Artifacts) {
		return fmt.Errorf("artifact count: manifest has %d, sequence has %d", r.Manifest.ArtifactCount, len(r.Artifacts))
	}
	if r.Manifest.ContributionCount != len(r.Contributions) {
		return fmt.Errorf("contribution count: manifest has %d, sequence has %d", r.Manifest.ContributionCount, len(r.Contributions))
	}

	seen := make(map[string]struct{}, len(r.Artifacts))
	artifactsByID := make(map[string]Artifact, len(r.Artifacts))
	for i, artifact := range r.Artifacts {
		if err := artifact.validate(); err != nil {
			return fmt.Errorf("artifact %d: %w", i, err)
		}
		if artifact.SourceID != r.Manifest.Source.ID {
			return fmt.Errorf("artifact %q source id %q does not match source %q", artifact.ID, artifact.SourceID, r.Manifest.Source.ID)
		}
		if _, exists := seen[artifact.ID]; exists {
			return fmt.Errorf("artifact %q: duplicate id", artifact.ID)
		}
		seen[artifact.ID] = struct{}{}
		artifactsByID[artifact.ID] = artifact
	}

	seen = make(map[string]struct{}, len(r.Contributions))
	for i, contribution := range r.Contributions {
		if err := contribution.validate(); err != nil {
			return fmt.Errorf("contribution %d: %w", i, err)
		}
		artifact, exists := artifactsByID[contribution.ArtifactID]
		if !exists {
			return fmt.Errorf("contribution %q references unknown artifact %q", contribution.ID, contribution.ArtifactID)
		}
		if contribution.Locator.ArtifactID != "" && contribution.Locator.ArtifactID != contribution.ArtifactID {
			return fmt.Errorf("contribution %q locator artifact id %q does not match artifact %q", contribution.ID, contribution.Locator.ArtifactID, contribution.ArtifactID)
		}
		if contribution.Locator.SourceID != "" && contribution.Locator.SourceID != artifact.SourceID {
			return fmt.Errorf("contribution %q locator source id %q does not match source %q", contribution.ID, contribution.Locator.SourceID, artifact.SourceID)
		}
		if _, exists := seen[contribution.ID]; exists {
			return fmt.Errorf("contribution %q: duplicate id", contribution.ID)
		}
		seen[contribution.ID] = struct{}{}
	}
	return nil
}

// Validate checks the structural invariants of a manifest without reading
// its artifact and contribution sequences.
func (m Manifest) Validate() error {
	if err := validateVersion(m.ContractVersion); err != nil {
		return err
	}
	return m.validate()
}

// Validate checks that a source has the identity and descriptive fields
// required by the contract.
func (s Source) Validate() error { return s.validate() }

// Validate checks that an artifact has a stable identity and content hash.
func (a Artifact) Validate() error { return a.validate() }

// Validate checks that a contribution has provenance and a locator.
func (c Contribution) Validate() error { return c.validate() }

// Validate checks that a coverage entry has a known state and dimension.
func (c Coverage) Validate() error { return c.validate() }

// Validate checks that a gap has a code and an actionable explanation.
func (g Gap) Validate() error { return g.validate() }

// Validate checks that a failure has an operation, code, and explanation.
func (f Failure) Validate() error { return f.validate() }

func (m Manifest) validate() error {
	if strings.TrimSpace(m.ResultID) == "" {
		return fmt.Errorf("result id is required")
	}
	if err := m.Source.validate(); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if strings.TrimSpace(m.Snapshot.ID) == "" {
		return fmt.Errorf("snapshot id is required")
	}
	if strings.TrimSpace(m.Snapshot.SourceID) == "" {
		return fmt.Errorf("snapshot source id is required")
	}
	if m.Snapshot.SourceID != m.Source.ID {
		return fmt.Errorf("snapshot source id %q does not match source %q", m.Snapshot.SourceID, m.Source.ID)
	}
	if err := m.Execution.validate(); err != nil {
		return fmt.Errorf("execution: %w", err)
	}
	if m.ArtifactCount < 0 || m.ContributionCount < 0 {
		return fmt.Errorf("counts must not be negative")
	}

	seen := make(map[string]struct{}, len(m.Coverage))
	for i, coverage := range m.Coverage {
		if err := coverage.validate(); err != nil {
			return fmt.Errorf("coverage %d: %w", i, err)
		}
		if coverage.ID != "" {
			if _, exists := seen[coverage.ID]; exists {
				return fmt.Errorf("coverage %q: duplicate id", coverage.ID)
			}
			seen[coverage.ID] = struct{}{}
		}
	}
	for i, gap := range m.Gaps {
		if err := gap.validate(); err != nil {
			return fmt.Errorf("gap %d: %w", i, err)
		}
	}
	for i, failure := range m.Failures {
		if err := failure.validate(); err != nil {
			return fmt.Errorf("failure %d: %w", i, err)
		}
	}
	seen = make(map[string]struct{}, len(m.Gaps))
	for _, gap := range m.Gaps {
		if gap.ID == "" {
			continue
		}
		if _, exists := seen[gap.ID]; exists {
			return fmt.Errorf("gap %q: duplicate id", gap.ID)
		}
		seen[gap.ID] = struct{}{}
	}
	seen = make(map[string]struct{}, len(m.Failures))
	for _, failure := range m.Failures {
		if failure.ID == "" {
			continue
		}
		if _, exists := seen[failure.ID]; exists {
			return fmt.Errorf("failure %q: duplicate id", failure.ID)
		}
		seen[failure.ID] = struct{}{}
	}
	return nil
}

func (s Source) validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(s.Type) == "" {
		return fmt.Errorf("type is required")
	}
	return nil
}

func (e ExecutionMetadata) validate() error {
	if strings.TrimSpace(e.RunID) == "" {
		return fmt.Errorf("run id is required")
	}
	return nil
}

func (a Artifact) validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.SourceID) == "" {
		return fmt.Errorf("id and source id are required")
	}
	if strings.TrimSpace(a.Path) == "" {
		return fmt.Errorf("path is required")
	}
	if strings.TrimSpace(a.Hash) == "" {
		return fmt.Errorf("hash is required")
	}
	if a.Size < 0 {
		return fmt.Errorf("size must not be negative")
	}
	return nil
}

func (c Contribution) validate() error {
	if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.ArtifactID) == "" {
		return fmt.Errorf("id and artifact id are required")
	}
	if strings.TrimSpace(c.AnalyzerID) == "" || strings.TrimSpace(c.AnalyzerVersion) == "" {
		return fmt.Errorf("analyzer id and version are required")
	}
	if strings.TrimSpace(c.Method) == "" || strings.TrimSpace(c.Type) == "" {
		return fmt.Errorf("method and type are required")
	}
	return c.Locator.Validate()
}

func (c Coverage) validate() error {
	if strings.TrimSpace(c.Dimension) == "" {
		return fmt.Errorf("dimension is required")
	}
	if !validCoverageState(c.State) {
		return fmt.Errorf("invalid state %q", c.State)
	}
	if c.Locator != nil {
		return c.Locator.Validate()
	}
	return nil
}

func (g Gap) validate() error {
	if strings.TrimSpace(g.Code) == "" || strings.TrimSpace(g.Message) == "" {
		return fmt.Errorf("code and message are required")
	}
	if g.Locator != nil {
		return g.Locator.Validate()
	}
	return nil
}

func (f Failure) validate() error {
	if strings.TrimSpace(f.Code) == "" || strings.TrimSpace(f.Operation) == "" || strings.TrimSpace(f.Message) == "" {
		return fmt.Errorf("code, operation, and message are required")
	}
	if f.Locator != nil {
		return f.Locator.Validate()
	}
	return nil
}

func validCoverageState(state CoverageState) bool {
	switch state {
	case CoverageProduced, CoverageIncomplete, CoverageNotSupported, CoverageNotApplicable, CoverageFailed:
		return true
	default:
		return false
	}
}

// Validate checks that a locator contains enough information to identify a
// source position. A locator may use a URI, path, member, artifact, or byte/
// line range; it must not be completely empty.
func (l Locator) Validate() error {
	if strings.TrimSpace(l.URI) == "" && strings.TrimSpace(l.SourceID) == "" &&
		strings.TrimSpace(l.ArtifactID) == "" && strings.TrimSpace(l.Path) == "" &&
		strings.TrimSpace(l.Member) == "" && l.StartLine == 0 && l.EndLine == 0 &&
		l.ByteOffset == 0 && l.ByteLength == 0 {
		return fmt.Errorf("locator must identify a uri, artifact, path, member, or position")
	}
	if l.StartLine < 0 || l.StartColumn < 0 || l.EndLine < 0 || l.EndColumn < 0 || l.ByteOffset < 0 || l.ByteLength < 0 {
		return fmt.Errorf("locator positions must not be negative")
	}
	if l.EndLine > 0 && l.StartLine > l.EndLine {
		return fmt.Errorf("locator end line precedes start line")
	}
	return nil
}
