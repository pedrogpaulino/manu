package fact_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestFrontendManifestValidateAndJSONRoundTrip(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"object","properties":{"name":{"type":"string"}}}`)
	manifest := validFrontendManifest(payload)
	if err := manifest.Validate(); err != nil {
		t.Fatalf("FrontendManifest.Validate() error = %v", err)
	}
	if err := manifest.Extensions[0].Verify(payload); err != nil {
		t.Fatalf("ExtensionSchema.Verify() error = %v", err)
	}

	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded fact.FrontendManifest
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("round-tripped manifest validation error = %v", err)
	}
	if decoded.ID != manifest.ID || decoded.Execution != manifest.Execution || len(decoded.Extensions) != 1 {
		t.Fatalf("round-tripped manifest differs: got %#v, want %#v", decoded, manifest)
	}
}

func TestFrontendManifestRejectsMalformedFields(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"schema":"v1"}`)
	tests := []struct {
		name   string
		mutate func(*fact.FrontendManifest)
	}{
		{
			name: "missing manifest version",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.ManifestVersion = ""
			},
		},
		{
			name: "missing identity",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.ID = ""
			},
		},
		{
			name: "missing frontend version",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Version = ""
			},
		},
		{
			name: "missing method",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Method = ""
			},
		},
		{
			name: "duplicate family",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Families = append(manifest.Families, manifest.Families[0])
			},
		},
		{
			name: "duplicate version",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Versions = append(manifest.Versions, manifest.Versions[0])
			},
		},
		{
			name: "missing capability",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Capabilities = nil
			},
		},
		{
			name: "empty capability dimension",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Capabilities[0] = contract.Dimension("")
			},
		},
		{
			name: "unknown predicate",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Predicates[0] = fact.Predicate("future")
			},
		},
		{
			name: "duplicate predicate",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Predicates = append(manifest.Predicates, manifest.Predicates[0])
			},
		},
		{
			name: "unknown execution profile",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Execution = fact.ExecutionProfile("future")
			},
		},
		{
			name: "malformed extension digest",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Extensions[0].Digest = "not-a-sha256"
			},
		},
		{
			name: "duplicate extension schema",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Extensions = append(manifest.Extensions, manifest.Extensions[0])
			},
		},
		{
			name: "duplicate limitation",
			mutate: func(manifest *fact.FrontendManifest) {
				manifest.Limitations = []string{"requires-isolation", "requires-isolation"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := validFrontendManifest(payload)
			tt.mutate(&manifest)
			if err := manifest.Validate(); !errors.Is(err, fact.ErrInvalidManifest) && !errors.Is(err, fact.ErrUnsupportedVersion) {
				t.Fatalf("FrontendManifest.Validate() error = %v, want manifest validation error", err)
			}
		})
	}
}

