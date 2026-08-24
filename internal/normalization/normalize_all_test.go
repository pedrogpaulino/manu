package normalization_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

func TestRegistryNormalizeAllComposesDistinctProducers(t *testing.T) {
	t.Parallel()

	language := normalizationInput(t, "frontend-language")
	language.Contribution.ID = "contribution-language"
	language.Contribution.Type = "language"
	framework := normalizationInput(t, "frontend-framework")
	framework.Contribution.ID = "contribution-framework"
	framework.Contribution.Type = "framework"

	registry, err := normalization.NewRegistry(
		normalizationRegistration(language, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "shared-endpoint", fact.PredicateDefinition)}}
		}),
		normalizationRegistration(framework, func(input normalization.Input) normalization.Output {
			return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "shared-endpoint", fact.PredicateDefinition)}}
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{framework, language})
	if err != nil {
		t.Fatalf("NormalizeAll() error = %v", err)
	}
	if len(output.Facts) != 2 || output.Facts[0].ID == output.Facts[1].ID {
		t.Fatalf("facts = %#v, want two distinct producer identities", output.Facts)
	}
	if output.Facts[0].ID > output.Facts[1].ID {
		t.Fatalf("facts are not globally sorted: %#v", output.Facts)
	}
}

func TestRegistryNormalizeAllPreservesFallbackExtensionsAdditively(t *testing.T) {
	t.Parallel()

	fallback := normalizationInputWithExtension(t, "frontend-fallback-all")
	fallback.Contribution.ID = "contribution-fallback"
	fallback.Contribution.Type = "fallback"
	mapped := normalizationInput(t, "frontend-mapped-all")
	mapped.Contribution.ID = "contribution-mapped"
	mapped.Contribution.Type = "mapped"
	inputSnapshot := fallback
	inputSnapshot.Extensions = cloneTestExtensions(fallback.Extensions)

	registry, err := normalization.NewRegistry(normalizationRegistration(mapped, func(input normalization.Input) normalization.Output {
		return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "mapped-subject", fact.PredicateDefinition)}}
	}))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{mapped, fallback})
	if err != nil {
		t.Fatalf("NormalizeAll() error = %v", err)
	}
	if len(output.Facts) != 1 || len(output.Extensions) != len(fallback.Extensions) {
		t.Fatalf("output = %#v, want mapped fact and fallback extension", output)
	}
	if !reflect.DeepEqual(output.Extensions[0], fallback.Extensions[0]) {
		t.Fatalf("fallback extension = %#v, want %#v", output.Extensions[0], fallback.Extensions[0])
	}
	if len(output.Coverage) != len(fallback.Manifest.Capabilities) {
		t.Fatalf("coverage count = %d, want %d fallback entries", len(output.Coverage), len(fallback.Manifest.Capabilities))
	}
	for _, coverage := range output.Coverage {
		if coverage.State != contract.CoverageIncomplete || coverage.Scope != fallback.Contribution.ID {
			t.Fatalf("coverage = %#v, want incomplete fallback coverage", coverage)
		}
	}
	output.Extensions[0].Payload[0] = 'X'
	if !reflect.DeepEqual(fallback, inputSnapshot) {
		t.Fatalf("NormalizeAll() mutated fallback input: got %#v, want %#v", fallback, inputSnapshot)
	}
}

