package source

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// InspectArchive opens a ZIP or WSO2 CAR in place and validates its member
// metadata. It never extracts a member to disk and does not read member
// bodies unless the caller explicitly asks through ReadArchiveMember.
func InspectArchive(ctx context.Context, filePath string, options ArchiveOptions) (ArchiveResult, error) {
	format, err := archiveFormat(filePath)
	if err != nil {
		return ArchiveResult{}, err
	}
	return inspectArchive(ctx, filePath, format, options, func() (*os.File, error) {
		return openRegular(filePath)
	})
}

// InspectArchiveInRoot validates a ZIP or CAR named by a relative path below
// an authorized os.Root. It never extracts members. The caller owns root's
// lifecycle; the returned Path is the relative path supplied to the helper.
func InspectArchiveInRoot(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	options ArchiveOptions,
) (ArchiveResult, error) {
	relativePath, err := normalizeRootRelativePath(relativePath)
	if err != nil {
		return ArchiveResult{}, err
	}
	format, err := archiveFormat(relativePath)
	if err != nil {
		return ArchiveResult{}, err
	}
	return inspectArchive(ctx, relativePath, format, options, func() (*os.File, error) {
		return openRegularRoot(root, relativePath)
	})
}

func inspectArchive(
	ctx context.Context,
	filePath string,
	format Format,
	options ArchiveOptions,
	open func() (*os.File, error),
) (ArchiveResult, error) {
	if ctx == nil {
		return ArchiveResult{}, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	file, err := open()
	if err != nil {
		return ArchiveResult{}, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("stat archive %q: %w", filePath, err)
	}
	normalized, err := options.normalized()
	if err != nil {
		return ArchiveResult{}, err
	}
	if normalized.MaxCompressedBytes > 0 && stat.Size() > normalized.MaxCompressedBytes {
		return ArchiveResult{}, fmt.Errorf(
			"%w: archive %q is larger than %d compressed bytes",
			ErrLimitExceeded,
			filePath,
			normalized.MaxCompressedBytes,
		)
	}
	reader, err := zip.NewReader(file, stat.Size())
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("open %s %q: %w: %v", format, filePath, ErrUnsupported, err)
	}
	result, err := inspectZipReader(ctx, reader, normalized)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("inspect %q: %w", filePath, err)
	}
	result.Path = filePath
	result.Format = format
	return result, nil
}

