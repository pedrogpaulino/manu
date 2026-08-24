package derivation_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/derivation"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestRegistryValidatesAndOrdersRuleVersions(t *testing.T) {
	t.Parallel()

	rule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
		return nil, nil
	})
	registrations := []derivation.Registration{
		{RuleID: "rule-z", Version: "2", Rule: rule},
		{RuleID: "rule-a", Version: "2", Rule: rule},
		{RuleID: "rule-a", Version: "1", Rule: rule},
	}
	registry, err := derivation.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	want := []derivation.RuleVersion{
		{RuleID: "rule-a", Version: "1"},
		{RuleID: "rule-a", Version: "2"},
		{RuleID: "rule-z", Version: "2"},
	}
	if got := registry.RuleVersions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RuleVersions() = %#v, want %#v", got, want)
	}

	duplicate := derivation.Registration{RuleID: "rule-a", Version: "1", Rule: rule}
	if err := registry.Register(duplicate); !errors.Is(err, derivation.ErrDuplicateRule) {
		t.Fatalf("duplicate Register() error = %v, want ErrDuplicateRule", err)
	}
	if err := registry.Register(derivation.Registration{RuleID: "rule-a", Version: "3", Rule: rule}); err != nil {
		t.Fatalf("distinct version Register() error = %v", err)
	}

	var nilRule *testRule
	tests := []struct {
		name string
		item derivation.Registration
	}{
		{
			name: "empty id",
			item: derivation.Registration{Version: "1", Rule: rule},
		},
		{
			name: "whitespace id",
			item: derivation.Registration{RuleID: "rule bad", Version: "1", Rule: rule},
		},
		{
			name: "empty version",
			item: derivation.Registration{RuleID: "rule", Rule: rule},
		},
		{
			name: "nil rule",
			item: derivation.Registration{RuleID: "rule", Version: "1", Rule: nilRule},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := derivation.NewRegistry(tt.item); !errors.Is(err, derivation.ErrInvalidRule) {
				t.Fatalf("NewRegistry() error = %v, want ErrInvalidRule", err)
			}
		})
	}
}

func TestExecutorReachesFixedPointDeterministically(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seedA := observedFact(t, scope, "seed-a")
	seedB := observedFact(t, scope, "seed-b")
	middle := derivedFact(t, seedA, "rule-middle", "1", "middle", seedA)
	last := derivedFact(t, middle, "rule-last", "1", "last", middle, seedB)

	middleRule := derivation.RuleFunc(func(_ context.Context, view derivation.FactView) ([]fact.CanonicalFact, error) {
		if _, ok := view.Get(seedA.ID); !ok {
			return nil, nil
		}
		return []fact.CanonicalFact{middle}, nil
	})
	lastRule := derivation.RuleFunc(func(_ context.Context, view derivation.FactView) ([]fact.CanonicalFact, error) {
		if _, ok := view.Get(middle.ID); !ok {
			return nil, nil
		}
		if _, ok := view.Get(seedB.ID); !ok {
			return nil, nil
		}
		return []fact.CanonicalFact{last, last}, nil
	})

	first, err := derivation.NewRegistry(
		derivation.Registration{RuleID: "rule-last", Version: "1", Rule: lastRule},
		derivation.Registration{RuleID: "rule-middle", Version: "1", Rule: middleRule},
	)
	if err != nil {
		t.Fatalf("NewRegistry(first) error = %v", err)
	}
	second, err := derivation.NewRegistry(
		derivation.Registration{RuleID: "rule-middle", Version: "1", Rule: middleRule},
		derivation.Registration{RuleID: "rule-last", Version: "1", Rule: lastRule},
	)
	if err != nil {
		t.Fatalf("NewRegistry(second) error = %v", err)
	}
	firstExecutor, err := derivation.NewExecutor(first)
	if err != nil {
		t.Fatalf("NewExecutor(first) error = %v", err)
	}
	secondExecutor, err := derivation.NewExecutor(second)
	if err != nil {
		t.Fatalf("NewExecutor(second) error = %v", err)
	}

	inputs := []fact.CanonicalFact{seedB, seedA}
	wantInputs := cloneFacts(inputs)
	gotFirst, err := firstExecutor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive(first) error = %v", err)
	}
	gotSecond, err := secondExecutor.Derive(context.Background(), []fact.CanonicalFact{seedA, seedB})
	if err != nil {
		t.Fatalf("Derive(second) error = %v", err)
	}
	if !reflect.DeepEqual(gotFirst, gotSecond) {
		t.Fatalf("permuted derivation differs:\nfirst: %#v\nsecond: %#v", gotFirst, gotSecond)
	}
	repeated, err := firstExecutor.Derive(context.Background(), inputs)
	if err != nil {
		t.Fatalf("Derive(repeated) error = %v", err)
	}
	if !reflect.DeepEqual(gotFirst, repeated) {
		t.Fatalf("repeated derivation differs:\nfirst: %#v\nrepeated: %#v", gotFirst, repeated)
	}
	if len(gotFirst) != 4 {
		t.Fatalf("fixed-point fact count = %d, want 4", len(gotFirst))
	}
	if !reflect.DeepEqual(inputs, wantInputs) {
		t.Fatalf("Derive() mutated inputs: got %#v, want %#v", inputs, wantInputs)
	}
	if got := countFactID(gotFirst, middle.ID); got != 1 {
		t.Fatalf("middle fact occurrences = %d, want 1", got)
	}
	if got := countFactID(gotFirst, last.ID); got != 1 {
		t.Fatalf("last fact occurrences = %d, want 1", got)
	}
	for index := 1; index < len(gotFirst); index++ {
		if gotFirst[index-1].ID >= gotFirst[index].ID {
			t.Fatalf("output is not sorted by identity: %#v", gotFirst)
		}
	}
	for _, candidate := range gotFirst {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("output fact validation error = %v", err)
		}
	}
	for index := range gotFirst {
		firstDigest, err := fact.CanonicalDigest(gotFirst[index])
		if err != nil {
			t.Fatalf("CanonicalDigest(first) error = %v", err)
		}
		secondDigest, err := fact.CanonicalDigest(gotSecond[index])
		if err != nil {
			t.Fatalf("CanonicalDigest(second) error = %v", err)
		}
		if firstDigest != secondDigest {
			t.Fatalf("permuted digest %d = %q, want %q", index, secondDigest, firstDigest)
		}
	}
}

