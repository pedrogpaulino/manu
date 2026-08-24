package wso2

import (
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestManifestValidatesAndMatchesNormalizer(t *testing.T) {
	manifest := Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Manifest().Validate() error: %v", err)
	}
	if manifest.ID != AnalyzerID || manifest.Version != AnalyzerVersion || manifest.Method != AnalyzerMethod {
		t.Fatalf("manifest identity = %q/%q/%q, want %q/%q/%q", manifest.ID, manifest.Version, manifest.Method, AnalyzerID, AnalyzerVersion, AnalyzerMethod)
	}
	if manifest.Execution != fact.ExecutionProfileSafeStatic {
		t.Fatalf("manifest execution = %q, want %q", manifest.Execution, fact.ExecutionProfileSafeStatic)
	}

	registrations, err := NormalizerRegistrations(manifest)
	if err != nil {
		t.Fatalf("NormalizerRegistrations(Manifest()) error: %v", err)
	}
	if len(registrations) != 6 {
		t.Fatalf("normalizer registration count = %d, want 6", len(registrations))
	}
	for _, registration := range registrations {
		if registration.FrontendID != manifest.ID || registration.FrontendVersion != manifest.Version || registration.FrontendMethod != manifest.Method {
			t.Fatalf("registration %q has identity %q/%q/%q, want manifest identity", registration.ContributionType, registration.FrontendID, registration.FrontendVersion, registration.FrontendMethod)
		}
	}
}

func TestManifestReturnsIndependentCopies(t *testing.T) {
	first := Manifest()
	first.SourceTypes[0] = "mutated-source"
	first.Families[0] = "mutated-family"
	first.Versions[0] = "mutated-version"
	first.Capabilities[0] = contract.DimensionDocumentation
	first.Limitations[0] = "mutated-limitation"
	first.Predicates[0] = fact.PredicateArtifact

	second := Manifest()
	if second.SourceTypes[0] == "mutated-source" || second.Families[0] == "mutated-family" || second.Versions[0] == "mutated-version" {
		t.Fatalf("Manifest() reused mutable source identity slices: %#v", second)
	}
	if second.Capabilities[0] == contract.DimensionDocumentation || second.Limitations[0] == "mutated-limitation" || second.Predicates[0] == fact.PredicateArtifact {
		t.Fatalf("Manifest() reused mutable capability slices: %#v", second)
	}
}

func TestManifestSelectionIsSupportedButCoverageIncomplete(t *testing.T) {
	manifest := Manifest()
	selection, err := fact.SelectFrontend([]fact.FrontendManifest{manifest}, "filesystem", "wso2", ManifestSourceVersion)
	if err != nil {
		t.Fatalf("SelectFrontend() error: %v", err)
	}
	if selection.Status != fact.FrontendSelectionSupported || selection.Manifest == nil || selection.Manifest.ID != manifest.ID {
		t.Fatalf("selection = %#v, want supported WSO2 manifest", selection)
	}
	if selection.Coverage != contract.CoverageIncomplete {
		t.Fatalf("selection coverage = %q, want %q", selection.Coverage, contract.CoverageIncomplete)
	}
	if selection.Fallback || !selection.SourceTypeRecognized || !selection.FamilyRecognized || !selection.VersionRecognized {
		t.Fatalf("selection recognition = fallback:%t source:%t family:%t version:%t", selection.Fallback, selection.SourceTypeRecognized, selection.FamilyRecognized, selection.VersionRecognized)
	}
}

func TestManifestUnknownVersionIsUnsupportedAndNeverComplete(t *testing.T) {
	manifest := Manifest()
	selection, err := fact.SelectFrontend([]fact.FrontendManifest{manifest}, "filesystem", "wso2", "runtime-unknown")
	if err != nil {
		t.Fatalf("SelectFrontend() error: %v", err)
	}
	if selection.Status != fact.FrontendSelectionUnsupported || selection.Manifest != nil {
		t.Fatalf("unknown-version selection = %#v, want unsupported without manifest", selection)
	}
	if selection.Coverage != contract.CoverageNotSupported {
		t.Fatalf("unknown-version coverage = %q, want %q", selection.Coverage, contract.CoverageNotSupported)
	}
	if !selection.SourceTypeRecognized || !selection.FamilyRecognized || selection.VersionRecognized {
		t.Fatalf("unknown-version recognition = source:%t family:%t version:%t", selection.SourceTypeRecognized, selection.FamilyRecognized, selection.VersionRecognized)
	}
	if !containsManifestLimitation(selection.Limitations, "requested_version_not_recognized") {
		t.Fatalf("unknown-version limitations = %#v, want requested version limitation", selection.Limitations)
	}
}

func containsManifestLimitation(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
