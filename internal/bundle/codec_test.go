package bundle_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

func TestWriteBundleGoldenAndReadRoundTrip(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	want := validBundle()
	if err := bundle.WriteBundle(context.Background(), directory, want); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}

	goldenDirectory := filepath.Join("testdata", "golden")
	for _, name := range []string{
		bundle.ManifestFileName,
		bundle.ArtifactsFileName,
		bundle.ContributionsFileName,
		bundle.EvidenceFileName,
	} {
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		golden, err := os.ReadFile(filepath.Join(goldenDirectory, name))
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if !reflect.DeepEqual(got, golden) {
			t.Errorf("generated %s differs from golden", name)
		}
	}

	got, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle() error = %v", err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("read bundle validation error = %v", err)
	}
	if !reflect.DeepEqual(got.Artifacts, want.Artifacts) {
		t.Fatalf("read artifacts differ:\n got %#v\nwant %#v", got.Artifacts, want.Artifacts)
	}
	if !reflect.DeepEqual(got.Contributions, want.Contributions) {
		t.Fatalf("read contributions differ:\n got %#v\nwant %#v", got.Contributions, want.Contributions)
	}
	if !reflect.DeepEqual(got.Evidence, want.Evidence) {
		t.Fatalf("read evidence differs:\n got %#v\nwant %#v", got.Evidence, want.Evidence)
	}
	if got.Manifest.FactualDigest == "" || got.Manifest.Evidence.State != bundle.EvidenceStateAvailable {
		t.Fatalf("read manifest lost factual evidence metadata: %#v", got.Manifest)
	}
	if temporary, err := filepath.Glob(filepath.Join(directory, ".*.tmp-*")); err != nil {
		t.Fatalf("glob temporary files: %v", err)
	} else if len(temporary) != 0 {
		t.Fatalf("temporary files remain after successful write: %v", temporary)
	}
}

func TestWriteBundleRejectsUnpreparedEvidenceWithoutEchoingContent(t *testing.T) {
	t.Parallel()

	input := validBundle()
	const secret = "password=super-secret"
	unit := input.Evidence[0]
	unit.ContentState = evidence.ContentStatePresent
	unit.Content = secret
	unit.ContentHash = evidence.ContentDigest(secret)
	unit.ContentBytes = int64(len([]byte(secret)))
	unit.ContentCharacters = int64(len([]rune(secret)))
	unit.Classification = evidence.ClassificationUnknown
	unit.Findings = nil
	unit.ID = evidence.EvidenceID(unit)
	input.Evidence[0] = unit

	err := bundle.WriteBundle(context.Background(), t.TempDir(), input)
	if !errors.Is(err, evidence.ErrNotPrepared) {
		t.Fatalf("WriteBundle() error = %v, want ErrNotPrepared", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("writer error echoed source material")
	}
}

func TestWriteBundleRejectsClaimedSafeSensitiveEvidenceWithoutWritingIt(t *testing.T) {
	t.Parallel()

	input := validBundle()
	const secret = "password=super-secret"
	unit := input.Evidence[0]
	unit.Classification = evidence.ClassificationSafeText
	unit.Findings = nil
	unit.ContentState = evidence.ContentStatePresent
	unit.Content = secret
	unit.ContentHash = evidence.ContentDigest(secret)
	unit.ContentBytes = int64(len([]byte(secret)))
	unit.ContentCharacters = int64(len([]rune(secret)))
	unit.Persist = evidence.DecisionAllow
	unit.ExternalTransfer = evidence.DecisionAllow
	unit.ID = evidence.EvidenceID(unit)
	input.Evidence[0] = unit

	directory := t.TempDir()
	err := bundle.WriteBundle(context.Background(), directory, input)
	if !errors.Is(err, evidence.ErrNotPrepared) {
		t.Fatalf("WriteBundle() error = %v, want ErrNotPrepared", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("writer error echoed source material")
	}
	entries, readErr := os.ReadDir(directory)
	if readErr != nil {
		t.Fatalf("read output directory: %v", readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("writer left files after rejecting unsafe evidence: %v", entries)
	}
}

func TestReadBundleRejectsDigestMismatch(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	path := filepath.Join(directory, bundle.ManifestFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	marker := `"digest": "`
	digestStart := strings.Index(string(content), marker)
	if digestStart < 0 {
		t.Fatal("generated manifest has no file digest")
	}
	digestStart += len(marker)
	if content[digestStart] == '0' {
		content[digestStart] = '1'
	} else {
		content[digestStart] = '0'
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}

	_, err = bundle.ReadBundle(context.Background(), directory)
	if !errors.Is(err, bundle.ErrDigestMismatch) {
		t.Fatalf("ReadBundle() error = %v, want digest mismatch", err)
	}
}

func TestReadBundleRejectsTruncatedAndExtraJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "truncated json",
			mutate: func(content []byte) []byte {
				return content[:len(content)-3]
			},
		},
		{
			name: "extra json",
			mutate: func(content []byte) []byte {
				return append(content, []byte("{}\n")...)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
				t.Fatalf("WriteBundle() error = %v", err)
			}
			path := filepath.Join(directory, bundle.ArtifactsFileName)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read artifacts: %v", err)
			}
			if err := os.WriteFile(path, tt.mutate(content), 0o644); err != nil {
				t.Fatalf("rewrite artifacts: %v", err)
			}

			if _, err := bundle.ReadBundle(context.Background(), directory); err == nil {
				t.Fatalf("ReadBundle() succeeded for %s", tt.name)
			}
		})
	}
}

