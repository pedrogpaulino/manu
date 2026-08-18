package source

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectArchiveBytesWithoutExtraction(t *testing.T) {
	data := makeZip(t, map[string]string{
		"flows/":         "",
		"flows/main.xml": "<flow/>\n",
		"docs/readme.md": "read me\n",
	})
	result, err := InspectArchiveBytes(context.Background(), data, FormatCAR, ArchiveOptions{
		MaxMembers:         10,
		MaxExpandedBytes:   1 << 20,
		MaxMemberBytes:     1 << 20,
		MaxCompressedBytes: 1 << 20,
		MaxExpansionRatio:  100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Format != FormatCAR || len(result.Members) != 3 {
		t.Fatalf("archive result = %+v, want CAR with three members", result)
	}
	if result.Members[0].Name != "docs/readme.md" || result.Members[1].Name != "flows/" {
		t.Fatalf("members are not sorted and normalized: %+v", result.Members)
	}
	if result.Members[0].Classification != ClassificationText {
		t.Fatalf("markdown classification = %q, want text", result.Members[0].Classification)
	}
	if result.Members[2].Classification != ClassificationText {
		t.Fatalf("xml classification = %q, want text", result.Members[2].Classification)
	}

	memberData, truncated, err := readArchiveMemberBytes(t, data, "flows/main.xml", 64)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || string(memberData) != "<flow/>\n" {
		t.Fatalf("member data = %q, truncated = %v", memberData, truncated)
	}
}

func TestInspectArchiveRejectsUnsafeMembersAndUnsupportedFormats(t *testing.T) {
	tests := []struct {
		name      string
		member    string
		wantError error
	}{
		{name: "parent", member: "../outside.txt", wantError: ErrArchivePath},
		{name: "absolute", member: "/outside.txt", wantError: ErrArchivePath},
		{name: "backslash", member: `..\outside.txt`, wantError: ErrArchivePath},
		{name: "empty component", member: "dir//file.txt", wantError: ErrArchivePath},
		{name: "dot component", member: "dir/./file.txt", wantError: ErrArchivePath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := makeZip(t, map[string]string{test.member: "untrusted"})
			_, err := InspectArchiveBytes(context.Background(), data, FormatZIP, ArchiveOptions{})
			if !errors.Is(err, test.wantError) {
				t.Fatalf("InspectArchiveBytes(%q) error = %v, want %v", test.member, err, test.wantError)
			}
		})
	}
	if _, err := InspectArchiveBytes(context.Background(), []byte("not an archive"), FormatZIP, ArchiveOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("invalid archive error = %v, want ErrUnsupported", err)
	}
	if _, err := InspectArchiveBytes(context.Background(), makeZip(t, map[string]string{"file.txt": "data"}), Format("tar"), ArchiveOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported format error = %v, want ErrUnsupported", err)
	}
	if _, err := NormalizeArchiveMemberName("a\x00b"); !errors.Is(err, ErrArchivePath) {
		t.Fatalf("NUL member error = %v, want ErrArchivePath", err)
	}
}

func TestInspectArchiveLimitsAndUnsupportedCompression(t *testing.T) {
	data := makeZip(t, map[string]string{
		"one.txt": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"two.txt": "small",
	})
	tests := []struct {
		name string
		opts ArchiveOptions
	}{
		{name: "member count", opts: ArchiveOptions{MaxMembers: 1}},
		{name: "expanded bytes", opts: ArchiveOptions{MaxExpandedBytes: 10}},
		{name: "member bytes", opts: ArchiveOptions{MaxMemberBytes: 10}},
		{name: "compressed bytes", opts: ArchiveOptions{MaxCompressedBytes: 1}},
		{name: "ratio", opts: ArchiveOptions{MaxExpansionRatio: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := InspectArchiveBytes(context.Background(), data, FormatZIP, test.opts)
			if !errors.Is(err, ErrLimitExceeded) {
				t.Fatalf("InspectArchiveBytes(%s) error = %v, want ErrLimitExceeded", test.name, err)
			}
		})
	}

	unsupported := makeUnsupportedMethodZip(t)
	if _, err := InspectArchiveBytes(context.Background(), unsupported, FormatZIP, ArchiveOptions{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unsupported method error = %v, want ErrUnsupported", err)
	}
}

func TestReadArchiveMemberHonorsLimitAndDoesNotWrite(t *testing.T) {
	root := t.TempDir()
	archivePath := filepath.Join(root, "sample.zip")
	data := makeZip(t, map[string]string{"nested/file.txt": "content that is bounded"})
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	beforeEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadArchiveMember(context.Background(), archivePath, "../outside", 100); !errors.Is(err, ErrArchivePath) {
		t.Fatalf("unsafe requested member error = %v, want ErrArchivePath", err)
	}
	if _, _, err := ReadArchiveMember(context.Background(), archivePath, "nested/file.txt", 5); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("member limit error = %v, want ErrLimitExceeded", err)
	}
	afterEntries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(beforeEntries) != len(afterEntries) {
		t.Fatalf("archive read changed destination entries: before=%d after=%d", len(beforeEntries), len(afterEntries))
	}
}

func TestConfinedArchiveReaderUsesRelativePath(t *testing.T) {
	rootPath := t.TempDir()
	archivePath := filepath.Join(rootPath, "bundle.car")
	data := makeZip(t, map[string]string{"nested/file.txt": "member content"})
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	result, err := InspectArchiveInRoot(context.Background(), root, "bundle.car", ArchiveOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != "bundle.car" || result.Format != FormatCAR || len(result.Members) != 1 {
		t.Fatalf("confined archive result = %+v, want relative CAR metadata", result)
	}
	member, truncated, err := ReadArchiveMemberInRoot(
		context.Background(),
		root,
		"bundle.car",
		"nested/file.txt",
		64,
	)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || string(member) != "member content" {
		t.Fatalf("confined member = %q, truncated = %v", member, truncated)
	}
	if _, err := InspectArchiveInRoot(context.Background(), root, archivePath, ArchiveOptions{}); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("absolute archive path error = %v, want ErrPathTraversal", err)
	}
}

func TestNormalizeArchiveMemberName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantError bool
	}{
		{name: "file", input: "dir/file.txt", want: "dir/file.txt"},
		{name: "directory", input: "dir/", want: "dir/"},
		{name: "parent", input: "../file", wantError: true},
		{name: "absolute", input: "/file", wantError: true},
		{name: "empty", input: "", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeArchiveMemberName(test.input)
			if test.wantError {
				if !errors.Is(err, ErrArchivePath) {
					t.Fatalf("NormalizeArchiveMemberName(%q) error = %v, want ErrArchivePath", test.input, err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("NormalizeArchiveMemberName(%q) = %q, %v; want %q", test.input, got, err, test.want)
			}
		})
	}
}

