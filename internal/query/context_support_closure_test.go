package query

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCloseContextSupportIncludesRelationAndRequiredSupportAtomically(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("closure-from", "closure-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("closure-to", "closure-to"), 0.9, 2, "to", 1, 1, 1)
	support := closureTestCandidate(selectionTestEntityItem("closure-support", "closure-support"), 0, 3, "support", 2, 3, 4)
	request := closureTestRequest(contextTestLimits(), from, to, support)
	base := closureTestBase(t, request)
	relation := closureTestRelation("closure-relation", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request:   request,
		Base:      base,
		Relations: []ContextRelationCandidate{closureTestRelationCandidate(relation, 0.9, 1, 5, 7, 9)},
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if err := result.ValidateAgainst(closureRequest); err != nil {
		t.Fatalf("result.ValidateAgainst() error = %v", err)
	}
	if got := relationIDs(result.Relations); !reflect.DeepEqual(got, []string{relation.ID}) {
		t.Fatalf("relations = %#v, want %#v", got, []string{relation.ID})
	}
	if result.Selection.ItemCount != 3 || result.ItemCount != 3 || result.RelationCount != 1 || result.TotalCount != 4 {
		t.Fatalf("counts = items:%d/%d relations:%d total:%d, want 3/3/1/4", result.Selection.ItemCount, result.ItemCount, result.RelationCount, result.TotalCount)
	}
	if result.Selection.TokenEstimate != base.TokenEstimate+support.TokenCost ||
		result.Selection.CharactersUsed != base.CharactersUsed+support.CharacterCost ||
		result.Selection.BytesUsed != base.BytesUsed+support.ByteCost {
		t.Fatalf("selection accounting = tokens:%d chars:%d bytes:%d, want support added to base", result.Selection.TokenEstimate, result.Selection.CharactersUsed, result.Selection.BytesUsed)
	}
	if result.TokenEstimate != result.Selection.TokenEstimate+5 || result.CharactersUsed != result.Selection.CharactersUsed+7 || result.BytesUsed != result.Selection.BytesUsed+9 {
		t.Fatalf("closure accounting = tokens:%d chars:%d bytes:%d, want relation cost included", result.TokenEstimate, result.CharactersUsed, result.BytesUsed)
	}
	if result.SupportIncomplete || result.BudgetExhausted {
		t.Fatalf("closure status = incomplete:%t exhausted:%t, want false/false", result.SupportIncomplete, result.BudgetExhausted)
	}
	if audit := closureTestRelationAudit(t, result, relation.ID); !audit.Included || audit.Reason != ContextRelationIncluded {
		t.Fatalf("relation audit = %#v, want included", audit)
	}
	if audit := selectionTestAudit(t, result.Selection, support.Item.ID); !audit.Included || audit.Reason != ContextSelectionIncluded {
		t.Fatalf("support audit = %#v, want included", audit)
	}
}

func TestCloseContextSupportExcludesRelationWithoutSupport(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("missing-from", "missing-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("missing-to", "missing-to"), 0.9, 2, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to)
	base := closureTestBase(t, request)
	relation := closureTestRelation("missing-relation", from.Item.ID, to.Item.ID, []string{"not-selected"}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request:   request,
		Base:      base,
		Relations: []ContextRelationCandidate{closureTestRelationCandidate(relation, 1, 1, 2, 2, 2)},
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 0 {
		t.Fatalf("relations = %#v, want none", result.Relations)
	}
	if !result.SupportIncomplete || result.BudgetExhausted {
		t.Fatalf("closure status = incomplete:%t exhausted:%t, want true/false", result.SupportIncomplete, result.BudgetExhausted)
	}
	if result.Selection.ItemCount != base.ItemCount || result.TokenEstimate != base.TokenEstimate || result.CharactersUsed != base.CharactersUsed || result.BytesUsed != base.BytesUsed {
		t.Fatalf("missing support changed base accounting: result=%#v base=%#v", result, base)
	}
	audit := closureTestRelationAudit(t, result, relation.ID)
	if audit.Included || audit.Reason != ContextRelationExcludedMissingSupport || !reflect.DeepEqual(audit.MissingIDs, []string{"not-selected"}) {
		t.Fatalf("relation audit = %#v, want missing support", audit)
	}
	if err := result.ValidateAgainst(closureRequest); err != nil {
		t.Fatalf("result.ValidateAgainst() error = %v", err)
	}
}

func TestCloseContextSupportExcludesInvalidSupportWithoutPartialMutation(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("invalid-from", "invalid-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("invalid-to", "invalid-to"), 0.9, 2, "to", 1, 1, 1)
	validSupport := closureTestCandidate(selectionTestEntityItem("valid-support", "valid-support"), 0, 3, "valid-support", 2, 2, 2)
	invalidItem := selectionTestEntityItem("invalid-support", "invalid-support")
	invalidItem.Entity = nil
	invalidSupport := closureTestCandidate(invalidItem, 0, 4, "invalid-support", 3, 3, 3)
	request := closureTestRequest(contextTestLimits(), from, to, validSupport, invalidSupport)
	base := closureTestBase(t, request)
	relation := closureTestRelation("invalid-support-relation", from.Item.ID, to.Item.ID, []string{validSupport.Item.ID, invalidSupport.Item.ID}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request:   request,
		Base:      base,
		Relations: []ContextRelationCandidate{closureTestRelationCandidate(relation, 1, 1, 1, 1, 1)},
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 0 || result.Selection.ItemCount != base.ItemCount {
		t.Fatalf("invalid support produced partial closure: relations=%#v items=%d base-items=%d", result.Relations, result.Selection.ItemCount, base.ItemCount)
	}
	audit := closureTestRelationAudit(t, result, relation.ID)
	if audit.Included || audit.Reason != ContextRelationExcludedMissingSupport {
		t.Fatalf("relation audit = %#v, want missing support", audit)
	}
	if !reflect.DeepEqual(audit.MissingIDs, []string{invalidSupport.Item.ID}) {
		t.Fatalf("missing IDs = %#v, want invalid support only", audit.MissingIDs)
	}
	if supportAudit := selectionTestAudit(t, result.Selection, validSupport.Item.ID); supportAudit.Included {
		t.Fatalf("valid support was partially included: %#v", supportAudit)
	}
}

func TestCloseContextSupportExcludesScopeFailures(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("scope-from", "scope-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("scope-to", "scope-to"), 0.9, 2, "to", 1, 1, 1)
	otherScope := contextTestScope()
	otherScope.SnapshotID = contextTestUUID(99)
	otherItem := selectionTestEntityItem("other-scope-support", "other-scope-support")
	otherItem.Scope = otherScope
	otherSupport := closureTestCandidate(otherItem, 0, 3, "other-scope-support", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to, otherSupport)
	base := closureTestBase(t, request)
	relation := closureTestRelation("scope-relation", from.Item.ID, to.Item.ID, []string{otherSupport.Item.ID}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request:   request,
		Base:      base,
		Relations: []ContextRelationCandidate{closureTestRelationCandidate(relation, 1, 1, 1, 1, 1)},
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 0 {
		t.Fatalf("relations = %#v, want none", result.Relations)
	}
	audit := closureTestRelationAudit(t, result, relation.ID)
	if audit.Reason != ContextRelationExcludedScope || !reflect.DeepEqual(audit.MissingIDs, []string{otherSupport.Item.ID}) {
		t.Fatalf("relation audit = %#v, want scope exclusion", audit)
	}
	if result.SupportIncomplete {
		t.Fatal("scope exclusion was reported as missing support")
	}
}

func TestCloseContextSupportExcludesRelationOutsideScope(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("relation-scope-from", "relation-scope-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("relation-scope-to", "relation-scope-to"), 0.9, 2, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to)
	base := closureTestBase(t, request)
	otherScope := contextTestScope()
	otherScope.SnapshotID = contextTestUUID(98)
	relation := closureTestRelation("relation-outside-scope", from.Item.ID, to.Item.ID, []string{from.Item.ID}, nil)
	relation.Scope = otherScope
	closureRequest := ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relation, 1, 1, 1, 1, 1),
		},
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 0 {
		t.Fatalf("relations = %#v, want none", result.Relations)
	}
	audit := closureTestRelationAudit(t, result, relation.ID)
	if audit.Reason != ContextRelationExcludedScope {
		t.Fatalf("relation audit = %#v, want scope exclusion", audit)
	}
	if result.SupportIncomplete {
		t.Fatal("relation scope exclusion was reported as missing support")
	}
}

func TestCloseContextSupportExcludesSemanticallyInvalidRelationWithSafeAuditIdentity(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("invalid-relation-from", "invalid-relation-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("invalid-relation-to", "invalid-relation-to"), 0.9, 2, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to)
	base := closureTestBase(t, request)
	invalidRelation := closureTestRelation("safe-invalid-relation", from.Item.ID, "missing-endpoint", nil, nil)
	closureRequest := ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(invalidRelation, 0.8, 1, 1, 1, 1),
		},
	}
	if err := closureRequest.Validate(); err != nil {
		t.Fatalf("Validate() rejected safe metadata with semantic relation error: %v", err)
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 0 {
		t.Fatalf("relations = %#v, want none", result.Relations)
	}
	audit := closureTestRelationAudit(t, result, invalidRelation.ID)
	if audit.Included || audit.Reason != ContextRelationExcludedInvalid {
		t.Fatalf("relation audit = %#v, want controlled invalid exclusion", audit)
	}
	if err := result.ValidateAgainst(closureRequest); err != nil {
		t.Fatalf("ValidateAgainst() error = %v", err)
	}
}

func TestCloseContextSupportEnforcesEachBudgetIncludingRelationCost(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*ContextLimits)
		relation ContextRelationCandidate
	}{
		{
			name: "tokens",
			mutate: func(limits *ContextLimits) {
				limits.MaxTokens = 3
			},
			relation: closureTestRelationCandidate(closureTestRelation("budget-tokens-relation", "budget-tokens-from", "budget-tokens-to", []string{"budget-tokens-support"}, nil), 1, 1, 2, 1, 1),
		},
		{
			name: "characters",
			mutate: func(limits *ContextLimits) {
				limits.MaxCharacters = 3
			},
			relation: closureTestRelationCandidate(closureTestRelation("budget-characters-relation", "budget-characters-from", "budget-characters-to", []string{"budget-characters-support"}, nil), 1, 1, 1, 2, 1),
		},
		{
			name: "bytes",
			mutate: func(limits *ContextLimits) {
				limits.MaxBytes = 3
			},
			relation: closureTestRelationCandidate(closureTestRelation("budget-bytes-relation", "budget-bytes-from", "budget-bytes-to", []string{"budget-bytes-support"}, nil), 1, 1, 1, 1, 2),
		},
		{
			name: "items",
			mutate: func(limits *ContextLimits) {
				limits.MaxItems = 2
			},
			relation: closureTestRelationCandidate(closureTestRelation("budget-items-relation", "budget-items-from", "budget-items-to", []string{"budget-items-support"}, nil), 1, 1, 1, 1, 1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fromID := tt.relation.Relation.FromID
			toID := tt.relation.Relation.ToID
			supportID := tt.relation.Relation.SupportIDs[0]
			from := closureTestCandidate(selectionTestEntityItem(fromID, fromID), 1, 1, "from", 1, 1, 1)
			to := closureTestCandidate(selectionTestEntityItem(toID, toID), 0.9, 2, "to", 1, 1, 1)
			supportCost := 1
			support := closureTestCandidate(selectionTestEntityItem(supportID, supportID), 0, 3, "support", supportCost, int64(supportCost), int64(supportCost))
			limits := contextTestLimits()
			tt.mutate(&limits)
			request := closureTestRequest(limits, from, to, support)
			base := closureTestBase(t, request)
			result, err := CloseContextSupport(context.Background(), ContextSupportClosureRequest{
				Request:   request,
				Base:      base,
				Relations: []ContextRelationCandidate{tt.relation},
			})
			if err != nil {
				t.Fatalf("CloseContextSupport() error = %v", err)
			}
			if len(result.Relations) != 0 {
				t.Fatalf("relations = %#v, want none", result.Relations)
			}
			audit := closureTestRelationAudit(t, result, tt.relation.Relation.ID)
			if audit.Reason != ContextRelationExcludedBudget {
				t.Fatalf("relation audit = %#v, want budget", audit)
			}
			if !result.BudgetExhausted {
				t.Fatal("BudgetExhausted = false, want true")
			}
			if result.Selection.ItemCount != base.ItemCount || result.TokenEstimate != base.TokenEstimate || result.CharactersUsed != base.CharactersUsed || result.BytesUsed != base.BytesUsed {
				t.Fatalf("budget exclusion partially changed result: result=%#v base=%#v", result, base)
			}
		})
	}
}

func TestCloseContextSupportCountsSharedSupportOnce(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("shared-from", "shared-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("shared-to", "shared-to"), 0.9, 2, "to", 1, 1, 1)
	support := closureTestCandidate(selectionTestEntityItem("shared-support", "shared-support"), 0, 3, "support", 4, 5, 6)
	request := closureTestRequest(contextTestLimits(), from, to, support)
	base := closureTestBase(t, request)
	relationA := closureTestRelation("shared-relation-a", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil)
	relationB := closureTestRelation("shared-relation-b", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relationB, 0.8, 2, 2, 3, 4),
			closureTestRelationCandidate(relationA, 0.9, 1, 2, 3, 4),
		},
	}

	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if got := relationIDs(result.Relations); !reflect.DeepEqual(got, []string{relationA.ID, relationB.ID}) {
		t.Fatalf("relations = %#v, want A/B", got)
	}
	if result.Selection.ItemCount != base.ItemCount+1 {
		t.Fatalf("item count = %d, want one shared support added", result.Selection.ItemCount)
	}
	wantTokens := base.TokenEstimate + support.TokenCost + 2 + 2
	wantCharacters := base.CharactersUsed + support.CharacterCost + 3 + 3
	wantBytes := base.BytesUsed + support.ByteCost + 4 + 4
	if result.TokenEstimate != wantTokens || result.CharactersUsed != wantCharacters || result.BytesUsed != wantBytes {
		t.Fatalf("accounting = tokens:%d chars:%d bytes:%d, want %d/%d/%d", result.TokenEstimate, result.CharactersUsed, result.BytesUsed, wantTokens, wantCharacters, wantBytes)
	}
}

func TestCloseContextSupportAddsPathNodesAndDeduplicatesRequiredIDs(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("path-from", "path-from"), 1, 1, "from", 1, 1, 1)
	middle := closureTestCandidate(selectionTestEntityItem("path-middle", "path-middle"), 0, 2, "middle", 2, 2, 2)
	to := closureTestCandidate(selectionTestEntityItem("path-to", "path-to"), 0.9, 3, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, middle, to)
	base := closureTestBase(t, request)
	relation := closureTestRelation("path-relation", from.Item.ID, to.Item.ID, []string{middle.Item.ID}, []string{from.Item.ID, middle.Item.ID, to.Item.ID})
	result, err := CloseContextSupport(context.Background(), ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relation, 1, 1, 3, 4, 5),
		},
	})
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 1 || result.Selection.ItemCount != base.ItemCount+1 {
		t.Fatalf("path closure = relations:%d items:%d, want 1/%d", len(result.Relations), result.Selection.ItemCount, base.ItemCount+1)
	}
	if result.Selection.TokenEstimate != base.TokenEstimate+middle.TokenCost || result.TokenEstimate != result.Selection.TokenEstimate+3 {
		t.Fatalf("path accounting = selection:%d closure:%d, want one middle and relation cost", result.Selection.TokenEstimate, result.TokenEstimate)
	}
}

