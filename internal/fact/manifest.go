package fact

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// FrontendManifestVersion identifies the first version of the frontend
	// manifest contract. It is independent from the frontend's own Version.
	FrontendManifestVersion = "v1alpha1"
)

// ExecutionProfile describes the trust and isolation boundary in which a
// frontend may produce or import results. The zero value is intentionally
// invalid.
type ExecutionProfile string

const (
	ExecutionProfileUnknown          ExecutionProfile = ""
	ExecutionProfileSafeStatic       ExecutionProfile = "safe-static"
	ExecutionProfileSemanticIsolated ExecutionProfile = "semantic-isolated"
	ExecutionProfileImportedIndex    ExecutionProfile = "imported-index"
)

// FrontendManifest describes one versioned frontend without asserting that
// every declared capability will be present in a particular analysis.
type FrontendManifest struct {
	ManifestVersion string               `json:"manifest_version"`
	ID              string               `json:"id"`
	Version         string               `json:"version"`
	Method          string               `json:"method"`
	SourceTypes     []string             `json:"source_types"`
	Families        []string             `json:"families"`
	Versions        []string             `json:"versions"`
	Capabilities    []contract.Dimension `json:"capabilities"`
	Limitations     []string             `json:"limitations"`
	Predicates      []Predicate          `json:"predicates"`
	Execution       ExecutionProfile     `json:"execution_profile"`
	Extensions      []ExtensionSchema    `json:"extensions,omitempty"`
	Fallback        bool                 `json:"fallback,omitempty"`
}

// ExtensionSchema identifies a versioned extension schema by its content
// digest. Digest is the lowercase hexadecimal SHA-256 of the schema bytes
// supplied to Verify.
type ExtensionSchema struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Digest  string `json:"digest_sha256"`
}

// FrontendSelectionStatus describes the result of selecting a frontend. The
// zero value is invalid and is never returned by SelectFrontend.
type FrontendSelectionStatus string

const (
	FrontendSelectionUnknown     FrontendSelectionStatus = ""
	FrontendSelectionSupported   FrontendSelectionStatus = "supported"
	FrontendSelectionFallback    FrontendSelectionStatus = "fallback"
	FrontendSelectionUnsupported FrontendSelectionStatus = "not_supported"
)

// FrontendSelection is an explicit support decision. A supported manifest is
// selected, but its coverage remains incomplete until an analysis reports
// what it actually produced. Unknown requests use a validated fallback when
// one is available; otherwise Manifest is nil and Coverage is not_supported.
type FrontendSelection struct {
	RequestedSourceType  string                  `json:"requested_source_type"`
	RequestedFamily      string                  `json:"requested_family"`
	RequestedVersion     string                  `json:"requested_version"`
	Manifest             *FrontendManifest       `json:"manifest,omitempty"`
	Status               FrontendSelectionStatus `json:"status"`
	Coverage             contract.CoverageState  `json:"coverage"`
	Fallback             bool                    `json:"fallback"`
	SourceTypeRecognized bool                    `json:"source_type_recognized"`
	FamilyRecognized     bool                    `json:"family_recognized"`
	VersionRecognized    bool                    `json:"version_recognized"`
	Limitations          []string                `json:"limitations"`
}

// Validate checks the manifest's version, identity, declarations, enums, and
// duplicate-free collections. It does not verify an extension digest against
// bytes; use ExtensionSchema.Verify for that content-bound check.
func (m FrontendManifest) Validate() error {
	if m.ManifestVersion != FrontendManifestVersion {
		return fmt.Errorf("%w: frontend manifest version %q", ErrUnsupportedVersion, m.ManifestVersion)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "frontend id", value: m.ID},
		{name: "frontend version", value: m.Version},
		{name: "frontend method", value: m.Method},
	} {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
		}
	}
	if err := validateNonEmptyUniqueStrings("source type", m.SourceTypes); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateNonEmptyUniqueStrings("source family", m.Families); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateNonEmptyUniqueStrings("source version", m.Versions); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("%w: at least one capability is required", ErrInvalidManifest)
	}
	if err := validateDimensions(m.Capabilities); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateOptionalUniqueStrings("limitation", m.Limitations); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if len(m.Predicates) == 0 {
		return fmt.Errorf("%w: at least one predicate is required", ErrInvalidManifest)
	}
	if err := validatePredicates(m.Predicates); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := validateExecutionProfile(m.Execution); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	seenSchemas := make(map[string]struct{}, len(m.Extensions))
	for i, schema := range m.Extensions {
		if err := schema.Validate(); err != nil {
			return fmt.Errorf("%w: extension schema %d: %v", ErrInvalidManifest, i, err)
		}
		key := schema.ID + "\x00" + schema.Version
		if _, exists := seenSchemas[key]; exists {
			return fmt.Errorf("%w: duplicate extension schema %q version %q", ErrInvalidManifest, schema.ID, schema.Version)
		}
		seenSchemas[key] = struct{}{}
	}
	return nil
}

