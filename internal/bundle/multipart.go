package bundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MultipartManifestPartName is the only accepted manifest part name and
	// filename. Part names are protocol constants, never caller-controlled
	// paths.
	MultipartManifestPartName = ManifestFileName
	// MultipartArtifactsPartName is the canonical artifacts sequence part.
	MultipartArtifactsPartName = ArtifactsFileName
	// MultipartContributionsPartName is the canonical contributions sequence
	// part.
	MultipartContributionsPartName = ContributionsFileName
	// MultipartEvidencePartName is the canonical optional evidence sequence
	// part.
	MultipartEvidencePartName = EvidenceFileName

	// MultipartManifestMediaType is the media type of manifest.json.
	MultipartManifestMediaType = "application/json"
	// MultipartSequenceMediaType is the media type of all NDJSON sequences.
	MultipartSequenceMediaType = "application/x-ndjson"
)

var (
	// ErrMultipartInvalid identifies a malformed multipart envelope.
	ErrMultipartInvalid = errors.New("bundle: invalid multipart")
	// ErrMultipartPart identifies an unexpected, duplicated, or missing part.
	ErrMultipartPart = errors.New("bundle: invalid multipart part")
	// ErrMultipartTraversal identifies a part filename that is not canonical.
	ErrMultipartTraversal = errors.New("bundle: multipart filename traversal")
)

// MultipartWriteOptions controls a multipart sender. A zero Limits value is
// replaced with safe finite defaults before any source bytes are opened.
type MultipartWriteOptions struct {
	Limits   Limits
	Boundary string
	Bundle   Options
}

// MultipartReadOptions controls a multipart receiver. Installation limits
// and the limits in Bundle are combined by taking the stricter value. Bundle
// carries optional scope checks for the extended manifest.
type MultipartReadOptions struct {
	Limits         Limits
	Bundle         Options
	Organization   Organization
	OrganizationID string
	Analysis       Analysis
}

// MultipartMetadata describes the validated canonical payload. Bytes counts
// only sequence payload bytes, excluding MIME headers and boundaries.
type MultipartMetadata struct {
	ContentType   string
	Boundary      string
	FactualDigest string
	// Digest is a compatibility shorthand for FactualDigest.
	Digest        string
	Counts        Counts
	ManifestBytes int64
	SequenceBytes int64
	// Bytes is the canonical sequence-byte total. It is retained as the
	// concise spelling for callers that do not need the manifest split.
	Bytes int64
}

// MultipartSender streams one bundle directory as a multipart/form-data
// body. Constructing a sender chooses and exposes the boundary before Send is
// called, so a future HTTP client can set Content-Type before transmitting.
type MultipartSender struct {
	directory string
	options   MultipartWriteOptions
	limits    Limits
	boundary  string
}

// MultipartWriter is an alias for callers that use encoder terminology.
type MultipartWriter = MultipartSender

// NewMultipartSender prepares a sender without changing or opening the source
// directory. The source is checked read-only when Send starts.
func NewMultipartSender(directory string, options MultipartWriteOptions) (*MultipartSender, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, fmt.Errorf("creating multipart sender: %w", ErrMultipartInvalid)
	}
	if err := options.Limits.Validate(); err != nil {
		return nil, err
	}
	if err := options.Bundle.Limits.Validate(); err != nil {
		return nil, err
	}
	limits := stricterLimits(defaultLimits(options.Limits), options.Bundle.Limits)
	boundary := options.Boundary
	if boundary == "" {
		writer := multipart.NewWriter(io.Discard)
		boundary = writer.Boundary()
		_ = writer.Close()
	}
	if !validMultipartBoundary(boundary) {
		return nil, fmt.Errorf("creating multipart sender: %w", ErrMultipartInvalid)
	}
	options.Limits = limits
	options.Bundle.Limits = limits
	return &MultipartSender{
		directory: directory,
		options:   options,
		limits:    limits,
		boundary:  boundary,
	}, nil
}

// NewMultipartWriter is an alias for NewMultipartSender.
func NewMultipartWriter(directory string, options MultipartWriteOptions) (*MultipartSender, error) {
	return NewMultipartSender(directory, options)
}

