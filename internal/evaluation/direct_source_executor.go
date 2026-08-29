package evaluation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// DirectSourceGenerationNotExecuted is a controlled limitation attached to
	// every local baseline result. This adapter never invokes a Generator.
	DirectSourceGenerationNotExecuted = "generation_not_executed"
	DirectSourceLimitsReached         = "source_limits_reached"

	defaultDirectSourceMaxFiles     = 256
	defaultDirectSourceMaxBytes     = 8 << 20
	defaultDirectSourceMaxFileBytes = 1 << 20
	maxDirectSourceFiles            = 4_096
	maxDirectSourceBytes            = 64 << 20
	maxDirectSourceFileBytes        = 64 << 20
)

var (
	// ErrInvalidDirectSourceExecutor identifies an absent or unsafe root or a
	// malformed read budget. The configured root is never included in it.
	ErrInvalidDirectSourceExecutor = errors.New("evaluation: invalid direct-source executor")
	// ErrDirectSourceUnavailable identifies an artifact that could not be read
	// under the authorized root. It intentionally carries no path or OS detail.
	ErrDirectSourceUnavailable = errors.New("evaluation: direct-source unavailable")
	// ErrDirectSourceUnsafePath identifies traversal, an absolute path, or a
	// symlink resolving outside the authorized root.
	ErrDirectSourceUnsafePath = errors.New("evaluation: direct-source unsafe path")

	// Descriptive aliases keep callers independent from the exact sentinel
	// spelling while preserving one safe error identity.
	ErrDirectSourceNotConfigured = ErrInvalidDirectSourceExecutor
	ErrDirectSourceReadFailed    = ErrDirectSourceUnavailable
)

// DirectSourceLimits bounds all filesystem work performed by the baseline.
// A zero value is replaced by DefaultDirectSourceLimits at construction time.
type DirectSourceLimits struct {
	MaxFiles     int   `json:"max_files"`
	MaxBytes     int64 `json:"max_bytes"`
	MaxFileBytes int64 `json:"max_file_bytes"`
}

// DefaultDirectSourceLimits returns the bounded local baseline budget.
func DefaultDirectSourceLimits() DirectSourceLimits {
	return DirectSourceLimits{
		MaxFiles: defaultDirectSourceMaxFiles, MaxBytes: defaultDirectSourceMaxBytes,
		MaxFileBytes: defaultDirectSourceMaxFileBytes,
	}
}

// Validate checks that every direct-source limit is positive and bounded.
func (l DirectSourceLimits) Validate() error {
	if l.MaxFiles < 1 || l.MaxFiles > maxDirectSourceFiles ||
		l.MaxBytes < 1 || l.MaxBytes > maxDirectSourceBytes ||
		l.MaxFileBytes < 1 || l.MaxFileBytes > maxDirectSourceFileBytes {
		return ErrInvalidDirectSourceExecutor
	}
	return nil
}

// DirectSourceExecutorConfig contains only construction-time local data. Root
// never crosses the result, metric, digest, or error boundary.
type DirectSourceExecutorConfig struct {
	Root   string             `json:"-"`
	Limits DirectSourceLimits `json:"limits"`
}

// DirectSourceConfig is the descriptive spelling used by local callers.
type DirectSourceConfig = DirectSourceExecutorConfig

// DirectSourceExecutor is a read-only, bounded filesystem baseline. It opens
// only the relative artifacts declared by the evaluation case scope.
type DirectSourceExecutor struct {
	root     string
	resolved string
	limits   DirectSourceLimits
}

var _ VariantExecutor = (*DirectSourceExecutor)(nil)

// NewDirectSourceExecutor validates an absolute root and freezes its physical
// resolution. A relative root, missing directory, or non-directory is rejected.
func NewDirectSourceExecutor(config DirectSourceExecutorConfig) (*DirectSourceExecutor, error) {
	root := strings.TrimSpace(config.Root)
	if root == "" || root != config.Root || !filepath.IsAbs(root) || !utf8.ValidString(root) {
		return nil, ErrInvalidDirectSourceExecutor
	}
	limits := config.Limits
	if limits == (DirectSourceLimits{}) {
		limits = DefaultDirectSourceLimits()
	}
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	cleanRoot := filepath.Clean(root)
	resolvedRoot, err := filepath.EvalSymlinks(cleanRoot)
	if err != nil {
		return nil, ErrInvalidDirectSourceExecutor
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		return nil, ErrInvalidDirectSourceExecutor
	}
	return &DirectSourceExecutor{
		root: cleanRoot, resolved: filepath.Clean(resolvedRoot), limits: limits,
	}, nil
}

