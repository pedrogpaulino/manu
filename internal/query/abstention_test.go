package query

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/aigateway"
)

type abstentionGeneratorStub struct {
	calls int
}

func (s *abstentionGeneratorStub) Generate(context.Context, aigateway.GenerationRequest) (aigateway.GenerationResult, error) {
	s.calls++
	return aigateway.GenerationResult{}, errors.New("generator must be called only after the gate allows generation")
}

func TestEvaluateAbstentionBlocksProviderForUnsafeSupport(t *testing.T) {
	tests := []struct {
		name          string
		input         AbstentionInput
		wantReason    AbstentionReasonCode
		wantGenerator int
	}{
		{
			name: "empty transferable package",
			input: func() AbstentionInput {
				input := abstentionInput()
				input.Package.Evidence = nil
				return input
			}(),
			wantReason:    AbstentionReasonNoTransferableEvidence,
			wantGenerator: 0,
		},
		{
			name: "local evidence prohibited by transfer policy",
			input: func() AbstentionInput {
				input := abstentionInput()
				input.Package.Evidence = nil
				input.LocalOnlyEvidenceCount = 2
				return input
			}(),
			wantReason:    AbstentionReasonTransferProhibited,
			wantGenerator: 0,
		},
		{
			name: "transferable evidence below minimum support",
			input: func() AbstentionInput {
				input := abstentionInput()
				input.Support.Level = EvidenceSupportInsufficient
				return input
			}(),
			wantReason:    AbstentionReasonInsufficientSupport,
			wantGenerator: 0,
		},
		{
			name: "missing semantic support assessment",
			input: func() AbstentionInput {
				input := abstentionInput()
				input.Support = SupportAssessment{}
				return input
			}(),
			wantReason:    AbstentionReasonInsufficientSupport,
			wantGenerator: 0,
		},
		{
			name: "possible flow cannot prove observed execution",
			input: func() AbstentionInput {
				input := abstentionInput()
				input.QuestionKind = KnowledgeQuestionObservedExecution
				input.Support = SupportAssessment{
					Kind:  KnowledgeQuestionPossibleFlow,
					Level: EvidenceSupportSufficient,
				}
				return input
			}(),
			wantReason:    AbstentionReasonKindMismatch,
			wantGenerator: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := EvaluateAbstention(test.input)
			if err != nil {
				t.Fatalf("EvaluateAbstention() error = %v", err)
			}
			if !result.Decision.Abstain {
				t.Fatal("decision allowed generation for unsupported input")
			}
			if result.Decision.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", result.Decision.Reason, test.wantReason)
			}
			generator := &abstentionGeneratorStub{}
			callGeneratorIfAllowed(result, generator)
			if generator.calls != test.wantGenerator {
				t.Fatalf("generator calls = %d, want %d", generator.calls, test.wantGenerator)
			}
			assertValidAbstentionResponse(t, test.input, result)
		})
	}
}

func TestEvaluateAbstentionAllowsCompatibleSupport(t *testing.T) {
	input := abstentionInput()
	input.LocalOnlyEvidenceCount = 2
	result, err := EvaluateAbstention(input)
	if err != nil {
		t.Fatalf("EvaluateAbstention() error = %v", err)
	}
	if result.Decision.Abstain {
		t.Fatalf("compatible support was blocked with reason %q", result.Decision.Reason)
	}
	if result.Decision.Reason != "" {
		t.Fatalf("allowed decision reason = %q, want empty", result.Decision.Reason)
	}
	if result.Response.Version != "" || len(result.Response.Claims) != 0 {
		t.Fatalf("allowed decision unexpectedly contains response: %#v", result.Response)
	}
	generator := &abstentionGeneratorStub{}
	callGeneratorIfAllowed(result, generator)
	if generator.calls != 1 {
		t.Fatalf("compatible generator calls = %d, want 1", generator.calls)
	}
}

func TestEvaluateAbstentionPreservesSemanticKinds(t *testing.T) {
	tests := []struct {
		name        string
		question    KnowledgeQuestionKind
		support     KnowledgeQuestionKind
		level       EvidenceSupportLevel
		wantAbstain bool
		wantReason  AbstentionReasonCode
	}{
		{
			name:        "inventory remains inventory",
			question:    KnowledgeQuestionInventory,
			support:     KnowledgeQuestionInventory,
			level:       EvidenceSupportSufficient,
			wantAbstain: false,
		},
		{
			name:        "possible flow remains possible flow",
			question:    KnowledgeQuestionPossibleFlow,
			support:     KnowledgeQuestionPossibleFlow,
			level:       EvidenceSupportSufficient,
			wantAbstain: false,
		},
		{
			name:        "observed execution needs observed evidence",
			question:    KnowledgeQuestionObservedExecution,
			support:     KnowledgeQuestionPossibleFlow,
			level:       EvidenceSupportSufficient,
			wantAbstain: true,
			wantReason:  AbstentionReasonKindMismatch,
		},
		{
			name:        "business intent needs explicit support",
			question:    KnowledgeQuestionBusinessIntent,
			support:     KnowledgeQuestionPossibleFlow,
			level:       EvidenceSupportSufficient,
			wantAbstain: true,
			wantReason:  AbstentionReasonKindMismatch,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := abstentionInput()
			input.QuestionKind = test.question
			input.Support = SupportAssessment{Kind: test.support, Level: test.level}
			result, err := EvaluateAbstention(input)
			if err != nil {
				t.Fatalf("EvaluateAbstention() error = %v", err)
			}
			if result.Decision.Abstain != test.wantAbstain {
				t.Fatalf("abstain = %t, want %t", result.Decision.Abstain, test.wantAbstain)
			}
			if result.Decision.Reason != test.wantReason {
				t.Fatalf("reason = %q, want %q", result.Decision.Reason, test.wantReason)
			}
			if result.Decision.Abstain {
				if result.Response.Claims[0].Kind != ClaimKindGap || result.Response.Claims[0].Support == SupportSupported {
					t.Fatalf("abstention promoted semantic claim: %#v", result.Response.Claims)
				}
			}
		})
	}
}

