package derivation_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/derivation"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestMembershipRuleDerivesValidatedFactWithSupportAndLineage(t *testing.T) {
	t.Parallel()

	scope := membershipTestScope()
	definition := membershipDefinition(t, scope, "symbol-booking", fact.ParticipantArtifact, "artifact-booking", "evidence-z", "evidence-a")
	before := cloneMembershipFacts([]fact.CanonicalFact{definition})

	got := executeMembership(t, derivation.MembershipRuleRegistration(), []fact.CanonicalFact{definition})
	if len(got) != 2 {
		t.Fatalf("derived output count = %d, want observed definition plus membership", len(got))
	}
	derived := membershipDerivedFact(t, got)
	if err := derived.Validate(); err != nil {
		t.Fatalf("derived membership validation error = %v", err)
	}
	if derived.Scope != scope {
		t.Fatalf("derived scope = %#v, want %#v", derived.Scope, scope)
	}
	if derived.Subject != definition.Subject || derived.Object == nil || *derived.Object != *definition.Object {
		t.Fatalf("derived relation = %#v, want %s -> %s", derived, definition.Subject.ID, definition.Object.ID)
	}
	if derived.Value != nil || len(derived.Qualifiers) != 0 {
		t.Fatalf("derived relation carried unsupported fields: %#v", derived)
	}
	wantProducer := fact.Producer{
		ID:      derivation.MembershipRuleID,
		Version: derivation.MembershipRuleVersion,
		Method:  derivation.MembershipRuleMethod,
	}
	if derived.Producer != wantProducer {
		t.Fatalf("derived producer = %#v, want %#v", derived.Producer, wantProducer)
	}
	if derived.Lineage == nil || derived.Lineage.RuleID != derivation.MembershipRuleID || derived.Lineage.RuleVersion != derivation.MembershipRuleVersion || !reflect.DeepEqual(derived.Lineage.InputFactIDs, []string{definition.ID}) {
		t.Fatalf("derived lineage = %#v, want one definition input and version", derived.Lineage)
	}
	if gotIDs := membershipEvidenceIDs(derived.Evidence); !reflect.DeepEqual(gotIDs, []string{"evidence-a", "evidence-z"}) {
		t.Fatalf("derived evidence IDs = %#v, want sorted sustaining evidence", gotIDs)
	}
	for _, reference := range derived.Evidence {
		if reference.Locator.SourceID != scope.SourceID || reference.Locator.ArtifactID != definition.Object.ID {
			t.Fatalf("derived evidence locator = %#v, want definition support", reference.Locator)
		}
	}
	if gotObserved := observedByID(got, definition.ID); gotObserved.Lineage != nil || gotObserved.Predicate != fact.PredicateDefinition {
		t.Fatalf("observed definition was reclassified: %#v", gotObserved)
	}
	if !reflect.DeepEqual([]fact.CanonicalFact{definition}, before) {
		t.Fatal("membership derivation mutated observed input")
	}
}

func TestMembershipRuleAbstainsFromUnsupportedAndAmbiguousRelations(t *testing.T) {
	t.Parallel()

	scope := membershipTestScope()
	tests := []struct {
		name  string
		input []fact.CanonicalFact
	}{
		{
			name: "definition value",
			input: []fact.CanonicalFact{
				membershipDefinitionValue(t, scope, "symbol-value", "evidence-value"),
			},
		},
		{
			name: "non artifact object",
			input: []fact.CanonicalFact{
				membershipDefinition(t, scope, "symbol-object", fact.ParticipantSymbol, "symbol-target", "evidence-object"),
			},
		},
		{
			name: "named element object",
			input: []fact.CanonicalFact{
				membershipDefinition(t, scope, "symbol-element", fact.ParticipantNamedElement, "element-target", "evidence-element"),
			},
		},
		{
			name: "non symbol subject",
			input: []fact.CanonicalFact{
				membershipDefinitionWithSubject(t, scope, fact.ParticipantNamedElement, "element-subject", "artifact-target", "evidence-subject"),
			},
		},
		{
			name: "ambiguous artifacts",
			input: []fact.CanonicalFact{
				membershipDefinition(t, scope, "symbol-ambiguous", fact.ParticipantArtifact, "artifact-a", "evidence-a"),
				membershipDefinition(t, scope, "symbol-ambiguous", fact.ParticipantArtifact, "artifact-b", "evidence-b"),
			},
		},
		{
			name: "membership is not transitive",
			input: []fact.CanonicalFact{
				membershipFactObserved(t, scope, "symbol-existing", "artifact-existing", "evidence-existing"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := cloneMembershipFacts(tt.input)
			got := executeMembership(t, derivation.MembershipRuleRegistration(), tt.input)
			if countMembershipDerivations(got) != 0 {
				t.Fatalf("derived membership facts = %#v, want none", got)
			}
			if !reflect.DeepEqual(tt.input, before) {
				t.Fatal("abstaining derivation mutated input")
			}
		})
	}
}

func TestMembershipRuleIsDeterministicAndDoesNotMutateEquivalentInputs(t *testing.T) {
	t.Parallel()

	scope := membershipTestScope()
	first := membershipDefinitionWithProducer(t, scope, "symbol-same", "artifact-same", "evidence-first", "frontend-a")
	second := membershipDefinitionWithProducer(t, scope, "symbol-same", "artifact-same", "evidence-second", "frontend-b")
	forwardInputs := []fact.CanonicalFact{first, second}
	reverseInputs := []fact.CanonicalFact{second, first}
	forwardBefore := cloneMembershipFacts(forwardInputs)
	reverseBefore := cloneMembershipFacts(reverseInputs)

	forward := executeMembership(t, derivation.MembershipRuleRegistration(), forwardInputs)
	reverse := executeMembership(t, derivation.MembershipRuleRegistration(), reverseInputs)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("permuted membership derivation differs:\nforward: %#v\nreverse: %#v", forward, reverse)
	}
	if !reflect.DeepEqual(forwardInputs, forwardBefore) || !reflect.DeepEqual(reverseInputs, reverseBefore) {
		t.Fatal("membership derivation mutated caller facts")
	}
	for index := 1; index < len(forward); index++ {
		if forward[index-1].ID >= forward[index].ID {
			t.Fatalf("derivation output is not identity ordered: %#v", forward)
		}
	}
	derived := membershipDerivedFact(t, forward)
	selectedID := first.ID
	if second.ID < selectedID {
		selectedID = second.ID
	}
	if !reflect.DeepEqual(derived.Lineage.InputFactIDs, []string{selectedID}) {
		t.Fatalf("selected lineage = %#v, want deterministic lowest input %q", derived.Lineage, selectedID)
	}
}

