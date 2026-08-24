package derivation_test

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/derivation"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestDependencyRuleRegistrationUsesStableInitialVersion(t *testing.T) {
	t.Parallel()

	registration := derivation.DependencyRuleRegistration()
	if registration.RuleID != derivation.DependencyRuleID {
		t.Fatalf("registration RuleID = %q, want %q", registration.RuleID, derivation.DependencyRuleID)
	}
	if registration.Version != derivation.DependencyRuleVersion {
		t.Fatalf("registration Version = %q, want %q", registration.Version, derivation.DependencyRuleVersion)
	}
	if err := registration.Validate(); err != nil {
		t.Fatalf("registration.Validate() error = %v", err)
	}
	if got := derivation.DependencyRuleRegistrationForVersion("2"); got.RuleID != registration.RuleID || got.Version == registration.Version {
		t.Fatalf("versioned registration = %#v, want same rule ID and distinct version from %#v", got.VersionInfo(), registration.VersionInfo())
	}
}

func TestDependencyTransitiveRuleReachesFiniteClosureWithImmediateLineage(t *testing.T) {
	t.Parallel()

	scope := testScope()
	ab := dependencyObservedFact(t, scope, "A", "B", "e-a", "e-shared")
	bc := dependencyObservedFact(t, scope, "B", "C", "e-shared", "e-b")
	cd := dependencyObservedFact(t, scope, "C", "D", "e-c", "e-d")
	inputs := []fact.CanonicalFact{cd, ab, bc}

	registry, err := derivation.NewRegistry(derivation.DependencyRuleRegistration())
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

	wantEdges := map[string]struct{}{
		"A->B": {},
		"B->C": {},
		"C->D": {},
		"A->C": {},
		"B->D": {},
		"A->D": {},
	}
	edges := dependencyEdges(got)
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Fatalf("dependency edges = %#v, want %#v", edges, wantEdges)
	}
	if len(got) != len(wantEdges) {
		t.Fatalf("fact count = %d, want %d", len(got), len(wantEdges))
	}

	observed := map[string]fact.CanonicalFact{ab.ID: ab, bc.ID: bc, cd.ID: cd}
	for _, candidate := range got {
		if _, isObserved := observed[candidate.ID]; isObserved {
			if candidate.Lineage != nil {
				t.Fatalf("observed fact %q gained lineage: %#v", candidate.ID, candidate.Lineage)
			}
			continue
		}
		if candidate.Producer.ID != derivation.DependencyRuleID ||
			candidate.Producer.Version != derivation.DependencyRuleVersion ||
			candidate.Producer.Method != derivation.DependencyRuleMethod {
			t.Fatalf("derived producer = %#v", candidate.Producer)
		}
		if candidate.Lineage == nil || candidate.Lineage.RuleID != derivation.DependencyRuleID || candidate.Lineage.RuleVersion != derivation.DependencyRuleVersion {
			t.Fatalf("derived lineage = %#v", candidate.Lineage)
		}
		if !sort.StringsAreSorted(candidate.Lineage.InputFactIDs) || len(candidate.Lineage.InputFactIDs) != 2 {
			t.Fatalf("derived lineage inputs = %#v, want two sorted IDs", candidate.Lineage.InputFactIDs)
		}
		if err := candidate.Validate(); err != nil {
			t.Fatalf("derived fact validation error = %v", err)
		}
		assertImmediateDependencyLineage(t, candidate, got)
	}

	ac := findDependencyFact(t, got, "A", "C")
	if gotEvidence := dependencyEvidenceIDs(ac); !reflect.DeepEqual(gotEvidence, []string{"e-a", "e-b", "e-shared"}) {
		t.Fatalf("A->C evidence = %#v, want sorted union", gotEvidence)
	}
	ad := findDependencyFact(t, got, "A", "D")
	if gotEvidence := dependencyEvidenceIDs(ad); !reflect.DeepEqual(gotEvidence, []string{"e-a", "e-b", "e-c", "e-d", "e-shared"}) {
		t.Fatalf("A->D evidence = %#v, want transitive union", gotEvidence)
	}

	permuted, err := executor.Derive(context.Background(), []fact.CanonicalFact{bc, cd, ab})
	if err != nil {
		t.Fatalf("Derive(permuted) error = %v", err)
	}
	if !reflect.DeepEqual(got, permuted) {
		t.Fatalf("permuted derivation differs:\ngot: %#v\npermuted: %#v", got, permuted)
	}
	repeated, err := executor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive(repeated) error = %v", err)
	}
	if !reflect.DeepEqual(got, repeated) {
		t.Fatalf("repeated derivation differs:\ngot: %#v\nrepeated: %#v", got, repeated)
	}
}

