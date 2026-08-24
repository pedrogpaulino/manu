package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestContextRequestValidateAcceptsEveryIntentKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		intent Intent
	}{
		{
			name: "question",
			intent: Intent{
				Version:  ContextVersion,
				Kind:     IntentKindQuestion,
				Question: "where is the source of this behavior?",
			},
		},
		{
			name: "entity",
			intent: Intent{
				Version: ContextVersion,
				Kind:    IntentKindEntity,
				Target:  IntentTarget{Kind: IntentTargetEntity, ID: "entity-1"},
			},
		},
		{
			name: "symbol",
			intent: Intent{
				Version: ContextVersion,
				Kind:    IntentKindSymbol,
				Target:  IntentTarget{Kind: IntentTargetSymbol, ID: "symbol-1"},
			},
		},
		{
			name: "possible impact of entity",
			intent: Intent{
				Version: ContextVersion,
				Kind:    IntentKindPossibleImpact,
				Target:  IntentTarget{Kind: IntentTargetEntity, ID: "entity-1"},
			},
		},
		{
			name: "possible impact of symbol",
			intent: Intent{
				Version: ContextVersion,
				Kind:    IntentKindPossibleImpact,
				Target:  IntentTarget{Kind: IntentTargetSymbol, ID: "symbol-1"},
			},
		},
		{
			name: "evidence inspection",
			intent: Intent{
				Version: ContextVersion,
				Kind:    IntentKindEvidenceInspection,
				Target:  IntentTarget{Kind: IntentTargetEvidence, ID: "evidence-1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := contextTestRequest(tt.intent)
			if err := request.Validate(); err != nil {
				t.Fatalf("ContextRequest.Validate() error = %v", err)
			}
		})
	}
}

