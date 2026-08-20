package query

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
)

func TestResponseValidateTable(t *testing.T) {
	valid := responseFixture()
	tests := []struct {
		name      string
		mutate    func(*Response)
		wantError error
		wantText  string
	}{
		{name: "supported generated response"},
		{
			name: "unsupported version",
			mutate: func(response *Response) {
				response.Version = "v9"
			},
			wantError: ErrUnsupportedVersion,
		},
		{
			name: "invalid package digest",
			mutate: func(response *Response) {
				response.Generation.PackageDigest = "not-a-digest"
			},
			wantError: ErrInvalidDigest,
		},
		{
			name: "invalid query digest",
			mutate: func(response *Response) {
				response.Generation.QueryDigest = "ABC"
			},
			wantError: ErrInvalidDigest,
		},
		{
			name: "invalid citation organization id",
			mutate: func(response *Response) {
				response.Citations[0].OrganizationID = "organization-1"
			},
			wantError: ErrInvalidReference,
		},
		{
			name: "provider protocol mismatch",
			mutate: func(response *Response) {
				response.Generation.Provider = ProviderOpenAI
				response.Generation.Protocol = ProtocolChatCompletions
			},
			wantError: ErrInvalidResponse,
		},
		{
			name: "latency does not match timestamps",
			mutate: func(response *Response) {
				response.Generation.Latency = time.Second
			},
			wantError: ErrInvalidResponse,
		},
		{
			name: "duplicate evidence citation",
			mutate: func(response *Response) {
				duplicate := response.Citations[0]
				duplicate.Ordinal = 2
				duplicate.Role = CitationRoleContext
				response.Citations = append(response.Citations, duplicate)
				response.Claims[0].CitationOrdinals = []int{1, 2}
			},
			wantError: ErrInvalidResponse,
		},
		{
			name: "orphan citation",
			mutate: func(response *Response) {
				citation := response.Citations[0]
				citation.Ordinal = 2
				citation.EvidenceID = "evidence-2"
				response.Citations = append(response.Citations, citation)
			},
			wantError: ErrInvalidReference,
		},
		{
			name: "mixed citation scope",
			mutate: func(response *Response) {
				citation := response.Citations[0]
				citation.Ordinal = 2
				citation.EvidenceID = "evidence-2"
				citation.SourceID = testUUID(99)
				response.Citations = append(response.Citations, citation)
				response.Claims[0].CitationOrdinals = []int{1, 2}
			},
			wantError: ErrInvalidReference,
		},
		{
			name: "claim ordinal is not contiguous",
			mutate: func(response *Response) {
				response.Claims[0].Ordinal = 2
			},
			wantError: ErrInvalidResponse,
		},
		{
			name: "supported claim needs supporting role",
			mutate: func(response *Response) {
				response.Citations[0].Role = CitationRoleContext
			},
			wantError: ErrInvalidResponse,
		},
		{
			name: "control character is rejected",
			mutate: func(response *Response) {
				response.Claims[0].Text = "statement\x00"
			},
			wantError: ErrInvalidResponse,
		},
		{
			name: "locator traversal",
			mutate: func(response *Response) {
				response.Citations[0].Locator.Path = "../secret.go"
			},
			wantError: ErrUnsafeLocator,
		},
		{
			name: "secret is rejected without echo",
			mutate: func(response *Response) {
				response.Generation.Model = "OPENAI_API_KEY=sk-test-secret"
			},
			wantError: ErrInvalidResponse,
			wantText:  "sk-test-secret",
		},
		{
			name: "prefixed secret is rejected without echo",
			mutate: func(response *Response) {
				response.Generation.Model = "AWS_SECRET_ACCESS_KEY=aws-test-secret"
			},
			wantError: ErrInvalidResponse,
			wantText:  "aws-test-secret",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneResponse(valid)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			err := candidate.Validate()
			if test.wantError == nil {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Validate() error = %v, want %v", err, test.wantError)
			}
			if test.wantText != "" && strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("validation error echoed secret material: %v", err)
			}
		})
	}
}

func TestResponseValidateAbstentionAndGap(t *testing.T) {
	response := abstentionFixture()
	if err := response.Validate(); err != nil {
		t.Fatalf("abstention Validate() error = %v", err)
	}
	response.Claims[0].GapOrdinals = nil
	if err := response.Validate(); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("abstention without gap reference error = %v, want invalid response", err)
	}

	response = abstentionFixture()
	response.Generation.Termination = TerminationCompleted
	if err := response.Validate(); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("completed empty response error = %v, want invalid response", err)
	}

	partial := responseFixture()
	partial.Text = ""
	partial.Claims[0].Support = SupportUnsupported
	partial.Claims[0].CitationOrdinals = nil
	partial.Claims[0].GapOrdinals = []int{1}
	partial.Citations = nil
	partial.Gaps = []Gap{{Ordinal: 1, ID: "gap-1", Code: "partial", Message: "Some support is unavailable."}}
	partial.Generation.Termination = TerminationPartial
	partial.Generation.Usage.OutputItems = 0
	if err := partial.Validate(); err != nil {
		t.Fatalf("partial response Validate() error = %v", err)
	}
	partial.Gaps = nil
	if err := partial.Validate(); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("partial response without gap error = %v, want invalid response", err)
	}
}

