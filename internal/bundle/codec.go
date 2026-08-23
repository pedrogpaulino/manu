package bundle

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// DefaultMaxBundleBytes bounds legacy and otherwise unconfigured bundle
	// sequence data while it is decoded from an untrusted directory.
	DefaultMaxBundleBytes int64 = 64 << 20
	// DefaultMaxManifestBytes bounds the manifest transport part before its
	// declared limits can be trusted.
	DefaultMaxManifestBytes int64 = 1 << 20
	// DefaultMaxEvidenceBytes bounds an evidence sequence when no explicit
	// installation limit is supplied.
	DefaultMaxEvidenceBytes int64 = 256 << 10
	// DefaultMaxArtifacts and DefaultMaxContributions bound legacy sequences
	// that do not carry bundle-level count limits.
	DefaultMaxArtifacts     int64 = 100_000
	DefaultMaxContributions int64 = 100_000
	// DefaultMaxEvidenceUnits matches the local configuration default.
	DefaultMaxEvidenceUnits int64 = 10_000
	// DefaultMaxFrontendManifests, DefaultMaxCanonicalFacts, and
	// DefaultMaxExtensions bound the additional v1alpha2 sequences when no
	// deployment-specific limits are supplied.
	DefaultMaxFrontendManifests int64 = 10_000
	DefaultMaxCanonicalFacts    int64 = 100_000
	DefaultMaxExtensions        int64 = 100_000
)

// Options controls bundle reads. Limits are applied in addition to the
// limits declared by an extended manifest; the stricter value wins. Zero
// limits receive the safe DefaultMax* values before any input is read. The
// Organization and Analysis fields are used only when a legacy result is
// read. A legacy result has no bundle organization or analysis envelope, so
// the caller must provide the missing boundary explicitly or the values must
// be derivable from valid legacy metadata.
type Options struct {
	Limits Limits

	// ImportExpectation is applied only by ReadImportedBundle. It is kept out
	// of the ordinary ReadBundle contract so callers must opt into the
	// explicit authorization boundary.
	ImportExpectation *ImportExpectation `json:"-"`

	// Organization is the explicit organization boundary for legacy result
	// directories. Extended manifests carry this value themselves.
	Organization Organization
	// OrganizationID is a convenience for legacy callers that only have the
	// configured boundary ID. It is never inferred from a path or source.
	OrganizationID string
	Analysis       Analysis
}

// ReadOptions is the descriptive spelling used by callers that keep codec
// options alongside other read inputs.
type ReadOptions = Options

// ErrSizeMismatch identifies a sequence whose serialized byte count differs
// from its manifest descriptor.
var ErrSizeMismatch = errors.New("bundle: size mismatch")

// WriteBundle writes a complete Analysis Bundle into directory. Sequence
// files are serialized one item at a time into same-directory temporary
// files. The manifest is renamed last, after all sequence descriptors and the
// factual digest have been calculated from the bytes actually written.
func WriteBundle(ctx context.Context, directory string, input Bundle) error {
	return writeBundle(ctx, directory, input, nil)
}

// WriteImportedBundle writes a bundle only after validating it against the
// caller-provided import expectation. Invalid input is rejected before the
// destination directory is created or any existing bundle is touched.
func WriteImportedBundle(ctx context.Context, directory string, input Bundle, expectation ImportExpectation) error {
	if err := expectation.Validate(); err != nil {
		return err
	}
	return writeBundle(ctx, directory, input, &expectation)
}

func writeBundle(ctx context.Context, directory string, input Bundle, expectation *ImportExpectation) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if strings.TrimSpace(directory) == "" {
		return fmt.Errorf("writing bundle: %w: output directory is required", ErrInvalid)
	}

	working, err := prepareBundleForWrite(input)
	if err != nil {
		return fmt.Errorf("preparing bundle: %w", err)
	}
	if expectation != nil {
		working.Manifest.Limits = stricterLimits(defaultLimitsV2(working.Manifest.Limits), defaultLimitsV2(expectation.Limits))
		if err := validateImportExpectationBeforeWrite(working, *expectation); err != nil {
			return fmt.Errorf("validating imported bundle: %w", err)
		}
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("creating bundle directory %q: %w", directory, err)
	}

	temporary := make([]bundleTemp, 0, 4)
	var publication *bundlePublication
	defer func() {
		if publication != nil && !publication.committed {
			_ = publication.rollback()
		}
		for _, file := range temporary {
			if !file.published {
				_ = os.Remove(file.name)
			}
		}
	}()

	stats := make(map[string]sequenceStats, 6)
	writeSequence := func(name string, values any) error {
		finalPath := filepath.Join(directory, name)
		file, sequence, writeErr := writeSequenceTemp(ctx, directory, finalPath, values)
		if writeErr != nil {
			return writeErr
		}
		temporary = append(temporary, file)
		stats[name] = sequence
		return nil
	}
	if err := writeSequence(ArtifactsFileName, working.Artifacts); err != nil {
		return fmt.Errorf("writing %s: %w", ArtifactsFileName, err)
	}
	if err := writeSequence(ContributionsFileName, working.Contributions); err != nil {
		return fmt.Errorf("writing %s: %w", ContributionsFileName, err)
	}
	if working.Manifest.Evidence.State == EvidenceStateAvailable {
		if err := writeSequence(EvidenceFileName, working.Evidence); err != nil {
			return fmt.Errorf("writing %s: %w", EvidenceFileName, err)
		}
	}
	if working.Manifest.Version == VersionV1Alpha2 {
		if err := writeSequence(FrontendManifestsFileName, working.FrontendManifests); err != nil {
			return fmt.Errorf("writing %s: %w", FrontendManifestsFileName, err)
		}
		if err := writeSequence(CanonicalFactsFileName, working.Facts); err != nil {
			return fmt.Errorf("writing %s: %w", CanonicalFactsFileName, err)
		}
		if err := writeSequence(ExtensionsFileName, working.Extensions); err != nil {
			return fmt.Errorf("writing %s: %w", ExtensionsFileName, err)
		}
	}

	if working.Manifest.Version == VersionV1Alpha2 {
		working.Manifest.Files = manifestFilesV2(stats, working.Manifest.Evidence.State == EvidenceStateAvailable)
	} else {
		working.Manifest.Files = manifestFiles(stats, working.Manifest.Evidence.State == EvidenceStateAvailable)
	}
	if err := working.Manifest.Validate(); err != nil {
		return fmt.Errorf("validating written manifest: %w", err)
	}
	manifestBytes, err := marshalManifest(working.Manifest)
	if err != nil {
		return fmt.Errorf("encoding manifest: %w", err)
	}
	if working.Manifest.Limits.MaxManifestBytes > 0 && int64(len(manifestBytes)) > working.Manifest.Limits.MaxManifestBytes {
		return fmt.Errorf("encoding manifest: %w: manifest bytes %d exceed %d", ErrLimitExceeded, len(manifestBytes), working.Manifest.Limits.MaxManifestBytes)
	}
	manifestPath := filepath.Join(directory, ManifestFileName)
	manifestTemp, err := writeBytesTemp(ctx, directory, manifestPath, manifestBytes)
	if err != nil {
		return fmt.Errorf("writing %s: %w", ManifestFileName, err)
	}
	temporary = append(temporary, manifestTemp)

	removeAfterCommit := []string(nil)
	if working.Manifest.Evidence.State == EvidenceStateLimited {
		removeAfterCommit = []string{filepath.Join(directory, EvidenceFileName)}
	}
	publication, err = preparePublication(ctx, directory, temporary, removeAfterCommit)
	if err != nil {
		return err
	}
	if err := publication.publish(ctx); err != nil {
		return err
	}
	publication.commit()
	return nil
}

