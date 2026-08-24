package python

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/normalization"
)

func TestPythonNormalizerRegistrationsDeclareOnlySafeStaticMappings(t *testing.T) {
	manifest := pythonManifest()
	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations() error = %v", err)
	}
	if len(registrations) != 5 {
		t.Fatalf("registration count = %d, want five", len(registrations))
	}
	seen := make(map[string]bool, len(registrations))
	for _, registration := range registrations {
		if registration.FrontendID != manifest.ID || registration.FrontendVersion != manifest.Version || registration.FrontendMethod != manifest.Method {
			t.Fatalf("registration identity = %#v, want manifest identity", registration)
		}
		if seen[registration.ContributionType] {
			t.Fatalf("duplicate registration %q", registration.ContributionType)
		}
		seen[registration.ContributionType] = true
	}
	for _, contributionType := range []string{
		ArtifactContributionType,
		SymbolContributionType,
		ImportContributionType,
		RelationContributionType,
		ConfigurationContributionType,
	} {
		if !seen[contributionType] {
			t.Fatalf("missing registration for %q", contributionType)
		}
	}
}

func TestPythonNormalizationMapsContributionsToFactsWithEvidence(t *testing.T) {
	registry := pythonRegistry(t, pythonManifest())
	tests := []struct {
		name         string
		contribution string
		value        string
		predicates   []fact.Predicate
		dimension    contract.Dimension
	}{
		{
			name:         "artifact",
			contribution: ArtifactContributionType,
			value:        `{"path":"doctype.py","type":"python"}`,
			predicates:   []fact.Predicate{fact.PredicateArtifact},
			dimension:    contract.DimensionLandscapeInventoryStructure,
		},
		{
			name:         "symbol",
			contribution: SymbolContributionType,
			value:        `{"kind":"function","name":"load_status","qualified_name":"SalesOrder.load_status","signature":"(self,name)"}`,
			predicates:   []fact.Predicate{fact.PredicateSymbol, fact.PredicateDefinition},
			dimension:    contract.DimensionEntitiesAndRelationships,
		},
		{
			name:         "import",
			contribution: ImportContributionType,
			value:        `{"module":"frappe.model.document","name":"Document","alias":"Doc"}`,
			predicates:   []fact.Predicate{fact.PredicateReference, fact.PredicateDependency},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "relation",
			contribution: RelationContributionType,
			value:        `{"kind":"frappe_call","callee":"frappe.get_doc","target":"Sales Order","source_symbol":"SalesOrder.load_status"}`,
			predicates:   []fact.Predicate{fact.PredicateReference},
			dimension:    contract.DimensionFlowsAndDependencies,
		},
		{
			name:         "configuration",
			contribution: ConfigurationContributionType,
			value:        `{"key":"ERP_MODE","kind":"frappe.conf.get","path":"doctype.py"}`,
			predicates:   []fact.Predicate{fact.PredicateConfiguration},
			dimension:    contract.DimensionConfigurationVariations,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := pythonInput(test.contribution, "observed:"+test.name, json.RawMessage(test.value))
			output, err := registry.Normalize(context.Background(), input)
			if err != nil {
				t.Fatalf("Normalize() error = %v", err)
			}
			if len(output.Facts) != len(test.predicates) || len(output.Coverage) != 1 {
				t.Fatalf("output counts = facts:%d coverage:%d, want facts:%d and one coverage", len(output.Facts), len(output.Coverage), len(test.predicates))
			}
			coverage := output.Coverage[0]
			if coverage.Dimension != string(test.dimension) || coverage.Scope != input.Contribution.ID || coverage.State != contract.CoverageProduced || coverage.AnalyzerID != input.Manifest.ID {
				t.Fatalf("coverage = %#v, want produced %q coverage", coverage, test.dimension)
			}
			if coverage.ID != contract.CoverageID(coverage.Dimension, coverage.Scope, coverage.State, coverage.AnalyzerID) {
				t.Fatalf("coverage ID = %q is not deterministic", coverage.ID)
			}

			gotPredicates := make([]fact.Predicate, 0, len(output.Facts))
			for _, candidate := range output.Facts {
				if err := candidate.Validate(); err != nil {
					t.Fatalf("fact.Validate() error = %v, fact = %#v", err, candidate)
				}
				wantProducer := fact.Producer{ID: input.Manifest.ID, Version: input.Manifest.Version, Method: input.Manifest.Method}
				if candidate.Scope != input.Scope || candidate.Producer != wantProducer {
					t.Fatalf("fact provenance = %#v, want scope and producer %#v", candidate, wantProducer)
				}
				if !reflect.DeepEqual(candidate.Evidence, input.Evidence) {
					t.Fatalf("fact evidence = %#v, want %#v", candidate.Evidence, input.Evidence)
				}
				if candidate.Lineage != nil {
					t.Fatalf("observed fact unexpectedly carries lineage: %#v", candidate.Lineage)
				}
				gotPredicates = append(gotPredicates, candidate.Predicate)
			}
			if !samePythonPredicates(gotPredicates, test.predicates) {
				t.Fatalf("predicates = %#v, want %#v", gotPredicates, test.predicates)
			}

			switch test.contribution {
			case SymbolContributionType:
				for _, candidate := range output.Facts {
					if candidate.Predicate == fact.PredicateDefinition && (candidate.Object == nil || candidate.Object.Kind != fact.ParticipantArtifact || candidate.Object.ID != input.Contribution.ArtifactID) {
						t.Fatalf("definition = %#v, want symbol -> artifact", candidate)
					}
				}
			case ImportContributionType:
				for _, candidate := range output.Facts {
					if candidate.Subject.Kind != fact.ParticipantArtifact || candidate.Object == nil || candidate.Object.Kind != fact.ParticipantSymbol {
						t.Fatalf("import fact = %#v, want artifact -> lexical symbol", candidate)
					}
				}
			case RelationContributionType:
				for _, candidate := range output.Facts {
					if candidate.Subject.Kind != fact.ParticipantSymbol || candidate.Object == nil || candidate.Object.Kind != fact.ParticipantNamedElement {
						t.Fatalf("relation fact = %#v, want symbol -> named element", candidate)
					}
					if qualifierString(candidate, "source_symbol") != "SalesOrder.load_status" || qualifierString(candidate, "target") != "Sales Order" {
						t.Fatalf("relation qualifiers = %#v, want source and target qualifiers", candidate.Qualifiers)
					}
				}
			case ConfigurationContributionType:
				candidate := output.Facts[0]
				if candidate.Value == nil || candidate.Value.Kind != fact.ValueString || candidate.Value.String != "ERP_MODE" {
					t.Fatalf("configuration value = %#v, want key only", candidate.Value)
				}
				if qualifierString(candidate, "kind") != "frappe.conf.get" || qualifierString(candidate, "path") != "doctype.py" {
					t.Fatalf("configuration qualifiers = %#v, want kind/path only", candidate.Qualifiers)
				}
			}

			encoded, err := json.Marshal(output)
			if err != nil {
				t.Fatalf("json.Marshal(output) error = %v", err)
			}
			if strings.Contains(string(encoded), "sensitive") || strings.Contains(string(encoded), "secret") {
				t.Fatalf("normalized output leaked unsupported payload data: %s", encoded)
			}
		})
	}
}

