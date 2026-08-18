package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type discoveryCandidate struct {
	path         string
	relativePath string
	size         int64
}

type discoveryState struct {
	mu             sync.Mutex
	result         DiscoveryResult
	reservedBytes  int64
	limitTriggered bool
}

// Discover walks a configured source without following links or writing to
// it. Hashing and classification run in a bounded worker pool. The returned
// result retains successful artifacts when cancellation, a limit, or an
// individual file failure stops the remaining work.
func Discover(ctx context.Context, config Config) (DiscoveryResult, error) {
	started := time.Now()
	if ctx == nil {
		return DiscoveryResult{}, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	config, root, limits, err := normalizedConfig(config)
	if err != nil {
		return DiscoveryResult{}, err
	}
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return DiscoveryResult{}, fmt.Errorf("open source root: %w", err)
	}
	defer rootHandle.Close()
	workCtx := ctx
	var cancel context.CancelFunc
	if limits.MaxDuration > 0 {
		workCtx, cancel = context.WithTimeout(ctx, limits.MaxDuration)
	} else {
		workCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	state := &discoveryState{result: DiscoveryResult{
		Root:        root,
		Artifacts:   make([]Artifact, 0),
		Exclusions:  make([]Exclusion, 0),
		Failures:    make([]Failure, 0),
		Concurrency: limits.MaxConcurrency,
	}}

	jobs := make(chan discoveryCandidate)
	var workers sync.WaitGroup
	workers.Add(limits.MaxConcurrency)
	for range limits.MaxConcurrency {
		go func() {
			defer workers.Done()
			for {
				select {
				case <-workCtx.Done():
					return
				case candidate, ok := <-jobs:
					if !ok {
						return
					}
					processCandidate(workCtx, rootHandle, candidate, limits, state, cancel)
				}
			}
		}()
	}

	walkErr := filepath.WalkDir(root, func(pathName string, entry fs.DirEntry, walkErr error) error {
		if err := workCtx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			relativePath := relativePathFor(root, pathName)
			state.addFailure(FailureTechnical, relativePath, failureCodeRead, "filesystem entry could not be read")
			return nil
		}
		if entry == nil {
			state.addFailure(FailureTechnical, relativePathFor(root, pathName), failureCodeRead, "filesystem entry is missing")
			return nil
		}
		if entryIsSymlink(entry) {
			state.addFailure(FailureSecurity, relativePathFor(root, pathName), failureCodeSymlink, "symbolic links are excluded")
			return nil
		}
		relativePath := relativePathFor(root, pathName)
		if entry.IsDir() {
			if pattern := excludePattern(relativePath, config.Excludes); pattern != "" && relativePath != "." {
				state.addExclusion(relativePath, pattern, "configured exclusion")
				return fs.SkipDir
			}
			return nil
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			state.addFailure(FailureTechnical, relativePath, failureCodeRead, "filesystem metadata could not be read")
			return nil
		}
		if entryIsSpecial(info) {
			state.addFailure(FailureSecurity, relativePath, failureCodeSpecial, "special files are excluded")
			return nil
		}
		if !info.Mode().IsRegular() {
			state.addFailure(FailureSecurity, relativePath, failureCodeSpecial, "non-regular files are excluded")
			return nil
		}
		if !includePath(relativePath, config.Includes) {
			state.addExclusion(relativePath, "", "outside configured inclusions")
			return nil
		}
		if pattern := excludePattern(relativePath, config.Excludes); pattern != "" {
			state.addExclusion(relativePath, pattern, "configured exclusion")
			return nil
		}
		if !config.IncludeSensitive {
			patterns := append(DefaultSensitivePatterns(), config.SensitivePatterns...)
			if sensitive, pattern := IsSensitivePath(relativePath, patterns); sensitive {
				state.addExclusion(relativePath, pattern, "sensitive path")
				return nil
			}
		}
		if limits.MaxFiles > 0 && state.fileCount() >= limits.MaxFiles {
			state.addFailure(FailureLimit, relativePath, failureCodeLimitFiles, "maximum file count reached")
			state.triggerLimit(nil)
			return fs.SkipAll
		}
		if info.Size() < 0 || (limits.MaxFileBytes > 0 && info.Size() > limits.MaxFileBytes) {
			state.addFailure(FailureLimit, relativePath, failureCodeLimitFile, "file exceeds the per-file byte limit")
			state.triggerLimit(nil)
			return fs.SkipAll
		}
		if limits.MaxBytes > 0 && !state.reserveBytes(info.Size(), limits.MaxBytes) {
			state.addFailure(FailureLimit, relativePath, failureCodeLimitBytes, "source exceeds the total byte limit")
			state.triggerLimit(nil)
			return fs.SkipAll
		}
		state.incrementFiles()
		candidate := discoveryCandidate{
			path:         pathName,
			relativePath: relativePath,
			size:         info.Size(),
		}
		select {
		case jobs <- candidate:
			return nil
		case <-workCtx.Done():
			return workCtx.Err()
		}
	})
	close(jobs)
	workers.Wait()

	state.mu.Lock()
	result := state.result
	result.DurationNanos = time.Since(started).Nanoseconds()
	result.Limited = state.limitTriggered
	result.Cancelled = !state.limitTriggered && (ctx.Err() != nil || errors.Is(workCtx.Err(), context.DeadlineExceeded))
	sort.Slice(result.Artifacts, func(i, j int) bool {
		return result.Artifacts[i].RelativePath < result.Artifacts[j].RelativePath
	})
	sort.Slice(result.Exclusions, func(i, j int) bool {
		return result.Exclusions[i].Path < result.Exclusions[j].Path
	})
	sort.Slice(result.Failures, func(i, j int) bool {
		if result.Failures[i].Path != result.Failures[j].Path {
			return result.Failures[i].Path < result.Failures[j].Path
		}
		return result.Failures[i].Code < result.Failures[j].Code
	})
	state.mu.Unlock()

	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) &&
		!errors.Is(walkErr, context.Canceled) && !errors.Is(walkErr, context.DeadlineExceeded) {
		return result, fmt.Errorf("walk source: %w", walkErr)
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if errors.Is(workCtx.Err(), context.DeadlineExceeded) {
		return result, context.DeadlineExceeded
	}
	return result, nil
}

