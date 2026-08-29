package evaluation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/query"
)

func TestManuContextExecutorRunsAllTaskKindsWithReadOnlyPartialProjection(t *testing.T) {
	for _, taskKind := range []TaskKind{TaskKindLocalization, TaskKindExplanation, TaskKindImpact} {
		t.Run(string(taskKind), func(t *testing.T) {
			request := manuContextTestRequest(t, taskKind)
			scope := manuContextTestScope()
			service := &manuContextTestService{}
			resolver := EvaluationScopeResolverFunc(func(_ context.Context, organization, sourceID, revision string) (query.Scope, error) {
				if organization != defaultEvaluationOrganization || sourceID != request.SourceID || revision != request.SourceRevision {
					t.Fatalf("resolver arguments = %q/%q/%q", organization, sourceID, revision)
				}
				return scope, nil
			})
			executor, err := NewManuContextExecutor(service, resolver, manuContextTestLimits())
			if err != nil {
				t.Fatalf("NewManuContextExecutor() error = %v", err)
			}
			service.packageFn = func(contextRequest query.ContextRequest) query.ContextPackage {
				return manuContextTestPackage(contextRequest, manuContextExpectedLocator(request))
			}

			result, err := executor.Execute(context.Background(), request)
			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if service.calls != 1 {
				t.Fatalf("BuildContext calls = %d, want 1", service.calls)
			}
			if len(service.requests) != 1 {
				t.Fatalf("captured requests = %d, want 1", len(service.requests))
			}
			wantIntent := query.Intent{
				Version:  query.ContextVersion,
				Kind:     query.IntentKindQuestion,
				Question: request.Case.CompetenceQuestion,
			}
			if !reflect.DeepEqual(service.requests[0].Intent, wantIntent) {
				t.Fatalf("intent = %#v, want %#v", service.requests[0].Intent, wantIntent)
			}
			if result.Status != VariantStatusLimited || result.Conclusion != VariantConclusionPartial {
				t.Fatalf("result state = %#v", result)
			}
			if result.OutputDigest == "" || !isVariantSHA256(result.OutputDigest) {
				t.Fatalf("projection digest = %q", result.OutputDigest)
			}
			if len(result.EvidenceIDs) != 1 || result.EvidenceIDs[0] != request.Case.ExpectedEvidence[0].EvidenceID {
				t.Fatalf("matched evidence = %#v", result.EvidenceIDs)
			}
			if len(result.ClaimIDs) != 0 || len(result.GapIDs) != 0 || len(result.Citations) != 0 {
				t.Fatalf("rubric material crossed result boundary = %#v", result)
			}
			if !manuContextHasLimitation(result.Limitations, ManuContextGenerationNotExecuted) ||
				!manuContextHasLimitation(result.Limitations, ManuContextContentFreeResult) {
				t.Fatalf("limitations = %#v", result.Limitations)
			}
			if taskKind == TaskKindImpact && !manuContextHasLimitation(result.Limitations, ManuContextTypedTargetNotAvailable) {
				t.Fatalf("impact limitation missing = %#v", result.Limitations)
			}
			if taskKind != TaskKindImpact && manuContextHasLimitation(result.Limitations, ManuContextTypedTargetNotAvailable) {
				t.Fatalf("unexpected typed-target limitation = %#v", result.Limitations)
			}
			if result.Metrics == nil || result.Metrics.ToolCalls == nil || *result.Metrics.ToolCalls != 1 ||
				result.Metrics.FilesRead != nil || result.Metrics.BytesRead == nil || *result.Metrics.BytesRead != 1 || result.Metrics.Duration == nil {
				t.Fatalf("observed metrics = %#v", result.Metrics)
			}
			if result.Metrics.MeasuredTokens != nil || result.Metrics.EstimatedTokens != nil ||
				result.Metrics.ModelCalls != nil || result.Metrics.EstimatedCost != nil || result.Metrics.ActualCost != nil {
				t.Fatalf("unobserved metrics invented = %#v", result.Metrics)
			}
			if err := result.Validate(); err != nil {
				t.Fatalf("result validation = %v", err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), request.Case.CompetenceQuestion) {
				t.Fatalf("question leaked into result projection: %s", encoded)
			}
		})
	}
}

