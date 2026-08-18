package benchmark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/pedrogpaulino/manu/internal/source"
)

// BenchmarkDiscoveryHash exercises the engine's real read-only discovery
// hot path: bounded worker hashing and classification. It is kept separate
// from report bookkeeping so synthetic measurement overhead cannot be used as
// a runtime-selection decision.
func BenchmarkDiscoveryHash(b *testing.B) {
	root := b.TempDir()
	for index := range 32 {
		pathName := filepath.Join(root, fmt.Sprintf("source-%02d.txt", index))
		if err := os.WriteFile(pathName, []byte("discovery fixture\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	limits := source.Limits{
		MaxFiles:                  64,
		MaxBytes:                  1 << 20,
		MaxFileBytes:              1 << 16,
		MaxConcurrency:            4,
		MaxProbeBytes:             8 << 10,
		MaxExtractionBytes:        1 << 20,
		MaxArchiveMembers:         64,
		MaxArchiveBytes:           1 << 20,
		MaxArchiveMemberBytes:     1 << 20,
		MaxArchiveCompressedBytes: 1 << 20,
		MaxExpansionRatio:         100,
	}
	b.ReportAllocs()
	for b.Loop() {
		result, err := source.Discover(context.Background(), source.Config{Root: root, Limits: limits})
		if err != nil {
			b.Fatal(err)
		}
		if len(result.Artifacts) != 32 {
			b.Fatalf("discovered artifacts = %d, want 32", len(result.Artifacts))
		}
	}
}

// BenchmarkMetadataDigest measures the metadata walk used for the benchmark's
// read-only pre/post integrity check. It is an observable hot path of the
// report, not a synthetic basis for choosing a runtime or a commercial SLA.
func BenchmarkMetadataDigest(b *testing.B) {
	root := b.TempDir()
	for index := range 32 {
		pathName := filepath.Join(root, fmt.Sprintf("file-%02d.txt", index))
		if err := os.WriteFile(pathName, []byte("benchmark metadata fixture\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := metadataDigest(root); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDirectoryBytes measures the output-volume walk used after each
// scenario. Keeping it separate makes its cost visible without conflating it
// with source analysis.
func BenchmarkDirectoryBytes(b *testing.B) {
	root := b.TempDir()
	for index := range 16 {
		pathName := filepath.Join(root, fmt.Sprintf("output-%02d.ndjson", index))
		if err := os.WriteFile(pathName, []byte("output fixture\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := directoryBytes(root); err != nil {
			b.Fatal(err)
		}
	}
}
