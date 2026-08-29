package cli

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/evaluation"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/query"
)

func TestEvaluationSeedPlanBuildsDeterministicJobsLoaderAndResolver(t *testing.T) {
	t.Parallel()
	corpus := loadEvaluationSeedCorpus(t)

	first, err := newEvaluationSeedPlan(corpus)
	if err != nil {
		t.Fatalf("newEvaluationSeedPlan() error = %v", err)
	}
	second, err := newEvaluationSeedPlan(corpus)
	if err != nil {
		t.Fatalf("repeated newEvaluationSeedPlan() error = %v", err)
	}
	if first.organizationExternal != "evaluation-fixture" {
		t.Fatalf("organization external = %q, want evaluation-fixture", first.organizationExternal)
	}
	if first.organizationID != identity.CanonicalUUID("organization", "evaluation-fixture") {
		t.Fatalf("organization canonical id = %q", first.organizationID)
	}
	if !reflect.DeepEqual(first.jobs, second.jobs) {
		t.Fatalf("job construction is not deterministic:\nfirst=%#v\nsecond=%#v", first.jobs, second.jobs)
	}
	if len(first.jobs) != len(corpus.Snapshots) || len(first.loader.byJobID) != len(first.jobs) {
		t.Fatalf("plan cardinality jobs=%d loader=%d snapshots=%d", len(first.jobs), len(first.loader.byJobID), len(corpus.Snapshots))
	}

	for index, snapshot := range corpus.Snapshots {
		job := first.jobs[index]
		manifest := snapshot.Bundle.Manifest
		wantJobID := identity.CanonicalUUID(
			evaluationSeedJobKind,
			manifest.Organization.ID,
			manifest.Source.ID,
			manifest.Snapshot.ID,
			manifest.FactualDigest,
		)
		if job.ID != wantJobID {
			t.Fatalf("job %d id = %q, want %q", index, job.ID, wantJobID)
		}
		loaded, loadErr := first.loader.Load(context.Background(), job)
		if loadErr != nil {
			t.Fatalf("loader.Load(%q) error = %v", job.ID, loadErr)
		}
		if loaded.Manifest.FactualDigest != manifest.FactualDigest {
			t.Fatalf("loaded bundle digest = %q, want %q", loaded.Manifest.FactualDigest, manifest.FactualDigest)
		}
		if !loaded.Manifest.Snapshot.CapturedAt.Equal(time.Unix(0, 0).UTC()) {
			t.Fatalf("loaded capture time = %v, want epoch", loaded.Manifest.Snapshot.CapturedAt)
		}

		gotScope, resolveErr := first.resolver.Resolve(context.Background(), manifest.Organization.ID, manifest.Source.ID, manifest.Source.Revision)
		if resolveErr != nil {
			t.Fatalf("resolver.Resolve() error = %v", resolveErr)
		}
		wantScope := query.Scope{
			OrganizationID: identity.CanonicalUUID("organization", manifest.Organization.ID),
			SourceID:       identity.CanonicalUUID("source", manifest.Organization.ID, manifest.Source.ID),
			SnapshotID:     identity.CanonicalUUID("snapshot", manifest.Organization.ID, manifest.Source.ID, manifest.Snapshot.ID),
		}
		if gotScope != wantScope {
			t.Fatalf("resolved scope = %#v, want %#v", gotScope, wantScope)
		}
	}
}

func TestEvaluationSeedLoaderAndResolverFailClosedWithoutPayload(t *testing.T) {
	loader, err := newEvaluationSeedBundleLoader(map[string]bundle.Bundle{
		"job": {Manifest: bundle.Manifest{Version: bundle.VersionV1Alpha2}},
	})
	if err != nil {
		t.Fatalf("newEvaluationSeedBundleLoader() error = %v", err)
	}
	_, err = loader.Load(context.Background(), testEvaluationSeedJob("missing"))
	if !errors.Is(err, ErrEvaluationFactualSeed) {
		t.Fatalf("missing loader job error = %v, want ErrEvaluationFactualSeed", err)
	}
	if strings.Contains(err.Error(), "missing") {
		t.Fatalf("loader error leaked job identity: %v", err)
	}

	resolver, err := newEvaluationSeedScopeResolver(map[string]query.Scope{
		evaluationSeedScopeKey("local", "source", "revision"): {
			OrganizationID: identity.CanonicalUUID("organization", "local"),
			SourceID:       identity.CanonicalUUID("source", "local", "source"),
			SnapshotID:     identity.CanonicalUUID("snapshot", "local", "source", "snapshot"),
		},
	})
	if err != nil {
		t.Fatalf("newEvaluationSeedScopeResolver() error = %v", err)
	}
	_, err = resolver.Resolve(context.Background(), "local", "other-source", "revision")
	if !errors.Is(err, ErrEvaluationFactualScope) {
		t.Fatalf("missing resolver scope error = %v, want ErrEvaluationFactualScope", err)
	}
	if strings.Contains(err.Error(), "other-source") {
		t.Fatalf("resolver error leaked source identity: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = resolver.Resolve(cancelled, "local", "source", "revision")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolver error = %v, want context.Canceled", err)
	}
}

func TestEvaluationSeedPlanRejectsDuplicateAndMixedScopes(t *testing.T) {
	t.Parallel()
	corpus := loadEvaluationSeedCorpus(t)

	duplicate := corpus
	duplicate.Snapshots = append(append([]evaluation.FactualCorpusSnapshot(nil), corpus.Snapshots...), corpus.Snapshots[0])
	if _, err := newEvaluationSeedPlan(duplicate); !errors.Is(err, ErrEvaluationFactualSeed) {
		t.Fatalf("duplicate snapshot error = %v, want ErrEvaluationFactualSeed", err)
	}

	mixed := corpus
	mixed.Snapshots = append([]evaluation.FactualCorpusSnapshot(nil), corpus.Snapshots...)
	mixed.Snapshots[0].Bundle.Manifest.Organization.ID = "other"
	if _, err := newEvaluationSeedPlan(mixed); !errors.Is(err, ErrEvaluationFactualSeed) {
		t.Fatalf("mixed scope error = %v, want ErrEvaluationFactualSeed", err)
	}
}

func loadEvaluationSeedCorpus(t *testing.T) evaluation.FactualCorpus {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repository root: %v", err)
	}
	cases, err := evaluation.LoadCases(filepath.Join(root, "testdata", "evaluation", "context-efficiency.v1alpha2.json"))
	if err != nil {
		t.Fatalf("load evaluation cases: %v", err)
	}
	corpus, err := evaluation.BuildFactualCorpus(context.Background(), root, cases)
	if err != nil {
		t.Fatalf("build factual corpus: %v", err)
	}
	return corpus
}

func testEvaluationSeedJob(id string) (job ingestion.Job) {
	return ingestion.Job{ID: id}
}