// ContentType returns the complete media type, including the sender's stable
// boundary, and is safe to call before Send.
func (s *MultipartSender) ContentType() string {
	if s == nil {
		return ""
	}
	return multipartContentType(s.boundary)
}

// Boundary returns the boundary selected for this sender.
func (s *MultipartSender) Boundary() string {
	if s == nil {
		return ""
	}
	return s.boundary
}

// Send writes the manifest first, followed by artifacts, contributions, and
// evidence when the manifest declares evidence available. Every sequence is
// copied through a bounded, context-aware reader and a SHA-256/counting
// writer. The source is only opened read-only.
func (s *MultipartSender) Send(ctx context.Context, destination io.Writer) (MultipartMetadata, error) {
	var empty MultipartMetadata
	if s == nil || destination == nil {
		return empty, fmt.Errorf("sending multipart bundle: %w", ErrMultipartInvalid)
	}
	if err := contextError(ctx); err != nil {
		return empty, err
	}
	info, err := inspectMultipartSource(ctx, s.directory, s.limits, s.options.Bundle)
	if err != nil {
		return empty, err
	}

	writer := multipart.NewWriter(destination)
	if err := writer.SetBoundary(s.boundary); err != nil {
		return empty, fmt.Errorf("sending multipart bundle: %w", ErrMultipartInvalid)
	}
	writePart := func(ctx context.Context, spec multipartPart, maxBytes int64) (sequenceStats, error) {
		if err := contextError(ctx); err != nil {
			return sequenceStats{}, err
		}
		part, err := writer.CreatePart(partHeader(spec))
		if err != nil {
			return sequenceStats{}, fmt.Errorf("creating multipart part: %w", ErrMultipartInvalid)
		}
		return copySourcePayload(ctx, filepath.Join(s.directory, spec.name), part, maxBytes)
	}

	manifestStats, err := writePart(ctx, multipartPart{
		name:      MultipartManifestPartName,
		mediaType: MultipartManifestMediaType,
	}, s.limits.MaxManifestBytes)
	if err != nil {
		return empty, err
	}
	if manifestStats.Bytes != info.manifestBytes {
		return empty, fmt.Errorf("manifest: %w", ErrSizeMismatch)
	}

	var totalBytes int64
	metadata := MultipartMetadata{
		ContentType:   s.ContentType(),
		Boundary:      s.boundary,
		FactualDigest: info.factualDigest,
		Digest:        info.factualDigest,
		Counts:        info.counts,
		ManifestBytes: manifestStats.Bytes,
	}
	for _, spec := range info.parts {
		maxBytes := s.limits.MaxBundleBytes - totalBytes
		if spec.name == MultipartEvidencePartName {
			maxBytes = multipartMinimum(maxBytes, s.limits.MaxEvidenceBytes)
		}
		stats, copyErr := writePart(ctx, spec, maxBytes)
		if copyErr != nil {
			return empty, copyErr
		}
		if err := verifyMultipartSourceStats(spec, stats); err != nil {
			return empty, err
		}
		if totalBytes > int64(^uint64(0)>>1)-stats.Bytes {
			return empty, fmt.Errorf("multipart sequence bytes: %w", ErrLimitExceeded)
		}
		totalBytes += stats.Bytes
	}
	if err := contextError(ctx); err != nil {
		return empty, err
	}
	if err := writer.Close(); err != nil {
		return empty, fmt.Errorf("closing multipart bundle: %w", ErrMultipartInvalid)
	}
	metadata.Bytes = totalBytes
	metadata.SequenceBytes = totalBytes
	return metadata, nil
}

// WriteMultipart streams directory as multipart/form-data using a sender
// whose boundary is chosen for this call. Use NewMultipartSender when the
// Content-Type must be known before writing begins.
func WriteMultipart(ctx context.Context, directory string, destination io.Writer, options MultipartWriteOptions) (MultipartMetadata, error) {
	sender, err := NewMultipartSender(directory, options)
	if err != nil {
		return MultipartMetadata{}, err
	}
	return sender.Send(ctx, destination)
}

// SendMultipart is a descriptive alias for WriteMultipart.
func SendMultipart(ctx context.Context, directory string, destination io.Writer, options MultipartWriteOptions) (MultipartMetadata, error) {
	return WriteMultipart(ctx, directory, destination, options)
}