// Validate verifies the frozen executor shape without exposing the root.
func (e *DirectSourceExecutor) Validate() error {
	if e == nil || e.root == "" || e.resolved == "" ||
		!filepath.IsAbs(e.root) || !filepath.IsAbs(e.resolved) {
		return ErrInvalidDirectSourceExecutor
	}
	return e.limits.Validate()
}

// Execute reads all in-scope artifacts until the explicit bounded budget is
// reached. It returns a partial result because generation is deliberately not
// executed. Read failures and cancellation are returned as safe sentinels.
func (e *DirectSourceExecutor) Execute(ctx context.Context, request VariantExecutionRequest) (VariantExecutionResult, error) {
	if ctx == nil {
		return VariantExecutionResult{}, ErrInvalidVariantRequest
	}
	if err := ctx.Err(); err != nil {
		return VariantExecutionResult{}, err
	}
	if err := e.Validate(); err != nil {
		return VariantExecutionResult{}, err
	}
	if err := request.Validate(); err != nil || request.Variant.Kind != VariantDirectSource || !directSourcePolicyAllowed(request.Policy) {
		return VariantExecutionResult{}, ErrInvalidVariantRequest
	}
	started := time.Now()
	reads, filesRead, bytesRead, limited, err := e.readArtifacts(ctx, request.Case.Scope.Artifacts)
	if err != nil {
		return VariantExecutionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return VariantExecutionResult{}, err
	}
	matched := directSourceExpectedEvidence(request, reads)
	digest, err := directSourceOutputDigest(request, reads, matched, filesRead, bytesRead)
	if err != nil {
		return VariantExecutionResult{}, ErrDirectSourceUnavailable
	}
	toolCalls := int64(filesRead)
	files := int64(filesRead)
	bytesMetric := bytesRead
	metrics := &VariantMetrics{
		ObserverID:      "direct-source-executor",
		ObserverVersion: VariantExecutionVersion,
		ToolCalls:       &toolCalls,
		FilesRead:       &files,
		BytesRead:       &bytesMetric,
		Duration:        directSourceDuration(time.Since(started)),
	}
	limitations := []string{DirectSourceGenerationNotExecuted}
	if limited {
		limitations = append(limitations, DirectSourceLimitsReached)
	}
	return VariantExecutionResult{
		Version: VariantExecutionVersion, Status: VariantStatusLimited,
		Conclusion: VariantConclusionPartial, OutputDigest: digest,
		EvidenceIDs: matched, Limitations: limitations, Metrics: metrics,
	}, nil
}

type directSourceTarget struct {
	path     string
	resolved string
}

type directSourceRead struct {
	path     string
	content  []byte
	bytes    int64
	complete bool
}

// readArtifacts validates every declared path before consuming any budget.
// This prevents a traversal or external symlink hidden after MaxFiles from
// being silently ignored.
func (e *DirectSourceExecutor) readArtifacts(ctx context.Context, artifactPaths []string) ([]directSourceRead, int, int64, bool, error) {
	targets := make([]directSourceTarget, 0, len(artifactPaths))
	for _, artifactPath := range artifactPaths {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, false, err
		}
		relative, err := directSourceRelativePath(artifactPath)
		if err != nil {
			return nil, 0, 0, false, err
		}
		resolved, err := e.resolveArtifact(relative)
		if err != nil {
			return nil, 0, 0, false, err
		}
		targets = append(targets, directSourceTarget{path: relative, resolved: resolved})
	}

	reads := make([]directSourceRead, 0, minDirectSourceInt(len(targets), e.limits.MaxFiles))
	filesRead := 0
	var bytesRead int64
	limited := false
	for index, target := range targets {
		if err := ctx.Err(); err != nil {
			return nil, 0, 0, false, err
		}
		if index >= e.limits.MaxFiles || bytesRead >= e.limits.MaxBytes {
			limited = true
			break
		}
		info, err := os.Stat(target.resolved)
		if err != nil || !info.Mode().IsRegular() {
			return nil, 0, 0, false, ErrDirectSourceUnavailable
		}
		remaining := e.limits.MaxBytes - bytesRead
		readLimit := e.limits.MaxFileBytes
		complete := true
		if info.Size() > readLimit {
			complete = false
			limited = true
		}
		if remaining < readLimit {
			readLimit = remaining
			complete = false
			limited = true
		}
		if readLimit < 1 {
			limited = true
			break
		}
		file, err := os.Open(target.resolved)
		if err != nil {
			return nil, 0, 0, false, ErrDirectSourceUnavailable
		}
		data, readErr := io.ReadAll(io.LimitReader(file, readLimit+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, 0, 0, false, ErrDirectSourceUnavailable
		}
		if int64(len(data)) > readLimit {
			data = data[:readLimit]
			complete = false
			limited = true
		}
		readBytes := int64(len(data))
		reads = append(reads, directSourceRead{path: target.path, content: data, bytes: readBytes, complete: complete})
		filesRead++
		bytesRead += readBytes
		if !complete || bytesRead >= e.limits.MaxBytes {
			if index+1 < len(targets) {
				limited = true
			}
			if bytesRead >= e.limits.MaxBytes {
				break
			}
		}
	}
	if len(targets) > filesRead && filesRead >= e.limits.MaxFiles {
		limited = true
	}
	return reads, filesRead, bytesRead, limited, nil
}

