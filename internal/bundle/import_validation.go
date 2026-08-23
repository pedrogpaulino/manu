package bundle

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

// ImportExpectation is the caller-provided authorization boundary for an
// imported bundle. A bundle's self-declared organization, snapshot, or
// frontend manifest is descriptive only; callers must provide the exact
// scope and complete trusted frontend manifests.
type ImportExpectation struct {
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	SnapshotID     string `json:"snapshot_id"`
	Limits         Limits `json:"limits"`

	// AllowedFrontends contains complete trusted declarations. Identity alone
	// is not sufficient to authorize a frontend sequence.
	AllowedFrontends []fact.FrontendManifest `json:"allowed_frontends"`
}

// Validate checks that an import expectation is explicit and unambiguous.
func (e ImportExpectation) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "expected organization id", value: e.OrganizationID},
		{name: "expected source id", value: e.SourceID},
		{name: "expected snapshot id", value: e.SnapshotID},
	} {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return err
		}
	}
	if err := e.Limits.Validate(); err != nil {
		return err
	}
	seenFrontends := make(map[string]struct{}, len(e.AllowedFrontends))
	for _, frontend := range e.AllowedFrontends {
		if err := frontend.Validate(); err != nil {
			return fmt.Errorf("%w: allowed frontend: %v", ErrInvalid, err)
		}
		key := frontendManifestKey(frontend)
		if _, exists := seenFrontends[key]; exists {
			return fmt.Errorf("%w: duplicate allowed frontend", ErrInvalid)
		}
		seenFrontends[key] = struct{}{}
	}
	return nil
}

// ValidateImportedBundle validates a fully materialized bundle against an
// explicit import expectation. It returns no bundle or partial result.
func ValidateImportedBundle(input Bundle, expectation ImportExpectation) error {
	if err := expectation.Validate(); err != nil {
		return err
	}
	if err := input.Validate(); err != nil {
		return err
	}
	return validateImportExpectation(input, expectation)
}

// ReadImportedBundle reads a bundle only when it satisfies the caller's
// explicit scope and producer/frontend expectation. The expectation is
// applied to the manifest before sequence data is read and to the complete
// result before it is returned.
func ReadImportedBundle(ctx context.Context, directory string, expectation ImportExpectation, options ...Options) (Bundle, error) {
	if err := expectation.Validate(); err != nil {
		return Bundle{}, err
	}
	if len(options) > 1 {
		return Bundle{}, fmt.Errorf("reading imported bundle: %w: at most one options value is allowed", ErrInvalid)
	}
	var option Options
	if len(options) == 1 {
		option = options[0]
	}
	if option.Organization.ID == "" && option.OrganizationID == "" {
		option.OrganizationID = expectation.OrganizationID
	}
	option.Limits = stricterLimits(defaultLimitsV2(expectation.Limits), defaultLimitsV2(option.Limits))
	option.ImportExpectation = &expectation
	return ReadBundle(ctx, directory, option)
}

func validateImportExpectation(input Bundle, expectation ImportExpectation) error {
	return validateImportExpectationWithFiles(input, expectation, true)
}

func validateImportExpectationBeforeWrite(input Bundle, expectation ImportExpectation) error {
	return validateImportExpectationWithFiles(input, expectation, false)
}

func validateImportExpectationWithFiles(input Bundle, expectation ImportExpectation, checkFiles bool) error {
	if err := expectation.Validate(); err != nil {
		return err
	}
	if err := validateExpectedManifest(input.Manifest, expectation); err != nil {
		return err
	}
	frontendSet, err := allowedImportIdentities(expectation)
	if err != nil {
		return err
	}
	if len(input.FrontendManifests) > 0 {
		if len(frontendSet) == 0 {
			return fmt.Errorf("%w: trusted frontend manifests are required", ErrInvalidReference)
		}
		for _, frontendManifest := range input.FrontendManifests {
			key := frontendManifestKey(frontendManifest)
			trusted, allowed := frontendSet[key]
			actual, fingerprintErr := frontendManifestFingerprint(frontendManifest)
			if fingerprintErr != nil || !allowed || actual != trusted {
				return fmt.Errorf("%w: frontend is not authorized", ErrInvalidReference)
			}
		}
	}
	for _, canonicalFact := range input.Facts {
		key := frontendManifestKeyForProducer(canonicalFact.Producer)
		if _, allowed := frontendSet[key]; !allowed {
			return fmt.Errorf("%w: canonical fact producer is not authorized", ErrInvalidReference)
		}
	}
	if err := validateImportLimits(input, expectation.Limits, checkFiles); err != nil {
		return err
	}
	return nil
}

