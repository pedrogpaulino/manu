package evaluation

import (
	"math"
	"sort"
)

// VariantEfficiencyDuration is a per-success duration with an explicit,
// canonical unit. Values are averages and may therefore be fractional.
type VariantEfficiencyDuration struct {
	Value float64             `json:"value"`
	Unit  VariantDurationUnit `json:"unit"`
}

// VariantEfficiencyAggregate groups attempts by the complete safe identity
// of a variant and reports effort per successful task. Every optional metric
// is independent; a missing observation leaves only that field absent.
type VariantEfficiencyAggregate struct {
	VariantID            string      `json:"variant_id"`
	VariantKind          VariantKind `json:"variant_kind"`
	ConfigurationID      string      `json:"configuration_id"`
	ConfigurationVersion string      `json:"configuration_version"`
	ConfigurationDigest  string      `json:"configuration_digest"`
	AttemptedTasks       int         `json:"attempted_tasks"`
	SuccessfulTasks      int         `json:"successful_tasks"`

	ActualCostUSDPerSuccess         *float64                   `json:"actual_cost_usd_per_success,omitempty"`
	EstimatedCostUSDPerSuccess      *float64                   `json:"estimated_cost_usd_per_success,omitempty"`
	MeasuredInputTokensPerSuccess   *float64                   `json:"measured_input_tokens_per_success,omitempty"`
	MeasuredOutputTokensPerSuccess  *float64                   `json:"measured_output_tokens_per_success,omitempty"`
	EstimatedInputTokensPerSuccess  *float64                   `json:"estimated_input_tokens_per_success,omitempty"`
	EstimatedOutputTokensPerSuccess *float64                   `json:"estimated_output_tokens_per_success,omitempty"`
	ModelCallsPerSuccess            *float64                   `json:"model_calls_per_success,omitempty"`
	ToolCallsPerSuccess             *float64                   `json:"tool_calls_per_success,omitempty"`
	FilesReadPerSuccess             *float64                   `json:"files_read_per_success,omitempty"`
	BytesReadPerSuccess             *float64                   `json:"bytes_read_per_success,omitempty"`
	DurationPerSuccess              *VariantEfficiencyDuration `json:"duration_per_success,omitempty"`
}

// VariantEfficiencyComparison compares one case's candidate to its direct
// source baseline. Savings are dimensionless and are independent per metric.
type VariantEfficiencyComparison struct {
	CaseID         string `json:"case_id"`
	CaseVersion    int    `json:"case_version"`
	CorpusID       string `json:"corpus_id"`
	CorpusRevision string `json:"corpus_revision"`
	SourceID       string `json:"source_id"`
	SourceRevision string `json:"source_revision"`

	BaselineVariantID            string      `json:"baseline_variant_id"`
	BaselineVariantKind          VariantKind `json:"baseline_variant_kind"`
	BaselineConfigurationDigest  string      `json:"baseline_configuration_digest"`
	CandidateVariantID           string      `json:"candidate_variant_id"`
	CandidateVariantKind         VariantKind `json:"candidate_variant_kind"`
	CandidateConfigurationDigest string      `json:"candidate_configuration_digest"`
	Comparable                   bool        `json:"comparable"`
	Reason                       string      `json:"reason,omitempty"`

	ActualCostSaving            *float64 `json:"actual_cost_saving,omitempty"`
	EstimatedCostSaving         *float64 `json:"estimated_cost_saving,omitempty"`
	MeasuredInputTokensSaving   *float64 `json:"measured_input_tokens_saving,omitempty"`
	MeasuredOutputTokensSaving  *float64 `json:"measured_output_tokens_saving,omitempty"`
	EstimatedInputTokensSaving  *float64 `json:"estimated_input_tokens_saving,omitempty"`
	EstimatedOutputTokensSaving *float64 `json:"estimated_output_tokens_saving,omitempty"`
	ModelCallsSaving            *float64 `json:"model_calls_saving,omitempty"`
	ToolCallsSaving             *float64 `json:"tool_calls_saving,omitempty"`
	FilesReadSaving             *float64 `json:"files_read_saving,omitempty"`
	BytesReadSaving             *float64 `json:"bytes_read_saving,omitempty"`
	DurationSaving              *float64 `json:"duration_saving,omitempty"`
}

// VariantEfficiencyReport contains per-identity aggregates and per-case
// comparisons. It has no quality score, economy aggregate, or raw content.
type VariantEfficiencyReport struct {
	Aggregates  []VariantEfficiencyAggregate  `json:"aggregates"`
	Comparisons []VariantEfficiencyComparison `json:"comparisons"`
}