func TestDependencyTransitiveRuleIgnoresUncorrelatableFactsAndReflexiveCycles(t *testing.T) {
	t.Parallel()

	scope := testScope()
	ab := dependencyObservedFact(t, scope, "A", "B", "e-ab")
	ba := dependencyObservedFact(t, scope, "B", "A", "e-ba")
	noObject := dependencyObservedFact(t, scope, "B", "C", "e-no-object")
	noObject.Object = nil
	noObject.ID = mustFactID(t, noObject)
	wrongPredicate := dependencyObservedFact(t, scope, "B", "C", "e-wrong-predicate")
	wrongPredicate.Predicate = fact.PredicateReference
	wrongPredicate.ID = mustFactID(t, wrongPredicate)
	wrongKind := dependencyObservedFact(t, scope, "B", "C", "e-wrong-kind")
	wrongKind.Subject.Kind = fact.ParticipantArtifact
	wrongKind.ID = mustFactID(t, wrongKind)

	registry, err := derivation.NewRegistry(derivation.DependencyRuleRegistration())
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	inputs := []fact.CanonicalFact{wrongKind, noObject, ba, wrongPredicate, ab}
	got, err := executor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(got) != len(inputs) {
		t.Fatalf("fact count = %d, want observed-only count %d", len(got), len(inputs))
	}
	for _, candidate := range got {
		if candidate.Predicate == fact.PredicateDependency && candidate.Object != nil && candidate.Subject == *candidate.Object {
			t.Fatalf("reflexive dependency was derived: %#v", candidate)
		}
	}
}

func TestDependencyTransitiveRuleVersionsHaveDistinctDerivedIdentities(t *testing.T) {
	t.Parallel()

	scope := testScope()
	ab := dependencyObservedFact(t, scope, "A", "B", "e-ab-version")
	bc := dependencyObservedFact(t, scope, "B", "C", "e-bc-version")
	inputs := []fact.CanonicalFact{bc, ab}
	before := cloneFacts(inputs)

	firstRegistry, err := derivation.NewRegistry(derivation.DependencyRuleRegistrationForVersion("1"))
	if err != nil {
		t.Fatalf("NewRegistry(v1) error = %v", err)
	}
	firstExecutor, err := derivation.NewExecutor(firstRegistry)
	if err != nil {
		t.Fatalf("NewExecutor(v1) error = %v", err)
	}
	first, err := firstExecutor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive(v1) error = %v", err)
	}
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("v1 derivation mutated observed inputs")
	}

	secondRegistry, err := derivation.NewRegistry(derivation.DependencyRuleRegistrationForVersion("2"))
	if err != nil {
		t.Fatalf("NewRegistry(v2) error = %v", err)
	}
	secondExecutor, err := derivation.NewExecutor(secondRegistry)
	if err != nil {
		t.Fatalf("NewExecutor(v2) error = %v", err)
	}
	second, err := secondExecutor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive(v2) error = %v", err)
	}
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("v2 derivation mutated observed inputs")
	}

	if len(first) != 3 || len(second) != 3 {
		t.Fatalf("rebuild fact counts = %d/%d, want 3/3", len(first), len(second))
	}
	for _, observed := range inputs {
		for version, output := range map[string][]fact.CanonicalFact{"1": first, "2": second} {
			got := findFactByID(t, output, observed.ID)
			if !reflect.DeepEqual(got, observed) {
				t.Fatalf("observed fact %q changed in v%s rebuild:\ngot: %#v\nwant: %#v", observed.ID, version, got, observed)
			}
			if got.Lineage != nil || got.Producer != observed.Producer {
				t.Fatalf("observed fact %q was reclassified in v%s rebuild: %#v", observed.ID, version, got)
			}
		}
	}

	v1Derived := findDependencyFact(t, first, "A", "C")
	v2Derived := findDependencyFact(t, second, "A", "C")
	if v1Derived.ID == v2Derived.ID {
		t.Fatalf("versioned derived IDs are equal: %q", v1Derived.ID)
	}
	if v1Derived.Producer == v2Derived.Producer {
		t.Fatalf("versioned producers are equal: %#v", v1Derived.Producer)
	}
	if v1Derived.Lineage == nil || v1Derived.Lineage.RuleVersion != "1" {
		t.Fatalf("v1 lineage = %#v", v1Derived.Lineage)
	}
	if v2Derived.Lineage == nil || v2Derived.Lineage.RuleVersion != "2" {
		t.Fatalf("v2 lineage = %#v", v2Derived.Lineage)
	}
	if reflect.DeepEqual(v1Derived.Lineage, v2Derived.Lineage) {
		t.Fatalf("versioned lineages are equal: %#v", v1Derived.Lineage)
	}
	if reflect.DeepEqual(first, second) {
		t.Fatal("independent rebuilds with different rule versions are identical")
	}
}