func (e *DirectSourceExecutor) resolveArtifact(relative string) (string, error) {
	candidate := filepath.Join(e.resolved, filepath.FromSlash(relative))
	if !directSourcePathWithin(e.resolved, candidate) {
		return "", ErrDirectSourceUnsafePath
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", ErrDirectSourceUnavailable
		}
		return "", ErrDirectSourceUnavailable
	}
	resolved = filepath.Clean(resolved)
	if !directSourcePathWithin(e.resolved, resolved) {
		return "", ErrDirectSourceUnsafePath
	}
	return resolved, nil
}

func directSourceRelativePath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\\') || path.IsAbs(value) {
		return "", ErrDirectSourceUnsafePath
	}
	if len(value) >= 2 && value[1] == ':' && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) {
		return "", ErrDirectSourceUnsafePath
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return "", ErrDirectSourceUnsafePath
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", ErrDirectSourceUnsafePath
	}
	return cleaned, nil
}

func directSourcePathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return false
	}
	return !filepath.IsAbs(relative)
}

func directSourcePolicyAllowed(policy EvaluationPolicy) bool {
	return (policy.SourceAccess == "read-only" || policy.SourceAccess == "direct-read-only") &&
		policy.ExternalTransfer == "deny" && policy.NetworkAccess == "disabled" && policy.MutationAccess == "disabled"
}

func directSourceExpectedEvidence(request VariantExecutionRequest, reads []directSourceRead) []string {
	matched := make([]string, 0, len(request.Case.ExpectedEvidence))
	for _, expected := range request.Case.ExpectedEvidence {
		if expected.EvidenceID == "" {
			continue
		}
		if !directSourceExpectedPathInScope(expected, request.Case.Scope.Artifacts) {
			continue
		}
		for _, read := range reads {
			if read.complete && directSourceExpectedMatchesRead(expected, request.SourceID, read) {
				matched = append(matched, expected.EvidenceID)
				break
			}
		}
	}
	sort.Strings(matched)
	return directSourceUniqueStrings(matched)
}

func directSourceExpectedPathInScope(expected ExpectedEvidence, artifacts []string) bool {
	pattern := ""
	if expected.Locator != nil {
		pattern = expected.Locator.Path
	} else if expected.Pattern != nil {
		pattern = expected.Pattern.PathPattern
	}
	if !directSourceSafeExpectedPath(pattern) {
		return false
	}
	for _, artifact := range artifacts {
		relative, err := directSourceRelativePath(artifact)
		if err == nil && directSourcePortablePathMatch(pattern, relative) {
			return true
		}
	}
	return false
}

func directSourceExpectedMatchesRead(expected ExpectedEvidence, sourceID string, read directSourceRead) bool {
	if expected.Locator != nil {
		locator := expected.Locator
		if locator.SourceID != "" && locator.SourceID != sourceID {
			return false
		}
		if locator.ArtifactID != "" || locator.Path == "" || !directSourcePortablePathMatch(locator.Path, read.path) || !directSourceLocatorRangeExists(*locator, read.content) {
			return false
		}
		rangeContent, ok := directSourceLocatorRange(*locator, read.content)
		return ok && directSourceAllTokensObserved([]string{locator.Member}, rangeContent)
	}
	if expected.Pattern == nil || !directSourcePortablePathMatch(expected.Pattern.PathPattern, read.path) {
		return false
	}
	return directSourceAllTokensObserved([]string{expected.Pattern.Member, expected.Pattern.Symbol, expected.Pattern.Attribute, expected.Pattern.XPath, expected.Pattern.ValuePattern}, read.content)
}

func directSourceSafeExpectedPath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\\') || path.IsAbs(value) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == ".." {
			return false
		}
	}
	cleaned := path.Clean(value)
	return cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func directSourcePortablePathMatch(pattern, value string) bool {
	pattern = path.Clean(strings.ReplaceAll(pattern, "\\", "/"))
	value = path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if pattern == value {
		return true
	}
	matched, err := path.Match(pattern, value)
	return err == nil && matched
}