func TestCloseContextSupportAcceptsZeroAndSuppliedUTF8Costs(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("zero-from", "zero-from"), 1, 1, "from", 0, 0, 0)
	to := closureTestCandidate(selectionTestEntityItem("zero-to", "zero-to"), 0.9, 2, "to", 0, 0, 0)
	support := closureTestCandidate(selectionTestEntityItem("utf8-support", "artefato-ação"), 0, 3, "support", 0, 2, 4)
	request := closureTestRequest(contextTestLimits(), from, to, support)
	base := closureTestBase(t, request)
	relation := closureTestRelation("zero-utf8-relation", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil)
	result, err := CloseContextSupport(context.Background(), ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relation, 1, 1, 0, 3, 5),
		},
	})
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if len(result.Relations) != 1 || result.Selection.ItemCount != base.ItemCount+1 {
		t.Fatalf("zero/UTF-8 closure = relations:%d items:%d, want 1/%d", len(result.Relations), result.Selection.ItemCount, base.ItemCount+1)
	}
	if result.Selection.TokenEstimate != 0 || result.Selection.CharactersUsed != 2 || result.Selection.BytesUsed != 4 {
		t.Fatalf("selection costs = tokens:%d chars:%d bytes:%d, want 0/2/4", result.Selection.TokenEstimate, result.Selection.CharactersUsed, result.Selection.BytesUsed)
	}
	if result.TokenEstimate != 0 || result.CharactersUsed != 5 || result.BytesUsed != 9 {
		t.Fatalf("closure costs = tokens:%d chars:%d bytes:%d, want 0/5/9", result.TokenEstimate, result.CharactersUsed, result.BytesUsed)
	}
}

