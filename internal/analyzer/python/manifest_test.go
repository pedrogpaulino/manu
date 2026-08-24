package python

import (
	"reflect"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestManifestIsValidAndDeclaresOnlyProducedSafeStaticSurface(t *testing.T) {
	manifest := Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Manifest().Validate() error = %v", err)
	}

	if manifest.ID != AnalyzerID || manifest.Version != AnalyzerVersion || manifest.Method != AnalyzerMethod {
		t.Fatalf("manifest identity = %q/%q/%q, want analyzer identity %q/%q/%q", manifest.ID, manifest.Version, manifest.Method, AnalyzerID, AnalyzerVersion, AnalyzerMethod)
	}
	if !reflect.DeepEqual(manifest.SourceTypes, []string{"filesystem"}) {
		t.Fatalf("manifest source types = %#v, want filesystem only", manifest.SourceTypes)
	}
	if !reflect.DeepEqual(manifest.Families, []string{"python", "frappe"}) {
		t.Fatalf("manifest families = %#v, want tested Python/Frappe families", manifest.Families)
	}
	if !reflect.DeepEqual(manifest.Versions, []string{"python-3", "frappe-17"}) {
		t.Fatalf("manifest versions = %#v, want tested Python 3/Frappe 17 versions", manifest.Versions)
	}
	if !reflect.DeepEqual(manifest.Capabilities, []contract.Dimension{
		contract.DimensionLandscapeInventoryStructure,
		contract.DimensionEntitiesAndRelationships,
		contract.DimensionFlowsAndDependencies,
		contract.DimensionConfigurationVariations,
	}) {
		t.Fatalf("manifest capabilities = %#v, want only produced dimensions", manifest.Capabilities)
	}
	if !reflect.DeepEqual(manifest.Predicates, []fact.Predicate{
		fact.PredicateArtifact,
		fact.PredicateSymbol,
		fact.PredicateDefinition,
		fact.PredicateReference,
		fact.PredicateDependency,
		fact.PredicateConfiguration,
	}) {
		t.Fatalf("manifest predicates = %#v, want only produced predicates", manifest.Predicates)
	}
	if manifest.Execution != fact.ExecutionProfileSafeStatic {
		t.Fatalf("manifest execution = %q, want safe-static", manifest.Execution)
	}
	if manifest.Fallback {
		t.Fatal("Python manifest must not be a fallback")
	}

	for _, limitation := range []string{
		"lexical-only",
		"no-import-resolution",
		"no-build-or-dependency-installation",
		"no-runtime-execution",
		"no-site-access",
		"no-orm-semantics",
	} {
		if !containsPythonManifestString(manifest.Limitations, limitation) {
			t.Fatalf("manifest limitations = %#v, want %q", manifest.Limitations, limitation)
		}
	}
}

func TestManifestReturnsIndependentCopies(t *testing.T) {
	first := Manifest()
	second := Manifest()

	first.SourceTypes[0] = "changed-source"
	first.Families[0] = "changed-family"
	first.Versions[0] = "changed-version"
	first.Capabilities[0] = contract.DimensionCapabilities
	first.Limitations[0] = "changed-limitation"
	first.Predicates[0] = fact.PredicateEndpoint

	if got := Manifest(); !reflect.DeepEqual(got, second) {
		t.Fatalf("Manifest() returned state affected by a prior mutation:\n got %#v\nwant %#v", got, second)
	}
}

func TestManifestIsAcceptedByPythonNormalizers(t *testing.T) {
	registrations, err := NormalizerRegistrations(Manifest())
	if err != nil {
		t.Fatalf("NormalizerRegistrations(Manifest()) error = %v", err)
	}
	if len(registrations) != 5 {
		t.Fatalf("normalizer registration count = %d, want five", len(registrations))
	}
	for _, registration := range registrations {
		if registration.FrontendID != AnalyzerID || registration.FrontendVersion != AnalyzerVersion || registration.FrontendMethod != AnalyzerMethod {
			t.Fatalf("registration identity = %#v, want canonical Python identity", registration)
		}
	}
}

func TestManifestSelectionKeepsCoverageIncompleteAndLimitsUnknownRequests(t *testing.T) {
	manifests := []fact.FrontendManifest{Manifest()}

	supported, err := fact.SelectFrontend(manifests, "filesystem", "frappe", "frappe-17")
	if err != nil {
		t.Fatalf("SelectFrontend(supported) error = %v", err)
	}
	if supported.Status != fact.FrontendSelectionSupported || supported.Manifest == nil {
		t.Fatalf("supported selection = %#v, want supported manifest", supported)
	}
	if supported.Coverage != contract.CoverageIncomplete || supported.Fallback {
		t.Fatalf("supported selection coverage/fallback = %q/%v, want incomplete/non-fallback", supported.Coverage, supported.Fallback)
	}

	unknownFamily, err := fact.SelectFrontend(manifests, "filesystem", "django", "frappe-17")
	if err != nil {
		t.Fatalf("SelectFrontend(unknown family) error = %v", err)
	}
	if unknownFamily.Status != fact.FrontendSelectionUnsupported || unknownFamily.Manifest != nil || unknownFamily.Coverage != contract.CoverageNotSupported {
		t.Fatalf("unknown-family selection = %#v, want unsupported/not-supported", unknownFamily)
	}
	if !containsPythonManifestString(unknownFamily.Limitations, "requested_family_not_recognized") {
		t.Fatalf("unknown-family limitations = %#v, want explicit family limitation", unknownFamily.Limitations)
	}

	unknownVersion, err := fact.SelectFrontend(manifests, "filesystem", "frappe", "frappe-18")
	if err != nil {
		t.Fatalf("SelectFrontend(unknown version) error = %v", err)
	}
	if unknownVersion.Status != fact.FrontendSelectionUnsupported || unknownVersion.Manifest != nil || unknownVersion.Coverage != contract.CoverageNotSupported {
		t.Fatalf("unknown-version selection = %#v, want unsupported/not-supported", unknownVersion)
	}
	if !containsPythonManifestString(unknownVersion.Limitations, "requested_version_not_recognized") {
		t.Fatalf("unknown-version limitations = %#v, want explicit version limitation", unknownVersion.Limitations)
	}
}

func containsPythonManifestString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
