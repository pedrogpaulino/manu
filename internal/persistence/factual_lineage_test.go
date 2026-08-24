package persistence

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

func TestFactualLineageInspectionTraversesSupportChainAndDeduplicatesDiamond(t *testing.T) {
	snapshot := factualLineageGraphFixture(t)
	index, err := NewFactualLineageIndex(snapshot)
	if err != nil {
		t.Fatalf("NewFactualLineageIndex() error = %v", err)
	}
	root := factualLineageFact(t, snapshot, "derived-final")

	inspection, err := index.InspectSupport(root.ID)
	if err != nil {
		t.Fatalf("InspectSupport() error = %v", err)
	}
	if inspection.Scope != snapshot.Scope || inspection.Root.ID != root.ID {
		t.Fatalf("scope/root = %#v/%q, want %#v/%q", inspection.Scope, inspection.Root.ID, snapshot.Scope, root.ID)
	}
	if got, want := len(inspection.Facts), 6; got != want {
		t.Fatalf("support fact count = %d, want %d", got, want)
	}
	if got, want := len(inspection.Edges), 6; got != want {
		t.Fatalf("support edge count = %d, want %d", got, want)
	}
	if !sortedLineageFacts(inspection.Facts) || !sortedLineageEdges(inspection.Edges) {
		t.Fatalf("support result is not deterministically ordered: %#v", inspection)
	}
	if got := countLineageFact(inspection.Facts, factualLineageFact(t, snapshot, "observed-a").ID); got != 1 {
		t.Fatalf("shared observed-a fact count = %d, want 1", got)
	}
	if got := countLineageFact(inspection.Facts, factualLineageFact(t, snapshot, "observed-b").ID); got != 1 {
		t.Fatalf("observed-b fact count = %d, want 1", got)
	}
	if got := countLineageFact(inspection.Facts, factualLineageFact(t, snapshot, "observed-c").ID); got != 1 {
		t.Fatalf("observed-c fact count = %d, want 1", got)
	}
	for _, candidate := range inspection.Facts {
		if candidate.ID == root.ID {
			continue
		}
		if candidate.Lineage == nil && candidate.Producer.ID != "java" {
			t.Fatalf("non-observed fact without lineage: %#v", candidate)
		}
	}
}

func TestFactualLineageInspectionReverseLookupUsesFanoutAndRecurses(t *testing.T) {
	snapshot := factualLineageGraphFixture(t)
	index, err := NewFactualLineageIndex(snapshot)
	if err != nil {
		t.Fatalf("NewFactualLineageIndex() error = %v", err)
	}
	root := factualLineageFact(t, snapshot, "observed-a")

	inspection, err := index.InspectDependents(root.ID)
	if err != nil {
		t.Fatalf("InspectDependents() error = %v", err)
	}
	if inspection.Root.ID != root.ID {
		t.Fatalf("reverse root = %q, want %q", inspection.Root.ID, root.ID)
	}
	if got, want := len(inspection.Facts), 4; got != want {
		t.Fatalf("reverse fact count = %d, want %d", got, want)
	}
	if got, want := len(inspection.Edges), 4; got != want {
		t.Fatalf("reverse edge count = %d, want %d", got, want)
	}
	for _, candidate := range inspection.Facts {
		if candidate.ID == root.ID || candidate.Producer.ID == "rule-engine" {
			continue
		}
		t.Fatalf("reverse lookup included unrelated fact %q", candidate.ID)
	}
	if !sortedLineageFacts(inspection.Facts) || !sortedLineageEdges(inspection.Edges) {
		t.Fatalf("reverse result is not deterministically ordered: %#v", inspection)
	}
}