// ReadBundle reads an extended Analysis Bundle or a legacy contract result
// from directory. Decoding is incremental for all NDJSON files. At most one
// optional Options value may be supplied; omitted or zero limits receive the
// safe DefaultMax* values. Legacy bundles still require an explicit
// organization boundary.
func ReadBundle(ctx context.Context, directory string, options ...Options) (Bundle, error) {
	var empty Bundle
	if err := contextError(ctx); err != nil {
		return empty, err
	}
	if strings.TrimSpace(directory) == "" {
		return empty, fmt.Errorf("reading bundle: %w: output directory is required", ErrInvalid)
	}
	if len(options) > 1 {
		return empty, fmt.Errorf("reading bundle: %w: at most one options value is allowed", ErrInvalid)
	}
	var option Options
	if len(options) == 1 {
		option = options[0]
	}
	if err := option.Limits.Validate(); err != nil {
		return empty, err
	}
	manifestPath := filepath.Join(directory, ManifestFileName)
	option.Limits = defaultLimits(option.Limits)
	raw, manifestBytes, err := readManifestRaw(ctx, manifestPath, option.Limits.MaxManifestBytes)
	if err != nil {
		return empty, err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return empty, fmt.Errorf("decoding manifest %q: %w: %v", manifestPath, ErrInvalid, err)
	}
	if _, extended := fields["version"]; extended {
		return readExtendedBundle(ctx, directory, raw, manifestBytes, option)
	}
	if field := firstBundleOnlyField(fields); field != "" {
		return empty, fmt.Errorf("manifest field %q requires bundle version: %w", field, ErrUnsupportedVersion)
	}
	return readLegacyBundle(ctx, directory, raw, manifestBytes, option)
}

// Write is a concise alias for WriteBundle for code that treats a bundle
// directory as the codec resource itself.
func Write(ctx context.Context, directory string, input Bundle) error {
	return WriteBundle(ctx, directory, input)
}

// Read is a concise alias for ReadBundle.
func Read(ctx context.Context, directory string, options ...Options) (Bundle, error) {
	return ReadBundle(ctx, directory, options...)
}

type sequenceStats struct {
	Bytes  int64
	Count  int64
	Digest string
}

type bundleTemp struct {
	name      string
	finalPath string
	published bool
}

type bundleBackup struct {
	finalPath  string
	backupPath string
}

type bundlePublication struct {
	files     []*bundleTemp
	backups   []bundleBackup
	committed bool
}

func prepareBundleForWrite(input Bundle) (Bundle, error) {
	working := cloneBundle(input)
	legacy := contract.Result{
		Manifest:      working.Manifest.Manifest,
		Artifacts:     working.Artifacts,
		Contributions: working.Contributions,
	}
	if err := legacy.Normalize(); err != nil {
		return Bundle{}, fmt.Errorf("normalizing legacy result: %w", err)
	}
	working.Manifest.Manifest = legacy.Manifest
	working.Artifacts = legacy.Artifacts
	working.Contributions = legacy.Contributions
	if working.Manifest.Version == "" {
		working.Manifest.Version = Version
	}
	if working.Manifest.Version != VersionV1Alpha1 && working.Manifest.Version != VersionV1Alpha2 {
		return Bundle{}, fmt.Errorf("%w: got %q", ErrUnsupportedVersion, working.Manifest.Version)
	}
	if err := rejectV2DataForV1(working); err != nil {
		return Bundle{}, err
	}
	if working.Manifest.Organization.ID == "" {
		return Bundle{}, fmt.Errorf("%w: organization id is required", ErrInvalid)
	}
	if working.Manifest.Analysis.ConfigurationID == "" {
		working.Manifest.Analysis.ConfigurationID = working.Manifest.Execution.ConfigurationID
	}
	if working.Manifest.Analysis.Revision == "" && working.Manifest.Analysis.Hash == "" {
		working.Manifest.Analysis.Revision = firstNonEmpty(
			working.Manifest.Snapshot.Revision,
			working.Manifest.Source.Revision,
		)
		if working.Manifest.Analysis.Revision == "" {
			working.Manifest.Analysis.Hash = firstValidDigest(
				working.Manifest.Snapshot.Hash,
				working.Manifest.Source.Hash,
			)
		}
	}
	working.Manifest.ArtifactCount = len(working.Artifacts)
	working.Manifest.ContributionCount = len(working.Contributions)
	working.Manifest.Counts = Counts{
		ArtifactCount:     int64(len(working.Artifacts)),
		ContributionCount: int64(len(working.Contributions)),
		EvidenceUnitCount: int64(len(working.Evidence)),
	}
	if working.Manifest.Version == VersionV1Alpha2 {
		working.Manifest.Counts.FrontendManifestCount = int64(len(working.FrontendManifests))
		working.Manifest.Counts.CanonicalFactCount = int64(len(working.Facts))
		working.Manifest.Counts.ExtensionCount = int64(len(working.Extensions))
		working.Manifest.Limits = defaultLimitsV2(working.Manifest.Limits)
		for index := range working.FrontendManifests {
			working.FrontendManifests[index] = canonicalFrontendManifestValue(working.FrontendManifests[index])
		}
		sort.SliceStable(working.FrontendManifests, func(left, right int) bool {
			return frontendManifestKey(working.FrontendManifests[left]) < frontendManifestKey(working.FrontendManifests[right])
		})
		sort.SliceStable(working.Facts, func(left, right int) bool {
			return working.Facts[left].ID < working.Facts[right].ID
		})
		for index := range working.Facts {
			canonicalBytes, err := fact.CanonicalBytes(working.Facts[index])
			if err != nil {
				return Bundle{}, fmt.Errorf("canonicalizing fact %d: %w", index, err)
			}
			var canonicalFact fact.CanonicalFact
			if err := json.Unmarshal(canonicalBytes, &canonicalFact); err != nil {
				return Bundle{}, fmt.Errorf("canonicalizing fact %d: %w", index, err)
			}
			working.Facts[index] = canonicalFact
		}
		canonicalExtensions, err := canonicalExtensionsForDigest(working.Extensions)
		if err != nil {
			return Bundle{}, err
		}
		working.Extensions = canonicalExtensions
	} else {
		working.Manifest.Limits = defaultLimits(working.Manifest.Limits)
	}
	if err := working.Manifest.Limits.Validate(); err != nil {
		return Bundle{}, err
	}
	if err := validateCountLimits(working.Manifest.Counts, working.Manifest.Limits); err != nil {
		return Bundle{}, err
	}
	if len(working.Evidence) == 0 && working.Manifest.Evidence.State == EvidenceStateUnknown {
		working.Manifest.Evidence.State = EvidenceStateLimited
	}
	if len(working.Evidence) > 0 && working.Manifest.Evidence.State == EvidenceStateUnknown {
		working.Manifest.Evidence.State = EvidenceStateAvailable
	}
	if working.Manifest.Evidence.State == EvidenceStateLimited && len(working.Evidence) != 0 {
		return Bundle{}, fmt.Errorf("%w: limited evidence cannot contain units", ErrInvalidFile)
	}
	if working.Manifest.Evidence.State == EvidenceStateAvailable && working.Manifest.Evidence.State != working.Manifest.EffectiveEvidenceState() {
		return Bundle{}, fmt.Errorf("%w: invalid evidence state", ErrInvalid)
	}
	for index := range working.Evidence {
		if err := working.Evidence[index].ValidatePrepared(); err != nil {
			return Bundle{}, fmt.Errorf("%w: evidence unit %d is not prepared: %w", ErrInvalid, index, err)
		}
	}
	sort.SliceStable(working.Evidence, func(left, right int) bool {
		return working.Evidence[left].ID < working.Evidence[right].ID
	})
	if err := validateOrganization(working.Manifest.Organization); err != nil {
		return Bundle{}, err
	}
	if err := validateSourceSnapshot(working.Manifest.Source, working.Manifest.Snapshot); err != nil {
		return Bundle{}, err
	}
	if err := validateAnalysis(working.Manifest.Analysis, working.Manifest.Execution.ConfigurationID); err != nil {
		return Bundle{}, err
	}
	if err := validateArtifactsAndContributions(working.Manifest, working.Artifacts, working.Contributions); err != nil {
		return Bundle{}, err
	}
	if err := validateEvidenceUnits(working.Manifest, working.Artifacts, working.Contributions, working.Evidence); err != nil {
		return Bundle{}, err
	}
	if working.Manifest.Version == VersionV1Alpha2 {
		if err := validateV2Sequences(working.Manifest, working.FrontendManifests, working.Facts, working.Extensions); err != nil {
			return Bundle{}, err
		}
		if err := validateImportedV2Data(
			working.Manifest,
			working.Artifacts,
			working.Contributions,
			working.Evidence,
			working.FrontendManifests,
			working.Facts,
			working.Extensions,
		); err != nil {
			return Bundle{}, err
		}
	}
	legacy = contract.Result{
		Manifest:      working.Manifest.Manifest,
		Artifacts:     working.Artifacts,
		Contributions: working.Contributions,
	}
	digest, err := working.FactualDigest()
	if err != nil {
		return Bundle{}, err
	}
	working.Manifest.FactualDigest = digest
	return working, nil
}