// ReadMultipart receives an extended Analysis Bundle body into a private
// staging directory. It requires the canonical part order, validates each
// part's media type and disposition, and only returns after ReadBundle has
// validated all staged bytes and factual references. MIME and NDJSON payloads
// are copied with a fixed buffer; the raw body and sequence files are never
// loaded as one value. The returned Bundle is necessarily materialized after
// validation for this convenience API. Staging is removed on every exit.
func ReadMultipart(ctx context.Context, source io.Reader, contentType string, options MultipartReadOptions) (Bundle, MultipartMetadata, error) {
	var empty Bundle
	staged, err := StageMultipart(ctx, source, contentType, os.TempDir(), options)
	if err != nil {
		return empty, MultipartMetadata{}, err
	}
	defer staged.Remove()
	_, bundleOptions, err := normalizeMultipartReadOptions(options)
	if err != nil {
		return empty, MultipartMetadata{}, err
	}
	bundleOptions.Limits = stricterLimits(defaultLimits(options.Limits), bundleOptions.Limits)
	result, err := ReadBundle(ctx, staged.Directory, bundleOptions)
	if err != nil {
		return empty, staged.Metadata, err
	}
	staged.Metadata.FactualDigest = result.Manifest.FactualDigest
	staged.Metadata.Digest = staged.Metadata.FactualDigest
	staged.Metadata.Counts = result.Manifest.Counts
	return result, staged.Metadata, nil
}

// StagedMultipart is a validated, durable-on-disk multipart envelope. The
// sequence files remain in Directory and are owned by the caller. No bundle
// sequence is materialized by StageMultipart; callers can pass the directory
// to ReadBundle from a later worker attempt.
type StagedMultipart struct {
	Directory string
	Manifest  Manifest
	Metadata  MultipartMetadata
}

// Remove releases a staged multipart directory. It is safe to call more than
// once and does not follow a symlink in place of the directory itself.
func (s StagedMultipart) Remove() error {
	if strings.TrimSpace(s.Directory) == "" {
		return nil
	}
	info, err := os.Lstat(s.Directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrMultipartTraversal
	}
	return os.RemoveAll(s.Directory)
}