func TestResponseValidateLimits(t *testing.T) {
	valid := responseFixture()
	tests := []struct {
		name      string
		limits    ResponseLimits
		mutate    func(*Response)
		wantError error
	}{
		{
			name:      "text bytes",
			limits:    ResponseLimits{MaxTextBytes: 4},
			wantError: ErrLimitExceeded,
		},
		{
			name:   "claim count",
			limits: ResponseLimits{MaxClaims: 1},
			mutate: func(response *Response) {
				response.Claims = append(response.Claims, Claim{
					Ordinal: 2, Kind: ClaimKindGenerated, Support: SupportSupported,
					Text: "A second supported statement.", CitationOrdinals: []int{1},
				})
			},
			wantError: ErrLimitExceeded,
		},
		{
			name:      "locator bytes",
			limits:    ResponseLimits{MaxLocatorBytes: 16},
			wantError: ErrLimitExceeded,
		},
		{
			name:      "total bytes",
			limits:    ResponseLimits{MaxTotalBytes: 8},
			wantError: ErrLimitExceeded,
		},
		{
			name:      "negative limit",
			limits:    ResponseLimits{MaxGaps: -1},
			wantError: ErrLimitExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneResponse(valid)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			if err := candidate.ValidateWithLimits(test.limits); !errors.Is(err, test.wantError) {
				t.Fatalf("ValidateWithLimits() error = %v, want %v", err, test.wantError)
			}
		})
	}
}

func TestResponseJSONRoundTripAndSecretShape(t *testing.T) {
	want := responseFixture()
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, forbidden := range []string{"password", "api_key", "authorization", "sk-test-secret"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("response JSON contains forbidden field/material %q: %s", forbidden, encoded)
		}
	}
	var got Response
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed response:\n got %#v\nwant %#v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("round-tripped response Validate() error = %v", err)
	}
}

func TestResponseJSONRejectsUnknownSecretFieldWithoutEcho(t *testing.T) {
	encoded, err := json.Marshal(responseFixture())
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	encoded = append(encoded[:len(encoded)-1], []byte(`,"api_key":"sk-test-secret"}`)...)
	var response Response
	err = json.Unmarshal(encoded, &response)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("Unmarshal() error = %v, want invalid response", err)
	}
	if strings.Contains(err.Error(), "sk-test-secret") {
		t.Fatalf("Unmarshal() echoed secret material: %v", err)
	}
}

func TestResponseValidationDefersPackageMembership(t *testing.T) {
	response := responseFixture()
	response.Citations[0].EvidenceID = "evidence-not-present-in-package"
	if err := response.Validate(); err != nil {
		t.Fatalf("structurally valid orphan-to-package citation was rejected in 8.1: %v", err)
	}
}

func responseFixture() Response {
	start := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	return Response{
		Version:        Version,
		KnowledgeState: KnowledgeStateGeneratedReviewable,
		Text:           "The service declares a direct dependency.",
		Claims: []Claim{{
			Ordinal: 1, Kind: ClaimKindGenerated, Support: SupportSupported,
			Text: "The service declares a direct dependency.", CitationOrdinals: []int{1},
		}},
		Citations: []Citation{{
			Ordinal: 1, OrganizationID: testUUID(1), SourceID: testUUID(2), SnapshotID: testUUID(3),
			EvidenceID: "evidence-1", Locator: contract.Locator{
				ArtifactID: "artifact-1", Path: "src/service.go", StartLine: 12, EndLine: 12,
			}, Role: CitationRoleSupports,
		}},
		Generation: GenerationMetadata{
			Provider: ProviderSimulated, Model: "simulated-model", Profile: "generation-profile-1",
			Protocol: ProtocolResponses, Usage: Usage{InputItems: 1, OutputItems: 1, InputTokens: 12, OutputTokens: 8},
			Termination: TerminationCompleted, PackageID: "package-1", PackageDigest: testDigest('a'),
			QueryID: "query-1", QueryDigest: testDigest('b'), StartedAt: start,
			FinishedAt: start.Add(25 * time.Millisecond), Latency: 25 * time.Millisecond,
		},
	}
}

func abstentionFixture() Response {
	response := responseFixture()
	response.Text = ""
	response.Claims = []Claim{{
		Ordinal: 1, Kind: ClaimKindGap, Support: SupportAbstained,
		Text: "The available package does not support an execution claim.", GapOrdinals: []int{1},
	}}
	response.Citations = nil
	response.Gaps = []Gap{{Ordinal: 1, ID: "gap-1", Code: "insufficient_support", Message: "Execution evidence is unavailable."}}
	response.Generation.Termination = TerminationAbstained
	response.Generation.Usage.OutputItems = 0
	return response
}

func cloneResponse(response Response) Response {
	encoded, err := json.Marshal(response)
	if err != nil {
		panic(fmt.Sprintf("clone response: %v", err))
	}
	var clone Response
	if err := json.Unmarshal(encoded, &clone); err != nil {
		panic(fmt.Sprintf("unmarshal clone response: %v", err))
	}
	return clone
}

func testUUID(value int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", value)
}

func testDigest(value byte) string {
	return strings.Repeat(string(value), 64)
}