func TestRegistryNormalizeAllIsPermutationInvariant(t *testing.T) {
	t.Parallel()

	first := normalizationInputWithExtension(t, "frontend-permutation-a")
	first.Contribution.ID = "contribution-a"
	first.Contribution.Type = "a"
	second := normalizationInputWithExtension(t, "frontend-permutation-b")
	second.Contribution.ID = "contribution-b"
	second.Contribution.Type = "b"
	registry, err := normalization.NewRegistry(
		normalizationRegistration(first, func(input normalization.Input) normalization.Output {
			extra := input.Extensions[0]
			extra.Payload = []byte(`{"source":"a"}`)
			return normalization.Output{
				Facts:      []fact.CanonicalFact{normalizationFact(t, input, "a", fact.PredicateDefinition)},
				Extensions: []bundle.ExtensionRecord{extra},
			}
		}),
		normalizationRegistration(second, func(input normalization.Input) normalization.Output {
			extra := input.Extensions[0]
			extra.Payload = []byte(`{"source":"b"}`)
			return normalization.Output{
				Facts:      []fact.CanonicalFact{normalizationFact(t, input, "b", fact.PredicateReference)},
				Extensions: []bundle.ExtensionRecord{extra},
			}
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
		t.Fatalf("permuted inputs changed output:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
}

func TestRegistryNormalizeAllRejectsFailureOrInvalidOutputWithoutPartialResult(t *testing.T) {
	t.Parallel()

	first := normalizationInput(t, "frontend-all-a")
	first.Contribution.ID = "contribution-a"
	first.Contribution.Type = "a"
	middle := normalizationInput(t, "frontend-all-b")
	middle.Contribution.ID = "contribution-b"
	middle.Contribution.Type = "b"
	last := normalizationInput(t, "frontend-all-c")
	last.Contribution.ID = "contribution-c"
	last.Contribution.Type = "c"

	called := make([]string, 0, 3)
	var callsMu sync.Mutex
	registrationFor := func(input normalization.Input, output normalization.NormalizerFunc) normalization.Registration {
		return normalization.Registration{
			FrontendID:       input.Manifest.ID,
			FrontendVersion:  input.Manifest.Version,
			FrontendMethod:   input.Manifest.Method,
			ContributionType: input.Contribution.Type,
			Normalizer: normalization.NormalizerFunc(func(ctx context.Context, received normalization.Input) (normalization.Output, error) {
				callsMu.Lock()
				called = append(called, received.Manifest.ID)
				callsMu.Unlock()
				return output(ctx, received)
			}),
		}
	}
	registry, err := normalization.NewRegistry(
		registrationFor(first, func(_ context.Context, input normalization.Input) (normalization.Output, error) {
			return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "first", fact.PredicateDefinition)}}, nil
		}),
		registrationFor(middle, func(_ context.Context, _ normalization.Input) (normalization.Output, error) {
			return normalization.Output{}, errors.New("sensitive normalizer failure")
		}),
		registrationFor(last, func(_ context.Context, input normalization.Input) (normalization.Output, error) {
			return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "last", fact.PredicateDefinition)}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{last, first, middle})
	if !errors.Is(err, normalization.ErrNormalizationFailed) || !reflect.DeepEqual(output, normalization.Output{}) || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("failure output/error = %#v/%v, want redacted zero result", output, err)
	}
	if !reflect.DeepEqual(called, []string{"frontend-all-a", "frontend-all-b"}) {
		t.Fatalf("normalizer call order = %#v, want sorted prefix", called)
	}

	invalidRegistry, err := normalization.NewRegistry(normalizationRegistration(first, func(input normalization.Input) normalization.Output {
		candidate := normalizationFact(t, input, "invalid", fact.PredicateDefinition)
		candidate.Producer.ID = "other"
		candidate.ID = mustFactID(t, candidate)
		return normalization.Output{Facts: []fact.CanonicalFact{candidate}}
	}))
	if err != nil {
		t.Fatalf("NewRegistry(invalid) error = %v", err)
	}
	invalidOutput, err := invalidRegistry.NormalizeAll(context.Background(), []normalization.Input{first})
	if !errors.Is(err, normalization.ErrInvalidOutput) || !reflect.DeepEqual(invalidOutput, normalization.Output{}) {
		t.Fatalf("invalid output/error = %#v/%v, want invalid output and zero result", invalidOutput, err)
	}
}

func TestRegistryNormalizeAllValidatesEverythingBeforeDispatch(t *testing.T) {
	t.Parallel()

	valid := normalizationInput(t, "frontend-validate-all")
	valid.Contribution.Type = "valid"
	invalid := normalizationInput(t, "frontend-invalid-all")
	invalid.Contribution.Type = "invalid"
	invalid.Manifest.ID = ""
	calls := 0
	registry, err := normalization.NewRegistry(normalizationRegistration(valid, func(input normalization.Input) normalization.Output {
		calls++
		return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "never", fact.PredicateDefinition)}}
	}))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{valid, invalid})
	if !errors.Is(err, normalization.ErrInvalidInput) || !reflect.DeepEqual(output, normalization.Output{}) || calls != 0 {
		t.Fatalf("output/error/calls = %#v/%v/%d, want zero, invalid input, and no dispatch", output, err, calls)
	}
}

