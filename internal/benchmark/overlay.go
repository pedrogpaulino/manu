package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/source"
)

const defaultOverlayMarker = "manu-benchmark-localized-overlay"

type overlayInfo struct {
	Method      string
	Limitations []string
}

func chooseUpdatePath(configured string, artifacts []contract.Artifact) (string, error) {
	if configured != "" {
		cleaned, err := cleanRelativePath(configured)
		if err != nil {
			return "", fmt.Errorf("%w: update path: %v", ErrInvalidConfig, err)
		}
		for _, artifact := range artifacts {
			if artifact.Path == cleaned {
				return cleaned, nil
			}
		}
		return "", fmt.Errorf("%w: update path %q is not present in first analysis artifacts", ErrInvalidConfig, cleaned)
	}
	for _, artifact := range artifacts {
		if isLikelyTextPath(artifact.Path) {
			return artifact.Path, nil
		}
	}
	if len(artifacts) == 0 {
		return "", fmt.Errorf("%w: no artifact is available for localized update", ErrInvalidConfig)
	}
	return artifacts[0].Path, nil
}

func stageOverlay(ctx context.Context, root, relativePath, marker string, maxFileBytes int64) (string, overlayInfo, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", overlayInfo{}, func() {}, err
	}
	relativePath, err := cleanRelativePath(relativePath)
	if err != nil {
		return "", overlayInfo{}, func() {}, fmt.Errorf("%w: overlay path: %v", ErrInvalidConfig, err)
	}
	sourcePath := filepath.Join(root, filepath.FromSlash(relativePath))
	if !isWithin(root, sourcePath) {
		return "", overlayInfo{}, func() {}, fmt.Errorf("%w: overlay escapes root", ErrInvalidConfig)
	}
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return "", overlayInfo{}, func() {}, fmt.Errorf("stating overlay target: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", overlayInfo{}, func() {}, fmt.Errorf("%w: overlay target is not regular", ErrInvalidConfig)
	}

	stagedRoot, err := os.MkdirTemp("", "manu-benchmark-overlay-")
	if err != nil {
		return "", overlayInfo{}, func() {}, fmt.Errorf("creating overlay staging: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(stagedRoot) }
	method := "temporary_regular_file_staging"
	limitations := []string{
		"localized_update_is_simulated_in_temporary_staging",
		"simulation_does_not_measure_kernel_overlayfs_or_filesystem_copy_on_write",
		"localized_update_root_is_ephemeral_staging_and_is_removed_after_run",
	}
	if err := copyTree(ctx, root, stagedRoot); err != nil {
		cleanup()
		return "", overlayInfo{}, func() {}, err
	}
	if err := mutateOverlayFile(sourcePath, filepath.Join(stagedRoot, filepath.FromSlash(relativePath)), marker, maxFileBytes); err != nil {
		cleanup()
		return "", overlayInfo{}, func() {}, err
	}
	return stagedRoot, overlayInfo{Method: method, Limitations: limitations}, cleanup, nil
}

func copyTree(ctx context.Context, sourceRoot, destinationRoot string) error {
	return filepath.WalkDir(sourceRoot, func(pathName string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, pathName)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		destination := filepath.Join(destinationRoot, relative)
		mode := entry.Type()
		if mode&os.ModeSymlink != 0 {
			// The source runtime rejects links. Omitting one from the temporary
			// root keeps the overlay from creating a link that could cross the
			// authorized boundary.
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if err := os.Link(pathName, destination); err == nil {
			return nil
		}
		return copyFile(pathName, destination, info.Mode().Perm(), info.ModTime())
	})
}

func copyFile(sourcePath, destinationPath string, mode os.FileMode, modTime time.Time) error {
	input, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("opening overlay source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("creating overlay file: %w", err)
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(destinationPath)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("copying overlay file: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("syncing overlay file: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("closing overlay file: %w", err)
	}
	if err := os.Chtimes(destinationPath, modTime, modTime); err != nil {
		return fmt.Errorf("preserving overlay metadata: %w", err)
	}
	remove = false
	return nil
}

// mutateOverlayFile copies the selected source file in bounded chunks and
// appends a marker without retaining the source in memory. MaxFileBytes is the
// applicable limit here; MaxExtractionBytes governs previews, not this source
// staging operation.
func mutateOverlayFile(sourcePath, stagedPath, marker string, maxFileBytes int64) error {
	if marker == "" {
		marker = defaultOverlayMarker
	}
	if maxFileBytes <= 0 {
		return fmt.Errorf("%w: overlay file limit is not positive", ErrInvalidConfig)
	}
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stating localized overlay source: %w", err)
	}
	if info.Size() < 0 {
		return fmt.Errorf("%w: overlay source size is invalid", ErrInvalidConfig)
	}
	lastByte, hasLastByte, err := lastByte(sourcePath, info.Size())
	if err != nil {
		return fmt.Errorf("reading localized overlay tail: %w", err)
	}
	appendBytes := int64(len(marker) + 1)
	if !hasLastByte || lastByte != '\n' {
		appendBytes++
	}
	if info.Size() > maxFileBytes || appendBytes > maxFileBytes-info.Size() {
		return fmt.Errorf("%w: localized overlay exceeds MaxFileBytes", source.ErrLimitExceeded)
	}
	if err := os.Remove(stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("replacing localized overlay file: %w", err)
	}
	input, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("opening localized overlay source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(stagedPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("creating localized overlay file: %w", err)
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(stagedPath)
		}
	}()
	limited := io.LimitReader(input, info.Size())
	copied, err := io.Copy(output, limited)
	if err != nil {
		return fmt.Errorf("copying localized overlay source: %w", err)
	}
	if copied != info.Size() {
		return fmt.Errorf("%w: localized overlay source changed while copying", ErrInvalidConfig)
	}
	if !hasLastByte || lastByte != '\n' {
		if _, err := io.WriteString(output, "\n"); err != nil {
			return fmt.Errorf("writing localized overlay separator: %w", err)
		}
	}
	if _, err := io.WriteString(output, marker+"\n"); err != nil {
		return fmt.Errorf("writing localized overlay marker: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("syncing localized overlay file: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("closing localized overlay file: %w", err)
	}
	if err := os.Chtimes(stagedPath, info.ModTime(), info.ModTime()); err != nil {
		return fmt.Errorf("preserving localized overlay time: %w", err)
	}
	remove = false
	return nil
}

func lastByte(pathName string, size int64) (byte, bool, error) {
	if size == 0 {
		return 0, false, nil
	}
	file, err := os.Open(pathName)
	if err != nil {
		return 0, false, err
	}
	defer file.Close()
	var value [1]byte
	if _, err := file.ReadAt(value[:], size-1); err != nil {
		return 0, false, err
	}
	return value[0], true, nil
}

func cleanRelativePath(value string) (string, error) {
	value = strings.TrimSpace(filepath.ToSlash(value))
	if value == "" {
		return "", errors.New("path is empty")
	}
	if filepath.IsAbs(filepath.FromSlash(value)) || strings.HasPrefix(value, "/") {
		return "", errors.New("path must be relative")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("path traversal is not allowed")
	}
	return cleaned, nil
}

func isWithin(root, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isLikelyTextPath(pathName string) bool {
	switch strings.ToLower(filepath.Ext(pathName)) {
	case ".c", ".cc", ".cpp", ".css", ".csv", ".go", ".html", ".java", ".js", ".json", ".md", ".py", ".sql", ".txt", ".ts", ".xml", ".xsd", ".yaml", ".yml":
		return true
	default:
		return false
	}

}

func metadataDigest(root string) (string, error) {
	hash := sha256.New()
	paths := make([]string, 0)
	metadata := make(map[string]string)
	err := filepath.WalkDir(root, func(pathName string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, pathName)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		relative = filepath.ToSlash(relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := strings.Join([]string{
			strconv.FormatUint(uint64(info.Mode()), 10),
			strconv.FormatInt(info.Size(), 10),
			strconv.FormatInt(info.ModTime().UnixNano(), 10),
		}, "\x00")
		if entry.Type()&os.ModeSymlink != 0 {
			target, readErr := os.Readlink(pathName)
			if readErr != nil {
				return readErr
			}
			value += "\x00" + target
		}
		paths = append(paths, relative)
		metadata[relative] = value
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)
	for _, pathName := range paths {
		_, _ = fmt.Fprintf(hash, "%d:%s:%s\n", len(pathName), pathName, metadata[pathName])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func sampleHeap() HeapMetrics {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	maxRSS, method := linuxMaxRSS()
	return HeapMetrics{
		HeapAllocBytes: stats.HeapAlloc,
		HeapInuseBytes: stats.HeapInuse,
		MaxRSSBytes:    maxRSS,
		MaxRSSMethod:   method,
	}
}
