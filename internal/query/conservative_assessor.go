package query

import (
	"context"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

// ConservativeSupportAssessor is the default local support boundary used by
// the first runtime composition. It consumes only the typed question kind and
// bounded evidence metadata; it never classifies the natural-language
// question. Inventory and possible-flow questions may proceed when at least
// one transferable textual unit was retrieved. Observed execution and
// business intent require a future specialized evidence source and therefore
// abstain until one is explicitly wired.
type ConservativeSupportAssessor struct{}

var _ SupportAssessor = ConservativeSupportAssessor{}

// Assess returns a deterministic support level without inspecting question
// text. Restricted, omitted, or denied units cannot establish support for a
// provider call even if they remain useful as local-only audit material.
func (ConservativeSupportAssessor) Assess(ctx context.Context, input QueryRetrievalInput, result QueryRetrievalResult) (SupportAssessment, error) {
	if ctx == nil {
		return SupportAssessment{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return SupportAssessment{}, err
	}
	if !validKnowledgeQuestionKind(input.QuestionKind) {
		return SupportAssessment{}, ErrInvalidAbstentionInput
	}

	assessment := SupportAssessment{
		Kind:  input.QuestionKind,
		Level: EvidenceSupportNone,
	}
	if input.QuestionKind == KnowledgeQuestionObservedExecution || input.QuestionKind == KnowledgeQuestionBusinessIntent {
		assessment.Level = EvidenceSupportInsufficient
		return assessment, nil
	}
	for _, candidate := range result.Candidates {
		if transferableTextUnit(candidate.Unit) {
			assessment.Level = EvidenceSupportSufficient
			break
		}
	}
	if assessment.Level == EvidenceSupportNone && result.LocalOnlyEvidenceCount > 0 {
		assessment.Level = EvidenceSupportInsufficient
	}
	return assessment, nil
}

func transferableTextUnit(unit evidence.EvidenceUnit) bool {
	if unit.ExternalTransfer == evidence.DecisionDeny || unit.ContentState == evidence.ContentStateOmitted {
		return false
	}
	if unit.Classification != evidence.ClassificationUnknown && unit.Classification != evidence.ClassificationSafeText {
		return false
	}
	if unit.ContentState == evidence.ContentStatePresent {
		return unit.Content != ""
	}
	return unit.ContentState == evidence.ContentStateRedacted && unit.Content == evidence.RedactedContent
}