// StageMultipart validates the multipart envelope and spools its payloads to
// a private directory below root. It is intended for asynchronous ingestion:
// the HTTP request can be acknowledged only after the returned directory is
// durably published by the caller. StageMultipart never calls ReadBundle and
// therefore does not load the complete bundle into memory.
func StageMultipart(ctx context.Context, source io.Reader, contentType, root string, options MultipartReadOptions) (StagedMultipart, error) {
	var empty StagedMultipart
	if source == nil {
		return empty, fmt.Errorf("staging multipart bundle: %w", ErrMultipartInvalid)
	}
	if err := contextError(ctx); err != nil {
		return empty, err
	}
	limits, bundleOptions, err := normalizeMultipartReadOptions(options)
	if err != nil {
		return empty, err
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "multipart/form-data" {
		return empty, fmt.Errorf("staging multipart bundle: %w", ErrMultipartInvalid)
	}
	boundary := params["boundary"]
	if !validMultipartBoundary(boundary) {
		return empty, fmt.Errorf("staging multipart bundle: %w", ErrMultipartInvalid)
	}
	stagingRoot, err := prepareMultipartStagingRoot(root)
	if err != nil {
		return empty, err
	}
	staging, err := os.MkdirTemp(stagingRoot, ".multipart-*")
	if err != nil {
		return empty, fmt.Errorf("creating multipart staging: %w", ErrMultipartInvalid)
	}
	defer func() {
		if empty.Directory != staging {
			_ = os.RemoveAll(staging)
		}
	}()

	reader := multipart.NewReader(&multipartContextReader{ctx: ctx, reader: source}, boundary)
	manifestPart, err := reader.NextPart()
	if err != nil {
		return empty, multipartReadError(err)
	}
	manifestSpec := multipartPart{name: MultipartManifestPartName, mediaType: MultipartManifestMediaType}
	if err := validateMultipartPart(manifestPart, manifestSpec); err != nil {
		return empty, err
	}
	manifestPath := filepath.Join(staging, ManifestFileName)
	manifestFile, err := createMultipartStagingFile(manifestPath)
	if err != nil {
		return empty, err
	}
	manifestStats, copyErr := copyMultipartPayload(ctx, manifestPart, manifestFile, limits.MaxManifestBytes)
	closeErr := manifestFile.Close()
	if copyErr != nil {
		return empty, copyErr
	}
	if closeErr != nil {
		return empty, fmt.Errorf("closing staged manifest: %w", ErrMultipartInvalid)
	}
	raw, manifestBytes, err := readManifestRaw(ctx, manifestPath, limits.MaxManifestBytes)
	if err != nil {
		return empty, multipartReadError(err)
	}
	if manifestBytes != manifestStats.Bytes {
		return empty, fmt.Errorf("manifest: %w", ErrSizeMismatch)
	}
	manifestInfo, err := parseMultipartManifest(raw, manifestBytes, limits)
	if err != nil {
		return empty, err
	}
	organization, err := optionOrganization(bundleOptions)
	if err != nil {
		return empty, err
	}
	if organization.ID != "" && organization.ID != manifestInfo.manifest.Organization.ID {
		return empty, fmt.Errorf("multipart organization scope: %w", ErrScopeMismatch)
	}
	if bundleOptions.Analysis.ConfigurationID != "" && bundleOptions.Analysis.ConfigurationID != manifestInfo.manifest.Analysis.ConfigurationID {
		return empty, fmt.Errorf("multipart analysis scope: %w", ErrScopeMismatch)
	}

	metadata := MultipartMetadata{
		ContentType:   contentType,
		Boundary:      boundary,
		ManifestBytes: manifestStats.Bytes,
		FactualDigest: manifestInfo.factualDigest,
		Digest:        manifestInfo.factualDigest,
		Counts:        manifestInfo.counts,
	}
	var totalBytes int64
	for index, spec := range manifestInfo.parts {
		if err := contextError(ctx); err != nil {
			return empty, err
		}
		part, nextErr := reader.NextPart()
		if nextErr != nil {
			if errors.Is(nextErr, io.EOF) {
				return empty, fmt.Errorf("missing multipart part %d: %w", index, ErrMultipartPart)
			}
			return empty, multipartReadError(nextErr)
		}
		if err := validateMultipartPart(part, spec); err != nil {
			return empty, err
		}
		path := filepath.Join(staging, spec.name)
		file, fileErr := createMultipartStagingFile(path)
		if fileErr != nil {
			return empty, fileErr
		}
		maxBytes := limits.MaxBundleBytes - totalBytes
		if spec.name == MultipartEvidencePartName {
			maxBytes = multipartMinimum(maxBytes, limits.MaxEvidenceBytes)
		}
		if spec.descriptor != nil {
			maxBytes = multipartMinimum(maxBytes, spec.descriptor.Bytes)
		}
		stats, payloadErr := copyMultipartPayload(ctx, part, file, maxBytes)
		fileCloseErr := file.Close()
		if payloadErr != nil {
			return empty, payloadErr
		}
		if fileCloseErr != nil {
			return empty, fmt.Errorf("closing staged sequence: %w", ErrMultipartInvalid)
		}
		if err := verifyMultipartStats(spec, stats); err != nil {
			return empty, err
		}
		if totalBytes > int64(^uint64(0)>>1)-stats.Bytes {
			return empty, fmt.Errorf("multipart sequence bytes: %w", ErrLimitExceeded)
		}
		totalBytes += stats.Bytes
	}
	if _, nextErr := reader.NextPart(); !errors.Is(nextErr, io.EOF) {
		if nextErr == nil {
			return empty, fmt.Errorf("unexpected multipart part: %w", ErrMultipartPart)
		}
		return empty, multipartReadError(nextErr)
	}
	if err := contextError(ctx); err != nil {
		return empty, err
	}
	metadata.Bytes = totalBytes
	metadata.SequenceBytes = totalBytes
	empty = StagedMultipart{Directory: staging, Manifest: manifestInfo.manifest, Metadata: metadata}
	return empty, nil
}

func prepareMultipartStagingRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.ContainsAny(root, "\x00\r\n") {
		return "", fmt.Errorf("multipart staging root: %w", ErrMultipartInvalid)
	}
	clean := filepath.Clean(root)
	if err := os.MkdirAll(clean, 0o700); err != nil {
		return "", fmt.Errorf("creating multipart staging root: %w", ErrMultipartInvalid)
	}
	info, err := os.Lstat(clean)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("multipart staging root: %w", ErrMultipartInvalid)
	}
	return clean, nil
}

