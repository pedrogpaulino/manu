package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"
)

// HashResult contains a streaming SHA-256 digest and the number of bytes
// consumed. The digest is encoded in lowercase hexadecimal.
type HashResult struct {
	SHA256    string `json:"sha256"`
	BytesRead int64  `json:"bytes_read"`
}

// HashFile hashes one regular file without loading it into memory. A
// maxBytes value <= 0 means the caller does not impose a per-call limit.
func HashFile(ctx context.Context, filePath string, maxBytes int64) (HashResult, error) {
	return hashFile(ctx, filePath, maxBytes, func() (*os.File, error) {
		return openRegular(filePath)
	})
}

// HashFileInRoot hashes a regular file named by a relative path below an
// authorized os.Root. The caller owns root's lifecycle; the file is opened
// through the root and revalidated before any bytes are consumed.
func HashFileInRoot(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	maxBytes int64,
) (HashResult, error) {
	relativePath, err := normalizeRootRelativePath(relativePath)
	if err != nil {
		return HashResult{}, err
	}
	return hashFile(ctx, relativePath, maxBytes, func() (*os.File, error) {
		return openRegularRoot(root, relativePath)
	})
}

func hashFile(
	ctx context.Context,
	filePath string,
	maxBytes int64,
	open func() (*os.File, error),
) (HashResult, error) {
	if ctx == nil {
		return HashResult{}, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	if maxBytes < 0 {
		return HashResult{}, fmt.Errorf("%w: negative hash limit", ErrLimitExceeded)
	}
	file, err := open()
	if err != nil {
		return HashResult{}, err
	}
	defer file.Close()
	result, err := HashReader(ctx, file, maxBytes)
	if err != nil {
		return HashResult{}, fmt.Errorf("hash %q: %w", filePath, err)
	}
	return result, nil
}

// SHA256File is a convenience wrapper for callers that only need the digest.
func SHA256File(ctx context.Context, filePath string, maxBytes int64) (string, error) {
	result, err := HashFile(ctx, filePath, maxBytes)
	if err != nil {
		return "", err
	}
	return result.SHA256, nil
}

// HashReader hashes a stream while observing cancellation and an optional
// byte limit. It never closes reader.
func HashReader(ctx context.Context, reader io.Reader, maxBytes int64) (HashResult, error) {
	if ctx == nil {
		return HashResult{}, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	if reader == nil {
		return HashResult{}, fmt.Errorf("%w: nil reader", ErrInvalidRoot)
	}
	if maxBytes < 0 {
		return HashResult{}, fmt.Errorf("%w: negative hash limit", ErrLimitExceeded)
	}
	hash := sha256.New()
	buffer := make([]byte, 32*1024)
	var bytesRead int64
	for {
		if err := ctx.Err(); err != nil {
			return HashResult{BytesRead: bytesRead}, err
		}
		readSize, readErr := reader.Read(buffer)
		if readSize > 0 {
			if maxBytes > 0 && bytesRead > maxBytes-int64(readSize) {
				return HashResult{BytesRead: bytesRead}, fmt.Errorf(
					"%w: more than %d bytes",
					ErrLimitExceeded,
					maxBytes,
				)
			}
			if _, err := hash.Write(buffer[:readSize]); err != nil {
				return HashResult{BytesRead: bytesRead}, fmt.Errorf("hash write: %w", err)
			}
			bytesRead += int64(readSize)
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			return HashResult{BytesRead: bytesRead}, fmt.Errorf("read: %w", readErr)
		}
		if readSize == 0 {
			return HashResult{BytesRead: bytesRead}, io.ErrNoProgress
		}
	}
	if err := ctx.Err(); err != nil {
		return HashResult{BytesRead: bytesRead}, err
	}
	return HashResult{
		SHA256:    hex.EncodeToString(hash.Sum(nil)),
		BytesRead: bytesRead,
	}, nil
}

// ClassifyFile reads only a bounded prefix and classifies the file as text or
// binary. It rejects symlinks and special files before opening.
func ClassifyFile(ctx context.Context, filePath string, maxProbeBytes int64) (Classification, int64, error) {
	if maxProbeBytes < 0 {
		return ClassificationUnknown, 0, fmt.Errorf("%w: negative probe limit", ErrLimitExceeded)
	}
	file, err := openRegular(filePath)
	if err != nil {
		return ClassificationUnknown, 0, err
	}
	defer file.Close()
	return ClassifyReader(ctx, file, maxProbeBytes)
}

// ClassifyReader reads at most maxProbeBytes and does not close reader. Empty
// input is considered text; invalid UTF-8, NUL bytes, and a high ratio of
// control characters are classified as binary.
func ClassifyReader(ctx context.Context, reader io.Reader, maxProbeBytes int64) (Classification, int64, error) {
	if ctx == nil {
		return ClassificationUnknown, 0, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	if reader == nil {
		return ClassificationUnknown, 0, fmt.Errorf("%w: nil reader", ErrInvalidRoot)
	}
	if maxProbeBytes < 0 {
		return ClassificationUnknown, 0, fmt.Errorf("%w: negative probe limit", ErrLimitExceeded)
	}
	if maxProbeBytes == 0 {
		maxProbeBytes = DefaultMaxProbeBytes
	}
	probe := make([]byte, maxProbeBytes)
	readSize, err := readWithContext(ctx, reader, probe)
	if err != nil && err != io.EOF {
		return ClassificationUnknown, int64(readSize), fmt.Errorf("classify read: %w", err)
	}
	return classifyBytes(probe[:readSize]), int64(readSize), nil
}

// ExtractText returns classification and bounded metadata. Unless
// IncludeContent is explicitly true, Content remains empty even for text.
func ExtractText(ctx context.Context, filePath string, options TextOptions) (TextResult, error) {
	return extractText(ctx, filePath, options, func() (*os.File, error) {
		return openRegular(filePath)
	})
}

// ExtractTextInRoot classifies and optionally extracts a regular file named by
// a relative path below an authorized os.Root. The caller owns root's
// lifecycle and must close it. The path is never reopened through an absolute
// Artifact.Path, so replacement links cannot escape the authorized root.
func ExtractTextInRoot(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	options TextOptions,
) (TextResult, error) {
	relativePath, err := normalizeRootRelativePath(relativePath)
	if err != nil {
		return TextResult{}, err
	}
	return extractText(ctx, relativePath, options, func() (*os.File, error) {
		return openRegularRoot(root, relativePath)
	})
}

// ReadTextInRoot is the explicit-content counterpart to ExtractTextInRoot.
// The returned content remains bounded by maxBytes and is never written to
// disk.
func ReadTextInRoot(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	maxBytes int64,
) (TextResult, error) {
	return ExtractTextInRoot(ctx, root, relativePath, TextOptions{
		MaxBytes:       maxBytes,
		IncludeContent: true,
	})
}

func extractText(
	ctx context.Context,
	filePath string,
	options TextOptions,
	open func() (*os.File, error),
) (TextResult, error) {
	if options.MaxBytes < 0 {
		return TextResult{}, fmt.Errorf("%w: negative extraction limit", ErrLimitExceeded)
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxExtractionBytes
	}
	file, err := open()
	if err != nil {
		return TextResult{}, err
	}
	defer file.Close()

	classification, probeBytes, err := ClassifyReader(ctx, file, minInt64(maxBytes, DefaultMaxProbeBytes))
	if err != nil {
		return TextResult{}, fmt.Errorf("classify %q: %w", filePath, err)
	}
	result := TextResult{Classification: classification, BytesRead: probeBytes}
	if !options.IncludeContent {
		return result, nil
	}
	if classification != ClassificationText {
		return result, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return TextResult{}, fmt.Errorf("rewind %q: %w", filePath, err)
	}
	data, truncated, err := readLimited(ctx, file, maxBytes)
	if err != nil {
		return TextResult{}, fmt.Errorf("extract %q: %w", filePath, err)
	}
	result.BytesRead = int64(len(data))
	result.Truncated = truncated
	result.Content = string(data)
	return result, nil
}

// ReadText is an explicit-content convenience wrapper. The limit is always
// enforced and the result is never larger than maxBytes.
func ReadText(ctx context.Context, filePath string, maxBytes int64) (TextResult, error) {
	return ExtractText(ctx, filePath, TextOptions{
		MaxBytes:       maxBytes,
		IncludeContent: true,
	})
}

func classifyBytes(data []byte) Classification {
	if len(data) == 0 {
		return ClassificationText
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return ClassificationBinary
	}
	control := 0
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' && b != '\f' {
			control++
		}
	}
	if control > 0 && control*10 > len(data) {
		return ClassificationBinary
	}
	return ClassificationText
}

func readWithContext(ctx context.Context, reader io.Reader, buffer []byte) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	readSize, err := reader.Read(buffer)
	if contextErr := ctx.Err(); contextErr != nil {
		return readSize, contextErr
	}
	return readSize, err
}

