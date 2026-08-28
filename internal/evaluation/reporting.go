package evaluation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"
)

const (
	// VariantReportingVersion identifies the raw and summary report contract.
	// It is independent from both the case-set and execution versions.
	VariantReportingVersion = "v1alpha1"
)

var (
	// ErrInvalidVariantReport identifies a malformed, incomplete, or
	// internally inconsistent report.
	ErrInvalidVariantReport = errors.New("evaluation: invalid variant report")
	// ErrVariantReportDigestMismatch identifies a digest that does not match
	// the canonical report projection it claims to identify.
	ErrVariantReportDigestMismatch = errors.New("evaluation: variant report digest mismatch")
	// ErrVariantReportLimitExceeded identifies a report above its bounded
	// serialization contract.
	ErrVariantReportLimitExceeded = errors.New("evaluation: variant report limit exceeded")
)

// EvaluationComponent identifies an agent, model, frontend, rule, context
// server, or tool. Digest is optional for generic runtime components and
// mandatory for components whose identity represents executable/indexed
// behavior (frontend, rule, and context server).
type EvaluationComponent struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest,omitempty"`
}

// Normalize validates and returns a detached component. The generic method
// permits an omitted digest; report metadata applies the stricter requirement
// where appropriate.
func (c EvaluationComponent) Normalize() (EvaluationComponent, error) {
	return normalizeEvaluationComponent(c, false)
}

// Validate checks a generic component identity.
func (c EvaluationComponent) Validate() error {
	_, err := c.Normalize()
	return err
}

func normalizeEvaluationComponent(c EvaluationComponent, requireDigest bool) (EvaluationComponent, error) {
	if c.ID == "" && c.Version == "" && c.Digest == "" {
		if requireDigest {
			return EvaluationComponent{}, ErrInvalidVariantReport
		}
		return EvaluationComponent{}, nil
	}
	if !validReportIdentity(c.ID) || !validReportIdentity(c.Version) {
		return EvaluationComponent{}, ErrInvalidVariantReport
	}
	if c.Digest == "" {
		if requireDigest {
			return EvaluationComponent{}, ErrInvalidVariantReport
		}
		return c, nil
	}
	if !isVariantSHA256(c.Digest) {
		return EvaluationComponent{}, ErrInvalidVariantReport
	}
	c.Digest = strings.ToLower(c.Digest)
	return c, nil
}

// VariantReportMetadata describes the fixed configuration surrounding one
// variant evaluation. It contains identities and safe settings only; source,
// prompts, responses, credentials, and provider diagnostics are absent.
type VariantReportMetadata struct {
	RunID           string                  `json:"run_id"`
	StartedAt       time.Time               `json:"started_at,omitempty"`
	FinishedAt      time.Time               `json:"finished_at,omitempty"`
	Agent           EvaluationComponent     `json:"agent"`
	Model           EvaluationComponent     `json:"model"`
	ContextServer   EvaluationComponent     `json:"context_server"`
	Frontends       []EvaluationComponent   `json:"frontends"`
	Rules           []EvaluationComponent   `json:"rules"`
	Retrieval       EvaluationConfiguration `json:"retrieval"`
	RetrievalDigest string                  `json:"retrieval_digest,omitempty"`
	Tools           []EvaluationComponent   `json:"tools"`
	Limitations     []string                `json:"limitations"`
}

// Clone returns a detached metadata value.
func (m VariantReportMetadata) Clone() VariantReportMetadata {
	clone := m
	clone.Frontends = append([]EvaluationComponent(nil), m.Frontends...)
	clone.Rules = append([]EvaluationComponent(nil), m.Rules...)
	clone.Tools = append([]EvaluationComponent(nil), m.Tools...)
	clone.Retrieval = cloneEvaluationConfiguration(m.Retrieval)
	clone.Retrieval.Settings = cloneCaseStringMap(m.Retrieval.Settings)
	clone.Limitations = cloneCaseStrings(m.Limitations)
	return clone
}

// Normalize validates and returns deterministic metadata. Component lists and
// limitations are sorted and exact duplicates are removed.
func (m VariantReportMetadata) Normalize() (VariantReportMetadata, error) {
	if !validReportIdentity(m.RunID) {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}
	if !m.StartedAt.IsZero() {
		m.StartedAt = m.StartedAt.UTC()
	}
	if !m.FinishedAt.IsZero() {
		m.FinishedAt = m.FinishedAt.UTC()
	}
	if !m.StartedAt.IsZero() && !m.FinishedAt.IsZero() && m.FinishedAt.Before(m.StartedAt) {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}

	var err error
	if m.Agent, err = normalizeRequiredReportComponent(m.Agent, false); err != nil {
		return VariantReportMetadata{}, err
	}
	if m.Model, err = normalizeRequiredReportComponent(m.Model, false); err != nil {
		return VariantReportMetadata{}, err
	}
	if m.ContextServer, err = normalizeRequiredReportComponent(m.ContextServer, true); err != nil {
		return VariantReportMetadata{}, err
	}
	if m.Frontends, err = normalizeReportComponents(m.Frontends, true); err != nil {
		return VariantReportMetadata{}, err
	}
	if m.Rules, err = normalizeReportComponents(m.Rules, true); err != nil {
		return VariantReportMetadata{}, err
	}
	if m.Tools, err = normalizeReportComponents(m.Tools, false); err != nil {
		return VariantReportMetadata{}, err
	}
	if len(m.Frontends) == 0 || len(m.Rules) == 0 || len(m.Tools) == 0 {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}

	if m.Retrieval.isZero() {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}
	if err := m.Retrieval.Validate(); err != nil {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}
	m.Retrieval = cloneEvaluationConfiguration(m.Retrieval)
	computed, digestErr := ConfigurationDigest(m.Retrieval)
	if digestErr != nil {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}
	if m.RetrievalDigest != "" && (!isVariantSHA256(m.RetrievalDigest) || strings.ToLower(m.RetrievalDigest) != computed) {
		return VariantReportMetadata{}, ErrVariantReportDigestMismatch
	}
	m.RetrievalDigest = computed
	m.Limitations, err = normalizeReportStrings(m.Limitations, "report limitation", maxTextBytes)
	if err != nil {
		return VariantReportMetadata{}, err
	}
	if len(m.Limitations) == 0 {
		return VariantReportMetadata{}, ErrInvalidVariantReport
	}
	return m, nil
}

// Validate checks metadata without changing the caller's value.
func (m VariantReportMetadata) Validate() error {
	_, err := m.Normalize()
	return err
}

func normalizeRequiredReportComponent(component EvaluationComponent, requireDigest bool) (EvaluationComponent, error) {
	if component.ID == "" || component.Version == "" {
		return EvaluationComponent{}, ErrInvalidVariantReport
	}
	return normalizeEvaluationComponent(component, requireDigest)
}

func normalizeReportComponents(values []EvaluationComponent, requireDigest bool) ([]EvaluationComponent, error) {
	if len(values) > maxListItems {
		return nil, ErrVariantReportLimitExceeded
	}
	if values == nil {
		return []EvaluationComponent{}, nil
	}
	normalized := make([]EvaluationComponent, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	byBase := make(map[string]string, len(values))
	for _, value := range values {
		item, err := normalizeEvaluationComponent(value, requireDigest)
		if err != nil {
			return nil, err
		}
		if item.ID == "" {
			return nil, ErrInvalidVariantReport
		}
		key := componentKey(item)
		base := item.ID + "\x00" + item.Version
		if prior, exists := byBase[base]; exists && prior != item.Digest {
			return nil, ErrInvalidVariantReport
		}
		byBase[base] = item.Digest
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, item)
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return componentKey(normalized[left]) < componentKey(normalized[right])
	})
	return normalized, nil
}

func componentKey(component EvaluationComponent) string {
	return component.ID + "\x00" + component.Version + "\x00" + component.Digest
}

