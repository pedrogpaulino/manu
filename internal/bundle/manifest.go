package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// VersionV1Alpha1 is the original Analysis Bundle representation. Version
	// remains an alias for this value so existing callers and fixtures retain
	// their v1alpha1 behavior byte for byte.
	VersionV1Alpha1 = "v1alpha1"
	// VersionV1Alpha2 is the additive envelope carrying frontend manifests,
	// canonical facts, and opaque extension records as separate sequences.
	VersionV1Alpha2 = "v1alpha2"
	// Version is the legacy spelling retained for v1alpha1 callers.
	Version = VersionV1Alpha1

	// ContractVersion is the result contract version carried by a bundle.
	ContractVersion = contract.Version

	// Canonical file names are shared with the legacy result contract. v1alpha2
	// adds independent sequences rather than changing the old files.
	ManifestFileName          = contract.ManifestFileName
	ArtifactsFileName         = contract.ArtifactsFileName
	ContributionsFileName     = contract.ContributionsFileName
	EvidenceFileName          = "evidence.ndjson"
	FrontendManifestsFileName = "frontend_manifests.ndjson"
	CanonicalFactsFileName    = "facts.ndjson"
	ExtensionsFileName        = "extensions.ndjson"
	FrontendManifestFileName  = FrontendManifestsFileName
	FactsFileName             = CanonicalFactsFileName
)

var (
	// ErrInvalid identifies a manifest or bundle that cannot be accepted.
	ErrInvalid = errors.New("bundle: invalid")
	// ErrUnsupportedVersion identifies a bundle representation this package
	// cannot validate.
	ErrUnsupportedVersion = errors.New("bundle: unsupported version")
	// ErrInvalidDigest identifies a digest that is not a lowercase SHA-256.
	ErrInvalidDigest = errors.New("bundle: invalid digest")
	// ErrInvalidFile identifies an unknown, missing, or duplicated sequence
	// descriptor.
	ErrInvalidFile = errors.New("bundle: invalid file")
	// ErrDuplicate identifies a duplicated factual identity.
	ErrDuplicate = errors.New("bundle: duplicate identity")
	// ErrInvalidReference identifies an orphan artifact or contribution
	// reference.
	ErrInvalidReference = errors.New("bundle: invalid reference")
	// ErrScopeMismatch identifies an item outside the manifest scope.
	ErrScopeMismatch = errors.New("bundle: scope mismatch")
	// ErrCountMismatch identifies a count that disagrees with a sequence or
	// the embedded legacy manifest.
	ErrCountMismatch = errors.New("bundle: count mismatch")
	// ErrDigestMismatch identifies a factual digest that differs from the
	// embedded legacy result.
	ErrDigestMismatch = errors.New("bundle: digest mismatch")
	// ErrLimitExceeded identifies a configured byte or count limit.
	ErrLimitExceeded = errors.New("bundle: limit exceeded")
	// ErrMissingRevision identifies a source snapshot without a revision or
	// content hash.
	ErrMissingRevision = errors.New("bundle: missing revision or hash")
	// ErrInvalidExtension identifies an extension record that is malformed or
	// does not match a schema declared by a frontend manifest.
	ErrInvalidExtension = errors.New("bundle: invalid extension")
)

// Organization is the organization boundary recorded by an Analysis Bundle.
// The current unauthenticated mode uses one configured organization, but the
// boundary remains explicit in the wire representation.
type Organization struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// OrganizationRef is a descriptive alias for callers that use reference
// terminology.
type OrganizationRef = Organization

// Analysis identifies the analysis configuration and the revision of the
// analysis input. Revision and Hash are alternative stable identities; Hash,
// when present, is a SHA-256 digest.
type Analysis struct {
	ID              string `json:"id,omitempty"`
	ConfigurationID string `json:"configuration_id"`
	Revision        string `json:"revision,omitempty"`
	Hash            string `json:"hash,omitempty"`
}

// AnalysisMetadata is an alias retained for callers that prefer an explicit
// metadata name.
type AnalysisMetadata = Analysis