func TestFactualLineageInspectionIsPermutationInvariantAndDefensive(t *testing.T) {
	base := factualLineageGraphFixture(t)
	baseBefore := cloneFactualSnapshotInput(base)
	firstIndex, err := NewFactualLineageIndex(base)
	if err != nil {
		t.Fatalf("first NewFactualLineageIndex() error = %v", err)
	}
	root := factualLineageFact(t, base, "derived-final")
	first, err := firstIndex.InspectSupport(root.ID)
	if err != nil {
		t.Fatalf("first InspectSupport() error = %v", err)
	}

	permuted := cloneFactualSnapshotInput(base)
	sort.Slice(permuted.FrontendManifests, func(left, right int) bool {
		return permuted.FrontendManifests[left].ID > permuted.FrontendManifests[right].ID
	})
	sort.Slice(permuted.RuleVersions, func(left, right int) bool {
		return permuted.RuleVersions[left].RuleID > permuted.RuleVersions[right].RuleID
	})
	sort.Slice(permuted.Facts, func(left, right int) bool {
		return permuted.Facts[left].ID > permuted.Facts[right].ID
	})
	for index := range permuted.Facts {
		permuteFactCollections(&permuted.Facts[index])
	}
	secondIndex, err := NewFactualLineageIndex(permuted)
	if err != nil {
		t.Fatalf("permuted NewFactualLineageIndex() error = %v", err)
	}
	second, err := secondIndex.InspectSupport(root.ID)
	if err != nil {
		t.Fatalf("permuted InspectSupport() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("permutation changed inspection:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if !reflect.DeepEqual(base, baseBefore) {
		t.Fatal("fixture clone changed the original snapshot")
	}

	first.Facts[0].Evidence[0].Locator.Path = "mutated/path"
	first.Facts[0].Lineage = nil
	first.Root.Evidence[0].Locator.Path = "mutated/root"
	secondCall, err := firstIndex.InspectSupport(root.ID)
	if err != nil {
		t.Fatalf("defensive InspectSupport() error = %v", err)
	}
	for _, candidate := range secondCall.Facts {
		if candidate.Lineage == nil && candidate.Producer.ID == "rule-engine" {
			t.Fatal("mutating a returned fact changed the index lineage")
		}
		for _, evidence := range candidate.Evidence {
			if evidence.Locator.Path == "mutated/path" || evidence.Locator.Path == "mutated/root" {
				t.Fatal("mutating a returned fact changed the index evidence")
			}
		}
	}
}

func TestFactualLineageInspectionRejectsMissingRootsAndInvalidIDs(t *testing.T) {
	index, err := NewFactualLineageIndex(factualLineageGraphFixture(t))
	if err != nil {
		t.Fatalf("NewFactualLineageIndex() error = %v", err)
	}
	for _, factID := range []string{"fact-does-not-exist", ""} {
		_, err := index.InspectSupport(factID)
		if factID == "" {
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("empty root error = %v, want invalid input", err)
			}
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing root error = %v, want not found", err)
		}
	}
}

func TestFactualLineageIndexRejectsMissingReferencesAndCyclesWithoutPayload(t *testing.T) {
	base := factualLineageGraphFixture(t)
	missing := cloneFactualSnapshotInput(base)
	missingFact := factualLineageFact(t, missing, "derived-final")
	for index := range missing.Facts {
		if missing.Facts[index].ID == missingFact.ID {
			missing.Facts[index].Lineage.InputFactIDs = []string{"fact-missing"}
		}
	}
	_, err := NewFactualLineageIndex(missing)
	if !errors.Is(err, ErrInvalidFactualSnapshot) || strings.Contains(err.Error(), "fact-missing") {
		t.Fatalf("missing reference error = %v, want redacted invalid factual snapshot", err)
	}

	cycle := cloneFactualSnapshotInput(base)
	first := factualLineageFact(t, cycle, "derived-left")
	second := factualLineageFact(t, cycle, "derived-right")
	for index := range cycle.Facts {
		switch cycle.Facts[index].ID {
		case first.ID:
			cycle.Facts[index].Lineage.InputFactIDs = []string{second.ID}
		case second.ID:
			cycle.Facts[index].Lineage.InputFactIDs = []string{first.ID}
		}
	}
	_, err = NewFactualLineageIndex(cycle)
	if !errors.Is(err, ErrInconsistent) || strings.Contains(err.Error(), first.ID) || strings.Contains(err.Error(), second.ID) {
		t.Fatalf("cycle error = %v, want redacted inconsistent", err)
	}
}

func TestInspectFactualLineageValidatesBeforeReadingScopedSnapshot(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	_, err := repository.InspectFactualLineage(context.Background(), "not-a-uuid", testUUID(2), testUUID(3), "fact-root")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("InspectFactualLineage() error = %v, want invalid input", err)
	}
	if starter.beginCalls != 0 {
		t.Fatalf("Begin calls = %d, want 0", starter.beginCalls)
	}
	_, err = repository.InspectFactualDependents(context.Background(), testUUID(1), testUUID(2), testUUID(3), "")
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("InspectFactualDependents() empty ID error = %v, want invalid input", err)
	}
	if starter.beginCalls != 0 {
		t.Fatalf("Begin calls after empty ID = %d, want 0", starter.beginCalls)
	}
}