func TestContextRequestValidateRejectsInvalidVersionScopeIntentLimitsAndCursor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request ContextRequest
		wantErr error
	}{
		{
			name: "request version",
			request: func() ContextRequest {
				request := contextTestRequest(contextTestQuestionIntent())
				request.Version = "v0"
				return request
			}(),
			wantErr: ErrUnsupportedContextVersion,
		},
		{
			name: "intent version",
			request: func() ContextRequest {
				request := contextTestRequest(contextTestQuestionIntent())
				request.Intent.Version = "v0"
				return request
			}(),
			wantErr: ErrUnsupportedContextVersion,
		},
		{
			name: "organization scope is not uuid",
			request: func() ContextRequest {
				request := contextTestRequest(contextTestQuestionIntent())
				request.Scope.OrganizationID = "organization-not-a-uuid"
				return request
			}(),
			wantErr: ErrInvalidContextScope,
		},
		{
			name: "source scope is not uuid",
			request: func() ContextRequest {
				request := contextTestRequest(contextTestQuestionIntent())
				request.Scope.SourceID = "source-not-a-uuid"
				return request
			}(),
			wantErr: ErrInvalidContextScope,
		},
		{
			name: "snapshot scope is not uuid",
			request: func() ContextRequest {
				request := contextTestRequest(contextTestQuestionIntent())
				request.Scope.SnapshotID = "snapshot-not-a-uuid"
				return request
			}(),
			wantErr: ErrInvalidContextScope,
		},
		{
			name: "question has a target",
			request: func() ContextRequest {
				request := contextTestRequest(contextTestQuestionIntent())
				request.Intent.Target = IntentTarget{Kind: IntentTargetEntity, ID: "entity-1"}
				return request
			}(),
			wantErr: ErrInvalidContext,
		},
		{
			name: "entity has question text",
			request: func() ContextRequest {
				request := contextTestRequest(Intent{
					Version:  ContextVersion,
					Kind:     IntentKindEntity,
					Question: "ambiguous question",
					Target:   IntentTarget{Kind: IntentTargetEntity, ID: "entity-1"},
				})
				return request
			}(),
			wantErr: ErrInvalidContextReference,
		},
		{
			name: "entity has symbol target",
			request: contextTestRequest(Intent{
				Version: ContextVersion,
				Kind:    IntentKindEntity,
				Target:  IntentTarget{Kind: IntentTargetSymbol, ID: "symbol-1"},
			}),
			wantErr: ErrInvalidContextReference,
		},
		{
			name: "possible impact has ambiguous target",
			request: contextTestRequest(Intent{
				Version: ContextVersion,
				Kind:    IntentKindPossibleImpact,
				Target:  IntentTarget{ID: "entity-1"},
			}),
			wantErr: ErrInvalidContextReference,
		},
		{
			name: "evidence inspection has entity target",
			request: contextTestRequest(Intent{
				Version: ContextVersion,
				Kind:    IntentKindEvidenceInspection,
				Target:  IntentTarget{Kind: IntentTargetEntity, ID: "entity-1"},
			}),
			wantErr: ErrInvalidContextReference,
		},
		{
			name:    "unknown intent kind",
			request: contextTestRequest(Intent{Version: ContextVersion, Kind: IntentKind("unknown")}),
			wantErr: ErrInvalidContext,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.request.Validate(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("ContextRequest.Validate() error = %v, want errors.Is(_, %v)", err, tt.wantErr)
			}
		})
	}

	for _, tt := range []struct {
		name   string
		mutate func(*ContextLimits)
	}{
		{name: "tokens zero", mutate: func(limits *ContextLimits) { limits.MaxTokens = 0 }},
		{name: "items zero", mutate: func(limits *ContextLimits) { limits.MaxItems = 0 }},
		{name: "characters zero", mutate: func(limits *ContextLimits) { limits.MaxCharacters = 0 }},
		{name: "bytes zero", mutate: func(limits *ContextLimits) { limits.MaxBytes = 0 }},
		{name: "tokens negative", mutate: func(limits *ContextLimits) { limits.MaxTokens = -1 }},
		{name: "items negative", mutate: func(limits *ContextLimits) { limits.MaxItems = -1 }},
		{name: "characters negative", mutate: func(limits *ContextLimits) { limits.MaxCharacters = -1 }},
		{name: "bytes negative", mutate: func(limits *ContextLimits) { limits.MaxBytes = -1 }},
		{name: "tokens excessive", mutate: func(limits *ContextLimits) { limits.MaxTokens = int(maxContextTokens) + 1 }},
		{name: "items excessive", mutate: func(limits *ContextLimits) { limits.MaxItems = maxContextItems + 1 }},
		{name: "characters excessive", mutate: func(limits *ContextLimits) { limits.MaxCharacters = maxContextCharacters + 1 }},
		{name: "bytes excessive", mutate: func(limits *ContextLimits) { limits.MaxBytes = maxContextBytes + 1 }},
	} {
		t.Run("limit/"+tt.name, func(t *testing.T) {
			request := contextTestRequest(contextTestQuestionIntent())
			tt.mutate(&request.Limits)
			if err := request.Validate(); !errors.Is(err, ErrInvalidContextBudget) {
				t.Fatalf("ContextRequest.Validate() error = %v, want budget error", err)
			}
		})
	}

	for _, tt := range []struct {
		name         string
		continuation ContextContinuation
		wantErr      error
	}{
		{name: "empty token", continuation: ContextContinuation{}, wantErr: ErrInvalidContextContinuation},
		{name: "token too large", continuation: ContextContinuation{Token: strings.Repeat("x", int(maxContextContinuation)+1)}, wantErr: ErrInvalidContextContinuation},
		{name: "intent digest is malformed", continuation: ContextContinuation{Token: "cursor-1", IntentDigest: "not-a-sha256"}, wantErr: ErrInvalidContextContinuation},
		{name: "policy digest is malformed", continuation: ContextContinuation{Token: "cursor-1", PolicyDigest: "not-a-sha256"}, wantErr: ErrInvalidContextContinuation},
		{name: "algorithm version is malformed", continuation: ContextContinuation{Token: "cursor-1", AlgorithmVersion: "bad version"}, wantErr: ErrInvalidContextContinuation},
		{name: "ordering is malformed", continuation: ContextContinuation{Token: "cursor-1", Ordering: "bad ordering"}, wantErr: ErrInvalidContextContinuation},
	} {
		t.Run("cursor/"+tt.name, func(t *testing.T) {
			request := contextTestRequest(contextTestQuestionIntent())
			request.Continuation = &tt.continuation
			if err := request.Validate(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("ContextRequest.Validate() error = %v, want continuation error", err)
			}
		})
	}

	request := contextTestRequest(contextTestQuestionIntent())
	otherScope := contextTestScope()
	otherScope.SnapshotID = contextTestUUID(12)
	request.Continuation = &ContextContinuation{Token: "cursor-1", Scope: &otherScope}
	if err := request.Validate(); !errors.Is(err, ErrInvalidContextContinuation) {
		t.Fatalf("scope-mismatched continuation error = %v, want continuation error", err)
	}
}