// InspectArchiveBytes validates an in-memory ZIP/CAR without extracting it.
// It is intended for fuzzing and callers that already have a bounded package
// representation. The format must be zip or car.
func InspectArchiveBytes(
	ctx context.Context,
	data []byte,
	format Format,
	options ArchiveOptions,
) (ArchiveResult, error) {
	if ctx == nil {
		return ArchiveResult{}, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	if format != FormatZIP && format != FormatCAR {
		return ArchiveResult{}, fmt.Errorf("%w: archive format %q", ErrUnsupported, format)
	}
	if !hasZIPSignature(data) {
		return ArchiveResult{}, fmt.Errorf("%w: not a ZIP container", ErrUnsupported)
	}
	normalized, err := options.normalized()
	if err != nil {
		return ArchiveResult{}, err
	}
	if normalized.MaxCompressedBytes > 0 && int64(len(data)) > normalized.MaxCompressedBytes {
		return ArchiveResult{}, fmt.Errorf("%w: compressed archive bytes", ErrLimitExceeded)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("open archive bytes: %w: %v", ErrUnsupported, err)
	}
	result, err := inspectZipReader(ctx, reader, normalized)
	if err != nil {
		return ArchiveResult{}, err
	}
	result.Format = format
	return result, nil
}

// ReadArchiveMember reads one member into a bounded buffer without writing it
// to disk. The requested name is validated against the archive's normalized
// member names before it is opened.
func ReadArchiveMember(
	ctx context.Context,
	filePath string,
	memberName string,
	maxBytes int64,
) ([]byte, bool, error) {
	format, err := archiveFormat(filePath)
	if err != nil {
		return nil, false, err
	}
	return readArchiveMember(ctx, filePath, format, memberName, maxBytes, func() (*os.File, error) {
		return openRegular(filePath)
	})
}

// ReadArchiveMemberInRoot reads one bounded ZIP/CAR member from a relative
// path below an authorized os.Root. No member is extracted to disk and the
// caller owns root's lifecycle.
func ReadArchiveMemberInRoot(
	ctx context.Context,
	root *os.Root,
	relativePath string,
	memberName string,
	maxBytes int64,
) ([]byte, bool, error) {
	relativePath, err := normalizeRootRelativePath(relativePath)
	if err != nil {
		return nil, false, err
	}
	format, err := archiveFormat(relativePath)
	if err != nil {
		return nil, false, err
	}
	return readArchiveMember(ctx, relativePath, format, memberName, maxBytes, func() (*os.File, error) {
		return openRegularRoot(root, relativePath)
	})
}

func readArchiveMember(
	ctx context.Context,
	filePath string,
	format Format,
	memberName string,
	maxBytes int64,
	open func() (*os.File, error),
) ([]byte, bool, error) {
	if ctx == nil {
		return nil, false, fmt.Errorf("%w: nil context", ErrInvalidRoot)
	}
	file, err := open()
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, false, fmt.Errorf("stat %q: %w", filePath, err)
	}
	reader, err := zip.NewReader(file, stat.Size())
	if err != nil {
		return nil, false, fmt.Errorf("open %s %q: %w: %v", format, filePath, ErrUnsupported, err)
	}
	wanted, err := NormalizeArchiveMemberName(memberName)
	if err != nil {
		return nil, false, err
	}
	for _, member := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		name, err := NormalizeArchiveMemberName(member.Name)
		if err != nil {
			return nil, false, fmt.Errorf("member %q: %w", member.Name, err)
		}
		if name != wanted {
			continue
		}
		if member.Flags&0x1 != 0 {
			return nil, false, ErrArchiveEncrypted
		}
		if member.FileInfo().Mode()&os.ModeSymlink != 0 || !member.FileInfo().Mode().IsRegular() {
			return nil, false, fmt.Errorf("%w: member %q", ErrSpecialFile, name)
		}
		if maxBytes <= 0 {
			maxBytes = DefaultMaxExtractionBytes
		}
		if uint64(maxBytes) < member.UncompressedSize64 {
			return nil, false, fmt.Errorf("%w: member %q", ErrLimitExceeded, name)
		}
		body, err := member.Open()
		if err != nil {
			return nil, false, fmt.Errorf("open member %q: %w", name, err)
		}
		data, truncated, readErr := readLimited(ctx, body, maxBytes)
		closeErr := body.Close()
		if readErr != nil {
			return nil, false, fmt.Errorf("read member %q: %w", name, readErr)
		}
		if closeErr != nil {
			return nil, false, fmt.Errorf("close member %q: %w", name, closeErr)
		}
		return data, truncated, nil
	}
	return nil, false, os.ErrNotExist
}

// NormalizeArchiveMemberName rejects absolute, traversal, drive-qualified,
// NUL-containing, backslash-containing, and empty-component member names.
func NormalizeArchiveMemberName(memberName string) (string, error) {
	if memberName == "" || strings.IndexByte(memberName, 0) >= 0 {
		return "", fmt.Errorf("%w: empty or NUL name", ErrArchivePath)
	}
	if strings.Contains(memberName, "\\") || strings.HasPrefix(memberName, "/") || hasDrivePrefix(memberName) {
		return "", fmt.Errorf("%w: %q", ErrArchivePath, memberName)
	}
	directory := strings.HasSuffix(memberName, "/")
	trimmedName := strings.TrimSuffix(memberName, "/")
	parts := strings.Split(trimmedName, "/")
	for index, part := range parts {
		if index == len(parts)-1 && part == "" {
			return "", fmt.Errorf("%w: %q", ErrArchivePath, memberName)
		}
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("%w: %q", ErrArchivePath, memberName)
		}
	}
	clean := path.Clean(trimmedName)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrArchivePath, memberName)
	}
	if directory {
		clean += "/"
	}
	return clean, nil
}

