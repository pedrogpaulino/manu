package source

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyFileAndExtractText(t *testing.T) {
	root := t.TempDir()
	textPath := filepath.Join(root, "text.txt")
	binaryPath := filepath.Join(root, "binary.dat")
	invalidPath := filepath.Join(root, "invalid.bin")
	if err := os.WriteFile(textPath, []byte("line one\nline two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte{0x00, 0x01, 0x02, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidPath, []byte{0xff, 0xfe, 0xfd}, 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		path      string
		want      Classification
		wantError error
	}{
		{name: "text", path: textPath, want: ClassificationText},
		{name: "binary nul", path: binaryPath, want: ClassificationBinary},
		{name: "binary invalid utf8", path: invalidPath, want: ClassificationBinary},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := ClassifyFile(context.Background(), test.path, 64)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("ClassifyFile(%q) error = %v, want %v", test.path, err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("ClassifyFile(%q) = %q, %v; want %q", test.path, got, err, test.want)
			}
		})
	}

	defaultResult, err := ExtractText(context.Background(), textPath, TextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if defaultResult.Classification != ClassificationText || defaultResult.Content != "" {
		t.Fatalf("default extraction = %+v, want text without content", defaultResult)
	}
	preview, err := ExtractText(context.Background(), textPath, TextOptions{
		MaxBytes:       8,
		IncludeContent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Classification != ClassificationText || preview.Content != "line one" || !preview.Truncated {
		t.Fatalf("bounded extraction = %+v, want truncated line one", preview)
	}
	binaryResult, err := ExtractText(context.Background(), binaryPath, TextOptions{IncludeContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if binaryResult.Classification != ClassificationBinary || binaryResult.Content != "" {
		t.Fatalf("binary extraction = %+v, want no content", binaryResult)
	}
}

func TestExtractTextLimitAndCancellation(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "text.txt")
	if err := os.WriteFile(filePath, []byte(strings.Repeat("x", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractText(context.Background(), filePath, TextOptions{MaxBytes: -1}); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("negative extraction limit error = %v, want ErrLimitExceeded", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExtractText(ctx, filePath, TextOptions{IncludeContent: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled extraction error = %v, want context.Canceled", err)
	}
}

func TestConfinedTextReaderRequiresRelativePathAndRejectsLinks(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	textPath := filepath.Join(rootPath, "text.txt")
	if err := os.WriteFile(textPath, []byte("inside text"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(rootPath, "escape.txt")
	linkErr := os.Symlink(outsidePath, linkPath)
	if linkErr != nil && !errors.Is(linkErr, os.ErrPermission) {
		t.Logf("symbolic links unavailable; continuing without link case: %v", linkErr)
	}
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "nested.txt"), []byte("outside nested"), 0o600); err != nil {
		t.Fatal(err)
	}
	directoryLinkErr := os.Symlink(outsideDir, filepath.Join(rootPath, "linkdir"))

	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	metadata, err := ExtractTextInRoot(context.Background(), root, "text.txt", TextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Classification != ClassificationText || metadata.Content != "" {
		t.Fatalf("confined metadata = %+v, want text without content", metadata)
	}
	preview, err := ReadTextInRoot(context.Background(), root, "text.txt", 5)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Content != "insid" || !preview.Truncated {
		t.Fatalf("confined preview = %+v, want bounded content", preview)
	}
	if _, err := ExtractTextInRoot(context.Background(), root, textPath, TextOptions{}); !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("absolute confined path error = %v, want ErrPathTraversal", err)
	}
	if linkErr == nil {
		if _, err := ExtractTextInRoot(context.Background(), root, "escape.txt", TextOptions{}); !errors.Is(err, ErrSymlink) {
			t.Fatalf("link confined path error = %v, want ErrSymlink", err)
		}
	}
	if directoryLinkErr == nil {
		if _, err := ExtractTextInRoot(context.Background(), root, "linkdir/nested.txt", TextOptions{}); !errors.Is(err, ErrSymlink) {
			t.Fatalf("symlinked directory path error = %v, want ErrSymlink", err)
		}
	}
}

func FuzzClassifyBytes(f *testing.F) {
	f.Add([]byte("plain text\n"))
	f.Add([]byte{0, 1, 2})
	f.Add([]byte{0xff, 0xfe})
	f.Fuzz(func(t *testing.T, data []byte) {
		classification := classifyBytes(data)
		if classification != ClassificationText && classification != ClassificationBinary {
			t.Fatalf("classifyBytes returned unexpected classification %q", classification)
		}
	})
}
