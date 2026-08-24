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
		dimension    contract.Dimension
	}{
		{
			name:         "artifact",
			contribution: javaArtifactContribution,
			method:       "inventory:Main.java",
			value:        `{"path":"src/Main.java","type":"java","hash":"abc","size":42}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateArtifact},
			dimension:    contract.DimensionLandscapeInventoryStructure,
		},
		{
			name:         "package",
			contribution: javaPackageContribution,
			method:       "package:example.booking",
			value:        `{"name":"example.booking"}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateNamedElement, fact.PredicateMembership},
			dimension:    contract.DimensionLandscapeInventoryStructure,
		},
		{
			name:         "type",
			contribution: javaTypeContribution,
			method:       "type:BookingService",
			value:        `{"kind":"class","name":"BookingService","qualified_name":"example.BookingService"}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateSymbol, fact.PredicateDefinition},
			dimension:    contract.DimensionEntitiesAndRelationships,
		},
		{
			name:         "method",
			contribution: javaMethodContribution,
			method:       "method:create:12",
			value:        `{"kind":"method","name":"create","parameters":"String id","return_type":"Booking"}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateSymbol, fact.PredicateDefinition},
			dimension:    contract.DimensionEntitiesAndRelationships,
		},
		{
			name:         "import",
			contribution: javaImportContribution,
			method:       "import:example.Booking",
			value:        `{"name":"example.Booking","static":false}`,
			factCount:    2,
			predicates:   []fact.Predicate{fact.PredicateReference, fact.PredicateDependency},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "relation call",
			contribution: javaRelationContribution,
			method:       "relation:call:1",
			value:        `{"kind":"call","from":"service","to":"repository.save"}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateCall},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "relation extends",
			contribution: javaRelationContribution,
			method:       "relation:extends:1",
			value:        `{"kind":"extends","to":"BaseService"}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateDependency},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "relation implements",
			contribution: javaRelationContribution,
			method:       "relation:implements:1",
			value:        `{"kind":"implements","from":"BookingService","to":"Auditable"}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateDependency},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "relation constructs",
			contribution: javaRelationContribution,
			method:       "relation:constructs:1",
			value:        `{"kind":"constructs","to":"Booking"}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateDependency},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "configuration",
			contribution: javaConfigurationContribution,
			method:       "config:booking.url:1",
			value:        `{"key":"booking.url","kind":"property-access"}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateConfiguration},
			dimension:    contract.DimensionConfigurationVariations,
		},
		{
			name:         "endpoint",
			contribution: javaEndpointContribution,
			method:       "endpoint:/bookings:1",
			value:        `{"path":"/bookings","http_method":"GET"}`,
			factCount:    1,
			predicates:   []fact.Predicate{fact.PredicateEndpoint},
			dimension:    contract.DimensionEntitiesAndRelationships,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := javaInput(test.contribution, test.method, json.RawMessage(test.value))
			output, err := registry.Normalize(context.Background(), input)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(output.Facts) != test.factCount || len(output.Coverage) != 1 {
				t.Fatalf("output counts = facts:%d coverage:%d, want facts:%d and one coverage", len(output.Facts), len(output.Coverage), test.factCount)
			}
			coverage := output.Coverage[0]
			if coverage.Dimension != string(test.dimension) || coverage.Scope != input.Contribution.ID || coverage.AnalyzerID != input.Manifest.ID || coverage.State != contract.CoverageProduced || coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
				t.Fatalf("coverage = %#v, want produced coverage for %q and contribution scope", coverage, test.dimension)
			}
			if err := coverage.Validate(); err != nil {
				t.Fatalf("coverage.Validate() error = %v", err)
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
			if test.contribution == javaPackageContribution {
				for _, candidate := range output.Facts {
					if candidate.Predicate == fact.PredicateMembership {
						if candidate.Subject.Kind != fact.ParticipantArtifact || candidate.Subject.ID != input.Contribution.ArtifactID || candidate.Object == nil || candidate.Object.Kind != fact.ParticipantNamedElement {
							t.Fatalf("package membership = %#v, want artifact -> package", candidate)
						}
					}
				}
			}
			if test.contribution == javaConfigurationContribution {
				for _, candidate := range output.Facts {
					if candidate.Value == nil || candidate.Value.Kind != fact.ValueString || candidate.Value.String != "booking.url" || len(candidate.Qualifiers) != 1 || candidate.Qualifiers[0].Name != "kind" || candidate.Qualifiers[0].Value.String != "property-access" {
						t.Fatalf("configuration value = %#v, want key literal", candidate.Value)
					}
				}
			}
			if test.contribution == javaEndpointContribution {
				for _, candidate := range output.Facts {
					if candidate.Value == nil || candidate.Value.Kind != fact.ValueString || candidate.Value.String != "/bookings" || candidate.Subject.Kind != fact.ParticipantNamedElement || len(candidate.Qualifiers) != 1 || candidate.Qualifiers[0].Name != "http_method" || candidate.Qualifiers[0].Value.String != "GET" {
						t.Fatalf("endpoint fact = %#v, want deterministic named endpoint with path value", candidate)
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
	for _, contributionType := range []string{
		javaArtifactContribution,
		javaPackageContribution,
		javaTypeContribution,
		javaMethodContribution,
		javaImportContribution,
		javaRelationContribution,
		javaConfigurationContribution,
		javaEndpointContribution,
	} {
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
	}
	for _, predicate := range []fact.Predicate{
		fact.PredicateArtifact,
		fact.PredicateSymbol,
		fact.PredicateNamedElement,
		fact.PredicateDefinition,
		fact.PredicateReference,
		fact.PredicateCall,
		fact.PredicateDependency,
		fact.PredicateConfiguration,
		fact.PredicateEndpoint,
		fact.PredicateMembership,
	} {
		predicate := predicate
		mutations = append(mutations, func(manifest *fact.FrontendManifest) {
			manifest.Predicates = removePredicate(manifest.Predicates, predicate)
		})
	}
	for _, dimension := range []contract.Dimension{
		contract.DimensionLandscapeInventoryStructure,
		contract.DimensionEntitiesAndRelationships,
		contract.DimensionFlowsAndDependencies,
		contract.DimensionConfigurationVariations,
	} {
		dimension := dimension
		mutations = append(mutations, func(manifest *fact.FrontendManifest) {
			manifest.Capabilities = removeDimension(manifest.Capabilities, dimension)
		})
	}
	for index, mutate := range mutations {
		candidate := javaManifest()
		mutate(&candidate)
		if _, err := NormalizerRegistrations(candidate); err == nil {
			t.Fatalf("mutation %d accepted incompatible Java manifest", index)
		}
	}
}

func TestJavaNormalizationRejectsNewMappingErrorsWithoutLeakingPayload(t *testing.T) {
	tests := []struct {
		name         string
		contribution string
		value        string
	}{
		{name: "package missing name", contribution: javaPackageContribution, value: `{"secret":"package"}`},
		{name: "relation missing target", contribution: javaRelationContribution, value: `{"kind":"extends"}`},
		{name: "relation call missing source", contribution: javaRelationContribution, value: `{"kind":"call","to":"target"}`},
		{name: "relation unknown kind", contribution: javaRelationContribution, value: `{"kind":"throws","to":"target"}`},
		{name: "configuration missing key", contribution: javaConfigurationContribution, value: `{"kind":"property","secret":"configuration"}`},
		{name: "endpoint missing path", contribution: javaEndpointContribution, value: `{"http_method":"GET","secret":"endpoint"}`},
		{name: "endpoint unsupported method", contribution: javaEndpointContribution, value: `{"path":"/bookings","http_method":"TRACE","secret":"endpoint"}`},
		{name: "endpoint control path", contribution: javaEndpointContribution, value: "{\"path\":\"/bookings\\nsecret\"}"},
	}
	registry := javaRegistry(t, javaManifest())
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := javaInput(test.contribution, "invalid", json.RawMessage(test.value))
			output, err := registry.Normalize(context.Background(), input)
			if !errors.Is(err, normalization.ErrNormalizationFailed) || !reflect.DeepEqual(output, normalization.Output{}) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("Normalize() output/error = %#v/%v, want redacted failure and zero output", output, err)
			}
		})
	}
}

func TestJavaNormalizationComposesAllMappedContributionsDeterministically(t *testing.T) {
	registry := javaRegistry(t, javaManifest())
	inputs := []normalization.Input{
		javaInput(javaArtifactContribution, "artifact", json.RawMessage(`{"path":"src/Main.java"}`)),
		javaInput(javaTypeContribution, "type", json.RawMessage(`{"kind":"class","name":"Main"}`)),
		javaInput(javaMethodContribution, "method", json.RawMessage(`{"name":"run","parameters":"String value"}`)),
		javaInput(javaImportContribution, "import", json.RawMessage(`{"name":"example.Dependency"}`)),
		javaInput(javaConfigurationContribution, "configuration", json.RawMessage(`{"key":"service.url","kind":"property"}`)),
		javaInput(javaEndpointContribution, "endpoint", json.RawMessage(`{"path":"/run","http_method":"POST"}`)),
	}
	forward, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll(forward) error = %v", err)
	}
	reverseInputs := append([]normalization.Input(nil), inputs...)
	for left, right := 0, len(reverseInputs)-1; left < right; left, right = left+1, right-1 {
		reverseInputs[left], reverseInputs[right] = reverseInputs[right], reverseInputs[left]
	}
	reverse, err := registry.NormalizeAll(context.Background(), reverseInputs)
	if err != nil {
		t.Fatalf("NormalizeAll(reverse) error = %v", err)
	}
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("NormalizeAll() changed with input order:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	if len(forward.Facts) != 9 || len(forward.Coverage) != len(inputs) {
		t.Fatalf("composed output counts = facts:%d coverage:%d, want 9 facts and %d coverages", len(forward.Facts), len(forward.Coverage), len(inputs))
	}
	for _, candidate := range forward.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("composed fact.Validate() error = %v", err)
		}
	}
	for _, coverage := range forward.Coverage {
		if coverage.State != contract.CoverageProduced || coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
			t.Fatalf("composed coverage = %#v, want deterministic produced coverage", coverage)
		}
	}
}

func TestJavaNormalizerLeavesUnsupportedObservationTypesOnRegistryFallback(t *testing.T) {
	registrations, err := NormalizerRegistrations(javaManifest())
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error = %v", err)
	}
	if len(registrations) != 8 {
		t.Fatalf("registration count = %d, want eight supported contribution types", len(registrations))
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	for _, contributionType := range []string{"java.annotation", "java.exception"} {
		input := javaInput(contributionType, "unsupported", json.RawMessage(`{"name":"unsupported"}`))
		output, normalizeErr := registry.Normalize(context.Background(), input)
		if normalizeErr != nil || len(output.Facts) != 0 || len(output.Coverage) != len(input.Manifest.Capabilities) {
			t.Fatalf("fallback for %q = %#v/%v, want no facts and manifest capability coverage", contributionType, output, normalizeErr)
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
			contract.DimensionConfigurationVariations,
		},
		Predicates: []fact.Predicate{
			fact.PredicateArtifact,
			fact.PredicateSymbol,
			fact.PredicateNamedElement,
			fact.PredicateDefinition,
			fact.PredicateReference,
			fact.PredicateCall,
			fact.PredicateDependency,
			fact.PredicateConfiguration,
			fact.PredicateEndpoint,
			fact.PredicateMembership,
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