func TestManuContextExecutorReconcilesOnlyRecoveredLocators(t *testing.T) {
	request := manuContextTestRequest(t, TaskKindLocalization)
	actualLocator := manuContextExpectedLocator(request)
	service := &manuContextTestService{}
	resolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return manuContextTestScope(), nil
	})
	service.packageFn = func(contextRequest query.ContextRequest) query.ContextPackage {
		return manuContextTestPackage(contextRequest, actualLocator)
	}
	executor, err := NewManuContextExecutor(service, resolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}

	matched, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("matched Execute() error = %v", err)
	}
	if len(matched.EvidenceIDs) != 1 {
		t.Fatalf("matched evidence = %#v", matched.EvidenceIDs)
	}

	noMatchRequest := request.Clone()
	noMatchRequest.Case.ExpectedEvidence[0].Locator = &contract.Locator{Path: "different.go", Member: "different", StartLine: 1, EndLine: 1}
	noMatch, err := executor.Execute(context.Background(), noMatchRequest)
	if err != nil {
		t.Fatalf("no-match Execute() error = %v", err)
	}
	if len(noMatch.EvidenceIDs) != 0 {
		t.Fatalf("unrecovered locator matched evidence = %#v", noMatch.EvidenceIDs)
	}
	if service.calls != 2 {
		t.Fatalf("BuildContext calls = %d, want 2", service.calls)
	}
}