func TestReadBundleAppliesSafeDefaultManifestLimit(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	padding := strings.Repeat("x", int(bundle.DefaultMaxManifestBytes)+1)
	manifest := `{"contract_version":"v1alpha1","padding":"` + padding + `"}`
	if err := os.WriteFile(filepath.Join(directory, bundle.ManifestFileName), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write oversized manifest: %v", err)
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("ReadBundle() error = %v, want default manifest limit", err)
	}
}

func TestReadBundleRejectsBundleFieldsWithoutVersion(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	manifest := []byte(`{"contract_version":"v1alpha1","organization":{"id":"organization-injected"}}`)
	if err := os.WriteFile(filepath.Join(directory, bundle.ManifestFileName), manifest, 0o644); err != nil {
		t.Fatalf("write malformed bundle manifest: %v", err)
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); !errors.Is(err, bundle.ErrUnsupportedVersion) {
		t.Fatalf("ReadBundle() error = %v, want unsupported bundle version", err)
	}
}

func TestWriteBundleAvailableToLimitedRemovesEvidenceAfterCommit(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
		t.Fatalf("WriteBundle(available) error = %v", err)
	}
	limited := validBundle()
	limited.Evidence = nil
	limited.Manifest.Evidence = bundle.EvidenceMetadata{State: bundle.EvidenceStateLimited}
	if err := bundle.WriteBundle(context.Background(), directory, limited); err != nil {
		t.Fatalf("WriteBundle(limited) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, bundle.EvidenceFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("evidence file after limited commit: err=%v, want not exist", err)
	}
	got, err := bundle.ReadBundle(context.Background(), directory)
	if err != nil {
		t.Fatalf("ReadBundle(limited) error = %v", err)
	}
	if got.Manifest.Evidence.State != bundle.EvidenceStateLimited || len(got.Evidence) != 0 {
		t.Fatalf("limited bundle = state %q evidence %d", got.Manifest.Evidence.State, len(got.Evidence))
	}
}

func TestBundleCodecHonorsCancellationAndLeavesNoPartialFinal(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
		t.Fatalf("WriteBundle(previous) error = %v", err)
	}
	previous := make(map[string][]byte)
	for _, name := range []string{
		bundle.ManifestFileName,
		bundle.ArtifactsFileName,
		bundle.ContributionsFileName,
		bundle.EvidenceFileName,
	} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read previous %s: %v", name, err)
		}
		previous[name] = content
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bundle.WriteBundle(ctx, directory, validBundle()); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteBundle() error = %v, want context canceled", err)
	}
	for name, want := range previous {
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read preserved %s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("canceled write replaced %s", name)
		}
	}
	if temporary, err := filepath.Glob(filepath.Join(directory, ".*.tmp-*")); err != nil {
		t.Fatalf("glob temporary files: %v", err)
	} else if len(temporary) != 0 {
		t.Fatalf("temporary files remain after canceled write: %v", temporary)
	}

	if _, err := bundle.ReadBundle(ctx, directory); !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadBundle() error = %v, want context canceled", err)
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); err != nil {
		t.Fatalf("preserved bundle is unreadable after cancellation: %v", err)
	}

	invalid := validBundle()
	invalid.Manifest.Organization.ID = ""
	if err := bundle.WriteBundle(context.Background(), directory, invalid); err == nil {
		t.Fatal("WriteBundle(invalid) succeeded")
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); err != nil {
		t.Fatalf("preserved bundle is unreadable after failed write: %v", err)
	}
}