func TestCloseContextSupportOrdersRelationsByScoreRankAndID(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("order-from", "order-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("order-to", "order-to"), 0.9, 2, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to)
	base := closureTestBase(t, request)
	relationZ := closureTestRelation("order-z", from.Item.ID, to.Item.ID, []string{from.Item.ID}, nil)
	relationRank := closureTestRelation("order-rank", from.Item.ID, to.Item.ID, []string{from.Item.ID}, nil)
	relationA := closureTestRelation("order-a", from.Item.ID, to.Item.ID, []string{from.Item.ID}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relationZ, 0.8, 2, 1, 1, 1),
			closureTestRelationCandidate(relationA, 0.9, 2, 1, 1, 1),
			closureTestRelationCandidate(relationRank, 0.9, 1, 1, 1, 1),
		},
	}
	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	want := []string{relationRank.ID, relationA.ID, relationZ.ID}
	if got := relationIDs(result.Relations); !reflect.DeepEqual(got, want) {
		t.Fatalf("relations = %#v, want %#v", got, want)
	}
	if got := relationAuditIDs(result.RelationAudit); !reflect.DeepEqual(got, want) {
		t.Fatalf("relation audit IDs = %#v, want %#v", got, want)
	}
}

func TestCloseContextSupportIsDeterministicUnderRelationPermutation(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("perm-from", "perm-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("perm-to", "perm-to"), 0.9, 2, "to", 1, 1, 1)
	support := closureTestCandidate(selectionTestEntityItem("perm-support", "perm-support"), 0, 3, "support", 2, 2, 2)
	request := closureTestRequest(contextTestLimits(), from, to, support)
	base := closureTestBase(t, request)
	relations := []ContextRelationCandidate{
		closureTestRelationCandidate(closureTestRelation("perm-z", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil), 0.7, 3, 1, 1, 1),
		closureTestRelationCandidate(closureTestRelation("perm-a", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil), 0.7, 3, 1, 1, 1),
		closureTestRelationCandidate(closureTestRelation("perm-m", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil), 0.8, 2, 1, 1, 1),
	}
	forwardRequest := ContextSupportClosureRequest{Request: request, Base: base, Relations: relations}
	reverseRequest := ContextSupportClosureRequest{Request: request, Base: base, Relations: []ContextRelationCandidate{relations[1], relations[0], relations[2]}}
	forward, err := CloseContextSupport(context.Background(), forwardRequest)
	if err != nil {
		t.Fatalf("forward CloseContextSupport() error = %v", err)
	}
	reverse, err := CloseContextSupport(context.Background(), reverseRequest)
	if err != nil {
		t.Fatalf("reverse CloseContextSupport() error = %v", err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("closure changed under candidate permutation:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
}

func TestContextSupportClosureRejectsDuplicateRelationIDs(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("duplicate-relation-from", "duplicate-relation-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("duplicate-relation-to", "duplicate-relation-to"), 0.9, 2, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to)
	base := closureTestBase(t, request)
	relation := closureTestRelation("duplicate-relation", from.Item.ID, to.Item.ID, []string{from.Item.ID}, nil)
	candidate := closureTestRelationCandidate(relation, 1, 1, 1, 1, 1)
	closureRequest := ContextSupportClosureRequest{
		Request:   request,
		Base:      base,
		Relations: []ContextRelationCandidate{candidate, candidate},
	}
	if err := closureRequest.Validate(); !errors.Is(err, ErrInvalidContextSelection) {
		t.Fatalf("Validate() error = %v, want duplicate relation ID", err)
	}
}

func TestCloseContextSupportDoesNotMutateBaseOrRequest(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("immutable-from", "immutable-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("immutable-to", "immutable-to"), 0.9, 2, "to", 1, 1, 1)
	support := closureTestCandidate(selectionTestEntityItem("immutable-support", "immutable-support"), 0, 3, "support", 2, 2, 2)
	request := closureTestRequest(contextTestLimits(), from, to, support)
	base := closureTestBase(t, request)
	requestBefore := cloneSelectionTestRequest(request)
	baseBefore := cloneContextSelectionResult(base)
	relation := closureTestRelation("immutable-relation", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil)
	result, err := CloseContextSupport(context.Background(), ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relation, 1, 1, 1, 1, 1),
		},
	})
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if !reflect.DeepEqual(request, requestBefore) {
		t.Fatalf("closure mutated request:\nbefore=%#v\nafter=%#v", requestBefore, request)
	}
	if !reflect.DeepEqual(base, baseBefore) {
		t.Fatalf("closure mutated base:\nbefore=%#v\nafter=%#v", baseBefore, base)
	}
	result.Selection.Items[0].Entity.ID = "mutated-result"
	if base.Items[0].Entity != nil && base.Items[0].Entity.ID == "mutated-result" {
		t.Fatal("result selection shares mutable item state with base")
	}
}

func TestCloseContextSupportHonorsCancellation(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("cancel-from", "cancel-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("cancel-to", "cancel-to"), 0.9, 2, "to", 1, 1, 1)
	request := closureTestRequest(contextTestLimits(), from, to)
	base := closureTestBase(t, request)
	relation := closureTestRelation("cancel-relation", from.Item.ID, to.Item.ID, []string{from.Item.ID}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CloseContextSupport(ctx, ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relation, 1, 1, 1, 1, 1),
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CloseContextSupport() error = %v, want context.Canceled", err)
	}
}

func TestContextSupportClosureValidateAndValidateAgainstRejectAdulteration(t *testing.T) {
	t.Parallel()

	from := closureTestCandidate(selectionTestEntityItem("validate-from", "validate-from"), 1, 1, "from", 1, 1, 1)
	to := closureTestCandidate(selectionTestEntityItem("validate-to", "validate-to"), 0.9, 2, "to", 1, 1, 1)
	support := closureTestCandidate(selectionTestEntityItem("validate-support", "validate-support"), 0, 3, "support", 2, 2, 2)
	request := closureTestRequest(contextTestLimits(), from, to, support)
	base := closureTestBase(t, request)
	relation := closureTestRelation("validate-relation", from.Item.ID, to.Item.ID, []string{support.Item.ID}, nil)
	closureRequest := ContextSupportClosureRequest{
		Request: request,
		Base:    base,
		Relations: []ContextRelationCandidate{
			closureTestRelationCandidate(relation, 1, 1, 3, 3, 3),
		},
	}
	result, err := CloseContextSupport(context.Background(), closureRequest)
	if err != nil {
		t.Fatalf("CloseContextSupport() error = %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("normal result Validate() error = %v", err)
	}
	if err := result.ValidateAgainst(closureRequest); err != nil {
		t.Fatalf("normal result ValidateAgainst() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ContextSupportClosureResult)
	}{
		{
			name: "relation token accounting",
			mutate: func(value *ContextSupportClosureResult) {
				value.TokenEstimate++
			},
		},
		{
			name: "relation support ID",
			mutate: func(value *ContextSupportClosureResult) {
				value.Relations[0].SupportIDs[0] = "other-support"
			},
		},
		{
			name: "relation audit identity",
			mutate: func(value *ContextSupportClosureResult) {
				value.RelationAudit[0].RelationID = "other-relation"
			},
		},
		{
			name: "relation audit cost",
			mutate: func(value *ContextSupportClosureResult) {
				value.RelationAudit[0].ByteCost++
			},
		},
		{
			name: "relation count",
			mutate: func(value *ContextSupportClosureResult) {
				value.RelationCount++
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := result
			mutated.Relations = append([]ContextRelation(nil), result.Relations...)
			mutated.Relations[0].SupportIDs = append([]string(nil), result.Relations[0].SupportIDs...)
			mutated.RelationAudit = append([]ContextRelationAudit(nil), result.RelationAudit...)
			tt.mutate(&mutated)
			if err := mutated.Validate(); err == nil {
				t.Fatalf("Validate() accepted adulterated %s", tt.name)
			}
			if err := mutated.ValidateAgainst(closureRequest); err == nil {
				t.Fatalf("ValidateAgainst() accepted adulterated %s", tt.name)
			}
		})
	}
}

func closureTestRequest(limits ContextLimits, candidates ...ContextSelectionCandidate) ContextSelectionRequest {
	return selectionTestRequest(limits, selectionTestConfiguration(1, 0, 0, 0), candidates...)
}

func closureTestBase(t *testing.T, request ContextSelectionRequest) ContextSelectionResult {
	t.Helper()
	base, err := SelectContext(context.Background(), request)
	if err != nil {
		t.Fatalf("SelectContext() error = %v", err)
	}
	return base
}

func closureTestCandidate(item ContextItem, relevance float64, rank int, redundancyKey string, tokens int, characters, bytes int64) ContextSelectionCandidate {
	return selectionTestCandidate(item, relevance, rank, nil, redundancyKey, tokens, characters, bytes)
}

func closureTestRelation(id, fromID, toID string, supportIDs, path []string) ContextRelation {
	relation := cloneContextRelation(contextTestPackage().Relations[0])
	relation.ID = id
	relation.FromID = fromID
	relation.ToID = toID
	relation.SupportIDs = append([]string(nil), supportIDs...)
	relation.Path = append([]string(nil), path...)
	return relation
}

func closureTestRelationCandidate(relation ContextRelation, score float64, rank int, tokens int, characters, bytes int64) ContextRelationCandidate {
	return ContextRelationCandidate{
		Relation:      relation,
		Score:         score,
		Rank:          rank,
		TokenCost:     tokens,
		CharacterCost: characters,
		ByteCost:      bytes,
	}
}

func closureTestRelationAudit(t *testing.T, result ContextSupportClosureResult, relationID string) ContextRelationAudit {
	t.Helper()
	for _, audit := range result.RelationAudit {
		if audit.RelationID == relationID {
			return audit
		}
	}
	t.Fatalf("relation audit for %q not found in %#v", relationID, result.RelationAudit)
	return ContextRelationAudit{}
}

func relationIDs(relations []ContextRelation) []string {
	ids := make([]string, 0, len(relations))
	for _, relation := range relations {
		ids = append(ids, relation.ID)
	}
	return ids
}

func relationAuditIDs(audits []ContextRelationAudit) []string {
	ids := make([]string, 0, len(audits))
	for _, audit := range audits {
		ids = append(ids, audit.RelationID)
	}
	return ids
}