func normalizeReportStrings(values []string, label string, maxBytes int) ([]string, error) {
	if len(values) > maxListItems {
		return nil, ErrVariantReportLimitExceeded
	}
	if values == nil {
		return []string{}, nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateSafeText(label, value, maxBytes); err != nil {
			return nil, ErrInvalidVariantReport
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func validReportIdentity(value string) bool {
	return validEvaluationIdentity(value) && !containsSensitiveLiteral(value) && !containsRawSourceMarker(value)
}

// VariantRawReport is the content-free, audit-oriented report. CaseSet is
// represented by its digest rather than copied into the artifact, preventing
// case prompts, source excerpts, or reference prose from crossing the report
// boundary.
type VariantRawReport struct {
	Version               string                 `json:"version"`
	Metadata              VariantReportMetadata  `json:"metadata"`
	Execution             VariantExecutionReport `json:"execution"`
	CaseSetDigest         string                 `json:"case_set_digest"`
	InputDigest           string                 `json:"input_digest"`
	ResultDigest          string                 `json:"result_digest"`
	ReproducibilityDigest string                 `json:"reproducibility_digest"`
	ArtifactDigest        string                 `json:"artifact_digest"`
}

// Clone returns a detached raw report.
func (r VariantRawReport) Clone() VariantRawReport {
	clone := r
	clone.Metadata = r.Metadata.Clone()
	clone.Execution = cloneVariantExecutionReport(r.Execution)
	return clone
}

// Normalize validates and returns a canonical raw report. Missing derived
// digests are populated; supplied digests must match exactly.
func (r VariantRawReport) Normalize() (VariantRawReport, error) {
	normalized := r.Clone()
	if normalized.Version == "" {
		normalized.Version = VariantReportingVersion
	}
	if normalized.Version != VariantReportingVersion {
		return VariantRawReport{}, ErrInvalidVariantReport
	}
	var err error
	if normalized.Metadata, err = normalized.Metadata.Normalize(); err != nil {
		return VariantRawReport{}, err
	}
	if normalized.Execution, err = normalizeVariantExecutionReport(normalized.Execution); err != nil {
		return VariantRawReport{}, err
	}
	if err := validateReportPayload(normalized); err != nil {
		return VariantRawReport{}, err
	}
	if !isVariantSHA256(normalized.CaseSetDigest) {
		return VariantRawReport{}, ErrInvalidVariantReport
	}
	normalized.CaseSetDigest = strings.ToLower(normalized.CaseSetDigest)

	expectedInput := variantInputDigest(normalized.CaseSetDigest, normalized.Metadata)
	if normalized.InputDigest != "" && (!isVariantSHA256(normalized.InputDigest) || strings.ToLower(normalized.InputDigest) != expectedInput) {
		return VariantRawReport{}, ErrVariantReportDigestMismatch
	}
	normalized.InputDigest = expectedInput
	expectedResult := variantResultDigest(normalized.Execution)
	if normalized.ResultDigest != "" && (!isVariantSHA256(normalized.ResultDigest) || strings.ToLower(normalized.ResultDigest) != expectedResult) {
		return VariantRawReport{}, ErrVariantReportDigestMismatch
	}
	normalized.ResultDigest = expectedResult
	expectedReproducibility := variantReproducibilityDigest(normalized.InputDigest, normalized.ResultDigest)
	if normalized.ReproducibilityDigest != "" && (!isVariantSHA256(normalized.ReproducibilityDigest) || strings.ToLower(normalized.ReproducibilityDigest) != expectedReproducibility) {
		return VariantRawReport{}, ErrVariantReportDigestMismatch
	}
	normalized.ReproducibilityDigest = expectedReproducibility
	expectedArtifact := variantArtifactDigest(normalized)
	if normalized.ArtifactDigest != "" && (!isVariantSHA256(normalized.ArtifactDigest) || strings.ToLower(normalized.ArtifactDigest) != expectedArtifact) {
		return VariantRawReport{}, ErrVariantReportDigestMismatch
	}
	normalized.ArtifactDigest = expectedArtifact
	return normalized, nil
}

// Validate checks a complete raw report. Unlike Normalize, validation does
// not accept omitted derived digests.
func (r VariantRawReport) Validate() error {
	if r.CaseSetDigest == "" || r.InputDigest == "" || r.ResultDigest == "" || r.ReproducibilityDigest == "" || r.ArtifactDigest == "" {
		return ErrInvalidVariantReport
	}
	normalized, err := r.Normalize()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalized, r) {
		return ErrInvalidVariantReport
	}
	return nil
}

// VariantReportSample contains bounded sample and outcome counts.
type VariantReportSample struct {
	Cases                 int `json:"cases"`
	Executions            int `json:"executions"`
	Successful            int `json:"successful"`
	Completed             int `json:"completed"`
	Limited               int `json:"limited"`
	Failed                int `json:"failed"`
	Unavailable           int `json:"unavailable"`
	Cancelled             int `json:"cancelled"`
	Comparisons           int `json:"comparisons"`
	ComparableComparisons int `json:"comparable_comparisons"`
}

func (s VariantReportSample) validate() error {
	values := []int{s.Cases, s.Executions, s.Successful, s.Completed, s.Limited, s.Failed, s.Unavailable, s.Cancelled, s.Comparisons, s.ComparableComparisons}
	for _, value := range values {
		if value < 0 || value > maxCases*maxListItems {
			return ErrInvalidVariantReport
		}
	}
	if s.Cases == 0 || s.Executions == 0 || s.Successful > s.Executions || s.ComparableComparisons > s.Comparisons {
		return ErrInvalidVariantReport
	}
	if s.Completed+s.Limited+s.Failed+s.Unavailable+s.Cancelled != s.Executions {
		return ErrInvalidVariantReport
	}
	return nil
}

// VariantReportOutcome groups outcome and successful-task counts by the full
// variant/configuration identity, never only by variant kind.
type VariantReportOutcome struct {
	VariantID            string      `json:"variant_id"`
	VariantKind          VariantKind `json:"variant_kind"`
	ConfigurationID      string      `json:"configuration_id"`
	ConfigurationVersion string      `json:"configuration_version"`
	ConfigurationDigest  string      `json:"configuration_digest"`
	Attempted            int         `json:"attempted"`
	Successful           int         `json:"successful"`
	Completed            int         `json:"completed"`
	Limited              int         `json:"limited"`
	Failed               int         `json:"failed"`
	Unavailable          int         `json:"unavailable"`
	Cancelled            int         `json:"cancelled"`
}

func (o VariantReportOutcome) Validate() error {
	if !validEvaluationIdentity(o.VariantID) || !validVariantKind(o.VariantKind) ||
		!validEvaluationIdentity(o.ConfigurationID) || !validEvaluationIdentity(o.ConfigurationVersion) ||
		!isVariantSHA256(o.ConfigurationDigest) || o.Attempted <= 0 || o.Successful < 0 || o.Successful > o.Attempted {
		return ErrInvalidVariantReport
	}
	counts := []int{o.Completed, o.Limited, o.Failed, o.Unavailable, o.Cancelled}
	total := 0
	for _, value := range counts {
		if value < 0 {
			return ErrInvalidVariantReport
		}
		total += value
	}
	if total != o.Attempted || o.Successful > o.Completed {
		return ErrInvalidVariantReport
	}
	return nil
}

func variantOutcomeKey(o VariantReportOutcome) string {
	return o.VariantID + "\x00" + string(o.VariantKind) + "\x00" + o.ConfigurationID + "\x00" + o.ConfigurationVersion + "\x00" + o.ConfigurationDigest
}

// MetricDistribution describes one observed population. A nil pointer in a
// parent metric field means the metric was unavailable; Available is never
// represented as zero for an available distribution.
type MetricDistribution struct {
	Available        int     `json:"available"`
	Min              float64 `json:"min"`
	Mean             float64 `json:"mean"`
	Median           float64 `json:"median"`
	Max              float64 `json:"max"`
	PopulationStdDev float64 `json:"population_std_dev"`
}

// Validate checks distribution cardinality and finite, ordered statistics.
func (d MetricDistribution) Validate() error {
	if d.Available <= 0 || d.Available > maxCases*maxListItems ||
		!finiteReportFloat(d.Min) || !finiteReportFloat(d.Mean) || !finiteReportFloat(d.Median) ||
		!finiteReportFloat(d.Max) || !finiteReportFloat(d.PopulationStdDev) || d.Min > d.Mean ||
		d.Mean > d.Max || d.Median < d.Min || d.Median > d.Max || d.PopulationStdDev < 0 {
		return ErrInvalidVariantReport
	}
	return nil
}

func cloneMetricDistribution(value *MetricDistribution) *MetricDistribution {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

// VariantMetricDistribution groups independently available observed metrics
// by the full variant/configuration identity.
type VariantMetricDistribution struct {
	VariantID            string      `json:"variant_id"`
	VariantKind          VariantKind `json:"variant_kind"`
	ConfigurationID      string      `json:"configuration_id"`
	ConfigurationVersion string      `json:"configuration_version"`
	ConfigurationDigest  string      `json:"configuration_digest"`

	ActualCostUSD         *MetricDistribution `json:"actual_cost_usd,omitempty"`
	EstimatedCostUSD      *MetricDistribution `json:"estimated_cost_usd,omitempty"`
	MeasuredInputTokens   *MetricDistribution `json:"measured_input_tokens,omitempty"`
	MeasuredOutputTokens  *MetricDistribution `json:"measured_output_tokens,omitempty"`
	EstimatedInputTokens  *MetricDistribution `json:"estimated_input_tokens,omitempty"`
	EstimatedOutputTokens *MetricDistribution `json:"estimated_output_tokens,omitempty"`
	ModelCalls            *MetricDistribution `json:"model_calls,omitempty"`
	ToolCalls             *MetricDistribution `json:"tool_calls,omitempty"`
	FilesRead             *MetricDistribution `json:"files_read,omitempty"`
	BytesRead             *MetricDistribution `json:"bytes_read,omitempty"`
	DurationNanoseconds   *MetricDistribution `json:"duration_nanoseconds,omitempty"`
	Correct               *MetricDistribution `json:"correct,omitempty"`
	Completed             *MetricDistribution `json:"completed,omitempty"`
	CriteriaPassed        *MetricDistribution `json:"criteria_passed,omitempty"`
	EvidenceRecall        *MetricDistribution `json:"evidence_recall,omitempty"`
	EvidencePrecision     *MetricDistribution `json:"evidence_precision,omitempty"`
	CitationRate          *MetricDistribution `json:"citation_rate,omitempty"`
	GapRecall             *MetricDistribution `json:"gap_recall,omitempty"`
	AbstentionAppropriate *MetricDistribution `json:"abstention_appropriate,omitempty"`
}

func (d VariantMetricDistribution) Validate() error {
	if !validEvaluationIdentity(d.VariantID) || !validVariantKind(d.VariantKind) ||
		!validEvaluationIdentity(d.ConfigurationID) || !validEvaluationIdentity(d.ConfigurationVersion) ||
		!isVariantSHA256(d.ConfigurationDigest) {
		return ErrInvalidVariantReport
	}
	for _, value := range d.distributions() {
		if value != nil {
			if err := value.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d VariantMetricDistribution) distributions() []*MetricDistribution {
	return []*MetricDistribution{
		d.ActualCostUSD, d.EstimatedCostUSD,
		d.MeasuredInputTokens, d.MeasuredOutputTokens,
		d.EstimatedInputTokens, d.EstimatedOutputTokens,
		d.ModelCalls, d.ToolCalls, d.FilesRead, d.BytesRead,
		d.DurationNanoseconds, d.Correct, d.Completed, d.CriteriaPassed,
		d.EvidenceRecall, d.EvidencePrecision, d.CitationRate, d.GapRecall,
		d.AbstentionAppropriate,
	}
}

func cloneVariantMetricDistribution(value VariantMetricDistribution) VariantMetricDistribution {
	clone := value
	clone.ActualCostUSD = cloneMetricDistribution(value.ActualCostUSD)
	clone.EstimatedCostUSD = cloneMetricDistribution(value.EstimatedCostUSD)
	clone.MeasuredInputTokens = cloneMetricDistribution(value.MeasuredInputTokens)
	clone.MeasuredOutputTokens = cloneMetricDistribution(value.MeasuredOutputTokens)
	clone.EstimatedInputTokens = cloneMetricDistribution(value.EstimatedInputTokens)
	clone.EstimatedOutputTokens = cloneMetricDistribution(value.EstimatedOutputTokens)
	clone.ModelCalls = cloneMetricDistribution(value.ModelCalls)
	clone.ToolCalls = cloneMetricDistribution(value.ToolCalls)
	clone.FilesRead = cloneMetricDistribution(value.FilesRead)
	clone.BytesRead = cloneMetricDistribution(value.BytesRead)
	clone.DurationNanoseconds = cloneMetricDistribution(value.DurationNanoseconds)
	clone.Correct = cloneMetricDistribution(value.Correct)
	clone.Completed = cloneMetricDistribution(value.Completed)
	clone.CriteriaPassed = cloneMetricDistribution(value.CriteriaPassed)
	clone.EvidenceRecall = cloneMetricDistribution(value.EvidenceRecall)
	clone.EvidencePrecision = cloneMetricDistribution(value.EvidencePrecision)
	clone.CitationRate = cloneMetricDistribution(value.CitationRate)
	clone.GapRecall = cloneMetricDistribution(value.GapRecall)
	clone.AbstentionAppropriate = cloneMetricDistribution(value.AbstentionAppropriate)
	return clone
}

// VariantSavingsDistribution contains only savings that the execution
// report already classified as comparable. Observed metrics and savings use
// separate collections so estimates and measurements cannot be mixed.
type VariantSavingsDistribution struct {
	BaselineVariantID            string      `json:"baseline_variant_id"`
	BaselineVariantKind          VariantKind `json:"baseline_variant_kind"`
	BaselineConfigurationDigest  string      `json:"baseline_configuration_digest"`
	CandidateVariantID           string      `json:"candidate_variant_id"`
	CandidateVariantKind         VariantKind `json:"candidate_variant_kind"`
	CandidateConfigurationDigest string      `json:"candidate_configuration_digest"`

	ActualCostSaving            *MetricDistribution `json:"actual_cost_saving,omitempty"`
	EstimatedCostSaving         *MetricDistribution `json:"estimated_cost_saving,omitempty"`
	MeasuredInputTokensSaving   *MetricDistribution `json:"measured_input_tokens_saving,omitempty"`
	MeasuredOutputTokensSaving  *MetricDistribution `json:"measured_output_tokens_saving,omitempty"`
	EstimatedInputTokensSaving  *MetricDistribution `json:"estimated_input_tokens_saving,omitempty"`
	EstimatedOutputTokensSaving *MetricDistribution `json:"estimated_output_tokens_saving,omitempty"`
	ModelCallsSaving            *MetricDistribution `json:"model_calls_saving,omitempty"`
	ToolCallsSaving             *MetricDistribution `json:"tool_calls_saving,omitempty"`
	FilesReadSaving             *MetricDistribution `json:"files_read_saving,omitempty"`
	BytesReadSaving             *MetricDistribution `json:"bytes_read_saving,omitempty"`
	DurationSaving              *MetricDistribution `json:"duration_saving,omitempty"`
}

func (d VariantSavingsDistribution) Validate() error {
	if !validEvaluationIdentity(d.BaselineVariantID) || d.BaselineVariantKind != VariantDirectSource ||
		!isVariantSHA256(d.BaselineConfigurationDigest) || !validEvaluationIdentity(d.CandidateVariantID) ||
		!validVariantKind(d.CandidateVariantKind) || d.CandidateVariantKind == VariantDirectSource ||
		!isVariantSHA256(d.CandidateConfigurationDigest) || d.BaselineVariantID == d.CandidateVariantID {
		return ErrInvalidVariantReport
	}
	for _, value := range d.distributions() {
		if value != nil {
			if err := value.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d VariantSavingsDistribution) distributions() []*MetricDistribution {
	return []*MetricDistribution{
		d.ActualCostSaving, d.EstimatedCostSaving,
		d.MeasuredInputTokensSaving, d.MeasuredOutputTokensSaving,
		d.EstimatedInputTokensSaving, d.EstimatedOutputTokensSaving,
		d.ModelCallsSaving, d.ToolCallsSaving, d.FilesReadSaving,
		d.BytesReadSaving, d.DurationSaving,
	}
}

func cloneVariantSavingsDistribution(value VariantSavingsDistribution) VariantSavingsDistribution {
	clone := value
	clone.ActualCostSaving = cloneMetricDistribution(value.ActualCostSaving)
	clone.EstimatedCostSaving = cloneMetricDistribution(value.EstimatedCostSaving)
	clone.MeasuredInputTokensSaving = cloneMetricDistribution(value.MeasuredInputTokensSaving)
	clone.MeasuredOutputTokensSaving = cloneMetricDistribution(value.MeasuredOutputTokensSaving)
	clone.EstimatedInputTokensSaving = cloneMetricDistribution(value.EstimatedInputTokensSaving)
	clone.EstimatedOutputTokensSaving = cloneMetricDistribution(value.EstimatedOutputTokensSaving)
	clone.ModelCallsSaving = cloneMetricDistribution(value.ModelCallsSaving)
	clone.ToolCallsSaving = cloneMetricDistribution(value.ToolCallsSaving)
	clone.FilesReadSaving = cloneMetricDistribution(value.FilesReadSaving)
	clone.BytesReadSaving = cloneMetricDistribution(value.BytesReadSaving)
	clone.DurationSaving = cloneMetricDistribution(value.DurationSaving)
	return clone
}

func savingsKey(value VariantSavingsDistribution) string {
	return value.BaselineVariantID + "\x00" + value.BaselineConfigurationDigest + "\x00" + value.CandidateVariantID + "\x00" + value.CandidateConfigurationDigest
}

// VariantSummaryReport is a descriptive projection of a raw report. It
// intentionally has no quality score, superiority claim, or extrapolation.
type VariantSummaryReport struct {
	Version               string                       `json:"version"`
	SummaryDigest         string                       `json:"summary_digest"`
	RawDigest             string                       `json:"raw_digest"`
	ArtifactDigest        string                       `json:"artifact_digest"`
	CaseSetDigest         string                       `json:"case_set_digest"`
	InputDigest           string                       `json:"input_digest"`
	ResultDigest          string                       `json:"result_digest"`
	ReproducibilityDigest string                       `json:"reproducibility_digest"`
	Samples               VariantReportSample          `json:"samples"`
	Outcomes              []VariantReportOutcome       `json:"outcomes"`
	Observed              []VariantMetricDistribution  `json:"observed"`
	Savings               []VariantSavingsDistribution `json:"savings"`
	Configurations        []EvaluationComponent        `json:"configurations"`
	Limitations           []string                     `json:"limitations"`
}

// Clone returns a detached summary.
func (s VariantSummaryReport) Clone() VariantSummaryReport {
	clone := s
	clone.Outcomes = append([]VariantReportOutcome(nil), s.Outcomes...)
	clone.Observed = append([]VariantMetricDistribution(nil), s.Observed...)
	for index := range clone.Observed {
		clone.Observed[index] = cloneVariantMetricDistribution(s.Observed[index])
	}
	clone.Savings = append([]VariantSavingsDistribution(nil), s.Savings...)
	for index := range clone.Savings {
		clone.Savings[index] = cloneVariantSavingsDistribution(s.Savings[index])
	}
	clone.Configurations = append([]EvaluationComponent(nil), s.Configurations...)
	clone.Limitations = cloneCaseStrings(s.Limitations)
	return clone
}

// Normalize validates and sorts a summary. It also verifies the stable
// reproducibility digest relationship between input and result digests.
func (s VariantSummaryReport) Normalize() (VariantSummaryReport, error) {
	normalized := s.Clone()
	providedSummaryDigest := normalized.SummaryDigest
	normalized.SummaryDigest = ""
	if normalized.Version == "" {
		normalized.Version = VariantReportingVersion
	}
	if normalized.Version != VariantReportingVersion || !isVariantSHA256(normalized.RawDigest) ||
		!isVariantSHA256(normalized.ArtifactDigest) || normalized.RawDigest != normalized.ArtifactDigest ||
		!isVariantSHA256(normalized.CaseSetDigest) || !isVariantSHA256(normalized.InputDigest) ||
		!isVariantSHA256(normalized.ResultDigest) || !isVariantSHA256(normalized.ReproducibilityDigest) ||
		normalized.ReproducibilityDigest != variantReproducibilityDigest(normalized.InputDigest, normalized.ResultDigest) {
		return VariantSummaryReport{}, ErrVariantReportDigestMismatch
	}
	if err := normalized.Samples.validate(); err != nil {
		return VariantSummaryReport{}, err
	}
	if len(normalized.Outcomes) == 0 || len(normalized.Outcomes) > maxListItems {
		return VariantSummaryReport{}, ErrVariantReportLimitExceeded
	}
	sort.SliceStable(normalized.Outcomes, func(left, right int) bool {
		return variantOutcomeKey(normalized.Outcomes[left]) < variantOutcomeKey(normalized.Outcomes[right])
	})
	seenOutcomes := make(map[string]struct{}, len(normalized.Outcomes))
	counts := VariantReportSample{}
	for _, outcome := range normalized.Outcomes {
		if err := outcome.Validate(); err != nil {
			return VariantSummaryReport{}, err
		}
		key := variantOutcomeKey(outcome)
		if _, exists := seenOutcomes[key]; exists {
			return VariantSummaryReport{}, ErrInvalidVariantReport
		}
		seenOutcomes[key] = struct{}{}
		counts.Executions += outcome.Attempted
		counts.Successful += outcome.Successful
		counts.Completed += outcome.Completed
		counts.Limited += outcome.Limited
		counts.Failed += outcome.Failed
		counts.Unavailable += outcome.Unavailable
		counts.Cancelled += outcome.Cancelled
	}
	if counts.Executions != normalized.Samples.Executions || counts.Successful != normalized.Samples.Successful ||
		counts.Completed != normalized.Samples.Completed || counts.Limited != normalized.Samples.Limited ||
		counts.Failed != normalized.Samples.Failed || counts.Unavailable != normalized.Samples.Unavailable ||
		counts.Cancelled != normalized.Samples.Cancelled {
		return VariantSummaryReport{}, ErrInvalidVariantReport
	}

	if len(normalized.Observed) > maxListItems || len(normalized.Savings) > maxListItems {
		return VariantSummaryReport{}, ErrVariantReportLimitExceeded
	}
	sort.SliceStable(normalized.Observed, func(left, right int) bool {
		return variantMetricDistributionKey(normalized.Observed[left]) < variantMetricDistributionKey(normalized.Observed[right])
	})
	seenObserved := make(map[string]struct{}, len(normalized.Observed))
	for _, metrics := range normalized.Observed {
		if err := metrics.Validate(); err != nil {
			return VariantSummaryReport{}, err
		}
		key := variantMetricDistributionKey(metrics)
		if _, exists := seenObserved[key]; exists {
			return VariantSummaryReport{}, ErrInvalidVariantReport
		}
		seenObserved[key] = struct{}{}
	}
	sort.SliceStable(normalized.Savings, func(left, right int) bool {
		return savingsKey(normalized.Savings[left]) < savingsKey(normalized.Savings[right])
	})
	seenSavings := make(map[string]struct{}, len(normalized.Savings))
	for _, savings := range normalized.Savings {
		if err := savings.Validate(); err != nil {
			return VariantSummaryReport{}, err
		}
		key := savingsKey(savings)
		if _, exists := seenSavings[key]; exists {
			return VariantSummaryReport{}, ErrInvalidVariantReport
		}
		seenSavings[key] = struct{}{}
	}
	var err error
	normalized.Configurations, err = normalizeReportComponents(normalized.Configurations, true)
	if err != nil {
		return VariantSummaryReport{}, err
	}
	normalized.Limitations, err = normalizeReportStrings(normalized.Limitations, "summary limitation", maxTextBytes)
	if err != nil {
		return VariantSummaryReport{}, err
	}
	if err := validateReportPayload(normalized); err != nil {
		return VariantSummaryReport{}, err
	}
	expectedSummaryDigest := variantSummaryDigest(normalized)
	if providedSummaryDigest != "" && (!isVariantSHA256(providedSummaryDigest) || strings.ToLower(providedSummaryDigest) != expectedSummaryDigest) {
		return VariantSummaryReport{}, ErrVariantReportDigestMismatch
	}
	normalized.SummaryDigest = expectedSummaryDigest
	return normalized, nil
}

// Validate checks a complete summary without changing its value.
func (s VariantSummaryReport) Validate() error {
	if s.SummaryDigest == "" {
		return ErrInvalidVariantReport
	}
	normalized, err := s.Normalize()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(normalized, s) {
		return ErrInvalidVariantReport
	}
	return nil
}

func variantMetricDistributionKey(value VariantMetricDistribution) string {
	return value.VariantID + "\x00" + string(value.VariantKind) + "\x00" + value.ConfigurationID + "\x00" + value.ConfigurationVersion + "\x00" + value.ConfigurationDigest
}

// VariantReportComparison identifies changed dimensions between two raw
// reports. It does not judge whether a change is good, bad, or significant.
type VariantReportComparison struct {
	Equal             bool     `json:"equal"`
	Reproducible      bool     `json:"reproducible"`
	ChangedDimensions []string `json:"changed_dimensions"`
}

// CompareVariantReports compares canonical raw reports. Equal includes
// artifact identity, while Reproducible compares the stable digest that
// excludes run identifiers and timestamps.
func CompareVariantReports(before, after VariantRawReport) (VariantReportComparison, error) {
	if err := before.Validate(); err != nil {
		return VariantReportComparison{}, err
	}
	if err := after.Validate(); err != nil {
		return VariantReportComparison{}, err
	}
	left, err := before.Normalize()
	if err != nil {
		return VariantReportComparison{}, err
	}
	right, err := after.Normalize()
	if err != nil {
		return VariantReportComparison{}, err
	}
	changed := make([]string, 0, 8)
	if !reflect.DeepEqual(left.Metadata.Frontends, right.Metadata.Frontends) {
		changed = append(changed, "frontend")
	}
	if !reflect.DeepEqual(left.Metadata.Rules, right.Metadata.Rules) {
		changed = append(changed, "rule")
	}
	if left.Metadata.RetrievalDigest != right.Metadata.RetrievalDigest {
		changed = append(changed, "retrieval")
	}
	if !reflect.DeepEqual(left.Metadata.ContextServer, right.Metadata.ContextServer) {
		changed = append(changed, "context-server")
	}
	if !reflect.DeepEqual(left.Metadata.Agent, right.Metadata.Agent) {
		changed = append(changed, "agent")
	}
	if !reflect.DeepEqual(left.Metadata.Model, right.Metadata.Model) {
		changed = append(changed, "model")
	}
	if !reflect.DeepEqual(left.Metadata.Tools, right.Metadata.Tools) {
		changed = append(changed, "tools")
	}
	if left.CaseSetDigest != right.CaseSetDigest {
		changed = append(changed, "cases")
	}
	if !reflect.DeepEqual(executionConfigurationIdentities(left.Execution), executionConfigurationIdentities(right.Execution)) {
		changed = append(changed, "configuration")
	}
	if left.ResultDigest != right.ResultDigest {
		changed = append(changed, "results")
	}
	if !reflect.DeepEqual(left.Metadata.Limitations, right.Metadata.Limitations) {
		changed = append(changed, "limitations")
	}
	sort.Strings(changed)
	return VariantReportComparison{
		Equal:             left.ArtifactDigest == right.ArtifactDigest,
		Reproducible:      left.ReproducibilityDigest == right.ReproducibilityDigest,
		ChangedDimensions: changed,
	}, nil
}

// BuildVariantReports validates a case set and matching execution report,
// then creates detached raw and summary artifacts with canonical digests.
func BuildVariantReports(cases CaseSet, execution VariantExecutionReport, metadata VariantReportMetadata) (VariantRawReport, VariantSummaryReport, error) {
	normalizedCases, err := cases.Normalize()
	if err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	if normalizedCases.Version != Version {
		return VariantRawReport{}, VariantSummaryReport{}, ErrInvalidVariantReport
	}
	normalizedExecution, err := normalizeVariantExecutionReport(execution)
	if err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	if err := validateExecutionCaseCoherence(normalizedCases, normalizedExecution); err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	normalizedMetadata, err := metadata.Normalize()
	if err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	caseDigest, err := evaluationCaseSetDigest(normalizedCases)
	if err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	raw := VariantRawReport{
		Version:       VariantReportingVersion,
		Metadata:      normalizedMetadata,
		Execution:     normalizedExecution,
		CaseSetDigest: caseDigest,
	}
	raw, err = raw.Normalize()
	if err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	summary, err := deriveVariantSummary(raw)
	if err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	if err := raw.Validate(); err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	if err := summary.Validate(); err != nil {
		return VariantRawReport{}, VariantSummaryReport{}, err
	}
	return raw, summary, nil
}

// MarshalVariantRawReport returns canonical, indented JSON with a final
// newline. It rejects incomplete or tampered report digests.
func MarshalVariantRawReport(report VariantRawReport) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, ErrInvalidVariantReport
	}
	return append(data, '\n'), nil
}

// WriteVariantRawReport writes canonical raw report JSON to writer.
func WriteVariantRawReport(writer io.Writer, report VariantRawReport) error {
	if writer == nil {
		return ErrInvalidVariantReport
	}
	data, err := MarshalVariantRawReport(report)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return ErrInvalidVariantReport
	}
	return nil
}

// MarshalVariantSummaryReport returns canonical, indented summary JSON with
// a final newline.
func MarshalVariantSummaryReport(report VariantSummaryReport) ([]byte, error) {
	if err := report.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, ErrInvalidVariantReport
	}
	return append(data, '\n'), nil
}

// WriteVariantSummaryReport writes canonical summary JSON to writer.
func WriteVariantSummaryReport(writer io.Writer, report VariantSummaryReport) error {
	if writer == nil {
		return ErrInvalidVariantReport
	}
	data, err := MarshalVariantSummaryReport(report)
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		return ErrInvalidVariantReport
	}
	return nil
}

func normalizeVariantExecutionReport(input VariantExecutionReport) (VariantExecutionReport, error) {
	if err := input.Validate(); err != nil {
		return VariantExecutionReport{}, ErrInvalidVariantReport
	}
	normalized := cloneVariantExecutionReport(input)
	sort.SliceStable(normalized.Cases, func(left, right int) bool {
		if normalized.Cases[left].CaseID != normalized.Cases[right].CaseID {
			return normalized.Cases[left].CaseID < normalized.Cases[right].CaseID
		}
		return normalized.Cases[left].CaseVersion < normalized.Cases[right].CaseVersion
	})
	for index := range normalized.Cases {
		caseReport := &normalized.Cases[index]
		caseReport.Limitations, _ = normalizeVariantLimitations(caseReport.Limitations)
		sort.SliceStable(caseReport.Executions, func(left, right int) bool {
			return caseReport.Executions[left].VariantID < caseReport.Executions[right].VariantID
		})
		for executionIndex := range caseReport.Executions {
			record := &caseReport.Executions[executionIndex]
			result, err := record.Result.Normalize()
			if err != nil {
				return VariantExecutionReport{}, ErrInvalidVariantReport
			}
			record.Result = result
			quality, err := record.Quality.Normalize()
			if err != nil {
				return VariantExecutionReport{}, ErrInvalidVariantReport
			}
			record.Quality = &quality
			if record.Differences != nil {
				difference := normalizeVariantDifference(*record.Differences)
				if err := difference.Validate(); err != nil {
					return VariantExecutionReport{}, ErrInvalidVariantReport
				}
				record.Differences = &difference
			}
			sort.Strings(record.ToolIDs)
		}
	}
	var err error
	normalized.Efficiency, err = normalized.Efficiency.Normalize()
	if err != nil {
		return VariantExecutionReport{}, ErrInvalidVariantReport
	}
	if err := normalized.Validate(); err != nil {
		return VariantExecutionReport{}, ErrInvalidVariantReport
	}
	return normalized, nil
}

func cloneVariantExecutionReport(input VariantExecutionReport) VariantExecutionReport {
	clone := input
	clone.Cases = make([]VariantCaseReport, len(input.Cases))
	for index, item := range input.Cases {
		clone.Cases[index] = item
		clone.Cases[index].Executions = make([]VariantExecutionRecord, len(item.Executions))
		for executionIndex, record := range item.Executions {
			clone.Cases[index].Executions[executionIndex] = record
			clone.Cases[index].Executions[executionIndex].ToolIDs = cloneCaseStrings(record.ToolIDs)
			clone.Cases[index].Executions[executionIndex].Result = record.Result.Clone()
			if record.Quality != nil {
				quality := record.Quality.Clone()
				clone.Cases[index].Executions[executionIndex].Quality = &quality
			}
			if record.Differences != nil {
				difference := normalizeVariantDifference(*record.Differences)
				clone.Cases[index].Executions[executionIndex].Differences = &difference
			}
		}
		clone.Cases[index].Limitations = cloneCaseStrings(item.Limitations)
	}
	clone.Efficiency = input.Efficiency.Clone()
	return clone
}

func normalizeVariantDifference(value VariantDifference) VariantDifference {
	value.ToolIDsAdded = cloneCaseStrings(value.ToolIDsAdded)
	value.ToolIDsRemoved = cloneCaseStrings(value.ToolIDsRemoved)
	value.ConfigurationKeysAdded = cloneCaseStrings(value.ConfigurationKeysAdded)
	value.ConfigurationKeysRemoved = cloneCaseStrings(value.ConfigurationKeysRemoved)
	value.ConfigurationKeysChanged = cloneCaseStrings(value.ConfigurationKeysChanged)
	sort.Strings(value.ToolIDsAdded)
	sort.Strings(value.ToolIDsRemoved)
	sort.Strings(value.ConfigurationKeysAdded)
	sort.Strings(value.ConfigurationKeysRemoved)
	sort.Strings(value.ConfigurationKeysChanged)
	return value
}

func validateExecutionCaseCoherence(cases CaseSet, execution VariantExecutionReport) error {
	if execution.CasesVersion != cases.Version || len(cases.Cases) != len(execution.Cases) {
		return ErrInvalidVariantReport
	}
	byIdentity := make(map[string]EvaluationCase, len(cases.Cases))
	for _, item := range cases.Cases {
		byIdentity[caseIdentity(item)] = item
	}
	for _, caseReport := range execution.Cases {
		item, exists := byIdentity[caseIdentity(EvaluationCase{CaseID: caseReport.CaseID, CaseVersion: caseReport.CaseVersion})]
		if !exists || item.CorpusID != caseReport.CorpusID || item.CorpusRevision != caseReport.CorpusRevision ||
			item.SourceID != caseReport.SourceID || item.SourceRevision != caseReport.SourceRevision {
			return ErrInvalidVariantReport
		}
		variants := make(map[string]EvaluationVariant, len(item.Variants))
		for _, variant := range item.Variants {
			variants[variant.ID] = variant
		}
		configurations := make(map[string]EvaluationConfiguration, len(item.Configurations))
		for _, configuration := range item.Configurations {
			configurations[configuration.ID] = configuration
		}
		for _, record := range caseReport.Executions {
			variant, exists := variants[record.VariantID]
			if !exists || variant.Kind != record.VariantKind || variant.ConfigurationID != record.ConfigurationID ||
				!sameStringSet(variant.ToolIDs, record.ToolIDs) {
				return ErrInvalidVariantReport
			}
			configuration, exists := configurations[record.ConfigurationID]
			if !exists || configuration.Version != record.ConfigurationVersion {
				return ErrInvalidVariantReport
			}
			digest, err := ConfigurationDigest(configuration)
			if err != nil || digest != record.ConfigurationDigest {
				return ErrInvalidVariantReport
			}
		}
	}
	return nil
}

func evaluationCaseSetDigest(cases CaseSet) (string, error) {
	data, err := MarshalCases(cases)
	if err != nil {
		return "", ErrInvalidVariantReport
	}
	return reportDigestBytes("case-set", data), nil
}

func variantInputDigest(caseDigest string, metadata VariantReportMetadata) string {
	stable := makeStableVariantReportMetadata(metadata)
	data, _ := json.Marshal(struct {
		Version  string                      `json:"version"`
		CaseSet  string                      `json:"case_set_digest"`
		Metadata stableVariantReportMetadata `json:"metadata"`
	}{Version: VariantReportingVersion, CaseSet: caseDigest, Metadata: stable})
	return reportDigestBytes("variant-input", data)
}

func variantResultDigest(execution VariantExecutionReport) string {
	data, _ := json.Marshal(struct {
		Version   string                 `json:"version"`
		Execution VariantExecutionReport `json:"execution"`
	}{Version: VariantReportingVersion, Execution: execution})
	return reportDigestBytes("variant-result", data)
}

func variantReproducibilityDigest(inputDigest, resultDigest string) string {
	data, _ := json.Marshal(struct {
		Version string `json:"version"`
		Input   string `json:"input_digest"`
		Result  string `json:"result_digest"`
	}{Version: VariantReportingVersion, Input: inputDigest, Result: resultDigest})
	return reportDigestBytes("variant-reproducibility", data)
}

func variantArtifactDigest(report VariantRawReport) string {
	data, _ := json.Marshal(struct {
		Version               string                 `json:"version"`
		Metadata              VariantReportMetadata  `json:"metadata"`
		Execution             VariantExecutionReport `json:"execution"`
		CaseSetDigest         string                 `json:"case_set_digest"`
		InputDigest           string                 `json:"input_digest"`
		ResultDigest          string                 `json:"result_digest"`
		ReproducibilityDigest string                 `json:"reproducibility_digest"`
	}{Version: report.Version, Metadata: report.Metadata, Execution: report.Execution, CaseSetDigest: report.CaseSetDigest, InputDigest: report.InputDigest, ResultDigest: report.ResultDigest, ReproducibilityDigest: report.ReproducibilityDigest})
	return reportDigestBytes("variant-artifact", data)
}

func variantSummaryDigest(summary VariantSummaryReport) string {
	canonical := summary
	canonical.SummaryDigest = ""
	data, _ := json.Marshal(canonical)
	return reportDigestBytes("variant-summary", data)
}

type stableVariantReportMetadata struct {
	Agent         EvaluationComponent   `json:"agent"`
	Model         EvaluationComponent   `json:"model"`
	ContextServer EvaluationComponent   `json:"context_server"`
	Frontends     []EvaluationComponent `json:"frontends"`
	Rules         []EvaluationComponent `json:"rules"`
	Retrieval     EvaluationComponent   `json:"retrieval"`
	Tools         []EvaluationComponent `json:"tools"`
	Limitations   []string              `json:"limitations"`
}

func makeStableVariantReportMetadata(metadata VariantReportMetadata) stableVariantReportMetadata {
	retrieval := EvaluationComponent{}
	if !metadata.Retrieval.isZero() {
		retrieval = EvaluationComponent{ID: metadata.Retrieval.ID, Version: metadata.Retrieval.Version, Digest: metadata.RetrievalDigest}
	}
	return stableVariantReportMetadata{
		Agent: metadata.Agent, Model: metadata.Model, ContextServer: metadata.ContextServer,
		Frontends: append([]EvaluationComponent(nil), metadata.Frontends...),
		Rules:     append([]EvaluationComponent(nil), metadata.Rules...), Retrieval: retrieval,
		Tools: append([]EvaluationComponent(nil), metadata.Tools...), Limitations: append([]string(nil), metadata.Limitations...),
	}
}

func reportDigestBytes(domain string, data []byte) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(data)
	return hex.EncodeToString(hash.Sum(nil))
}

func finiteReportFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateReportPayload(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return ErrInvalidVariantReport
	}
	text := string(data)
	if containsSensitiveLiteral(text) || containsRawSourceMarker(text) {
		return ErrInvalidVariantReport
	}
	return nil
}