func processCandidate(
	ctx context.Context,
	root *os.Root,
	candidate discoveryCandidate,
	limits Limits,
	state *discoveryState,
	cancel context.CancelFunc,
) {
	hashResult, classification, bytesRead, err := inspectRootFile(
		ctx,
		root,
		candidate.relativePath,
		candidate.size,
		limits.MaxProbeBytes,
	)
	// Account for every byte consumed by the confined stream, including a
	// partial read that ends in cancellation or another error. This is done
	// exactly once, before handling the result, so metrics describe actual I/O
	// rather than only successful artifacts.
	state.addBytes(bytesRead)
	if err != nil {
		state.addFailure(failureKindFor(err), candidate.relativePath, failureCodeFor(err), safeFailureMessage(err))
		if errors.Is(err, ErrLimitExceeded) {
			state.triggerLimit(cancel)
		}
		return
	}
	state.addArtifact(Artifact{
		Path:           candidate.path,
		RelativePath:   candidate.relativePath,
		Size:           candidate.size,
		SHA256:         hashResult.SHA256,
		Classification: classification,
		Format:         formatForPath(candidate.relativePath),
	})
}

// inspectRootFile hashes and classifies one artifact in a single bounded
// stream. maxBytes is strict here: unlike the public HashFile helper where a
// zero means "unlimited", zero means that no content may be read. Discovery
// passes the file's reserved source size, so total I/O cannot exceed the
// configured MaxBytes budget even when classification is requested.
func inspectRootFile(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	maxBytes int64,
	maxProbeBytes int64,
) (HashResult, Classification, int64, error) {
	if ctx == nil {
		return HashResult{}, ClassificationUnknown, 0, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	if maxBytes < 0 || maxProbeBytes < 0 {
		return HashResult{}, ClassificationUnknown, 0, fmt.Errorf("%w: negative inspection limit", ErrLimitExceeded)
	}
	if maxProbeBytes == 0 {
		maxProbeBytes = DefaultMaxProbeBytes
	}
	file, err := openRegularRoot(root, relativePath)
	if err != nil {
		return HashResult{}, ClassificationUnknown, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return HashResult{}, ClassificationUnknown, 0, fmt.Errorf("stat %q: %w", relativePath, err)
	}
	if info.Size() < 0 || info.Size() > maxBytes {
		return HashResult{}, ClassificationUnknown, 0, fmt.Errorf("%w: %q exceeds the reserved byte budget", ErrLimitExceeded, relativePath)
	}

	hash := sha256.New()
	probe := make([]byte, 0, minInt64(maxProbeBytes, 32*1024))
	buffer := make([]byte, 32*1024)
	var bytesRead int64
	for {
		if err := ctx.Err(); err != nil {
			return HashResult{BytesRead: bytesRead}, ClassificationUnknown, bytesRead, err
		}
		if bytesRead == maxBytes {
			break
		}
		readBuffer := buffer
		remaining := maxBytes - bytesRead
		if int64(len(readBuffer)) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		readSize, readErr := file.Read(readBuffer)
		if readSize > 0 {
			if _, err := hash.Write(readBuffer[:readSize]); err != nil {
				return HashResult{BytesRead: bytesRead}, ClassificationUnknown, bytesRead, fmt.Errorf("hash write: %w", err)
			}
			if int64(len(probe)) < maxProbeBytes {
				remainingProbe := maxProbeBytes - int64(len(probe))
				if int64(readSize) < remainingProbe {
					remainingProbe = int64(readSize)
				}
				probe = append(probe, readBuffer[:remainingProbe]...)
			}
			bytesRead += int64(readSize)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return HashResult{BytesRead: bytesRead}, ClassificationUnknown, bytesRead, fmt.Errorf("read %q: %w", relativePath, readErr)
		}
		if readSize == 0 {
			return HashResult{BytesRead: bytesRead}, ClassificationUnknown, bytesRead, io.ErrNoProgress
		}
	}
	if err := ctx.Err(); err != nil {
		return HashResult{BytesRead: bytesRead}, ClassificationUnknown, bytesRead, err
	}
	return HashResult{
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
		BytesRead: bytesRead,
	}, classifyBytes(probe), bytesRead, nil
}

func (state *discoveryState) addArtifact(artifact Artifact) {
	state.mu.Lock()
	state.result.Artifacts = append(state.result.Artifacts, artifact)
	state.mu.Unlock()
}

func (state *discoveryState) addFailure(kind FailureKind, pathName, code, message string) {
	state.mu.Lock()
	state.result.Failures = append(state.result.Failures, Failure{
		Kind:    kind,
		Path:    pathName,
		Code:    code,
		Message: message,
	})
	state.mu.Unlock()
}

func (state *discoveryState) addExclusion(pathName, pattern, reason string) {
	state.mu.Lock()
	state.result.Exclusions = append(state.result.Exclusions, Exclusion{
		Path:    pathName,
		Pattern: pattern,
		Reason:  reason,
	})
	state.mu.Unlock()
}

func (state *discoveryState) addBytes(bytesRead int64) {
	state.mu.Lock()
	state.result.BytesRead += bytesRead
	state.mu.Unlock()
}

func (state *discoveryState) incrementFiles() {
	state.mu.Lock()
	state.result.FilesDiscovered++
	state.mu.Unlock()
}

func (state *discoveryState) fileCount() int {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.result.FilesDiscovered
}

func (state *discoveryState) reserveBytes(size, max int64) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	if size < 0 || state.reservedBytes > max-size {
		return false
	}
	state.reservedBytes += size
	return true
}

