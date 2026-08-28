package evaluation

import (
	"math"
	"sort"
)

// VariantEvidenceQuality contains content-free evidence retrieval outcomes.
// K is derived from the number of evidence identities returned by an
// executor; it is never supplied as a second, potentially inconsistent,
// measurement.
type VariantEvidenceQuality struct {
	Expected  int      `json:"expected"`
	Retrieved int      `json:"retrieved"`
	Relevant  int      `json:"relevant"`
	K         int      `json:"k"`
	Recall    *float64 `json:"recall,omitempty"`
	Precision *float64 `json:"precision,omitempty"`
}

// VariantCitationQuality contains only counts and a derived rate for the
// structured citations returned by an executor.
type VariantCitationQuality struct {
	Total int      `json:"total"`
	Valid int      `json:"valid"`
	Rate  *float64 `json:"rate,omitempty"`
}

// VariantGapQuality records recognition of the curated gap identities.
type VariantGapQuality struct {
	Expected   int      `json:"expected"`
	Recognized int      `json:"recognized"`
	Recall     *float64 `json:"recall,omitempty"`
}

// VariantAbstentionQuality distinguishes expected abstention from the
// conclusion actually returned by the executor.
type VariantAbstentionQuality struct {
	Expected    bool `json:"expected"`
	Actual      bool `json:"actual"`
	Appropriate bool `json:"appropriate"`
}

// VariantCriterionEvaluation is the runner-derived result for one curated
// success criterion. Reason is a controlled code, never response content.
type VariantCriterionEvaluation struct {
	ID        string        `json:"id"`
	Kind      CriterionKind `json:"kind"`
	Required  bool          `json:"required"`
	Evaluated bool          `json:"evaluated"`
	Passed    bool          `json:"passed"`
	Reason    string        `json:"reason"`
}

// VariantQuality is derived by the runner from case references and the
// executor's safe identities. It is not a field an executor may declare.
type VariantQuality struct {
	Correct                bool                         `json:"correct"`
	Completed              bool                         `json:"completed"`
	RequiredCriteriaPassed bool                         `json:"required_criteria_passed"`
	Evidence               VariantEvidenceQuality       `json:"evidence"`
	Citations              VariantCitationQuality       `json:"citations"`
	Gaps                   VariantGapQuality            `json:"gaps"`
	Abstention             VariantAbstentionQuality     `json:"abstention"`
	Criteria               []VariantCriterionEvaluation `json:"criteria"`
}

const (
	qualityReasonCorrectness   = "claim_set_compared"
	qualityReasonCompletion    = "completion_state_compared"
	qualityReasonEvidence      = "evidence_ids_compared"
	qualityReasonCitation      = "citation_support_compared"
	qualityReasonGap           = "gap_ids_compared"
	qualityReasonAbstention    = "abstention_state_compared"
	qualityReasonAuthorization = "authorization_signal_unavailable"
)

// Validate checks derived quality arithmetic, intervals, and controlled
// criterion metadata. It deliberately does not recalculate quality without a
// case, because the case is held by the runner rather than in the report.
func (q VariantQuality) Validate() error {
	_, err := q.Normalize()
	return err
}

// Clone returns a detached quality value, including optional rate pointers.
func (q VariantQuality) Clone() VariantQuality {
	clone := q
	clone.Evidence.Recall = cloneQualityRate(q.Evidence.Recall)
	clone.Evidence.Precision = cloneQualityRate(q.Evidence.Precision)
	clone.Citations.Rate = cloneQualityRate(q.Citations.Rate)
	clone.Gaps.Recall = cloneQualityRate(q.Gaps.Recall)
	clone.Criteria = append([]VariantCriterionEvaluation(nil), q.Criteria...)
	return clone
}

// Normalize validates and returns a detached deterministic quality value.
func (q VariantQuality) Normalize() (VariantQuality, error) {
	normalized := q.Clone()
	var err error
	if normalized.Evidence, err = normalizeVariantEvidenceQuality(normalized.Evidence); err != nil {
		return VariantQuality{}, err
	}
	if normalized.Citations, err = normalizeVariantCitationQuality(normalized.Citations); err != nil {
		return VariantQuality{}, err
	}
	if normalized.Gaps, err = normalizeVariantGapQuality(normalized.Gaps); err != nil {
		return VariantQuality{}, err
	}
	if len(normalized.Criteria) > maxListItems {
		return VariantQuality{}, ErrInvalidVariantResult
	}
	sort.SliceStable(normalized.Criteria, func(left, right int) bool {
		return normalized.Criteria[left].ID < normalized.Criteria[right].ID
	})
	seen := make(map[string]struct{}, len(normalized.Criteria))
	requiredPassed := true
	for _, criterion := range normalized.Criteria {
		if err := validateVariantCriterionEvaluation(criterion); err != nil {
			return VariantQuality{}, err
		}
		if err := validateVariantCriterionCoherence(criterion, normalized); err != nil {
			return VariantQuality{}, err
		}
		if _, exists := seen[criterion.ID]; exists {
			return VariantQuality{}, ErrInvalidVariantResult
		}
		seen[criterion.ID] = struct{}{}
		if criterion.Required && (!criterion.Evaluated || !criterion.Passed) {
			requiredPassed = false
		}
	}
	if normalized.RequiredCriteriaPassed != requiredPassed {
		return VariantQuality{}, ErrInvalidVariantResult
	}
	return normalized, nil
}

