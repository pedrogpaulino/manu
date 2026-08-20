package query

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalidAbstentionInput identifies a malformed pre-generation
	// assessment. It never contains question or evidence content.
	ErrInvalidAbstentionInput = errors.New("query: invalid abstention input")
)

// KnowledgeQuestionKind qualifies the kind of conclusion requested by a
// query. It is intentionally an enum: abstention must not infer a semantic
// kind from natural-language text.
type KnowledgeQuestionKind string

const (
	KnowledgeQuestionInventory         KnowledgeQuestionKind = "inventory"
	KnowledgeQuestionPossibleFlow      KnowledgeQuestionKind = "possible_flow"
	KnowledgeQuestionObservedExecution KnowledgeQuestionKind = "observed_execution"
	KnowledgeQuestionBusinessIntent    KnowledgeQuestionKind = "business_intent"
)

// QueryKind is a shorter name for KnowledgeQuestionKind.
type QueryKind = KnowledgeQuestionKind

const (
	QueryKindInventory         = KnowledgeQuestionInventory
	QueryKindPossibleFlow      = KnowledgeQuestionPossibleFlow
	QueryKindObservedExecution = KnowledgeQuestionObservedExecution
	QueryKindBusinessIntent    = KnowledgeQuestionBusinessIntent
)

// EvidenceSupportLevel is the caller's typed assessment of whether the
// transferable package is sufficient for the requested kind. The assessment
// is supplied by retrieval/coverage code; this package does not inspect or
// interpret question language.
type EvidenceSupportLevel string

const (
	EvidenceSupportNone         EvidenceSupportLevel = "none"
	EvidenceSupportInsufficient EvidenceSupportLevel = "insufficient"
	EvidenceSupportSufficient   EvidenceSupportLevel = "sufficient"
)

// SupportAssessment records the semantic kind and support level available to
// the pre-provider gate. A Possible Flow assessment cannot satisfy an
// Observed Execution question, even when both contain technical references.
type SupportAssessment struct {
	Kind  KnowledgeQuestionKind `json:"kind"`
	Level EvidenceSupportLevel  `json:"level"`
}

// AbstentionInput is the complete, bounded input to the deterministic gate.
// Package.Evidence contains only evidence authorized for transfer. Evidence
// that exists locally but is excluded by policy is represented separately by
// LocalOnlyEvidenceCount and remains out of the provider package.
type AbstentionInput struct {
	Package                EvidencePackage       `json:"package"`
	QueryID                string                `json:"query_id"`
	QueryDigest            string                `json:"query_digest"`
	QuestionKind           KnowledgeQuestionKind `json:"question_kind"`
	Support                SupportAssessment     `json:"support"`
	LocalOnlyEvidenceCount int                   `json:"local_only_evidence_count"`
}

// AbstentionReasonCode is a stable, safe reason suitable for persistence and
// metrics. Values never contain question text, source content or provider
// diagnostics.
type AbstentionReasonCode string

const (
	AbstentionReasonNoTransferableEvidence AbstentionReasonCode = "no_transferable_evidence"
	AbstentionReasonTransferProhibited     AbstentionReasonCode = "transfer_prohibited"
	AbstentionReasonInsufficientSupport    AbstentionReasonCode = "insufficient_support"
	AbstentionReasonKindMismatch           AbstentionReasonCode = "knowledge_kind_mismatch"
)

// AbstentionDecision describes the pre-provider decision. When Abstain is
// false, callers may continue with their already-authorized generator flow.
// This type itself has no provider dependency and performs no external call.
type AbstentionDecision struct {
	Abstain bool                 `json:"abstain"`
	Reason  AbstentionReasonCode `json:"reason,omitempty"`
}

// AbstentionResult contains the decision and, when generation is blocked, a
// complete valid generated-reviewable response. Response remains its zero
// value when generation is allowed.
type AbstentionResult struct {
	Decision AbstentionDecision `json:"decision"`
	Response Response           `json:"response"`
}

// DecideAbstention validates the typed assessment and applies the fixed
// decision order. No provider, generator, model or natural-language
// heuristic is consulted.
func DecideAbstention(input AbstentionInput) (AbstentionDecision, error) {
	if err := input.validate(); err != nil {
		return AbstentionDecision{}, err
	}
	if len(input.Package.Evidence) == 0 {
		if input.LocalOnlyEvidenceCount > 0 {
			return AbstentionDecision{Abstain: true, Reason: AbstentionReasonTransferProhibited}, nil
		}
		return AbstentionDecision{Abstain: true, Reason: AbstentionReasonNoTransferableEvidence}, nil
	}
	if input.Support.Kind != "" && input.Support.Kind != input.QuestionKind {
		return AbstentionDecision{Abstain: true, Reason: AbstentionReasonKindMismatch}, nil
	}
	if input.Support.Kind == "" || input.Support.Level != EvidenceSupportSufficient {
		return AbstentionDecision{Abstain: true, Reason: AbstentionReasonInsufficientSupport}, nil
	}
	return AbstentionDecision{}, nil
}