func TestContextPackageValidateAcceptsCompleteBoundedPackage(t *testing.T) {
	t.Parallel()

	packageContext := contextTestPackage()
	if err := packageContext.Validate(); err != nil {
		t.Fatalf("ContextPackage.Validate() error = %v", err)
	}
}

func TestContextPackageValidateRejectsMalformedReferencesAndAccounting(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		mutate func(*ContextPackage)
		want   error
	}{
		{
			name: "unsupported package version",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Version = "v0"
			},
			want: ErrUnsupportedContextVersion,
		},
		{
			name: "invalid package scope",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Scope.OrganizationID = "not-a-uuid"
			},
			want: ErrInvalidContextScope,
		},
		{
			name: "payload kind has no matching payload",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[0].Fact = nil
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "payload kind carries another payload",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[0].Entity = &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "entity-extra"}
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "mixed item scope",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[1].Scope.SnapshotID = contextTestUUID(12)
			},
			want: ErrInvalidContextScope,
		},
		{
			name: "mixed relation scope",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Relations[0].Scope.SnapshotID = contextTestUUID(12)
			},
			want: ErrInvalidContextScope,
		},
		{
			name: "duplicate item ids",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[1].ID = packageContext.Items[0].ID
				packageContext.Items[1].Entity.ID = packageContext.Items[1].ID
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "relation id collides with item",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Relations[0].ID = packageContext.Items[0].ID
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "context item support is dangling",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[0].SupportIDs = []string{"missing-support-item"}
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "relation has no support",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Relations[0].SupportIDs = nil
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "relation support is dangling",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Relations[0].SupportIDs = []string{"missing-support-item"}
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "relation endpoint is dangling",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Relations[0].ToID = "missing-endpoint"
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "relation path is dangling",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Relations[0].Path = []string{packageContext.Relations[0].FromID, "missing-path-node", packageContext.Relations[0].ToID}
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "fact version is invalid",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[0].Fact.Version = "v0"
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "evidence version is invalid",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Items[2].Evidence.Version = "v0"
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "token accounting exceeds budget",
			mutate: func(packageContext *ContextPackage) {
				packageContext.TokenEstimate = packageContext.Limits.MaxTokens + 1
			},
			want: ErrInvalidContextBudget,
		},
		{
			name: "character accounting exceeds budget",
			mutate: func(packageContext *ContextPackage) {
				packageContext.CharactersUsed = packageContext.Limits.MaxCharacters + 1
			},
			want: ErrInvalidContextBudget,
		},
		{
			name: "byte accounting exceeds budget",
			mutate: func(packageContext *ContextPackage) {
				packageContext.BytesUsed = packageContext.Limits.MaxBytes + 1
			},
			want: ErrInvalidContextBudget,
		},
		{
			name: "item count exceeds budget",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Limits.MaxItems = 2
			},
			want: ErrInvalidContextBudget,
		},
		{
			name: "degradation is duplicated",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Degradations = append(packageContext.Degradations, packageContext.Degradations[0])
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "audit is duplicated",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Audit = append(packageContext.Audit, packageContext.Audit[0])
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "included audit has exclusion reason",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Audit[0].Reason = ContextSelectionExcludedBudget
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "excluded audit has included reason",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Audit[len(packageContext.Audit)-1].Included = true
			},
			want: ErrInvalidContextReference,
		},
		{
			name: "truncated package has no continuation",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Truncated = true
				packageContext.Continuation = nil
			},
			want: ErrInvalidContextContinuation,
		},
		{
			name: "nontruncated package has continuation",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Truncated = false
			},
			want: ErrInvalidContextContinuation,
		},
		{
			name: "continuation revision mismatches package",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Continuation.SnapshotRevision = "revision-other"
			},
			want: ErrInvalidContextContinuation,
		},
		{
			name: "continuation scope mismatches package",
			mutate: func(packageContext *ContextPackage) {
				otherScope := contextTestScope()
				otherScope.SnapshotID = contextTestUUID(12)
				packageContext.Continuation.Scope = &otherScope
			},
			want: ErrInvalidContextContinuation,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			packageContext := contextTestPackage()
			tt.mutate(&packageContext)
			if err := packageContext.Validate(); !errors.Is(err, tt.want) {
				t.Fatalf("ContextPackage.Validate() error = %v, want errors.Is(_, %v)", err, tt.want)
			}
		})
	}
}