const (
	efficiencyReasonBaselineNotSuccessful  = "baseline_not_successful"
	efficiencyReasonCandidateNotSuccessful = "candidate_not_successful"
	efficiencyReasonBothNotSuccessful      = "baseline_and_candidate_not_successful"
)

// Validate checks aggregate and comparison domains without recomputing them
// against a case set.
func (r VariantEfficiencyReport) Validate() error {
	_, err := r.Normalize()
	return err
}

// Clone returns a detached efficiency report.
func (r VariantEfficiencyReport) Clone() VariantEfficiencyReport {
	clone := r
	clone.Aggregates = append([]VariantEfficiencyAggregate(nil), r.Aggregates...)
	for index := range clone.Aggregates {
		clone.Aggregates[index] = cloneEfficiencyAggregate(r.Aggregates[index])
	}
	clone.Comparisons = append([]VariantEfficiencyComparison(nil), r.Comparisons...)
	for index := range clone.Comparisons {
		clone.Comparisons[index] = cloneEfficiencyComparison(r.Comparisons[index])
	}
	return clone
}

// Normalize validates and returns a detached deterministic efficiency report.
func (r VariantEfficiencyReport) Normalize() (VariantEfficiencyReport, error) {
	normalized := r.Clone()
	if len(normalized.Aggregates) == 0 || len(normalized.Aggregates) > maxListItems || len(normalized.Comparisons) > maxListItems*maxListItems {
		return VariantEfficiencyReport{}, ErrInvalidVariantResult
	}
	sort.SliceStable(normalized.Aggregates, func(left, right int) bool {
		return efficiencyAggregateKey(normalized.Aggregates[left]) < efficiencyAggregateKey(normalized.Aggregates[right])
	})
	seenAggregates := make(map[string]struct{}, len(normalized.Aggregates))
	for _, aggregate := range normalized.Aggregates {
		if err := aggregate.Validate(); err != nil {
			return VariantEfficiencyReport{}, ErrInvalidVariantResult
		}
		key := efficiencyAggregateKey(aggregate)
		if _, exists := seenAggregates[key]; exists {
			return VariantEfficiencyReport{}, ErrInvalidVariantResult
		}
		seenAggregates[key] = struct{}{}
	}
	sort.SliceStable(normalized.Comparisons, func(left, right int) bool {
		return efficiencyComparisonKey(normalized.Comparisons[left]) < efficiencyComparisonKey(normalized.Comparisons[right])
	})
	seenComparisons := make(map[string]struct{}, len(normalized.Comparisons))
	for _, comparison := range normalized.Comparisons {
		if err := comparison.Validate(); err != nil {
			return VariantEfficiencyReport{}, ErrInvalidVariantResult
		}
		key := efficiencyComparisonKey(comparison)
		if _, exists := seenComparisons[key]; exists {
			return VariantEfficiencyReport{}, ErrInvalidVariantResult
		}
		seenComparisons[key] = struct{}{}
	}
	return normalized, nil
}

func (a VariantEfficiencyAggregate) Validate() error {
	_, err := normalizeEfficiencyAggregate(a)
	return err
}

func (a VariantEfficiencyAggregate) Normalize() (VariantEfficiencyAggregate, error) {
	return normalizeEfficiencyAggregate(a)
}

func normalizeEfficiencyAggregate(a VariantEfficiencyAggregate) (VariantEfficiencyAggregate, error) {
	normalized := cloneEfficiencyAggregate(a)
	if !validEvaluationIdentity(normalized.VariantID) || !validVariantKind(normalized.VariantKind) ||
		!validEvaluationIdentity(normalized.ConfigurationID) || !validEvaluationIdentity(normalized.ConfigurationVersion) ||
		!isVariantSHA256(normalized.ConfigurationDigest) || normalized.AttemptedTasks <= 0 ||
		normalized.SuccessfulTasks < 0 || normalized.SuccessfulTasks > normalized.AttemptedTasks {
		return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
	}
	metrics := []*float64{
		normalized.ActualCostUSDPerSuccess, normalized.EstimatedCostUSDPerSuccess,
		normalized.MeasuredInputTokensPerSuccess, normalized.MeasuredOutputTokensPerSuccess,
		normalized.EstimatedInputTokensPerSuccess, normalized.EstimatedOutputTokensPerSuccess,
		normalized.ModelCallsPerSuccess, normalized.ToolCallsPerSuccess,
		normalized.FilesReadPerSuccess, normalized.BytesReadPerSuccess,
	}
	for _, value := range metrics {
		if value != nil && (!finiteEfficiencyValue(*value) || *value < 0) {
			return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
		}
	}
	if normalized.DurationPerSuccess != nil {
		if !validDurationUnit(normalized.DurationPerSuccess.Unit) || !finiteEfficiencyValue(normalized.DurationPerSuccess.Value) || normalized.DurationPerSuccess.Value < 0 {
			return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
		}
	}
	if normalized.SuccessfulTasks == 0 {
		for _, value := range metrics {
			if value != nil {
				return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
			}
		}
		if normalized.DurationPerSuccess != nil {
			return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
		}
	}
	return normalized, nil
}

