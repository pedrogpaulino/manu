package normalization_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

func TestRegistryNormalizeAllPublishesDeterministicConflictsWithOriginalFacts(t *testing.T) {
	t.Parallel()

	first, second := conflictInputs(t, "frontend-a", "frontend-b")
	firstSnapshot := cloneNormalizationInputForTest(first)
	secondSnapshot := cloneNormalizationInputForTest(second)
	registry, err := normalization.NewRegistry(
		normalizationRegistration(first, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "claim-a", "origin-a")}}
		}),
		normalizationRegistration(second, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "claim-b", "origin-b")}}
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	forward, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, second})
	if err != nil {
		t.Fatalf("NormalizeAll(forward) error = %v", err)
	}
	reverse, err := registry.NormalizeAll(context.Background(), []normalization.Input{second, first})
	if err != nil {
		t.Fatalf("NormalizeAll(reverse) error = %v", err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("input order changed output:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	if len(forward.Facts) != 2 || len(forward.Conflicts) != 1 {
		t.Fatalf("output facts/conflicts = %d/%d, want 2/1", len(forward.Facts), len(forward.Conflicts))
	}
	if err := forward.Conflicts[0].Validate(); err != nil {
		t.Fatalf("Conflict.Validate() error = %v", err)
	}
	conflict := forward.Conflicts[0]
	if len(conflict.Assertions) != 2 || len(conflict.Facts) != 2 {
		t.Fatalf("conflict assertions/facts = %d/%d, want 2/2", len(conflict.Assertions), len(conflict.Facts))
	}
	producers := map[string]bool{}
	evidence := map[string]bool{}
	qualifiers := map[string]bool{}
	claims := map[string]bool{}
	for _, candidate := range conflict.Facts {
		producers[candidate.Producer.ID+"/"+candidate.Producer.Version+"/"+candidate.Producer.Method] = true
		for _, support := range candidate.Evidence {
			evidence[support.ID] = true
		}
		for _, qualifier := range candidate.Qualifiers {
			qualifiers[qualifier.Value.String] = true
		}
		if candidate.Object != nil {
			claims[candidate.Object.ID] = true
		}
	}
	if !reflect.DeepEqual(producers, map[string]bool{
		"frontend-a/1/symbols": true,
		"frontend-b/1/symbols": true,
	}) {
		t.Fatalf("conflict producers = %#v", producers)
	}
	if !reflect.DeepEqual(evidence, map[string]bool{"evidence-a": true, "evidence-b": true}) {
		t.Fatalf("conflict evidence = %#v", evidence)
	}
	if !reflect.DeepEqual(qualifiers, map[string]bool{"origin-a": true, "origin-b": true}) {
		t.Fatalf("conflict qualifiers = %#v", qualifiers)
	}
	if !reflect.DeepEqual(claims, map[string]bool{"claim-a": true, "claim-b": true}) {
		t.Fatalf("conflict assertions = %#v", claims)
	}
	if !reflect.DeepEqual(first, firstSnapshot) || !reflect.DeepEqual(second, secondSnapshot) {
		t.Fatalf("NormalizeAll mutated inputs: first=%#v second=%#v", first, second)
	}

	forwardFactsSnapshot := cloneCanonicalFactsForTest(forward.Facts)
	forward.Conflicts[0].Facts[0].Evidence[0].ID = "mutated"
	if !reflect.DeepEqual(first, firstSnapshot) || !reflect.DeepEqual(second, secondSnapshot) {
		t.Fatalf("published conflict shares input support metadata")
	}
	if !reflect.DeepEqual(forward.Facts, forwardFactsSnapshot) {
		t.Fatalf("published conflict shares support metadata with composed facts")
	}
}

func TestRegistryNormalizeAllDoesNotConflictForSameAssertion(t *testing.T) {
	t.Parallel()

	first, second := conflictInputs(t, "frontend-same-a", "frontend-same-b")
	registry, err := normalization.NewRegistry(
		normalizationRegistration(first, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "same-claim", "origin-a")}}
		}),
		normalizationRegistration(second, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "same-claim", "origin-b")}}
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, second})
	if err != nil {
		t.Fatalf("NormalizeAll() error = %v", err)
	}
	if len(output.Facts) != 2 || len(output.Conflicts) != 0 {
		t.Fatalf("facts/conflicts = %d/%d, want 2/0", len(output.Facts), len(output.Conflicts))
	}
}

func TestRegistryNormalizeAllSkipsMultivaluedPredicateConflicts(t *testing.T) {
	t.Parallel()

	first, second := conflictInputs(t, "frontend-reference-a", "frontend-reference-b")
	registry, err := normalization.NewRegistry(
		normalizationRegistration(first, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictReferenceFact(t, input, "target-a")}}
		}),
		normalizationRegistration(second, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictReferenceFact(t, input, "target-b")}}
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, second})
	if err != nil {
		t.Fatalf("NormalizeAll() error = %v", err)
	}
	if len(output.Facts) != 2 || len(output.Conflicts) != 0 {
		t.Fatalf("facts/conflicts = %d/%d, want 2/0", len(output.Facts), len(output.Conflicts))
	}
}

func TestRegistryNormalizeAllDistinguishesProducerVersionAndMethod(t *testing.T) {
	t.Parallel()

	first, second := conflictInputs(t, "frontend-versioned", "frontend-versioned")
	second.Manifest.Version = "2"
	second.Manifest.Method = "symbols-v2"
	second.Manifest.Versions = []string{"2"}
	second.Contribution.AnalyzerVersion = second.Manifest.Version
	second.Contribution.Method = second.Manifest.Method
	registry, err := normalization.NewRegistry(
		normalizationRegistration(first, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "claim-v1", "origin-v1")}}
		}),
		normalizationRegistration(second, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "claim-v2", "origin-v2")}}
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, second})
	if err != nil {
		t.Fatalf("NormalizeAll() error = %v", err)
	}
	if len(output.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1 for distinct version/method", len(output.Conflicts))
	}
}