func TestContextPackageValidateRejectsIncoherentSelectionAudit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ContextPackage)
	}{
		{
			name: "selected item has no included audit",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Audit = packageContext.Audit[1:]
			},
		},
		{
			name: "excluded audit refers to selected item",
			mutate: func(packageContext *ContextPackage) {
				packageContext.Audit[len(packageContext.Audit)-1].ItemID = packageContext.Items[1].ID
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packageContext := contextTestPackage()
			tt.mutate(&packageContext)
			if err := packageContext.Validate(); !errors.Is(err, ErrInvalidContextReference) {
				t.Fatalf("ContextPackage.Validate() error = %v, want invalid reference", err)
			}
		})
	}
}

func TestContextPackageValidateRejectsCoverageAndGapCardinalityAboveBound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ContextPackage)
	}{
		{
			name: "coverage exceeds finite bound",
			mutate: func(packageContext *ContextPackage) {
				coverage := packageContext.Coverage[0]
				packageContext.Coverage = make([]contract.Coverage, maxContextItems+1)
				for index := range packageContext.Coverage {
					coverage.ID = fmt.Sprintf("coverage-bound-%d", index)
					packageContext.Coverage[index] = coverage
				}
			},
		},
		{
			name: "gaps exceed finite bound",
			mutate: func(packageContext *ContextPackage) {
				gap := packageContext.Gaps[0]
				packageContext.Gaps = make([]contract.Gap, maxContextItems+1)
				for index := range packageContext.Gaps {
					gap.ID = fmt.Sprintf("gap-bound-%d", index)
					packageContext.Gaps[index] = gap
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			packageContext := contextTestPackage()
			tt.mutate(&packageContext)
			if err := packageContext.Validate(); !errors.Is(err, ErrInvalidContextBudget) {
				t.Fatalf("ContextPackage.Validate() error = %v, want budget error", err)
			}
		})
	}
}

func TestContextPackageValidateRejectsProvenanceEvidenceCardinalityAboveBound(t *testing.T) {
	t.Parallel()

	packageContext := contextTestPackage()
	provenanceEvidence := make([]fact.EvidenceRef, maxContextItems+1)
	for index := range provenanceEvidence {
		provenanceEvidence[index] = fact.EvidenceRef{
			ID:      fmt.Sprintf("provenance-evidence-bound-%d", index),
			Locator: packageContext.Items[0].Locator,
		}
	}
	packageContext.Items[0].Provenance.Evidence = provenanceEvidence

	err := packageContext.Validate()
	if err == nil || (!errors.Is(err, ErrInvalidContextBudget) && !errors.Is(err, ErrInvalidContextReference)) {
		t.Fatalf("ContextPackage.Validate() error = %v, want budget or reference error", err)
	}
}