// ReceiveMultipart is a descriptive alias for ReadMultipart.
func ReceiveMultipart(ctx context.Context, source io.Reader, contentType string, options MultipartReadOptions) (Bundle, MultipartMetadata, error) {
	return ReadMultipart(ctx, source, contentType, options)
}

type multipartPart struct {
	name       string
	mediaType  string
	descriptor *File
}

type multipartManifestInfo struct {
	manifest      Manifest
	manifestBytes int64
	parts         []multipartPart
	factualDigest string
	counts        Counts
}

func inspectMultipartSource(ctx context.Context, directory string, limits Limits, bundleOptions Options) (multipartManifestInfo, error) {
	var empty multipartManifestInfo
	dirInfo, err := os.Lstat(directory)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return empty, fmt.Errorf("inspecting multipart source: %w", ErrMultipartInvalid)
	}
	manifestPath := filepath.Join(directory, ManifestFileName)
	if err := requireRegularSource(manifestPath); err != nil {
		return empty, err
	}
	raw, manifestBytes, err := readManifestRaw(ctx, manifestPath, limits.MaxManifestBytes)
	if err != nil {
		return empty, multipartReadError(err)
	}
	info, err := parseMultipartManifest(raw, manifestBytes, limits)
	if err != nil {
		return empty, err
	}
	organization, optionErr := optionOrganization(bundleOptions)
	if optionErr != nil {
		return empty, optionErr
	}
	if organization.ID != "" && organization.ID != info.manifest.Organization.ID {
		return empty, fmt.Errorf("multipart organization scope: %w", ErrScopeMismatch)
	}
	if bundleOptions.Analysis.ConfigurationID != "" && bundleOptions.Analysis.ConfigurationID != info.manifest.Analysis.ConfigurationID {
		return empty, fmt.Errorf("multipart analysis scope: %w", ErrScopeMismatch)
	}
	var totalBytes int64
	for _, spec := range info.parts {
		path := filepath.Join(directory, spec.name)
		if err := requireRegularSource(path); err != nil {
			return empty, err
		}
		stats, statErr := validateSourceSequence(ctx, path, spec, limits, totalBytes)
		if statErr != nil {
			return empty, statErr
		}
		if totalBytes > int64(^uint64(0)>>1)-stats.Bytes {
			return empty, fmt.Errorf("multipart sequence bytes: %w", ErrLimitExceeded)
		}
		totalBytes += stats.Bytes
	}
	if info.manifest.EffectiveEvidenceState() == EvidenceStateLimited {
		evidencePath := filepath.Join(directory, EvidenceFileName)
		if _, statErr := os.Lstat(evidencePath); statErr == nil {
			return empty, fmt.Errorf("unexpected evidence source: %w", ErrInvalidFile)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return empty, fmt.Errorf("checking evidence source: %w", ErrMultipartInvalid)
		}
	}
	return info, nil
}