func TestRegistryNormalizeAllRejectsMixedScopesAndDuplicateIdentities(t *testing.T) {
	t.Parallel()

	first := normalizationInput(t, "frontend-scope-a")
	first.Contribution.Type = "scope"
	second := normalizationInput(t, "frontend-scope-b")
	second.Contribution.Type = "scope"
	second.Scope.SourceID = "source-two"
	second.Contribution.Locator.SourceID = second.Scope.SourceID
	second.Evidence[0].Locator.SourceID = second.Scope.SourceID
	registry, err := normalization.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	output, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, second})
	if !errors.Is(err, normalization.ErrInvalidInput) || !reflect.DeepEqual(output, normalization.Output{}) {
		t.Fatalf("mixed-scope output/error = %#v/%v, want invalid zero result", output, err)
	}

	duplicate, err := registry.NormalizeAll(context.Background(), []normalization.Input{first, first})
	if !errors.Is(err, normalization.ErrInvalidOutput) || !reflect.DeepEqual(duplicate, normalization.Output{}) {
		t.Fatalf("duplicate coverage output/error = %#v/%v, want invalid zero result", duplicate, err)
	}

	factRegistry, err := normalization.NewRegistry(normalizationRegistration(first, func(input normalization.Input) normalization.Output {
		return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "same", fact.PredicateDefinition)}}
	}))
	if err != nil {
		t.Fatalf("NewRegistry(facts) error = %v", err)
	}
	first.Contribution.ID = "first-fact"
	secondFact := first
	secondFact.Contribution.ID = "second-fact"
	duplicateFacts, err := factRegistry.NormalizeAll(context.Background(), []normalization.Input{first, secondFact})
	if !errors.Is(err, normalization.ErrInvalidOutput) || !reflect.DeepEqual(duplicateFacts, normalization.Output{}) {
		t.Fatalf("duplicate fact output/error = %#v/%v, want invalid zero result", duplicateFacts, err)
	}
}

func TestRegistryNormalizeAllHandlesEmptyCanceledAndConcurrentCalls(t *testing.T) {
	t.Parallel()

	input := normalizationInput(t, "frontend-concurrent-all")
	input.Contribution.Type = "concurrent"
	registry, err := normalization.NewRegistry(normalizationRegistration(input, func(input normalization.Input) normalization.Output {
		return normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "stable", fact.PredicateDefinition)}}
	}))
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	empty, err := registry.NormalizeAll(context.Background(), nil)
	if err != nil || !reflect.DeepEqual(empty, normalization.Output{}) {
		t.Fatalf("empty output/error = %#v/%v, want zero/nil", empty, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := registry.NormalizeAll(canceled, []normalization.Input{input})
	if !errors.Is(err, normalization.ErrInvalidInput) || !reflect.DeepEqual(output, normalization.Output{}) {
		t.Fatalf("canceled output/error = %#v/%v, want invalid zero result", output, err)
	}

	const workers = 32
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			got, normalizeErr := registry.NormalizeAll(context.Background(), []normalization.Input{input})
			want := normalization.Output{Facts: []fact.CanonicalFact{normalizationFact(t, input, "stable", fact.PredicateDefinition)}}
			if normalizeErr != nil || !reflect.DeepEqual(got, want) {
				errorsChannel <- errors.New("concurrent NormalizeAll result mismatch")
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for normalizeErr := range errorsChannel {
		t.Errorf("concurrent NormalizeAll() error = %v", normalizeErr)
	}
}

func normalizationRegistration(input normalization.Input, produce func(normalization.Input) normalization.Output) normalization.Registration {
	return normalization.Registration{
		FrontendID:       input.Manifest.ID,
		FrontendVersion:  input.Manifest.Version,
		FrontendMethod:   input.Manifest.Method,
		ContributionType: input.Contribution.Type,
		Normalizer: normalization.NormalizerFunc(func(_ context.Context, received normalization.Input) (normalization.Output, error) {
			return produce(received), nil
		}),
	}
}