func inspectZipReader(
	ctx context.Context,
	reader *zip.Reader,
	options ArchiveOptions,
) (ArchiveResult, error) {
	if len(reader.File) > options.MaxMembers {
		return ArchiveResult{}, fmt.Errorf("%w: archive has %d members", ErrLimitExceeded, len(reader.File))
	}
	result := ArchiveResult{
		Members: make([]ArchiveMember, 0, len(reader.File)),
	}
	seen := make(map[string]struct{}, len(reader.File))
	for _, member := range reader.File {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		name, err := NormalizeArchiveMemberName(member.Name)
		if err != nil {
			return result, fmt.Errorf("member %q: %w", member.Name, err)
		}
		if _, exists := seen[name]; exists {
			return result, fmt.Errorf("%w: duplicate member %q", ErrUnsupported, name)
		}
		seen[name] = struct{}{}
		if member.Flags&0x1 != 0 {
			return result, fmt.Errorf("member %q: %w", name, ErrArchiveEncrypted)
		}
		mode := member.FileInfo().Mode()
		if mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return result, fmt.Errorf("member %q: %w", name, ErrSpecialFile)
		}
		if member.Method != zip.Store && member.Method != zip.Deflate {
			return result, fmt.Errorf("member %q: %w: method %d", name, ErrUnsupported, member.Method)
		}
		if member.UncompressedSize64 > uint64(options.MaxMemberBytes) {
			return result, fmt.Errorf("%w: member %q expanded bytes", ErrLimitExceeded, name)
		}
		if member.CompressedSize64 > uint64(options.MaxCompressedBytes) {
			return result, fmt.Errorf("%w: member %q compressed bytes", ErrLimitExceeded, name)
		}
		if member.UncompressedSize64 > 0 {
			if member.CompressedSize64 == 0 || float64(member.UncompressedSize64)/float64(member.CompressedSize64) > options.MaxExpansionRatio {
				return result, fmt.Errorf("%w: member %q expansion ratio", ErrLimitExceeded, name)
			}
		}
		if result.ExpandedBytes > ^uint64(0)-member.UncompressedSize64 {
			return result, fmt.Errorf("%w: expanded byte overflow", ErrLimitExceeded)
		}
		if result.CompressedBytes > ^uint64(0)-member.CompressedSize64 {
			return result, fmt.Errorf("%w: compressed byte overflow", ErrLimitExceeded)
		}
		result.ExpandedBytes += member.UncompressedSize64
		result.CompressedBytes += member.CompressedSize64
		if result.ExpandedBytes > uint64(options.MaxExpandedBytes) {
			return result, fmt.Errorf("%w: archive expanded bytes", ErrLimitExceeded)
		}
		if result.CompressedBytes > uint64(options.MaxCompressedBytes) {
			return result, fmt.Errorf("%w: archive compressed bytes", ErrLimitExceeded)
		}
		result.Members = append(result.Members, ArchiveMember{
			Name:           name,
			Size:           member.UncompressedSize64,
			CompressedSize: member.CompressedSize64,
			Method:         member.Method,
			Directory:      mode.IsDir() || strings.HasSuffix(member.Name, "/"),
			Classification: classifyArchiveName(name),
		})
	}
	sort.Slice(result.Members, func(i, j int) bool {
		return result.Members[i].Name < result.Members[j].Name
	})
	return result, nil
}

func archiveFormat(filePath string) (Format, error) {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".zip":
		return FormatZIP, nil
	case ".car":
		return FormatCAR, nil
	default:
		return FormatUnknown, fmt.Errorf("%w: extension %q", ErrUnsupported, ext)
	}
}

func hasZIPSignature(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' &&
		((data[2] == 3 && data[3] == 4) || (data[2] == 5 && data[3] == 6) ||
			(data[2] == 7 && data[3] == 8))
}

func classifyArchiveName(name string) Classification {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".txt", ".md", ".markdown", ".xml", ".json", ".yaml", ".yml", ".properties", ".java", ".js", ".ts", ".py", ".sql", ".wsdl", ".xsd", ".xslt", ".xsl":
		return ClassificationText
	default:
		return ClassificationUnknown
	}
}
