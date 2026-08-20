package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

func TestServeIngestionStagesBeforeJobAndLoadsAfterStagerRestart(t *testing.T) {
	sourceDirectory := t.TempDir()
	input := serveIngestionTestBundle()
	if err := bundle.WriteBundle(context.Background(), sourceDirectory, input); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
	sender, err := bundle.NewMultipartSender(sourceDirectory, bundle.MultipartWriteOptions{Boundary: "serve-ingestion-test"})
	if err != nil {
		t.Fatalf("NewMultipartSender() error = %v", err)
	}
	var body bytes.Buffer
	if _, err := sender.Send(context.Background(), &body); err != nil {
		t.Fatalf("Send() error = %v", err)
	}

	stagingRoot := t.TempDir()
	limits := configLimitsForServeTest()
	stager, err := newServeBundleStager(stagingRoot, "local", limits)
	if err != nil {
		t.Fatalf("newServeBundleStager() error = %v", err)
	}
	jobs := ingestion.NewMemoryStore()
	service := newServeIngestionService(jobs, stager, "local")
	job, err := service.CreateMultipart(context.Background(), "local", bytes.NewReader(body.Bytes()), sender.ContentType(), bundle.MultipartReadOptions{OrganizationID: "local"})
	if err != nil {
		t.Fatalf("CreateMultipart() error = %v", err)
	}
	if job.State != ingestion.JobStatePending {
		t.Fatalf("job state = %q, want pending", job.State)
	}
	if _, err := os.Stat(sourceDirectory); err != nil {
		t.Fatalf("source directory unexpectedly unavailable before restart: %v", err)
	}
	if err := os.RemoveAll(sourceDirectory); err != nil {
		t.Fatalf("remove Agent source directory: %v", err)
	}

	restarted, err := newServeBundleStager(stagingRoot, "local", limits)
	if err != nil {
		t.Fatalf("new restarted stager: %v", err)
	}
	loaded, err := restarted.Load(context.Background(), job)
	if err != nil {
		t.Fatalf("Load() after restart error = %v", err)
	}
	if loaded.Manifest.FactualDigest != job.FactualDigest {
		t.Fatalf("loaded digest = %q, want %q", loaded.Manifest.FactualDigest, job.FactualDigest)
	}
	if len(loaded.Artifacts) != 1 || len(loaded.Contributions) != 1 {
		t.Fatalf("loaded bundle counts = artifacts %d contributions %d", len(loaded.Artifacts), len(loaded.Contributions))
	}
}

func TestServeBundleStagerRepairsIncompletePublication(t *testing.T) {
	root := t.TempDir()
	stager, err := newServeBundleStager(root, "local", configLimitsForServeTest())
	if err != nil {
		t.Fatal(err)
	}
	input := serveIngestionTestBundle()
	source := t.TempDir()
	if err := bundle.WriteBundle(context.Background(), source, input); err != nil {
		t.Fatal(err)
	}
	sender, err := bundle.NewMultipartSender(source, bundle.MultipartWriteOptions{Boundary: "serve-repair-test"})
	if err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	if _, err := sender.Send(context.Background(), &body); err != nil {
		t.Fatal(err)
	}
	staged, err := stager.stage(context.Background(), bytes.NewReader(body.Bytes()), sender.ContentType(), bundle.MultipartReadOptions{OrganizationID: "local"})
	if err != nil {
		t.Fatal(err)
	}
	finalPath := filepath.Join(root, serveBundleKey("local", staged.Manifest))
	if err := os.Mkdir(finalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := stager.publish(staged); err != nil {
		t.Fatalf("publish() should replace incomplete directory: %v", err)
	}
	if ready, err := serveReadyDirectory(finalPath); err != nil || !ready {
		t.Fatalf("published directory ready = %t, error = %v", ready, err)
	}
}

func TestServeIngestionServiceDoesNotCreateUnstagedJobs(t *testing.T) {
	service := newServeIngestionService(ingestion.NewMemoryStore(), nil, "local")
	if _, err := service.Create(context.Background(), "local", bundle.Bundle{}); !errors.Is(err, ingestion.ErrInvalidJob) {
		t.Fatalf("Create() error = %v, want ErrInvalidJob", err)
	}
}

func TestServeBundleStagerLoadsPersistedFixtureWhenConfigured(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("MANU_TEST_STAGING_DIR"))
	if root == "" {
		t.Skip("MANU_TEST_STAGING_DIR is not set")
	}
	organization := "organization-1"
	limits := config.Default().Limits
	stager, err := newServeBundleStager(root, organization, limits)
	if err != nil {
		t.Fatalf("newServeBundleStager() error = %v", err)
	}
	job, err := ingestion.NewJob(ingestion.NewJobInput{
		ID:                      "aea13147-e8ee-44bb-a1b9-3cee7986939c",
		OrganizationID:          identity.CanonicalUUID("organization", organization),
		OrganizationExternalID:  organization,
		SourceExternalID:        "source-1",
		SnapshotExternalID:      "snapshot-7bc99e16ac6905a0deb1ce0a44a3c03d76797036adb37e5ea2f221b9c65044ac",
		FactualDigest:           "8f092747f4dde9b221d8bc33bac7198d75120157f81d3965102d910620f8f1c2",
		AnalysisConfigurationID: "configuration-1",
	})
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	if _, err := stager.Load(context.Background(), job); err != nil {
		t.Fatalf("Load() persisted fixture error = %v", err)
	}
}