func deriveVariantSummary(raw VariantRawReport) (VariantSummaryReport, error) {
	summary := VariantSummaryReport{
		Version: VariantReportingVersion, RawDigest: raw.ArtifactDigest, ArtifactDigest: raw.ArtifactDigest,
		CaseSetDigest: raw.CaseSetDigest, InputDigest: raw.InputDigest, ResultDigest: raw.ResultDigest,
		ReproducibilityDigest: raw.ReproducibilityDigest,
		Samples:               VariantReportSample{Cases: len(raw.Execution.Cases)},
	}
	type identity struct {
		variantID, variantKind, configurationID, configurationVersion, configurationDigest string
	}
	groups := make(map[identity][]VariantExecutionRecord)
	limitations := append([]string(nil), raw.Metadata.Limitations...)
	configurations := make([]EvaluationComponent, 0)
	if !raw.Metadata.Retrieval.isZero() {
		configurations = append(configurations, EvaluationComponent{ID: raw.Metadata.Retrieval.ID, Version: raw.Metadata.Retrieval.Version, Digest: raw.Metadata.RetrievalDigest})
	}
	for _, caseReport := range raw.Execution.Cases {
		limitations = append(limitations, caseReport.Limitations...)
		for _, record := range caseReport.Executions {
			limitations = append(limitations, record.Result.Limitations...)
			summary.Samples.Executions++
			identity := identity{record.VariantID, string(record.VariantKind), record.ConfigurationID, record.ConfigurationVersion, record.ConfigurationDigest}
			groups[identity] = append(groups[identity], record)
			configurations = append(configurations, EvaluationComponent{ID: record.ConfigurationID, Version: record.ConfigurationVersion, Digest: record.ConfigurationDigest})
			switch record.Outcome {
			case VariantOutcomeCompleted:
				summary.Samples.Completed++
			case VariantOutcomeLimited:
				summary.Samples.Limited++
			case VariantOutcomeFailed:
				summary.Samples.Failed++
			case VariantOutcomeUnavailable:
				summary.Samples.Unavailable++
			case VariantOutcomeCancelled:
				summary.Samples.Cancelled++
			}
			if variantExecutionIsSuccess(record) {
				summary.Samples.Successful++
			}
		}
	}
	summary.Samples.Comparisons = len(raw.Execution.Efficiency.Comparisons)
	for _, comparison := range raw.Execution.Efficiency.Comparisons {
		if comparison.Comparable {
			summary.Samples.ComparableComparisons++
		}
	}
	for key, records := range groups {
		outcome := VariantReportOutcome{
			VariantID: key.variantID, VariantKind: VariantKind(key.variantKind), ConfigurationID: key.configurationID,
			ConfigurationVersion: key.configurationVersion, ConfigurationDigest: key.configurationDigest, Attempted: len(records),
		}
		values := metricValueSet{}
		for _, record := range records {
			switch record.Outcome {
			case VariantOutcomeCompleted:
				outcome.Completed++
			case VariantOutcomeLimited:
				outcome.Limited++
			case VariantOutcomeFailed:
				outcome.Failed++
			case VariantOutcomeUnavailable:
				outcome.Unavailable++
			case VariantOutcomeCancelled:
				outcome.Cancelled++
			}
			if variantExecutionIsSuccess(record) {
				outcome.Successful++
			}
			appendRecordMetricValues(&values, record)
		}
		metrics, err := values.distributions(key)
		if err != nil {
			return VariantSummaryReport{}, err
		}
		summary.Outcomes = append(summary.Outcomes, outcome)
		summary.Observed = append(summary.Observed, metrics)
	}
	savingsGroups := make(map[string]*savingsGroup)
	for _, comparison := range raw.Execution.Efficiency.Comparisons {
		if !comparison.Comparable {
			continue
		}
		key := savingsKeyFromComparison(comparison)
		group, exists := savingsGroups[key]
		if !exists {
			group = &savingsGroup{summary: VariantSavingsDistribution{
				BaselineVariantID: comparison.BaselineVariantID, BaselineVariantKind: comparison.BaselineVariantKind,
				BaselineConfigurationDigest: comparison.BaselineConfigurationDigest, CandidateVariantID: comparison.CandidateVariantID,
				CandidateVariantKind: comparison.CandidateVariantKind, CandidateConfigurationDigest: comparison.CandidateConfigurationDigest,
			}}
			savingsGroups[key] = group
		}
		appendComparisonSavings(&group.values, comparison)
	}
	for _, group := range savingsGroups {
		distributions, err := group.values.distributions()
		if err != nil {
			return VariantSummaryReport{}, err
		}
		group.summary.ActualCostSaving = distributions.ActualCostSaving
		group.summary.EstimatedCostSaving = distributions.EstimatedCostSaving
		group.summary.MeasuredInputTokensSaving = distributions.MeasuredInputTokensSaving
		group.summary.MeasuredOutputTokensSaving = distributions.MeasuredOutputTokensSaving
		group.summary.EstimatedInputTokensSaving = distributions.EstimatedInputTokensSaving
		group.summary.EstimatedOutputTokensSaving = distributions.EstimatedOutputTokensSaving
		group.summary.ModelCallsSaving = distributions.ModelCallsSaving
		group.summary.ToolCallsSaving = distributions.ToolCallsSaving
		group.summary.FilesReadSaving = distributions.FilesReadSaving
		group.summary.BytesReadSaving = distributions.BytesReadSaving
		group.summary.DurationSaving = distributions.DurationSaving
		summary.Savings = append(summary.Savings, group.summary)
	}
	var err error
	summary.Configurations, err = normalizeReportComponents(configurations, true)
	if err != nil {
		return VariantSummaryReport{}, err
	}
	summary.Limitations, err = normalizeReportStrings(limitations, "summary limitation", maxTextBytes)
	if err != nil {
		return VariantSummaryReport{}, err
	}
	return summary.Normalize()
}

