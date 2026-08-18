package source

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeRoot(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "file.txt")
	if err := os.WriteFile(filePath, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		path      string
		wantError error
	}{
		{name: "directory", path: root},
		{name: "file", path: filePath, wantError: ErrInvalidRoot},
		{name: "missing", path: filepath.Join(root, "missing"), wantError: ErrInvalidRoot},
		{name: "empty", path: "", wantError: ErrInvalidRoot},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeRoot(test.path)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("NormalizeRoot(%q) error = %v, want %v", test.path, err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want, err := filepath.Abs(root)
			if err != nil {
				t.Fatal(err)
			}
			if got != filepath.Clean(want) {
				t.Fatalf("NormalizeRoot(%q) = %q, want %q", test.path, got, want)
			}
		})
	}
}

func TestNormalizeRelativePathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name      string
		candidate string
		want      string
		wantError error
	}{
		{name: "relative file", candidate: "src/main.go", want: "src/main.go"},
		{name: "root", candidate: ".", want: "."},
		{name: "parent", candidate: "../outside", wantError: ErrPathTraversal},
		{name: "embedded parent", candidate: "src/../outside", wantError: ErrPathTraversal},
		{name: "absolute outside", candidate: filepath.Join(filepath.Dir(root), "outside"), wantError: ErrPathTraversal},
		{name: "drive qualified", candidate: `C:\outside`, wantError: ErrPathTraversal},
		{name: "nul", candidate: "a\x00b", wantError: ErrPathTraversal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NormalizeRelativePath(root, test.candidate)
			if test.wantError != nil {
				if !errors.Is(err, test.wantError) {
					t.Fatalf("NormalizeRelativePath(%q) error = %v, want %v", test.candidate, err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("NormalizeRelativePath(%q) = %q, want %q", test.candidate, got, test.want)
			}
		})
	}
}

func TestSensitivePathDefaultsAndDefensiveCopy(t *testing.T) {
	patterns := DefaultSensitivePatterns()
	if len(patterns) == 0 {
		t.Fatal("DefaultSensitivePatterns returned no patterns")
	}
	patterns[0] = "not-sensitive"
	copyAgain := DefaultSensitivePatterns()
	if copyAgain[0] == "not-sensitive" {
		t.Fatal("DefaultSensitivePatterns returned mutable package state")
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "dotenv", path: ".env", want: true},
		{name: "nested dotenv", path: "config/.env.production", want: true},
		{name: "private key", path: "keys/id_ed25519", want: true},
		{name: "secret name", path: "config/client-secret.yaml", want: true},
		{name: "ordinary source", path: "src/main.go", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, _ := IsSensitivePath(test.path, nil)
			if got != test.want {
				t.Fatalf("IsSensitivePath(%q) = %v, want %v", test.path, got, test.want)
			}
		})
	}
}

func TestMatchSlashGlob(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		want    bool
	}{
		{name: "single component", pattern: "*.go", path: "main.go", want: true},
		{name: "nested recursive", pattern: "**/*.go", path: "src/internal/main.go", want: true},
		{name: "recursive zero components", pattern: "src/**", path: "src/main.go", want: true},
		{name: "recursive nested", pattern: "src/**", path: "src/internal/main.go", want: true},
		{name: "different extension", pattern: "**/*.go", path: "src/main.txt", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := matchSlashGlob(test.pattern, test.path); got != test.want {
				t.Fatalf("matchSlashGlob(%q, %q) = %v, want %v", test.pattern, test.path, got, test.want)
			}
		})
	}
}

func TestIsPathWithin(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "source")
	tests := []struct {
		name      string
		candidate string
		want      bool
	}{
		{name: "root", candidate: root, want: true},
		{name: "child", candidate: filepath.Join(root, "file.txt"), want: true},
		{name: "prefix sibling", candidate: root + "-other/file.txt", want: false},
		{name: "parent", candidate: filepath.Join(root, "..", "file.txt"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsPathWithin(root, test.candidate); got != test.want {
				t.Fatalf("IsPathWithin(%q, %q) = %v, want %v", root, test.candidate, got, test.want)
			}
		})
	}
}

func FuzzNormalizeRelativePath(f *testing.F) {
	f.Add("src/main.go")
	f.Add("../outside")
	f.Add("src/../../outside")
	f.Add("C:\\outside")
	f.Fuzz(func(t *testing.T, candidate string) {
		root := t.TempDir()
		got, err := NormalizeRelativePath(root, candidate)
		if err == nil && got != "." && isParentSlashPath(got) {
			t.Fatalf("accepted traversal path %q from candidate %q", got, candidate)
		}
	})
}