func readLimited(ctx context.Context, reader io.Reader, maxBytes int64) ([]byte, bool, error) {
	if maxBytes < 0 {
		return nil, false, fmt.Errorf("%w: negative read limit", ErrLimitExceeded)
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxExtractionBytes
	}
	buffer := make([]byte, 32*1024)
	result := make([]byte, 0, minInt64(maxBytes, 32*1024))
	var bytesRead int64
	for {
		if err := ctx.Err(); err != nil {
			return result, false, err
		}
		remaining := maxBytes + 1 - bytesRead
		if remaining <= 0 {
			return result, true, nil
		}
		readBuffer := buffer
		if int64(len(readBuffer)) > remaining {
			readBuffer = readBuffer[:remaining]
		}
		readSize, err := readWithContext(ctx, reader, readBuffer)
		if readSize > 0 {
			bytesRead += int64(readSize)
			if bytesRead > maxBytes {
				result = append(result, readBuffer[:maxBytes-bytesRead+int64(readSize)]...)
				return result, true, nil
			}
			result = append(result, readBuffer[:readSize]...)
		}
		if err != nil {
			if err == io.EOF {
				return result, false, nil
			}
			return result, false, err
		}
		if readSize == 0 {
			return result, false, io.ErrNoProgress
		}
	}
}

