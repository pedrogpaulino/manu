package fact_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestDetectConflictsPreservesIncompatibleClaimsAndSupport(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-java", fact.PredicateNamedElement, "element-main", "class-java", "evidence-java")
	second := conflictFact(t, "frontend-python", fact.PredicateNamedElement, "element-main", "class-python", "evidence-python")

	got, err := fact.DetectConflicts([]fact.CanonicalFact{second, first})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("DetectConflicts() returned %d conflicts, want 1", len(got))
	}
	conflict := got[0]
	if conflict.ID == "" || conflict.Predicate != fact.PredicateNamedElement || conflict.Subject.ID != "element-main" {
		t.Fatalf("conflict identity/slot = %#v", conflict)
	}
	if len(conflict.Assertions) != 2 || len(conflict.Facts) != 2 {
		t.Fatalf("conflict assertions/facts = %d/%d, want 2/2", len(conflict.Assertions), len(conflict.Facts))
	}
	if conflict.Facts[0].ID > conflict.Facts[1].ID {
		t.Fatalf("facts are not ordered by identity: %q, %q", conflict.Facts[0].ID, conflict.Facts[1].ID)
	}
	producers := map[string]bool{}
	evidence := map[string]bool{}
	for _, candidate := range conflict.Facts {
		producers[candidate.Producer.ID] = true
		for _, support := range candidate.Evidence {
			evidence[support.ID] = true
		}
	}
	if len(producers) != 2 || len(evidence) != 2 {
		t.Fatalf("preserved producer/evidence sets = %#v/%#v", producers, evidence)
	}
	if err := conflict.Validate(); err != nil {
		t.Fatalf("Conflict.Validate() error = %v", err)
	}
}

func TestDetectConflictsIncludesSupportersOfEachAlternative(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-java", fact.PredicateDefinition, "symbol-main", "element-java", "evidence-java")
	second := conflictFact(t, "frontend-python", fact.PredicateDefinition, "symbol-main", "element-python", "evidence-python")
	support := conflictFact(t, "frontend-kotlin", fact.PredicateDefinition, "symbol-main", "element-java", "evidence-kotlin")

	got, err := fact.DetectConflicts([]fact.CanonicalFact{first, second, support})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 1 || len(got[0].Assertions) != 2 || len(got[0].Facts) != 3 {
		t.Fatalf("conflict shape = conflicts:%d assertions:%d facts:%d, want 1/2/3", len(got), len(got[0].Assertions), len(got[0].Facts))
	}
	counts := make([]int, 0, len(got[0].Assertions))
	for _, assertion := range got[0].Assertions {
		counts = append(counts, len(assertion.Facts))
	}
	sort.Ints(counts)
	if !reflect.DeepEqual(counts, []int{1, 2}) {
		t.Fatalf("assertion supporter counts = %v, want [1 2]", counts)
	}
}

func TestDetectConflictsIgnoresMetadataOnlyDifferences(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-java", fact.PredicateArtifact, "artifact-main", "python", "evidence-java")
	second := conflictFact(t, "frontend-python", fact.PredicateArtifact, "artifact-main", "python", "evidence-python")
	second.Qualifiers = []fact.Qualifier{{Name: fact.QualifierMethod, Value: fact.TypedValue{Kind: fact.ValueString, String: "different-observation"}}}
	second.ID = mustFactID(t, second)

	got, err := fact.DetectConflicts([]fact.CanonicalFact{first, second})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("metadata-only differences produced %d conflicts, want none", len(got))
	}
}

func TestDetectConflictsSkipsMultivaluedPredicates(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-java", fact.PredicateReference, "symbol-main", "symbol-target-a", "evidence-java")
	second := conflictFact(t, "frontend-python", fact.PredicateReference, "symbol-main", "symbol-target-b", "evidence-python")
	got, err := fact.DetectConflicts([]fact.CanonicalFact{first, second})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("multivalued predicate produced %d conflicts, want none", len(got))
	}
}