func TestFactViewIsOrderedAndDefensivelyCopied(t *testing.T) {
	t.Parallel()

	scope := testScope()
	first := observedFact(t, scope, "first")
	second := observedFact(t, scope, "second")
	before := cloneFacts([]fact.CanonicalFact{second, first})
	called := false
	rule := derivation.RuleFunc(func(_ context.Context, view derivation.FactView) ([]fact.CanonicalFact, error) {
		called = true
		if view.Len() != 2 {
			return nil, errors.New("unexpected view length")
		}
		ids := view.IDs()
		if ids[0] >= ids[1] {
			return nil, errors.New("view is not ordered")
		}
		facts := view.Facts()
		facts[0].Evidence[0].ID = "mutated-view-evidence"
		facts[0].Lineage = &fact.Lineage{RuleID: "mutated", RuleVersion: "1", InputFactIDs: []string{facts[1].ID}}
		one, ok := view.At(0)
		if !ok {
			return nil, errors.New("missing first fact")
		}
		one.Evidence[0].ID = "mutated-at-evidence"
		lookup, ok := view.Get(ids[0])
		if !ok || lookup.Evidence[0].ID == "mutated-view-evidence" || lookup.Evidence[0].ID == "mutated-at-evidence" {
			return nil, errors.New("view accessor leaked mutable state")
		}
		return nil, nil
	})
	registry, err := derivation.NewRegistry(derivation.Registration{RuleID: "read-only", Version: "1", Rule: rule})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}
	got, err := executor.Derive(context.Background(), []fact.CanonicalFact{second, first})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if !called {
		t.Fatal("rule was not called")
	}
	if !reflect.DeepEqual([]fact.CanonicalFact{second, first}, before) {
		t.Fatal("executor or rule mutated caller facts")
	}
	if len(got) != 2 {
		t.Fatalf("output facts = %d, want 2", len(got))
	}
}

