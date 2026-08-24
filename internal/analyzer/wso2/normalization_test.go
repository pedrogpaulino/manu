package wso2

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

func TestWSO2NormalizerRegistrationsMapDeclaredContributions(t *testing.T) {
	manifest := wso2TestManifest()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error = %v", err)
	}
	if len(registrations) != 6 {
		t.Fatalf("registration count = %d, want six", len(registrations))
	}
	seen := make(map[string]bool, len(registrations))
	for _, registration := range registrations {
		if registration.FrontendID != AnalyzerID || registration.FrontendVersion != AnalyzerVersion || registration.FrontendMethod != AnalyzerMethod {
			t.Fatalf("registration identity = %#v, want WSO2 manifest identity", registration)
		}
		if seen[registration.ContributionType] {
			t.Fatalf("duplicate contribution registration %q", registration.ContributionType)
		}
		seen[registration.ContributionType] = true
	}
	for _, contributionType := range []string{
		wso2TypeContribution,
		wso2IncludeContribution,
		wso2ReferenceContribution,
		wso2EndpointContribution,
		wso2MessageContribution,
		wso2ConfigurationContribution,
	} {
		if !seen[contributionType] {
			t.Fatalf("missing registration for %q", contributionType)
		}
	}
}

func TestWSO2NormalizationMapsAllContributionsThroughRegistry(t *testing.T) {
	registry := wso2TestRegistry(t)
	tests := []struct {
		name         string
		contribution string
		payload      string
		predicates   []fact.Predicate
		dimension    contract.Dimension
	}{
		{
			name:         "type and membership",
			contribution: wso2TypeContribution,
			payload:      `{"kind":"proxy","name":"BookingProxy","path":"proxy"}`,
			predicates:   []fact.Predicate{fact.PredicateMembership, fact.PredicateNamedElement},
			dimension:    contract.DimensionEntitiesAndRelationships,
		},
		{
			name:         "include dependency",
			contribution: wso2IncludeContribution,
			payload:      `{"kind":"location","target":"conf/booking.xml","path":"proxy/import"}`,
			predicates:   []fact.Predicate{fact.PredicateDependency},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "generic reference",
			contribution: wso2ReferenceContribution,
			payload:      `{"kind":"ref","target":"bookingEndpoint","path":"proxy/@ref"}`,
			predicates:   []fact.Predicate{fact.PredicateReference},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "structural reference",
			contribution: wso2ReferenceContribution,
			payload:      `{"kind":"targetEndpoint","target":"bookingEndpoint","path":"proxy/@targetEndpoint"}`,
			predicates:   []fact.Predicate{fact.PredicateDependency, fact.PredicateReference},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "endpoint",
			contribution: wso2EndpointContribution,
			payload:      `{"kind":"http","name":"booking","path":"/bookings","context":"api","uri":"https://example.test/bookings","media_type":"application/json"}`,
			predicates:   []fact.Predicate{fact.PredicateEndpoint},
			dimension:    contract.DimensionEntitiesAndRelationships,
		},
		{
			name:         "message metadata",
			contribution: wso2MessageContribution,
			payload:      `{"kind":"request","name":"booking","context":"api","path":"/bookings","media_type":"application/json","body":"never normalize this","template":"also excluded"}`,
			predicates:   []fact.Predicate{fact.PredicateMessage},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "configuration",
			contribution: wso2ConfigurationContribution,
			payload:      `{"key":"booking.endpoint","kind":"property","value":"secret-value"}`,
			predicates:   []fact.Predicate{fact.PredicateConfiguration},
			dimension:    contract.DimensionConfigurationVariations,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := wso2TestInput(test.contribution, test.name, test.payload, "")
			output, err := registry.Normalize(context.Background(), input)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(output.Facts) != len(test.predicates) || len(output.Coverage) != 1 {
				t.Fatalf("output counts = facts:%d coverage:%d, want facts:%d and one coverage", len(output.Facts), len(output.Coverage), len(test.predicates))
			}
			gotPredicates := make([]fact.Predicate, 0, len(output.Facts))
			for _, candidate := range output.Facts {
				if err := candidate.Validate(); err != nil {
					t.Fatalf("fact.Validate() error = %v, fact = %#v", err, candidate)
				}
				if candidate.Version != fact.Version || candidate.Scope != input.Scope || candidate.Producer != (fact.Producer{ID: AnalyzerID, Version: AnalyzerVersion, Method: AnalyzerMethod}) {
					t.Fatalf("fact provenance = %#v, want input scope and WSO2 producer", candidate)
				}
				if !reflect.DeepEqual(candidate.Evidence, input.Evidence) {
					t.Fatalf("fact evidence = %#v, want %#v", candidate.Evidence, input.Evidence)
				}
				gotPredicates = append(gotPredicates, candidate.Predicate)
			}
			if !sameWSO2Predicates(gotPredicates, test.predicates) {
				t.Fatalf("predicates = %#v, want %#v", gotPredicates, test.predicates)
			}
			coverage := output.Coverage[0]
			if coverage.Dimension != string(test.dimension) || coverage.Scope != input.Contribution.ID || coverage.State != contract.CoverageProduced || coverage.AnalyzerID != AnalyzerID || coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
				t.Fatalf("coverage = %#v, want produced coverage for %q", coverage, test.dimension)
			}
			encoded, err := json.Marshal(output)
			if err != nil {
				t.Fatalf("json.Marshal(output) error = %v", err)
			}
			if strings.Contains(string(encoded), "secret-value") || strings.Contains(string(encoded), "never normalize") {
				t.Fatalf("unsupported payload material leaked into output: %s", encoded)
			}
		})
	}
}