func validateExpectedManifest(manifest Manifest, expectation ImportExpectation) error {
	if err := expectation.Validate(); err != nil {
		return err
	}
	if manifest.Organization.ID != expectation.OrganizationID ||
		manifest.Source.ID != expectation.SourceID ||
		manifest.Snapshot.ID != expectation.SnapshotID {
		return fmt.Errorf("%w: imported bundle is outside expected scope", ErrScopeMismatch)
	}
	return nil
}

func allowedImportIdentities(expectation ImportExpectation) (map[string]string, error) {
	frontends := make(map[string]string, len(expectation.AllowedFrontends))
	for _, frontend := range expectation.AllowedFrontends {
		fingerprint, err := frontendManifestFingerprint(frontend)
		if err != nil {
			return nil, fmt.Errorf("%w: trusted frontend manifest: %v", ErrInvalid, err)
		}
		frontends[frontendManifestKey(frontend)] = fingerprint
	}
	return frontends, nil
}

func validateImportLimits(input Bundle, expected Limits, checkFiles bool) error {
	effective := stricterLimits(defaultLimitsV2(input.Manifest.Limits), defaultLimitsV2(expected))
	if err := effective.Validate(); err != nil {
		return err
	}
	if err := validateCountLimits(input.Manifest.Counts, effective); err != nil {
		return err
	}
	if checkFiles && len(input.Manifest.Files) > 0 {
		if _, err := validateManifestFiles(input.Manifest.Version, input.Manifest.Files, input.Manifest.Counts, effective); err != nil {
			return err
		}
	}
	return nil
}