func (state *discoveryState) triggerLimit(cancel context.CancelFunc) {
	state.mu.Lock()
	state.limitTriggered = true
	state.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func relativePathFor(root, pathName string) string {
	relativePath, err := filepath.Rel(root, pathName)
	if err != nil || relativePath == "." {
		return "."
	}
	return filepath.ToSlash(relativePath)
}

func formatForPath(pathName string) Format {
	switch strings.ToLower(filepath.Ext(pathName)) {
	case ".zip":
		return FormatZIP
	case ".car":
		return FormatCAR
	default:
		return FormatUnknown
	}
}

func failureKindFor(err error) FailureKind {
	switch {
	case errors.Is(err, ErrSymlink), errors.Is(err, ErrSpecialFile), errors.Is(err, ErrPathTraversal):
		return FailureSecurity
	case errors.Is(err, ErrLimitExceeded):
		return FailureLimit
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return FailureCancelled
	case errors.Is(err, ErrUnsupported):
		return FailureUnsupported
	default:
		return FailureTechnical
	}
}

func failureCodeFor(err error) string {
	switch {
	case errors.Is(err, ErrSymlink):
		return failureCodeSymlink
	case errors.Is(err, ErrSpecialFile):
		return failureCodeSpecial
	case errors.Is(err, ErrPathTraversal):
		return failureCodeTraversal
	case errors.Is(err, ErrLimitExceeded):
		return failureCodeLimitFile
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return failureCodeCancelled
	case errors.Is(err, ErrUnsupported):
		return failureCodeUnsupported
	default:
		return failureCodeHash
	}
}

func safeFailureMessage(err error) string {
	switch {
	case errors.Is(err, ErrSymlink):
		return "symbolic links are excluded"
	case errors.Is(err, ErrSpecialFile):
		return "special files are excluded"
	case errors.Is(err, ErrPathTraversal):
		return "path is outside the configured root"
	case errors.Is(err, ErrLimitExceeded):
		return "configured resource limit exceeded"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "work was cancelled"
	case errors.Is(err, ErrUnsupported):
		return "input format is not supported"
	default:
		return "source could not be read or hashed"
	}
}