// EvidenceState records whether the bundle contains an evidence sequence.
// Limited is the explicit state used when a legacy v1alpha1 result is
// represented without Evidence Units.
type EvidenceState string

const (
	EvidenceStateUnknown   EvidenceState = ""
	EvidenceStateAvailable EvidenceState = "available"
	EvidenceStateLimited   EvidenceState = "limited"
)

// EvidenceMetadata records the evidence availability declaration separately
// from the file descriptors, so a legacy input can remain representable.
type EvidenceMetadata struct {
	State EvidenceState `json:"state"`
}

// File describes one external NDJSON sequence without opening or reading the
// file. Digest is the SHA-256 of the exact serialized sequence. The manifest
// itself is not described here: doing so would make its digest refer to itself
// and therefore impossible to verify.
type File struct {
	Name   string `json:"name"`
	Bytes  int64  `json:"bytes"`
	Count  int64  `json:"count"`
	Digest string `json:"digest"`
}

// Sequence is an alias for callers that use the terminology from the bundle
// transport design.
type Sequence = File

// FileDescriptor is an alias retained for callers that prefer a descriptive
// type name.
type FileDescriptor = File

// Counts records the number of factual entries in each sequence. Artifact
// and contribution counts also remain in the embedded legacy manifest and
// must agree with these values.
type Counts struct {
	ArtifactCount         int64 `json:"artifact_count"`
	ContributionCount     int64 `json:"contribution_count"`
	EvidenceUnitCount     int64 `json:"evidence_unit_count"`
	FrontendManifestCount int64 `json:"frontend_manifest_count,omitempty"`
	CanonicalFactCount    int64 `json:"canonical_fact_count,omitempty"`
	ExtensionCount        int64 `json:"extension_count,omitempty"`
}

// Limits bounds the manifest and external sequences. A zero value means that
// the corresponding dimension is not additionally bounded here; deployment
// configuration normally supplies positive finite values. MaxManifestBytes
// applies to the manifest transport part and is intentionally not checked
// against a self-referential File descriptor.
type Limits struct {
	MaxBundleBytes       int64 `json:"max_bundle_bytes,omitempty"`
	MaxManifestBytes     int64 `json:"max_manifest_bytes,omitempty"`
	MaxEvidenceBytes     int64 `json:"max_evidence_bytes,omitempty"`
	MaxArtifacts         int64 `json:"max_artifacts,omitempty"`
	MaxContributions     int64 `json:"max_contributions,omitempty"`
	MaxEvidenceUnits     int64 `json:"max_evidence_units,omitempty"`
	MaxFrontendManifests int64 `json:"max_frontend_manifests,omitempty"`
	MaxCanonicalFacts    int64 `json:"max_canonical_facts,omitempty"`
	MaxExtensions        int64 `json:"max_extensions,omitempty"`
}

// Manifest extends the current result manifest without copying its source,
// snapshot, coverage, or failure models. The embedded contract.Manifest is
// serialized with its existing JSON field names.
type Manifest struct {
	Version      string       `json:"version"`
	Organization Organization `json:"organization"`
	contract.Manifest
	Analysis      Analysis         `json:"analysis"`
	FactualDigest string           `json:"factual_digest"`
	Files         []File           `json:"files"`
	Counts        Counts           `json:"counts"`
	Limits        Limits           `json:"limits"`
	Evidence      EvidenceMetadata `json:"evidence"`
}

// Bundle is the in-memory validation envelope for a manifest and its factual
// sequences. It does not read, write, or stream files; codec work belongs to a
// later task.
type Bundle struct {
	Manifest          Manifest                `json:"manifest"`
	Artifacts         []contract.Artifact     `json:"artifacts"`
	Contributions     []contract.Contribution `json:"contributions"`
	Evidence          []evidence.EvidenceUnit `json:"evidence,omitempty"`
	FrontendManifests []fact.FrontendManifest `json:"frontend_manifests,omitempty"`
	Facts             []fact.CanonicalFact    `json:"facts,omitempty"`
	Extensions        []json.RawMessage       `json:"extensions,omitempty"`
}