func (c VariantEfficiencyComparison) Validate() error {
	_, err := normalizeEfficiencyComparison(c)
	return err
}

func (c VariantEfficiencyComparison) Normalize() (VariantEfficiencyComparison, error) {
	return normalizeEfficiencyComparison(c)
}

func normalizeEfficiencyComparison(c VariantEfficiencyComparison) (VariantEfficiencyComparison, error) {
	normalized := cloneEfficiencyComparison(c)
	if !validEvaluationIdentity(normalized.CaseID) || normalized.CaseVersion < 1 ||
		!validEvaluationIdentity(normalized.CorpusID) || !validEvaluationIdentity(normalized.CorpusRevision) ||
		!validEvaluationIdentity(normalized.SourceID) || !validEvaluationIdentity(normalized.SourceRevision) ||
		!validEvaluationIdentity(normalized.BaselineVariantID) || normalized.BaselineVariantKind != VariantDirectSource ||
		!isVariantSHA256(normalized.BaselineConfigurationDigest) || !validEvaluationIdentity(normalized.CandidateVariantID) ||
		!validVariantKind(normalized.CandidateVariantKind) || normalized.CandidateVariantKind == VariantDirectSource ||
		!isVariantSHA256(normalized.CandidateConfigurationDigest) {
		return VariantEfficiencyComparison{}, ErrInvalidVariantResult
	}
	if normalized.BaselineVariantID == normalized.CandidateVariantID {
		return VariantEfficiencyComparison{}, ErrInvalidVariantResult
	}
	if normalized.Comparable {
		if normalized.Reason != "" {
			return VariantEfficiencyComparison{}, ErrInvalidVariantResult
		}
	} else {
		if !validEfficiencyReason(normalized.Reason) || anyEfficiencySaving(normalized) {
			return VariantEfficiencyComparison{}, ErrInvalidVariantResult
		}
	}
	for _, value := range efficiencySavings(normalized) {
		if value != nil && (!finiteEfficiencyValue(*value) || *value > 1) {
			return VariantEfficiencyComparison{}, ErrInvalidVariantResult
		}
	}
	return normalized, nil
}

// deriveVariantEfficiency derives aggregates and per-case comparisons from
// already validated isolated records. It does not read case prose/content.
func deriveVariantEfficiency(cases []VariantCaseReport) (VariantEfficiencyReport, error) {
	if len(cases) == 0 || len(cases) > maxCases {
		return VariantEfficiencyReport{}, ErrInvalidVariantResult
	}
	orderedCases := append([]VariantCaseReport(nil), cases...)
	sort.SliceStable(orderedCases, func(left, right int) bool {
		if orderedCases[left].CaseID != orderedCases[right].CaseID {
			return orderedCases[left].CaseID < orderedCases[right].CaseID
		}
		return orderedCases[left].CaseVersion < orderedCases[right].CaseVersion
	})
	type groupKey struct {
		variantID   string
		variantKind VariantKind
		digest      string
	}
	groups := make(map[groupKey][]VariantExecutionRecord)
	comparisons := make([]VariantEfficiencyComparison, 0)
	for _, caseReport := range orderedCases {
		if err := caseReport.Validate(); err != nil {
			return VariantEfficiencyReport{}, ErrInvalidVariantResult
		}
		records := append([]VariantExecutionRecord(nil), caseReport.Executions...)
		sort.SliceStable(records, func(left, right int) bool {
			return records[left].VariantID < records[right].VariantID
		})
		var baseline *VariantExecutionRecord
		for index := range records {
			record := &records[index]
			key := groupKey{variantID: record.VariantID, variantKind: record.VariantKind, digest: record.ConfigurationDigest}
			groups[key] = append(groups[key], *record)
			if record.VariantKind == VariantDirectSource {
				baseline = record
			}
		}
		if baseline == nil {
			return VariantEfficiencyReport{}, ErrInvalidVariantResult
		}
		for _, candidate := range records {
			if candidate.VariantKind == VariantDirectSource {
				continue
			}
			comparison, err := deriveVariantEfficiencyComparison(caseReport, *baseline, candidate)
			if err != nil {
				return VariantEfficiencyReport{}, err
			}
			comparisons = append(comparisons, comparison)
		}
	}
	aggregates := make([]VariantEfficiencyAggregate, 0, len(groups))
	for key, records := range groups {
		sort.SliceStable(records, func(left, right int) bool {
			if records[left].CaseID != records[right].CaseID {
				return records[left].CaseID < records[right].CaseID
			}
			if records[left].CaseVersion != records[right].CaseVersion {
				return records[left].CaseVersion < records[right].CaseVersion
			}
			return records[left].VariantID < records[right].VariantID
		})
		aggregate, err := deriveVariantEfficiencyAggregate(key.variantID, key.variantKind, key.digest, records)
		if err != nil {
			return VariantEfficiencyReport{}, err
		}
		aggregates = append(aggregates, aggregate)
	}
	report := VariantEfficiencyReport{Aggregates: aggregates, Comparisons: comparisons}
	return report.Normalize()
}