func openRegular(filePath string) (*os.File, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %q", ErrSymlink, filePath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q", ErrSpecialFile, filePath)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat %q: %w", filePath, err)
	}
	if !openedInfo.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: %q", ErrSpecialFile, filePath)
	}
	return file, nil
}

// openRegularRoot opens a path relative to an os.Root. The Lstat checks keep
// ordinary symlinks excluded while Root.Open provides confinement if a path is
// replaced between discovery and opening. The post-open checks ensure that a
// replacement with a special file is not accepted as a regular artifact.
func openRegularRoot(root *os.Root, relativePath string) (*os.File, error) {
	if root == nil {
		return nil, fmt.Errorf("%w: nil root", ErrInvalidRoot)
	}
	validatedPath, err := normalizeRootRelativePath(relativePath)
	if err != nil {
		return nil, err
	}
	if validatedPath == "." {
		return nil, fmt.Errorf("%w: root is not a file", ErrSpecialFile)
	}
	relativePath = validatedPath
	name := filepath.FromSlash(relativePath)
	info, err := lstatRootPath(root, relativePath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: %q", ErrSymlink, relativePath)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %q", ErrSpecialFile, relativePath)
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("stat %q: %w", relativePath, err)
	}
	if !openedInfo.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: %q", ErrSpecialFile, relativePath)
	}
	currentInfo, err := lstatRootPath(root, relativePath)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("revalidate %q: %w", relativePath, err)
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 {
		file.Close()
		return nil, fmt.Errorf("%w: %q", ErrSymlink, relativePath)
	}
	if !currentInfo.Mode().IsRegular() {
		file.Close()
		return nil, fmt.Errorf("%w: %q", ErrSpecialFile, relativePath)
	}
	return file, nil
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}