func (m Manifest) rejectV2FieldsForV1() error {
	if m.Version != VersionV1Alpha1 {
		return nil
	}
	if m.Counts.FrontendManifestCount != 0 || m.Counts.CanonicalFactCount != 0 || m.Counts.ExtensionCount != 0 {
		return fmt.Errorf("%w: v1alpha1 manifest contains v1alpha2 counts", ErrUnsupportedVersion)
	}
	if m.Limits.MaxFrontendManifests != 0 || m.Limits.MaxCanonicalFacts != 0 || m.Limits.MaxExtensions != 0 {
		return fmt.Errorf("%w: v1alpha1 manifest contains v1alpha2 limits", ErrUnsupportedVersion)
	}
	for _, file := range m.Files {
		switch file.Name {
		case FrontendManifestsFileName, CanonicalFactsFileName, ExtensionsFileName:
			return fmt.Errorf("%w: v1alpha1 manifest contains %q", ErrUnsupportedVersion, file.Name)
		}
	}
	return nil
}

func rejectV2DataForV1(b Bundle) error {
	if b.Manifest.Version != VersionV1Alpha1 {
		return nil
	}
	if err := b.Manifest.rejectV2FieldsForV1(); err != nil {
		return err
	}
	if len(b.FrontendManifests) != 0 || len(b.Facts) != 0 || len(b.Extensions) != 0 {
		return fmt.Errorf("%w: v1alpha1 bundle contains v1alpha2 sequences", ErrUnsupportedVersion)
	}
	return nil
}

// Validate checks the manifest fields that are independent of sequence
// contents. Use Bundle.Validate to check references, counts, and factual
// digest against the legacy result and Evidence Units.
func (m Manifest) Validate() error {
	switch m.Version {
	case VersionV1Alpha1, VersionV1Alpha2:
	default:
		return fmt.Errorf("%w: got %q", ErrUnsupportedVersion, m.Version)
	}
	if err := m.rejectV2FieldsForV1(); err != nil {
		return err
	}
	if err := validateOrganization(m.Organization); err != nil {
		return err
	}
	if err := validateSourceSnapshot(m.Source, m.Snapshot); err != nil {
		return err
	}
	if m.ArtifactCount < 0 || m.ContributionCount < 0 {
		return fmt.Errorf("%w: legacy artifact or contribution count is negative", ErrLimitExceeded)
	}
	if err := m.Manifest.Validate(); err != nil {
		return fmt.Errorf("%w: legacy manifest: %v", ErrInvalid, err)
	}
	if err := validateAnalysis(m.Analysis, m.Execution.ConfigurationID); err != nil {
		return err
	}
	if !isSHA256Digest(m.FactualDigest) {
		return fmt.Errorf("%w: factual digest", ErrInvalidDigest)
	}
	if err := m.Counts.Validate(); err != nil {
		return err
	}
	if int64(m.ArtifactCount) != m.Counts.ArtifactCount {
		return fmt.Errorf("%w: artifact count %d does not match legacy count %d", ErrCountMismatch, m.Counts.ArtifactCount, m.ArtifactCount)
	}
	if int64(m.ContributionCount) != m.Counts.ContributionCount {
		return fmt.Errorf("%w: contribution count %d does not match legacy count %d", ErrCountMismatch, m.Counts.ContributionCount, m.ContributionCount)
	}
	if err := m.Limits.Validate(); err != nil {
		return err
	}
	if err := validateCountLimits(m.Counts, m.Limits); err != nil {
		return err
	}
	files, err := validateManifestFiles(m.Version, m.Files, m.Counts, m.Limits)
	if err != nil {
		return err
	}
	state := m.EffectiveEvidenceState()
	if err := validateEvidenceMetadata(state, files, m.Counts.EvidenceUnitCount); err != nil {
		return err
	}
	return nil
}

// Normalize fills the bundle version and derives the explicit legacy
// evidence limitation when no evidence sequence was declared. It does not
// compute a file digest or access the filesystem.
func (m *Manifest) Normalize() error {
	if m == nil {
		return fmt.Errorf("%w: nil manifest", ErrInvalid)
	}
	if m.Version == "" {
		m.Version = Version
	}
	if m.Evidence.State == EvidenceStateUnknown {
		m.Evidence.State = m.EffectiveEvidenceState()
	}
	return m.Validate()
}