func TestExtensionSchemaVerifyRejectsChangedPayload(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"schema":"v1"}`)
	schema := fact.ExtensionSchema{ID: "extension.example", Version: "1", Digest: fact.ExtensionDigest(payload)}
	if err := schema.Verify([]byte(`{"schema":"v2"}`)); !errors.Is(err, fact.ErrInvalidExtensionSchema) {
		t.Fatalf("ExtensionSchema.Verify(changed payload) error = %v, want invalid schema", err)
	}
	if err := (fact.ExtensionSchema{ID: schema.ID, Version: schema.Version, Digest: "ABC"}).Validate(); !errors.Is(err, fact.ErrInvalidExtensionSchema) {
		t.Fatalf("ExtensionSchema.Validate(uppercase digest) error = %v, want invalid schema", err)
	}
}

func TestSelectFrontendSupportedDoesNotClaimCompleteCoverage(t *testing.T) {
	t.Parallel()

	manifest := validFrontendManifest(nil)
	selection, err := fact.SelectFrontend([]fact.FrontendManifest{manifest}, "repository", "jvm", "17")
	if err != nil {
		t.Fatalf("SelectFrontend() error = %v", err)
	}
	if selection.Status != fact.FrontendSelectionSupported {
		t.Fatalf("selection status = %q, want supported", selection.Status)
	}
	if selection.Manifest == nil || selection.Manifest.ID != manifest.ID {
		t.Fatalf("selected manifest = %#v, want %q", selection.Manifest, manifest.ID)
	}
	if selection.Coverage != contract.CoverageIncomplete {
		t.Fatalf("selection coverage = %q, want incomplete", selection.Coverage)
	}
	if selection.Fallback || !selection.SourceTypeRecognized || !selection.FamilyRecognized || !selection.VersionRecognized {
		t.Fatalf("selection recognition flags = fallback:%t source_type:%t family:%t version:%t", selection.Fallback, selection.SourceTypeRecognized, selection.FamilyRecognized, selection.VersionRecognized)
	}
}

func TestSelectFrontendUnknownRequestUsesIncompleteFallback(t *testing.T) {
	t.Parallel()

	specialized := validFrontendManifest(nil)
	fallback := validFrontendManifest(nil)
	fallback.ID = "generic-fallback"
	fallback.Fallback = true
	fallback.Families = []string{"generic"}
	fallback.Versions = []string{"any"}

	selection, err := fact.SelectFrontend([]fact.FrontendManifest{specialized, fallback}, "unknown-source-type", "unknown-family", "unknown-version")
	if err != nil {
		t.Fatalf("SelectFrontend(unknown request) error = %v", err)
	}
	if selection.Status != fact.FrontendSelectionFallback || !selection.Fallback {
		t.Fatalf("selection = %#v, want fallback", selection)
	}
	if selection.Manifest == nil || selection.Manifest.ID != fallback.ID {
		t.Fatalf("selected fallback = %#v, want %q", selection.Manifest, fallback.ID)
	}
	if selection.Coverage != contract.CoverageIncomplete {
		t.Fatalf("fallback coverage = %q, want incomplete", selection.Coverage)
	}
	if selection.SourceTypeRecognized || selection.FamilyRecognized || selection.VersionRecognized {
		t.Fatalf("unknown request was recognized: source_type=%t family=%t version=%t", selection.SourceTypeRecognized, selection.FamilyRecognized, selection.VersionRecognized)
	}
	if !contains(selection.Limitations, "fallback_frontend_selected") || !contains(selection.Limitations, "requested_source_type_not_recognized") || !contains(selection.Limitations, "requested_family_not_recognized") || !contains(selection.Limitations, "requested_version_not_recognized") {
		t.Fatalf("fallback limitations = %#v", selection.Limitations)
	}
}

func TestSelectFrontendUnknownRequestIsExplicitlyUnsupported(t *testing.T) {
	t.Parallel()

	selection, err := fact.SelectFrontend([]fact.FrontendManifest{validFrontendManifest(nil)}, "unknown-source-type", "unknown-family", "unknown-version")
	if err != nil {
		t.Fatalf("SelectFrontend(unknown request) error = %v", err)
	}
	if selection.Status != fact.FrontendSelectionUnsupported {
		t.Fatalf("selection status = %q, want not_supported", selection.Status)
	}
	if selection.Manifest != nil || selection.Coverage != contract.CoverageNotSupported {
		t.Fatalf("unsupported selection = %#v", selection)
	}
	if !contains(selection.Limitations, "requested_source_type_not_recognized") || !contains(selection.Limitations, "requested_family_not_recognized") || !contains(selection.Limitations, "requested_version_not_recognized") {
		t.Fatalf("unsupported limitations = %#v", selection.Limitations)
	}
}

func TestSelectFrontendRequiresRecognizedSourceType(t *testing.T) {
	t.Parallel()

	manifest := validFrontendManifest(nil)
	selection, err := fact.SelectFrontend([]fact.FrontendManifest{manifest}, "archive", "jvm", "17")
	if err != nil {
		t.Fatalf("SelectFrontend(unknown source type) error = %v", err)
	}
	if selection.Status != fact.FrontendSelectionUnsupported || selection.Manifest != nil {
		t.Fatalf("selection = %#v, want unsupported without manifest", selection)
	}
	if selection.SourceTypeRecognized || !selection.FamilyRecognized || !selection.VersionRecognized {
		t.Fatalf("recognition flags = source_type:%t family:%t version:%t", selection.SourceTypeRecognized, selection.FamilyRecognized, selection.VersionRecognized)
	}
	if selection.Coverage != contract.CoverageNotSupported || !contains(selection.Limitations, "requested_source_type_not_recognized") {
		t.Fatalf("selection coverage/limitations = %q/%#v", selection.Coverage, selection.Limitations)
	}

	fallback := manifest
	fallback.ID = "generic-fallback"
	fallback.Fallback = true
	fallback.Families = []string{"generic"}
	fallback.Versions = []string{"any"}
	fallbackSelection, err := fact.SelectFrontend([]fact.FrontendManifest{manifest, fallback}, "archive", "jvm", "17")
	if err != nil {
		t.Fatalf("SelectFrontend(unknown source type with fallback) error = %v", err)
	}
	if fallbackSelection.Status != fact.FrontendSelectionFallback || fallbackSelection.Coverage != contract.CoverageIncomplete {
		t.Fatalf("fallback selection = %#v, want incomplete fallback", fallbackSelection)
	}
	if !contains(fallbackSelection.Limitations, "requested_source_type_not_recognized") {
		t.Fatalf("fallback limitations = %#v", fallbackSelection.Limitations)
	}
}

func TestSelectFrontendRejectsDuplicateIDsAndAmbiguousFallbacks(t *testing.T) {
	t.Parallel()

	base := validFrontendManifest(nil)
	duplicate := base
	if _, err := fact.SelectFrontend([]fact.FrontendManifest{base, duplicate}, "repository", "jvm", "17"); !errors.Is(err, fact.ErrInvalidManifest) {
		t.Fatalf("SelectFrontend(duplicate IDs) error = %v, want invalid manifest", err)
	}

	firstFallback := base
	firstFallback.ID = "fallback-1"
	firstFallback.Fallback = true
	secondFallback := base
	secondFallback.ID = "fallback-2"
	secondFallback.Fallback = true
	if _, err := fact.SelectFrontend([]fact.FrontendManifest{firstFallback, secondFallback}, "unknown", "unknown", "unknown"); !errors.Is(err, fact.ErrInvalidSelection) {
		t.Fatalf("SelectFrontend(ambiguous fallbacks) error = %v, want invalid selection", err)
	}
}

func validFrontendManifest(payload []byte) fact.FrontendManifest {
	manifest := fact.FrontendManifest{
		ManifestVersion: fact.FrontendManifestVersion,
		ID:              "frontend-jvm",
		Version:         "1.4.0",
		Method:          "ast-index",
		SourceTypes:     []string{"repository"},
		Families:        []string{"jvm"},
		Versions:        []string{"17"},
		Capabilities:    []contract.Dimension{contract.DimensionEntitiesAndRelationships},
		Limitations:     []string{"no-build-execution"},
		Predicates:      []fact.Predicate{fact.PredicateSymbol, fact.PredicateDefinition},
		Execution:       fact.ExecutionProfileSafeStatic,
	}
	if payload != nil {
		manifest.Extensions = []fact.ExtensionSchema{{
			ID:      "extension.example",
			Version: "1",
			Digest:  fact.ExtensionDigest(payload),
		}}
	}
	return manifest
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