func cloneBundle(input Bundle) Bundle {
	output := input
	output.Manifest.Files = append([]File(nil), input.Manifest.Files...)
	output.Artifacts = append([]contract.Artifact(nil), input.Artifacts...)
	output.Contributions = append([]contract.Contribution(nil), input.Contributions...)
	output.Evidence = append([]evidence.EvidenceUnit(nil), input.Evidence...)
	output.FrontendManifests = cloneFrontendManifests(input.FrontendManifests)
	output.Facts = cloneCanonicalFacts(input.Facts)
	output.Extensions = cloneExtensions(input.Extensions)
	output.Manifest.Coverage = append([]contract.Coverage(nil), input.Manifest.Coverage...)
	output.Manifest.Gaps = append([]contract.Gap(nil), input.Manifest.Gaps...)
	output.Manifest.Failures = append([]contract.Failure(nil), input.Manifest.Failures...)
	for i := range output.Manifest.Coverage {
		if input.Manifest.Coverage[i].Locator != nil {
			locator := *input.Manifest.Coverage[i].Locator
			output.Manifest.Coverage[i].Locator = &locator
		}
	}
	for i := range output.Manifest.Gaps {
		if input.Manifest.Gaps[i].Locator != nil {
			locator := *input.Manifest.Gaps[i].Locator
			output.Manifest.Gaps[i].Locator = &locator
		}
	}
	for i := range output.Manifest.Failures {
		if input.Manifest.Failures[i].Locator != nil {
			locator := *input.Manifest.Failures[i].Locator
			output.Manifest.Failures[i].Locator = &locator
		}
	}
	for i := range output.Contributions {
		output.Contributions[i].Value = append(json.RawMessage(nil), input.Contributions[i].Value...)
	}
	// Evidence is immutable by value, and all its fields are value types.
	return output
}

func cloneFrontendManifests(manifests []fact.FrontendManifest) []fact.FrontendManifest {
	result := append([]fact.FrontendManifest(nil), manifests...)
	for index := range result {
		result[index].SourceTypes = append([]string(nil), manifests[index].SourceTypes...)
		result[index].Families = append([]string(nil), manifests[index].Families...)
		result[index].Versions = append([]string(nil), manifests[index].Versions...)
		result[index].Capabilities = append([]contract.Dimension(nil), manifests[index].Capabilities...)
		result[index].Limitations = append([]string(nil), manifests[index].Limitations...)
		result[index].Predicates = append([]fact.Predicate(nil), manifests[index].Predicates...)
		result[index].Extensions = append([]fact.ExtensionSchema(nil), manifests[index].Extensions...)
	}
	return result
}

func cloneCanonicalFacts(facts []fact.CanonicalFact) []fact.CanonicalFact {
	result := append([]fact.CanonicalFact(nil), facts...)
	for index := range result {
		if facts[index].Object != nil {
			object := *facts[index].Object
			result[index].Object = &object
		}
		if facts[index].Value != nil {
			value := *facts[index].Value
			result[index].Value = &value
		}
		result[index].Qualifiers = append([]fact.Qualifier(nil), facts[index].Qualifiers...)
		result[index].Evidence = append([]fact.EvidenceRef(nil), facts[index].Evidence...)
		if facts[index].Lineage != nil {
			lineage := *facts[index].Lineage
			lineage.InputFactIDs = append([]string(nil), facts[index].Lineage.InputFactIDs...)
			result[index].Lineage = &lineage
		}
	}
	return result
}

func cloneExtensions(extensions []json.RawMessage) []json.RawMessage {
	result := make([]json.RawMessage, len(extensions))
	for index := range extensions {
		result[index] = append(json.RawMessage(nil), extensions[index]...)
	}
	return result
}