func TestDetectConflictsRequiresSameScopeAndSubject(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-java", fact.PredicateSymbol, "symbol-a", "class-java", "evidence-a")
	second := conflictFact(t, "frontend-python", fact.PredicateSymbol, "symbol-b", "class-python", "evidence-b")
	second.Scope.SnapshotID = "snapshot-other"
	second.ID = mustFactID(t, second)
	third := conflictFact(t, "frontend-python", fact.PredicateSymbol, "symbol-a", "class-python", "evidence-c")
	third.Scope.SourceID = "source-other"
	third.Evidence[0].Locator.SourceID = "source-other"
	third.ID = mustFactID(t, third)

	got, err := fact.DetectConflicts([]fact.CanonicalFact{first, second, third})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("different scope/subject produced %d conflicts, want none", len(got))
	}
}

func TestDetectConflictsDistinguishesCompleteProducerIdentity(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-single", fact.PredicateDefinition, "symbol-main", "element-a", "evidence-a")
	second := conflictFact(t, "frontend-single", fact.PredicateDefinition, "symbol-main", "element-b", "evidence-b")
	second.Producer.Method = "second-method"
	second.ID = mustFactID(t, second)
	got, err := fact.DetectConflicts([]fact.CanonicalFact{first, second})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("two complete producer identities produced %d conflicts, want 1", len(got))
	}
	if err := got[0].Validate(); err != nil {
		t.Fatalf("Conflict.Validate() error = %v", err)
	}
}

func TestDetectConflictsSameCompleteProducerDoesNotConflict(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-single", fact.PredicateDefinition, "symbol-main", "element-a", "evidence-a")
	second := conflictFact(t, "frontend-single", fact.PredicateDefinition, "symbol-main", "element-b", "evidence-b")
	got, err := fact.DetectConflicts([]fact.CanonicalFact{first, second})
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("one complete producer identity produced %d conflicts, want none", len(got))
	}
}

func TestDetectConflictsIsDeterministicAcrossInputOrder(t *testing.T) {
	t.Parallel()

	facts := []fact.CanonicalFact{
		conflictFact(t, "frontend-z", fact.PredicateDefinition, "symbol-z", "element-z", "evidence-z"),
		conflictFact(t, "frontend-a", fact.PredicateDefinition, "symbol-a", "element-a", "evidence-a"),
		conflictFact(t, "frontend-y", fact.PredicateDefinition, "symbol-z", "element-y", "evidence-y"),
		conflictFact(t, "frontend-b", fact.PredicateDefinition, "symbol-a", "element-b", "evidence-b"),
	}
	left, err := fact.DetectConflicts(facts)
	if err != nil {
		t.Fatalf("DetectConflicts(left) error = %v", err)
	}
	rightInput := []fact.CanonicalFact{facts[3], facts[1], facts[0], facts[2]}
	right, err := fact.DetectConflicts(rightInput)
	if err != nil {
		t.Fatalf("DetectConflicts(right) error = %v", err)
	}
	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatalf("json.Marshal(left) error = %v", err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatalf("json.Marshal(right) error = %v", err)
	}
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("reordered facts changed conflict result:\nleft:  %s\nright: %s", leftJSON, rightJSON)
	}
}

