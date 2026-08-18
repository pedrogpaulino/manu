package contract

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManifestGolden(t *testing.T) {
	directory := t.TempDir()
	if err := WriteResult(context.Background(), directory, fixtureResult()); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(directory, ManifestFileName))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "..", "testdata", "contract", "manifest.golden.json"))
	if err != nil {
		t.Fatalf("golden fixture is missing; generated manifest follows:\n%s\nread golden: %v", got, err)
	}
	if string(got) != string(want) {
		t.Fatalf("manifest differs from golden\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