// EffectiveEvidenceState derives the limited state for a legacy manifest
// that has no evidence descriptor. An explicit state always wins.
func (m Manifest) EffectiveEvidenceState() EvidenceState {
	if m.Evidence.State != EvidenceStateUnknown {
		return m.Evidence.State
	}
	for _, file := range m.Files {
		if file.Name == EvidenceFileName {
			return EvidenceStateAvailable
		}
	}
	return EvidenceStateLimited
}

// EvidenceLimited reports whether this manifest represents a legacy or
// otherwise limited input without transferable Evidence Units.
func (m Manifest) EvidenceLimited() bool {
	return m.EffectiveEvidenceState() == EvidenceStateLimited
}

// Validate checks the full in-memory bundle, including legacy result
// coherence and Evidence Unit references. It performs no I/O.
func (b Bundle) Validate() error {
	if err := rejectV2DataForV1(b); err != nil {
		return err
	}
	if err := b.Manifest.Validate(); err != nil {
		return err
	}
	if err := validateArtifactsAndContributions(b.Manifest, b.Artifacts, b.Contributions); err != nil {
		return err
	}
	result := contract.Result{
		Manifest:      b.Manifest.Manifest,
		Artifacts:     b.Artifacts,
		Contributions: b.Contributions,
	}
	if err := result.Validate(); err != nil {
		return fmt.Errorf("%w: legacy result: %v", ErrInvalid, err)
	}
	if err := validateSequenceCounts(b.Manifest, b.Evidence); err != nil {
		return err
	}
	if err := validateEvidenceUnits(b.Manifest, b.Artifacts, b.Contributions, b.Evidence); err != nil {
		return err
	}
	if b.Manifest.Version == VersionV1Alpha2 {
		if err := validateV2Sequences(b.Manifest, b.FrontendManifests, b.Facts, b.Extensions); err != nil {
			return err
		}
		if err := validateImportedV2Data(
			b.Manifest,
			b.Artifacts,
			b.Contributions,
			b.Evidence,
			b.FrontendManifests,
			b.Facts,
			b.Extensions,
		); err != nil {
			return err
		}
	}
	actualDigest, err := b.FactualDigest()
	if err != nil {
		return fmt.Errorf("%w: factual digest: %v", ErrInvalid, err)
	}
	if actualDigest != b.Manifest.FactualDigest {
		return fmt.Errorf("%w: got %q, want %q", ErrDigestMismatch, b.Manifest.FactualDigest, actualDigest)
	}
	return nil
}

// ValidateBundle validates sequence values against a manifest without
// requiring callers to construct the envelope explicitly.
func (m Manifest) ValidateBundle(
	artifacts []contract.Artifact,
	contributions []contract.Contribution,
	units []evidence.EvidenceUnit,
) error {
	return (Bundle{
		Manifest:      m,
		Artifacts:     artifacts,
		Contributions: contributions,
		Evidence:      units,
	}).Validate()
}

// Validate checks that count limits are non-negative and internally usable.
func (c Counts) Validate() error {
	if c.ArtifactCount < 0 || c.ContributionCount < 0 || c.EvidenceUnitCount < 0 ||
		c.FrontendManifestCount < 0 || c.CanonicalFactCount < 0 || c.ExtensionCount < 0 {
		return fmt.Errorf("%w: counts must not be negative", ErrLimitExceeded)
	}
	return nil
}

// Validate checks that limit values are non-negative.
func (l Limits) Validate() error {
	values := []struct {
		name  string
		value int64
	}{
		{name: "max_bundle_bytes", value: l.MaxBundleBytes},
		{name: "max_manifest_bytes", value: l.MaxManifestBytes},
		{name: "max_evidence_bytes", value: l.MaxEvidenceBytes},
		{name: "max_artifacts", value: l.MaxArtifacts},
		{name: "max_contributions", value: l.MaxContributions},
		{name: "max_evidence_units", value: l.MaxEvidenceUnits},
		{name: "max_frontend_manifests", value: l.MaxFrontendManifests},
		{name: "max_canonical_facts", value: l.MaxCanonicalFacts},
		{name: "max_extensions", value: l.MaxExtensions},
	}
	for _, value := range values {
		if value.value < 0 {
			return fmt.Errorf("%w: %s is negative", ErrLimitExceeded, value.name)
		}
	}
	return nil
}