func deriveVariantEfficiencyAggregate(variantID string, kind VariantKind, digest string, records []VariantExecutionRecord) (VariantEfficiencyAggregate, error) {
	if len(records) == 0 {
		return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
	}
	first := records[0]
	for _, record := range records {
		if record.ConfigurationDigest != digest || record.ConfigurationID != first.ConfigurationID || record.ConfigurationVersion != first.ConfigurationVersion {
			return VariantEfficiencyAggregate{}, ErrInvalidVariantResult
		}
	}
	successful := 0
	for _, record := range records {
		if variantExecutionIsSuccess(record) {
			successful++
		}
	}
	aggregate := VariantEfficiencyAggregate{
		VariantID: variantID, VariantKind: kind,
		ConfigurationID: first.ConfigurationID, ConfigurationVersion: first.ConfigurationVersion,
		ConfigurationDigest: digest, AttemptedTasks: len(records), SuccessfulTasks: successful,
	}
	if successful == 0 {
		return aggregate, nil
	}
	aggregate.ActualCostUSDPerSuccess = aggregateCost(records, successful, true)
	aggregate.EstimatedCostUSDPerSuccess = aggregateCost(records, successful, false)
	aggregate.MeasuredInputTokensPerSuccess = aggregateTokens(records, successful, true, true)
	aggregate.MeasuredOutputTokensPerSuccess = aggregateTokens(records, successful, true, false)
	aggregate.EstimatedInputTokensPerSuccess = aggregateTokens(records, successful, false, true)
	aggregate.EstimatedOutputTokensPerSuccess = aggregateTokens(records, successful, false, false)
	aggregate.ModelCallsPerSuccess = aggregateCounter(records, successful, func(metrics *VariantMetrics) *int64 { return metrics.ModelCalls })
	aggregate.ToolCallsPerSuccess = aggregateCounter(records, successful, func(metrics *VariantMetrics) *int64 { return metrics.ToolCalls })
	aggregate.FilesReadPerSuccess = aggregateCounter(records, successful, func(metrics *VariantMetrics) *int64 { return metrics.FilesRead })
	aggregate.BytesReadPerSuccess = aggregateCounter(records, successful, func(metrics *VariantMetrics) *int64 { return metrics.BytesRead })
	duration, err := aggregateDuration(records, successful)
	if err != nil {
		return VariantEfficiencyAggregate{}, err
	}
	aggregate.DurationPerSuccess = duration
	return aggregate, nil
}

