package bundle_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
)

const (
	frozenV1Alpha1Digest = "8f092747f4dde9b221d8bc33bac7198d75120157f81d3965102d910620f8f1c2"
	frozenV1Alpha2Digest = "e1cbe31ac1b741c634c7aa920cbfa42902099e6ef36edef04ce0c7c05efc2301"
)

func TestReadFrozenV1Alpha2Golden(t *testing.T) {
	t.Parallel()

	got := readFrozenBundle(t, filepath.Join("testdata", "v1alpha2-golden"))
	if got.Manifest.Version != bundle.VersionV1Alpha2 {
		t.Fatalf("golden bundle version = %q, want %q", got.Manifest.Version, bundle.VersionV1Alpha2)
	}
	if got.Manifest.ContractVersion != bundle.ContractVersion {
		t.Fatalf("golden contract version = %q, want %q", got.Manifest.ContractVersion, bundle.ContractVersion)
	}
	if got.Manifest.FactualDigest != frozenV1Alpha2Digest {
		t.Fatalf("golden factual digest = %q, want %q", got.Manifest.FactualDigest, frozenV1Alpha2Digest)
	}
	if got.Manifest.Counts.FrontendManifestCount != 2 ||
		got.Manifest.Counts.CanonicalFactCount != 2 ||
		got.Manifest.Counts.ExtensionCount != 2 {
		t.Fatalf("golden v1alpha2 counts = %#v", got.Manifest.Counts)
	}
	if len(got.FrontendManifests) != 2 || len(got.Facts) != 2 || len(got.Extensions) != 2 {
		t.Fatalf("golden v1alpha2 sequences were not read: frontends=%d facts=%d extensions=%d", len(got.FrontendManifests), len(got.Facts), len(got.Extensions))
	}
}

func TestWriteV1Alpha2GoldenPreservesCanonicalBytes(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validV2Bundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}

	fixture := filepath.Join("testdata", "v1alpha2-golden")
	for _, name := range []string{
		bundle.ManifestFileName,
		bundle.ArtifactsFileName,
		bundle.ContributionsFileName,
		bundle.EvidenceFileName,
		bundle.FrontendManifestsFileName,
		bundle.CanonicalFactsFileName,
		bundle.ExtensionsFileName,
	} {
		want, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			t.Fatalf("read frozen %s: %v", name, err)
		}
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("generated %s differs from frozen golden", name)
		}
	}
}

func TestReadFrozenV1Alpha1GoldenPreservesDigest(t *testing.T) {
	t.Parallel()

	got := readFrozenBundle(t, filepath.Join("testdata", "golden"))
	if got.Manifest.Version != bundle.VersionV1Alpha1 {
		t.Fatalf("golden bundle version = %q, want %q", got.Manifest.Version, bundle.VersionV1Alpha1)
	}
	if got.Manifest.FactualDigest != frozenV1Alpha1Digest {
		t.Fatalf("golden factual digest = %q, want %q", got.Manifest.FactualDigest, frozenV1Alpha1Digest)
	}
	if got.Manifest.Evidence.State != bundle.EvidenceStateAvailable || len(got.Evidence) != 1 {
		t.Fatalf("golden v1alpha1 evidence = state %q count %d", got.Manifest.Evidence.State, len(got.Evidence))
	}
}

func TestFrozenV1Alpha2RejectsManifestAndSequenceCorruption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   error
	}{
		{
			name: "factual digest",
			mutate: func(t *testing.T, directory string) {
				mutateManifest(t, directory, func(manifest map[string]any) {
					manifest["factual_digest"] = strings.Repeat("0", 64)
				})
			},
			want: bundle.ErrDigestMismatch,
		},
		{
			name: "sequence digest",
			mutate: func(t *testing.T, directory string) {
				mutateManifest(t, directory, func(manifest map[string]any) {
					for _, file := range manifest["files"].([]any) {
						descriptor := file.(map[string]any)
						if descriptor["name"] == bundle.CanonicalFactsFileName {
							descriptor["digest"] = strings.Repeat("0", 64)
						}
					}
				})
			},
			want: bundle.ErrDigestMismatch,
		},
		{
			name: "count",
			mutate: func(t *testing.T, directory string) {
				mutateManifest(t, directory, func(manifest map[string]any) {
					counts := manifest["counts"].(map[string]any)
					counts["canonical_fact_count"] = float64(1)
				})
			},
			want: bundle.ErrCountMismatch,
		},
		{
			name: "extra sequence line",
			mutate: func(t *testing.T, directory string) {
				path := filepath.Join(directory, bundle.CanonicalFactsFileName)
				content, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read facts: %v", err)
				}
				if err := os.WriteFile(path, append(content, content...), 0o644); err != nil {
					t.Fatalf("write facts: %v", err)
				}
			},
			want: bundle.ErrLimitExceeded,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := copyBundleFixture(t, filepath.Join("testdata", "v1alpha2-golden"))
			tt.mutate(t, directory)
			got, err := bundle.ReadBundle(context.Background(), directory)
			if !errors.Is(err, tt.want) {
				t.Fatalf("ReadBundle() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
			if got.Manifest.Version != "" || got.Artifacts != nil || got.Facts != nil {
				t.Fatalf("corrupt fixture returned a partial bundle: %#v", got)
			}
		})
	}
}