func TestExecutorRejectsInvalidInputsBeforeRuleExecution(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed")
	called := false
	rule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
		called = true
		return nil, nil
	})
	registry, err := derivation.NewRegistry(derivation.Registration{RuleID: "validation", Version: "1", Rule: rule})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor() error = %v", err)
	}

	invalidID := seed
	invalidID.ID = "secret-invalid-fact"
	otherScope := cloneFacts([]fact.CanonicalFact{seed})[0]
	otherScope.Scope.SourceID = "source-other"
	otherScope.Evidence[0].Locator.SourceID = otherScope.Scope.SourceID
	otherScope.ID = mustFactID(t, otherScope)
	tests := []struct {
		name   string
		inputs []fact.CanonicalFact
		want   error
	}{
		{name: "invalid fact identity", inputs: []fact.CanonicalFact{invalidID}, want: derivation.ErrInvalidInput},
		{name: "duplicate identity", inputs: []fact.CanonicalFact{seed, seed}, want: derivation.ErrDuplicateFact},
		{name: "multiple scopes", inputs: []fact.CanonicalFact{seed, otherScope}, want: derivation.ErrInvalidInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := executor.Derive(context.Background(), tt.inputs); !errors.Is(err, tt.want) {
				t.Fatalf("Derive() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
		})
	}
	if called {
		t.Fatal("rule executed for invalid input")
	}
}

func TestExecutorRejectsInvalidCandidatesWithoutPublishingPartialResults(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed")
	tests := []struct {
		name string
		make func() fact.CanonicalFact
	}{
		{
			name: "missing lineage",
			make: func() fact.CanonicalFact { return observedFact(t, scope, "candidate-observed") },
		},
		{
			name: "wrong rule identity",
			make: func() fact.CanonicalFact {
				return derivedFact(t, seed, "different-rule", "1", "wrong-rule", seed)
			},
		},
		{
			name: "unknown input",
			make: func() fact.CanonicalFact {
				unknown := observedFact(t, scope, "unknown")
				return derivedFact(t, seed, "validation", "1", "unknown-input", unknown)
			},
		},
		{
			name: "wrong scope",
			make: func() fact.CanonicalFact {
				otherScope := testScope()
				otherScope.OrganizationID = "organization-other"
				otherScope.SourceID = "source-other"
				otherScope.SnapshotID = "snapshot-other"
				otherSeed := observedFact(t, otherScope, "other-seed")
				return derivedFact(t, otherSeed, "validation", "1", "wrong-scope", otherSeed)
			},
		},
		{
			name: "malformed fact",
			make: func() fact.CanonicalFact { return fact.CanonicalFact{} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := tt.make()
			registry, err := derivation.NewRegistry(derivation.Registration{
				RuleID:  "validation",
				Version: "1",
				Rule: derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
					return []fact.CanonicalFact{candidate}, nil
				}),
			})
			if err != nil {
				t.Fatalf("NewRegistry() error = %v", err)
			}
			executor, err := derivation.NewExecutor(registry)
			if err != nil {
				t.Fatalf("NewExecutor() error = %v", err)
			}
			got, err := executor.Derive(context.Background(), []fact.CanonicalFact{seed})
			if got != nil {
				t.Fatalf("partial result = %#v, want nil", got)
			}
			if !errors.Is(err, derivation.ErrInvalidOutput) {
				t.Fatalf("Derive() error = %v, want ErrInvalidOutput", err)
			}
		})
	}
}

