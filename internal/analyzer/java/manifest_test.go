package java

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestManifestDeclaresTheBoundedJavaQuarkusFrontend(t *testing.T) {
	manifest := Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Manifest().Validate() error = %v", err)
	}
	if manifest.ManifestVersion != fact.FrontendManifestVersion {
		t.Fatalf("manifest version = %q, want %q", manifest.ManifestVersion, fact.FrontendManifestVersion)
	}
	if manifest.ID != AnalyzerID || manifest.Version != AnalyzerVersion || manifest.Method != AnalyzerMethod {
		t.Fatalf("manifest identity = %q/%q/%q, want %q/%q/%q", manifest.ID, manifest.Version, manifest.Method, AnalyzerID, AnalyzerVersion, AnalyzerMethod)
	}
	if manifest.Execution != fact.ExecutionProfileSafeStatic {
		t.Fatalf("execution profile = %q, want %q", manifest.Execution, fact.ExecutionProfileSafeStatic)
	}
	if !sameJavaStrings(manifest.SourceTypes, []string{"filesystem"}) {
		t.Fatalf("source types = %#v, want filesystem", manifest.SourceTypes)
	}
	if !sameJavaStrings(manifest.Families, []string{"java", "quarkus"}) {
		t.Fatalf("families = %#v, want fixture/corpus families", manifest.Families)
	}
	if !sameJavaStrings(manifest.Versions, []string{"java-21", "quarkus-3.26.4"}) {
		t.Fatalf("versions = %#v, want fixture/corpus versions", manifest.Versions)
	}

	wantCapabilities := []contract.Dimension{
		contract.DimensionLandscapeInventoryStructure,
		contract.DimensionEntitiesAndRelationships,
		contract.DimensionFlowsAndDependencies,
		contract.DimensionConfigurationVariations,
	}
	if !reflect.DeepEqual(manifest.Capabilities, wantCapabilities) {
		t.Fatalf("capabilities = %#v, want %#v", manifest.Capabilities, wantCapabilities)
	}
	wantPredicates := []fact.Predicate{
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
	}
	if !reflect.DeepEqual(manifest.Predicates, wantPredicates) {
		t.Fatalf("predicates = %#v, want %#v", manifest.Predicates, wantPredicates)
	}
	wantLimitations := []string{"lexical-only", "no-build-resolution", "no-runtime-semantics"}
	if !reflect.DeepEqual(manifest.Limitations, wantLimitations) {
		t.Fatalf("limitations = %#v, want %#v", manifest.Limitations, wantLimitations)
	}
	for _, limitation := range manifest.Limitations {
		if strings.Contains(strings.ToLower(limitation), "complete") {
			t.Fatalf("limitation %q unexpectedly indicates completeness", limitation)
		}
	}
	if manifest.Fallback {
		t.Fatal("Java manifest must not be a generic fallback")
	}
}

func TestManifestReturnsIndependentCopies(t *testing.T) {
	first := Manifest()
	first.SourceTypes[0] = "mutated-source"
	first.Families[0] = "mutated-family"
	first.Versions[0] = "mutated-version"
	first.Capabilities[0] = contract.DimensionDocumentation
	first.Limitations[0] = "mutated-limitation"
	first.Predicates[0] = fact.PredicateMessage

	second := Manifest()
	want := Manifest()
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("Manifest() shares mutable declarations: got %#v, want %#v", second, want)
	}
}

func TestManifestIsCompatibleWithJavaNormalizers(t *testing.T) {
	registrations, err := NormalizerRegistrations(Manifest())
	if err != nil {
		t.Fatalf("NormalizerRegistrations(Manifest()) error = %v", err)
	}
	if len(registrations) != 8 {
		t.Fatalf("registration count = %d, want 8", len(registrations))
	}
	for _, registration := range registrations {
		if registration.FrontendID != AnalyzerID || registration.FrontendVersion != AnalyzerVersion || registration.FrontendMethod != AnalyzerMethod {
			t.Fatalf("registration identity = %#v, want production manifest identity", registration)
		}
	}
}

func TestManifestSelectionIsExactButIncomplete(t *testing.T) {
	selection, err := fact.SelectFrontend([]fact.FrontendManifest{Manifest()}, "filesystem", "java", "java-21")
	if err != nil {
		t.Fatalf("SelectFrontend(exact Java/Quarkus) error = %v", err)
	}
	if selection.Status != fact.FrontendSelectionSupported || selection.Manifest == nil {
		t.Fatalf("selection = %#v, want supported manifest", selection)
	}
	if selection.Coverage != contract.CoverageIncomplete {
		t.Fatalf("selection coverage = %q, want %q", selection.Coverage, contract.CoverageIncomplete)
	}
	if selection.Fallback || selection.Manifest.Fallback {
		t.Fatalf("exact selection unexpectedly used fallback: %#v", selection)
	}
	if selection.Manifest.ID != AnalyzerID || selection.Manifest.Method != AnalyzerMethod {
		t.Fatalf("selected manifest identity = %#v, want Java production manifest", selection.Manifest)
	}
}

func TestManifestSelectionRejectsUnknownFamilyOrVersionExplicitly(t *testing.T) {
	tests := []struct {
		name    string
		family  string
		version string
		wantGap string
	}{
		{name: "unknown family", family: "kotlin", version: "java-21", wantGap: "requested_family_not_recognized"},
		{name: "unknown version", family: "java", version: "java-22", wantGap: "requested_version_not_recognized"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := fact.SelectFrontend([]fact.FrontendManifest{Manifest()}, "filesystem", test.family, test.version)
			if err != nil {
				t.Fatalf("SelectFrontend() error = %v", err)
			}
			if selection.Status != fact.FrontendSelectionUnsupported || selection.Manifest != nil {
				t.Fatalf("selection = %#v, want unsupported without manifest", selection)
			}
			if selection.Coverage != contract.CoverageNotSupported {
				t.Fatalf("selection coverage = %q, want %q", selection.Coverage, contract.CoverageNotSupported)
			}
			if !containsJavaString(selection.Limitations, test.wantGap) {
				t.Fatalf("selection limitations = %#v, want %q", selection.Limitations, test.wantGap)
			}
		})
	}
}

func sameJavaStrings(left, right []string) bool {
	return reflect.DeepEqual(left, right)
}
