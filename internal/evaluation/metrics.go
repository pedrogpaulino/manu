package evaluation

import "math"

// VariantMeasuredTokens records provider-reported token usage. Its source is
// kept separate from the estimator metadata used by VariantEstimatedTokens.
type VariantMeasuredTokens struct {
	InputTokens   *int64 `json:"input_tokens,omitempty"`
	OutputTokens  *int64 `json:"output_tokens,omitempty"`
	SourceID      string `json:"source_id"`
	SourceVersion string `json:"source_version"`
}

// VariantEstimatedTokens records a deterministic token estimate. It is never
// combined with provider-reported usage by this package.
type VariantEstimatedTokens struct {
	InputTokens      *int64 `json:"input_tokens,omitempty"`
	OutputTokens     *int64 `json:"output_tokens,omitempty"`
	EstimatorID      string `json:"estimator_id"`
	EstimatorVersion string `json:"estimator_version"`
}

// VariantDurationUnit identifies the unit of a duration measurement.
type VariantDurationUnit string

const (
	VariantDurationNanoseconds  VariantDurationUnit = "nanoseconds"
	VariantDurationMicroseconds VariantDurationUnit = "microseconds"
	VariantDurationMilliseconds VariantDurationUnit = "milliseconds"
	VariantDurationSeconds      VariantDurationUnit = "seconds"
)

// VariantDuration records a non-negative duration without relying on Go's
// implementation-specific time.Duration JSON representation.
type VariantDuration struct {
	Value int64               `json:"value"`
	Unit  VariantDurationUnit `json:"unit"`
}

// VariantEstimatedCost records a cost estimated by an identified estimator.
type VariantEstimatedCost struct {
	USD              float64 `json:"usd"`
	EstimatorID      string  `json:"estimator_id"`
	EstimatorVersion string  `json:"estimator_version"`
}

// VariantActualCost records a cost reported by an identified usage source.
type VariantActualCost struct {
	USD           float64 `json:"usd"`
	SourceID      string  `json:"source_id"`
	SourceVersion string  `json:"source_version"`
}

// VariantMetrics contains optional, content-free execution observations. A
// nil pointer means unavailable; a pointer to zero means an observed zero.
// Estimated and measured groups remain independent by construction.
type VariantMetrics struct {
	ObserverID      string                  `json:"observer_id"`
	ObserverVersion string                  `json:"observer_version"`
	MeasuredTokens  *VariantMeasuredTokens  `json:"measured_tokens,omitempty"`
	EstimatedTokens *VariantEstimatedTokens `json:"estimated_tokens,omitempty"`
	ModelCalls      *int64                  `json:"model_calls,omitempty"`
	ToolCalls       *int64                  `json:"tool_calls,omitempty"`
	FilesRead       *int64                  `json:"files_read,omitempty"`
	BytesRead       *int64                  `json:"bytes_read,omitempty"`
	Duration        *VariantDuration        `json:"duration,omitempty"`
	EstimatedCost   *VariantEstimatedCost   `json:"estimated_cost,omitempty"`
	ActualCost      *VariantActualCost      `json:"actual_cost,omitempty"`
}

// Validate checks metric identities, numeric domains, and optional-group
// coherence without imposing an arbitrary upper bound on observations.
func (m VariantMetrics) Validate() error {
	_, err := m.Normalize()
	return err
}

// Clone returns a detached metrics value, preserving unavailable pointers and
// observed zero values exactly as supplied.
func (m VariantMetrics) Clone() VariantMetrics {
	clone := m
	if m.MeasuredTokens != nil {
		value := *m.MeasuredTokens
		value.InputTokens = cloneMetricCountWithoutValidation(m.MeasuredTokens.InputTokens)
		value.OutputTokens = cloneMetricCountWithoutValidation(m.MeasuredTokens.OutputTokens)
		clone.MeasuredTokens = &value
	}
	if m.EstimatedTokens != nil {
		value := *m.EstimatedTokens
		value.InputTokens = cloneMetricCountWithoutValidation(m.EstimatedTokens.InputTokens)
		value.OutputTokens = cloneMetricCountWithoutValidation(m.EstimatedTokens.OutputTokens)
		clone.EstimatedTokens = &value
	}
	clone.ModelCalls = cloneMetricCountWithoutValidation(m.ModelCalls)
	clone.ToolCalls = cloneMetricCountWithoutValidation(m.ToolCalls)
	clone.FilesRead = cloneMetricCountWithoutValidation(m.FilesRead)
	clone.BytesRead = cloneMetricCountWithoutValidation(m.BytesRead)
	if m.Duration != nil {
		value := *m.Duration
		clone.Duration = &value
	}
	if m.EstimatedCost != nil {
		value := *m.EstimatedCost
		clone.EstimatedCost = &value
	}
	if m.ActualCost != nil {
		value := *m.ActualCost
		clone.ActualCost = &value
	}
	return clone
}