func validateCountLimits(counts Counts, limits Limits) error {
	values := []struct {
		name  string
		count int64
		limit int64
	}{
		{name: "artifacts", count: counts.ArtifactCount, limit: limits.MaxArtifacts},
		{name: "contributions", count: counts.ContributionCount, limit: limits.MaxContributions},
		{name: "evidence units", count: counts.EvidenceUnitCount, limit: limits.MaxEvidenceUnits},
		{name: "frontend manifests", count: counts.FrontendManifestCount, limit: limits.MaxFrontendManifests},
		{name: "canonical facts", count: counts.CanonicalFactCount, limit: limits.MaxCanonicalFacts},
		{name: "extensions", count: counts.ExtensionCount, limit: limits.MaxExtensions},
	}
	for _, value := range values {
		if value.limit > 0 && value.count > value.limit {
			return fmt.Errorf("%w: %s count %d exceeds %d", ErrLimitExceeded, value.name, value.count, value.limit)
		}
	}
	return nil
}

func validateOrganization(organization Organization) error {
	if err := validateIdentifier("organization id", organization.ID); err != nil {
		return err
	}
	if organization.Name != "" && containsControl(organization.Name) {
		return fmt.Errorf("%w: organization name contains control characters", ErrInvalid)
	}
	return nil
}

func validateSourceSnapshot(source contract.Source, snapshot contract.Snapshot) error {
	if snapshot.SourceID != source.ID {
		return fmt.Errorf("%w: snapshot source id %q does not match source %q", ErrScopeMismatch, snapshot.SourceID, source.ID)
	}
	if source.Hash != "" && !isSHA256Digest(source.Hash) {
		return fmt.Errorf("%w: source hash", ErrInvalidDigest)
	}
	if snapshot.Hash != "" && !isSHA256Digest(snapshot.Hash) {
		return fmt.Errorf("%w: snapshot hash", ErrInvalidDigest)
	}
	if strings.TrimSpace(source.Revision) == "" && strings.TrimSpace(source.Hash) == "" &&
		strings.TrimSpace(snapshot.Revision) == "" && strings.TrimSpace(snapshot.Hash) == "" {
		return ErrMissingRevision
	}
	if source.Revision != "" && snapshot.Revision != "" && source.Revision != snapshot.Revision {
		return fmt.Errorf("%w: source and snapshot revisions differ", ErrScopeMismatch)
	}
	if source.Hash != "" && snapshot.Hash != "" && source.Hash != snapshot.Hash {
		return fmt.Errorf("%w: source and snapshot hashes differ", ErrScopeMismatch)
	}
	return nil
}

func validateAnalysis(analysis Analysis, legacyConfigurationID string) error {
	if err := validateIdentifier("analysis configuration id", analysis.ConfigurationID); err != nil {
		return err
	}
	if analysis.ID != "" {
		if err := validateIdentifier("analysis id", analysis.ID); err != nil {
			return err
		}
	}
	if analysis.Revision == "" && analysis.Hash == "" {
		return ErrMissingRevision
	}
	if analysis.Revision != "" && containsControl(analysis.Revision) {
		return fmt.Errorf("%w: analysis revision contains control characters", ErrInvalid)
	}
	if analysis.Hash != "" && !isSHA256Digest(analysis.Hash) {
		return fmt.Errorf("%w: analysis hash", ErrInvalidDigest)
	}
	if legacyConfigurationID != "" && legacyConfigurationID != analysis.ConfigurationID {
		return fmt.Errorf("%w: analysis configuration id differs from legacy result", ErrScopeMismatch)
	}
	return nil
}