func frontendManifestFingerprint(manifest fact.FrontendManifest) (string, error) {
	if err := manifest.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(canonicalFrontendManifestValue(manifest))
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

// validateImportedV2Data checks the cross-sequence invariants that cannot be
// established by validating one record at a time. It is deliberately called
// only after the manifest has established counts and limits, and before a
// bundle is returned or published.
func validateImportedV2Data(
	manifest Manifest,
	artifacts []contract.Artifact,
	contributions []contract.Contribution,
	units []evidence.EvidenceUnit,
	frontendManifests []fact.FrontendManifest,
	facts []fact.CanonicalFact,
	extensions []json.RawMessage,
) error {
	artifactIDs := make(map[string]struct{}, len(artifacts))
	artifactsByID := make(map[string]contract.Artifact, len(artifacts))
	for _, artifact := range artifacts {
		if err := validateImportedRelativePath(artifact.Path); err != nil {
			return fmt.Errorf("%w: artifact path is unsafe", ErrInvalid)
		}
		artifactIDs[artifact.ID] = struct{}{}
		artifactsByID[artifact.ID] = artifact
	}

	for _, contribution := range contributions {
		artifact, exists := artifactsByID[contribution.ArtifactID]
		if !exists {
			return fmt.Errorf("%w: contribution references an unknown artifact", ErrInvalidReference)
		}
		if err := validateImportedLocator(contribution.Locator, manifest.Source.ID, artifact.ID); err != nil {
			return fmt.Errorf("%w: contribution locator", err)
		}
	}

	evidenceByID := make(map[string]evidence.EvidenceUnit, len(units))
	for _, unit := range units {
		evidenceByID[unit.ID] = unit
	}

	producerManifests := make(map[string]fact.FrontendManifest, len(frontendManifests))
	for _, frontendManifest := range frontendManifests {
		producerManifests[frontendManifestKey(frontendManifest)] = frontendManifest
	}

	factIDs := make(map[string]struct{}, len(facts))
	for _, canonicalFact := range facts {
		factIDs[canonicalFact.ID] = struct{}{}
	}
	for _, canonicalFact := range facts {
		if canonicalFact.Scope.OrganizationID != manifest.Organization.ID ||
			canonicalFact.Scope.SourceID != manifest.Source.ID ||
			canonicalFact.Scope.SnapshotID != manifest.Snapshot.ID {
			return fmt.Errorf("%w: canonical fact is outside bundle scope", ErrScopeMismatch)
		}
		producer, exists := producerManifests[frontendManifestKeyForProducer(canonicalFact.Producer)]
		if !exists {
			return fmt.Errorf("%w: canonical fact producer is not declared", ErrInvalidReference)
		}
		if !producer.SupportsSourceType(manifest.Source.Type) {
			return fmt.Errorf("%w: canonical fact producer is incompatible with source type", ErrInvalidReference)
		}
		if err := validateImportedParticipant(canonicalFact.Subject, artifactIDs); err != nil {
			return err
		}
		if canonicalFact.Object != nil {
			if err := validateImportedParticipant(*canonicalFact.Object, artifactIDs); err != nil {
				return err
			}
		}
		for _, evidenceRef := range canonicalFact.Evidence {
			unit, exists := evidenceByID[evidenceRef.ID]
			if !exists {
				return fmt.Errorf("%w: canonical fact references unknown evidence", ErrInvalidReference)
			}
			if unit.OrganizationID != canonicalFact.Scope.OrganizationID ||
				unit.SourceID != canonicalFact.Scope.SourceID ||
				unit.SnapshotID != canonicalFact.Scope.SnapshotID {
				return fmt.Errorf("%w: evidence is outside canonical fact scope", ErrScopeMismatch)
			}
			if err := validateImportedLocator(evidenceRef.Locator, canonicalFact.Scope.SourceID, unit.ArtifactID); err != nil {
				return fmt.Errorf("%w: canonical fact evidence locator", err)
			}
			if evidenceRef.Locator != unit.Locator {
				return fmt.Errorf("%w: canonical fact evidence locator differs from evidence", ErrInvalidReference)
			}
		}
		if canonicalFact.Lineage != nil {
			for _, inputID := range canonicalFact.Lineage.InputFactIDs {
				if _, exists := factIDs[inputID]; !exists {
					return fmt.Errorf("%w: canonical fact lineage input is not present", ErrInvalidReference)
				}
			}
		}
	}

	if err := validateImportedExtensions(frontendManifests, extensions); err != nil {
		return err
	}
	return nil
}

func validateImportedParticipant(participant fact.Participant, artifactIDs map[string]struct{}) error {
	if participant.Kind != fact.ParticipantArtifact {
		return nil
	}
	if _, exists := artifactIDs[participant.ID]; !exists {
		return fmt.Errorf("%w: canonical fact artifact participant is not present", ErrInvalidReference)
	}
	return nil
}

func frontendManifestKeyForProducer(producer fact.Producer) string {
	return producer.ID + "\x00" + producer.Version + "\x00" + producer.Method
}

func validateImportedLocator(locator contract.Locator, sourceID, artifactID string) error {
	if err := locator.Validate(); err != nil {
		return fmt.Errorf("%w: malformed locator", ErrInvalid)
	}
	if locator.SourceID != "" && locator.SourceID != sourceID {
		return fmt.Errorf("%w: locator source is outside bundle scope", ErrScopeMismatch)
	}
	if locator.ArtifactID != "" && locator.ArtifactID != artifactID {
		return fmt.Errorf("%w: locator artifact differs from referenced artifact", ErrInvalidReference)
	}
	if err := validateImportedRelativePath(locator.Path); err != nil {
		return fmt.Errorf("%w: locator path is unsafe", ErrInvalid)
	}
	for _, value := range []string{locator.URI, locator.Member} {
		if !utf8.ValidString(value) || containsImportedControl(value) {
			return fmt.Errorf("%w: locator text is malformed", ErrInvalid)
		}
	}
	return nil
}

func validateImportedRelativePath(value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || containsImportedControl(value) {
		return ErrInvalid
	}
	normalized := strings.ReplaceAll(value, "\\", "/")
	if path.IsAbs(normalized) || hasImportedWindowsDrivePrefix(normalized) {
		return ErrInvalid
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return ErrInvalid
		}
	}
	if path.Clean(normalized) == "." {
		return ErrInvalid
	}
	return nil
}

func hasImportedWindowsDrivePrefix(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	return (value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')
}

func containsImportedControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}