func factualLineageGraphFixture(t *testing.T) FactualSnapshotInput {
	t.Helper()
	scope := fact.Scope{OrganizationID: "lineage-org", SourceID: "lineage-source", SnapshotID: "lineage-snapshot"}
	manifest := validFrontendManifest("java", "1", "symbols")
	rule := RuleVersion{RuleID: "dependency", Version: "1", ImplementationDigest: strings.Repeat("a", 64)}
	snapshot := FactualSnapshotInput{
		OrganizationID:    identity.CanonicalUUID("organization", scope.OrganizationID),
		SourceID:          identity.CanonicalUUID("source", scope.OrganizationID, scope.SourceID),
		SnapshotID:        identity.CanonicalUUID("snapshot", scope.OrganizationID, scope.SourceID, scope.SnapshotID),
		Scope:             scope,
		FrontendManifests: []fact.FrontendManifest{manifest},
		RuleVersions:      []RuleVersion{rule},
	}
	observedA := factualLineageObserved(scope, "observed-a")
	observedB := factualLineageObserved(scope, "observed-b")
	observedC := factualLineageObserved(scope, "observed-c")
	derivedLeft := factualLineageDerived(scope, "derived-left", observedA.ID, observedB.ID)
	derivedRight := factualLineageDerived(scope, "derived-right", observedA.ID, observedC.ID)
	derivedFinal := factualLineageDerived(scope, "derived-final", derivedLeft.ID, derivedRight.ID)
	snapshot.Facts = []fact.CanonicalFact{derivedFinal, observedC, derivedLeft, observedA, derivedRight, observedB}
	return snapshot
}

func factualLineageObserved(scope fact.Scope, subjectID string) fact.CanonicalFact {
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
		Value:     &fact.TypedValue{Kind: fact.ValueString, String: subjectID},
		Producer:  fact.Producer{ID: "java", Version: "1", Method: "symbols"},
		Evidence:  []fact.EvidenceRef{{ID: "evidence-" + subjectID, Locator: contract.Locator{Path: subjectID + ".java", StartLine: 1, EndLine: 1}}},
	}
	candidate.ID = mustFactID(candidate)
	return candidate
}

func factualLineageDerived(scope fact.Scope, subjectID string, inputs ...string) fact.CanonicalFact {
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDependency,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
		Object:    &fact.Participant{Kind: fact.ParticipantNamedElement, ID: "target-" + subjectID},
		Producer:  fact.Producer{ID: "rule-engine", Version: "1", Method: "dependency"},
		Evidence:  []fact.EvidenceRef{{ID: "evidence-" + subjectID, Locator: contract.Locator{Path: subjectID + ".java", StartLine: 1, EndLine: 1}}},
		Lineage:   &fact.Lineage{RuleID: "dependency", RuleVersion: "1", InputFactIDs: append([]string(nil), inputs...)},
	}
	candidate.ID = mustFactID(candidate)
	return candidate
}

func factualLineageFact(t *testing.T, snapshot FactualSnapshotInput, subjectID string) fact.CanonicalFact {
	t.Helper()
	for _, candidate := range snapshot.Facts {
		if candidate.Subject.ID == subjectID {
			return candidate
		}
	}
	t.Fatalf("factual lineage subject %q not found", subjectID)
	return fact.CanonicalFact{}
}

func countLineageFact(facts []fact.CanonicalFact, id string) int {
	count := 0
	for _, candidate := range facts {
		if candidate.ID == id {
			count++
		}
	}
	return count
}

func sortedLineageFacts(facts []fact.CanonicalFact) bool {
	for index := 1; index < len(facts); index++ {
		if facts[index-1].ID >= facts[index].ID {
			return false
		}
	}
	return true
}

func sortedLineageEdges(edges []FactualLineageEdge) bool {
	for index := 1; index < len(edges); index++ {
		if !factualLineageEdgeLess(edges[index-1], edges[index]) {
			return false
		}
	}
	return true
}