func validateFiles(files []File, counts Counts, limits Limits) (map[string]File, error) {
	return validateManifestFiles(VersionV1Alpha1, files, counts, limits)
}

func validateManifestFiles(version string, files []File, counts Counts, limits Limits) (map[string]File, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("%w: no sequence descriptors", ErrInvalidFile)
	}
	knownNames := map[string]struct{}{
		ArtifactsFileName:     {},
		ContributionsFileName: {},
		EvidenceFileName:      {},
	}
	if version == VersionV1Alpha2 {
		knownNames[FrontendManifestsFileName] = struct{}{}
		knownNames[CanonicalFactsFileName] = struct{}{}
		knownNames[ExtensionsFileName] = struct{}{}
	}
	seen := make(map[string]struct{}, len(files))
	byName := make(map[string]File, len(files))
	var totalBytes int64
	for i, file := range files {
		if _, known := knownNames[file.Name]; !known {
			return nil, fmt.Errorf("%w: file %d has non-canonical name %q", ErrInvalidFile, i, file.Name)
		}
		if _, duplicate := seen[file.Name]; duplicate {
			return nil, fmt.Errorf("%w: file %q", ErrDuplicate, file.Name)
		}
		seen[file.Name] = struct{}{}
		if file.Bytes < 0 || file.Count < 0 {
			return nil, fmt.Errorf("%w: file %q has negative bytes or count", ErrLimitExceeded, file.Name)
		}
		if !isSHA256Digest(file.Digest) {
			return nil, fmt.Errorf("%w: file %q digest", ErrInvalidDigest, file.Name)
		}
		fileLimit := int64(0)
		switch file.Name {
		case EvidenceFileName:
			fileLimit = limits.MaxEvidenceBytes
		case FrontendManifestsFileName:
			fileLimit = limits.MaxBundleBytes
		case CanonicalFactsFileName, ExtensionsFileName:
			fileLimit = limits.MaxBundleBytes
		}
		if fileLimit > 0 && file.Bytes > fileLimit {
			return nil, fmt.Errorf("%w: %s bytes %d exceed %d", ErrLimitExceeded, file.Name, file.Bytes, fileLimit)
		}
		if totalBytes > math.MaxInt64-file.Bytes {
			return nil, fmt.Errorf("%w: total file bytes overflow", ErrLimitExceeded)
		}
		totalBytes += file.Bytes
		byName[file.Name] = file
	}
	requiredFiles := []string{ArtifactsFileName, ContributionsFileName}
	if version == VersionV1Alpha2 {
		requiredFiles = append(requiredFiles, FrontendManifestsFileName, CanonicalFactsFileName, ExtensionsFileName)
	}
	for _, required := range requiredFiles {
		if _, exists := byName[required]; !exists {
			return nil, fmt.Errorf("%w: required file %q is missing", ErrInvalidFile, required)
		}
	}
	if limits.MaxBundleBytes > 0 && totalBytes > limits.MaxBundleBytes {
		return nil, fmt.Errorf("%w: total file bytes exceed configured limit", ErrLimitExceeded)
	}
	if file, exists := byName[EvidenceFileName]; exists && file.Count != counts.EvidenceUnitCount {
		return nil, fmt.Errorf("%w: evidence file count %d does not match manifest count %d", ErrCountMismatch, file.Count, counts.EvidenceUnitCount)
	}
	if file := byName[ArtifactsFileName]; file.Count != counts.ArtifactCount {
		return nil, fmt.Errorf("%w: artifact file count %d does not match manifest count %d", ErrCountMismatch, file.Count, counts.ArtifactCount)
	}
	if file := byName[ContributionsFileName]; file.Count != counts.ContributionCount {
		return nil, fmt.Errorf("%w: contribution file count %d does not match manifest count %d", ErrCountMismatch, file.Count, counts.ContributionCount)
	}
	if version == VersionV1Alpha2 {
		for _, sequence := range []struct {
			name  string
			count int64
		}{
			{name: FrontendManifestsFileName, count: counts.FrontendManifestCount},
			{name: CanonicalFactsFileName, count: counts.CanonicalFactCount},
			{name: ExtensionsFileName, count: counts.ExtensionCount},
		} {
			if file := byName[sequence.name]; file.Count != sequence.count {
				return nil, fmt.Errorf("%w: %s file count %d does not match manifest count %d", ErrCountMismatch, sequence.name, file.Count, sequence.count)
			}
		}
	}
	return byName, nil
}