func deriveVariantEfficiencyComparison(caseReport VariantCaseReport, baseline, candidate VariantExecutionRecord) (VariantEfficiencyComparison, error) {
	comparison := VariantEfficiencyComparison{
		CaseID: caseReport.CaseID, CaseVersion: caseReport.CaseVersion,
		CorpusID: caseReport.CorpusID, CorpusRevision: caseReport.CorpusRevision,
		SourceID: caseReport.SourceID, SourceRevision: caseReport.SourceRevision,
		BaselineVariantID: baseline.VariantID, BaselineVariantKind: baseline.VariantKind,
		BaselineConfigurationDigest: baseline.ConfigurationDigest,
		CandidateVariantID:          candidate.VariantID, CandidateVariantKind: candidate.VariantKind,
		CandidateConfigurationDigest: candidate.ConfigurationDigest,
	}
	baselineSuccess := variantExecutionIsSuccess(baseline)
	candidateSuccess := variantExecutionIsSuccess(candidate)
	if !baselineSuccess || !candidateSuccess {
		comparison.Reason = efficiencyReasonForPair(baselineSuccess, candidateSuccess)
		return comparison.Normalize()
	}
	comparison.Comparable = true
	var err error
	comparison.ActualCostSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) { return recordCost(record, true) })
	comparison.EstimatedCostSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) { return recordCost(record, false) })
	comparison.MeasuredInputTokensSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordToken(record, true, true)
	})
	comparison.MeasuredOutputTokensSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordToken(record, true, false)
	})
	comparison.EstimatedInputTokensSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordToken(record, false, true)
	})
	comparison.EstimatedOutputTokensSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordToken(record, false, false)
	})
	comparison.ModelCallsSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordCounter(record, func(metrics *VariantMetrics) *int64 { return metrics.ModelCalls })
	})
	comparison.ToolCallsSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordCounter(record, func(metrics *VariantMetrics) *int64 { return metrics.ToolCalls })
	})
	comparison.FilesReadSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordCounter(record, func(metrics *VariantMetrics) *int64 { return metrics.FilesRead })
	})
	comparison.BytesReadSaving = comparisonMetricSaving(baseline, candidate, func(record VariantExecutionRecord) (efficiencyObservation, error) {
		return recordCounter(record, func(metrics *VariantMetrics) *int64 { return metrics.BytesRead })
	})
	comparison.DurationSaving, err = comparisonDurationSaving(baseline, candidate)
	if err != nil {
		return VariantEfficiencyComparison{}, err
	}
	return comparison.Normalize()
}

func variantExecutionIsSuccess(record VariantExecutionRecord) bool {
	if record.Quality == nil || !record.Quality.Completed || !record.Quality.Correct || !record.Quality.RequiredCriteriaPassed || !record.Quality.Abstention.Appropriate {
		return false
	}
	return qualityRateIsOne(record.Quality.Evidence.Recall) && qualityRateIsOne(record.Quality.Gaps.Recall) && qualityRateIsOne(record.Quality.Citations.Rate)
}

func qualityRateIsOne(value *float64) bool {
	return value != nil && *value == 1
}

func aggregateCounter(records []VariantExecutionRecord, successful int, getter func(*VariantMetrics) *int64) *float64 {
	if successful == 0 {
		return nil
	}
	var observerID, observerVersion string
	var sum int64
	for index, record := range records {
		if record.Result.Metrics == nil {
			return nil
		}
		metrics := record.Result.Metrics
		if index == 0 {
			observerID, observerVersion = metrics.ObserverID, metrics.ObserverVersion
		} else if metrics.ObserverID != observerID || metrics.ObserverVersion != observerVersion {
			return nil
		}
		value := getter(metrics)
		if value == nil || !addMetricInt64(&sum, *value) {
			return nil
		}
	}
	return efficiencyAverageInt64(sum, successful)
}

func aggregateTokens(records []VariantExecutionRecord, successful int, measured, input bool) *float64 {
	if successful == 0 {
		return nil
	}
	var sourceID, sourceVersion string
	var sum int64
	for index, record := range records {
		if record.Result.Metrics == nil {
			return nil
		}
		metrics := record.Result.Metrics
		var groupSourceID, groupSourceVersion string
		var value *int64
		if measured {
			if metrics.MeasuredTokens == nil {
				return nil
			}
			groupSourceID, groupSourceVersion = metrics.MeasuredTokens.SourceID, metrics.MeasuredTokens.SourceVersion
			if input {
				value = metrics.MeasuredTokens.InputTokens
			} else {
				value = metrics.MeasuredTokens.OutputTokens
			}
		} else {
			if metrics.EstimatedTokens == nil {
				return nil
			}
			groupSourceID, groupSourceVersion = metrics.EstimatedTokens.EstimatorID, metrics.EstimatedTokens.EstimatorVersion
			if input {
				value = metrics.EstimatedTokens.InputTokens
			} else {
				value = metrics.EstimatedTokens.OutputTokens
			}
		}
		if index == 0 {
			sourceID, sourceVersion = groupSourceID, groupSourceVersion
		} else if groupSourceID != sourceID || groupSourceVersion != sourceVersion {
			return nil
		}
		if value == nil || !addMetricInt64(&sum, *value) {
			return nil
		}
	}
	return efficiencyAverageInt64(sum, successful)
}