func TestDetectConflictsDeepCopiesFactsAndRejectsInvalidInputWithoutPartialOutput(t *testing.T) {
	t.Parallel()

	first := conflictFact(t, "frontend-java", fact.PredicateDefinition, "symbol-main", "element-java", "evidence-java")
	second := conflictFact(t, "frontend-python", fact.PredicateDefinition, "symbol-main", "element-python", "evidence-python")
	first.Qualifiers = []fact.Qualifier{{Name: fact.QualifierOrigin, Value: fact.TypedValue{Kind: fact.ValueString, String: "first"}}}
	first.Lineage = &fact.Lineage{RuleID: "rule", RuleVersion: "1", InputFactIDs: []string{"input-b", "input-a"}}
	first.ID = mustFactID(t, first)
	input := []fact.CanonicalFact{first, second}
	original := append([]fact.CanonicalFact(nil), input...)
	original[0].Qualifiers = append([]fact.Qualifier(nil), input[0].Qualifiers...)
	original[0].Evidence = append([]fact.EvidenceRef(nil), input[0].Evidence...)
	original[0].Lineage = &fact.Lineage{RuleID: input[0].Lineage.RuleID, RuleVersion: input[0].Lineage.RuleVersion, InputFactIDs: append([]string(nil), input[0].Lineage.InputFactIDs...)}

	got, err := fact.DetectConflicts(input)
	if err != nil {
		t.Fatalf("DetectConflicts() error = %v", err)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("DetectConflicts mutated input: got %#v, want %#v", input, original)
	}
	for index := range got[0].Facts {
		if got[0].Facts[index].Lineage == nil {
			continue
		}
		got[0].Facts[index].Evidence[0].ID = "mutated"
		got[0].Facts[index].Qualifiers[0].Value.String = "mutated"
		got[0].Facts[index].Lineage.InputFactIDs[0] = "mutated"
		break
	}
	for _, assertion := range got[0].Assertions {
		for _, candidate := range assertion.Facts {
			qualifierMutated := len(candidate.Qualifiers) > 0 && candidate.Qualifiers[0].Value.String == "mutated"
			lineageMutated := candidate.Lineage != nil && len(candidate.Lineage.InputFactIDs) > 0 && candidate.Lineage.InputFactIDs[0] == "mutated"
			if candidate.Evidence[0].ID == "mutated" || qualifierMutated || lineageMutated {
				t.Fatalf("conflict fact copies share mutable support metadata")
			}
		}
	}

	invalid := append([]fact.CanonicalFact(nil), input...)
	invalid[1].Producer.ID = ""
	if result, invalidErr := fact.DetectConflicts(invalid); result != nil || !errors.Is(invalidErr, fact.ErrInvalidConflict) {
		t.Fatalf("invalid input result/error = %#v/%v, want nil and ErrInvalidConflict", result, invalidErr)
	}
	duplicate := []fact.CanonicalFact{input[0], input[0]}
	if result, duplicateErr := fact.DetectConflicts(duplicate); result != nil || !errors.Is(duplicateErr, fact.ErrDuplicateFactID) {
		t.Fatalf("duplicate input result/error = %#v/%v, want nil and ErrDuplicateFactID", result, duplicateErr)
	}
}

func conflictFact(t *testing.T, producerID string, predicate fact.Predicate, subjectID, claim, evidenceID string) fact.CanonicalFact {
	t.Helper()
	candidate := fact.CanonicalFact{
		Version: fact.Version,
		Scope: fact.Scope{
			OrganizationID: "organization-local",
			SourceID:       "source-app",
			SnapshotID:     "snapshot-1",
		},
		Predicate: predicate,
		Subject: fact.Participant{
			Kind: fact.ParticipantSymbol,
			ID:   subjectID,
		},
		Producer: fact.Producer{
			ID:      producerID,
			Version: "1",
			Method:  "structural",
		},
		Evidence: []fact.EvidenceRef{{
			ID: evidenceID,
			Locator: contract.Locator{
				SourceID:  "source-app",
				Path:      "src/module.py",
				StartLine: 10,
				EndLine:   10,
			},
		}},
	}
	if predicate == fact.PredicateReference {
		candidate.Object = &fact.Participant{Kind: fact.ParticipantNamedElement, ID: claim}
	} else {
		candidate.Value = &fact.TypedValue{Kind: fact.ValueString, String: claim}
	}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func mustFactID(t *testing.T, candidate fact.CanonicalFact) string {
	t.Helper()
	id, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("FactID() error = %v", err)
	}
	return id
}
