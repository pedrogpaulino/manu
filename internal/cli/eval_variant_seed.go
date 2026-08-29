package cli

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/evaluation"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/persistence"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

var (
	// ErrEvaluationFactualSeed is the content-free boundary for errors while
	// preparing or ingesting factual evaluation snapshots. Database and bundle
	// details deliberately do not cross this CLI boundary.
	ErrEvaluationFactualSeed = errors.New("cli: factual evaluation seed failed")
	// ErrEvaluationFactualScope identifies an external source/revision that is
	// not present in the prepared evaluation corpus.
	ErrEvaluationFactualScope = errors.New("cli: factual evaluation scope unavailable")
)

const (
	evaluationSeedJobKind   = "evaluation-seed-job"
	evaluationSeedOwner     = "evaluation-seed"
	evaluationSeedLease     = 24 * time.Hour
	evaluationSeedHeartbeat = 12 * time.Hour
	evaluationSeedPoll      = time.Millisecond
)

// evaluationSeedBundleLoader keeps the complete, already validated bundles
// in memory for the duration of one local seed. The ingestion pipeline still
// owns validation and persistence; this adapter only resolves a deterministic
// job identity to its immutable bundle.
type evaluationSeedBundleLoader struct {
	byJobID map[string]bundle.Bundle
}

func newEvaluationSeedBundleLoader(values map[string]bundle.Bundle) (*evaluationSeedBundleLoader, error) {
	if len(values) == 0 {
		return nil, ErrEvaluationFactualSeed
	}
	loader := &evaluationSeedBundleLoader{byJobID: make(map[string]bundle.Bundle, len(values))}
	for jobID, input := range values {
		if strings.TrimSpace(jobID) == "" || input.Manifest.Version != bundle.VersionV1Alpha2 {
			return nil, ErrEvaluationFactualSeed
		}
		if _, exists := loader.byJobID[jobID]; exists {
			return nil, ErrEvaluationFactualSeed
		}
		loader.byJobID[jobID] = deterministicEvaluationSeedBundle(input)
	}
	return loader, nil
}

func (l *evaluationSeedBundleLoader) Load(ctx context.Context, job ingestion.Job) (bundle.Bundle, error) {
	if ctx == nil {
		return bundle.Bundle{}, ErrEvaluationFactualSeed
	}
	if err := ctx.Err(); err != nil {
		return bundle.Bundle{}, err
	}
	if l == nil || l.byJobID == nil {
		return bundle.Bundle{}, ErrEvaluationFactualSeed
	}
	input, ok := l.byJobID[job.ID]
	if !ok {
		return bundle.Bundle{}, ErrEvaluationFactualSeed
	}
	return input, nil
}

// evaluationSeedScopeResolver is intentionally a closed map. It accepts only
// the exact organization/source/revision tuple prepared before any database
// write and returns canonical query identities.
type evaluationSeedScopeResolver struct {
	byKey map[string]query.Scope
}

func newEvaluationSeedScopeResolver(values map[string]query.Scope) (*evaluationSeedScopeResolver, error) {
	if len(values) == 0 {
		return nil, ErrEvaluationFactualSeed
	}
	resolver := &evaluationSeedScopeResolver{byKey: make(map[string]query.Scope, len(values))}
	for key, scope := range values {
		if strings.TrimSpace(key) == "" {
			return nil, ErrEvaluationFactualSeed
		}
		if err := scope.Validate(); err != nil {
			return nil, ErrEvaluationFactualSeed
		}
		if _, exists := resolver.byKey[key]; exists {
			return nil, ErrEvaluationFactualSeed
		}
		resolver.byKey[key] = scope
	}
	return resolver, nil
}

var _ evaluation.EvaluationScopeResolver = (*evaluationSeedScopeResolver)(nil)

func (r *evaluationSeedScopeResolver) Resolve(ctx context.Context, organizationExternal, sourceID, sourceRevision string) (query.Scope, error) {
	if ctx == nil {
		return query.Scope{}, ErrEvaluationFactualScope
	}
	if err := ctx.Err(); err != nil {
		return query.Scope{}, err
	}
	if r == nil || r.byKey == nil {
		return query.Scope{}, ErrEvaluationFactualScope
	}
	scope, ok := r.byKey[evaluationSeedScopeKey(organizationExternal, sourceID, sourceRevision)]
	if !ok {
		return query.Scope{}, ErrEvaluationFactualScope
	}
	return scope, nil
}