func aggregateCost(records []VariantExecutionRecord, successful int, actual bool) *float64 {
	if successful == 0 {
		return nil
	}
	var sourceID, sourceVersion string
	var sum float64
	for index, record := range records {
		if record.Result.Metrics == nil {
			return nil
		}
		metrics := record.Result.Metrics
		var id, version string
		if actual {
			if metrics.ActualCost == nil {
				return nil
			}
			id, version = metrics.ActualCost.SourceID, metrics.ActualCost.SourceVersion
			cost := metrics.ActualCost.USD
			if !addFiniteNonNegative(&sum, cost) {
				return nil
			}
		} else {
			if metrics.EstimatedCost == nil {
				return nil
			}
			id, version = metrics.EstimatedCost.EstimatorID, metrics.EstimatedCost.EstimatorVersion
			cost := metrics.EstimatedCost.USD
			if !addFiniteNonNegative(&sum, cost) {
				return nil
			}
		}
		if index == 0 {
			sourceID, sourceVersion = id, version
		} else if id != sourceID || version != sourceVersion {
			return nil
		}
	}
	return efficiencyAverage(sum, successful)
}

func aggregateDuration(records []VariantExecutionRecord, successful int) (*VariantEfficiencyDuration, error) {
	if successful == 0 {
		return nil, nil
	}
	var observerID, observerVersion string
	var total int64
	for index, record := range records {
		if record.Result.Metrics == nil || record.Result.Metrics.Duration == nil {
			return nil, nil
		}
		metrics := record.Result.Metrics
		if index == 0 {
			observerID, observerVersion = metrics.ObserverID, metrics.ObserverVersion
		} else if metrics.ObserverID != observerID || metrics.ObserverVersion != observerVersion {
			return nil, nil
		}
		nanoseconds, err := durationNanoseconds(*metrics.Duration)
		if err != nil {
			return nil, nil
		}
		if nanoseconds > maxEfficiencyInt64-total {
			return nil, nil
		}
		total += nanoseconds
	}
	value := float64(total) / float64(successful)
	if !finiteEfficiencyValue(value) {
		return nil, ErrInvalidVariantResult
	}
	return &VariantEfficiencyDuration{Value: value, Unit: VariantDurationNanoseconds}, nil
}

func durationNanoseconds(duration VariantDuration) (int64, error) {
	if duration.Value < 0 || !validDurationUnit(duration.Unit) {
		return 0, ErrInvalidVariantResult
	}
	multiplier := int64(1)
	switch duration.Unit {
	case VariantDurationMicroseconds:
		multiplier = 1_000
	case VariantDurationMilliseconds:
		multiplier = 1_000_000
	case VariantDurationSeconds:
		multiplier = 1_000_000_000
	}
	if duration.Value > maxEfficiencyInt64/multiplier {
		return 0, ErrInvalidVariantResult
	}
	return duration.Value * multiplier, nil
}

const maxEfficiencyInt64 = int64(1<<63 - 1)

type efficiencyObservation struct {
	value             *float64
	provenanceID      string
	provenanceVersion string
}

func comparisonMetricSaving(baseline, candidate VariantExecutionRecord, getter func(VariantExecutionRecord) (efficiencyObservation, error)) *float64 {
	baselineObservation, baselineErr := getter(baseline)
	candidateObservation, candidateErr := getter(candidate)
	if baselineErr != nil || candidateErr != nil ||
		baselineObservation.value == nil || candidateObservation.value == nil ||
		baselineObservation.provenanceID == "" || candidateObservation.provenanceID == "" ||
		baselineObservation.provenanceID != candidateObservation.provenanceID ||
		baselineObservation.provenanceVersion != candidateObservation.provenanceVersion {
		return nil
	}
	return efficiencySaving(candidateObservation.value, baselineObservation.value)
}

func comparisonDurationSaving(baseline, candidate VariantExecutionRecord) (*float64, error) {
	baselineObservation, baselineErr := recordDuration(baseline)
	candidateObservation, candidateErr := recordDuration(candidate)
	if baselineErr != nil || candidateErr != nil {
		return nil, nil
	}
	if baselineObservation.provenanceID == "" || candidateObservation.provenanceID == "" ||
		baselineObservation.provenanceID != candidateObservation.provenanceID ||
		baselineObservation.provenanceVersion != candidateObservation.provenanceVersion {
		return nil, nil
	}
	return efficiencySaving(candidateObservation.value, baselineObservation.value), nil
}

func recordCost(record VariantExecutionRecord, actual bool) (efficiencyObservation, error) {
	if record.Result.Metrics == nil {
		return efficiencyObservation{}, nil
	}
	if actual {
		if record.Result.Metrics.ActualCost == nil {
			return efficiencyObservation{}, nil
		}
		value := record.Result.Metrics.ActualCost.USD
		return efficiencyObservation{
			value:             &value,
			provenanceID:      record.Result.Metrics.ActualCost.SourceID,
			provenanceVersion: record.Result.Metrics.ActualCost.SourceVersion,
		}, nil
	}
	if record.Result.Metrics.EstimatedCost == nil {
		return efficiencyObservation{}, nil
	}
	value := record.Result.Metrics.EstimatedCost.USD
	return efficiencyObservation{
		value:             &value,
		provenanceID:      record.Result.Metrics.EstimatedCost.EstimatorID,
		provenanceVersion: record.Result.Metrics.EstimatedCost.EstimatorVersion,
	}, nil
}