// Normalize validates and returns a detached metrics value. Every pointer is
// copied so an executor cannot mutate a report or another variant through a
// shared result value.
func (m VariantMetrics) Normalize() (VariantMetrics, error) {
	if !validMetricIdentity(m.ObserverID) || !validMetricIdentity(m.ObserverVersion) {
		return VariantMetrics{}, ErrInvalidVariantResult
	}
	if m.MeasuredTokens == nil && m.EstimatedTokens == nil && m.ModelCalls == nil && m.ToolCalls == nil &&
		m.FilesRead == nil && m.BytesRead == nil && m.Duration == nil && m.EstimatedCost == nil && m.ActualCost == nil {
		return VariantMetrics{}, ErrInvalidVariantResult
	}
	normalized := VariantMetrics{ObserverID: m.ObserverID, ObserverVersion: m.ObserverVersion}
	if m.MeasuredTokens != nil {
		if !hasTokenCount(m.MeasuredTokens.InputTokens, m.MeasuredTokens.OutputTokens) ||
			validateTokenCount(m.MeasuredTokens.InputTokens) != nil || validateTokenCount(m.MeasuredTokens.OutputTokens) != nil ||
			!validMetricIdentity(m.MeasuredTokens.SourceID) || !validMetricIdentity(m.MeasuredTokens.SourceVersion) {
			return VariantMetrics{}, ErrInvalidVariantResult
		}
		value := *m.MeasuredTokens
		value.InputTokens = cloneMetricCountWithoutValidation(m.MeasuredTokens.InputTokens)
		value.OutputTokens = cloneMetricCountWithoutValidation(m.MeasuredTokens.OutputTokens)
		normalized.MeasuredTokens = &value
	}
	if m.EstimatedTokens != nil {
		if !hasTokenCount(m.EstimatedTokens.InputTokens, m.EstimatedTokens.OutputTokens) ||
			validateTokenCount(m.EstimatedTokens.InputTokens) != nil || validateTokenCount(m.EstimatedTokens.OutputTokens) != nil ||
			!validMetricIdentity(m.EstimatedTokens.EstimatorID) || !validMetricIdentity(m.EstimatedTokens.EstimatorVersion) {
			return VariantMetrics{}, ErrInvalidVariantResult
		}
		value := *m.EstimatedTokens
		value.InputTokens = cloneMetricCountWithoutValidation(m.EstimatedTokens.InputTokens)
		value.OutputTokens = cloneMetricCountWithoutValidation(m.EstimatedTokens.OutputTokens)
		normalized.EstimatedTokens = &value
	}
	var err error
	normalized.ModelCalls, err = cloneMetricCount(m.ModelCalls)
	if err != nil {
		return VariantMetrics{}, err
	}
	normalized.ToolCalls, err = cloneMetricCount(m.ToolCalls)
	if err != nil {
		return VariantMetrics{}, err
	}
	normalized.FilesRead, err = cloneMetricCount(m.FilesRead)
	if err != nil {
		return VariantMetrics{}, err
	}
	normalized.BytesRead, err = cloneMetricCount(m.BytesRead)
	if err != nil {
		return VariantMetrics{}, err
	}
	if m.Duration != nil {
		if m.Duration.Value < 0 || !validDurationUnit(m.Duration.Unit) {
			return VariantMetrics{}, ErrInvalidVariantResult
		}
		value := *m.Duration
		normalized.Duration = &value
	}
	if m.EstimatedCost != nil {
		if !validCost(m.EstimatedCost.USD) || !validMetricIdentity(m.EstimatedCost.EstimatorID) ||
			!validMetricIdentity(m.EstimatedCost.EstimatorVersion) {
			return VariantMetrics{}, ErrInvalidVariantResult
		}
		value := *m.EstimatedCost
		normalized.EstimatedCost = &value
	}
	if m.ActualCost != nil {
		if !validCost(m.ActualCost.USD) || !validMetricIdentity(m.ActualCost.SourceID) ||
			!validMetricIdentity(m.ActualCost.SourceVersion) {
			return VariantMetrics{}, ErrInvalidVariantResult
		}
		value := *m.ActualCost
		normalized.ActualCost = &value
	}
	return normalized, nil
}

func validMetricIdentity(value string) bool {
	return validateIdentifier("metric identity", value, maxCaseIDBytes) == nil
}

func hasTokenCount(input, output *int64) bool {
	return input != nil || output != nil
}

func validateTokenCount(value *int64) error {
	if value != nil && *value < 0 {
		return ErrInvalidVariantResult
	}
	return nil
}

func cloneMetricCount(value *int64) (*int64, error) {
	if value == nil {
		return nil, nil
	}
	if *value < 0 {
		return nil, ErrInvalidVariantResult
	}
	clone := *value
	return &clone, nil
}

func cloneMetricCountWithoutValidation(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func validDurationUnit(unit VariantDurationUnit) bool {
	switch unit {
	case VariantDurationNanoseconds, VariantDurationMicroseconds, VariantDurationMilliseconds, VariantDurationSeconds:
		return true
	default:
		return false
	}
}

func validCost(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}