func TestPythonNormalizationUsesManifestProducerNotObservationMethod(t *testing.T) {
	manifest := pythonManifest()
	registry := pythonRegistry(t, manifest)
	input := pythonInput(SymbolContributionType, "symbol:observed:line:42", json.RawMessage(`{"kind":"function","name":"run","qualified_name":"run"}`))
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(output.Facts) != 2 {
		t.Fatalf("facts = %d, want symbol and definition", len(output.Facts))
	}
	for _, candidate := range output.Facts {
		if candidate.Producer.Method != manifest.Method {
			t.Fatalf("producer method = %q, want manifest method %q", candidate.Producer.Method, manifest.Method)
		}
	}
}

func TestPythonNormalizationRequiresEvidenceAndDoesNotRetainPayload(t *testing.T) {
	registry := pythonRegistry(t, pythonManifest())
	input := pythonInput(RelationContributionType, "relation:without-evidence", json.RawMessage(`{"kind":"frappe_call","callee":"frappe.get_doc","target":"secret-target"}`))
	input.Evidence = nil
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(output.Facts) != 0 || len(output.Coverage) != 1 {
		t.Fatalf("output = %#v, want no facts and one incomplete coverage", output)
	}
	coverage := output.Coverage[0]
	if coverage.State != contract.CoverageIncomplete || coverage.Message != MissingEvidenceCoverageMessage || coverage.Scope != input.Contribution.ID {
		t.Fatalf("coverage = %#v, want missing-evidence incomplete coverage", coverage)
	}
	if strings.Contains(coverage.Message, "secret-target") {
		t.Fatal("missing-evidence coverage leaked contribution payload")
	}
}