func TestManuContextLocatorMatchesRealFrontendShapes(t *testing.T) {
	tests := []struct {
		name     string
		expected contract.Locator
		actual   contract.Locator
		want     bool
	}{
		{
			name: "java lines narrower and member omitted",
			expected: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				Member:    "BookingResource",
				StartLine: 11,
				EndLine:   12,
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
				EndLine:   11,
			},
			want: true,
		},
		{
			name: "python lines narrower and member omitted",
			expected: contract.Locator{
				Path:      "internal/analyzer/python/testdata/frappe17/doctype.py",
				Member:    "SalesOrder",
				StartLine: 9,
				EndLine:   13,
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/python/testdata/frappe17/doctype.py",
				StartLine: 9,
				EndLine:   9,
			},
			want: true,
		},
		{
			name: "wso2 line and byte coordinates do not cross-match",
			expected: contract.Locator{
				Path:      "internal/analyzer/wso2/testdata/api-v1.xml",
				Member:    "OrdersAPI",
				StartLine: 2,
				EndLine:   2,
			},
			actual: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 42,
			},
			want: false,
		},
		{
			name: "normalized path separators",
			expected: contract.Locator{
				Path:      `internal\\analyzer\\java\\testdata\\quarkus3\\BookingResource.java`,
				StartLine: 11,
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
			},
			want: true,
		},
		{
			name: "different path rejected",
			expected: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
			},
			actual: contract.Locator{
				Path:      "other/BookingResource.java",
				StartLine: 11,
			},
			want: false,
		},
		{
			name: "basename equivalence follows safe path rule",
			expected: contract.Locator{
				Path:      "BookingResource.java",
				StartLine: 11,
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
			},
			want: true,
		},
		{
			name: "line ranges without intersection rejected",
			expected: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
				EndLine:   12,
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 13,
				EndLine:   14,
			},
			want: false,
		},
		{
			name: "divergent members rejected when both present",
			expected: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				Member:    "BookingResource",
				StartLine: 11,
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				Member:    "OtherResource",
				StartLine: 11,
			},
			want: false,
		},
		{
			name: "byte ranges without intersection rejected",
			expected: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 10,
				ByteLength: 3,
			},
			actual: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 20,
				ByteLength: 2,
			},
			want: false,
		},
		{
			name: "byte ranges overlap",
			expected: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 10,
				ByteLength: 10,
			},
			actual: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 15,
				ByteLength: 2,
			},
			want: true,
		},
		{
			name: "wso2 byte offsets match",
			expected: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 42,
			},
			actual: contract.Locator{
				Path:       "internal/analyzer/wso2/testdata/api-v1.xml",
				ByteOffset: 42,
			},
			want: true,
		},
		{
			name: "member-only expected cannot cross-match actual position",
			expected: contract.Locator{
				Path:   "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				Member: "BookingResource",
			},
			actual: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
			},
			want: false,
		},
		{
			name: "path only locators rejected",
			expected: contract.Locator{
				Path: "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
			},
			actual: contract.Locator{
				Path: "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
			},
			want: false,
		},
		{
			name: "actual locator without position rejected",
			expected: contract.Locator{
				Path:      "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
				StartLine: 11,
			},
			actual: contract.Locator{
				Path: "internal/analyzer/java/testdata/quarkus3/BookingResource.java",
			},
			want: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := manuContextLocatorMatches(test.expected, test.actual); got != test.want {
				t.Fatalf("manuContextLocatorMatches() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestManuContextMatchedEvidenceMaximizesCardinality(t *testing.T) {
	expected := []ExpectedEvidence{
		{
			EvidenceID: "evidence-broad",
			Kind:       "definition",
			Locator: &contract.Locator{
				Path:      "src/example.go",
				StartLine: 1,
				EndLine:   3,
			},
		},
		{
			EvidenceID: "evidence-narrow",
			Kind:       "definition",
			Locator: &contract.Locator{
				Path:      "src/example.go",
				StartLine: 2,
				EndLine:   2,
			},
		},
	}
	items := []query.ContextItem{
		{
			ID: "item-exact-broad",
			Locator: contract.Locator{
				Path:      "src/example.go",
				StartLine: 1,
				EndLine:   3,
			},
		},
		{
			ID: "item-overlap-broad",
			Locator: contract.Locator{
				Path:      "src/example.go",
				StartLine: 3,
				EndLine:   4,
			},
		},
	}

	got := manuContextMatchedEvidence(expected, items)
	want := []string{"evidence-broad", "evidence-narrow"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manuContextMatchedEvidence() = %#v, want %#v", got, want)
	}
}

func TestManuContextExecutorAcceptsValidItemWithPayloadLocatorOnly(t *testing.T) {
	request := manuContextTestRequest(t, TaskKindLocalization)
	service := &manuContextTestService{}
	resolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return manuContextTestScope(), nil
	})
	service.packageFn = func(contextRequest query.ContextRequest) query.ContextPackage {
		packageContext := manuContextTestPackage(contextRequest, manuContextExpectedLocator(request))
		unit := manuContextTestEvidence(contextRequest.Scope, manuContextExpectedLocator(request))
		packageContext.Items[0] = query.ContextItem{
			ID:       unit.ID,
			Kind:     query.ContextItemEvidence,
			Origin:   query.ContextKnowledgeObserved,
			Scope:    contextRequest.Scope,
			Evidence: &unit,
		}
		packageContext.Audit[0].ItemID = unit.ID
		return packageContext
	}
	executor, err := NewManuContextExecutor(service, resolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(result.EvidenceIDs) != 1 || result.Metrics == nil || result.Metrics.FilesRead != nil || result.Metrics.BytesRead == nil || *result.Metrics.BytesRead != 1 {
		t.Fatalf("payload-locator result = %#v", result)
	}
}

func TestManuContextExecutorIntentIgnoresRubricAndReferenceAnswer(t *testing.T) {
	request := manuContextTestRequest(t, TaskKindExplanation)
	changed := request.Clone()
	changed.Case.ExpectedEvidence[0].Locator = &contract.Locator{Path: "another.go", Member: "another", StartLine: 2, EndLine: 3}
	changed.Case.ReferenceAnswer.Summary = "different curation summary"
	changed.Case.ReferenceAnswer.ClaimIDs = []string{changed.Case.AcceptableClaims[0].ClaimID}

	service := &manuContextTestService{}
	resolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return manuContextTestScope(), nil
	})
	service.packageFn = func(contextRequest query.ContextRequest) query.ContextPackage {
		return manuContextTestPackage(contextRequest, contract.Locator{Path: "actual.go", Member: "actual", StartLine: 1, EndLine: 1})
	}
	executor, err := NewManuContextExecutor(service, resolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), request); err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if _, err := executor.Execute(context.Background(), changed); err != nil {
		t.Fatalf("changed Execute() error = %v", err)
	}
	if len(service.requests) != 2 || !reflect.DeepEqual(service.requests[0].Intent, service.requests[1].Intent) {
		t.Fatalf("rubric changed retrieval intent: %#v", service.requests)
	}
	want := query.Intent{Version: query.ContextVersion, Kind: query.IntentKindQuestion, Question: request.Case.CompetenceQuestion}
	if !reflect.DeepEqual(service.requests[0].Intent, want) {
		t.Fatalf("intent = %#v, want %#v", service.requests[0].Intent, want)
	}
}

