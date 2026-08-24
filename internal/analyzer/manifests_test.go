package analyzer

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestFrontendManifestsAreValidatedUniqueAndBounded(t *testing.T) {
	manifests, err := FrontendManifests()
	if err != nil {
		t.Fatalf("FrontendManifests() error = %v", err)
	}

	wantIDs := []string{"java", "python", "wso2"}
	if len(manifests) != len(wantIDs) {
		t.Fatalf("manifest count = %d, want %d", len(manifests), len(wantIDs))
	}
	seenIDs := make(map[string]struct{}, len(manifests))
	for index, manifest := range manifests {
		if manifest.ID != wantIDs[index] {
			t.Fatalf("manifest %d id = %q, want %q", index, manifest.ID, wantIDs[index])
		}
		if _, exists := seenIDs[manifest.ID]; exists {
			t.Fatalf("duplicate manifest id %q", manifest.ID)
		}
		seenIDs[manifest.ID] = struct{}{}
		if err := manifest.Validate(); err != nil {
			t.Fatalf("manifest %q validation error = %v", manifest.ID, err)
		}
		if !reflect.DeepEqual(manifest.SourceTypes, []string{"filesystem"}) {
			t.Fatalf("manifest %q source types = %#v, want filesystem", manifest.ID, manifest.SourceTypes)
		}
		if manifest.Execution != fact.ExecutionProfileSafeStatic {
			t.Fatalf("manifest %q execution = %q, want safe-static", manifest.ID, manifest.Execution)
		}
		if len(manifest.Limitations) == 0 {
			t.Fatalf("manifest %q has no limitations", manifest.ID)
		}
		for _, limitation := range manifest.Limitations {
			lower := strings.ToLower(limitation)
			if strings.Contains(lower, "complete") || strings.Contains(lower, "complet") {
				t.Fatalf("manifest %q limitation %q makes a completeness claim", manifest.ID, limitation)
			}
		}
		if len(manifest.Capabilities) == 0 {
			t.Fatalf("manifest %q has no capabilities", manifest.ID)
		}
		if len(manifest.Predicates) == 0 {
			t.Fatalf("manifest %q has no predicates", manifest.ID)
		}
	}
}

func TestFrontendManifestsSelectEachRecognizedFamilyAsIncomplete(t *testing.T) {
	manifests, err := FrontendManifests()
	if err != nil {
		t.Fatalf("FrontendManifests() error = %v", err)
	}

	tests := []struct {
		name    string
		family  string
		version string
	}{
		{name: "java", family: "java", version: "java-21"},
		{name: "python", family: "python", version: "python-3"},
		{name: "wso2", family: "wso2", version: "wso2-declarative-v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection, err := fact.SelectFrontend(manifests, "filesystem", test.family, test.version)
			if err != nil {
				t.Fatalf("SelectFrontend() error = %v", err)
			}
			if selection.Status != fact.FrontendSelectionSupported || selection.Manifest == nil {
				t.Fatalf("selection = %#v, want supported manifest", selection)
			}
			if selection.Coverage != contract.CoverageIncomplete {
				t.Fatalf("selection coverage = %q, want %q", selection.Coverage, contract.CoverageIncomplete)
			}
			if selection.Fallback || selection.Manifest.Fallback {
				t.Fatalf("selection unexpectedly used fallback: %#v", selection)
			}
		})
	}
}

func TestFrontendManifestsRejectUnknownFamilyAndVersion(t *testing.T) {
	manifests, err := FrontendManifests()
	if err != nil {
		t.Fatalf("FrontendManifests() error = %v", err)
	}

	selection, err := fact.SelectFrontend(manifests, "filesystem", "unknown-family", "unknown-version")
	if err != nil {
		t.Fatalf("SelectFrontend() error = %v", err)
	}
	if selection.Status != fact.FrontendSelectionUnsupported || selection.Manifest != nil {
		t.Fatalf("unknown selection = %#v, want unsupported without manifest", selection)
	}
	if selection.Coverage != contract.CoverageNotSupported {
		t.Fatalf("unknown selection coverage = %q, want %q", selection.Coverage, contract.CoverageNotSupported)
	}
	if selection.Fallback || selection.FamilyRecognized || selection.VersionRecognized {
		t.Fatalf("unknown selection recognition = fallback:%t family:%t version:%t", selection.Fallback, selection.FamilyRecognized, selection.VersionRecognized)
	}
}