func TestPythonNormalizationRejectsIncompatibleManifestsAndMalformedPayloads(t *testing.T) {
	valid := pythonManifest()
	mutations := []struct {
		name   string
		mutate func(*fact.FrontendManifest)
	}{
		{name: "frontend id", mutate: func(manifest *fact.FrontendManifest) { manifest.ID = "other" }},
		{name: "frontend version", mutate: func(manifest *fact.FrontendManifest) { manifest.Version = "2" }},
		{name: "frontend method", mutate: func(manifest *fact.FrontendManifest) { manifest.Method = "other" }},
		{name: "execution profile", mutate: func(manifest *fact.FrontendManifest) { manifest.Execution = fact.ExecutionProfileSemanticIsolated }},
	}
	for _, predicate := range pythonRequiredPredicates {
		predicate := predicate
		mutations = append(mutations, struct {
			name   string
			mutate func(*fact.FrontendManifest)
		}{name: "predicate " + string(predicate), mutate: func(manifest *fact.FrontendManifest) {
			manifest.Predicates = removePythonPredicate(manifest.Predicates, predicate)
		}})
	}
	for _, dimension := range pythonRequiredDimensions {
		dimension := dimension
		mutations = append(mutations, struct {
			name   string
			mutate func(*fact.FrontendManifest)
		}{name: "dimension " + string(dimension), mutate: func(manifest *fact.FrontendManifest) {
			manifest.Capabilities = removePythonDimension(manifest.Capabilities, dimension)
		}})
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := valid
			mutation.mutate(&candidate)
			if _, err := NormalizerRegistrations(candidate); err == nil {
				t.Fatal("NormalizerRegistrations() accepted incompatible manifest")
			}
		})
	}

	registry := pythonRegistry(t, valid)
	malformed := []string{
		`{"kind":17,"name":"secret-symbol","qualified_name":"secret-symbol"}`,
		`{"kind":"function","name":"secret\n-symbol","qualified_name":"secret-symbol"}`,
		`{"module":"frappe","name":17}`,
		`{"kind":"frappe_call","callee":"frappe.get_doc","target":"secret\u0000target"}`,
		`{"key":"secret-key","kind":"property"}`,
	}
	contributionTypes := []string{SymbolContributionType, SymbolContributionType, ImportContributionType, RelationContributionType, ConfigurationContributionType}
	for index, value := range malformed {
		input := pythonInput(contributionTypes[index], "malformed", json.RawMessage(value))
		output, err := registry.Normalize(context.Background(), input)
		if !errors.Is(err, normalization.ErrNormalizationFailed) || !reflect.DeepEqual(output, normalization.Output{}) {
			t.Fatalf("malformed payload output/error = %#v/%v, want redacted failure and zero output", output, err)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("normalization error leaked payload: %v", err)
		}
	}
}

func TestPythonNormalizationIsDeterministicAndCancellationAware(t *testing.T) {
	registry := pythonRegistry(t, pythonManifest())
	input := pythonInput(SymbolContributionType, "symbol:deterministic", json.RawMessage(`{"kind":"function","name":"run","qualified_name":"SalesOrder.run","signature":"(self)"}`))
	first, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("first Normalize() error = %v", err)
	}
	second, err := registry.Normalize(context.Background(), input)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated normalization = %#v/%v, want identical output", second, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	output, err := registry.Normalize(canceled, input)
	if !errors.Is(err, normalization.ErrInvalidInput) || !reflect.DeepEqual(output, normalization.Output{}) {
		t.Fatalf("canceled normalization output/error = %#v/%v, want zero output and invalid input", output, err)
	}

	inputs := []normalization.Input{
		pythonInput(ArtifactContributionType, "artifact", json.RawMessage(`{"path":"doctype.py","type":"python"}`)),
		pythonInput(SymbolContributionType, "symbol", json.RawMessage(`{"kind":"class","name":"SalesOrder","qualified_name":"SalesOrder"}`)),
		pythonInput(ImportContributionType, "import", json.RawMessage(`{"module":"frappe"}`)),
		pythonInput(RelationContributionType, "relation", json.RawMessage(`{"kind":"frappe_call","callee":"frappe.get_doc","target":"Sales Order"}`)),
		pythonInput(ConfigurationContributionType, "configuration", json.RawMessage(`{"key":"ERP_MODE","kind":"frappe.conf.get","path":"doctype.py"}`)),
	}
	all, err := registry.NormalizeAll(context.Background(), inputs)
	if err != nil {
		t.Fatalf("NormalizeAll() error = %v", err)
	}
	reverseInputs := append([]normalization.Input(nil), inputs...)
	for left, right := 0, len(reverseInputs)-1; left < right; left, right = left+1, right-1 {
		reverseInputs[left], reverseInputs[right] = reverseInputs[right], reverseInputs[left]
	}
	reversed, err := registry.NormalizeAll(context.Background(), reverseInputs)
	if err != nil || !reflect.DeepEqual(all, reversed) {
		t.Fatalf("NormalizeAll() changed with input order: %#v/%v", reversed, err)
	}
}