// EvaluateAbstention runs the pre-provider gate and builds a deterministic
// abstention response when required. A compatible support assessment returns
// an allowed decision with no response, leaving provider invocation to the
// later query orchestration task.
func EvaluateAbstention(input AbstentionInput) (AbstentionResult, error) {
	decision, err := DecideAbstention(input)
	if err != nil {
		return AbstentionResult{}, err
	}
	result := AbstentionResult{Decision: decision}
	if !decision.Abstain {
		return result, nil
	}
	response, err := buildAbstentionResponse(input, decision)
	if err != nil {
		return AbstentionResult{}, err
	}
	result.Response = response
	return result, nil
}

func (input AbstentionInput) validate() error {
	validation := ResponseValidationContext{
		Package:     input.Package,
		QueryID:     input.QueryID,
		QueryDigest: input.QueryDigest,
	}
	if err := validation.validateConfiguration(); err != nil {
		return fmt.Errorf("%w: validation context", ErrInvalidAbstentionInput)
	}
	if input.QuestionKind == "" || !validKnowledgeQuestionKind(input.QuestionKind) {
		return fmt.Errorf("%w: question kind", ErrInvalidAbstentionInput)
	}
	if input.LocalOnlyEvidenceCount < 0 {
		return fmt.Errorf("%w: local evidence count", ErrInvalidAbstentionInput)
	}
	if err := input.Support.validate(); err != nil {
		return err
	}
	return nil
}

func (support SupportAssessment) validate() error {
	if support.Kind != "" && !validKnowledgeQuestionKind(support.Kind) {
		return fmt.Errorf("%w: support kind", ErrInvalidAbstentionInput)
	}
	switch support.Level {
	case "", EvidenceSupportNone, EvidenceSupportInsufficient, EvidenceSupportSufficient:
		return nil
	default:
		return fmt.Errorf("%w: support level", ErrInvalidAbstentionInput)
	}
}

func validKnowledgeQuestionKind(kind KnowledgeQuestionKind) bool {
	switch kind {
	case KnowledgeQuestionInventory, KnowledgeQuestionPossibleFlow, KnowledgeQuestionObservedExecution, KnowledgeQuestionBusinessIntent:
		return true
	default:
		return false
	}
}

func buildAbstentionResponse(input AbstentionInput, decision AbstentionDecision) (Response, error) {
	if !decision.Abstain || !validAbstentionReason(decision.Reason) {
		return Response{}, fmt.Errorf("%w: decision", ErrInvalidAbstentionInput)
	}
	message := abstentionReasonMessage(decision.Reason)
	gapID := "gap-" + string(decision.Reason)
	// Abstention is local and deterministic. ProviderNone makes the absence of
	// an external model invocation explicit in the response metadata.
	epoch := time.Unix(0, 0).UTC()
	response := Response{
		Version:        Version,
		KnowledgeState: KnowledgeStateGeneratedReviewable,
		Claims: []Claim{{
			Ordinal:     1,
			Kind:        ClaimKindGap,
			Support:     SupportAbstained,
			Text:        message,
			GapOrdinals: []int{1},
		}},
		Gaps: []Gap{{
			Ordinal: 1,
			ID:      gapID,
			Code:    string(decision.Reason),
			Message: message,
		}},
		Generation: GenerationMetadata{
			Provider:      ProviderNone,
			Model:         "deterministic-abstention",
			Profile:       "deterministic-abstention",
			Protocol:      ProtocolNone,
			Usage:         Usage{},
			Termination:   TerminationAbstained,
			PackageID:     input.Package.ID,
			PackageDigest: input.Package.Digest,
			QueryID:       input.QueryID,
			QueryDigest:   input.QueryDigest,
			StartedAt:     epoch,
			FinishedAt:    epoch,
			Latency:       0,
		},
	}
	if err := (ResponseValidationContext{
		Package:     input.Package,
		QueryID:     input.QueryID,
		QueryDigest: input.QueryDigest,
	}).Validate(response); err != nil {
		return Response{}, fmt.Errorf("%w: abstention response", ErrInvalidAbstentionInput)
	}
	return response, nil
}

func validAbstentionReason(reason AbstentionReasonCode) bool {
	switch reason {
	case AbstentionReasonNoTransferableEvidence, AbstentionReasonTransferProhibited, AbstentionReasonInsufficientSupport, AbstentionReasonKindMismatch:
		return true
	default:
		return false
	}
}

func abstentionReasonMessage(reason AbstentionReasonCode) string {
	switch reason {
	case AbstentionReasonNoTransferableEvidence:
		return "Nenhuma evidência autorizada para transferência está disponível para esta consulta."
	case AbstentionReasonTransferProhibited:
		return "Há evidência local, mas a política de transferência não autoriza seu envio para geração."
	case AbstentionReasonInsufficientSupport:
		return "Há evidência transferível, mas ela não atende ao suporte mínimo desta consulta."
	case AbstentionReasonKindMismatch:
		return "As evidências disponíveis sustentam um tipo diferente de conhecimento e não estabelecem a conclusão solicitada."
	default:
		return "A consulta não possui suporte suficiente para uma resposta gerada."
	}
}