func recordToken(record VariantExecutionRecord, measured, input bool) (efficiencyObservation, error) {
	if record.Result.Metrics == nil {
		return efficiencyObservation{}, nil
	}
	var value *int64
	var sourceID, sourceVersion string
	if measured {
		if record.Result.Metrics.MeasuredTokens == nil {
			return efficiencyObservation{}, nil
		}
		sourceID, sourceVersion = record.Result.Metrics.MeasuredTokens.SourceID, record.Result.Metrics.MeasuredTokens.SourceVersion
		if input {
			value = record.Result.Metrics.MeasuredTokens.InputTokens
		} else {
			value = record.Result.Metrics.MeasuredTokens.OutputTokens
		}
	} else {
		if record.Result.Metrics.EstimatedTokens == nil {
			return efficiencyObservation{}, nil
		}
		sourceID, sourceVersion = record.Result.Metrics.EstimatedTokens.EstimatorID, record.Result.Metrics.EstimatedTokens.EstimatorVersion
		if input {
			value = record.Result.Metrics.EstimatedTokens.InputTokens
		} else {
			value = record.Result.Metrics.EstimatedTokens.OutputTokens
		}
	}
	if value == nil {
		return efficiencyObservation{}, nil
	}
	result := float64(*value)
	return efficiencyObservation{value: &result, provenanceID: sourceID, provenanceVersion: sourceVersion}, nil
}

func recordCounter(record VariantExecutionRecord, getter func(*VariantMetrics) *int64) (efficiencyObservation, error) {
	if record.Result.Metrics == nil {
		return efficiencyObservation{}, nil
	}
	value := getter(record.Result.Metrics)
	if value == nil {
		return efficiencyObservation{}, nil
	}
	result := float64(*value)
	return efficiencyObservation{
		value:             &result,
		provenanceID:      record.Result.Metrics.ObserverID,
		provenanceVersion: record.Result.Metrics.ObserverVersion,
	}, nil
}

func recordDuration(record VariantExecutionRecord) (efficiencyObservation, error) {
	if record.Result.Metrics == nil || record.Result.Metrics.Duration == nil {
		return efficiencyObservation{}, nil
	}
	nanoseconds, err := durationNanoseconds(*record.Result.Metrics.Duration)
	if err != nil {
		return efficiencyObservation{}, err
	}
	result := float64(nanoseconds)
	return efficiencyObservation{
		value:             &result,
		provenanceID:      record.Result.Metrics.ObserverID,
		provenanceVersion: record.Result.Metrics.ObserverVersion,
	}, nil
}

func efficiencyAverage(sum float64, successful int) *float64 {
	if successful == 0 || !finiteEfficiencyValue(sum) {
		return nil
	}
	value := sum / float64(successful)
	if !finiteEfficiencyValue(value) {
		return nil
	}
	return &value
}

func efficiencyAverageInt64(sum int64, successful int) *float64 {
	if successful <= 0 {
		return nil
	}
	value := float64(sum) / float64(successful)
	if !finiteEfficiencyValue(value) {
		return nil
	}
	return &value
}

func addMetricInt64(sum *int64, value int64) bool {
	if value < 0 || *sum < 0 || value > maxEfficiencyInt64-*sum {
		return false
	}
	*sum += value
	return true
}

func addFiniteNonNegative(sum *float64, value float64) bool {
	if !finiteEfficiencyValue(value) || value < 0 || !finiteEfficiencyValue(*sum) {
		return false
	}
	*sum += value
	return finiteEfficiencyValue(*sum)
}

func efficiencySaving(candidate, baseline *float64) *float64 {
	if candidate == nil || baseline == nil || !finiteEfficiencyValue(*candidate) || !finiteEfficiencyValue(*baseline) || *baseline <= 0 {
		return nil
	}
	ratio := *candidate / *baseline
	if !finiteEfficiencyValue(ratio) {
		return nil
	}
	saving := 1 - ratio
	if !finiteEfficiencyValue(saving) {
		return nil
	}
	return &saving
}