func parseMultipartManifest(raw []byte, manifestBytes int64, limits Limits) (multipartManifestInfo, error) {
	var info multipartManifestInfo
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal(raw, &fields); err != nil {
		return info, fmt.Errorf("decoding multipart manifest: %w", ErrMultipartInvalid)
	}
	if _, extended := fields["version"]; extended {
		var manifest Manifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return info, fmt.Errorf("decoding multipart manifest: %w", ErrMultipartInvalid)
		}
		if manifest.Version != Version {
			return info, fmt.Errorf("multipart manifest: %w", ErrUnsupportedVersion)
		}
		if manifest.Limits.MaxManifestBytes > 0 && manifestBytes > manifest.Limits.MaxManifestBytes {
			return info, fmt.Errorf("multipart manifest: %w", ErrLimitExceeded)
		}
		effective := stricterLimits(manifest.Limits, limits)
		validation := manifest
		validation.Limits = effective
		if err := validation.Validate(); err != nil {
			return info, err
		}
		manifest.Limits = effective
		files := filesByName(manifest.Files)
		parts := []multipartPart{
			{name: MultipartArtifactsPartName, mediaType: MultipartSequenceMediaType, descriptor: descriptorPointer(files[ArtifactsFileName])},
			{name: MultipartContributionsPartName, mediaType: MultipartSequenceMediaType, descriptor: descriptorPointer(files[ContributionsFileName])},
		}
		if manifest.EffectiveEvidenceState() == EvidenceStateAvailable {
			parts = append(parts, multipartPart{name: MultipartEvidencePartName, mediaType: MultipartSequenceMediaType, descriptor: descriptorPointer(files[EvidenceFileName])})
		}
		info.manifest = manifest
		info.manifestBytes = manifestBytes
		info.parts = parts
		info.factualDigest = manifest.FactualDigest
		info.counts = manifest.Counts
		return info, nil
	}
	if field := firstBundleOnlyField(fields); field != "" {
		return info, fmt.Errorf("multipart manifest field: %w", ErrUnsupportedVersion)
	}
	return info, fmt.Errorf("multipart manifest: %w", ErrUnsupportedVersion)
}

func validateSourceSequence(ctx context.Context, path string, spec multipartPart, limits Limits, totalBytes int64) (sequenceStats, error) {
	if spec.descriptor == nil {
		return sequenceStats{}, fmt.Errorf("%s: %w", spec.name, ErrInvalidFile)
	}
	maxBytes := limits.MaxBundleBytes - totalBytes
	if spec.name == MultipartEvidencePartName {
		maxBytes = multipartMinimum(maxBytes, limits.MaxEvidenceBytes)
	}
	if spec.descriptor != nil {
		maxBytes = multipartMinimum(maxBytes, spec.descriptor.Bytes)
	}
	expectedCount := spec.descriptor.Count
	if err := requireRegularSource(path); err != nil {
		return sequenceStats{}, err
	}
	if maxBytes == 0 {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Size() != 0 {
			return sequenceStats{}, fmt.Errorf("%s: %w", spec.name, ErrLimitExceeded)
		}
		if expectedCount != 0 {
			return sequenceStats{}, fmt.Errorf("%s: %w", spec.name, ErrCountMismatch)
		}
		digest := sha256.Sum256(nil)
		return sequenceStats{Digest: hex.EncodeToString(digest[:])}, nil
	}
	stats, err := readNDJSON[json.RawMessage](ctx, path, expectedCount, maxBytes, func(json.RawMessage) error { return nil })
	if err != nil {
		return sequenceStats{}, err
	}
	if spec.descriptor != nil {
		if err := verifySequenceStats(spec.name, *spec.descriptor, stats); err != nil {
			return sequenceStats{}, err
		}
	}
	return stats, nil
}

func verifyMultipartSourceStats(spec multipartPart, stats sequenceStats) error {
	if spec.descriptor == nil {
		return fmt.Errorf("%s: %w", spec.name, ErrInvalidFile)
	}
	return verifySequenceStats(spec.name, *spec.descriptor, stats)
}

func verifyMultipartStats(spec multipartPart, stats sequenceStats) error {
	if spec.descriptor == nil {
		return fmt.Errorf("%s: %w", spec.name, ErrInvalidFile)
	}
	return verifySequenceStats(spec.name, *spec.descriptor, stats)
}

func descriptorPointer(file File) *File {
	descriptor := file
	return &descriptor
}

func requireRegularSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("opening multipart source: %w", ErrInvalidFile)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("multipart source file: %w", ErrInvalidFile)
	}
	return nil
}

func createMultipartStagingFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("creating multipart staging file: %w", ErrMultipartInvalid)
	}
	return file, nil
}