func TestExecutorCancellationAndRuleErrorsAreAtomicAndSanitized(t *testing.T) {
	t.Parallel()

	scope := testScope()
	seed := observedFact(t, scope, "seed")
	canceled, cancel := context.WithCancel(context.Background())
	called := false
	cancelRule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
		called = true
		cancel()
		return nil, nil
	})
	registry, err := derivation.NewRegistry(derivation.Registration{RuleID: "cancel", Version: "1", Rule: cancelRule})
	if err != nil {
		t.Fatalf("NewRegistry(cancel) error = %v", err)
	}
	executor, err := derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor(cancel) error = %v", err)
	}
	got, err := executor.Derive(canceled, []fact.CanonicalFact{seed})
	if !called {
		t.Fatal("cancel rule was not called")
	}
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Derive() = %#v, %v; want nil and context.Canceled", got, err)
	}

	secret := "secret-rule-output"
	failingRule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
		return []fact.CanonicalFact{seed}, errors.New(secret)
	})
	registry, err = derivation.NewRegistry(derivation.Registration{RuleID: "failure", Version: "1", Rule: failingRule})
	if err != nil {
		t.Fatalf("NewRegistry(failure) error = %v", err)
	}
	executor, err = derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor(failure) error = %v", err)
	}
	got, err = executor.Derive(context.Background(), []fact.CanonicalFact{seed})
	if got != nil {
		t.Fatalf("failure partial result = %#v, want nil", got)
	}
	if !errors.Is(err, derivation.ErrRuleFailed) || strings.Contains(err.Error(), secret) {
		t.Fatalf("failure error = %v, want sanitized ErrRuleFailed", err)
	}

	panicSecret := "secret-rule-panic"
	panicRule := derivation.RuleFunc(func(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
		panic(panicSecret)
	})
	registry, err = derivation.NewRegistry(derivation.Registration{RuleID: "panic", Version: "1", Rule: panicRule})
	if err != nil {
		t.Fatalf("NewRegistry(panic) error = %v", err)
	}
	executor, err = derivation.NewExecutor(registry)
	if err != nil {
		t.Fatalf("NewExecutor(panic) error = %v", err)
	}
	got, err = executor.Derive(context.Background(), []fact.CanonicalFact{seed})
	if got != nil {
		t.Fatalf("panic partial result = %#v, want nil", got)
	}
	if !errors.Is(err, derivation.ErrRuleFailed) || strings.Contains(err.Error(), panicSecret) {
		t.Fatalf("panic error = %v, want sanitized ErrRuleFailed", err)
	}
}

type testRule struct{}

func (*testRule) Apply(context.Context, derivation.FactView) ([]fact.CanonicalFact, error) {
	return nil, nil
}

func testScope() fact.Scope {
	return fact.Scope{
		OrganizationID: "organization-test",
		SourceID:       "source-test",
		SnapshotID:     "snapshot-test",
	}
}

func observedFact(t *testing.T, scope fact.Scope, subjectID string) fact.CanonicalFact {
	t.Helper()
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     scope,
		Predicate: fact.PredicateDefinition,
		Subject: fact.Participant{
			Kind: fact.ParticipantSymbol,
			ID:   subjectID,
		},
		Producer: fact.Producer{
			ID:      "observed-frontend",
			Version: "1",
			Method:  "test",
		},
		Evidence: []fact.EvidenceRef{
			{
				ID: "evidence-" + subjectID,
				Locator: contract.Locator{
					SourceID:   scope.SourceID,
					ArtifactID: "artifact-" + subjectID,
					Path:       "src/" + subjectID + ".go",
					StartLine:  1,
					EndLine:    1,
				},
			},
		},
	}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func derivedFact(
	t *testing.T,
	base fact.CanonicalFact,
	ruleID,
	version,
	subjectID string,
	inputs ...fact.CanonicalFact,
) fact.CanonicalFact {
	t.Helper()
	inputIDs := make([]string, len(inputs))
	for index, input := range inputs {
		inputIDs[index] = input.ID
	}
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     base.Scope,
		Predicate: fact.PredicateDependency,
		Subject: fact.Participant{
			Kind: fact.ParticipantSymbol,
			ID:   subjectID,
		},
		Object: &fact.Participant{
			Kind: fact.ParticipantSymbol,
			ID:   "derived-object-" + subjectID,
		},
		Producer: fact.Producer{
			ID:      "test-rule-producer",
			Version: version,
			Method:  ruleID,
		},
		Evidence: []fact.EvidenceRef{
			{
				ID: "evidence-derived-" + subjectID,
				Locator: contract.Locator{
					SourceID:   base.Scope.SourceID,
					ArtifactID: "artifact-derived-" + subjectID,
					Path:       "derived/" + subjectID + ".go",
					StartLine:  1,
					EndLine:    1,
				},
			},
		},
		Lineage: &fact.Lineage{
			RuleID:       ruleID,
			RuleVersion:  version,
			InputFactIDs: inputIDs,
		},
	}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func mustFactID(t *testing.T, candidate fact.CanonicalFact) string {
	t.Helper()
	id, err := fact.DeriveID(candidate)
	if err != nil {
		t.Fatalf("DeriveID() error = %v", err)
	}
	return id
}

func countFactID(facts []fact.CanonicalFact, id string) int {
	count := 0
	for _, candidate := range facts {
		if candidate.ID == id {
			count++
		}
	}
	return count
}

func cloneFacts(input []fact.CanonicalFact) []fact.CanonicalFact {
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
