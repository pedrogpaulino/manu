package normalization_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

func TestRegistryDispatchesExactRegistrationAndFallsBack(t *testing.T) {
	t.Parallel()

	input := normalizationInput(t, "frontend-one")
	called := 0
	registry, err := normalization.NewRegistry(normalization.Registration{
		FrontendID:       input.Manifest.ID,
		FrontendVersion:  input.Manifest.Version,
		FrontendMethod:   input.Manifest.Method,
		ContributionType: input.Contribution.Type,
		Normalizer: normalization.NormalizerFunc(func(context.Context, normalization.Input) (normalization.Output, error) {
			called++
			return normalization.Output{}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	if _, err := registry.Normalize(context.Background(), input); err != nil {
		t.Fatalf("Normalize(exact) error = %v", err)
	}
	if called != 1 {
		t.Fatalf("normalizer calls = %d, want 1", called)
	}

	tests := []struct {
		name   string
		mutate func(*normalization.Input)
	}{
		{name: "different version", mutate: func(candidate *normalization.Input) {
			candidate.Manifest.Version = "2"
			candidate.Contribution.AnalyzerVersion = "2"
		}},
		{name: "different manifest method", mutate: func(candidate *normalization.Input) {
			candidate.Manifest.Method = "other"
		}},
		{name: "different contribution type", mutate: func(candidate *normalization.Input) { candidate.Contribution.Type = "other" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := normalizationInput(t, "frontend-one")
			tt.mutate(&candidate)
			fallback, err := registry.Normalize(context.Background(), candidate)
			if err != nil {
				t.Fatalf("Normalize(fallback) error = %v", err)
			}
			if len(fallback.Facts) != 0 || len(fallback.Coverage) != len(candidate.Manifest.Capabilities) {
				t.Fatalf("fallback output = %#v, want no facts and one coverage per capability", fallback)
			}
			for _, coverage := range fallback.Coverage {
				if coverage.State != contract.CoverageIncomplete || coverage.Scope != candidate.Contribution.ID || coverage.AnalyzerID != candidate.Manifest.ID {
					t.Fatalf("fallback coverage = %#v, want incomplete contribution coverage", coverage)
				}
			}
		})
	}
	if called != 1 {
		t.Fatalf("fallback dispatched normalizer %d times, want 1", called)
	}
}

func TestRegistryDispatchesContributionObservationMethodAndUsesManifestProducer(t *testing.T) {
	t.Parallel()

	input := normalizationInput(t, "frontend-observation-method")
	input.Contribution.Method = "type:definition"
	wantInput := input
	calledMethod := ""
	registry, err := normalization.NewRegistry(normalization.Registration{
		FrontendID:       input.Manifest.ID,
		FrontendVersion:  input.Manifest.Version,
		FrontendMethod:   input.Manifest.Method,
		ContributionType: input.Contribution.Type,
		Normalizer: normalization.NormalizerFunc(func(_ context.Context, received normalization.Input) (normalization.Output, error) {
			calledMethod = received.Contribution.Method
			return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, received, "method-aware", fact.PredicateDefinition)}}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if calledMethod != input.Contribution.Method {
		t.Fatalf("normalizer contribution method = %q, want %q", calledMethod, input.Contribution.Method)
	}
	if len(output.Facts) != 1 || output.Facts[0].Producer.Method != input.Manifest.Method {
		t.Fatalf("facts = %#v, want producer method %q", output.Facts, input.Manifest.Method)
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("Normalize() mutated contribution/input: got %#v, want %#v", input, wantInput)
	}
}

func TestRegistryFallbackPreservesExtensionsAndDetachesInput(t *testing.T) {
	t.Parallel()

	input := normalizationInputWithExtension(t, "frontend-fallback")
	wantInput := input
	wantInput.Extensions = cloneTestExtensions(input.Extensions)
	registry, err := normalization.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize(fallback) error = %v", err)
	}
	if !reflect.DeepEqual(output.Extensions, input.Extensions) {
		t.Fatalf("fallback extensions = %#v, want exact extension sequence %#v", output.Extensions, input.Extensions)
	}
	output.Extensions[0].Payload[0] = 'X'
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("Normalize() mutated input: got %#v, want %#v", input, wantInput)
	}
}

func TestRegistryNormalizerAddsOutputAndSortsDetachedSlices(t *testing.T) {
	t.Parallel()

	input := normalizationInputWithExtension(t, "frontend-normalizer")
	first := normalizationFact(t, input, "subject-a", fact.PredicateDefinition)
	second := normalizationFact(t, input, "subject-b", fact.PredicateReference)
	coverageOne := normalizationCoverage(input, contract.DimensionDocumentation)
	coverageTwo := normalizationCoverage(input, contract.DimensionEntitiesAndRelationships)
	extraExtension := input.Extensions[0]
	extraExtension.Payload = json.RawMessage(`{"kind":"extra"}`)
	registry, err := normalization.NewRegistry(normalization.Registration{
		FrontendID:       input.Manifest.ID,
		FrontendVersion:  input.Manifest.Version,
		FrontendMethod:   input.Manifest.Method,
		ContributionType: input.Contribution.Type,
		Normalizer: normalization.NormalizerFunc(func(_ context.Context, received normalization.Input) (normalization.Output, error) {
			if len(received.Extensions) != len(input.Extensions) {
				return normalization.Output{}, fmt.Errorf("input extensions were not preserved")
			}
			return normalization.Output{
				Facts:      []fact.CanonicalFact{second, first},
				Extensions: []bundle.ExtensionRecord{extraExtension},
				Coverage:   []contract.Coverage{coverageTwo, coverageOne},
			}, nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(output.Facts) != 2 || output.Facts[0].ID > output.Facts[1].ID {
		t.Fatalf("facts are not sorted by identity: %#v", output.Facts)
	}
	if len(output.Coverage) != 2 || output.Coverage[0].ID > output.Coverage[1].ID {
		t.Fatalf("coverage is not sorted by identity: %#v", output.Coverage)
	}
	if len(output.Extensions) != len(input.Extensions)+1 || !reflect.DeepEqual(output.Extensions[0], input.Extensions[0]) {
		t.Fatalf("extensions were not preserved additively: %#v", output.Extensions)
	}
}

func TestRegistryRejectsInvalidNormalizerOutputWithoutPartialResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output func(*testing.T, normalization.Input) normalization.Output
	}{
		{
			name: "producer",
			output: func(t *testing.T, input normalization.Input) normalization.Output {
				candidate := normalizationFact(t, input, "subject", fact.PredicateDefinition)
				candidate.Producer.ID = "other-frontend"
				candidate.ID = mustFactID(t, candidate)
				return normalization.Output{Facts: []fact.CanonicalFact{candidate}}
			},
		},
		{
			name: "predicate",
			output: func(t *testing.T, input normalization.Input) normalization.Output {
				candidate := normalizationFact(t, input, "subject", fact.PredicateDefinition)
				candidate.Predicate = fact.PredicateCall
				candidate.ID = mustFactID(t, candidate)
				return normalization.Output{Facts: []fact.CanonicalFact{candidate}}
			},
		},
		{
			name: "evidence",
			output: func(t *testing.T, input normalization.Input) normalization.Output {
				candidate := normalizationFact(t, input, "subject", fact.PredicateDefinition)
				candidate.Evidence[0].ID = "evidence-unknown"
				return normalization.Output{Facts: []fact.CanonicalFact{candidate}}
			},
		},
		{
			name: "coverage",
			output: func(_ *testing.T, input normalization.Input) normalization.Output {
				coverage := normalizationCoverage(input, contract.DimensionDocumentation)
				coverage.ID = "coverage-wrong"
				return normalization.Output{Coverage: []contract.Coverage{coverage}}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := normalizationInput(t, "frontend-invalid-output")
			registry, err := normalization.NewRegistry(normalization.Registration{
				FrontendID:       input.Manifest.ID,
				FrontendVersion:  input.Manifest.Version,
				FrontendMethod:   input.Manifest.Method,
				ContributionType: input.Contribution.Type,
				Normalizer: normalization.NormalizerFunc(func(_ context.Context, received normalization.Input) (normalization.Output, error) {
					return tt.output(t, received), nil
				}),
			})
			if err != nil {
				t.Fatalf("NewRegistry() error = %v", err)
			}
			output, err := registry.Normalize(context.Background(), input)
			if !errors.Is(err, normalization.ErrInvalidOutput) || !reflect.DeepEqual(output, normalization.Output{}) {
				t.Fatalf("Normalize() output/error = %#v/%v, want zero output and invalid output", output, err)
			}
		})
	}
}

func TestRegistryNormalizersCannotLeakFailureOrMutateInput(t *testing.T) {
	t.Parallel()

	input := normalizationInputWithExtension(t, "frontend-failure")
	wantInput := input
	wantInput.Extensions = cloneTestExtensions(input.Extensions)
	secret := "normalizer-secret-payload"
	registry, err := normalization.NewRegistry(normalization.Registration{
		FrontendID:       input.Manifest.ID,
		FrontendVersion:  input.Manifest.Version,
		FrontendMethod:   input.Manifest.Method,
		ContributionType: input.Contribution.Type,
		Normalizer: normalization.NormalizerFunc(func(_ context.Context, received normalization.Input) (normalization.Output, error) {
			received.Extensions[0].Payload[0] = 'X'
			return normalization.Output{}, errors.New(secret)
		}),
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.Normalize(context.Background(), input)
	if !errors.Is(err, normalization.ErrNormalizationFailed) || strings.Contains(err.Error(), secret) || !reflect.DeepEqual(output, normalization.Output{}) {
		t.Fatalf("failure output/error = %#v/%v, want redacted failure and zero output", output, err)
	}
	if !reflect.DeepEqual(input, wantInput) {
		t.Fatalf("normalizer mutated caller input: got %#v, want %#v", input, wantInput)
	}
}

func TestRegistryRejectsDuplicateAndTypedNilRegistrations(t *testing.T) {
	t.Parallel()

	input := normalizationInput(t, "frontend-registration")
	registration := normalization.Registration{
		FrontendID:       input.Manifest.ID,
		FrontendVersion:  input.Manifest.Version,
		FrontendMethod:   input.Manifest.Method,
		ContributionType: input.Contribution.Type,
		Normalizer: normalization.NormalizerFunc(func(context.Context, normalization.Input) (normalization.Output, error) {
			return normalization.Output{}, nil
		}),
	}
	if _, err := normalization.NewRegistry(registration, registration); !errors.Is(err, normalization.ErrDuplicateRegistration) || !errors.Is(err, normalization.ErrInvalidRegistration) {
		t.Fatalf("duplicate registration error = %v, want duplicate/invalid registration", err)
	}
	var nilNormalizer normalization.NormalizerFunc
	registration.Normalizer = nilNormalizer
	if _, err := normalization.NewRegistry(registration); !errors.Is(err, normalization.ErrInvalidRegistration) {
		t.Fatalf("typed nil registration error = %v, want invalid registration", err)
	}
}

func TestRegistryIsSafeForConcurrentReadsAndDistinguishesProducers(t *testing.T) {
	t.Parallel()

	firstInput := normalizationInput(t, "frontend-one")
	secondInput := normalizationInput(t, "frontend-two")
	registry, err := normalization.NewRegistry(
		normalization.Registration{
			FrontendID:       firstInput.Manifest.ID,
			FrontendVersion:  firstInput.Manifest.Version,
			FrontendMethod:   firstInput.Manifest.Method,
			ContributionType: firstInput.Contribution.Type,
			Normalizer: normalization.NormalizerFunc(func(_ context.Context, input normalization.Input) (normalization.Output, error) {
				return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "shared", fact.PredicateDefinition)}}, nil
			}),
		},
		normalization.Registration{
			FrontendID:       secondInput.Manifest.ID,
			FrontendVersion:  secondInput.Manifest.Version,
			FrontendMethod:   secondInput.Manifest.Method,
			ContributionType: secondInput.Contribution.Type,
			Normalizer: normalization.NormalizerFunc(func(_ context.Context, input normalization.Input) (normalization.Output, error) {
				return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "shared", fact.PredicateDefinition)}}, nil
			}),
		},
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	firstOutput, err := registry.Normalize(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("Normalize(first) error = %v", err)
	}
	secondOutput, err := registry.Normalize(context.Background(), secondInput)
	if err != nil {
		t.Fatalf("Normalize(second) error = %v", err)
	}
	if len(firstOutput.Facts) != 1 || len(secondOutput.Facts) != 1 || firstOutput.Facts[0].ID == secondOutput.Facts[0].ID {
		t.Fatalf("producer identities are not distinguished: %#v/%#v", firstOutput.Facts, secondOutput.Facts)
	}

	const workers = 32
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			_, normalizeErr := registry.Normalize(context.Background(), firstInput)
			if normalizeErr != nil {
				errorsChannel <- normalizeErr
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for normalizeErr := range errorsChannel {
		t.Errorf("concurrent Normalize() error = %v", normalizeErr)
	}
}

func TestRegistryRejectsCanceledContext(t *testing.T) {
	t.Parallel()

	input := normalizationInput(t, "frontend-canceled")
	registry, err := normalization.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := registry.Normalize(ctx, input)
	if !errors.Is(err, normalization.ErrInvalidInput) || !reflect.DeepEqual(output, normalization.Output{}) {
		t.Fatalf("canceled Normalize() output/error = %#v/%v, want invalid input and zero output", output, err)
	}
}

func normalizationInput(t *testing.T, frontendID string) normalization.Input {
	t.Helper()
	scope := fact.Scope{OrganizationID: "organization-one", SourceID: "source-one", SnapshotID: "snapshot-one"}
	manifest := fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              frontendID,
		Version:         "1",
		Method:          "symbols",
		SourceTypes:     []string{"filesystem"},
		Families:        []string{"go"},
		Versions:        []string{"1"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships, contract.DimensionDocumentation},
		Predicates:      []fact.Predicate{fact.PredicateDefinition, fact.PredicateReference},
		Execution:       fact.ExecutionProfileSafeStatic,
	}
	contribution := contract.Contribution{
		ID:              "contribution-one",
		ArtifactID:      "artifact-one",
		AnalyzerID:      manifest.ID,
		AnalyzerVersion: manifest.Version,
		Method:          manifest.Method,
		Type:            "symbols",
		Locator: contract.Locator{
			SourceID:   scope.SourceID,
			ArtifactID: "artifact-one",
			Path:       "main.go",
		},
	}
	contribution.Locator.ArtifactID = contribution.ArtifactID
	evidence := fact.EvidenceRef{
		ID: "evidence-one",
		Locator: contract.Locator{
			SourceID:   scope.SourceID,
			ArtifactID: contribution.ArtifactID,
			Path:       "main.go",
			StartLine:  1,
			EndLine:    1,
		},
	}
	return normalization.Input{
		Scope:        scope,
		Manifest:     manifest,
		Contribution: contribution,
		Evidence:     []fact.EvidenceRef{evidence},
	}
}

func normalizationInputWithExtension(t *testing.T, frontendID string) normalization.Input {
	t.Helper()
	input := normalizationInput(t, frontendID)
	schema := json.RawMessage(`{"type":"object"}`)
	digest := fact.ExtensionDigest(schema)
	input.Manifest.Extensions = []fact.ExtensionSchema{{ID: "schema-one", Version: "1", Digest: digest}}
	input.Extensions = []bundle.ExtensionRecord{{
		SchemaID:      "schema-one",
		SchemaVersion: "1",
		SchemaDigest:  digest,
		Schema:        schema,
		Payload:       json.RawMessage(`{"kind":"one"}`),
	}}
	return input
}

func normalizationFact(t *testing.T, input normalization.Input, subjectID string, predicate fact.Predicate) fact.CanonicalFact {
	t.Helper()
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     input.Scope,
		Predicate: predicate,
		Subject:   fact.Participant{Kind: fact.ParticipantSymbol, ID: subjectID},
		Producer:  fact.Producer{ID: input.Manifest.ID, Version: input.Manifest.Version, Method: input.Manifest.Method},
		Evidence:  append([]fact.EvidenceRef(nil), input.Evidence...),
	}
	candidate.ID = mustFactID(t, candidate)
	return candidate
}

func normalizationCoverage(input normalization.Input, dimension contract.Dimension) contract.Coverage {
	coverage := contract.Coverage{
		Dimension:  string(dimension),
		Scope:      input.Contribution.ID,
		State:      contract.CoverageProduced,
		AnalyzerID: input.Manifest.ID,
	}
	coverage.ID = contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID)
	return coverage
}

func mustFactID(t *testing.T, candidate fact.CanonicalFact) string {
	t.Helper()
	id, err := fact.FactID(candidate)
	if err != nil {
		t.Fatalf("FactID() error = %v", err)
	}
	return id
}

func cloneTestExtensions(extensions []bundle.ExtensionRecord) []bundle.ExtensionRecord {
	if extensions == nil {
		return nil
	}
	cloned := make([]bundle.ExtensionRecord, len(extensions))
	for index, extension := range extensions {
		cloned[index] = extension
		cloned[index].Schema = append([]byte(nil), extension.Schema...)
		cloned[index].Payload = append([]byte(nil), extension.Payload...)
	}
	return cloned
}