type metricValueSet struct {
	actualCost, estimatedCost                             []float64
	measuredInput, measuredOutput                         []float64
	estimatedInput, estimatedOutput                       []float64
	modelCalls, toolCalls, filesRead, bytesRead, duration []float64
	correct, completed, criteriaPassed                    []float64
	evidenceRecall, evidencePrecision, citationRate       []float64
	gapRecall, abstentionAppropriate                      []float64
}

func appendRecordMetricValues(values *metricValueSet, record VariantExecutionRecord) {
	if record.Quality != nil {
		values.correct = append(values.correct, boolMetric(record.Quality.Correct))
		values.completed = append(values.completed, boolMetric(record.Quality.Completed))
		values.criteriaPassed = append(values.criteriaPassed, boolMetric(record.Quality.RequiredCriteriaPassed))
		values.evidenceRecall = appendOptionalFloat(values.evidenceRecall, record.Quality.Evidence.Recall)
		values.evidencePrecision = appendOptionalFloat(values.evidencePrecision, record.Quality.Evidence.Precision)
		values.citationRate = appendOptionalFloat(values.citationRate, record.Quality.Citations.Rate)
		values.gapRecall = appendOptionalFloat(values.gapRecall, record.Quality.Gaps.Recall)
		values.abstentionAppropriate = append(values.abstentionAppropriate, boolMetric(record.Quality.Abstention.Appropriate))
	}
	metrics := record.Result.Metrics
	if metrics == nil {
		return
	}
	if metrics.ActualCost != nil {
		values.actualCost = append(values.actualCost, metrics.ActualCost.USD)
	}
	if metrics.EstimatedCost != nil {
		values.estimatedCost = append(values.estimatedCost, metrics.EstimatedCost.USD)
	}
	if metrics.MeasuredTokens != nil {
		if metrics.MeasuredTokens.InputTokens != nil {
			values.measuredInput = append(values.measuredInput, float64(*metrics.MeasuredTokens.InputTokens))
		}
		if metrics.MeasuredTokens.OutputTokens != nil {
			values.measuredOutput = append(values.measuredOutput, float64(*metrics.MeasuredTokens.OutputTokens))
		}
	}
	if metrics.EstimatedTokens != nil {
		if metrics.EstimatedTokens.InputTokens != nil {
			values.estimatedInput = append(values.estimatedInput, float64(*metrics.EstimatedTokens.InputTokens))
		}
		if metrics.EstimatedTokens.OutputTokens != nil {
			values.estimatedOutput = append(values.estimatedOutput, float64(*metrics.EstimatedTokens.OutputTokens))
		}
	}
	if metrics.ModelCalls != nil {
		values.modelCalls = append(values.modelCalls, float64(*metrics.ModelCalls))
	}
	if metrics.ToolCalls != nil {
		values.toolCalls = append(values.toolCalls, float64(*metrics.ToolCalls))
	}
	if metrics.FilesRead != nil {
		values.filesRead = append(values.filesRead, float64(*metrics.FilesRead))
	}
	if metrics.BytesRead != nil {
		values.bytesRead = append(values.bytesRead, float64(*metrics.BytesRead))
	}
	if metrics.Duration != nil {
		if nanoseconds, err := durationNanoseconds(*metrics.Duration); err == nil {
			values.duration = append(values.duration, float64(nanoseconds))
		}
	}
}