func FuzzNormalizeArchiveMemberName(f *testing.F) {
	f.Add("safe/file.xml")
	f.Add("../outside")
	f.Add("/absolute")
	f.Add("..\\outside")
	f.Add("dir/")
	f.Fuzz(func(t *testing.T, memberName string) {
		got, err := NormalizeArchiveMemberName(memberName)
		if err == nil && (got == ".." || bytes.HasPrefix([]byte(got), []byte("../"))) {
			t.Fatalf("accepted traversal member %q from %q", got, memberName)
		}
	})
}

func FuzzInspectArchiveBytes(f *testing.F) {
	f.Add(makeZip(tForFuzz{}, map[string]string{"file.txt": "text"}))
	f.Add([]byte("not a zip"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256*1024 {
			t.Skip()
		}
		_, _ = InspectArchiveBytes(context.Background(), data, FormatZIP, ArchiveOptions{
			MaxMembers:         8,
			MaxExpandedBytes:   64 * 1024,
			MaxMemberBytes:     16 * 1024,
			MaxCompressedBytes: 64 * 1024,
			MaxExpansionRatio:  20,
		})
	})
}

// tForFuzz is the smallest testing.T-compatible helper needed by makeZip for
// a deterministic fuzz seed without allocating a real test object.
type tForFuzz struct{}

func (tForFuzz) Helper() {}

func (tForFuzz) Fatal(args ...any) { panic(args) }

func makeZip(t interface {
	Helper()
	Fatal(...any)
}, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range files {
		header := &zip.FileHeader{Name: name, Method: zip.Deflate}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func makeUnsupportedMethodZip(t *testing.T) []byte {
	t.Helper()
	data := makeZip(t, map[string]string{"file.txt": "data"})
	for index := 0; index+3 < len(data); index++ {
		if data[index] != 'P' || data[index+1] != 'K' {
			continue
		}
		if data[index+2] == 3 && data[index+3] == 4 {
			data[index+8] = 99
			data[index+9] = 0
		}
		if data[index+2] == 1 && data[index+3] == 2 {
			data[index+10] = 99
			data[index+11] = 0
		}
	}
	return data
}

func readArchiveMemberBytes(t *testing.T, data []byte, name string, maxBytes int64) ([]byte, bool, error) {
	t.Helper()
	root := t.TempDir()
	archivePath := filepath.Join(root, "sample.zip")
	if err := os.WriteFile(archivePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return ReadArchiveMember(context.Background(), archivePath, name, maxBytes)
}