func TestFrozenV1Alpha2ExtensionAllowlist(t *testing.T) {
	t.Parallel()

	input := readFrozenBundle(t, filepath.Join("testdata", "v1alpha2-golden"))
	tests := []struct {
		name   string
		mutate func(*bundle.ExtensionRecord)
	}{
		{
			name: "unknown schema id",
			mutate: func(record *bundle.ExtensionRecord) {
				record.SchemaID = "unknown-schema"
			},
		},
		{
			name: "unknown schema version",
			mutate: func(record *bundle.ExtensionRecord) {
				record.SchemaVersion = "2"
			},
		},
		{
			name: "unknown schema digest",
			mutate: func(record *bundle.ExtensionRecord) {
				record.SchemaDigest = strings.Repeat("0", 64)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mutated := input
			mutated.Extensions = append([]json.RawMessage(nil), input.Extensions...)
			var record bundle.ExtensionRecord
			if err := json.Unmarshal(mutated.Extensions[0], &record); err != nil {
				t.Fatalf("decode extension: %v", err)
			}
			tt.mutate(&record)
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("encode extension: %v", err)
			}
			mutated.Extensions[0] = encoded
			if err := mutated.Validate(); !errors.Is(err, bundle.ErrInvalidExtension) {
				t.Fatalf("Bundle.Validate() error = %v, want invalid extension", err)
			}
		})
	}
}

func TestFrozenV1Alpha2RejectsLimitsBeforeMaterializing(t *testing.T) {
	t.Parallel()

	fixture := filepath.Join("testdata", "v1alpha2-golden")
	t.Run("count", func(t *testing.T) {
		_, err := bundle.ReadBundle(context.Background(), fixture, bundle.Options{
			Limits: bundle.Limits{MaxCanonicalFacts: 1},
		})
		if !errors.Is(err, bundle.ErrLimitExceeded) {
			t.Fatalf("ReadBundle(count limit) error = %v, want limit exceeded", err)
		}
	})
	t.Run("sequence budget and oversized line", func(t *testing.T) {
		input := readFrozenBundle(t, fixture)
		var total int64
		for _, file := range input.Manifest.Files {
			total += file.Bytes
		}
		directory := copyBundleFixture(t, fixture)
		const sentinel = "fixture-secret-sentinel"
		line := []byte(`{"payload":"` + sentinel + strings.Repeat("x", 64<<10) + `"}` + "\n")
		path := filepath.Join(directory, bundle.ExtensionsFileName)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read extensions: %v", err)
		}
		if err := os.WriteFile(path, append(content, line...), 0o644); err != nil {
			t.Fatalf("write oversized extension: %v", err)
		}
		got, err := bundle.ReadBundle(context.Background(), directory, bundle.Options{
			Limits: bundle.Limits{MaxBundleBytes: total},
		})
		if !errors.Is(err, bundle.ErrLimitExceeded) {
			t.Fatalf("ReadBundle(oversized line) error = %v, want limit exceeded", err)
		}
		if strings.Contains(err.Error(), sentinel) || len(err.Error()) > 1024 {
			t.Fatalf("oversized-line error leaked or exceeded bound: len=%d error=%v", len(err.Error()), err)
		}
		if got.Manifest.Version != "" || got.Extensions != nil {
			t.Fatalf("oversized-line read returned partial bundle: %#v", got)
		}
	})
}

func TestFrozenV1Alpha2MalformedPayloadErrorIsBounded(t *testing.T) {
	t.Parallel()

	input := readFrozenBundle(t, filepath.Join("testdata", "v1alpha2-golden"))
	const sentinel = "fixture-secret-sentinel"
	input.Extensions[0] = json.RawMessage(`{"schema":"` + sentinel + strings.Repeat("x", 64<<10))
	directory := filepath.Join(t.TempDir(), "not-created", "bundle")
	err := bundle.WriteBundle(context.Background(), directory, input)
	if !errors.Is(err, bundle.ErrInvalidExtension) {
		t.Fatalf("WriteBundle(malformed payload) error = %v, want invalid extension", err)
	}
	if strings.Contains(err.Error(), sentinel) || len(err.Error()) > 1024 {
		t.Fatalf("malformed-payload error leaked or exceeded bound: len=%d error=%v", len(err.Error()), err)
	}
	if _, statErr := os.Stat(directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("malformed payload created destination: %v", statErr)
	}
}

func readFrozenBundle(t *testing.T, directory string) bundle.Bundle {
	t.Helper()
	got, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle(%s) error = %v", directory, err)
	}
	return got
}

func copyBundleFixture(t *testing.T, source string) string {
	t.Helper()
	destination := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatalf("create fixture copy: %v", err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read fixture directory: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			t.Fatalf("unexpected nested fixture directory %q", entry.Name())
		}
		content, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatalf("read fixture %s: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), content, 0o644); err != nil {
			t.Fatalf("copy fixture %s: %v", entry.Name(), err)
		}
	}
	return destination
}

func mutateManifest(t *testing.T, directory string, mutate func(map[string]any)) {
	t.Helper()
	path := filepath.Join(directory, bundle.ManifestFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(content, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	mutate(manifest)
	content, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}