func (v metricValueSet) distributions(key struct {
	variantID, variantKind, configurationID, configurationVersion, configurationDigest string
}) (VariantMetricDistribution, error) {
	distribution := VariantMetricDistribution{VariantID: key.variantID, VariantKind: VariantKind(key.variantKind), ConfigurationID: key.configurationID, ConfigurationVersion: key.configurationVersion, ConfigurationDigest: key.configurationDigest}
	var err error
	if distribution.ActualCostUSD, err = makeMetricDistribution(v.actualCost); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.EstimatedCostUSD, err = makeMetricDistribution(v.estimatedCost); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.MeasuredInputTokens, err = makeMetricDistribution(v.measuredInput); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.MeasuredOutputTokens, err = makeMetricDistribution(v.measuredOutput); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.EstimatedInputTokens, err = makeMetricDistribution(v.estimatedInput); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.EstimatedOutputTokens, err = makeMetricDistribution(v.estimatedOutput); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.ModelCalls, err = makeMetricDistribution(v.modelCalls); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.ToolCalls, err = makeMetricDistribution(v.toolCalls); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.FilesRead, err = makeMetricDistribution(v.filesRead); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.BytesRead, err = makeMetricDistribution(v.bytesRead); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.DurationNanoseconds, err = makeMetricDistribution(v.duration); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.Correct, err = makeMetricDistribution(v.correct); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.Completed, err = makeMetricDistribution(v.completed); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.CriteriaPassed, err = makeMetricDistribution(v.criteriaPassed); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.EvidenceRecall, err = makeMetricDistribution(v.evidenceRecall); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.EvidencePrecision, err = makeMetricDistribution(v.evidencePrecision); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.CitationRate, err = makeMetricDistribution(v.citationRate); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.GapRecall, err = makeMetricDistribution(v.gapRecall); err != nil {
		return VariantMetricDistribution{}, err
	}
	if distribution.AbstentionAppropriate, err = makeMetricDistribution(v.abstentionAppropriate); err != nil {
		return VariantMetricDistribution{}, err
	}
	return distribution, nil
}

