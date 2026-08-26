package query

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestContextSnapshotValidate(t *testing.T) {
	t.Parallel()

	valid := contextSnapshotTestValue(t)
	tests := []struct {
		name   string
		mutate func(*ContextSnapshot)
		wantOK bool
	}{
		{
			name:   "valid",
			wantOK: true,
		},
		{
			name: "invalid scope",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Scope.SourceID = "not-a-uuid"
			},
		},
		{
			name: "invalid revision",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Revision = strings.Repeat("r", int(maxContextRevisionBytes)+1)
			},
		},
		{
			name: "invalid fact",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Facts[0].ID = "invalid-fact-id"
			},
		},
		{
			name: "duplicate fact",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Facts = append(snapshot.Facts, snapshot.Facts[0])
			},
		},
		{
			name: "invalid coverage",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Coverage[0].State = contract.CoverageUnknown
			},
		},
		{
			name: "duplicate coverage",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Coverage = append(snapshot.Coverage, snapshot.Coverage[0])
			},
		},
		{
			name: "invalid gap",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Gaps[0].Message = ""
			},
		},
		{
			name: "duplicate gap",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Gaps = append(snapshot.Gaps, snapshot.Gaps[0])
			},
		},
		{
			name: "facts exceed limit",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Facts = make([]fact.CanonicalFact, maxContextItems+1)
			},
		},
		{
			name: "coverage exceeds limit",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Coverage = make([]contract.Coverage, maxContextItems+1)
			},
		},
		{
			name: "gaps exceed limit",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Gaps = make([]contract.Gap, maxContextItems+1)
			},
		},
		{
			name: "fact scope is independently validated",
			mutate: func(snapshot *ContextSnapshot) {
				snapshot.Facts[0].Scope = fact.Scope{
					OrganizationID: contextSnapshotTestUUID(11),
					SourceID:       contextSnapshotTestUUID(12),
					SnapshotID:     contextSnapshotTestUUID(13),
				}
				snapshot.Facts[0].ID = ""
				snapshot.Facts[0].ID, _ = fact.FactID(snapshot.Facts[0])
			},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := valid.Clone()
			if tt.mutate != nil {
				tt.mutate(&snapshot)
			}
			err := snapshot.Validate()
			if tt.wantOK {
				if err != nil {
					t.Fatalf("ContextSnapshot.Validate() error = %v", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidContextSnapshot) {
				t.Fatalf("ContextSnapshot.Validate() error = %v, want errors.Is(_, ErrInvalidContextSnapshot)", err)
			}
			if strings.Contains(err.Error(), "not-a-uuid") || strings.Contains(err.Error(), "invalid-fact-id") {
				t.Fatalf("ContextSnapshot.Validate() echoed untrusted content: %v", err)
			}
		})
	}
}

func TestContextSnapshotCloneIsDeeplyIndependent(t *testing.T) {
	t.Parallel()

	original := contextSnapshotTestValue(t)
	clone := original.Clone()
	clone.Facts[0].Object = &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "clone-object"}
	clone.Facts[0].Value = &fact.TypedValue{Kind: fact.ValueString, String: "clone-value"}
	clone.Facts[0].Qualifiers[0].Name = "clone-qualifier"
	clone.Facts[0].Evidence[0].ID = "clone-evidence"
	clone.Facts[0].Lineage = &fact.Lineage{RuleID: "clone-rule", RuleVersion: "v1", InputFactIDs: []string{"clone-input"}}
	clone.Coverage[0].Locator.Path = "clone-coverage.go"
	clone.Gaps[0].Locator.Path = "clone-gap.go"

	if original.Facts[0].Object != nil || clone.Facts[0].Object == nil ||
		original.Facts[0].Object == clone.Facts[0].Object ||
		original.Facts[0].Value.String == clone.Facts[0].Value.String ||
		original.Facts[0].Qualifiers[0].Name == clone.Facts[0].Qualifiers[0].Name ||
		original.Facts[0].Evidence[0].ID == clone.Facts[0].Evidence[0].ID ||
		original.Facts[0].Lineage.InputFactIDs[0] == clone.Facts[0].Lineage.InputFactIDs[0] ||
		original.Coverage[0].Locator.Path == clone.Coverage[0].Locator.Path ||
		original.Gaps[0].Locator.Path == clone.Gaps[0].Locator.Path {
		t.Fatal("ContextSnapshot.Clone() shares mutable nested state")
	}
}

func contextSnapshotTestValue(t *testing.T) ContextSnapshot {
	t.Helper()

	scope := Scope{
		OrganizationID: contextSnapshotTestUUID(1),
		SourceID:       contextSnapshotTestUUID(2),
		SnapshotID:     contextSnapshotTestUUID(3),
	}
	locator := contract.Locator{Path: "src/context.go", StartLine: 1, EndLine: 2}
	value := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     fact.Scope{OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID},
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: "symbol-context-snapshot"},
		Value:     &fact.TypedValue{Kind: fact.ValueString, String: "observed-value"},
		Qualifiers: []fact.Qualifier{{
			Name:  fact.QualifierMethod,
			Value: fact.TypedValue{Kind: fact.ValueString, String: "static"},
		}},
		Producer: fact.Producer{ID: "frontend-context-snapshot", Version: "v1", Method: "ast"},
		Evidence: []fact.EvidenceRef{{ID: "evidence-context-snapshot", Locator: locator}},
		Lineage: &fact.Lineage{
			RuleID:       "rule-context-snapshot",
			RuleVersion:  "v1",
			InputFactIDs: []string{"input-context-snapshot"},
		},
	}
	value.ID, _ = fact.FactID(value)
	coverageLocator := locator
	gapLocator := locator
	return ContextSnapshot{
		Scope:    scope,
		Revision: "revision-context-snapshot",
		Facts:    []fact.CanonicalFact{value},
		Coverage: []contract.Coverage{{
			ID:        "coverage-context-snapshot",
			Dimension: string(contract.DimensionEntitiesAndRelationships),
			State:     contract.CoverageProduced,
			Locator:   &coverageLocator,
		}},
		Gaps: []contract.Gap{{
			ID:      "gap-context-snapshot",
			Code:    "runtime-unobserved",
			Message: "runtime evidence is unavailable",
			Locator: &gapLocator,
		}},
	}
}

func contextSnapshotTestUUID(value int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", value)
}