func TestWSO2NormalizationUsesMemberContainerAndPreservesEvidenceLocator(t *testing.T) {
	registry := wso2TestRegistry(t)
	input := wso2TestInput(wso2TypeContribution, "member-type", `{"kind":"proxy","name":"BookingProxy","path":"proxy","member":"apis/booking.xml"}`, "apis/booking.xml")
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(output.Facts) != 2 {
		t.Fatalf("facts = %d, want named element plus membership", len(output.Facts))
	}
	var membership fact.CanonicalFact
	for _, candidate := range output.Facts {
		if candidate.Predicate == fact.PredicateMembership {
			membership = candidate
		}
	}
	if membership.Subject.Kind != fact.ParticipantNamedElement || membership.Subject.ID != wso2Identity("member", input.Contribution.ArtifactID, "apis/booking.xml") {
		t.Fatalf("membership subject = %#v, want deterministic CAR member", membership.Subject)
	}
	if membership.Object == nil || membership.Object.Kind != fact.ParticipantNamedElement {
		t.Fatalf("membership object = %#v, want named element", membership.Object)
	}
	if membership.Evidence[0].Locator.Member != "apis/booking.xml" {
		t.Fatalf("evidence locator member = %q, want CAR member preserved", membership.Evidence[0].Locator.Member)
	}

	standalone := wso2TestInput(wso2TypeContribution, "standalone-type", `{"kind":"proxy","name":"BookingProxy","path":"proxy"}`, "")
	standaloneOutput, err := registry.Normalize(context.Background(), standalone)
	if err != nil {
		t.Fatalf("Normalize(standalone) error = %v", err)
	}
	for _, candidate := range standaloneOutput.Facts {
		if candidate.Predicate == fact.PredicateMembership && (candidate.Subject.Kind != fact.ParticipantArtifact || candidate.Subject.ID != standalone.Contribution.ArtifactID) {
			t.Fatalf("standalone membership subject = %#v, want artifact participant", candidate.Subject)
		}
	}
}

func TestWSO2IncludeCorrelatesCARMemberTarget(t *testing.T) {
	registry := wso2TestRegistry(t)
	include := wso2TestInput(wso2IncludeContribution, "include-shared", `{"kind":"include","target":"./synapse//shared-v1.xml","path":"api"}`, "synapse/api-v1.xml")
	sharedType := wso2TestInput(wso2TypeContribution, "shared-type", `{"kind":"sequence","name":"sharedSequence","path":"sequence"}`, "synapse/shared-v1.xml")

	includeOutput, err := registry.Normalize(context.Background(), include)
	if err != nil {
		t.Fatalf("Normalize(include) error = %v", err)
	}
	sharedOutput, err := registry.Normalize(context.Background(), sharedType)
	if err != nil {
		t.Fatalf("Normalize(shared type) error = %v", err)
	}

	var dependency fact.CanonicalFact
	for _, candidate := range includeOutput.Facts {
		if candidate.Predicate == fact.PredicateDependency {
			dependency = candidate
		}
	}
	if dependency.Object == nil {
		t.Fatalf("include dependency = %#v, want target participant", dependency)
	}
	wantMember := wso2Container(sharedType, "synapse/shared-v1.xml")
	if dependency.Object.Kind != wantMember.Kind || dependency.Object.ID != wantMember.ID {
		t.Fatalf("include target = %#v, want member container %#v", dependency.Object, wantMember)
	}
	if target := qualifierString(dependency, "target"); target != "./synapse//shared-v1.xml" {
		t.Fatalf("target qualifier = %q, want original safe literal preserved", target)
	}

	var membership fact.CanonicalFact
	for _, candidate := range sharedOutput.Facts {
		if candidate.Predicate == fact.PredicateMembership {
			membership = candidate
		}
	}
	if membership.Subject.Kind != wantMember.Kind || membership.Subject.ID != wantMember.ID {
		t.Fatalf("shared membership subject = %#v, want member container %#v", membership.Subject, wantMember)
	}
}