func preparePublication(ctx context.Context, directory string, files []bundleTemp, removeAfterCommit []string) (*bundlePublication, error) {
	publication := &bundlePublication{files: make([]*bundleTemp, len(files))}
	for index := range files {
		publication.files[index] = &files[index]
	}
	paths := make([]string, 0, len(files)+len(removeAfterCommit))
	seen := make(map[string]struct{}, len(files)+len(removeAfterCommit))
	for _, file := range files {
		if _, exists := seen[file.finalPath]; !exists {
			seen[file.finalPath] = struct{}{}
			paths = append(paths, file.finalPath)
		}
	}
	for _, path := range removeAfterCommit {
		if _, exists := seen[path]; !exists {
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	for _, finalPath := range paths {
		if err := contextError(ctx); err != nil {
			_ = publication.rollback()
			return nil, err
		}
		info, err := os.Lstat(finalPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			_ = publication.rollback()
			return nil, fmt.Errorf("checking existing bundle file %q: %w", finalPath, err)
		}
		if info.IsDir() {
			_ = publication.rollback()
			return nil, fmt.Errorf("existing bundle file %q: %w", finalPath, ErrInvalidFile)
		}
		backupPath, err := createRollbackPath(directory, finalPath)
		if err != nil {
			_ = publication.rollback()
			return nil, err
		}
		if err := os.Rename(finalPath, backupPath); err != nil {
			_ = os.Remove(backupPath)
			_ = publication.rollback()
			return nil, fmt.Errorf("backing up bundle file %q: %w", finalPath, err)
		}
		publication.backups = append(publication.backups, bundleBackup{
			finalPath:  finalPath,
			backupPath: backupPath,
		})
	}
	return publication, nil
}

func (p *bundlePublication) publish(ctx context.Context) error {
	if p == nil || len(p.files) == 0 {
		return fmt.Errorf("publishing bundle: %w", ErrInvalid)
	}
	for _, file := range p.files {
		if err := contextError(ctx); err != nil {
			return err
		}
		if err := publishTemp(file); err != nil {
			return err
		}
	}
	return nil
}

func (p *bundlePublication) commit() {
	if p == nil {
		return
	}
	// Mark committed before cleanup: a cleanup failure must not trigger a
	// rollback after the new manifest has become authoritative.
	p.committed = true
	for _, backup := range p.backups {
		_ = os.Remove(backup.backupPath)
	}
}

func (p *bundlePublication) rollback() error {
	if p == nil || p.committed {
		return nil
	}
	var rollbackErrors []error
	for index := len(p.files) - 1; index >= 0; index-- {
		file := p.files[index]
		if !file.published {
			continue
		}
		if err := os.Remove(file.finalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("removing published bundle file %q: %w", file.finalPath, err))
		}
		file.published = false
	}
	for index := len(p.backups) - 1; index >= 0; index-- {
		backup := p.backups[index]
		if _, err := os.Lstat(backup.backupPath); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("checking bundle backup %q: %w", backup.backupPath, err))
			continue
		}
		if err := os.Rename(backup.backupPath, backup.finalPath); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restoring bundle file %q: %w", backup.finalPath, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func createRollbackPath(directory, finalPath string) (string, error) {
	file, err := os.CreateTemp(directory, "."+filepath.Base(finalPath)+".rollback-*")
	if err != nil {
		return "", fmt.Errorf("creating rollback file for %q: %w", finalPath, err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("closing rollback file for %q: %w", finalPath, err)
	}
	if err := os.Remove(name); err != nil {
		return "", fmt.Errorf("preparing rollback file for %q: %w", finalPath, err)
	}
	return name, nil
}

func writeSequenceTemp(ctx context.Context, directory, finalPath string, values any) (bundleTemp, sequenceStats, error) {
	file, err := createTemp(directory, finalPath)
	if err != nil {
		return bundleTemp{}, sequenceStats{}, err
	}
	remove := true
	defer func() {
		if remove {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()

	hash := sha256.New()
	counted := &countingWriter{writer: io.MultiWriter(file, hash)}
	encoder := json.NewEncoder(counted)
	var stats sequenceStats
	writeValue := func(value any, index int64) error {
		if err := contextError(ctx); err != nil {
			return fmt.Errorf("sequence item %d: %w", index, err)
		}
		if err := encoder.Encode(value); err != nil {
			return fmt.Errorf("encoding sequence item %d: %w", index, err)
		}
		stats.Count++
		return nil
	}
	switch typed := values.(type) {
	case []contract.Artifact:
		for index := range typed {
			if err := writeValue(typed[index], int64(index)); err != nil {
				return bundleTemp{}, sequenceStats{}, err
			}
		}
	case []contract.Contribution:
		for index := range typed {
			if err := writeValue(typed[index], int64(index)); err != nil {
				return bundleTemp{}, sequenceStats{}, err
			}
		}
	case []evidence.EvidenceUnit:
		for index := range typed {
			if err := writeValue(typed[index], int64(index)); err != nil {
				return bundleTemp{}, sequenceStats{}, err
			}
		}
	case []fact.FrontendManifest:
		for index := range typed {
			if err := writeValue(typed[index], int64(index)); err != nil {
				return bundleTemp{}, sequenceStats{}, err
			}
		}
	case []fact.CanonicalFact:
		for index := range typed {
			if err := writeValue(typed[index], int64(index)); err != nil {
				return bundleTemp{}, sequenceStats{}, err
			}
		}
	case []json.RawMessage:
		for index := range typed {
			if len(typed[index]) == 0 || !json.Valid(typed[index]) {
				return bundleTemp{}, sequenceStats{}, fmt.Errorf("%w: extension %d", ErrInvalidExtension, index)
			}
			if err := writeValue(typed[index], int64(index)); err != nil {
				return bundleTemp{}, sequenceStats{}, err
			}
		}
	default:
		return bundleTemp{}, sequenceStats{}, fmt.Errorf("%w: unsupported sequence type %T", ErrInvalid, values)
	}
	if err := contextError(ctx); err != nil {
		return bundleTemp{}, sequenceStats{}, err
	}
	if err := file.Sync(); err != nil {
		return bundleTemp{}, sequenceStats{}, fmt.Errorf("syncing temporary sequence: %w", err)
	}
	if err := file.Close(); err != nil {
		return bundleTemp{}, sequenceStats{}, fmt.Errorf("closing temporary sequence: %w", err)
	}
	stats.Bytes = counted.bytes
	stats.Digest = hex.EncodeToString(hash.Sum(nil))
	remove = false
	return bundleTemp{name: file.Name(), finalPath: finalPath}, stats, nil
}

func writeBytesTemp(ctx context.Context, directory, finalPath string, content []byte) (bundleTemp, error) {
	file, err := createTemp(directory, finalPath)
	if err != nil {
		return bundleTemp{}, err
	}
	remove := true
	defer func() {
		if remove {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()
	if err := contextError(ctx); err != nil {
		return bundleTemp{}, err
	}
	written, err := file.Write(content)
	if err != nil {
		return bundleTemp{}, err
	}
	if written != len(content) {
		return bundleTemp{}, io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return bundleTemp{}, fmt.Errorf("syncing temporary manifest: %w", err)
	}
	if err := file.Close(); err != nil {
		return bundleTemp{}, fmt.Errorf("closing temporary manifest: %w", err)
	}
	remove = false
	return bundleTemp{name: file.Name(), finalPath: finalPath}, nil
}

func createTemp(directory, finalPath string) (*os.File, error) {
	base := filepath.Base(finalPath)
	file, err := os.CreateTemp(directory, "."+base+".tmp-")
	if err != nil {
		return nil, fmt.Errorf("creating temporary %q: %w", finalPath, err)
	}
	return file, nil
}

func publishTemp(file *bundleTemp) error {
	if file == nil {
		return fmt.Errorf("publishing temporary bundle file: %w", ErrInvalid)
	}
	if err := os.Rename(file.name, file.finalPath); err != nil {
		return fmt.Errorf("publishing bundle file %q: %w", file.finalPath, err)
	}
	file.published = true
	return nil
}

func marshalManifest(manifest Manifest) ([]byte, error) {
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func manifestFiles(stats map[string]sequenceStats, evidenceAvailable bool) []File {
	files := []File{
		statsFile(ArtifactsFileName, stats[ArtifactsFileName]),
		statsFile(ContributionsFileName, stats[ContributionsFileName]),
	}
	if evidenceAvailable {
		files = append(files, statsFile(EvidenceFileName, stats[EvidenceFileName]))
	}
	return files
}

func manifestFilesV2(stats map[string]sequenceStats, evidenceAvailable bool) []File {
	files := manifestFiles(stats, evidenceAvailable)
	files = append(files,
		statsFile(FrontendManifestsFileName, stats[FrontendManifestsFileName]),
		statsFile(CanonicalFactsFileName, stats[CanonicalFactsFileName]),
		statsFile(ExtensionsFileName, stats[ExtensionsFileName]),
	)
	return files
}

func statsFile(name string, stats sequenceStats) File {
	return File{Name: name, Bytes: stats.Bytes, Count: stats.Count, Digest: stats.Digest}
}

func readManifestRaw(ctx context.Context, path string, maxBytes int64) ([]byte, int64, error) {
	if maxBytes == 0 {
		maxBytes = DefaultMaxManifestBytes
	}
	if maxBytes < 0 {
		return nil, 0, fmt.Errorf("reading manifest: %w", ErrLimitExceeded)
	}
	// A bundle directory is an untrusted boundary. Refuse symlinks and other
	// non-regular entries before opening the manifest so a hostile bundle
	// cannot make the reader follow a link into an unrelated file.
	if err := requireRegularSource(path); err != nil {
		return nil, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("opening manifest %q: %w", path, err)
	}
	defer file.Close()
	reader := io.Reader(file)
	if maxBytes > 0 {
		limit := maxBytes
		if maxBytes < int64(^uint64(0)>>1) {
			limit++
		}
		reader = io.LimitReader(file, limit)
	}
	counting := &countingReader{reader: reader}
	decoder := json.NewDecoder(bufio.NewReader(counting))
	var raw json.RawMessage
	if err := contextError(ctx); err != nil {
		return nil, 0, err
	}
	if err := decoder.Decode(&raw); err != nil {
		if maxBytes > 0 && counting.bytes > maxBytes {
			return nil, counting.bytes, fmt.Errorf("reading manifest %q: %w", path, ErrLimitExceeded)
		}
		return nil, counting.bytes, fmt.Errorf("decoding manifest %q: %w: %v", path, ErrInvalid, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, counting.bytes, fmt.Errorf("decoding manifest %q: %w: multiple JSON values", path, ErrInvalid)
		}
		return nil, counting.bytes, fmt.Errorf("decoding manifest %q: extra JSON: %w: %v", path, ErrInvalid, err)
	}
	if err := contextError(ctx); err != nil {
		return nil, counting.bytes, err
	}
	if maxBytes > 0 && counting.bytes > maxBytes {
		return nil, counting.bytes, fmt.Errorf("reading manifest %q: %w", path, ErrLimitExceeded)
	}
	return raw, counting.bytes, nil
}

type countingReader struct {
	reader io.Reader
	bytes  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.bytes += int64(n)
	}
	return n, err
}

type countingWriter struct {
	writer io.Writer
	bytes  int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil {
		return 0, fmt.Errorf("writing sequence: %w: nil writer", ErrInvalid)
	}
	if int64(len(p)) > int64(^uint64(0)>>1)-w.bytes {
		return 0, ErrLimitExceeded
	}
	written, err := w.writer.Write(p)
	if written > 0 {
		w.bytes += int64(written)
	}
	return written, err
}

func readExtendedBundle(ctx context.Context, directory string, raw []byte, manifestBytes int64, options Options) (Bundle, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Bundle{}, fmt.Errorf("decoding extended manifest: %w: %v", ErrInvalid, err)
	}
	if manifest.Version == VersionV1Alpha2 {
		return readExtendedBundleV2(ctx, directory, manifest, manifestBytes, options)
	}
	if manifest.Version != Version {
		return Bundle{}, fmt.Errorf("%w: got %q, want %q", ErrUnsupportedVersion, manifest.Version, Version)
	}
	if manifest.Limits.MaxManifestBytes > 0 && manifestBytes > manifest.Limits.MaxManifestBytes {
		return Bundle{}, fmt.Errorf("manifest: %w", ErrLimitExceeded)
	}
	effective := stricterLimits(manifest.Limits, options.Limits)
	if effective.MaxManifestBytes > 0 && manifestBytes > effective.MaxManifestBytes {
		return Bundle{}, fmt.Errorf("manifest: %w", ErrLimitExceeded)
	}
	validation := manifest
	validation.Limits = effective
	if err := validation.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validating extended manifest: %w", err)
	}
	if options.ImportExpectation != nil {
		if err := validateExpectedManifest(manifest, *options.ImportExpectation); err != nil {
			return Bundle{}, err
		}
	}
	manifest.Limits = effective
	organization, err := optionOrganization(options)
	if err != nil {
		return Bundle{}, err
	}
	if organization.ID != "" && organization.ID != manifest.Organization.ID {
		return Bundle{}, fmt.Errorf("%w: option organization differs from manifest", ErrScopeMismatch)
	}
	if options.Analysis.ConfigurationID != "" && options.Analysis.ConfigurationID != manifest.Analysis.ConfigurationID {
		return Bundle{}, fmt.Errorf("%w: option analysis configuration differs from manifest", ErrScopeMismatch)
	}
	files := filesByName(manifest.Files)
	artifacts := make([]contract.Artifact, 0)
	artifactStats, err := readSequenceFile(ctx, filepath.Join(directory, ArtifactsFileName), files[ArtifactsFileName], effective.MaxBundleBytes, func(value contract.Artifact) error {
		if err := value.Validate(); err != nil {
			return err
		}
		artifacts = append(artifacts, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", ArtifactsFileName, err)
	}
	if err := verifySequenceStats(ArtifactsFileName, files[ArtifactsFileName], artifactStats); err != nil {
		return Bundle{}, err
	}
	contributions := make([]contract.Contribution, 0)
	contributionStats, err := readSequenceFile(ctx, filepath.Join(directory, ContributionsFileName), files[ContributionsFileName], effective.MaxBundleBytes, func(value contract.Contribution) error {
		if err := value.Validate(); err != nil {
			return err
		}
		contributions = append(contributions, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", ContributionsFileName, err)
	}
	if err := verifySequenceStats(ContributionsFileName, files[ContributionsFileName], contributionStats); err != nil {
		return Bundle{}, err
	}

	var units []evidence.EvidenceUnit
	if manifest.EffectiveEvidenceState() == EvidenceStateAvailable {
		units = make([]evidence.EvidenceUnit, 0)
		evidenceStats, readErr := readSequenceFile(ctx, filepath.Join(directory, EvidenceFileName), files[EvidenceFileName], effective.MaxEvidenceBytes, func(value evidence.EvidenceUnit) error {
			if err := value.Validate(); err != nil {
				return err
			}
			units = append(units, value)
			return nil
		})
		if readErr != nil {
			return Bundle{}, fmt.Errorf("reading %s: %w", EvidenceFileName, readErr)
		}
		if err := verifySequenceStats(EvidenceFileName, files[EvidenceFileName], evidenceStats); err != nil {
			return Bundle{}, err
		}
	}
	result := Bundle{Manifest: manifest, Artifacts: artifacts, Contributions: contributions, Evidence: units}
	if err := result.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validating extended bundle: %w", err)
	}
	if options.ImportExpectation != nil {
		if err := validateImportExpectation(result, *options.ImportExpectation); err != nil {
			return Bundle{}, fmt.Errorf("validating imported bundle: %w", err)
		}
	}
	return result, nil
}

func readExtendedBundleV2(ctx context.Context, directory string, manifest Manifest, manifestBytes int64, options Options) (Bundle, error) {
	if manifest.Limits.MaxManifestBytes > 0 && manifestBytes > manifest.Limits.MaxManifestBytes {
		return Bundle{}, fmt.Errorf("manifest: %w", ErrLimitExceeded)
	}
	effective := stricterLimits(manifest.Limits, options.Limits)
	effective = defaultLimitsV2(effective)
	if effective.MaxManifestBytes > 0 && manifestBytes > effective.MaxManifestBytes {
		return Bundle{}, fmt.Errorf("manifest: %w", ErrLimitExceeded)
	}
	validation := manifest
	validation.Limits = effective
	if err := validation.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validating v1alpha2 manifest: %w", err)
	}
	if options.ImportExpectation != nil {
		if err := validateExpectedManifest(manifest, *options.ImportExpectation); err != nil {
			return Bundle{}, err
		}
	}
	manifest.Limits = effective
	organization, err := optionOrganization(options)
	if err != nil {
		return Bundle{}, err
	}
	if organization.ID != "" && organization.ID != manifest.Organization.ID {
		return Bundle{}, fmt.Errorf("%w: option organization differs from manifest", ErrScopeMismatch)
	}
	if options.Analysis.ConfigurationID != "" && options.Analysis.ConfigurationID != manifest.Analysis.ConfigurationID {
		return Bundle{}, fmt.Errorf("%w: option analysis configuration differs from manifest", ErrScopeMismatch)
	}
	files := filesByName(manifest.Files)
	artifacts := make([]contract.Artifact, 0)
	artifactStats, err := readSequenceFile(ctx, filepath.Join(directory, ArtifactsFileName), files[ArtifactsFileName], effective.MaxBundleBytes, func(value contract.Artifact) error {
		if err := value.Validate(); err != nil {
			return err
		}
		artifacts = append(artifacts, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", ArtifactsFileName, err)
	}
	if err := verifySequenceStats(ArtifactsFileName, files[ArtifactsFileName], artifactStats); err != nil {
		return Bundle{}, err
	}
	contributions := make([]contract.Contribution, 0)
	contributionStats, err := readSequenceFile(ctx, filepath.Join(directory, ContributionsFileName), files[ContributionsFileName], effective.MaxBundleBytes, func(value contract.Contribution) error {
		if err := value.Validate(); err != nil {
			return err
		}
		contributions = append(contributions, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", ContributionsFileName, err)
	}
	if err := verifySequenceStats(ContributionsFileName, files[ContributionsFileName], contributionStats); err != nil {
		return Bundle{}, err
	}

	var units []evidence.EvidenceUnit
	if manifest.EffectiveEvidenceState() == EvidenceStateAvailable {
		units = make([]evidence.EvidenceUnit, 0)
		evidenceStats, readErr := readSequenceFile(ctx, filepath.Join(directory, EvidenceFileName), files[EvidenceFileName], effective.MaxEvidenceBytes, func(value evidence.EvidenceUnit) error {
			if err := value.Validate(); err != nil {
				return err
			}
			units = append(units, value)
			return nil
		})
		if readErr != nil {
			return Bundle{}, fmt.Errorf("reading %s: %w", EvidenceFileName, readErr)
		}
		if err := verifySequenceStats(EvidenceFileName, files[EvidenceFileName], evidenceStats); err != nil {
			return Bundle{}, err
		}
	}

	frontendManifests := make([]fact.FrontendManifest, 0)
	frontendStats, err := readSequenceFile(ctx, filepath.Join(directory, FrontendManifestsFileName), files[FrontendManifestsFileName], effective.MaxBundleBytes, func(value fact.FrontendManifest) error {
		frontendManifests = append(frontendManifests, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", FrontendManifestsFileName, err)
	}
	if err := verifySequenceStats(FrontendManifestsFileName, files[FrontendManifestsFileName], frontendStats); err != nil {
		return Bundle{}, err
	}
	facts := make([]fact.CanonicalFact, 0)
	factsStats, err := readSequenceFile(ctx, filepath.Join(directory, CanonicalFactsFileName), files[CanonicalFactsFileName], effective.MaxBundleBytes, func(value fact.CanonicalFact) error {
		facts = append(facts, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", CanonicalFactsFileName, err)
	}
	if err := verifySequenceStats(CanonicalFactsFileName, files[CanonicalFactsFileName], factsStats); err != nil {
		return Bundle{}, err
	}
	extensions := make([]json.RawMessage, 0)
	extensionsStats, err := readSequenceFile(ctx, filepath.Join(directory, ExtensionsFileName), files[ExtensionsFileName], effective.MaxBundleBytes, func(value json.RawMessage) error {
		if len(value) == 0 || !json.Valid(value) {
			return ErrInvalidExtension
		}
		extensions = append(extensions, append(json.RawMessage(nil), value...))
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading %s: %w", ExtensionsFileName, err)
	}
	if err := verifySequenceStats(ExtensionsFileName, files[ExtensionsFileName], extensionsStats); err != nil {
		return Bundle{}, err
	}
	result := Bundle{
		Manifest:          manifest,
		Artifacts:         artifacts,
		Contributions:     contributions,
		Evidence:          units,
		FrontendManifests: frontendManifests,
		Facts:             facts,
		Extensions:        extensions,
	}
	if err := result.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validating v1alpha2 bundle: %w", err)
	}
	if options.ImportExpectation != nil {
		if err := validateImportExpectation(result, *options.ImportExpectation); err != nil {
			return Bundle{}, fmt.Errorf("validating imported bundle: %w", err)
		}
	}
	return result, nil
}

func readLegacyBundle(ctx context.Context, directory string, raw []byte, manifestBytes int64, options Options) (Bundle, error) {
	organization, err := optionOrganization(options)
	if err != nil {
		return Bundle{}, err
	}
	if organization.ID == "" {
		return Bundle{}, fmt.Errorf("reading legacy bundle: %w: explicit organization is required", ErrInvalid)
	}
	if err := validateOrganization(organization); err != nil {
		return Bundle{}, err
	}
	var legacy contract.Manifest
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return Bundle{}, fmt.Errorf("decoding legacy manifest: %w: %v", ErrInvalid, err)
	}
	if err := legacy.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validating legacy manifest: %w", err)
	}
	if options.Limits.MaxManifestBytes > 0 && manifestBytes > options.Limits.MaxManifestBytes {
		return Bundle{}, fmt.Errorf("legacy manifest: %w", ErrLimitExceeded)
	}
	if err := ensureAbsent(ctx, filepath.Join(directory, EvidenceFileName)); err != nil {
		return Bundle{}, err
	}
	limits := options.Limits
	artifacts := make([]contract.Artifact, 0)
	artifactStats, err := readLegacySequence(ctx, filepath.Join(directory, ArtifactsFileName), int64(legacy.ArtifactCount), limits.MaxBundleBytes, func(value contract.Artifact) error {
		if err := value.Validate(); err != nil {
			return err
		}
		artifacts = append(artifacts, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading legacy %s: %w", ArtifactsFileName, err)
	}
	contributions := make([]contract.Contribution, 0)
	contributionStats, err := readLegacySequence(ctx, filepath.Join(directory, ContributionsFileName), int64(legacy.ContributionCount), limits.MaxBundleBytes, func(value contract.Contribution) error {
		if err := value.Validate(); err != nil {
			return err
		}
		contributions = append(contributions, value)
		return nil
	})
	if err != nil {
		return Bundle{}, fmt.Errorf("reading legacy %s: %w", ContributionsFileName, err)
	}
	result := contract.Result{Manifest: legacy, Artifacts: artifacts, Contributions: contributions}
	if err := result.Validate(); err != nil {
		return Bundle{}, fmt.Errorf("validating legacy result: %w", err)
	}
	analysis, err := legacyAnalysis(legacy, options.Analysis)
	if err != nil {
		return Bundle{}, err
	}
	digest, err := FactualDigest(result, nil)
	if err != nil {
		return Bundle{}, err
	}
	manifest := Manifest{
		Version:       Version,
		Organization:  organization,
		Manifest:      legacy,
		Analysis:      analysis,
		FactualDigest: digest,
		Files: []File{
			statsFile(ArtifactsFileName, artifactStats),
			statsFile(ContributionsFileName, contributionStats),
		},
		Counts: Counts{
			ArtifactCount:     int64(len(artifacts)),
			ContributionCount: int64(len(contributions)),
		},
		Limits:   limits,
		Evidence: EvidenceMetadata{State: EvidenceStateLimited},
	}
	if err := manifest.Limits.Validate(); err != nil {
		return Bundle{}, err
	}
	if err := validateCountLimits(manifest.Counts, manifest.Limits); err != nil {
		return Bundle{}, err
	}
	if _, err := validateFiles(manifest.Files, manifest.Counts, manifest.Limits); err != nil {
		return Bundle{}, err
	}
	if manifest.Limits.MaxBundleBytes > 0 && artifactStats.Bytes > manifest.Limits.MaxBundleBytes-contributionStats.Bytes {
		return Bundle{}, fmt.Errorf("legacy sequences: %w", ErrLimitExceeded)
	}
	// The limited conversion deliberately validates the legacy contract above
	// rather than applying stricter extended hash rules to an old manifest.
	bundleResult := Bundle{Manifest: manifest, Artifacts: artifacts, Contributions: contributions}
	if options.ImportExpectation != nil {
		if err := validateImportExpectation(bundleResult, *options.ImportExpectation); err != nil {
			return Bundle{}, fmt.Errorf("validating imported bundle: %w", err)
		}
	}
	return bundleResult, nil
}

func readSequenceFile[T any](ctx context.Context, path string, descriptor File, maxBytes int64, visit func(T) error) (sequenceStats, error) {
	if maxBytes > 0 && descriptor.Bytes > maxBytes {
		return sequenceStats{}, fmt.Errorf("%s: %w", descriptor.Name, ErrLimitExceeded)
	}
	return readNDJSON(ctx, path, descriptor.Count, descriptor.Bytes, visit)
}

func readLegacySequence[T any](ctx context.Context, path string, expectedCount, maxBytes int64, visit func(T) error) (sequenceStats, error) {
	if expectedCount < 0 {
		return sequenceStats{}, fmt.Errorf("%s: %w", filepath.Base(path), ErrCountMismatch)
	}
	return readNDJSON(ctx, path, expectedCount, maxBytes, visit)
}

func readNDJSON[T any](ctx context.Context, path string, expectedCount, maxBytes int64, visit func(T) error) (sequenceStats, error) {
	if visit == nil {
		return sequenceStats{}, fmt.Errorf("reading %s: %w: nil visitor", filepath.Base(path), ErrInvalid)
	}
	// Sequence names are fixed by the bundle contract, but the directory and
	// its entries are still attacker-controlled when a bundle is imported.
	// Reject symlinks before opening a sequence to prevent arbitrary file reads.
	if err := requireRegularSource(path); err != nil {
		return sequenceStats{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return sequenceStats{}, fmt.Errorf("opening %q: %w", path, err)
	}
	defer file.Close()
	reader := io.Reader(file)
	if maxBytes > 0 {
		if maxBytes == int64(^uint64(0)>>1) {
			reader = io.LimitReader(file, maxBytes)
		} else {
			reader = io.LimitReader(file, maxBytes+1)
		}
	}
	buffered := bufio.NewReader(reader)
	hash := sha256.New()
	var stats sequenceStats
	for {
		if err := contextError(ctx); err != nil {
			return sequenceStats{}, err
		}
		line, readErr := buffered.ReadBytes('\n')
		if len(line) > 0 {
			if stats.Bytes > int64(^uint64(0)>>1)-int64(len(line)) {
				return sequenceStats{}, fmt.Errorf("%s: %w", filepath.Base(path), ErrLimitExceeded)
			}
			stats.Bytes += int64(len(line))
			if _, err := hash.Write(line); err != nil {
				return sequenceStats{}, fmt.Errorf("hashing %s item %d: %w", filepath.Base(path), stats.Count, err)
			}
			if maxBytes > 0 && stats.Bytes > maxBytes {
				return sequenceStats{}, fmt.Errorf("%s: %w", filepath.Base(path), ErrLimitExceeded)
			}
			var value T
			if err := json.Unmarshal(line, &value); err != nil {
				return sequenceStats{}, fmt.Errorf("decoding %s item %d: %w: %v", filepath.Base(path), stats.Count, ErrInvalid, err)
			}
			if expectedCount >= 0 && stats.Count >= expectedCount {
				return sequenceStats{}, fmt.Errorf("%s: %w: more items than declared", filepath.Base(path), ErrCountMismatch)
			}
			if err := visit(value); err != nil {
				return sequenceStats{}, fmt.Errorf("visiting %s item %d: %w", filepath.Base(path), stats.Count, err)
			}
			stats.Count++
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return sequenceStats{}, fmt.Errorf("reading %s: %w", filepath.Base(path), readErr)
		}
	}
	if expectedCount >= 0 && stats.Count != expectedCount {
		return sequenceStats{}, fmt.Errorf("%s: %w: got %d, want %d", filepath.Base(path), ErrCountMismatch, stats.Count, expectedCount)
	}
	if err := contextError(ctx); err != nil {
		return sequenceStats{}, err
	}
	stats.Digest = hex.EncodeToString(hash.Sum(nil))
	return stats, nil
}

func verifySequenceStats(name string, expected File, actual sequenceStats) error {
	if actual.Bytes != expected.Bytes {
		return fmt.Errorf("%s: %w: got %d, want %d", name, ErrSizeMismatch, actual.Bytes, expected.Bytes)
	}
	if actual.Count != expected.Count {
		return fmt.Errorf("%s: %w: got %d, want %d", name, ErrCountMismatch, actual.Count, expected.Count)
	}
	if actual.Digest != expected.Digest {
		return fmt.Errorf("%s: %w", name, ErrDigestMismatch)
	}
	return nil
}

func filesByName(files []File) map[string]File {
	result := make(map[string]File, len(files))
	for _, file := range files {
		result[file.Name] = file
	}
	return result
}

func stricterLimits(declared, configured Limits) Limits {
	return Limits{
		MaxBundleBytes:       stricterPositive(declared.MaxBundleBytes, configured.MaxBundleBytes),
		MaxManifestBytes:     stricterPositive(declared.MaxManifestBytes, configured.MaxManifestBytes),
		MaxEvidenceBytes:     stricterPositive(declared.MaxEvidenceBytes, configured.MaxEvidenceBytes),
		MaxArtifacts:         stricterPositive(declared.MaxArtifacts, configured.MaxArtifacts),
		MaxContributions:     stricterPositive(declared.MaxContributions, configured.MaxContributions),
		MaxEvidenceUnits:     stricterPositive(declared.MaxEvidenceUnits, configured.MaxEvidenceUnits),
		MaxFrontendManifests: stricterPositive(declared.MaxFrontendManifests, configured.MaxFrontendManifests),
		MaxCanonicalFacts:    stricterPositive(declared.MaxCanonicalFacts, configured.MaxCanonicalFacts),
		MaxExtensions:        stricterPositive(declared.MaxExtensions, configured.MaxExtensions),
	}
}

func defaultLimits(limits Limits) Limits {
	if limits.MaxBundleBytes == 0 {
		limits.MaxBundleBytes = DefaultMaxBundleBytes
	}
	if limits.MaxManifestBytes == 0 {
		limits.MaxManifestBytes = DefaultMaxManifestBytes
	}
	if limits.MaxEvidenceBytes == 0 {
		limits.MaxEvidenceBytes = DefaultMaxEvidenceBytes
	}
	if limits.MaxArtifacts == 0 {
		limits.MaxArtifacts = DefaultMaxArtifacts
	}
	if limits.MaxContributions == 0 {
		limits.MaxContributions = DefaultMaxContributions
	}
	if limits.MaxEvidenceUnits == 0 {
		limits.MaxEvidenceUnits = DefaultMaxEvidenceUnits
	}
	return limits
}

func defaultLimitsV2(limits Limits) Limits {
	limits = defaultLimits(limits)
	if limits.MaxFrontendManifests == 0 {
		limits.MaxFrontendManifests = DefaultMaxFrontendManifests
	}
	if limits.MaxCanonicalFacts == 0 {
		limits.MaxCanonicalFacts = DefaultMaxCanonicalFacts
	}
	if limits.MaxExtensions == 0 {
		limits.MaxExtensions = DefaultMaxExtensions
	}
	return limits
}

func firstBundleOnlyField(fields map[string]json.RawMessage) string {
	for _, field := range []string{
		"organization",
		"analysis",
		"factual_digest",
		"files",
		"counts",
		"evidence",
		"limits",
		"frontend_manifests",
		"facts",
		"extensions",
	} {
		if _, exists := fields[field]; exists {
			return field
		}
	}
	return ""
}

func stricterPositive(left, right int64) int64 {
	if left == 0 {
		return right
	}
	if right == 0 || left < right {
		return left
	}
	return right
}

func legacyAnalysis(manifest contract.Manifest, explicit Analysis) (Analysis, error) {
	analysis := explicit
	if analysis.ConfigurationID == "" {
		analysis.ConfigurationID = manifest.Execution.ConfigurationID
	}
	if analysis.Revision == "" && analysis.Hash == "" {
		analysis.Revision = firstNonEmpty(manifest.Snapshot.Revision, manifest.Source.Revision)
		if analysis.Revision == "" {
			analysis.Hash = firstValidDigest(manifest.Snapshot.Hash, manifest.Source.Hash)
		}
	}
	if err := validateAnalysis(analysis, manifest.Execution.ConfigurationID); err != nil {
		return Analysis{}, fmt.Errorf("legacy analysis metadata: %w", err)
	}
	return analysis, nil
}

func optionOrganization(options Options) (Organization, error) {
	organization := options.Organization
	if options.OrganizationID == "" {
		return organization, nil
	}
	if organization.ID != "" && organization.ID != options.OrganizationID {
		return Organization{}, fmt.Errorf("%w: organization options differ", ErrScopeMismatch)
	}
	organization.ID = options.OrganizationID
	return organization, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstValidDigest(values ...string) string {
	for _, value := range values {
		if isSHA256Digest(value) {
			return value
		}
	}
	return ""
}

func ensureAbsent(ctx context.Context, path string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("%s: %w: legacy result must not contain evidence", filepath.Base(path), ErrInvalidFile)
	}
	return fmt.Errorf("checking %q: %w", path, err)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
