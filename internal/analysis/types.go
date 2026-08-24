// Package analysis composes bounded analyzers into the common result
// contract. It deliberately contains no language-specific parsing logic.
package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

const (
	// ArtifactTypeAny matches every discovered artifact.
	ArtifactTypeAny = "*"
	// ArtifactTypeText identifies a textual artifact without a more specific
	// language or package type.
	ArtifactTypeText = "text"
	// ArtifactTypeBinary identifies a binary artifact.
	ArtifactTypeBinary = "binary"
	// ArtifactTypeJava identifies a Java source file.
	ArtifactTypeJava = "java"
	// ArtifactTypePython identifies a Python source file.
	ArtifactTypePython = "python"
	// ArtifactTypeXML identifies an XML document.
	ArtifactTypeXML = "xml"
	// ArtifactTypeCAR identifies a WSO2 CAR package.
	ArtifactTypeCAR = "car"
	// ArtifactTypeZIP identifies a generic ZIP package.
	ArtifactTypeZIP = "zip"
)

var (
	// ErrInvalidAnalyzer identifies a malformed analyzer descriptor or a nil
	// analyzer passed to a registry.
	ErrInvalidAnalyzer = errors.New("analysis: invalid analyzer")
	// ErrDuplicateAnalyzer identifies a descriptor already registered.
	ErrDuplicateAnalyzer = errors.New("analysis: duplicate analyzer")
	// ErrInvalidRequest identifies a runner request that cannot be scoped.
	ErrInvalidRequest = errors.New("analysis: invalid request")
	// ErrInvalidEvidence identifies an evidence run that cannot be safely
	// materialized or validated.
	ErrInvalidEvidence = errors.New("analysis: invalid evidence request")
	// ErrEvidenceLimitExceeded identifies a configured evidence bound that
	// cannot be accepted.
	ErrEvidenceLimitExceeded = errors.New("analysis: evidence limit exceeded")
)