func TestPythonNormalizerLeavesUnknownContributionOnFallback(t *testing.T) {
	registry := pythonRegistry(t, pythonManifest())
	input := pythonInput("python.unsupported", "unsupported", json.RawMessage(`{"secret":"not a universal fact"}`))
	output, err := registry.Normalize(context.Background(), input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(output.Facts) != 0 || len(output.Coverage) != len(input.Manifest.Capabilities) {
		t.Fatalf("fallback output = %#v, want no facts and manifest coverage", output)
	}
	for _, coverage := range output.Coverage {
		if coverage.State != contract.CoverageIncomplete {
			t.Fatalf("fallback coverage = %#v, want incomplete", coverage)
		}
	}
}

func pythonRegistry(t *testing.T, manifest fact.FrontendManifest) *normalization.Registry {
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

func pythonManifest() fact.FrontendManifest {
	return fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              AnalyzerID,
		Version:         AnalyzerVersion,
		Method:          AnalyzerMethod,
		SourceTypes:     []string{analysis.ArtifactTypePython},
		Families:        []string{"python", "frappe"},
		Versions:        []string{"python-3", "frappe-17"},
		Capabilities: []contract.Dimension{
			contract.DimensionLandscapeInventoryStructure,
			contract.DimensionEntitiesAndRelationships,
			contract.DimensionFlowsAndDependencies,
			contract.DimensionConfigurationVariations,
		},
		Limitations: []string{
			"lexical-only",
			"no-import-resolution",
			"no-runtime-execution",
			"no-build-or-dependency-installation",
		},
		Predicates: []fact.Predicate{
			fact.PredicateArtifact,
			fact.PredicateSymbol,
			fact.PredicateDefinition,
			fact.PredicateReference,
			fact.PredicateDependency,
			fact.PredicateConfiguration,
		},
		Execution: fact.ExecutionProfileSafeStatic,
	}
}

func pythonInput(contributionType, method string, value json.RawMessage) normalization.Input {
	scope := fact.Scope{OrganizationID: "organization-python", SourceID: "source-python", SnapshotID: "snapshot-python"}
	artifactID := "artifact-python-doctype"
	locator := contract.Locator{
		SourceID:   scope.SourceID,
		ArtifactID: artifactID,
		Path:       "doctype.py",
		StartLine:  9,
		EndLine:    9,
	}
	contribution := contract.Contribution{
		ID:              contract.ContributionID(artifactID, AnalyzerID, AnalyzerVersion, method),
		ArtifactID:      artifactID,
		AnalyzerID:      AnalyzerID,
		AnalyzerVersion: AnalyzerVersion,
		Method:          method,
		Type:            contributionType,
		Locator:         locator,
		Value:           value,
	}
	return normalization.Input{
		Scope:        scope,
		Manifest:     pythonManifest(),
		Contribution: contribution,
		Evidence: []fact.EvidenceRef{{
			ID:      "evidence-" + strings.ReplaceAll(method, ":", "-"),
			Locator: locator,
		}},
	}
}

func samePythonPredicates(left, right []fact.Predicate) bool {
	if len(left) != len(right) {
		return false
	}
	left = append([]fact.Predicate(nil), left...)
	right = append([]fact.Predicate(nil), right...)
	sort.Slice(left, func(i, j int) bool { return left[i] < left[j] })
	sort.Slice(right, func(i, j int) bool { return right[i] < right[j] })
	return reflect.DeepEqual(left, right)
}

func qualifierString(candidate fact.CanonicalFact, name string) string {
	for _, qualifier := range candidate.Qualifiers {
		if qualifier.Name == name && qualifier.Value.Kind == fact.ValueString {
			return qualifier.Value.String
		}
	}
	return ""
}

func removePythonPredicate(values []fact.Predicate, unwanted fact.Predicate) []fact.Predicate {
	result := make([]fact.Predicate, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}

func removePythonDimension(values []contract.Dimension, unwanted contract.Dimension) []contract.Dimension {
	result := make([]contract.Dimension, 0, len(values))
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}
