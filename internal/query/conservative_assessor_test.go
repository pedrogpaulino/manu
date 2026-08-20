package query

import (
	"context"
	"errors"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestConservativeSupportAssessorUsesTypedKindWithoutTextHeuristics(t *testing.T) {
	assessor := ConservativeSupportAssessor{}
	result := QueryRetrievalResult{Candidates: []PackageCandidate{{Unit: evidence.EvidenceUnit{
		ContentState:     evidence.ContentStatePresent,
		Content:          "authorized text",
		Classification:   evidence.ClassificationSafeText,
		ExternalTransfer: evidence.DecisionAllow,
	}}}}

	assessment, err := assessor.Assess(context.Background(), QueryRetrievalInput{
		Question:     "what happened during the runtime?",
		QuestionKind: KnowledgeQuestionInventory,
	}, result)
	if err != nil {
		t.Fatalf("Assess() error = %v", err)
	}
	if assessment.Kind != KnowledgeQuestionInventory || assessment.Level != EvidenceSupportSufficient {
		t.Fatalf("assessment = %#v, want typed inventory/sufficient", assessment)
	}

	assessment, err = assessor.Assess(context.Background(), QueryRetrievalInput{
		Question:     "list the files",
		QuestionKind: KnowledgeQuestionObservedExecution,
	}, result)
	if err != nil {
		t.Fatalf("Assess(observed) error = %v", err)
	}
	if assessment.Kind != KnowledgeQuestionObservedExecution || assessment.Level != EvidenceSupportInsufficient {
		t.Fatalf("observed assessment = %#v, want typed observed/insufficient", assessment)
	}
}

func TestConservativeSupportAssessorRejectsRestrictedEvidenceAndInvalidInput(t *testing.T) {
	assessor := ConservativeSupportAssessor{}
	result := QueryRetrievalResult{Candidates: []PackageCandidate{{Unit: evidence.EvidenceUnit{
		ContentState:     evidence.ContentStateOmitted,
		Classification:   evidence.ClassificationProhibited,
		ExternalTransfer: evidence.DecisionDeny,
	}}}, LocalOnlyEvidenceCount: 1}
	assessment, err := assessor.Assess(context.Background(), QueryRetrievalInput{
		QuestionKind: KnowledgeQuestionPossibleFlow,
	}, result)
	if err != nil {
		t.Fatalf("Assess() error = %v", err)
	}
	if assessment.Level != EvidenceSupportInsufficient {
		t.Fatalf("assessment level = %q, want insufficient", assessment.Level)
	}

	_, err = assessor.Assess(context.Background(), QueryRetrievalInput{QuestionKind: KnowledgeQuestionKind("untyped")}, result)
	if !errors.Is(err, ErrInvalidAbstentionInput) {
		t.Fatalf("invalid kind error = %v, want ErrInvalidAbstentionInput", err)
	}
}

func TestConservativeSupportAssessorHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (ConservativeSupportAssessor{}).Assess(ctx, QueryRetrievalInput{QuestionKind: KnowledgeQuestionInventory}, QueryRetrievalResult{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v, want context.Canceled", err)
	}
}
