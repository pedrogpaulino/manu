package source

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	failureCodeRoot        = "invalid_root"
	failureCodeTraversal   = "path_traversal"
	failureCodeSymlink     = "symlink"
	failureCodeSpecial     = "special_file"
	failureCodeSensitive   = "sensitive"
	failureCodeLimitFiles  = "max_files"
	failureCodeLimitBytes  = "max_bytes"
	failureCodeLimitFile   = "max_file_bytes"
	failureCodeCancelled   = "cancelled"
	failureCodeHash        = "hash"
	failureCodeClassify    = "classify"
	failureCodeArchive     = "archive"
	failureCodeUnsupported = "unsupported"
	failureCodeRead        = "read"
)

var defaultSensitivePatterns = []string{
	".env",
	".env.*",
	"**/.env",
	"**/.env.*",
	"*.pem",
	"**/*.pem",
	"*.key",
	"**/*.key",
	"*.p12",
	"**/*.p12",
	"*.pfx",
	"**/*.pfx",
	"id_rsa",
	"id_dsa",
	"id_ecdsa",
	"id_ed25519",
	"**/id_rsa",
	"**/id_dsa",
	"**/id_ecdsa",
	"**/id_ed25519",
	"**/secrets.*",
	"**/*secret*",
	"**/*password*",
	"**/*credential*",
	"**/*token*",
}

// DefaultSensitivePatterns returns a defensive copy of the built-in
// sensitive-file patterns.
func DefaultSensitivePatterns() []string {
	patterns := make([]string, len(defaultSensitivePatterns))
	copy(patterns, defaultSensitivePatterns)
	return patterns
}

// NormalizeRoot validates and cleans a configured source root. The root must
// already exist, be a directory, and not itself be a symbolic link.
func NormalizeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidRoot)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: absolute path: %v", ErrInvalidRoot, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRoot, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: root", ErrSymlink)
	}
	if info.IsDir() {
		return abs, nil
	}
	if info.Mode()&os.ModeType != 0 {
		return "", fmt.Errorf("%w: root is special", ErrSpecialFile)
	}
	return "", fmt.Errorf("%w: root is not a directory", ErrInvalidRoot)
}

// NormalizeRelativePath converts a configured relative path or an absolute
// path under root to a portable slash-separated path. Absolute paths outside
// root, drive-qualified paths, NUL bytes, and traversal components are
// rejected before any filesystem access.
func NormalizeRelativePath(root, candidate string) (string, error) {
	if strings.IndexByte(candidate, 0) >= 0 {
		return "", fmt.Errorf("%w: NUL byte", ErrPathTraversal)
	}
	if strings.Contains(candidate, "\\") {
		return "", fmt.Errorf("%w: alternate path separator", ErrPathTraversal)
	}
	if candidate == "" || candidate == "." {
		return ".", nil
	}
	if hasDrivePrefix(candidate) {
		return "", fmt.Errorf("%w: drive-qualified path", ErrPathTraversal)
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: root: %v", ErrPathTraversal, err)
	}
	rootAbs = filepath.Clean(rootAbs)

	pathCandidate := candidate
	if filepath.IsAbs(candidate) {
		pathCandidate = candidate
	} else {
		// filepath.Join cleans lexical components, so validate the relative
		// form separately before joining to avoid accepting ../ paths.
		if hasParentComponent(candidate) {
			return "", fmt.Errorf("%w: %q", ErrPathTraversal, candidate)
		}
		pathCandidate = filepath.Join(rootAbs, candidate)
	}
	pathCandidate = filepath.Clean(pathCandidate)
	rel, err := filepath.Rel(rootAbs, pathCandidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrPathTraversal, err)
	}
	if rel == ".." || isParentPath(rel) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, candidate)
	}
	if rel == "." {
		return ".", nil
	}
	return filepath.ToSlash(rel), nil
}

// normalizeRootRelativePath accepts only a portable relative path for use
// with os.Root. Absolute paths and traversal components are rejected even when
// they would happen to resolve below the root; this keeps the confined API's
// contract explicit at its boundary.
func normalizeRootRelativePath(relativePath string) (string, error) {
	if relativePath == "" || relativePath == "." {
		return ".", nil
	}
	if strings.IndexByte(relativePath, 0) >= 0 || strings.Contains(relativePath, "\\") ||
		strings.HasPrefix(relativePath, "/") || filepath.IsAbs(relativePath) ||
		hasDrivePrefix(relativePath) || hasParentComponent(relativePath) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, relativePath)
	}
	clean := path.Clean(filepath.ToSlash(relativePath))
	if clean == "." || clean == ".." || isParentSlashPath(clean) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, relativePath)
	}
	return clean, nil
}