func copySourcePayload(ctx context.Context, path string, destination io.Writer, maxBytes int64) (sequenceStats, error) {
	if err := requireRegularSource(path); err != nil {
		return sequenceStats{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return sequenceStats{}, fmt.Errorf("opening multipart source: %w", ErrInvalidFile)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return sequenceStats{}, fmt.Errorf("checking multipart source: %w", ErrInvalidFile)
	}
	return copyPayload(ctx, file, destination, maxBytes)
}

func copyMultipartPayload(ctx context.Context, source io.Reader, destination io.Writer, maxBytes int64) (sequenceStats, error) {
	if source == nil || destination == nil {
		return sequenceStats{}, fmt.Errorf("copying multipart payload: %w", ErrMultipartInvalid)
	}
	return copyPayload(ctx, source, destination, maxBytes)
}

func copyPayload(ctx context.Context, source io.Reader, destination io.Writer, maxBytes int64) (sequenceStats, error) {
	if maxBytes < 0 {
		return sequenceStats{}, fmt.Errorf("multipart payload: %w", ErrLimitExceeded)
	}
	hasher := &multipartPayloadWriter{
		writer:   &multipartContextWriter{ctx: ctx, writer: destination},
		maxBytes: maxBytes,
		hash:     sha256.New(),
	}
	reader := &multipartContextReader{ctx: ctx, reader: source}
	_, err := io.CopyBuffer(hasher, reader, make([]byte, 32*1024))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return sequenceStats{}, err
		}
		return sequenceStats{}, err
	}
	if err := contextError(ctx); err != nil {
		return sequenceStats{}, err
	}
	return hasher.stats(), nil
}

type multipartPayloadWriter struct {
	writer   io.Writer
	maxBytes int64
	hash     hashWriter
	bytes    int64
	newlines int64
	last     byte
}

func (w *multipartPayloadWriter) Write(p []byte) (int, error) {
	if w == nil || w.writer == nil || w.hash == nil {
		return 0, fmt.Errorf("writing multipart payload: %w", ErrMultipartInvalid)
	}
	if int64(len(p)) > w.maxBytes-w.bytes {
		return 0, fmt.Errorf("multipart payload: %w", ErrLimitExceeded)
	}
	n, err := w.writer.Write(p)
	if n < 0 || n > len(p) {
		return 0, io.ErrShortWrite
	}
	if n > 0 {
		if _, hashErr := w.hash.Write(p[:n]); hashErr != nil {
			return n, hashErr
		}
		w.bytes += int64(n)
		w.newlines += int64(countByte(p[:n], '\n'))
		w.last = p[n-1]
	}
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (w *multipartPayloadWriter) stats() sequenceStats {
	count := w.newlines
	if w.bytes > 0 && w.last != '\n' {
		count++
	}
	return sequenceStats{Bytes: w.bytes, Count: count, Digest: hex.EncodeToString(w.hash.Sum(nil))}
}

type hashWriter interface {
	io.Writer
	Sum([]byte) []byte
}

func countByte(value []byte, needle byte) int {
	count := 0
	for _, current := range value {
		if current == needle {
			count++
		}
	}
	return count
}

type multipartContextReader struct {
	ctx    context.Context
	reader io.Reader
}

type multipartContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w *multipartContextWriter) Write(p []byte) (int, error) {
	if err := contextError(w.ctx); err != nil {
		return 0, err
	}
	n, err := w.writer.Write(p)
	if contextErr := contextError(w.ctx); contextErr != nil {
		return n, contextErr
	}
	return n, err
}

func (r *multipartContextReader) Read(p []byte) (int, error) {
	if err := contextError(r.ctx); err != nil {
		return 0, err
	}
	n, err := r.reader.Read(p)
	if contextErr := contextError(r.ctx); contextErr != nil {
		return n, contextErr
	}
	return n, err
}

