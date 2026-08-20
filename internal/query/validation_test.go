package query

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/contract"
)

type responseRepairerStub struct {
	calls   int
	request ResponseRepairRequest
	outputs [][]byte
	err     error
}

func (s *responseRepairerStub) Repair(_ context.Context, request ResponseRepairRequest) ([]byte, error) {
	s.calls++
	s.request = request
	if s.err != nil {
		return nil, s.err
	}
	if len(s.outputs) == 0 {
		return nil, nil
	}
	output := append([]byte(nil), s.outputs[0]...)
	s.outputs = s.outputs[1:]
	return output, nil
}

func TestValidateAndRepairResponse(t *testing.T) {
	valid := validationResponse(t)
	encoded := mustResponseJSON(t, valid)
	validation := validationContext()

	tests := []struct {
		name          string
		candidate     []byte
		repairer      *responseRepairerStub
		policy        RepairPolicy
		wantErr       error
		wantAttempts  int
		wantRepaired  bool
		wantCalls     int
		wantReason    string
		wantSecretErr string
	}{
		{
			name:         "initial valid response",
			candidate:    encoded,
			policy:       RepairPolicy{MaxAttempts: 1},
			repairer:     &responseRepairerStub{outputs: [][]byte{[]byte("must not be called")}},
			wantCalls:    0,
			wantAttempts: 0,
		},
		{
			name:          "invalid JSON without repair",
			candidate:     []byte("OPENAI_API_KEY=sk-test-secret"),
			wantErr:       ErrInvalidResponse,
			wantSecretErr: "sk-test-secret",
		},
		{
			name:         "valid repair",
			candidate:    []byte("free text is not a response"),
			policy:       RepairPolicy{MaxAttempts: 1},
			repairer:     &responseRepairerStub{outputs: [][]byte{encoded}},
			wantAttempts: 1,
			wantRepaired: true,
			wantCalls:    1,
			wantReason:   "invalid_response",
		},
		{
			name:         "invalid repair is not published",
			candidate:    []byte("free text"),
			policy:       RepairPolicy{MaxAttempts: 1},
			repairer:     &responseRepairerStub{outputs: [][]byte{[]byte(`{"version":"v1alpha1"}`)}},
			wantErr:      ErrRepairFailed,
			wantAttempts: 1,
			wantCalls:    1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ValidateAndRepairResponse(context.Background(), test.candidate, validation, test.repairer, test.policy)
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("ValidateAndRepairResponse() error = %v", err)
				}
				if result.Response.Generation.PackageID != validation.Package.ID {
					t.Fatalf("accepted response package = %q, want %q", result.Response.Generation.PackageID, validation.Package.ID)
				}
			} else if !errors.Is(err, test.wantErr) {
				t.Fatalf("ValidateAndRepairResponse() error = %v, want %v", err, test.wantErr)
			}
			if result.RepairAttempts != test.wantAttempts {
				t.Fatalf("repair attempts = %d, want %d", result.RepairAttempts, test.wantAttempts)
			}
			if result.Repaired != test.wantRepaired {
				t.Fatalf("repaired = %t, want %t", result.Repaired, test.wantRepaired)
			}
			calls := 0
			if test.repairer != nil {
				calls = test.repairer.calls
				if test.wantReason != "" && test.repairer.request.Reason != test.wantReason {
					t.Fatalf("repair reason = %q, want %q", test.repairer.request.Reason, test.wantReason)
				}
			}
			if calls != test.wantCalls {
				t.Fatalf("repair calls = %d, want %d", calls, test.wantCalls)
			}
			if test.wantSecretErr != "" && strings.Contains(err.Error(), test.wantSecretErr) {
				t.Fatalf("error echoed untrusted candidate: %v", err)
			}
		})
	}
}