func TestFrontendManifestsReturnIndependentCopies(t *testing.T) {
	first, err := FrontendManifests()
	if err != nil {
		t.Fatalf("first FrontendManifests() error = %v", err)
	}
	want, err := FrontendManifests()
	if err != nil {
		t.Fatalf("want FrontendManifests() error = %v", err)
	}
	mutateFrontendManifests(first)

	got, err := FrontendManifests()
	if err != nil {
		t.Fatalf("got FrontendManifests() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog shares mutable state after caller mutation:\n got %#v\nwant %#v", got, want)
	}
}

func TestFrontendManifestCanonicalBytesAndDigestsIgnoreCollectionOrder(t *testing.T) {
	manifests, err := FrontendManifests()
	if err != nil {
		t.Fatalf("FrontendManifests() error = %v", err)
	}
	for _, manifest := range manifests {
		t.Run(manifest.ID, func(t *testing.T) {
			originalBytes, err := fact.CanonicalFrontendManifestBytes(manifest)
			if err != nil {
				t.Fatalf("CanonicalFrontendManifestBytes() error = %v", err)
			}
			originalDigest, err := fact.FrontendManifestDigest(manifest)
			if err != nil {
				t.Fatalf("FrontendManifestDigest() error = %v", err)
			}

			reordered := cloneFrontendManifest(manifest)
			reverseStrings(reordered.SourceTypes)
			reverseStrings(reordered.Families)
			reverseStrings(reordered.Versions)
			reverseDimensions(reordered.Capabilities)
			reverseStrings(reordered.Limitations)
			reversePredicates(reordered.Predicates)
			reverseExtensions(reordered.Extensions)

			reorderedBytes, err := fact.CanonicalFrontendManifestBytes(reordered)
			if err != nil {
				t.Fatalf("CanonicalFrontendManifestBytes(reordered) error = %v", err)
			}
			reorderedDigest, err := fact.FrontendManifestDigest(reordered)
			if err != nil {
				t.Fatalf("FrontendManifestDigest(reordered) error = %v", err)
			}
			if !bytes.Equal(reorderedBytes, originalBytes) || reorderedDigest != originalDigest {
				t.Fatalf("canonical representation depends on declaration order: bytes=%q/%q digest=%q/%q", originalBytes, reorderedBytes, originalDigest, reorderedDigest)
			}

			repeatedBytes, err := fact.CanonicalFrontendManifestBytes(manifest)
			if err != nil {
				t.Fatalf("repeated CanonicalFrontendManifestBytes() error = %v", err)
			}
			repeatedDigest, err := fact.FrontendManifestDigest(manifest)
			if err != nil {
				t.Fatalf("repeated FrontendManifestDigest() error = %v", err)
			}
			if !bytes.Equal(repeatedBytes, originalBytes) || repeatedDigest != originalDigest {
				t.Fatalf("canonical representation is not deterministic")
			}
		})
	}
}

func mutateFrontendManifests(manifests []fact.FrontendManifest) {
	for index := range manifests {
		manifest := &manifests[index]
		manifest.SourceTypes[0] = "mutated-source"
		manifest.Families[0] = "mutated-family"
		manifest.Versions[0] = "mutated-version"
		manifest.Capabilities[0] = contract.DimensionDocumentation
		manifest.Limitations[0] = "mutated-limitation"
		manifest.Predicates[0] = fact.PredicateMessage
		if len(manifest.Extensions) > 0 {
			manifest.Extensions[0].ID = "mutated-extension"
		}
	}
}

func reverseStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseDimensions(values []contract.Dimension) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reversePredicates(values []fact.Predicate) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseExtensions(values []fact.ExtensionSchema) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