func TestRegistryRejectsNormalizerProvidedConflicts(t *testing.T) {
	t.Parallel()

	input := normalizationInput(t, "frontend-forged-conflict")
	registry, err := normalization.NewRegistry(normalizationRegistration(input, func(_ normalization.Input) normalization.Output {
		return normalization.Output{Conflicts: []fact.Conflict{{}}}
	}))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	for name, normalize := range map[string]func() (normalization.Output, error){
		"single": func() (normalization.Output, error) {
			return registry.Normalize(context.Background(), input)
		},
		"all": func() (normalization.Output, error) {
			return registry.NormalizeAll(context.Background(), []normalization.Input{input})
		},
	} {
		t.Run(name, func(t *testing.T) {
			output, normalizeErr := normalize()
			if !errors.Is(normalizeErr, normalization.ErrInvalidOutput) || !reflect.DeepEqual(output, normalization.Output{}) {
				t.Fatalf("output/error = %#v/%v, want zero output and ErrInvalidOutput", output, normalizeErr)
			}
		})
	}
}

func conflictInputs(t *testing.T, firstID, secondID string) (normalization.Input, normalization.Input) {
	t.Helper()
	first := normalizationInput(t, firstID)
	second := normalizationInput(t, secondID)
	first.Contribution.ID = "contribution-" + firstID
	second.Contribution.ID = "contribution-" + secondID
	first.Evidence[0].ID = "evidence-a"
	second.Evidence[0].ID = "evidence-b"
	return first, second
}

func conflictDefinitionFact(t *testing.T, input normalization.Input, claim, origin string) fact.CanonicalFact {
	t.Helper()
	candidate := normalizationFact(t, input, "shared-slot", fact.PredicateDefinition)
	candidate.Object = &fact.Participant{Kind: fact.ParticipantArtifact, ID: claim}
	candidate.Qualifiers = []fact.Qualifier{{
		Name:  fact.QualifierOrigin,
		Value: fact.TypedValue{Kind: fact.ValueString, String: origin},
	}}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func conflictReferenceFact(t *testing.T, input normalization.Input, target string) fact.CanonicalFact {
	t.Helper()
	candidate := normalizationFact(t, input, "shared-slot", fact.PredicateReference)
	candidate.Object = &fact.Participant{Kind: fact.ParticipantNamedElement, ID: target}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func cloneNormalizationInputForTest(input normalization.Input) normalization.Input {
	clone := input
	clone.Manifest.SourceTypes = append([]string(nil), input.Manifest.SourceTypes...)
	clone.Manifest.Families = append([]string(nil), input.Manifest.Families...)
	clone.Manifest.Versions = append([]string(nil), input.Manifest.Versions...)
	clone.Manifest.Capabilities = append([]contract.Dimension(nil), input.Manifest.Capabilities...)
	clone.Manifest.Limitations = append([]string(nil), input.Manifest.Limitations...)
	clone.Manifest.Predicates = append([]fact.Predicate(nil), input.Manifest.Predicates...)
	clone.Manifest.Extensions = append([]fact.ExtensionSchema(nil), input.Manifest.Extensions...)
	clone.Evidence = append([]fact.EvidenceRef(nil), input.Evidence...)
	clone.Extensions = cloneTestExtensions(input.Extensions)
	if input.Contribution.Value != nil {
		clone.Contribution.Value = append([]byte(nil), input.Contribution.Value...)
	}
	return clone
}

func cloneCanonicalFactsForTest(facts []fact.CanonicalFact) []fact.CanonicalFact {
	if facts == nil {
		return nil
	}
	cloned := make([]fact.CanonicalFact, len(facts))
	for index, candidate := range facts {
		cloned[index] = candidate
		if candidate.Object != nil {
			object := *candidate.Object
			cloned[index].Object = &object
		}
		if candidate.Value != nil {
			value := *candidate.Value
			cloned[index].Value = &value
		}
		cloned[index].Qualifiers = append([]fact.Qualifier(nil), candidate.Qualifiers...)
		cloned[index].Evidence = append([]fact.EvidenceRef(nil), candidate.Evidence...)
		if candidate.Lineage != nil {
			lineage := *candidate.Lineage
			lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
			cloned[index].Lineage = &lineage
		}
	}
	return cloned
}

func TestRegistryNormalizeAllConflictJSONIsDeterministic(t *testing.T) {
	t.Parallel()

	first, second := conflictInputs(t, "frontend-json-a", "frontend-json-b")
	registry, err := normalization.NewRegistry(
		normalizationRegistration(first, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "claim-a", "origin-a")}}
		}),
		normalizationRegistration(second, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{conflictDefinitionFact(t, input, "claim-b", "origin-b")}}
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	left, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, second})
	if err != nil {
		t.Fatalf("NormalizeAll(left) error = %v", err)
	}
	right, err := registry.NormalizeAll(context.Background(), []normalization.Input{second, first})
	if err != nil {
		t.Fatalf("NormalizeAll(right) error = %v", err)
	}
	leftJSON, err := json.Marshal(left.Conflicts)
	if err != nil {
		t.Fatalf("json.Marshal(left) error = %v", err)
	}
	rightJSON, err := json.Marshal(right.Conflicts)
	if err != nil {
		t.Fatalf("json.Marshal(right) error = %v", err)
	}
	if string(leftJSON) != string(rightJSON) {
		t.Fatalf("reordered inputs changed conflicts:\nleft=%s\nright=%s", leftJSON, rightJSON)
	}
}