func TestContextValidationErrorsDoNotEchoSecrets(t *testing.T) {
	t.Parallel()

	secret := "Bearer super-secret-context-value"
	request := contextTestRequest(contextTestQuestionIntent())
	request.Scope.OrganizationID = secret
	err := request.Validate()
	if !errors.Is(err, ErrInvalidContextScope) {
		t.Fatalf("secret scope error = %v, want invalid scope", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret scope value was echoed in error: %v", err)
	}

	packageContext := contextTestPackage()
	packageContext.ID = secret
	err = packageContext.Validate()
	if !errors.Is(err, ErrInvalidContextReference) {
		t.Fatalf("secret package id error = %v, want invalid reference", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("secret package value was echoed in error: %v", err)
	}
}

func TestContextCloneDeepCopiesRequestAndPackage(t *testing.T) {
	t.Parallel()

	request := contextTestRequest(contextTestQuestionIntent())
	request.Continuation = &ContextContinuation{
		Token:            "cursor-1",
		Scope:            contextTestScopePointer(),
		SnapshotRevision: "revision-1",
		IntentDigest:     strings.Repeat("a", 64),
		PolicyDigest:     strings.Repeat("b", 64),
		AlgorithmVersion: "algorithm-v1",
		Ordering:         "item-id-ascending",
	}
	requestClone := request.Clone()
	requestClone.Continuation.Token = "cursor-mutated"
	requestClone.Continuation.Scope.SnapshotID = contextTestUUID(12)
	if request.Continuation.Token == requestClone.Continuation.Token {
		t.Fatal("request continuation token shares storage")
	}
	if request.Continuation.Scope.SnapshotID == requestClone.Continuation.Scope.SnapshotID {
		t.Fatal("request continuation scope shares storage")
	}

	packageContext := contextTestPackage()
	clone := packageContext.Clone()

	clone.Items[0].SupportIDs[0] = "support-mutated"
	clone.Items[0].Fact.Object.ID = "object-mutated"
	clone.Items[3].Fact.Value.String = "value-mutated"
	clone.Items[0].Fact.Qualifiers[0].Name = "qualifier-mutated"
	clone.Items[0].Fact.Evidence[0].ID = "evidence-ref-mutated"
	clone.Items[3].Fact.Lineage.InputFactIDs[0] = "lineage-input-mutated"
	clone.Items[0].Provenance.Producer.ID = "producer-mutated"
	clone.Items[3].Provenance.Lineage.InputFactIDs[0] = "provenance-input-mutated"
	clone.Items[0].Provenance.Evidence[0].ID = "provenance-evidence-mutated"
	clone.Items[1].Entity.ID = "entity-mutated"
	clone.Items[2].Evidence.Findings[0] = "finding-mutated"
	clone.Relations[0].Path[0] = "path-mutated"
	clone.Relations[0].SupportIDs[0] = "relation-support-mutated"
	clone.Relations[0].Provenance.Producer.Version = "producer-version-mutated"
	clone.Coverage[0].Locator.Path = "coverage-mutated.go"
	clone.Gaps[0].Locator.Path = "gap-mutated.go"
	clone.Degradations[0].Code = ContextDegradationTextUnavailable
	clone.Audit[0].ItemID = "audit-mutated"
	clone.Continuation.Token = "continuation-mutated"
	clone.Continuation.Scope.SourceID = contextTestUUID(13)

	if packageContext.Items[0].SupportIDs[0] == clone.Items[0].SupportIDs[0] ||
		packageContext.Items[0].Fact.Object.ID == clone.Items[0].Fact.Object.ID ||
		packageContext.Items[3].Fact.Value.String == clone.Items[3].Fact.Value.String ||
		packageContext.Items[0].Fact.Qualifiers[0].Name == clone.Items[0].Fact.Qualifiers[0].Name ||
		packageContext.Items[0].Fact.Evidence[0].ID == clone.Items[0].Fact.Evidence[0].ID ||
		packageContext.Items[3].Fact.Lineage.InputFactIDs[0] == clone.Items[3].Fact.Lineage.InputFactIDs[0] ||
		packageContext.Items[0].Provenance.Producer.ID == clone.Items[0].Provenance.Producer.ID ||
		packageContext.Items[3].Provenance.Lineage.InputFactIDs[0] == clone.Items[3].Provenance.Lineage.InputFactIDs[0] ||
		packageContext.Items[0].Provenance.Evidence[0].ID == clone.Items[0].Provenance.Evidence[0].ID ||
		packageContext.Items[1].Entity.ID == clone.Items[1].Entity.ID ||
		packageContext.Items[2].Evidence.Findings[0] == clone.Items[2].Evidence.Findings[0] ||
		packageContext.Relations[0].Path[0] == clone.Relations[0].Path[0] ||
		packageContext.Relations[0].SupportIDs[0] == clone.Relations[0].SupportIDs[0] ||
		packageContext.Relations[0].Provenance.Producer.Version == clone.Relations[0].Provenance.Producer.Version ||
		packageContext.Coverage[0].Locator.Path == clone.Coverage[0].Locator.Path ||
		packageContext.Gaps[0].Locator.Path == clone.Gaps[0].Locator.Path ||
		packageContext.Degradations[0].Code == clone.Degradations[0].Code ||
		packageContext.Audit[0].ItemID == clone.Audit[0].ItemID ||
		packageContext.Continuation.Token == clone.Continuation.Token ||
		packageContext.Continuation.Scope.SourceID == clone.Continuation.Scope.SourceID {
		t.Fatal("ContextPackage.Clone() shares mutable nested state")
	}
}

type contextTestService struct{}

var _ ContextService = (*contextTestService)(nil)

func (*contextTestService) BuildContext(context.Context, ContextRequest) (ContextPackage, error) {
	return ContextPackage{}, nil
}

func contextTestRequest(intent Intent) ContextRequest {
	return ContextRequest{
		Version: ContextVersion,
		Scope:   contextTestScope(),
		Intent:  intent,
		Limits:  contextTestLimits(),
	}
}

func contextTestQuestionIntent() Intent {
	return Intent{Version: ContextVersion, Kind: IntentKindQuestion, Question: "where is the source?"}
}

func contextTestScope() Scope {
	return Scope{
		OrganizationID: contextTestUUID(1),
		SourceID:       contextTestUUID(2),
		SnapshotID:     contextTestUUID(3),
	}
}

func contextTestScopePointer() *Scope {
	scope := contextTestScope()
	return &scope
}

func contextTestUUID(value int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", value)
}

func contextTestLimits() ContextLimits {
	return ContextLimits{MaxTokens: 256, MaxItems: 8, MaxCharacters: 4_096, MaxBytes: 16_384}
}

func contextTestPackage() ContextPackage {
	scope := contextTestScope()
	evidenceUnit := packageTestUnit(scope, "artifact-context", "src/context.go", "context evidence")
	evidenceUnit.ContentState = evidence.ContentStateOmitted
	evidenceUnit.Content = ""
	evidenceUnit.ContentBytes = 0
	evidenceUnit.ContentCharacters = 0
	evidenceUnit.Classification = evidence.ClassificationSensitive
	evidenceUnit.Findings = []string{evidence.FindingSecret}
	evidenceUnit.ExternalTransfer = evidence.DecisionDeny
	evidenceUnit.ID = evidence.EvidenceID(evidenceUnit)
	locator := evidenceUnit.Locator

	observedFact := contextTestFact(scope, evidenceUnit.ID, locator)
	observedFact.Object = &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "object-context"}
	observedFact.Value = nil
	observedFact.ID = ""
	observedFact.ID, _ = fact.FactID(observedFact)
	observedFactID := observedFact.ID
	entity := fact.Participant{Kind: fact.ParticipantNamedElement, ID: "entity-context"}

	producer := observedFact.Producer
	lineage := &fact.Lineage{RuleID: "rule-context", RuleVersion: "v1", InputFactIDs: []string{observedFactID}}
	derivedFact := contextTestFact(scope, evidenceUnit.ID, locator)
	derivedFact.Subject.ID = "derived-context"
	derivedFact.Predicate = fact.PredicateReference
	derivedFact.Lineage = lineage
	derivedFact.ID = ""
	derivedFact.ID, _ = fact.FactID(derivedFact)
	derivedFactItem := ContextItem{
		ID:      derivedFact.ID,
		Kind:    ContextItemFact,
		Origin:  ContextKnowledgeDerived,
		Scope:   scope,
		Fact:    &derivedFact,
		Locator: locator,
		Provenance: ContextProvenance{
			Producer: &producer,
			Lineage:  &fact.Lineage{RuleID: "rule-context", RuleVersion: "v1", InputFactIDs: []string{observedFactID}},
			Evidence: []fact.EvidenceRef{{ID: evidenceUnit.ID, Locator: locator}},
		},
		SupportIDs: []string{evidenceUnit.ID},
	}

	observedItem := ContextItem{
		ID:      observedFact.ID,
		Kind:    ContextItemFact,
		Origin:  ContextKnowledgeObserved,
		Scope:   scope,
		Fact:    &observedFact,
		Locator: locator,
		Provenance: ContextProvenance{
			Producer: &producer,
			Evidence: []fact.EvidenceRef{{ID: evidenceUnit.ID, Locator: locator}},
		},
		SupportIDs: []string{evidenceUnit.ID},
	}
	entityItem := ContextItem{
		ID:      entity.ID,
		Kind:    ContextItemEntity,
		Origin:  ContextKnowledgeObserved,
		Scope:   scope,
		Entity:  &entity,
		Locator: locator,
		Provenance: ContextProvenance{
			Producer: &producer,
			Evidence: []fact.EvidenceRef{{ID: evidenceUnit.ID, Locator: locator}},
		},
		SupportIDs: []string{evidenceUnit.ID},
	}
	evidenceItem := ContextItem{
		ID:       evidenceUnit.ID,
		Kind:     ContextItemEvidence,
		Origin:   ContextKnowledgeObserved,
		Scope:    scope,
		Evidence: &evidenceUnit,
		Locator:  locator,
		Provenance: ContextProvenance{
			Producer: &producer,
			Evidence: []fact.EvidenceRef{{ID: evidenceUnit.ID, Locator: locator}},
		},
	}

	relation := ContextRelation{
		ID:         "relation-context",
		Predicate:  fact.PredicateReference,
		Origin:     ContextKnowledgeDerived,
		Scope:      scope,
		FromID:     entityItem.ID,
		ToID:       derivedFactItem.ID,
		Path:       []string{entityItem.ID, derivedFactItem.ID},
		SupportIDs: []string{evidenceItem.ID},
		Provenance: ContextProvenance{
			Producer: &producer,
			Lineage:  &fact.Lineage{RuleID: "rule-context", RuleVersion: "v1", InputFactIDs: []string{observedFactID}},
			Evidence: []fact.EvidenceRef{{ID: evidenceUnit.ID, Locator: locator}},
		},
	}
	coverageLocator := locator
	gapLocator := locator
	continuationScope := scope

	return ContextPackage{
		Version:   ContextVersion,
		ID:        "context-package-1",
		Digest:    strings.Repeat("a", 64),
		Revision:  "revision-1",
		Scope:     scope,
		Intent:    contextTestQuestionIntent(),
		Limits:    contextTestLimits(),
		Items:     []ContextItem{observedItem, entityItem, evidenceItem, derivedFactItem},
		Relations: []ContextRelation{relation},
		Coverage: []contract.Coverage{{
			ID:        "coverage-context",
			Dimension: string(contract.DimensionEntitiesAndRelationships),
			State:     contract.CoverageProduced,
			Message:   "relationship coverage",
			Locator:   &coverageLocator,
		}},
		Gaps: []contract.Gap{{
			ID:      "gap-context",
			Code:    "runtime-unobserved",
			Message: "runtime evidence is unavailable",
			Locator: &gapLocator,
		}},
		Degradations: []ContextDegradation{{Code: ContextDegradationVectorUnavailable}},
		Audit: []ContextSelectionAudit{
			{ItemID: observedItem.ID, Included: true, Reason: ContextSelectionIncluded, Rank: 1, Score: 0.95, TokenEstimate: 12, Characters: 48, Bytes: 64},
			{ItemID: entityItem.ID, Included: true, Reason: ContextSelectionIncluded, Rank: 2, Score: 0.85, TokenEstimate: 12, Characters: 40, Bytes: 56},
			{ItemID: evidenceItem.ID, Included: true, Reason: ContextSelectionIncluded, Rank: 3, Score: 0.75, TokenEstimate: 12, Characters: 36, Bytes: 52},
			{ItemID: derivedFactItem.ID, Included: true, Reason: ContextSelectionIncluded, Rank: 4, Score: 0.65, TokenEstimate: 12, Characters: 48, Bytes: 64},
			{ItemID: "candidate-excluded", Included: false, Reason: ContextSelectionExcludedBudget, Rank: 5, Score: 0.25, TokenEstimate: 10, Characters: 40, Bytes: 52},
		},
		TokenEstimate:  48,
		CharactersUsed: 256,
		BytesUsed:      1_024,
		Truncated:      true,
		Continuation: &ContextContinuation{
			Token:            "cursor-context-1",
			Scope:            &continuationScope,
			SnapshotRevision: "revision-1",
			IntentDigest:     strings.Repeat("b", 64),
			PolicyDigest:     strings.Repeat("c", 64),
			AlgorithmVersion: "algorithm-v1",
			Ordering:         "item-id-ascending",
		},
	}
}

func contextTestFact(scope Scope, evidenceID string, locator contract.Locator) fact.CanonicalFact {
	value := &fact.TypedValue{Kind: fact.ValueString, String: "observed-value"}
	factValue := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     fact.Scope{OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID},
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "symbol-context"},
		Value:     value,
		Qualifiers: []fact.Qualifier{{
			Name:  fact.QualifierMethod,
			Value: fact.TypedValue{Kind: fact.ValueString, String: "static"},
		}},
		Producer: fact.Producer{ID: "frontend-context", Version: "v1", Method: "ast"},
		Evidence: []fact.EvidenceRef{{ID: evidenceID, Locator: locator}},
	}
	factValue.ID, _ = fact.FactID(factValue)
	return factValue
}