type evaluationSeedPlan struct {
	organizationExternal string
	organizationID       string
	loader               *evaluationSeedBundleLoader
	jobs                 []ingestion.Job
	resolver             *evaluationSeedScopeResolver
}

// newEvaluationSeedPlan performs every corpus, scope and identity check that
// can be done without PostgreSQL. Keeping this phase separate makes the
// database operation all-or-nothing from the caller's perspective: malformed
// or ambiguous evaluation input is rejected before a job is created.
func newEvaluationSeedPlan(corpus evaluation.FactualCorpus) (evaluationSeedPlan, error) {
	if err := corpus.Validate(); err != nil {
		return evaluationSeedPlan{}, ErrEvaluationFactualSeed
	}
	if len(corpus.Snapshots) == 0 {
		return evaluationSeedPlan{}, ErrEvaluationFactualSeed
	}

	bundles := make(map[string]bundle.Bundle, len(corpus.Snapshots))
	scopes := make(map[string]query.Scope, len(corpus.Snapshots))
	jobs := make([]ingestion.Job, 0, len(corpus.Snapshots))
	organizationExternal := ""
	organizationID := ""
	for _, snapshot := range corpus.Snapshots {
		input := deterministicEvaluationSeedBundle(snapshot.Bundle)
		manifest := input.Manifest
		if manifest.Version != bundle.VersionV1Alpha2 ||
			snapshot.CorpusID == "" || snapshot.CorpusRevision == "" ||
			snapshot.SourceID != manifest.Source.ID ||
			snapshot.SourceRevision != manifest.Source.Revision ||
			snapshot.FactualDigest != manifest.FactualDigest ||
			snapshot.Scope.OrganizationID != manifest.Organization.ID ||
			snapshot.Scope.SourceID != manifest.Source.ID ||
			snapshot.Scope.SnapshotID != manifest.Snapshot.ID {
			return evaluationSeedPlan{}, ErrEvaluationFactualSeed
		}

		if organizationExternal == "" {
			organizationExternal = manifest.Organization.ID
			organizationID = identity.CanonicalUUID("organization", organizationExternal)
		} else if organizationExternal != manifest.Organization.ID {
			return evaluationSeedPlan{}, ErrEvaluationFactualSeed
		}

		canonicalSourceID := identity.CanonicalUUID("source", manifest.Organization.ID, manifest.Source.ID)
		canonicalSnapshotID := identity.CanonicalUUID("snapshot", manifest.Organization.ID, manifest.Source.ID, manifest.Snapshot.ID)
		scope := query.Scope{
			OrganizationID: organizationID,
			SourceID:       canonicalSourceID,
			SnapshotID:     canonicalSnapshotID,
		}
		if err := scope.Validate(); err != nil {
			return evaluationSeedPlan{}, ErrEvaluationFactualSeed
		}
		scopeKey := evaluationSeedScopeKey(manifest.Organization.ID, manifest.Source.ID, manifest.Source.Revision)
		if _, exists := scopes[scopeKey]; exists {
			return evaluationSeedPlan{}, ErrEvaluationFactualSeed
		}
		scopes[scopeKey] = scope

		jobID := identity.CanonicalUUID(
			evaluationSeedJobKind,
			manifest.Organization.ID,
			manifest.Source.ID,
			manifest.Snapshot.ID,
			manifest.FactualDigest,
		)
		job, err := ingestion.NewJob(ingestion.NewJobInput{
			ID:                      jobID,
			OrganizationID:          organizationID,
			OrganizationExternalID:  manifest.Organization.ID,
			OrganizationName:        manifest.Organization.Name,
			SourceID:                canonicalSourceID,
			SnapshotID:              canonicalSnapshotID,
			SourceExternalID:        manifest.Source.ID,
			SnapshotExternalID:      manifest.Snapshot.ID,
			FactualDigest:           manifest.FactualDigest,
			AnalysisConfigurationID: manifest.Analysis.ConfigurationID,
			CreatedAt:               time.Unix(0, 0).UTC(),
		})
		if err != nil {
			return evaluationSeedPlan{}, ErrEvaluationFactualSeed
		}
		if _, exists := bundles[job.ID]; exists {
			return evaluationSeedPlan{}, ErrEvaluationFactualSeed
		}
		bundles[job.ID] = input
		jobs = append(jobs, job)
	}

	loader, err := newEvaluationSeedBundleLoader(bundles)
	if err != nil {
		return evaluationSeedPlan{}, ErrEvaluationFactualSeed
	}
	resolver, err := newEvaluationSeedScopeResolver(scopes)
	if err != nil {
		return evaluationSeedPlan{}, ErrEvaluationFactualSeed
	}
	return evaluationSeedPlan{
		organizationExternal: organizationExternal,
		organizationID:       organizationID,
		loader:               loader,
		jobs:                 jobs,
		resolver:             resolver,
	}, nil
}