func finiteEfficiencyValue(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func efficiencyReasonForPair(baselineSuccess, candidateSuccess bool) string {
	switch {
	case !baselineSuccess && !candidateSuccess:
		return efficiencyReasonBothNotSuccessful
	case !baselineSuccess:
		return efficiencyReasonBaselineNotSuccessful
	default:
		return efficiencyReasonCandidateNotSuccessful
	}
}

func validEfficiencyReason(reason string) bool {
	switch reason {
	case efficiencyReasonBaselineNotSuccessful, efficiencyReasonCandidateNotSuccessful, efficiencyReasonBothNotSuccessful:
		return true
	default:
		return false
	}
}

func efficiencySavings(c VariantEfficiencyComparison) []*float64 {
	return []*float64{
		c.ActualCostSaving, c.EstimatedCostSaving,
		c.MeasuredInputTokensSaving, c.MeasuredOutputTokensSaving,
		c.EstimatedInputTokensSaving, c.EstimatedOutputTokensSaving,
		c.ModelCallsSaving, c.ToolCallsSaving, c.FilesReadSaving,
		c.BytesReadSaving, c.DurationSaving,
	}
}

func anyEfficiencySaving(c VariantEfficiencyComparison) bool {
	for _, value := range efficiencySavings(c) {
		if value != nil {
			return true
		}
	}
	return false
}

func cloneEfficiencyAggregate(a VariantEfficiencyAggregate) VariantEfficiencyAggregate {
	clone := a
	clone.ActualCostUSDPerSuccess = cloneEfficiencyValue(a.ActualCostUSDPerSuccess)
	clone.EstimatedCostUSDPerSuccess = cloneEfficiencyValue(a.EstimatedCostUSDPerSuccess)
	clone.MeasuredInputTokensPerSuccess = cloneEfficiencyValue(a.MeasuredInputTokensPerSuccess)
	clone.MeasuredOutputTokensPerSuccess = cloneEfficiencyValue(a.MeasuredOutputTokensPerSuccess)
	clone.EstimatedInputTokensPerSuccess = cloneEfficiencyValue(a.EstimatedInputTokensPerSuccess)
	clone.EstimatedOutputTokensPerSuccess = cloneEfficiencyValue(a.EstimatedOutputTokensPerSuccess)
	clone.ModelCallsPerSuccess = cloneEfficiencyValue(a.ModelCallsPerSuccess)
	clone.ToolCallsPerSuccess = cloneEfficiencyValue(a.ToolCallsPerSuccess)
	clone.FilesReadPerSuccess = cloneEfficiencyValue(a.FilesReadPerSuccess)
	clone.BytesReadPerSuccess = cloneEfficiencyValue(a.BytesReadPerSuccess)
	if a.DurationPerSuccess != nil {
		duration := *a.DurationPerSuccess
		clone.DurationPerSuccess = &duration
	}
	return clone
}

func cloneEfficiencyComparison(c VariantEfficiencyComparison) VariantEfficiencyComparison {
	clone := c
	clone.ActualCostSaving = cloneEfficiencyValue(c.ActualCostSaving)
	clone.EstimatedCostSaving = cloneEfficiencyValue(c.EstimatedCostSaving)
	clone.MeasuredInputTokensSaving = cloneEfficiencyValue(c.MeasuredInputTokensSaving)
	clone.MeasuredOutputTokensSaving = cloneEfficiencyValue(c.MeasuredOutputTokensSaving)
	clone.EstimatedInputTokensSaving = cloneEfficiencyValue(c.EstimatedInputTokensSaving)
	clone.EstimatedOutputTokensSaving = cloneEfficiencyValue(c.EstimatedOutputTokensSaving)
	clone.ModelCallsSaving = cloneEfficiencyValue(c.ModelCallsSaving)
	clone.ToolCallsSaving = cloneEfficiencyValue(c.ToolCallsSaving)
	clone.FilesReadSaving = cloneEfficiencyValue(c.FilesReadSaving)
	clone.BytesReadSaving = cloneEfficiencyValue(c.BytesReadSaving)
	clone.DurationSaving = cloneEfficiencyValue(c.DurationSaving)
	return clone
}

func cloneEfficiencyValue(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func efficiencyAggregateKey(a VariantEfficiencyAggregate) string {
	return a.VariantID + "\x00" + string(a.VariantKind) + "\x00" + a.ConfigurationDigest
}

func efficiencyComparisonKey(c VariantEfficiencyComparison) string {
	return c.CaseID + "\x00" + formatEfficiencyInt(c.CaseVersion) + "\x00" + c.CandidateVariantID + "\x00" + string(c.CandidateVariantKind) + "\x00" + c.CandidateConfigurationDigest
}

func formatEfficiencyInt(value int) string {
	if value < 0 {
		return "-"
	}
	if value == 0 {
		return "0"
	}
	result := make([]byte, 0, 20)
	for value > 0 {
		result = append(result, byte('0'+value%10))
		value /= 10
	}
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return string(result)
}