func TestWSO2IncludeKeepsUncorrelatableCARTargetsLiteral(t *testing.T) {
	registry := wso2TestRegistry(t)
	for _, target := range []string{"/synapse/shared-v1.xml", "../shared-v1.xml", "synapse/../../shared-v1.xml", "https://example.test/shared-v1.xml"} {
		t.Run(target, func(t *testing.T) {
			input := wso2TestInput(wso2IncludeContribution, "include-"+strings.ReplaceAll(target, "/", "-"), `{"kind":"include","target":"`+target+`","path":"api"}`, "synapse/api-v1.xml")
			output, err := registry.Normalize(context.Background(), input)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(output.Facts) != 1 || output.Facts[0].Object == nil {
				t.Fatalf("output = %#v, want one conservative literal dependency", output)
			}
			want := wso2Identity("literal", target)
			if output.Facts[0].Object.ID != want {
				t.Fatalf("target identity = %q, want literal identity %q", output.Facts[0].Object.ID, want)
			}
			if got := qualifierString(output.Facts[0], "target"); got != target {
				t.Fatalf("target qualifier = %q, want %q", got, target)
			}
		})
	}
}

func TestWSO2NormalizationReturnsIncompleteCoverageForMissingEvidenceOrUnsafeLiterals(t *testing.T) {
	registry := wso2TestRegistry(t)
	tests := []struct {
		name         string
		contribution string
		payload      string
	}{
		{name: "missing evidence", contribution: wso2TypeContribution, payload: `{"kind":"proxy","name":"BookingProxy","path":"proxy"}`},
		{name: "redacted include", contribution: wso2IncludeContribution, payload: `{"kind":"location","target":"[redacted]","redacted":true}`},
		{name: "dynamic reference", contribution: wso2ReferenceContribution, payload: `{"kind":"ref","target":"${ctx.target}"}`},
		{name: "empty include", contribution: wso2IncludeContribution, payload: `{"kind":"location"}`},
		{name: "redacted endpoint", contribution: wso2EndpointContribution, payload: `{"kind":"http","path":"/bookings","uri":"https://user:password@example.test/bookings","redacted":true}`},
		{name: "dynamic configuration", contribution: wso2ConfigurationContribution, payload: `{"key":"${ctx.key}","kind":"property"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := wso2TestInput(test.contribution, test.name, test.payload, "")
			if test.name == "missing evidence" {
				input.Evidence = nil
			}
			output, err := registry.Normalize(context.Background(), input)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(output.Facts) != 0 || len(output.Coverage) != 1 {
				t.Fatalf("output = %#v, want zero facts and one coverage", output)
			}
			coverage := output.Coverage[0]
			if coverage.State != contract.CoverageIncomplete || coverage.Scope != input.Contribution.ID || coverage.AnalyzerID != AnalyzerID || coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
				t.Fatalf("coverage = %#v, want deterministic incomplete WSO2 coverage", coverage)
			}
			encoded, marshalErr := json.Marshal(output)
			if marshalErr != nil {
				t.Fatalf("json.Marshal(output) error = %v", marshalErr)
			}
			if strings.Contains(string(encoded), "[redacted]") || strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "ctx.target") || strings.Contains(string(encoded), "ctx.key") {
				t.Fatalf("unsafe literal leaked into output: %s", encoded)
			}
		})
	}
}

func TestWSO2NormalizationRejectsTypedPayloadAndIncompatibleManifestSafely(t *testing.T) {
	registry := wso2TestRegistry(t)
	input := wso2TestInput(wso2TypeContribution, "typed-payload", `{"kind":17,"name":"sensitive-symbol","path":"proxy"}`, "")
	output, err := registry.Normalize(context.Background(), input)
	if !errors.Is(err, normalization.ErrNormalizationFailed) || !reflect.DeepEqual(output, normalization.Output{}) || strings.Contains(err.Error(), "sensitive-symbol") {
		t.Fatalf("malformed payload output/error = %#v/%v, want safe failure and zero output", output, err)
	}

	mutations := []func(*fact.FrontendManifest){
		func(manifest *fact.FrontendManifest) { manifest.ID = "other" },
		func(manifest *fact.FrontendManifest) { manifest.Version = "2" },
		func(manifest *fact.FrontendManifest) { manifest.Method = "other" },
	}
	for _, predicate := range wso2RequiredPredicates {
		predicate := predicate
		mutations = append(mutations, func(manifest *fact.FrontendManifest) {
			filtered := make([]fact.Predicate, 0, len(manifest.Predicates))
			for _, declared := range manifest.Predicates {
				if declared != predicate {
					filtered = append(filtered, declared)
				}
			}
			manifest.Predicates = filtered
		})
	}
	for _, dimension := range wso2RequiredDimensions {
		dimension := dimension
		mutations = append(mutations, func(manifest *fact.FrontendManifest) {
			filtered := make([]contract.Dimension, 0, len(manifest.Capabilities))
			for _, declared := range manifest.Capabilities {
				if declared != dimension {
					filtered = append(filtered, declared)
				}
			}
			manifest.Capabilities = filtered
		})
	}
	for index, mutate := range mutations {
		candidate := wso2TestManifest()
		mutate(&candidate)
		if _, err := NormalizerRegistrations(candidate); err == nil {
			t.Fatalf("mutation %d accepted incompatible WSO2 manifest", index)
		}
	}
}

func TestWSO2NormalizationComposesXMLAndCARDeterministically(t *testing.T) {
	registry := wso2TestRegistry(t)
	inputs := []normalization.Input{
		wso2TestInput(wso2TypeContribution, "xml-type", `{"kind":"proxy","name":"BookingProxy","path":"proxy"}`, ""),
		wso2TestInput(wso2TypeContribution, "car-type-a", `{"kind":"sequence","name":"Inbound","path":"sequence","member":"a.xml"}`, "a.xml"),
		wso2TestInput(wso2TypeContribution, "car-type-b", `{"kind":"sequence","name":"Outbound","path":"sequence","member":"b.xml"}`, "b.xml"),
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
	if len(forward.Facts) != 6 || len(forward.Coverage) != len(inputs) {
		t.Fatalf("composed output counts = facts:%d coverage:%d, want six facts and %d coverages", len(forward.Facts), len(forward.Coverage), len(inputs))
	}
	for _, candidate := range forward.Facts {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("composed fact.Validate() error = %v", err)
		}
	}
}

func TestWSO2NormalizationDoesNotMutateInputAndIsConcurrent(t *testing.T) {
	registry := wso2TestRegistry(t)
	input := wso2TestInput(wso2EndpointContribution, "endpoint-concurrent", `{"kind":"http","name":"booking","path":"/bookings","uri":"https://example.test/bookings"}`, "")
	original := input
	want, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if !reflect.DeepEqual(input, original) {
		t.Fatalf("Normalize() mutated input: got %#v, want %#v", input, original)
	}

	const workers = 16
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, workers)
	for index := 0; index < workers; index++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			got, normalizeErr := registry.Normalize(context.Background(), input)
			if normalizeErr != nil {
				errorsChannel <- normalizeErr
				return
			}
			if !reflect.DeepEqual(got, want) {
				errorsChannel <- errors.New("concurrent normalization returned a different output")
			}
		}()
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func wso2TestRegistry(t *testing.T) *normalization.Registry {
	t.Helper()
	registrations, err := NormalizerRegistrations(wso2TestManifest())
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error = %v", err)
	}
	registry, err := normalization.NewRegistry(registrations...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	return registry
}

func wso2TestManifest() fact.FrontendManifest {
	return Manifest()
}

func wso2TestInput(contributionType, method, payload, member string) normalization.Input {
	scope := fact.Scope{OrganizationID: "organization-wso2", SourceID: "source-wso2", SnapshotID: "snapshot-wso2"}
	artifactID := "artifact-wso2"
	path := "conf/wso2.xml"
	if member != "" {
		path = "bundle.car"
	}
	locator := contract.Locator{
		SourceID:   scope.SourceID,
		ArtifactID: artifactID,
		Path:       path,
		Member:     member,
		StartLine:  4,
		EndLine:    4,
	}
	contribution := contract.Contribution{
		ID:              contract.ContributionID(artifactID, AnalyzerID, AnalyzerVersion, method),
		ArtifactID:      artifactID,
		AnalyzerID:      AnalyzerID,
		AnalyzerVersion: AnalyzerVersion,
		Method:          method,
		Type:            contributionType,
		Locator:         locator,
		Value:           json.RawMessage(payload),
	}
	return normalization.Input{
		Scope:        scope,
		Manifest:     wso2TestManifest(),
		Contribution: contribution,
		Evidence: []fact.EvidenceRef{{
			ID:      "evidence-" + strings.ReplaceAll(method, " ", "-"),
			Locator: locator,
		}},
	}
}

func sameWSO2Predicates(left, right []fact.Predicate) bool {
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

func qualifierString(candidate fact.CanonicalFact, name string) string {
	for _, qualifier := range candidate.Qualifiers {
		if qualifier.Name == name && qualifier.Value.Kind == fact.ValueString {
			return qualifier.Value.String
		}
	}
	return ""
}