func normalizeVariantEvidenceQuality(value VariantEvidenceQuality) (VariantEvidenceQuality, error) {
	if value.Expected < 0 || value.Retrieved < 0 || value.Relevant < 0 || value.K < 0 ||
		value.Relevant > value.Expected || value.Relevant > value.Retrieved || value.K != value.Retrieved {
		return VariantEvidenceQuality{}, ErrInvalidVariantResult
	}
	if value.Expected == 0 {
		if value.Recall != nil {
			return VariantEvidenceQuality{}, ErrInvalidVariantResult
		}
	} else if !qualityRateMatches(value.Recall, value.Relevant, value.Expected) {
		return VariantEvidenceQuality{}, ErrInvalidVariantResult
	}
	if value.Retrieved == 0 {
		if value.Precision != nil {
			return VariantEvidenceQuality{}, ErrInvalidVariantResult
		}
	} else if !qualityRateMatches(value.Precision, value.Relevant, value.Retrieved) {
		return VariantEvidenceQuality{}, ErrInvalidVariantResult
	}
	value.Recall = cloneQualityRate(value.Recall)
	value.Precision = cloneQualityRate(value.Precision)
	return value, nil
}

func normalizeVariantCitationQuality(value VariantCitationQuality) (VariantCitationQuality, error) {
	if value.Total < 0 || value.Valid < 0 || value.Valid > value.Total {
		return VariantCitationQuality{}, ErrInvalidVariantResult
	}
	if value.Total == 0 {
		if value.Rate != nil {
			return VariantCitationQuality{}, ErrInvalidVariantResult
		}
	} else if !qualityRateMatches(value.Rate, value.Valid, value.Total) {
		return VariantCitationQuality{}, ErrInvalidVariantResult
	}
	value.Rate = cloneQualityRate(value.Rate)
	return value, nil
}

func normalizeVariantGapQuality(value VariantGapQuality) (VariantGapQuality, error) {
	if value.Expected < 0 || value.Recognized < 0 || value.Recognized > value.Expected {
		return VariantGapQuality{}, ErrInvalidVariantResult
	}
	if value.Expected == 0 {
		if value.Recall != nil {
			return VariantGapQuality{}, ErrInvalidVariantResult
		}
	} else if !qualityRateMatches(value.Recall, value.Recognized, value.Expected) {
		return VariantGapQuality{}, ErrInvalidVariantResult
	}
	value.Recall = cloneQualityRate(value.Recall)
	return value, nil
}

func validateVariantCriterionEvaluation(value VariantCriterionEvaluation) error {
	if !validEvaluationIdentity(value.ID) || !validCriterionKind(value.Kind) || value.Reason == "" {
		return ErrInvalidVariantResult
	}
	if value.Kind == CriterionAuthorization {
		if value.Evaluated || value.Passed || value.Reason != qualityReasonAuthorization {
			return ErrInvalidVariantResult
		}
		return nil
	}
	if !value.Evaluated || value.Reason != qualityReasonForCriterion(value.Kind) {
		return ErrInvalidVariantResult
	}
	return nil
}

func validateVariantCriterionCoherence(value VariantCriterionEvaluation, quality VariantQuality) error {
	switch value.Kind {
	case CriterionCorrectness:
		if value.Passed != quality.Correct {
			return ErrInvalidVariantResult
		}
	case CriterionCompletion:
		if value.Passed != quality.Completed {
			return ErrInvalidVariantResult
		}
	case CriterionAbstention:
		if value.Passed != quality.Abstention.Appropriate {
			return ErrInvalidVariantResult
		}
	}
	return nil
}

func validCriterionKind(kind CriterionKind) bool {
	switch kind {
	case CriterionCorrectness, CriterionCompletion, CriterionEvidence, CriterionCitation,
		CriterionGap, CriterionAbstention, CriterionAuthorization:
		return true
	default:
		return false
	}
}