// lstatRootPath checks every component so the confined readers reject a
// symlinked directory as well as a symlinked file. Root.Open still supplies
// the final confinement guarantee if a component changes after this check.
func lstatRootPath(root *os.Root, relativePath string) (fs.FileInfo, error) {
	parts := strings.Split(relativePath, "/")
	currentPath := ""
	var info fs.FileInfo
	for index, part := range parts {
		currentPath = path.Join(currentPath, part)
		var err error
		info, err = root.Lstat(filepath.FromSlash(currentPath))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("%w: %q", ErrSymlink, currentPath)
		}
		if index < len(parts)-1 && !info.IsDir() {
			return nil, fmt.Errorf("%w: %q", ErrSpecialFile, currentPath)
		}
	}
	return info, nil
}

func hasDrivePrefix(candidate string) bool {
	return len(candidate) >= 2 && ((candidate[0] >= 'a' && candidate[0] <= 'z') ||
		(candidate[0] >= 'A' && candidate[0] <= 'Z')) && candidate[1] == ':'
}

func hasParentComponent(candidate string) bool {
	for _, component := range strings.FieldsFunc(candidate, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == ".." {
			return true
		}
	}
	return false
}

// IsSensitivePath reports whether a relative path matches a configured
// sensitive pattern. Matching is done against both the full relative path and
// its base name so patterns such as *.pem apply in nested directories.
func IsSensitivePath(relativePath string, patterns []string) (bool, string) {
	relativePath = filepath.ToSlash(filepath.Clean(relativePath))
	if relativePath == "." || relativePath == ".." || isParentSlashPath(relativePath) {
		return false, ""
	}
	if len(patterns) == 0 {
		patterns = defaultSensitivePatterns
	}
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.TrimSpace(pattern))
		if pattern == "" {
			continue
		}
		if matchSlashGlob(pattern, relativePath) || matchSlashGlob(pattern, path.Base(relativePath)) {
			return true, pattern
		}
	}
	return false, ""
}

func isParentSlashPath(value string) bool {
	return value == ".." || strings.HasPrefix(value, "../")
}

// matchSlashGlob implements path.Match-like segments with a recursive **
// component. It avoids converting untrusted patterns into regular expressions.
func matchSlashGlob(pattern, value string) bool {
	patternParts := splitSlash(pattern)
	valueParts := splitSlash(value)
	var match func(int, int) bool
	match = func(pi, vi int) bool {
		if pi == len(patternParts) {
			return vi == len(valueParts)
		}
		if patternParts[pi] == "**" {
			if match(pi+1, vi) {
				return true
			}
			return vi < len(valueParts) && match(pi, vi+1)
		}
		if vi >= len(valueParts) {
			return false
		}
		ok, err := path.Match(patternParts[pi], valueParts[vi])
		return err == nil && ok && match(pi+1, vi+1)
	}
	return match(0, 0)
}

func splitSlash(value string) []string {
	value = strings.Trim(value, "/")
	if value == "" {
		return nil
	}
	return strings.Split(value, "/")
}

func validatePatterns(patterns []string) error {
	for _, pattern := range patterns {
		if strings.IndexByte(pattern, 0) >= 0 || strings.Contains(pattern, "\\") || hasDrivePrefix(pattern) || filepath.IsAbs(pattern) {
			return fmt.Errorf("%w: pattern %q", ErrPathTraversal, pattern)
		}
		if hasParentComponent(pattern) {
			return fmt.Errorf("%w: pattern %q", ErrPathTraversal, pattern)
		}
	}
	return nil
}

func normalizedConfig(config Config) (Config, string, Limits, error) {
	root, err := NormalizeRoot(config.Root)
	if err != nil {
		return Config{}, "", Limits{}, err
	}
	if err := validatePatterns(config.Includes); err != nil {
		return Config{}, "", Limits{}, err
	}
	if err := validatePatterns(config.Excludes); err != nil {
		return Config{}, "", Limits{}, err
	}
	if err := validatePatterns(config.SensitivePatterns); err != nil {
		return Config{}, "", Limits{}, err
	}
	limits, err := config.Limits.normalized()
	if err != nil {
		return Config{}, "", Limits{}, err
	}
	config.Root = root
	config.Includes = append([]string(nil), config.Includes...)
	config.Excludes = append([]string(nil), config.Excludes...)
	config.SensitivePatterns = append([]string(nil), config.SensitivePatterns...)
	return config, root, limits, nil
}

func includePath(relativePath string, includes []string) bool {
	if len(includes) == 0 {
		return true
	}
	for _, pattern := range includes {
		if matchSlashGlob(filepath.ToSlash(pattern), relativePath) ||
			matchSlashGlob(filepath.ToSlash(pattern), path.Base(relativePath)) {
			return true
		}
	}
	return false
}

func excludePattern(relativePath string, excludes []string) string {
	for _, pattern := range excludes {
		pattern = filepath.ToSlash(pattern)
		if matchSlashGlob(pattern, relativePath) || matchSlashGlob(pattern, path.Base(relativePath)) {
			return pattern
		}
	}
	return ""
}

func entryIsSymlink(entry fs.DirEntry) bool {
	return entry.Type()&os.ModeSymlink != 0
}

func entryIsSpecial(info fs.FileInfo) bool {
	return !info.Mode().IsRegular() && !info.IsDir()
}