func TestValidateAndRepairResponseNormalizesFailureBudgetAndCancellation(t *testing.T) {
	validation := validationContext()
	invalid := []byte("not JSON")

	budgetRepairer := &responseRepairerStub{err: aigateway.ErrBudget}
	result, err := ValidateAndRepairResponse(context.Background(), invalid, validation, budgetRepairer, RepairPolicy{MaxAttempts: 1})
	if !errors.Is(err, aigateway.ErrBudget) {
		t.Fatalf("budget repair error = %v, want budget", err)
	}
	if result.RepairAttempts != 1 || budgetRepairer.calls != 1 {
		t.Fatalf("budget repair accounting = attempts %d/calls %d, want 1/1", result.RepairAttempts, budgetRepairer.calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledRepairer := &responseRepairerStub{outputs: [][]byte{mustResponseJSON(t, validationResponse(t))}}
	result, err = ValidateAndRepairResponse(ctx, invalid, validation, cancelledRepairer, RepairPolicy{MaxAttempts: 1})
	if !errors.Is(err, aigateway.ErrCancelled) {
		t.Fatalf("cancelled repair error = %v, want cancelled", err)
	}
	if result.RepairAttempts != 0 || cancelledRepairer.calls != 0 {
		t.Fatalf("cancelled repair accounting = attempts %d/calls %d, want 0/0", result.RepairAttempts, cancelledRepairer.calls)
	}

	if _, err := ValidateAndRepairResponse(nil, invalid, validation, &responseRepairerStub{}, RepairPolicy{MaxAttempts: 1}); !errors.Is(err, aigateway.ErrConfiguration) {
		t.Fatalf("nil context error = %v, want configuration", err)
	}
}

func TestValidateAndRepairResponseNeverAttemptsSecondRepair(t *testing.T) {
	validation := validationContext()
	stub := &responseRepairerStub{outputs: [][]byte{[]byte(`{"version":"v1alpha1"}`), mustResponseJSON(t, validationResponse(t))}}
	result, err := ValidateAndRepairResponse(context.Background(), []byte("invalid"), validation, stub, RepairPolicy{MaxAttempts: 1})
	if !errors.Is(err, ErrRepairFailed) {
		t.Fatalf("error = %v, want repair failure", err)
	}
	if result.Response.Version != "" || result.Response.Generation.PackageID != "" || len(result.Response.Claims) != 0 {
		t.Fatalf("invalid repaired response was returned: %#v", result.Response)
	}
	if stub.calls != 1 {
		t.Fatalf("repair calls = %d, want exactly 1", stub.calls)
	}
}

func TestValidateAndRepairResponseRejectsMismatchedEvidenceAndSupport(t *testing.T) {
	validation := validationContext()
	tests := []struct {
		name      string
		mutate    func(*Response, *EvidencePackage)
		wantError error
	}{
		{
			name: "package digest",
			mutate: func(response *Response, _ *EvidencePackage) {
				response.Generation.PackageDigest = testDigest('c')
			},
			wantError: ErrPackageMismatch,
		},
		{
			name: "query digest",
			mutate: func(response *Response, _ *EvidencePackage) {
				response.Generation.QueryDigest = testDigest('c')
			},
			wantError: ErrQueryMismatch,
		},
		{
			name: "citation scope",
			mutate: func(response *Response, _ *EvidencePackage) {
				response.Citations[0].SourceID = testUUID(4)
			},
			wantError: ErrInvalidReference,
		},
		{
			name: "citation locator",
			mutate: func(response *Response, _ *EvidencePackage) {
				response.Citations[0].Locator.Path = "internal/other.go"
			},
			wantError: ErrInvalidReference,
		},
		{
			name: "citation identity",
			mutate: func(response *Response, _ *EvidencePackage) {
				response.Citations[0].EvidenceID = "evidence-not-in-package"
			},
			wantError: ErrInvalidReference,
		},
		{
			name: "support declaration",
			mutate: func(response *Response, _ *EvidencePackage) {
				response.Citations[0].Role = CitationRoleContext
			},
			wantError: ErrInvalidResponse,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := validationResponse(t)
			packageContext := validation.Package
			test.mutate(&response, &packageContext)
			localValidation := validation
			localValidation.Package = packageContext
			_, err := ValidateAndRepairResponse(context.Background(), mustResponseJSON(t, response), localValidation, nil, RepairPolicy{})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestValidateResponseJSONRejectsFreeText(t *testing.T) {
	_, err := ValidateResponseJSON([]byte("The answer is supported."), validationContext())
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("error = %v, want invalid response", err)
	}
}

func TestRepairPolicyAllowsAtMostOneAttempt(t *testing.T) {
	validation := validationContext()
	for _, attempts := range []int{-1, 2} {
		_, err := ValidateAndRepairResponse(context.Background(), []byte("invalid"), validation, &responseRepairerStub{}, RepairPolicy{MaxAttempts: attempts})
		if !errors.Is(err, ErrInvalidRepairPolicy) {
			t.Fatalf("attempts %d error = %v, want invalid policy", attempts, err)
		}
	}
}

func validationContext() ResponseValidationContext {
	return ResponseValidationContext{
		Package:     validationPackage(),
		QueryID:     "query-1",
		QueryDigest: testDigest('b'),
	}
}

func validationPackage() EvidencePackage {
	return EvidencePackage{
		ID:             "package-1",
		Digest:         testDigest('a'),
		OrganizationID: testUUID(1),
		SourceID:       testUUID(2),
		SnapshotID:     testUUID(3),
		Evidence: []EvidenceReference{{
			ID:             "evidence-1",
			OrganizationID: testUUID(1),
			SourceID:       testUUID(2),
			SnapshotID:     testUUID(3),
			Locator: contract.Locator{
				ArtifactID: "artifact-1",
				Path:       "src/service.go",
				StartLine:  12,
				EndLine:    12,
			},
		}},
	}
}

func validationResponse(t *testing.T) Response {
	t.Helper()
	response := responseFixture()
	packageContext := validationPackage()
	response.Generation.PackageID = packageContext.ID
	response.Generation.PackageDigest = packageContext.Digest
	response.Generation.QueryID = "query-1"
	response.Generation.QueryDigest = testDigest('b')
	return response
}

func mustResponseJSON(t *testing.T, response Response) []byte {
	t.Helper()
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