func qualityReasonForCriterion(kind CriterionKind) string {
	switch kind {
	case CriterionCorrectness:
		return qualityReasonCorrectness
	case CriterionCompletion:
		return qualityReasonCompletion
	case CriterionEvidence:
		return qualityReasonEvidence
	case CriterionCitation:
		return qualityReasonCitation
	case CriterionGap:
		return qualityReasonGap
	case CriterionAbstention:
		return qualityReasonAbstention
	case CriterionAuthorization:
		return qualityReasonAuthorization
	default:
		return ""
	}
}

func validQualityRate(value *float64) bool {
	return value != nil && !math.IsNaN(*value) && !math.IsInf(*value, 0) && *value >= 0 && *value <= 1
}

func qualityRateMatches(value *float64, numerator, denominator int) bool {
	if denominator == 0 {
		return value == nil
	}
	if !validQualityRate(value) {
		return false
	}
	return *value == float64(numerator)/float64(denominator)
}

func cloneQualityRate(value *float64) *float64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func qualityRate(numerator, denominator int) *float64 {
	if denominator == 0 {
		return nil
	}
	rate := float64(numerator) / float64(denominator)
	return &rate
}

// deriveVariantQuality computes all quality dimensions from curated case
// identities and the normalized, content-free result. It never reads a
// statement, summary, locator, or other textual field for matching.
func deriveVariantQuality(item EvaluationCase, result VariantExecutionResult) (VariantQuality, error) {
	if err := item.Validate(); err != nil {
		return VariantQuality{}, ErrInvalidVariantResult
	}
	normalizedResult, err := result.Normalize()
	if err != nil {
		return VariantQuality{}, ErrInvalidVariantResult
	}

	claimIDs := make(map[string]struct{}, len(normalizedResult.ClaimIDs))
	for _, id := range normalizedResult.ClaimIDs {
		claimIDs[id] = struct{}{}
	}
	referenceClaims := make(map[string]struct{}, len(item.ReferenceAnswer.ClaimIDs))
	for _, id := range item.ReferenceAnswer.ClaimIDs {
		referenceClaims[id] = struct{}{}
	}
	correct := len(claimIDs) == len(referenceClaims)
	if correct {
		for id := range referenceClaims {
			if _, ok := claimIDs[id]; !ok {
				correct = false
				break
			}
		}
	}

	evidenceIDs := make(map[string]struct{}, len(normalizedResult.EvidenceIDs))
	for _, id := range normalizedResult.EvidenceIDs {
		evidenceIDs[id] = struct{}{}
	}
	expectedEvidenceIDs := make(map[string]struct{}, len(item.ExpectedEvidence))
	for _, evidence := range item.ExpectedEvidence {
		if evidence.EvidenceID != "" {
			expectedEvidenceIDs[evidence.EvidenceID] = struct{}{}
		}
	}
	relevantEvidence := 0
	for id := range evidenceIDs {
		if _, ok := expectedEvidenceIDs[id]; ok {
			relevantEvidence++
		}
	}
	evidenceQuality := VariantEvidenceQuality{
		Expected:  len(item.ExpectedEvidence),
		Retrieved: len(normalizedResult.EvidenceIDs),
		Relevant:  relevantEvidence,
		K:         len(normalizedResult.EvidenceIDs),
		Recall:    qualityRate(relevantEvidence, len(item.ExpectedEvidence)),
		Precision: qualityRate(relevantEvidence, len(normalizedResult.EvidenceIDs)),
	}

	acceptableEvidence := make(map[string]map[string]struct{}, len(item.AcceptableClaims))
	for _, claim := range item.AcceptableClaims {
		allowed := make(map[string]struct{}, len(claim.EvidenceIDs))
		for _, id := range claim.EvidenceIDs {
			allowed[id] = struct{}{}
		}
		acceptableEvidence[claim.ClaimID] = allowed
	}
	validCitationEvidence := make(map[string]struct{})
	validCitations := 0
	for _, citation := range normalizedResult.Citations {
		if _, ok := claimIDs[citation.ClaimID]; !ok {
			continue
		}
		allowed, ok := acceptableEvidence[citation.ClaimID]
		if !ok {
			continue
		}
		if _, ok := evidenceIDs[citation.EvidenceID]; !ok {
			continue
		}
		if _, ok := allowed[citation.EvidenceID]; !ok {
			continue
		}
		validCitations++
		validCitationEvidence[citation.EvidenceID] = struct{}{}
	}
	citationQuality := VariantCitationQuality{
		Total: len(normalizedResult.Citations),
		Valid: validCitations,
		Rate:  qualityRate(validCitations, len(normalizedResult.Citations)),
	}

	gapIDs := make(map[string]struct{}, len(normalizedResult.GapIDs))
	for _, id := range normalizedResult.GapIDs {
		gapIDs[id] = struct{}{}
	}
	expectedGapIDs := make(map[string]struct{}, len(item.ExpectedGaps))
	for _, gap := range item.ExpectedGaps {
		expectedGapIDs[gap.GapID] = struct{}{}
	}
	recognizedGaps := 0
	for id := range gapIDs {
		if _, ok := expectedGapIDs[id]; ok {
			recognizedGaps++
		}
	}
	gapQuality := VariantGapQuality{
		Expected:   len(item.ExpectedGaps),
		Recognized: recognizedGaps,
		Recall:     qualityRate(recognizedGaps, len(item.ExpectedGaps)),
	}

	abstentionExpected := item.Kind == CaseKindAbstention
	for _, criterion := range item.Criteria.Items {
		if criterion.Kind == CriterionAbstention && criterion.Required {
			abstentionExpected = true
			break
		}
	}
	abstentionActual := normalizedResult.Conclusion == VariantConclusionAbstained
	evaluableCompletion := normalizedResult.Status == VariantStatusCompleted &&
		(normalizedResult.Conclusion == VariantConclusionPassed || abstentionActual)
	abstentionQuality := VariantAbstentionQuality{
		Expected:    abstentionExpected,
		Actual:      abstentionActual,
		Appropriate: evaluableCompletion && abstentionExpected == abstentionActual,
	}
	completed := normalizedResult.Status == VariantStatusCompleted &&
		(normalizedResult.Conclusion == VariantConclusionPassed ||
			(normalizedResult.Conclusion == VariantConclusionAbstained && abstentionExpected))

	quality := VariantQuality{
		Correct:    correct,
		Completed:  completed,
		Evidence:   evidenceQuality,
		Citations:  citationQuality,
		Gaps:       gapQuality,
		Abstention: abstentionQuality,
		Criteria:   make([]VariantCriterionEvaluation, 0, len(item.Criteria.Items)),
	}
	for _, criterion := range item.Criteria.Items {
		evaluation := VariantCriterionEvaluation{
			ID: criterion.ID, Kind: criterion.Kind, Required: criterion.Required,
			Evaluated: true, Reason: qualityReasonForCriterion(criterion.Kind),
		}
		switch criterion.Kind {
		case CriterionCorrectness:
			evaluation.Passed = quality.Correct
		case CriterionCompletion:
			evaluation.Passed = quality.Completed
		case CriterionEvidence:
			if len(criterion.EvidenceIDs) == 0 {
				evaluation.Passed = evidenceQuality.Recall != nil && *evidenceQuality.Recall == 1
			} else {
				evaluation.Passed = allVariantIDsPresent(criterion.EvidenceIDs, evidenceIDs)
			}
		case CriterionCitation:
			if len(criterion.EvidenceIDs) == 0 {
				evaluation.Passed = citationQuality.Rate != nil && *citationQuality.Rate == 1
			} else {
				evaluation.Passed = allVariantIDsPresent(criterion.EvidenceIDs, validCitationEvidence)
			}
		case CriterionGap:
			if len(criterion.GapIDs) == 0 {
				evaluation.Passed = gapQuality.Recall != nil && *gapQuality.Recall == 1
			} else {
				evaluation.Passed = allVariantIDsPresent(criterion.GapIDs, gapIDs)
			}
		case CriterionAbstention:
			evaluation.Passed = abstentionQuality.Appropriate
		case CriterionAuthorization:
			evaluation.Evaluated = false
			evaluation.Passed = false
			evaluation.Reason = qualityReasonAuthorization
		}
		quality.Criteria = append(quality.Criteria, evaluation)
	}
	quality.RequiredCriteriaPassed = true
	for _, criterion := range quality.Criteria {
		if criterion.Required && (!criterion.Evaluated || !criterion.Passed) {
			quality.RequiredCriteriaPassed = false
			break
		}
	}
	return quality.Normalize()
}

func allVariantIDsPresent(required []string, available map[string]struct{}) bool {
	for _, id := range required {
		if _, ok := available[id]; !ok {
			return false
		}
	}
	return true
}

func attachVariantQuality(item EvaluationCase, record *VariantExecutionRecord) error {
	if record == nil {
		return ErrInvalidVariantResult
	}
	quality, err := deriveVariantQuality(item, record.Result)
	if err != nil {
		return ErrInvalidVariantResult
	}
	detached := quality.Clone()
	record.Quality = &detached
	return nil
}
