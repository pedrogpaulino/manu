package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

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

// FactualDigest computes the digest for this in-memory bundle without
// validating sequence references or file descriptors.
func (b Bundle) FactualDigest() (string, error) {
	return FactualDigest(contract.Result{
		Manifest:      b.Manifest.Manifest,
		Artifacts:     b.Artifacts,
		Contributions: b.Contributions,
	}, b.Evidence)
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