func dependencyObservedFact(t *testing.T, scope fact.Scope, from, to string, evidenceIDs ...string) fact.CanonicalFact {
	t.Helper()
	evidence := make([]fact.EvidenceRef, 0, len(evidenceIDs))
	for index, id := range evidenceIDs {
		evidence = append(evidence, fact.EvidenceRef{
			ID: id,
			Locator: contract.Locator{
				SourceID:   scope.SourceID,
				ArtifactID: "artifact-" + id,
				Path:       "src/" + id + ".go",
				StartLine:  index + 1,
				EndLine:    index + 1,
			},
		})
	}
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDependency,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: from},
		Object:    &fact.Participant{Kind: fact.ParticipantSymbol, ID: to},
		Producer:  fact.Producer{ID: "observed-dependency", Version: "1", Method: "test"},
		Evidence:  evidence,
	}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func dependencyEdges(facts []fact.CanonicalFact) map[string]struct{} {
	edges := make(map[string]struct{})
	for _, candidate := range facts {
		if candidate.Predicate != fact.PredicateDependency || candidate.Object == nil {
			continue
		}
		edges[candidate.Subject.ID+"->"+candidate.Object.ID] = struct{}{}
	}
	return edges
}

func findDependencyFact(t *testing.T, facts []fact.CanonicalFact, subjectID, objectID string) fact.CanonicalFact {
	t.Helper()
	for _, candidate := range facts {
		if candidate.Predicate == fact.PredicateDependency && candidate.Subject.ID == subjectID && candidate.Object != nil && candidate.Object.ID == objectID {
			return candidate
		}
	}
	t.Fatalf("dependency fact %s->%s not found", subjectID, objectID)
	return fact.CanonicalFact{}
}

func dependencyEvidenceIDs(candidate fact.CanonicalFact) []string {
	ids := make([]string, 0, len(candidate.Evidence))
	for _, evidence := range candidate.Evidence {
		ids = append(ids, evidence.ID)
	}
	return ids
}

func assertImmediateDependencyLineage(t *testing.T, candidate fact.CanonicalFact, all []fact.CanonicalFact) {
	t.Helper()
	if candidate.Lineage == nil || len(candidate.Lineage.InputFactIDs) != 2 {
		t.Fatalf("candidate %q has invalid lineage %#v", candidate.ID, candidate.Lineage)
	}
	var left, right fact.CanonicalFact
	for _, inputID := range candidate.Lineage.InputFactIDs {
		input := findFactByID(t, all, inputID)
		if left.ID == "" {
			left = input
		} else {
			right = input
		}
	}
	if candidate.Predicate != fact.PredicateDependency || left.Predicate != fact.PredicateDependency || right.Predicate != fact.PredicateDependency || left.Object == nil || right.Object == nil || candidate.Object == nil {
		t.Fatalf("candidate %q lineage does not point to binary dependencies: %#v/%#v", candidate.ID, left, right)
	}
	forward := candidate.Subject == left.Subject && *left.Object == right.Subject && *candidate.Object == *right.Object
	reverse := candidate.Subject == right.Subject && *right.Object == left.Subject && *candidate.Object == *left.Object
	if !forward && !reverse {
		t.Fatalf("candidate %q lineage is not immediately correlated: %#v/%#v", candidate.ID, left.Lineage, right.Lineage)
	}
}

func findFactByID(t *testing.T, facts []fact.CanonicalFact, id string) fact.CanonicalFact {
	t.Helper()
	for _, candidate := range facts {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("fact %q not found", id)
	return fact.CanonicalFact{}
}