// Validate checks the extension schema identity and digest shape. It does
// not read schema content.
func (s ExtensionSchema) Validate() error {
	if err := validateIdentifier("extension schema id", s.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExtensionSchema, err)
	}
	if err := validateIdentifier("extension schema version", s.Version); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExtensionSchema, err)
	}
	if err := validateSHA256Digest(s.Digest); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidExtensionSchema, err)
	}
	return nil
}

// Verify checks that payload is the schema identified by this extension's
// SHA-256 digest.
func (s ExtensionSchema) Verify(payload []byte) error {
	if err := s.Validate(); err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	got := hex.EncodeToString(digest[:])
	if got != s.Digest {
		return fmt.Errorf("%w: extension schema %q digest mismatch", ErrInvalidExtensionSchema, s.ID)
	}
	return nil
}

// ExtensionDigest returns the lowercase hexadecimal SHA-256 digest used by
// ExtensionSchema.Digest.
func ExtensionDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

// SelectFrontend chooses a manifest for a requested source type, family, and
// version. Unknown request values are not errors: the result makes the
// unsupported/incomplete state explicit and uses one declared fallback when
// available. Invalid or ambiguous manifests remain errors.
func SelectFrontend(manifests []FrontendManifest, sourceType, family, version string) (FrontendSelection, error) {
	selection := FrontendSelection{
		RequestedSourceType: sourceType,
		RequestedFamily:     family,
		RequestedVersion:    version,
		Coverage:            contract.CoverageNotSupported,
	}
	if err := validateIdentifier("requested source type", sourceType); err != nil {
		return selection, fmt.Errorf("%w: %v", ErrInvalidSelection, err)
	}
	if err := validateIdentifier("requested source family", family); err != nil {
		return selection, fmt.Errorf("%w: %v", ErrInvalidSelection, err)
	}
	if err := validateIdentifier("requested source version", version); err != nil {
		return selection, fmt.Errorf("%w: %v", ErrInvalidSelection, err)
	}

	seenIDs := make(map[string]struct{}, len(manifests))
	exact := make([]FrontendManifest, 0, 1)
	fallbacks := make([]FrontendManifest, 0, 1)
	for i, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return selection, fmt.Errorf("manifest %d: %w", i, err)
		}
		if _, exists := seenIDs[manifest.ID]; exists {
			return selection, fmt.Errorf("%w: duplicate frontend manifest id %q", ErrInvalidManifest, manifest.ID)
		}
		seenIDs[manifest.ID] = struct{}{}
		if manifest.Supports(sourceType, family, version) {
			exact = append(exact, manifest)
		}
		if manifest.Fallback {
			fallbacks = append(fallbacks, manifest)
		}
		if manifest.SupportsSourceType(sourceType) {
			selection.SourceTypeRecognized = true
		}
		if manifest.SupportsFamily(family) {
			selection.FamilyRecognized = true
		}
		if manifest.SupportsVersion(version) {
			selection.VersionRecognized = true
		}
	}

	if len(exact) > 1 {
		return selection, fmt.Errorf("%w: multiple frontend manifests support source type %q family %q version %q", ErrInvalidSelection, sourceType, family, version)
	}
	if len(exact) == 1 {
		selected := exact[0]
		selection.Manifest = &selected
		selection.Status = FrontendSelectionSupported
		// Manifest recognition declares availability, not execution coverage.
		selection.Coverage = contract.CoverageIncomplete
		selection.Limitations = appendUnique(selection.Limitations, selected.Limitations...)
		return selection, nil
	}

	if len(fallbacks) > 1 {
		return selection, fmt.Errorf("%w: multiple fallback frontend manifests", ErrInvalidSelection)
	}
	if len(fallbacks) == 1 {
		selected := fallbacks[0]
		selection.Manifest = &selected
		selection.Status = FrontendSelectionFallback
		selection.Fallback = true
		selection.Coverage = contract.CoverageIncomplete
		if !selection.SourceTypeRecognized {
			selection.Limitations = appendUnique(selection.Limitations, "requested_source_type_not_recognized")
		}
		if !selection.FamilyRecognized {
			selection.Limitations = appendUnique(selection.Limitations, "requested_family_not_recognized")
		}
		if !selection.VersionRecognized {
			selection.Limitations = appendUnique(selection.Limitations, "requested_version_not_recognized")
		}
		selection.Limitations = appendUnique(selection.Limitations, "fallback_frontend_selected")
		selection.Limitations = appendUnique(selection.Limitations, selected.Limitations...)
		return selection, nil
	}

	selection.Status = FrontendSelectionUnsupported
	if !selection.SourceTypeRecognized {
		selection.Limitations = appendUnique(selection.Limitations, "requested_source_type_not_recognized")
	}
	if !selection.FamilyRecognized {
		selection.Limitations = appendUnique(selection.Limitations, "requested_family_not_recognized")
	}
	if !selection.VersionRecognized {
		selection.Limitations = appendUnique(selection.Limitations, "requested_version_not_recognized")
	}
	if len(selection.Limitations) == 0 {
		selection.Limitations = append(selection.Limitations, "no_frontend_manifest_supports_request")
	}
	return selection, nil
}