func TestManuContextExecutorEnforcesReadOnlyPolicyAndSafeCancellation(t *testing.T) {
	request := manuContextTestRequest(t, TaskKindLocalization)
	service := &manuContextTestService{packageFn: func(contextRequest query.ContextRequest) query.ContextPackage {
		return manuContextTestPackage(contextRequest, manuContextExpectedLocator(request))
	}}
	resolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return manuContextTestScope(), nil
	})
	executor, err := NewManuContextExecutor(service, resolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*EvaluationPolicy){
		"source access": func(policy *EvaluationPolicy) { policy.SourceAccess = "write" },
		"transfer":      func(policy *EvaluationPolicy) { policy.ExternalTransfer = "allow" },
		"network":       func(policy *EvaluationPolicy) { policy.NetworkAccess = "allow" },
		"mutation":      func(policy *EvaluationPolicy) { policy.MutationAccess = "allow" },
		"permission":    func(policy *EvaluationPolicy) { policy.Permissions = []string{"filesystem.read"} },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := request.Clone()
			mutate(&invalid.Policy)
			if _, err := executor.Execute(context.Background(), invalid); !errors.Is(err, ErrInvalidVariantRequest) {
				t.Fatalf("policy error = %v", err)
			}
		})
	}
	if service.calls != 0 {
		t.Fatalf("invalid policy invoked service %d times", service.calls)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executor.Execute(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel error = %v", err)
	}
	if service.calls != 0 {
		t.Fatalf("pre-cancel invoked service %d times", service.calls)
	}
}

func TestManuContextExecutorSanitizesDependenciesAndRejectsInvalidScopeOrPackage(t *testing.T) {
	request := manuContextTestRequest(t, TaskKindLocalization)
	unsafeResolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return query.Scope{}, errors.New("secret path /private/source.go")
	})
	service := &manuContextTestService{}
	executor, err := NewManuContextExecutor(service, unsafeResolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), request)
	if !errors.Is(err, ErrManuContextUnavailable) || strings.Contains(err.Error(), "private/source.go") {
		t.Fatalf("unsafe resolver error = %v", err)
	}

	invalidScopeResolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return query.Scope{OrganizationID: "not-a-uuid", SourceID: "not-a-uuid", SnapshotID: "not-a-uuid"}, nil
	})
	executor, err = NewManuContextExecutor(service, invalidScopeResolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrManuContextUnavailable) {
		t.Fatalf("invalid scope error = %v", err)
	}

	invalidPackageService := &manuContextTestService{packageFn: func(query.ContextRequest) query.ContextPackage {
		return query.ContextPackage{}
	}}
	validResolver := EvaluationScopeResolverFunc(func(context.Context, string, string, string) (query.Scope, error) {
		return manuContextTestScope(), nil
	})
	executor, err = NewManuContextExecutor(invalidPackageService, validResolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrManuContextInvalidPackage) {
		t.Fatalf("invalid package error = %v", err)
	}

	mismatchService := &manuContextTestService{packageFn: func(contextRequest query.ContextRequest) query.ContextPackage {
		packageContext := manuContextTestPackage(contextRequest, manuContextExpectedLocator(request))
		mismatchedScope := query.Scope{
			OrganizationID: "22222222-2222-4222-8222-222222222222",
			SourceID:       "33333333-3333-4333-8333-333333333333",
			SnapshotID:     "44444444-4444-4444-8444-444444444444",
		}
		packageContext.Scope = mismatchedScope
		packageContext.Items[0].Scope = mismatchedScope
		return packageContext
	}}
	executor, err = NewManuContextExecutor(mismatchService, validResolver, manuContextTestLimits())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := executor.Execute(context.Background(), request); !errors.Is(err, ErrManuContextScopeMismatch) {
		t.Fatalf("mismatched package error = %v", err)
	}
}