func boolMetric(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func makeMetricDistribution(values []float64) (*MetricDistribution, error) {
	return makeMetricDistributionWithDomain(values, false)
}

func makeSavingsDistribution(values []float64) (*MetricDistribution, error) {
	return makeMetricDistributionWithDomain(values, true)
}

func makeMetricDistributionWithDomain(values []float64, allowNegative bool) (*MetricDistribution, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxCases*maxListItems {
		return nil, ErrVariantReportLimitExceeded
	}
	ordered := append([]float64(nil), values...)
	for _, value := range ordered {
		if !finiteReportFloat(value) || (!allowNegative && value < 0) {
			return nil, ErrInvalidVariantReport
		}
	}
	sort.Float64s(ordered)
	var total float64
	for _, value := range ordered {
		total += value
		if !finiteReportFloat(total) {
			return nil, ErrInvalidVariantReport
		}
	}
	mean := total / float64(len(ordered))
	median := ordered[len(ordered)/2]
	if len(ordered)%2 == 0 {
		median = (ordered[len(ordered)/2-1] + ordered[len(ordered)/2]) / 2
	}
	variance := 0.0
	for _, value := range ordered {
		delta := value - mean
		variance += delta * delta
		if !finiteReportFloat(variance) {
			return nil, ErrInvalidVariantReport
		}
	}
	variance /= float64(len(ordered))
	result := &MetricDistribution{Available: len(ordered), Min: ordered[0], Mean: mean, Median: median, Max: ordered[len(ordered)-1], PopulationStdDev: math.Sqrt(variance)}
	if err := result.Validate(); err != nil {
		return nil, err
	}
	return result, nil
}

type savingsValues struct {
	actualCost, estimatedCost                             []float64
	measuredInput, measuredOutput                         []float64
	estimatedInput, estimatedOutput                       []float64
	modelCalls, toolCalls, filesRead, bytesRead, duration []float64
}

type savingsGroup struct {
	summary VariantSavingsDistribution
	values  savingsValues
}

func savingsKeyFromComparison(comparison VariantEfficiencyComparison) string {
	return comparison.BaselineVariantID + "\x00" + comparison.BaselineConfigurationDigest + "\x00" + comparison.CandidateVariantID + "\x00" + comparison.CandidateConfigurationDigest
}

func appendComparisonSavings(values *savingsValues, comparison VariantEfficiencyComparison) {
	values.actualCost = appendOptionalFloat(values.actualCost, comparison.ActualCostSaving)
	values.estimatedCost = appendOptionalFloat(values.estimatedCost, comparison.EstimatedCostSaving)
	values.measuredInput = appendOptionalFloat(values.measuredInput, comparison.MeasuredInputTokensSaving)
	values.measuredOutput = appendOptionalFloat(values.measuredOutput, comparison.MeasuredOutputTokensSaving)
	values.estimatedInput = appendOptionalFloat(values.estimatedInput, comparison.EstimatedInputTokensSaving)
	values.estimatedOutput = appendOptionalFloat(values.estimatedOutput, comparison.EstimatedOutputTokensSaving)
	values.modelCalls = appendOptionalFloat(values.modelCalls, comparison.ModelCallsSaving)
	values.toolCalls = appendOptionalFloat(values.toolCalls, comparison.ToolCallsSaving)
	values.filesRead = appendOptionalFloat(values.filesRead, comparison.FilesReadSaving)
	values.bytesRead = appendOptionalFloat(values.bytesRead, comparison.BytesReadSaving)
	values.duration = appendOptionalFloat(values.duration, comparison.DurationSaving)
}

func (v savingsValues) distributions() (VariantSavingsDistribution, error) {
	var result VariantSavingsDistribution
	var err error
	if result.ActualCostSaving, err = makeSavingsDistribution(v.actualCost); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.EstimatedCostSaving, err = makeSavingsDistribution(v.estimatedCost); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.MeasuredInputTokensSaving, err = makeSavingsDistribution(v.measuredInput); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.MeasuredOutputTokensSaving, err = makeSavingsDistribution(v.measuredOutput); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.EstimatedInputTokensSaving, err = makeSavingsDistribution(v.estimatedInput); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.EstimatedOutputTokensSaving, err = makeSavingsDistribution(v.estimatedOutput); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.ModelCallsSaving, err = makeSavingsDistribution(v.modelCalls); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.ToolCallsSaving, err = makeSavingsDistribution(v.toolCalls); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.FilesReadSaving, err = makeSavingsDistribution(v.filesRead); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.BytesReadSaving, err = makeSavingsDistribution(v.bytesRead); err != nil {
		return VariantSavingsDistribution{}, err
	}
	if result.DurationSaving, err = makeSavingsDistribution(v.duration); err != nil {
		return VariantSavingsDistribution{}, err
	}
	return result, nil
}

func appendOptionalFloat(values []float64, value *float64) []float64 {
	if value == nil {
		return values
	}
	return append(values, *value)
}

func executionConfigurationIdentities(report VariantExecutionReport) []string {
	identities := make([]string, 0)
	for _, caseReport := range report.Cases {
		for _, execution := range caseReport.Executions {
			identities = append(identities, execution.VariantID+"\x00"+string(execution.VariantKind)+"\x00"+execution.ConfigurationID+"\x00"+execution.ConfigurationVersion+"\x00"+execution.ConfigurationDigest)
		}
	}
	sort.Strings(identities)
	return identities
}