func TestEvaluateAbstentionResponseIsDeterministicAndValid(t *testing.T) {
	input := abstentionInput()
	input.Package.Evidence = nil
	first, err := EvaluateAbstention(input)
	if err != nil {
		t.Fatalf("first EvaluateAbstention() error = %v", err)
	}
	second, err := EvaluateAbstention(input)
	if err != nil {
		t.Fatalf("second EvaluateAbstention() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("abstention result is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	encoded, err := json.Marshal(first.Response)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Response
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("abstention JSON unmarshal error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("abstention response Validate() error = %v", err)
	}
	if err := (ResponseValidationContext{
		Package:     input.Package,
		QueryID:     input.QueryID,
		QueryDigest: input.QueryDigest,
	}).Validate(decoded); err != nil {
		t.Fatalf("abstention package validation error = %v", err)
	}
	if decoded.Generation.Termination != TerminationAbstained || decoded.Generation.Usage.OutputItems != 0 {
		t.Fatalf("abstention metadata = %#v", decoded.Generation)
	}
	if decoded.Generation.Provider != ProviderNone || decoded.Generation.Protocol != ProtocolNone || decoded.Generation.Usage != (Usage{}) {
		t.Fatalf("abstention falsely records provider usage: %#v", decoded.Generation)
	}
	if len(decoded.Citations) != 0 || len(decoded.Gaps) != 1 || len(decoded.Claims) != 1 {
		t.Fatalf("abstention shape = claims %d/citations %d/gaps %d", len(decoded.Claims), len(decoded.Citations), len(decoded.Gaps))
	}
}

func TestResponseRejectsIncoherentNoProviderMetadata(t *testing.T) {
	response := abstentionFixture()
	response.Generation.Provider = ProviderNone
	response.Generation.Protocol = ProtocolNone
	response.Generation.Usage = Usage{}
	if err := response.Validate(); err != nil {
		t.Fatalf("valid none-provider metadata error = %v", err)
	}

	response.Generation.Termination = TerminationCompleted
	if err := response.Validate(); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("none provider completed metadata error = %v, want invalid response", err)
	}

	response = abstentionFixture()
	response.Generation.Provider = ProviderNone
	response.Generation.Protocol = ProtocolNone
	response.Generation.Usage = Usage{InputItems: 1}
	if err := response.Validate(); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("none provider input usage error = %v, want invalid response", err)
	}
}

func TestEvaluateAbstentionRejectsUnsafeTypedInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*AbstentionInput)
	}{
		{
			name: "unknown question kind",
			mutate: func(input *AbstentionInput) {
				input.QuestionKind = KnowledgeQuestionKind("free-form-question")
			},
		},
		{
			name: "unknown support level",
			mutate: func(input *AbstentionInput) {
				input.Support.Level = EvidenceSupportLevel("guess")
			},
		},
		{
			name: "negative local evidence count",
			mutate: func(input *AbstentionInput) {
				input.LocalOnlyEvidenceCount = -1
			},
		},
		{
			name: "support kind is unknown",
			mutate: func(input *AbstentionInput) {
				input.Support.Kind = KnowledgeQuestionKind("execution-ish")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := abstentionInput()
			test.mutate(&input)
			_, err := EvaluateAbstention(input)
			if !errors.Is(err, ErrInvalidAbstentionInput) {
				t.Fatalf("error = %v, want invalid abstention input", err)
			}
		})
	}
}

func callGeneratorIfAllowed(result AbstentionResult, generator aigateway.Generator) {
	if result.Decision.Abstain {
		return
	}
	_, _ = generator.Generate(context.Background(), aigateway.GenerationRequest{})
}

func assertValidAbstentionResponse(t *testing.T, input AbstentionInput, result AbstentionResult) {
	t.Helper()
	if err := result.Response.Validate(); err != nil {
		t.Fatalf("abstention response Validate() error = %v", err)
	}
	if err := (ResponseValidationContext{
		Package:     input.Package,
		QueryID:     input.QueryID,
		QueryDigest: input.QueryDigest,
	}).Validate(result.Response); err != nil {
		t.Fatalf("abstention response package validation error = %v", err)
	}
	if result.Response.Gaps[0].Code != string(result.Decision.Reason) {
		t.Fatalf("gap code = %q, want %q", result.Response.Gaps[0].Code, result.Decision.Reason)
	}
}

func abstentionInput() AbstentionInput {
	return AbstentionInput{
		Package:      validationPackage(),
		QueryID:      "query-1",
		QueryDigest:  testDigest('b'),
		QuestionKind: KnowledgeQuestionInventory,
		Support: SupportAssessment{
			Kind:  KnowledgeQuestionInventory,
			Level: EvidenceSupportSufficient,
		},
	}
}