// Supports reports whether the manifest recognizes the requested source type,
// family, and version. It does not imply complete coverage.
func (m FrontendManifest) Supports(sourceType, family, version string) bool {
	return m.SupportsSourceType(sourceType) && m.SupportsFamily(family) && m.SupportsVersion(version)
}

// SupportsSourceType reports whether sourceType is declared by the manifest.
func (m FrontendManifest) SupportsSourceType(sourceType string) bool {
	return containsString(m.SourceTypes, sourceType)
}

// SupportsFamily reports whether family is declared by the manifest.
func (m FrontendManifest) SupportsFamily(family string) bool {
	return containsString(m.Families, family)
}

// SupportsVersion reports whether version is declared by the manifest.
func (m FrontendManifest) SupportsVersion(version string) bool {
	return containsString(m.Versions, version)
}

func validateExecutionProfile(profile ExecutionProfile) error {
	switch profile {
	case ExecutionProfileSafeStatic, ExecutionProfileSemanticIsolated, ExecutionProfileImportedIndex:
		return nil
	default:
		return fmt.Errorf("execution profile %q is invalid", profile)
	}
}

func validateNonEmptyUniqueStrings(name string, values []string) error {
	if len(values) == 0 {
		return fmt.Errorf("at least one %s is required", name)
	}
	return validateOptionalUniqueStrings(name, values)
}

func validateOptionalUniqueStrings(name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateIdentifier(name, value); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate %s %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateDimensions(dimensions []contract.Dimension) error {
	seen := make(map[contract.Dimension]struct{}, len(dimensions))
	for _, dimension := range dimensions {
		if strings.TrimSpace(string(dimension)) == "" {
			return fmt.Errorf("capability dimension is required")
		}
		if _, exists := seen[dimension]; exists {
			return fmt.Errorf("duplicate capability dimension %q", dimension)
		}
		seen[dimension] = struct{}{}
	}
	return nil
}

func validatePredicates(predicates []Predicate) error {
	seen := make(map[Predicate]struct{}, len(predicates))
	for _, predicate := range predicates {
		if err := predicate.Validate(); err != nil {
			return err
		}
		if _, exists := seen[predicate]; exists {
			return fmt.Errorf("duplicate predicate %q", predicate)
		}
		seen[predicate] = struct{}{}
	}
	return nil
}

func validateSHA256Digest(digest string) error {
	if len(digest) != sha256.Size*2 {
		return fmt.Errorf("SHA-256 digest must contain %d hexadecimal characters", sha256.Size*2)
	}
	if digest != strings.ToLower(digest) {
		return fmt.Errorf("SHA-256 digest must use lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return fmt.Errorf("SHA-256 digest is malformed")
	}
	return nil
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(additions))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	for _, value := range additions {
		if _, exists := seen[value]; exists {
			continue
		}
		values = append(values, value)
		seen[value] = struct{}{}
	}
	return values
}