func TestWriteServeBundleFixtureWhenConfigured(t *testing.T) {
	directory := strings.TrimSpace(os.Getenv("MANU_TEST_BUNDLE_OUTPUT"))
	if directory == "" {
		t.Skip("MANU_TEST_BUNDLE_OUTPUT is not set")
	}
	if err := bundle.WriteBundle(context.Background(), directory, serveIngestionTestBundle()); err != nil {
		t.Fatalf("WriteBundle() error = %v", err)
	}
}

func configLimitsForServeTest() config.LimitsConfig {
	return config.LimitsConfig{
		MaxBundleBytes: 1 << 20, MaxManifestBytes: 1 << 16,
		MaxEvidenceBytes: 1 << 16, MaxEvidenceUnits: 100,
		MaxEvidenceTextBytes: 1 << 14, MaxConcurrentIngestions: 1,
	}
}

func serveIngestionTestBundle() bundle.Bundle {
	const sourceID = "serve-source"
	const revision = "serve-revision"
	const configurationID = "serve-configuration"
	hash := strings.Repeat("b", 64)
	source := contract.Source{ID: sourceID, Name: "serve fixture", Type: "filesystem", Revision: revision}
	snapshot := contract.Snapshot{ID: contract.SnapshotID(sourceID, revision, hash), SourceID: sourceID, Revision: revision, Hash: hash}
	artifact := contract.Artifact{SourceID: sourceID, Path: "src/main.go", Type: "go", Hash: strings.Repeat("a", 64), Size: 4}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	contribution := contract.Contribution{ArtifactID: artifact.ID, AnalyzerID: "generic", AnalyzerVersion: "1", Method: "symbols", Type: "symbol", Locator: contract.Locator{SourceID: sourceID, ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1, EndLine: 1}}
	contribution.ID = contract.ContributionID(contribution.ArtifactID, contribution.AnalyzerID, contribution.AnalyzerVersion, contribution.Method)
	legacy := contract.Manifest{ContractVersion: contract.Version, ResultID: "serve-result", Source: source, Snapshot: snapshot, Execution: contract.ExecutionMetadata{RunID: "serve-run", ConfigurationID: configurationID}, ArtifactCount: 1, ContributionCount: 1, Coverage: []contract.Coverage{}, Gaps: []contract.Gap{}, Failures: []contract.Failure{}}
	result := contract.Result{Manifest: legacy, Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}}
	digest, err := bundle.FactualDigest(result, nil)
	if err != nil {
		panic(err)
	}
	return bundle.Bundle{Manifest: bundle.Manifest{Version: bundle.Version, Organization: bundle.Organization{ID: "local", Name: "Local"}, Manifest: legacy, Analysis: bundle.Analysis{ID: "serve-analysis", ConfigurationID: configurationID, Revision: "serve-analysis-revision"}, FactualDigest: digest, Counts: bundle.Counts{ArtifactCount: 1, ContributionCount: 1, EvidenceUnitCount: 0}, Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateLimited}}, Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}}
}