func TestMembershipRuleVersionProducesDistinctDerivationWithoutReclassifyingObservedFact(t *testing.T) {
	t.Parallel()

	scope := membershipTestScope()
	definition := membershipDefinition(t, scope, "symbol-versioned", fact.ParticipantArtifact, "artifact-versioned", "evidence-versioned")
	v1 := executeMembership(t, derivation.MembershipRuleRegistration(), []fact.CanonicalFact{definition})
	v2 := executeMembership(t, derivation.MembershipRuleRegistrationForVersion("2"), []fact.CanonicalFact{definition})
	v1Derived := membershipDerivedFact(t, v1)
	v2Derived := membershipDerivedFact(t, v2)
	if v1Derived.ID == v2Derived.ID {
		t.Fatalf("rule version did not distinguish derived identity: v1=%#v v2=%#v", v1Derived, v2Derived)
	}
	if v1Derived.Lineage == nil || v1Derived.Lineage.RuleVersion != derivation.MembershipRuleVersion {
		t.Fatalf("v1 lineage = %#v", v1Derived.Lineage)
	}
	if v2Derived.Lineage == nil || v2Derived.Lineage.RuleVersion != "2" {
		t.Fatalf("v2 lineage = %#v", v2Derived.Lineage)
	}
	if got := observedByID(v1, definition.ID); got.ID != definition.ID || got.Lineage != nil || got.Producer != definition.Producer {
		t.Fatalf("v1 reclassified observed fact: %#v", got)
	}
	if got := observedByID(v2, definition.ID); got.ID != definition.ID || got.Lineage != nil || got.Producer != definition.Producer {
		t.Fatalf("v2 reclassified observed fact: %#v", got)
	}
}

func TestMembershipRuleRegistrationExposesInitialIdentity(t *testing.T) {
	t.Parallel()

	registration := derivation.MembershipRuleRegistration()
	if registration.RuleID != derivation.MembershipRuleID || registration.Version != derivation.MembershipRuleVersion || registration.Rule == nil {
		t.Fatalf("membership registration = %#v, want explicit initial identity", registration)
	}
	if got := registration.VersionInfo(); got != (derivation.RuleVersion{RuleID: derivation.MembershipRuleID, Version: derivation.MembershipRuleVersion}) {
		t.Fatalf("registration version = %#v", got)
	}
}

func executeMembership(t *testing.T, registration derivation.Registration, inputs []fact.CanonicalFact) []fact.CanonicalFact {
	t.Helper()
	registry, err := derivation.NewRegistry(registration)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	got, err := executor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	return got
}

func membershipDerivedFact(t *testing.T, facts []fact.CanonicalFact) fact.CanonicalFact {
	t.Helper()
	var found []fact.CanonicalFact
	for _, candidate := range facts {
		if candidate.Predicate == fact.PredicateMembership && candidate.Lineage != nil && candidate.Lineage.RuleID == derivation.MembershipRuleID {
			found = append(found, candidate)
		}
	}
	if len(found) != 1 {
		t.Fatalf("membership derived facts = %#v, want exactly one", found)
	}
	return found[0]
}

func countMembershipDerivations(facts []fact.CanonicalFact) int {
	count := 0
	for _, candidate := range facts {
		if candidate.Predicate == fact.PredicateMembership && candidate.Lineage != nil && candidate.Lineage.RuleID == derivation.MembershipRuleID {
			count++
		}
	}
	return count
}