func validateV2Sequences(
	manifest Manifest,
	frontendManifests []fact.FrontendManifest,
	facts []fact.CanonicalFact,
	extensions []json.RawMessage,
) error {
	if int64(len(frontendManifests)) != manifest.Counts.FrontendManifestCount ||
		int64(len(facts)) != manifest.Counts.CanonicalFactCount ||
		int64(len(extensions)) != manifest.Counts.ExtensionCount {
		return fmt.Errorf("%w: v1alpha2 sequence lengths do not match manifest counts", ErrCountMismatch)
	}
	if manifest.Limits.MaxFrontendManifests > 0 && int64(len(frontendManifests)) > manifest.Limits.MaxFrontendManifests {
		return fmt.Errorf("%w: frontend manifests exceed configured limit", ErrLimitExceeded)
	}
	if manifest.Limits.MaxCanonicalFacts > 0 && int64(len(facts)) > manifest.Limits.MaxCanonicalFacts {
		return fmt.Errorf("%w: canonical facts exceed configured limit", ErrLimitExceeded)
	}
	if manifest.Limits.MaxExtensions > 0 && int64(len(extensions)) > manifest.Limits.MaxExtensions {
		return fmt.Errorf("%w: extensions exceed configured limit", ErrLimitExceeded)
	}
	manifestIDs := make(map[string]struct{}, len(frontendManifests))
	for index, frontendManifest := range frontendManifests {
		if err := frontendManifest.Validate(); err != nil {
			return fmt.Errorf("%w: frontend manifest %d: %v", ErrInvalid, index, err)
		}
		if _, exists := manifestIDs[frontendManifest.ID]; exists {
			return fmt.Errorf("%w: frontend manifest %q", ErrDuplicate, frontendManifest.ID)
		}
		manifestIDs[frontendManifest.ID] = struct{}{}
	}
	factIDs := make(map[string]struct{}, len(facts))
	for index, canonicalFact := range facts {
		if err := canonicalFact.Validate(); err != nil {
			return fmt.Errorf("%w: canonical fact %d: %v", ErrInvalid, index, err)
		}
		if _, exists := factIDs[canonicalFact.ID]; exists {
			return fmt.Errorf("%w: canonical fact %q", ErrDuplicate, canonicalFact.ID)
		}
		factIDs[canonicalFact.ID] = struct{}{}
	}
	for index, extension := range extensions {
		if len(extension) == 0 || !json.Valid(extension) {
			return fmt.Errorf("%w: extension %d", ErrInvalidExtension, index)
		}
	}
	return nil
}

func validateEvidenceMetadata(state EvidenceState, files map[string]File, count int64) error {
	switch state {
	case EvidenceStateAvailable:
		if _, exists := files[EvidenceFileName]; !exists {
			return fmt.Errorf("%w: available evidence has no evidence sequence", ErrInvalidFile)
		}
	case EvidenceStateLimited:
		if _, exists := files[EvidenceFileName]; exists {
			return fmt.Errorf("%w: limited evidence must not declare evidence sequence", ErrInvalidFile)
		}
		if count != 0 {
			return fmt.Errorf("%w: limited evidence count is %d", ErrCountMismatch, count)
		}
	default:
		return fmt.Errorf("%w: evidence state %q", ErrInvalid, state)
	}
	return nil
}

