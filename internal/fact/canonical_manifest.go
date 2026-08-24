package fact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/contract"
)

// CanonicalFrontendManifest returns a validated, detached representation of a
// frontend manifest. Collection order is normalized without mutating the
// caller's value, so transport writers and persistence adapters can share one
// canonical representation.
func CanonicalFrontendManifest(manifest FrontendManifest) (FrontendManifest, error) {
	if err := manifest.Validate(); err != nil {
		return FrontendManifest{}, err
	}

	manifest.SourceTypes = sortedManifestStrings(manifest.SourceTypes)
	manifest.Families = sortedManifestStrings(manifest.Families)
	manifest.Versions = sortedManifestStrings(manifest.Versions)
	manifest.Capabilities = append([]contract.Dimension(nil), manifest.Capabilities...)
	sort.Slice(manifest.Capabilities, func(left, right int) bool {
		return manifest.Capabilities[left] < manifest.Capabilities[right]
	})
	manifest.Limitations = sortedManifestStrings(manifest.Limitations)
	manifest.Predicates = append([]Predicate(nil), manifest.Predicates...)
	sort.Slice(manifest.Predicates, func(left, right int) bool {
		return manifest.Predicates[left] < manifest.Predicates[right]
	})
	manifest.Extensions = append([]ExtensionSchema(nil), manifest.Extensions...)
	sort.Slice(manifest.Extensions, func(left, right int) bool {
		return extensionSchemaKey(manifest.Extensions[left]) < extensionSchemaKey(manifest.Extensions[right])
	})
	return manifest, nil
}

// CanonicalFrontendManifestBytes returns the stable JSON encoding of one
// frontend manifest. The manifest is validated before encoding.
func CanonicalFrontendManifestBytes(manifest FrontendManifest) ([]byte, error) {
	canonical, err := CanonicalFrontendManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("canonical frontend manifest: %w", err)
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, fmt.Errorf("encoding frontend manifest: %w", err)
	}
	return encoded, nil
}

// FrontendManifestDigest returns the lowercase SHA-256 digest of the stable
// frontend manifest JSON representation.
func FrontendManifestDigest(manifest FrontendManifest) (string, error) {
	encoded, err := CanonicalFrontendManifestBytes(manifest)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sortedManifestStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func extensionSchemaKey(schema ExtensionSchema) string {
	return schema.ID + "\x00" + schema.Version + "\x00" + schema.Digest
}
