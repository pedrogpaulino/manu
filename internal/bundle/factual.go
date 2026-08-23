package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const factualDigestV2Domain = "manu:bundle:factual:v1alpha2\x00"

// FactualDigest returns the deterministic digest of a bundle's factual
// result. It starts with the legacy contract digest and adds the Evidence
// Units in identity order. Transport bytes, such as JSON or NDJSON framing,
// are deliberately not part of this digest.
func FactualDigest(result contract.Result, units []evidence.EvidenceUnit) (string, error) {
	legacyDigest, err := contract.FactualDigest(result)
	if err != nil {
		return "", fmt.Errorf("%w: legacy factual digest: %v", ErrInvalid, err)
	}

	facts := make([]factualEvidence, len(units))
	seen := make(map[string]struct{}, len(units))
	for index, unit := range units {
		if err := unit.Validate(); err != nil {
			return "", fmt.Errorf("%w: evidence unit %d: %v", ErrInvalid, index, err)
		}
		if _, exists := seen[unit.ID]; exists {
			return "", fmt.Errorf("%w: evidence unit %q", ErrDuplicate, unit.ID)
		}
		seen[unit.ID] = struct{}{}
		facts[index] = factualEvidenceFrom(unit)
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].ID < facts[j].ID })

	payload, err := json.Marshal(factualBundle{
		LegacyDigest: legacyDigest,
		Evidence:     facts,
	})
	if err != nil {
		return "", fmt.Errorf("%w: bundle factual digest: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// FactualHash is a convenience form of FactualDigest. Invalid values return
// an empty string; callers that need the reason should use FactualDigest.
func FactualHash(result contract.Result, units []evidence.EvidenceUnit) string {
	digest, err := FactualDigest(result, units)
	if err != nil {
		return ""
	}
	return digest
}

// FactualDigestV2 returns the v1alpha2 factual digest. The legacy result and
// evidence digest remains an explicit component, while each new canonical
// sequence is encoded under its own field and sorted before hashing. The
// domain separator keeps this digest distinct from both the legacy bundle
// digest and fact identity/canonical encodings.
func FactualDigestV2(
	result contract.Result,
	units []evidence.EvidenceUnit,
	frontendManifests []fact.FrontendManifest,
	facts []fact.CanonicalFact,
	extensions []json.RawMessage,
) (string, error) {
	legacyDigest, err := FactualDigest(result, units)
	if err != nil {
		return "", err
	}
	manifests, err := canonicalFrontendManifests(frontendManifests)
	if err != nil {
		return "", err
	}
	canonicalFacts, err := canonicalFactsForDigest(facts)
	if err != nil {
		return "", err
	}
	canonicalExtensions, err := canonicalExtensionsForDigest(extensions)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(factualBundleV2{
		Version:           VersionV1Alpha2,
		LegacyDigest:      legacyDigest,
		FrontendManifests: manifests,
		Facts:             canonicalFacts,
		Extensions:        canonicalExtensions,
	})
	if err != nil {
		return "", fmt.Errorf("%w: v1alpha2 bundle factual digest: %v", ErrInvalid, err)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(factualDigestV2Domain))
	_, _ = digest.Write(payload)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// FactualDigest computes the digest for this in-memory bundle without
// validating sequence references or file descriptors.
func (b Bundle) FactualDigest() (string, error) {
	if b.Manifest.Version == VersionV1Alpha2 {
		return FactualDigestV2(
			contract.Result{
				Manifest:      b.Manifest.Manifest,
				Artifacts:     b.Artifacts,
				Contributions: b.Contributions,
			},
			b.Evidence,
			b.FrontendManifests,
			b.Facts,
			b.Extensions,
		)
	}
	return FactualDigest(contract.Result{
		Manifest:      b.Manifest.Manifest,
		Artifacts:     b.Artifacts,
		Contributions: b.Contributions,
	}, b.Evidence)
}

type factualBundleV2 struct {
	Version           string            `json:"version"`
	LegacyDigest      string            `json:"legacy_digest"`
	FrontendManifests []json.RawMessage `json:"frontend_manifests"`
	Facts             []json.RawMessage `json:"facts"`
	Extensions        []json.RawMessage `json:"extensions"`
}

func canonicalFrontendManifests(manifests []fact.FrontendManifest) ([]json.RawMessage, error) {
	ordered := append([]fact.FrontendManifest(nil), manifests...)
	seen := make(map[string]struct{}, len(ordered))
	for index := range ordered {
		if err := ordered[index].Validate(); err != nil {
			return nil, fmt.Errorf("%w: frontend manifest %d: %v", ErrInvalid, index, err)
		}
		if _, exists := seen[ordered[index].ID]; exists {
			return nil, fmt.Errorf("%w: frontend manifest %q", ErrDuplicate, ordered[index].ID)
		}
		seen[ordered[index].ID] = struct{}{}
		ordered[index] = canonicalFrontendManifestValue(ordered[index])
	}
	sort.Slice(ordered, func(left, right int) bool {
		return frontendManifestKey(ordered[left]) < frontendManifestKey(ordered[right])
	})
	result := make([]json.RawMessage, 0, len(ordered))
	for index, manifest := range ordered {
		encoded, err := json.Marshal(manifest)
		if err != nil {
			return nil, fmt.Errorf("%w: encoding frontend manifest %d: %v", ErrInvalid, index, err)
		}
		result = append(result, encoded)
	}
	return result, nil
}

func canonicalFrontendManifestValue(manifest fact.FrontendManifest) fact.FrontendManifest {
	manifest.SourceTypes = sortedStrings(manifest.SourceTypes)
	manifest.Families = sortedStrings(manifest.Families)
	manifest.Versions = sortedStrings(manifest.Versions)
	manifest.Capabilities = append([]contract.Dimension(nil), manifest.Capabilities...)
	sort.Slice(manifest.Capabilities, func(left, right int) bool {
		return manifest.Capabilities[left] < manifest.Capabilities[right]
	})
	manifest.Limitations = sortedStrings(manifest.Limitations)
	manifest.Predicates = append([]fact.Predicate(nil), manifest.Predicates...)
	sort.Slice(manifest.Predicates, func(left, right int) bool {
		return manifest.Predicates[left] < manifest.Predicates[right]
	})
	manifest.Extensions = append([]fact.ExtensionSchema(nil), manifest.Extensions...)
	sort.Slice(manifest.Extensions, func(left, right int) bool {
		return extensionSchemaKey(manifest.Extensions[left]) < extensionSchemaKey(manifest.Extensions[right])
	})
	return manifest
}

func canonicalFactsForDigest(facts []fact.CanonicalFact) ([]json.RawMessage, error) {
	ordered := append([]fact.CanonicalFact(nil), facts...)
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	seen := make(map[string]struct{}, len(ordered))
	result := make([]json.RawMessage, 0, len(ordered))
	for index, canonicalFact := range ordered {
		if _, exists := seen[canonicalFact.ID]; exists {
			return nil, fmt.Errorf("%w: canonical fact %q", ErrDuplicate, canonicalFact.ID)
		}
		seen[canonicalFact.ID] = struct{}{}
		encoded, err := fact.CanonicalBytes(canonicalFact)
		if err != nil {
			return nil, fmt.Errorf("%w: encoding canonical fact %d: %v", ErrInvalid, index, err)
		}
		result = append(result, encoded)
	}
	return result, nil
}

func canonicalExtensionsForDigest(extensions []json.RawMessage) ([]json.RawMessage, error) {
	result := make([]json.RawMessage, 0, len(extensions))
	for index, extension := range extensions {
		canonical, err := canonicalExtensionJSON(extension)
		if err != nil {
			return nil, fmt.Errorf("%w: extension %d: %v", ErrInvalidExtension, index, err)
		}
		result = append(result, canonical)
	}
	sort.Slice(result, func(left, right int) bool { return string(result[left]) < string(result[right]) })
	return result, nil
}

// canonicalExtensionJSON decodes one extension as JSON with numbers retained
// lexically, rejects trailing data, and re-encodes it with the standard
// deterministic object-key ordering. It is shared by digesting and imported
// extension validation so the bytes that are checked are the bytes that are
// written and hashed.
func canonicalExtensionJSON(extension []byte) ([]byte, error) {
	if len(extension) == 0 {
		return nil, fmt.Errorf("empty extension")
	}
	decoder := json.NewDecoder(bytes.NewReader(extension))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("extension contains trailing JSON")
		}
		return nil, fmt.Errorf("extension has trailing data: %v", err)
	}
	return json.Marshal(value)
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func frontendManifestKey(manifest fact.FrontendManifest) string {
	return manifest.ID + "\x00" + manifest.Version + "\x00" + manifest.Method
}

func extensionSchemaKey(schema fact.ExtensionSchema) string {
	return schema.ID + "\x00" + schema.Version + "\x00" + schema.Digest
}

type factualBundle struct {
	LegacyDigest string            `json:"legacy_digest"`
	Evidence     []factualEvidence `json:"evidence"`
}

// factualEvidence intentionally excludes Content. ContentHash identifies the
// unit's content or retained origin according to its state while the retained
// digest, truncation, counts, and redaction metadata preserve the representation
// actually authorized by the bundle.
type factualEvidence struct {
	Version        string                   `json:"version"`
	ID             string                   `json:"id"`
	OrganizationID string                   `json:"organization_id"`
	SourceID       string                   `json:"source_id"`
	SnapshotID     string                   `json:"snapshot_id"`
	ArtifactID     string                   `json:"artifact_id"`
	Contribution   evidence.ContributionRef `json:"contribution"`
	Locator        contract.Locator         `json:"locator"`
	ContentState   evidence.ContentState    `json:"content_state"`
	ContentHash    string                   `json:"content_hash"`
	Truncated      bool                     `json:"truncated"`
	Classification evidence.Classification  `json:"classification,omitempty"`
	Findings       []string                 `json:"findings,omitempty"`
	// RetainedContentDigest covers the representation carried by this unit,
	// including redacted text, without copying that text into the digest
	// payload. ContentHash remains the source-content identity.
	RetainedContentDigest string            `json:"retained_content_digest"`
	RedactionReason       string            `json:"redaction_reason,omitempty"`
	ContentBytes          int64             `json:"content_bytes"`
	ContentCharacters     int64             `json:"content_characters"`
	Persist               evidence.Decision `json:"persist"`
	ExternalTransfer      evidence.Decision `json:"external_transfer"`
}

func factualEvidenceFrom(unit evidence.EvidenceUnit) factualEvidence {
	return factualEvidence{
		Version:               unit.Version,
		ID:                    unit.ID,
		OrganizationID:        unit.OrganizationID,
		SourceID:              unit.SourceID,
		SnapshotID:            unit.SnapshotID,
		ArtifactID:            unit.ArtifactID,
		Contribution:          unit.Contribution,
		Locator:               unit.Locator,
		ContentState:          unit.ContentState,
		ContentHash:           unit.ContentHash,
		Truncated:             unit.Truncated,
		Classification:        unit.Classification,
		Findings:              canonicalFindings(unit.Findings),
		RetainedContentDigest: evidence.ContentDigest(unit.Content),
		RedactionReason:       unit.RedactionReason,
		ContentBytes:          unit.ContentBytes,
		ContentCharacters:     unit.ContentCharacters,
		Persist:               unit.Persist,
		ExternalTransfer:      unit.ExternalTransfer,
	}
}

func canonicalFindings(findings []string) []string {
	if len(findings) == 0 {
		return nil
	}
	ordered := append([]string(nil), findings...)
	sort.Strings(ordered)
	result := ordered[:0]
	for _, finding := range ordered {
		if len(result) == 0 || result[len(result)-1] != finding {
			result = append(result, finding)
		}
	}
	return result
}