func validateArtifactsAndContributions(
	manifest Manifest,
	artifacts []contract.Artifact,
	contributions []contract.Contribution,
) error {
	artifactIDs := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if !isSHA256Digest(artifact.Hash) {
			return fmt.Errorf("%w: artifact %q hash", ErrInvalidDigest, artifact.ID)
		}
		if _, exists := artifactIDs[artifact.ID]; exists {
			return fmt.Errorf("%w: artifact %q", ErrDuplicate, artifact.ID)
		}
		artifactIDs[artifact.ID] = struct{}{}
	}
	contributionIDs := make(map[string]struct{}, len(contributions))
	for _, contribution := range contributions {
		if _, exists := contributionIDs[contribution.ID]; exists {
			return fmt.Errorf("%w: contribution %q", ErrDuplicate, contribution.ID)
		}
		contributionIDs[contribution.ID] = struct{}{}
	}
	if int64(len(artifacts)) != manifest.Counts.ArtifactCount || int64(len(contributions)) != manifest.Counts.ContributionCount {
		return fmt.Errorf("%w: sequence lengths do not match manifest counts", ErrCountMismatch)
	}
	if manifest.Limits.MaxArtifacts > 0 && int64(len(artifacts)) > manifest.Limits.MaxArtifacts {
		return fmt.Errorf("%w: artifacts exceed configured limit", ErrLimitExceeded)
	}
	if manifest.Limits.MaxContributions > 0 && int64(len(contributions)) > manifest.Limits.MaxContributions {
		return fmt.Errorf("%w: contributions exceed configured limit", ErrLimitExceeded)
	}
	return nil
}

func validateSequenceCounts(manifest Manifest, units []evidence.EvidenceUnit) error {
	if int64(len(units)) != manifest.Counts.EvidenceUnitCount {
		return fmt.Errorf("%w: evidence sequence length %d does not match manifest count %d", ErrCountMismatch, len(units), manifest.Counts.EvidenceUnitCount)
	}
	if manifest.Limits.MaxEvidenceUnits > 0 && int64(len(units)) > manifest.Limits.MaxEvidenceUnits {
		return fmt.Errorf("%w: evidence units exceed configured limit", ErrLimitExceeded)
	}
	return nil
}

func validateEvidenceUnits(
	manifest Manifest,
	artifacts []contract.Artifact,
	contributions []contract.Contribution,
	units []evidence.EvidenceUnit,
) error {
	artifactIDs := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		artifactIDs[artifact.ID] = struct{}{}
	}
	contributionsByID := make(map[string]contract.Contribution, len(contributions))
	for _, contribution := range contributions {
		contributionsByID[contribution.ID] = contribution
	}
	seen := make(map[string]struct{}, len(units))
	for index, unit := range units {
		if unit.OrganizationID != manifest.Organization.ID || unit.SourceID != manifest.Source.ID || unit.SnapshotID != manifest.Snapshot.ID {
			return fmt.Errorf("%w: evidence unit %q is outside manifest scope", ErrScopeMismatch, unit.ID)
		}
		if _, exists := artifactIDs[unit.ArtifactID]; !exists {
			return fmt.Errorf("%w: evidence unit %q references artifact %q", ErrInvalidReference, unit.ID, unit.ArtifactID)
		}
		contribution, exists := contributionsByID[unit.Contribution.ID]
		if !exists {
			return fmt.Errorf("%w: evidence unit %q references contribution %q", ErrInvalidReference, unit.ID, unit.Contribution.ID)
		}
		if contribution.ArtifactID != unit.Contribution.ArtifactID ||
			contribution.AnalyzerID != unit.Contribution.AnalyzerID ||
			contribution.AnalyzerVersion != unit.Contribution.AnalyzerVersion ||
			contribution.Method != unit.Contribution.Method {
			return fmt.Errorf("%w: evidence unit %q contribution metadata differs from contribution %q", ErrInvalidReference, unit.ID, unit.Contribution.ID)
		}
		if err := unit.Validate(); err != nil {
			return fmt.Errorf("%w: evidence unit %d: %v", ErrInvalid, index, err)
		}
		if _, duplicate := seen[unit.ID]; duplicate {
			return fmt.Errorf("%w: evidence unit %q", ErrDuplicate, unit.ID)
		}
		seen[unit.ID] = struct{}{}
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalid, name)
	}
	if containsControl(value) || strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return fmt.Errorf("%w: %s contains whitespace or control characters", ErrInvalid, name)
	}
	return nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func isSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