type manuContextTestService struct {
	calls     int
	requests  []query.ContextRequest
	packageFn func(query.ContextRequest) query.ContextPackage
	err       error
}

func (s *manuContextTestService) BuildContext(ctx context.Context, request query.ContextRequest) (query.ContextPackage, error) {
	if err := ctx.Err(); err != nil {
		return query.ContextPackage{}, err
	}
	s.calls++
	s.requests = append(s.requests, request.Clone())
	if s.err != nil {
		return query.ContextPackage{}, s.err
	}
	if s.packageFn == nil {
		return query.ContextPackage{}, nil
	}
	return s.packageFn(request), nil
}

func manuContextTestRequest(t *testing.T, taskKind TaskKind) VariantExecutionRequest {
	t.Helper()
	caseSet := loadContextEfficiencyFixture(t)
	for _, item := range caseSet.Cases {
		if item.Task.Kind != taskKind {
			continue
		}
		for _, variant := range item.Variants {
			if variant.Kind != VariantManuContext {
				continue
			}
			request, err := buildVariantRequest(item, variant)
			if err != nil {
				t.Fatalf("buildVariantRequest() error = %v", err)
			}
			return request
		}
	}
	t.Fatalf("task kind %q not found", taskKind)
	return VariantExecutionRequest{}
}

func manuContextTestLimits() query.ContextLimits {
	return query.ContextLimits{MaxTokens: 256, MaxItems: 8, MaxCharacters: 4_096, MaxBytes: 16_384}
}

func manuContextTestScope() query.Scope {
	return query.Scope{
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		SourceID:       "22222222-2222-4222-8222-222222222222",
		SnapshotID:     "33333333-3333-4333-8333-333333333333",
	}
}

func manuContextExpectedLocator(request VariantExecutionRequest) contract.Locator {
	if len(request.Case.ExpectedEvidence) > 0 && request.Case.ExpectedEvidence[0].Locator != nil {
		return *request.Case.ExpectedEvidence[0].Locator
	}
	return contract.Locator{Path: "actual.go", Member: "actual", StartLine: 1, EndLine: 1}
}

func manuContextTestPackage(request query.ContextRequest, locator contract.Locator) query.ContextPackage {
	itemID := "context-item-1"
	item := query.ContextItem{
		ID:      itemID,
		Kind:    query.ContextItemEntity,
		Origin:  query.ContextKnowledgeObserved,
		Scope:   request.Scope,
		Entity:  &fact.Participant{Kind: fact.ParticipantSymbol, ID: itemID},
		Locator: locator,
	}
	return query.ContextPackage{
		Version:  query.ContextVersion,
		ID:       "package-eval",
		Digest:   manuContextTestDigest("package-eval"),
		Revision: "snapshot-revision-v1",
		Scope:    request.Scope,
		Intent:   request.Intent,
		Limits:   request.Limits,
		Items:    []query.ContextItem{item},
		Audit: []query.ContextSelectionAudit{{
			ItemID: itemID, Included: true, Reason: query.ContextSelectionIncluded,
			Rank: 0, TokenEstimate: 1, Characters: 1, Bytes: 1,
		}},
		TokenEstimate:  1,
		CharactersUsed: 1,
		BytesUsed:      1,
	}
}

func manuContextTestEvidence(scope query.Scope, locator contract.Locator) evidence.EvidenceUnit {
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ArtifactID:     "artifact-eval",
		Contribution: evidence.ContributionRef{
			ID:              "contribution-eval",
			ArtifactID:      "artifact-eval",
			AnalyzerID:      "evaluation",
			AnalyzerVersion: "v1",
			Method:          "metadata",
		},
		Locator:          locator,
		ContentState:     evidence.ContentStateOmitted,
		ContentHash:      manuContextTestDigest("omitted"),
		Persist:          evidence.DecisionAllow,
		ExternalTransfer: evidence.DecisionDeny,
	}
	unit.ID = evidence.EvidenceID(unit)
	return unit
}

func manuContextTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func manuContextHasLimitation(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
