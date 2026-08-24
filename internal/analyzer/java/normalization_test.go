package java

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

func TestNormalizerRegistrationsMapJavaContributionsToCanonicalFacts(t *testing.T) {
	registry := javaRegistry(t, javaManifest())
	tests := []struct {
		name         string
		contribution string
		method       string
		value        string
		factCount    int
		predicates   []fact.Predicate
	}{
		{
			name:         "artifact",
			contribution: javaArtifactContribution,
			method:       "inventory:Main.java",
			value:        `{"path":"src/Main.java","type":"java","hash":"abc","size":42}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateArtifact},
		},
		{
			name:         "type",
			contribution: javaTypeContribution,
			method:       "type:BookingService",
			value:        `{"kind":"class","name":"BookingService","qualified_name":"example.BookingService"}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateSymbol, fact.PredicateDefinition},
		},
		{
			name:         "method",
			contribution: javaMethodContribution,
			method:       "method:create:12",
			value:        `{"kind":"method","name":"create","parameters":"String id","return_type":"Booking"}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateSymbol, fact.PredicateDefinition},
		},
		{
			name:         "import",
			contribution: javaImportContribution,
			method:       "import:example.Booking",
			value:        `{"name":"example.Booking","static":false}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateReference, fact.PredicateDependency},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := javaInput(test.contribution, test.method, json.RawMessage(test.value))
			output, err := registry.Normalize(context.Background(), input)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(output.Facts) != test.factCount || len(output.Coverage) != 0 {
				t.Fatalf("output counts = facts:%d coverage:%d, want facts:%d and no coverage", len(output.Facts), len(output.Coverage), test.factCount)
			}
			gotPredicates := make([]fact.Predicate, 0, len(output.Facts))
			for _, candidate := range output.Facts {
				if err := candidate.Validate(); err != nil {
					t.Fatalf("fact.Validate() error = %v, fact = %#v", err, candidate)
				}
				if candidate.Version != fact.Version || candidate.Scope != input.Scope || candidate.Producer != (fact.Producer{ID: input.Manifest.ID, Version: input.Manifest.Version, Method: input.Manifest.Method}) {
					t.Fatalf("fact provenance = %#v, want version/scope/producer from input", candidate)
				}
				if !reflect.DeepEqual(candidate.Evidence, input.Evidence) {
					t.Fatalf("fact evidence = %#v, want %#v", candidate.Evidence, input.Evidence)
				}
				gotPredicates = append(gotPredicates, candidate.Predicate)
			}
			if !samePredicates(gotPredicates, test.predicates) {
				t.Fatalf("predicates = %#v, want %#v", gotPredicates, test.predicates)
			}
			if test.contribution == javaTypeContribution || test.contribution == javaMethodContribution {
				for _, candidate := range output.Facts {
					if candidate.Predicate == fact.PredicateDefinition {
						if candidate.Object == nil || candidate.Object.Kind != fact.ParticipantArtifact || candidate.Object.ID != input.Contribution.ArtifactID {
							t.Fatalf("definition object = %#v, want artifact %q", candidate.Object, input.Contribution.ArtifactID)
						}
					}
				}
			}
			if test.contribution == javaImportContribution {
				for _, candidate := range output.Facts {
					if candidate.Subject.Kind != fact.ParticipantArtifact || candidate.Subject.ID != input.Contribution.ArtifactID || candidate.Object == nil || candidate.Object.Kind != fact.ParticipantSymbol {
						t.Fatalf("import relation = %#v, want artifact to lexical symbol", candidate)
					}
				}
			}
		})
	}
}

func TestJavaNormalizationUsesObservationMethodAndKeepsOverloadsDistinct(t *testing.T) {
	manifest := javaManifest()
	registry := javaRegistry(t, manifest)
	first := javaInput(javaMethodContribution, "method:overload:1", json.RawMessage(`{"kind":"method","name":"load","parameters":"String id"}`))
	second := javaInput(javaMethodContribution, "method:overload:2", json.RawMessage(`{"kind":"method","name":"load","parameters":"long id"}`))
	first.Contribution.Method = "method:observed:one"
	second.Contribution.Method = "method:observed:two"
	original := first
	firstOutput, err := registry.Normalize(context.Background(), first)
	if err != nil {
		t.Fatalf("Normalize(first) error = %v", err)
	}
	secondOutput, err := registry.Normalize(context.Background(), second)
	if err != nil {
		t.Fatalf("Normalize(second) error = %v", err)
	}
	if firstOutput.Facts[0].Producer.Method != manifest.Method || secondOutput.Facts[0].Producer.Method != manifest.Method {
		t.Fatalf("producer methods = %q/%q, want manifest method %q", firstOutput.Facts[0].Producer.Method, secondOutput.Facts[0].Producer.Method, manifest.Method)
	}
	if firstOutput.Facts[0].Subject.ID == secondOutput.Facts[0].Subject.ID {
		t.Fatalf("overloaded methods collided: %#v/%#v", firstOutput.Facts[0], secondOutput.Facts[0])
	}
	repeated, err := registry.Normalize(context.Background(), first)
	if err != nil || !reflect.DeepEqual(repeated, firstOutput) {
		t.Fatalf("repeated normalization = %#v/%v, want %#v/nil", repeated, err, firstOutput)
	}
	if !reflect.DeepEqual(first, original) {
		t.Fatalf("normalization mutated input: got %#v, want %#v", first, original)
	}

	const workers = 16
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			output, normalizeErr := registry.Normalize(context.Background(), first)
			if normalizeErr != nil || !reflect.DeepEqual(output, firstOutput) {
				errorsChannel <- normalizeErr
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Errorf("concurrent Normalize() error = %v", err)
		} else {
			t.Error("concurrent Normalize() returned a different output")
		}
	}
}