func observedByID(facts []fact.CanonicalFact, id string) fact.CanonicalFact {
	for _, candidate := range facts {
		if candidate.ID == id {
			return candidate
		}
	}
	return fact.CanonicalFact{}
}

func membershipDefinition(t *testing.T, scope fact.Scope, subjectID string, objectKind fact.ParticipantKind, objectID string, evidenceIDs ...string) fact.CanonicalFact {
	return membershipDefinitionWithSubjectAndEvidence(t, scope, fact.ParticipantSymbol, subjectID, objectKind, objectID, evidenceIDs, "frontend-definition")
}

func membershipDefinitionWithProducer(t *testing.T, scope fact.Scope, subjectID, objectID, evidenceID, producerID string) fact.CanonicalFact {
	return membershipDefinitionWithSubjectAndEvidence(t, scope, fact.ParticipantSymbol, subjectID, fact.ParticipantArtifact, objectID, []string{evidenceID}, producerID)
}

func membershipDefinitionWithSubject(t *testing.T, scope fact.Scope, subjectKind fact.ParticipantKind, subjectID, objectID, evidenceID string) fact.CanonicalFact {
	return membershipDefinitionWithSubjectAndEvidence(t, scope, subjectKind, subjectID, fact.ParticipantArtifact, objectID, []string{evidenceID}, "frontend-definition")
}

func membershipDefinitionWithSubjectAndEvidence(t *testing.T, scope fact.Scope, subjectKind fact.ParticipantKind, subjectID string, objectKind fact.ParticipantKind, objectID string, evidenceIDs []string, producerID string) fact.CanonicalFact {
	t.Helper()
	evidence := make([]fact.EvidenceRef, 0, len(evidenceIDs))
	for _, evidenceID := range evidenceIDs {
		evidence = append(evidence, fact.EvidenceRef{
			ID: evidenceID,
			Locator: contract.Locator{
				SourceID:   scope.SourceID,
				ArtifactID: objectID,
				Path:       "src/" + objectID + ".go",
				StartLine:  1,
				EndLine:    2,
			},
		})
	}
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: subjectKind, ID: subjectID},
		Object:    &fact.Participant{Kind: objectKind, ID: objectID},
		Producer:  fact.Producer{ID: producerID, Version: "1", Method: "symbols"},
		Evidence:  evidence,
	}
	candidate.ID = mustMembershipFactID(t, candidate)
	return candidate
}

func membershipDefinitionValue(t *testing.T, scope fact.Scope, subjectID, evidenceID string) fact.CanonicalFact {
	t.Helper()
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDefinition,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
		Value:     &fact.TypedValue{Kind: fact.ValueString, String: "artifact-value"},
		Producer:  fact.Producer{ID: "frontend-definition", Version: "1", Method: "symbols"},
		Evidence: []fact.EvidenceRef{{
			ID: evidenceID,
			Locator: contract.Locator{
				SourceID:   scope.SourceID,
				ArtifactID: "artifact-value",
				Path:       "src/artifact-value.go",
				StartLine:  1,
				EndLine:    2,
			},
		}},
	}
	candidate.ID = mustMembershipFactID(t, candidate)
	return candidate
}

func membershipFactObserved(t *testing.T, scope fact.Scope, subjectID, objectID, evidenceID string) fact.CanonicalFact {
	t.Helper()
	candidate := membershipDefinitionWithSubjectAndEvidence(t, scope, fact.ParticipantSymbol, subjectID, fact.ParticipantArtifact, objectID, []string{evidenceID}, "frontend-membership")
	candidate.Predicate = fact.PredicateMembership
	candidate.ID = mustMembershipFactID(t, candidate)
	return candidate
}

func membershipTestScope() fact.Scope {
	return fact.Scope{OrganizationID: "organization-membership", SourceID: "source-membership", SnapshotID: "snapshot-membership"}
}

func mustMembershipFactID(t *testing.T, candidate fact.CanonicalFact) string {
	t.Helper()
	id, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("FactID() error = %v", err)
	}
	return id
}

func membershipEvidenceIDs(evidence []fact.EvidenceRef) []string {
	result := make([]string, 0, len(evidence))
	for _, reference := range evidence {
		result = append(result, reference.ID)
	}
	return result
}

func cloneMembershipFacts(input []fact.CanonicalFact) []fact.CanonicalFact {
	result := make([]fact.CanonicalFact, len(input))
	for index, candidate := range input {
		result[index] = candidate
		if candidate.Object != nil {
			object := *candidate.Object
			result[index].Object = &object
		}
		if candidate.Value != nil {
			value := *candidate.Value
			result[index].Value = &value
		}
		result[index].Qualifiers = append([]fact.Qualifier(nil), candidate.Qualifiers...)
		result[index].Evidence = append([]fact.EvidenceRef(nil), candidate.Evidence...)
		if candidate.Lineage != nil {
			lineage := *candidate.Lineage
			lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
			result[index].Lineage = &lineage
		}
	}
	return result
}