func directSourceLocatorRangeExists(locator contract.Locator, content []byte) bool {
	if locator.StartLine > 0 || locator.EndLine > 0 {
		if locator.StartLine < 1 || locator.EndLine < locator.StartLine {
			return false
		}
		lineCount := bytes.Count(content, []byte{'\n'}) + 1
		if len(content) == 0 || locator.EndLine > lineCount {
			return false
		}
	}
	if locator.ByteOffset > 0 || locator.ByteLength > 0 {
		if locator.ByteOffset < 0 || locator.ByteLength < 0 || locator.ByteOffset > int64(len(content)) || locator.ByteLength > int64(len(content))-locator.ByteOffset {
			return false
		}
	}
	return true
}

// directSourceLocatorRange returns only the bytes selected by a locator. A
// zero line range and a zero byte range each mean that the corresponding
// dimension is unconstrained (the complete file). When both dimensions are
// present, their intersection is used so a token cannot be observed outside
// either declared range.
func directSourceLocatorRange(locator contract.Locator, content []byte) ([]byte, bool) {
	if !directSourceLocatorRangeExists(locator, content) {
		return nil, false
	}
	start, end := 0, len(content)
	if locator.StartLine > 0 || locator.EndLine > 0 {
		lineStart := directSourceLineOffset(content, locator.StartLine)
		lineEnd := len(content)
		if locator.EndLine < bytes.Count(content, []byte{'\n'})+1 {
			lineEnd = directSourceLineOffset(content, locator.EndLine+1)
		}
		if lineStart > start {
			start = lineStart
		}
		if lineEnd < end {
			end = lineEnd
		}
	}
	if locator.ByteOffset > 0 || locator.ByteLength > 0 {
		byteStart := int(locator.ByteOffset)
		byteEnd := byteStart + int(locator.ByteLength)
		if byteStart > start {
			start = byteStart
		}
		if byteEnd < end {
			end = byteEnd
		}
	}
	if start > end {
		return nil, false
	}
	return content[start:end], true
}

func directSourceLineOffset(content []byte, line int) int {
	if line <= 1 {
		return 0
	}
	current := 1
	for index, value := range content {
		if value != '\n' {
			continue
		}
		current++
		if current == line {
			return index + 1
		}
	}
	return len(content)
}

func directSourceAllTokensObserved(tokens []string, content []byte) bool {
	text := strings.ToLower(string(content))
	for _, token := range tokens {
		if token != "" && !strings.Contains(text, strings.ToLower(token)) {
			return false
		}
	}
	return true
}

type directSourceDigestProjection struct {
	Version     string                       `json:"version"`
	CaseID      string                       `json:"case_id"`
	CaseVersion int                          `json:"case_version"`
	SourceID    string                       `json:"source_id"`
	SourceRev   string                       `json:"source_revision"`
	Artifacts   []directSourceDigestArtifact `json:"artifacts"`
	EvidenceIDs []string                     `json:"evidence_ids"`
	FilesRead   int                          `json:"files_read"`
	BytesRead   int64                        `json:"bytes_read"`
}

type directSourceDigestArtifact struct {
	Path          string `json:"path"`
	Bytes         int64  `json:"bytes"`
	Complete      bool   `json:"complete"`
	ContentDigest string `json:"content_digest"`
}

func directSourceOutputDigest(request VariantExecutionRequest, reads []directSourceRead, matched []string, filesRead int, bytesRead int64) (string, error) {
	artifacts := make([]directSourceDigestArtifact, 0, len(reads))
	for _, read := range reads {
		contentDigest := sha256.Sum256(read.content)
		artifacts = append(artifacts, directSourceDigestArtifact{
			Path: read.path, Bytes: read.bytes, Complete: read.complete,
			ContentDigest: hex.EncodeToString(contentDigest[:]),
		})
	}
	projection := directSourceDigestProjection{
		Version: VariantExecutionVersion, CaseID: request.Case.CaseID, CaseVersion: request.Case.CaseVersion,
		SourceID: request.SourceID, SourceRev: request.SourceRevision, Artifacts: artifacts,
		EvidenceIDs: append([]string(nil), matched...), FilesRead: filesRead, BytesRead: bytesRead,
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func directSourceDuration(duration time.Duration) *VariantDuration {
	if duration < 0 {
		duration = 0
	}
	return &VariantDuration{Value: duration.Nanoseconds(), Unit: VariantDurationNanoseconds}
}

func directSourceUniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if value == "" || (len(result) > 0 && result[len(result)-1] == value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func minDirectSourceInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