func TestJavaNormalizationReturnsConservativeCoverageWithoutEvidence(t *testing.T) {
	manifest := javaManifest()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error = %v", err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	for _, contributionType := range []string{javaArtifactContribution, javaTypeContribution, javaMethodContribution, javaImportContribution} {
		input := javaInput(contributionType, "observation", json.RawMessage(`{"name":"kept out of output"}`))
		input.Evidence = nil
		output, normalizeErr := registry.Normalize(context.Background(), input)
		if normalizeErr != nil {
			t.Fatalf("Normalize(%q) error = %v", contributionType, normalizeErr)
		}
		if len(output.Facts) != 0 || len(output.Coverage) != 1 {
			t.Fatalf("Normalize(%q) output = %#v, want zero facts and one coverage", contributionType, output)
		}
		coverage := output.Coverage[0]
		if coverage.Scope != input.Contribution.ID || coverage.AnalyzerID != manifest.ID || coverage.State != contract.CoverageIncomplete || coverage.Message != MissingEvidenceCoverageMessage {
			t.Fatalf("coverage = %#v, want contribution scope, Java analyzer, incomplete state and fixed message", coverage)
		}
		if coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
			t.Fatalf("coverage id = %q is not deterministic", coverage.ID)
		}
		if err := coverage.Validate(); err != nil {
			t.Fatalf("coverage.Validate() error = %v", err)
		}
		if !declaresDimension(manifest, contract.Dimension(coverage.Dimension)) {
			t.Fatalf("coverage dimension %q was not declared", coverage.Dimension)
		}
	}
}

func TestJavaNormalizationRejectsMalformedPayloadAndManifestWithoutLeakingData(t *testing.T) {
	registry := javaRegistry(t, javaManifest())
	input := javaInput(javaTypeContribution, "type:invalid", json.RawMessage(`{"kind":"class","name":"sensitive-symbol"}`))
	input.Contribution.Value = json.RawMessage(`{"kind":17,"name":"sensitive-symbol"}`)
	output, err := registry.Normalize(context.Background(), input)
	if !errors.Is(err, normalization.ErrNormalizationFailed) || !reflect.DeepEqual(output, normalization.Output{}) || strings.Contains(err.Error(), "sensitive-symbol") {
		t.Fatalf("malformed payload output/error = %#v/%v, want redacted failure and zero output", output, err)
	}

	mutations := []func(*fact.FrontendManifest){
		func(manifest *fact.FrontendManifest) { manifest.ID = "other" },
		func(manifest *fact.FrontendManifest) { manifest.Version = "2" },
		func(manifest *fact.FrontendManifest) { manifest.Method = "other" },
		func(manifest *fact.FrontendManifest) {
			manifest.Predicates = removePredicate(manifest.Predicates, fact.PredicateDependency)
		},
		func(manifest *fact.FrontendManifest) {
			manifest.Capabilities = removeDimension(manifest.Capabilities, contract.DimensionFlowsAndDependencies)
		},
	}
	for index, mutate := range mutations {
		candidate := javaManifest()
		mutate(&candidate)
		if _, err := NormalizerRegistrations(candidate); err == nil {
			t.Fatalf("mutation %d accepted incompatible Java manifest", index)
		}
	}
}

func javaRegistry(t *testing.T, manifest fact.FrontendManifest) *normalization.Registry {
	t.Helper()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error = %v", err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

func javaManifest() fact.FrontendManifest {
	return fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		SourceTypes:     []string{"repository"},
		Families:        []string{"jvm"},
		Versions:        []string{"17"},
		Capabilities: []contract.Dimension{
			contract.DimensionLandscapeInventoryStructure,
			contract.DimensionEntitiesAndRelationships,
			contract.DimensionFlowsAndDependencies,
		},
		Predicates: []fact.Predicate{
			fact.PredicateArtifact,
			fact.PredicateSymbol,
			fact.PredicateDefinition,
			fact.PredicateReference,
			fact.PredicateDependency,
		},
		Execution: fact.ExecutionProfileSafeStatic,
	}
}

func javaInput(contributionType, method string, value json.RawMessage) normalization.Input {
	scope := fact.Scope{OrganizationID: "organization-java", SourceID: "source-java", SnapshotID: "snapshot-java"}
	contribution := contract.Contribution{
		ID:              "contribution-" + strings.ReplaceAll(contributionType, ".", "-"),
		ArtifactID:      "artifact-java-main",
		AnalyzerID:      AnalyzerID,
		AnalyzerVersion: AnalyzerVersion,
		Method:          method,
		Type:            contributionType,
		Locator: contract.Locator{
			SourceID:   scope.SourceID,
			ArtifactID: "artifact-java-main",
			Path:       "src/Main.java",
			StartLine:  4,
			EndLine:    4,
		},
		Value: value,
	}
	return normalization.Input{
		Scope:        scope,
		Manifest:     javaManifest(),
		Contribution: contribution,
		Evidence: []fact.EvidenceRef{{
			ID: "evidence-java-main",
			Locator: contract.Locator{
				SourceID:   scope.SourceID,
				ArtifactID: contribution.ArtifactID,
				Path:       contribution.Locator.Path,
				StartLine:  4,
				EndLine:    4,
			},
		}},
	}
}

func samePredicates(left, right []fact.Predicate) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]fact.Predicate(nil), left...)
	right = append([]fact.Predicate(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	sort.Slice(right, func(i, j int) bool { return right[i] < right[j] })
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func removePredicate(values []fact.Predicate, unwanted fact.Predicate) []fact.Predicate {
	result := make([]fact.Predicate, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}

func removeDimension(values []contract.Dimension, unwanted contract.Dimension) []contract.Dimension {
	result := make([]contract.Dimension, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}