func TestBundleCodecRollsBackAfterDataPublication(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), directory, validBundle()); err != nil {
		t.Fatalf("WriteBundle(previous) error = %v", err)
	}
	previous := make(map[string][]byte)
	for _, name := range []string{
		bundle.ManifestFileName,
		bundle.ArtifactsFileName,
		bundle.ContributionsFileName,
		bundle.EvidenceFileName,
	} {
		content, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read previous %s: %v", name, err)
		}
		previous[name] = content
	}
	ctx := cancelWhenFileExists{
		Context: context.Background(),
		Path:    filepath.Join(directory, bundle.ArtifactsFileName),
	}
	if err := bundle.WriteBundle(&ctx, directory, validBundle()); !errors.Is(err, context.Canceled) {
		t.Fatalf("WriteBundle() error = %v, want context canceled", err)
	}
	for name, want := range previous {
		got, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("read restored %s: %v", name, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("rollback changed %s", name)
		}
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); err != nil {
		t.Fatalf("restored bundle is unreadable: %v", err)
	}
	if rollback, err := filepath.Glob(filepath.Join(directory, ".*.rollback-*")); err != nil {
		t.Fatalf("glob rollback files: %v", err)
	} else if len(rollback) != 0 {
		t.Fatalf("rollback files remain after cancellation: %v", rollback)
	}
}

func TestWriteBundleCleansTemporaryFilesOnFailure(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := validBundle()
	input.Manifest.Limits.MaxManifestBytes = 1
	if err := bundle.WriteBundle(context.Background(), directory, input); !errors.Is(err, bundle.ErrLimitExceeded) {
		t.Fatalf("WriteBundle() error = %v, want limit exceeded", err)
	}
	for _, pattern := range []string{".*.tmp-*", ".*.rollback-*"} {
		files, err := filepath.Glob(filepath.Join(directory, pattern))
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		if len(files) != 0 {
			t.Fatalf("temporary files remain after failed write (%s): %v", pattern, files)
		}
	}
}

type cancelWhenFileExists struct {
	context.Context
	Path        string
	MissingSeen bool
}

func (c *cancelWhenFileExists) Err() error {
	_, err := os.Stat(c.Path)
	if !c.MissingSeen {
		if errors.Is(err, os.ErrNotExist) {
			c.MissingSeen = true
		}
		return nil
	}
	if err == nil {
		return context.Canceled
	}
	return nil
}

func TestReadLegacyResultIsExplicitlyLimited(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	input := validBundle()
	legacy := contract.Result{
		Manifest:      input.Manifest.Manifest,
		Artifacts:     input.Artifacts,
		Contributions: input.Contributions,
	}
	if err := contract.WriteResult(context.Background(), directory, legacy); err != nil {
		t.Fatalf("WriteResult() error = %v", err)
	}
	if _, err := bundle.ReadBundle(context.Background(), directory); !errors.Is(err, bundle.ErrInvalid) {
		t.Fatalf("ReadBundle() without organization error = %v, want invalid", err)
	}

	got, err := bundle.ReadBundle(context.Background(), directory, bundle.Options{
		OrganizationID: input.Manifest.Organization.ID,
	})
	if err != nil {
		t.Fatalf("ReadBundle() legacy error = %v", err)
	}
	if got.Manifest.Evidence.State != bundle.EvidenceStateLimited {
		t.Fatalf("legacy evidence state = %q, want limited", got.Manifest.Evidence.State)
	}
	if got.Manifest.Organization.ID != input.Manifest.Organization.ID {
		t.Fatalf("legacy organization = %q, want %q", got.Manifest.Organization.ID, input.Manifest.Organization.ID)
	}
	if got.Manifest.Analysis.ConfigurationID != input.Manifest.Execution.ConfigurationID ||
		got.Manifest.Analysis.Revision != input.Manifest.Snapshot.Revision {
		t.Fatalf("legacy analysis metadata was not derived from result: %#v", got.Manifest.Analysis)
	}
	if len(got.Evidence) != 0 {
		t.Fatalf("legacy result unexpectedly has evidence: %#v", got.Evidence)
	}
}