// seedEvaluationFactualCorpus persists all factual snapshots and rebuilds the
// local text/relational projections needed by ProductionContextService. It
// does not open a pool: lifecycle and DSN ownership remain at the CLI caller.
func seedEvaluationFactualCorpus(ctx context.Context, corpus evaluation.FactualCorpus, pool *pgxpool.Pool) (evaluation.EvaluationScopeResolver, error) {
	if ctx == nil || pool == nil {
		return nil, ErrEvaluationFactualSeed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plan, err := newEvaluationSeedPlan(corpus)
	if err != nil {
		return nil, err
	}

	repository := persistence.NewRepository(pool)
	jobs := ingestion.NewMemoryStore(ingestion.MemoryStoreOptions{
		Now: func() time.Time { return time.Unix(0, 0).UTC() },
	})
	for _, job := range plan.jobs {
		if _, err := jobs.Create(ctx, job); err != nil {
			return nil, ErrEvaluationFactualSeed
		}
	}

	relational := evaluationSeedRelationalValidator{repository: repository}
	pipeline, err := ingestion.NewPipelineWithEmbeddings(
		jobs,
		plan.loader,
		persistence.NewIngestionCanonicalPersister(repository),
		retrieval.NewTextProjection(persistence.NewTextProjectionStore(pool)),
		relational,
		repository,
		ingestion.EmbeddingOptions{Mode: ingestion.EmbeddingModeNotApplicable},
	)
	if err != nil {
		return nil, ErrEvaluationFactualSeed
	}
	options := ingestion.ExecutorOptions{
		OrganizationID:    plan.organizationID,
		Workers:           1,
		LeaseDuration:     evaluationSeedLease,
		HeartbeatInterval: evaluationSeedHeartbeat,
		PollInterval:      evaluationSeedPoll,
		Owner:             evaluationSeedOwner,
	}
	executor, err := ingestion.NewExecutor(jobs, pipeline.Handler(), options)
	if err != nil {
		return nil, ErrEvaluationFactualSeed
	}
	for {
		claimed, runErr := executor.RunOnce(ctx)
		if runErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, ErrEvaluationFactualSeed
		}
		if !claimed {
			break
		}
	}
	for _, job := range jobs.Snapshot() {
		if job.State != ingestion.JobStateCompleted || job.Stage != ingestion.JobStageActivation {
			return nil, ErrEvaluationFactualSeed
		}
	}
	return plan.resolver, nil
}

// evaluationSeedRelationalValidator deliberately rebuilds only disposable
// factual projections. Canonical facts remain owned by Repository.Persist.
type evaluationSeedRelationalValidator struct {
	repository *persistence.Repository
}

func (v evaluationSeedRelationalValidator) ValidateSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string) error {
	if v.repository == nil {
		return ErrEvaluationFactualSeed
	}
	_, err := v.repository.RebuildFactualProjection(ctx, organizationID, sourceID, snapshotID)
	return err
}

func evaluationSeedScopeKey(organizationExternal, sourceID, sourceRevision string) string {
	return strings.Join([]string{organizationExternal, sourceID, sourceRevision}, "\x00")
}

func deterministicEvaluationSeedBundle(input bundle.Bundle) bundle.Bundle {
	// Snapshot capture time is not factual identity, but PostgreSQL compares it
	// during idempotent retries. An epoch sentinel keeps independent corpus
	// builds compatible without changing the factual digest.
	input.Manifest.Snapshot.CapturedAt = time.Unix(0, 0).UTC()
	return input
}