// Descriptor declares the contract and input types supported by an analyzer.
// Empty source or artifact type lists mean that the analyzer accepts every
// value. Fallback marks an analyzer that provides baseline observations and
// must remain selected when specialized analyzers are also applicable.
type Descriptor struct {
	ID              string   `json:"id"`
	Version         string   `json:"version"`
	Method          string   `json:"method"`
	ContractVersion string   `json:"contract_version"`
	SourceTypes     []string `json:"source_types,omitempty"`
	ArtifactTypes   []string `json:"artifact_types,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	Fallback        bool     `json:"fallback,omitempty"`
}

// Analyzer contributes observations to the common result. Implementations
// must not execute source content and must keep every emitted contribution
// traceable to input.Artifact and a locator.
type Analyzer interface {
	Descriptor() Descriptor
	Analyze(context.Context, ArtifactInput) (Output, error)
}

// RunContext keeps the cancellation context and the optional process signal
// stream together at the execution boundary. Analyzers still receive the
// standard context.Context so they cannot accidentally depend on CLI details.
type RunContext struct {
	context.Context
	Signals <-chan os.Signal
}

// NewRunContext creates a run context. A nil context is replaced with
// context.Background at the boundary; callers should normally pass the
// context returned by signal.NotifyContext.
func NewRunContext(ctx context.Context, signals <-chan os.Signal) RunContext {
	if ctx == nil {
		ctx = context.Background()
	}
	return RunContext{Context: ctx, Signals: signals}
}

// Signal returns the optional signal stream associated with the run.
func (r RunContext) Signal() <-chan os.Signal { return r.Signals }

// Base returns the standard context propagated to analyzers and readers.
func (r RunContext) Base() context.Context {
	if r.Context == nil {
		return context.Background()
	}
	return r.Context
}

// ArtifactInput is the bounded view of one discovered source artifact given
// to analyzers. File contents are intentionally not stored here. An analyzer
// can request bounded text or an archive member through methods on this type.
type ArtifactInput struct {
	SourceID       string
	SourceType     string
	Root           string
	Artifact       contract.Artifact
	SourceArtifact source.Artifact
	Limits         source.Limits
	// RootHandle is owned by the runner for the duration of a run. Analyzer
	// reads must stay relative to this confined root.
	RootHandle *os.Root
	// Evidence describes whether this execution requested bounded evidence
	// drafts. It is empty for the legacy Run path.
	Evidence EvidenceInput
}

// Input is a shorter compatibility spelling for ArtifactInput.
type Input = ArtifactInput

// Output contains only additive contributions and their explicit coverage
// and gaps. A nil output is normalized by the runner to empty collections.
type Output struct {
	Contributions []contract.Contribution `json:"contributions,omitempty"`
	Coverage      []contract.Coverage     `json:"coverage,omitempty"`
	Gaps          []contract.Gap          `json:"gaps,omitempty"`
	// Evidence contains in-memory drafts only. The runner sanitizes and
	// validates them before exposing Evidence Units; raw drafts are never
	// serialized as analyzer output.
	Evidence []EvidenceDraft `json:"-"`
}

// AnalysisOutput is the domain-oriented spelling of Output.
type AnalysisOutput = Output

// Text returns a bounded textual preview. Complete source contents are never
// retained by the input and the requested limit is capped by configured
// extraction limits.
func (i ArtifactInput) Text(ctx context.Context, includeContent bool) (source.TextResult, error) {
	if i.RootHandle == nil {
		return source.TextResult{}, fmt.Errorf("%w: root handle is required", ErrInvalidRequest)
	}
	limit := i.Limits.MaxExtractionBytes
	if limit <= 0 {
		limit = source.DefaultMaxExtractionBytes
	}
	return source.ExtractTextInRoot(ctx, i.RootHandle, i.Artifact.Path, source.TextOptions{
		MaxBytes:       limit,
		IncludeContent: includeContent,
	})
}

// EvidenceText returns a separately bounded textual preview for an evidence
// run. Its bound is intentionally smaller and independent from the legacy
// analyzer extraction limit so a draft cannot retain an entire source file.
func (i ArtifactInput) EvidenceText(ctx context.Context) (source.TextResult, error) {
	if i.RootHandle == nil {
		return source.TextResult{}, fmt.Errorf("%w: root handle is required", ErrInvalidRequest)
	}
	if i.Evidence.Limits.MaxUnitsPerArtifact < 0 || i.Evidence.Limits.MaxBytesPerUnit < 0 || i.Evidence.Limits.MaxCharactersPerUnit < 0 {
		return source.TextResult{}, fmt.Errorf("%w: evidence limits must not be negative", ErrEvidenceLimitExceeded)
	}
	limit := i.Evidence.Limits.MaxBytesPerUnit
	if limit <= 0 {
		limit = DefaultEvidenceMaxBytesPerUnit
	}
	if i.Limits.MaxExtractionBytes > 0 && i.Limits.MaxExtractionBytes < limit {
		limit = i.Limits.MaxExtractionBytes
	}
	return source.ExtractTextInRoot(ctx, i.RootHandle, i.Artifact.Path, source.TextOptions{
		MaxBytes:       limit,
		IncludeContent: true,
	})
}

// Archive inspects a ZIP or CAR in place. It never extracts members to disk.
func (i ArtifactInput) Archive(ctx context.Context) (source.ArchiveResult, error) {
	if i.RootHandle == nil {
		return source.ArchiveResult{}, fmt.Errorf("%w: root handle is required", ErrInvalidRequest)
	}
	limits := i.Limits
	return source.InspectArchiveInRoot(ctx, i.RootHandle, i.Artifact.Path, source.ArchiveOptions{
		MaxMembers:         limits.MaxArchiveMembers,
		MaxExpandedBytes:   limits.MaxArchiveBytes,
		MaxMemberBytes:     limits.MaxArchiveMemberBytes,
		MaxCompressedBytes: limits.MaxArchiveCompressedBytes,
		MaxExpansionRatio:  limits.MaxExpansionRatio,
	})
}

// ArchiveMember reads one bounded CAR member in memory. The package itself
// is never expanded to the filesystem.
func (i ArtifactInput) ArchiveMember(ctx context.Context, member string) ([]byte, bool, error) {
	if i.RootHandle == nil {
		return nil, false, fmt.Errorf("%w: root handle is required", ErrInvalidRequest)
	}
	limit := i.Limits.MaxExtractionBytes
	if limit <= 0 {
		limit = source.DefaultMaxExtractionBytes
	}
	return source.ReadArchiveMemberInRoot(ctx, i.RootHandle, i.Artifact.Path, member, limit)
}

// NewContribution constructs a provenance-complete contribution and derives
// its stable identity from the contract. Analyzer methods should use a
// method value that distinguishes each observation (for example,
// "type:BookingService" or "member:foo.xml/element:service").
func NewContribution(
	input ArtifactInput,
	descriptor Descriptor,
	method string,
	typ string,
	locator contract.Locator,
	value any,
) (contract.Contribution, error) {
	method = strings.TrimSpace(method)
	typ = strings.TrimSpace(typ)
	if method == "" || typ == "" {
		return contract.Contribution{}, fmt.Errorf("%w: contribution method and type are required", ErrInvalidRequest)
	}
	if descriptor.ID == "" || descriptor.Version == "" {
		return contract.Contribution{}, fmt.Errorf("%w: analyzer id and version are required", ErrInvalidAnalyzer)
	}
	if locator.SourceID == "" {
		locator.SourceID = input.SourceID
	}
	if locator.ArtifactID == "" {
		locator.ArtifactID = input.Artifact.ID
	}
	if locator.Path == "" {
		locator.Path = input.Artifact.Path
	}
	if err := locator.Validate(); err != nil {
		return contract.Contribution{}, fmt.Errorf("%w: locator: %v", ErrInvalidRequest, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return contract.Contribution{}, fmt.Errorf("encoding contribution value: %w", err)
	}
	contribution := contract.Contribution{
		ArtifactID:      input.Artifact.ID,
		AnalyzerID:      descriptor.ID,
		AnalyzerVersion: descriptor.Version,
		Method:          method,
		Type:            typ,
		Locator:         locator,
		Value:           encoded,
	}
	contribution.ID = contract.ContributionID(
		contribution.ArtifactID,
		contribution.AnalyzerID,
		contribution.AnalyzerVersion,
		contribution.Method,
	)
	return contribution, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: nil context", ErrInvalidRequest)
	}
	return ctx.Err()
}

func artifactType(artifact contract.Artifact, sourceArtifact source.Artifact) string {
	typ := strings.ToLower(strings.TrimSpace(artifact.Type))
	if typ != "" && typ != ArtifactTypeText && typ != ArtifactTypeBinary {
		return typ
	}
	extension := strings.ToLower(filepath.Ext(artifact.Path))
	if extension == "" {
		extension = strings.ToLower(filepath.Ext(sourceArtifact.RelativePath))
	}
	switch extension {
	case ".java":
		return ArtifactTypeJava
	case ".py":
		return ArtifactTypePython
	case ".xml", ".wsdl", ".xsd", ".xsl", ".xslt":
		return ArtifactTypeXML
	case ".car":
		return ArtifactTypeCAR
	case ".zip":
		return ArtifactTypeZIP
	}
	if sourceArtifact.Format == source.FormatCAR {
		return ArtifactTypeCAR
	}
	if sourceArtifact.Format == source.FormatZIP {
		return ArtifactTypeZIP
	}
	if sourceArtifact.Classification == source.ClassificationBinary {
		return ArtifactTypeBinary
	}
	return ArtifactTypeText
}
