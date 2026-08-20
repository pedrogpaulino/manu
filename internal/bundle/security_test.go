package bundle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
)

func TestReadBundleRejectsSymlinkedManifestAndSequence(t *testing.T) {
	tests := []struct {
		name string
		file string
	}{
		{name: "manifest", file: bundle.ManifestFileName},
		{name: "artifacts", file: bundle.ArtifactsFileName},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
				t.Fatalf("WriteBundle() error = %v", err)
			}
			original, err := os.ReadFile(filepath.Join(directory, test.file))
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", test.file, err)
			}
			external := filepath.Join(t.TempDir(), "outside-bundle")
			if err := os.WriteFile(external, original, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			path := filepath.Join(directory, test.file)
			if err := os.Remove(path); err != nil {
				t.Fatalf("Remove(%q) error = %v", test.file, err)
			}
			if err := os.Symlink(external, path); err != nil {
				t.Skipf("symlink is unavailable: %v", err)
			}

			_, err = bundle.ReadBundle(context.Background(), directory)
			if !errors.Is(err, bundle.ErrInvalidFile) {
				t.Fatalf("ReadBundle() error = %v, want ErrInvalidFile", err)
			}
		})
	}
}

func TestReadBundleEnforcesConfiguredSequenceBudgetBeforeMaterializing(t *testing.T) {
	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}

	_, err := bundle.ReadBundle(context.Background(), directory, bundle.Options{
		Limits: bundle.Limits{MaxBundleBytes: 1, MaxManifestBytes: 1 << 20},
	})
	if !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("ReadBundle() error = %v, want ErrLimitExceeded", err)
	}
}