func normalizeMultipartReadOptions(options MultipartReadOptions) (Limits, Options, error) {
	if err := options.Limits.Validate(); err != nil {
		return Limits{}, Options{}, err
	}
	if err := options.Bundle.Limits.Validate(); err != nil {
		return Limits{}, Options{}, err
	}
	bundleOptions := options.Bundle
	if options.OrganizationID != "" {
		if bundleOptions.OrganizationID != "" && bundleOptions.OrganizationID != options.OrganizationID {
			return Limits{}, Options{}, fmt.Errorf("multipart organization options: %w", ErrScopeMismatch)
		}
		bundleOptions.OrganizationID = options.OrganizationID
	}
	if options.Organization.ID != "" || options.Organization.Name != "" {
		if bundleOptions.Organization.ID != "" && bundleOptions.Organization.ID != options.Organization.ID {
			return Limits{}, Options{}, fmt.Errorf("multipart organization options: %w", ErrScopeMismatch)
		}
		if bundleOptions.Organization.Name != "" && bundleOptions.Organization.Name != options.Organization.Name {
			return Limits{}, Options{}, fmt.Errorf("multipart organization options: %w", ErrScopeMismatch)
		}
		bundleOptions.Organization = options.Organization
	}
	if options.Analysis.ID != "" || options.Analysis.ConfigurationID != "" || options.Analysis.Revision != "" || options.Analysis.Hash != "" {
		if bundleOptions.Analysis.ConfigurationID != "" && bundleOptions.Analysis.ConfigurationID != options.Analysis.ConfigurationID {
			return Limits{}, Options{}, fmt.Errorf("multipart analysis options: %w", ErrScopeMismatch)
		}
		if bundleOptions.Analysis.ID != "" && bundleOptions.Analysis.ID != options.Analysis.ID {
			return Limits{}, Options{}, fmt.Errorf("multipart analysis options: %w", ErrScopeMismatch)
		}
		if bundleOptions.Analysis.Revision != "" && bundleOptions.Analysis.Revision != options.Analysis.Revision {
			return Limits{}, Options{}, fmt.Errorf("multipart analysis options: %w", ErrScopeMismatch)
		}
		if bundleOptions.Analysis.Hash != "" && bundleOptions.Analysis.Hash != options.Analysis.Hash {
			return Limits{}, Options{}, fmt.Errorf("multipart analysis options: %w", ErrScopeMismatch)
		}
		bundleOptions.Analysis = options.Analysis
	}
	limits := stricterLimits(defaultLimits(options.Limits), bundleOptions.Limits)
	bundleOptions.Limits = limits
	return limits, bundleOptions, nil
}

func validateMultipartPart(part *multipart.Part, expected multipartPart) error {
	if part == nil {
		return fmt.Errorf("multipart part: %w", ErrMultipartPart)
	}
	disposition, params, err := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
	if multipartPathValue(params["name"]) || multipartPathValue(params["filename"]) {
		return ErrMultipartTraversal
	}
	if err != nil || disposition != "form-data" || params["name"] != expected.name || params["filename"] != expected.name {
		return fmt.Errorf("multipart disposition: %w", ErrMultipartPart)
	}
	mediaType, _, mediaErr := mime.ParseMediaType(part.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != expected.mediaType {
		return fmt.Errorf("multipart media type: %w", ErrMultipartPart)
	}
	return nil
}

func multipartPathValue(value string) bool {
	return strings.Contains(value, "/") || strings.Contains(value, "\\") || strings.Contains(value, "..")
}

func partHeader(part multipartPart) textproto.MIMEHeader {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="`+part.name+`"; filename="`+part.name+`"`)
	header.Set("Content-Type", part.mediaType)
	return header
}

func validMultipartBoundary(boundary string) bool {
	if boundary == "" {
		return false
	}
	writer := multipart.NewWriter(io.Discard)
	if err := writer.SetBoundary(boundary); err != nil {
		return false
	}
	return true
}

func multipartContentType(boundary string) string {
	return mime.FormatMediaType("multipart/form-data", map[string]string{"boundary": boundary})
}

// MultipartContentType formats a validated boundary for a multipart request.
// It lets clients construct the Content-Type before they create a sender.
func MultipartContentType(boundary string) (string, error) {
	if !validMultipartBoundary(boundary) {
		return "", fmt.Errorf("multipart content type: %w", ErrMultipartInvalid)
	}
	return multipartContentType(boundary), nil
}

func multipartMinimum(left, right int64) int64 {
	if left < 0 || right < 0 {
		return -1
	}
	if left < right {
		return left
	}
	return right
}

func multipartReadError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrLimitExceeded) || errors.Is(err, ErrInvalid) || errors.Is(err, ErrUnsupportedVersion) {
		return err
	}
	return fmt.Errorf("reading multipart body: %w", ErrMultipartInvalid)
}
