//go:build integration

package persistence_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/ingestion"
	"github.com/pedrogpaulino/manu/internal/persistence"
	domainquery "github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

const integrationPostgresDSNEnv = "MANU_TEST_POSTGRES_DSN"

// TestPostgresIntegrationFirstIngestionAndIdempotentRepeat exercises the real
// migration, canonical, textual, vector, activation, and job boundaries. It
// is opt-in because it needs a PostgreSQL instance with pgvector.
func TestPostgresIntegrationFirstIngestionAndIdempotentRepeat(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "first", "snapshot-1", "class First {}")
	repository := persistence.NewRepository(database.pool)
	options, embedder := integrationEmbeddingOptions(t, database, repository, fixture.organizationID, false)
	jobs := persistence.NewJobStore(database.pool)
	pipeline := newIntegrationPipeline(t, database, repository, jobs, fixture, options)
	runIntegrationJob(t, jobs, pipeline, fixture.job)
	completed, err := jobs.Get(context.Background(), fixture.job.OrganizationID, fixture.job.ID)
	if err != nil {
		t.Fatalf("read completed job: %v", err)
	}
	if completed.State != ingestion.JobStateCompleted || completed.Stage != ingestion.JobStageActivation {
		t.Fatalf("completed job = %#v", completed)
	}
	if embedder.calls.Load() != 1 {
		t.Fatalf("embedding calls = %d, want 1", embedder.calls.Load())
	}

	assertIntegrationCount(t, database.pool, "organizations", 1)
	assertIntegrationCount(t, database.pool, "sources", 1)
	assertIntegrationCount(t, database.pool, "analysis_snapshots", 1)
	assertIntegrationCount(t, database.pool, "artifacts", 1)
	assertIntegrationCount(t, database.pool, "observations", 1)
	assertIntegrationCount(t, database.pool, "evidence_units", 1)
	assertIntegrationCount(t, database.pool, "textual_evidence_projection", 1)
	assertIntegrationCount(t, database.pool, "embedding_items", 1)
	assertIntegrationCount(t, database.pool, "factual_identities", 3)
	assertIntegrationCount(t, database.pool, "ingestion_jobs", 1)
	assertActiveSnapshot(t, database.pool, fixture.organizationID, fixture.sourceID, fixture.job.SnapshotID)
	assertVectorDimension(t, database.pool, 3)

	// Recreating the same durable job returns the existing identity and does
	// not make a completed job claimable again.
	repeated, err := jobs.Create(context.Background(), fixture.job)
	if err != nil {
		t.Fatalf("repeat job creation: %v", err)
	}
	if repeated.ID != fixture.job.ID || repeated.State != ingestion.JobStateCompleted {
		t.Fatalf("repeat job = %#v, want completed original", repeated)
	}
	executor, err := newIntegrationExecutor(jobs, pipeline, fixture.job.OrganizationID, "repeat")
	if err != nil {
		t.Fatalf("new repeat executor: %v", err)
	}
	claimed, err := executor.RunOnce(context.Background())
	if err != nil || claimed {
		t.Fatalf("repeat RunOnce() = claimed %v, err %v; want no claim", claimed, err)
	}

	// The canonical API is also safe to retry directly after activation. The
	// row counts prove that no snapshot-scoped facts or vectors are duplicated.
	if _, err := repository.PersistBundle(context.Background(), fixture.input); err != nil {
		t.Fatalf("repeat canonical persistence: %v", err)
	}
	assertIntegrationCount(t, database.pool, "analysis_snapshots", 1)
	assertIntegrationCount(t, database.pool, "evidence_units", 1)
	assertIntegrationCount(t, database.pool, "embedding_items", 1)
}

func TestPostgresIntegrationLocalizedUpdatePreservesHistoryAndRebuildsAffectedRows(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	previous := integrationFixture(t, "localized", "snapshot-1", "class Stable {}")
	current := integrationFixture(t, "localized", "snapshot-2", "class Changed {}")
	repository := persistence.NewRepository(database.pool)
	options, embedder := integrationEmbeddingOptions(t, database, repository, previous.organizationID, false)
	jobs := persistence.NewJobStore(database.pool)
	pipeline := newIntegrationPipeline(t, database, repository, jobs, previous, options)
	runIntegrationJob(t, jobs, pipeline, previous.job)

	result, err := pipeline.ApplyIncremental(context.Background(), previous.input, current.input)
	if err != nil {
		t.Fatalf("localized update: %v", err)
	}
	if result.Partial || result.Canonical.SnapshotID != current.job.SnapshotID {
		t.Fatalf("localized result = %#v", result)
	}
	if embedder.calls.Load() != 2 {
		t.Fatalf("embedding calls after changed update = %d, want 2", embedder.calls.Load())
	}

	assertIntegrationCount(t, database.pool, "analysis_snapshots", 2)
	assertIntegrationCount(t, database.pool, "evidence_units", 2)
	assertIntegrationCount(t, database.pool, "textual_evidence_projection", 2)
	assertIntegrationCount(t, database.pool, "embedding_items", 2)
	assertIntegrationCount(t, database.pool, "factual_identities", 6)
	assertActiveSnapshot(t, database.pool, previous.organizationID, previous.sourceID, current.job.SnapshotID)
	assertHistoricalIdentityState(t, database.pool, previous.organizationID)
	assertSnapshotText(t, database.pool, previous.job.SnapshotID, "class Stable {}")
	assertSnapshotText(t, database.pool, current.job.SnapshotID, "class Changed {}")
	assertVectorDimension(t, database.pool, 3)

	var distinctIdentityKeys int
	if err := database.pool.QueryRow(context.Background(), `
SELECT COUNT(DISTINCT identity_key)
FROM factual_identities
WHERE organization_id = $1`, previous.job.OrganizationID).Scan(&distinctIdentityKeys); err != nil {
		t.Fatalf("count stable factual identity keys: %v", err)
	}
	if distinctIdentityKeys != 3 {
		t.Fatalf("distinct factual identity keys = %d, want 3", distinctIdentityKeys)
	}
}

func TestPostgresIntegrationPartialEmbeddingActivatesLocallyAndResumesWithoutReingestion(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "partial", "snapshot-1", "class Partial {}")
	repository := persistence.NewRepository(database.pool)
	failingOptions, failingEmbedder := integrationEmbeddingOptions(t, database, repository, fixture.organizationID, true)
	jobs := persistence.NewJobStore(database.pool)
	pipeline := newIntegrationPipeline(t, database, repository, jobs, fixture, failingOptions)

	executor, err := newIntegrationExecutor(jobs, pipeline, fixture.job.OrganizationID, "partial")
	if err != nil {
		t.Fatalf("new partial executor: %v", err)
	}
	if _, err := jobs.Create(context.Background(), fixture.job); err != nil {
		t.Fatalf("create partial job: %v", err)
	}
	claimed, runErr := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(runErr, ingestion.ErrJobPartial) {
		t.Fatalf("partial RunOnce() = claimed %v, err %v", claimed, runErr)
	}
	if failingEmbedder.calls.Load() != 1 {
		t.Fatalf("failing embedding calls = %d, want 1", failingEmbedder.calls.Load())
	}
	partial, err := jobs.Get(context.Background(), fixture.job.OrganizationID, fixture.job.ID)
	if err != nil {
		t.Fatalf("read partial job: %v", err)
	}
	if partial.State != ingestion.JobStatePartial || partial.Stage != ingestion.JobStageEmbeddingProjection || partial.DiagnosticCode != ingestion.DiagnosticCodeEmbeddingUnavailable {
		t.Fatalf("partial job = %#v", partial)
	}
	assertActiveSnapshot(t, database.pool, fixture.organizationID, fixture.sourceID, fixture.job.SnapshotID)
	assertIntegrationCount(t, database.pool, "analysis_snapshots", 1)
	assertIntegrationCount(t, database.pool, "evidence_units", 1)
	assertIntegrationCount(t, database.pool, "textual_evidence_projection", 1)
	assertIntegrationCount(t, database.pool, "embedding_items", 0)

	successOptions, successfulEmbedder := integrationEmbeddingOptions(t, database, repository, fixture.organizationID, false)
	resumePipeline := newIntegrationPipeline(t, database, repository, jobs, fixture, successOptions)
	resumed, err := resumePipeline.ResumePartial(context.Background(), fixture.job.OrganizationID, fixture.job.ID, "resume-worker", time.Minute)
	if err != nil {
		t.Fatalf("resume partial: %v", err)
	}
	if resumed.State != ingestion.JobStateCompleted || resumed.Stage != ingestion.JobStageActivation {
		t.Fatalf("resumed job = %#v", resumed)
	}
	if successfulEmbedder.calls.Load() != 1 {
		t.Fatalf("successful embedding calls = %d, want 1", successfulEmbedder.calls.Load())
	}
	// Resume reads canonical evidence through the persistence source. It does
	// not invoke the bundle loader or canonical persister again.
	assertIntegrationCount(t, database.pool, "analysis_snapshots", 1)
	assertIntegrationCount(t, database.pool, "evidence_units", 1)
	assertIntegrationCount(t, database.pool, "embedding_items", 1)
	assertVectorDimension(t, database.pool, 3)
}

func TestPostgresIntegrationConcurrentCreateAndClaimDoesNotDuplicateFacts(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "concurrent", "snapshot-1", "class Concurrent {}")
	repository := persistence.NewRepository(database.pool)
	jobs := persistence.NewJobStore(database.pool)
	pipeline := newIntegrationPipeline(t, database, repository, jobs, fixture, ingestion.EmbeddingOptions{})

	const creators = 8
	created := make([]ingestion.Job, creators)
	errorsOut := make(chan error, creators)
	var wait sync.WaitGroup
	for index := 0; index < creators; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			job, err := jobs.Create(context.Background(), fixture.job)
			if err != nil {
				errorsOut <- err
				return
			}
			created[index] = job
		}(index)
	}
	wait.Wait()
	close(errorsOut)
	for err := range errorsOut {
		t.Fatalf("concurrent job creation: %v", err)
	}
	for index, job := range created {
		if job.ID != fixture.job.ID {
			t.Fatalf("created job %d = %#v, want id %s", index, job, fixture.job.ID)
		}
	}

	first, err := newIntegrationExecutor(jobs, pipeline, fixture.job.OrganizationID, "concurrent-a")
	if err != nil {
		t.Fatalf("new first executor: %v", err)
	}
	second, err := newIntegrationExecutor(jobs, pipeline, fixture.job.OrganizationID, "concurrent-b")
	if err != nil {
		t.Fatalf("new second executor: %v", err)
	}
	results := make(chan struct {
		claimed bool
		err     error
	}, 2)
	go func() {
		claimed, runErr := first.RunOnce(context.Background())
		results <- struct {
			claimed bool
			err     error
		}{claimed, runErr}
	}()
	go func() {
		claimed, runErr := second.RunOnce(context.Background())
		results <- struct {
			claimed bool
			err     error
		}{claimed, runErr}
	}()
	firstResult, secondResult := <-results, <-results
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("concurrent RunOnce() errors = %v / %v", firstResult.err, secondResult.err)
	}
	if firstResult.claimed == secondResult.claimed {
		t.Fatalf("concurrent claim results = %#v / %#v, want exactly one claim", firstResult, secondResult)
	}

	completed, err := jobs.Get(context.Background(), fixture.job.OrganizationID, fixture.job.ID)
	if err != nil {
		t.Fatalf("read concurrently processed job: %v", err)
	}
	if completed.State != ingestion.JobStateCompleted {
		t.Fatalf("concurrently processed job = %#v", completed)
	}
	assertIntegrationCount(t, database.pool, "ingestion_jobs", 1)
	assertIntegrationCount(t, database.pool, "analysis_snapshots", 1)
	assertIntegrationCount(t, database.pool, "evidence_units", 1)
	assertIntegrationCount(t, database.pool, "textual_evidence_projection", 1)
}

func TestPostgresIntegrationClaimEmptyQueueIsSafe(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	jobs := persistence.NewJobStore(database.pool)
	organizationID := "00000000-0000-4000-8000-000000000001"

	claimed, ok, err := jobs.Claim(context.Background(), organizationID, "empty-queue-worker", time.Minute)
	if err != nil {
		t.Fatalf("empty Claim() returned error: %v", err)
	}
	if ok {
		t.Fatalf("empty Claim() claimed unexpected job: %#v", claimed)
	}
}

func TestPostgresIntegrationPipelineFinishPersistsAbstentionWithCoherentPackageTimes(t *testing.T) {
	database := openPostgresIntegrationDatabase(t)
	fixture := integrationFixture(t, "query-finish", "snapshot-1", "class QueryFinish {}")
	organizationID := identity.CanonicalUUID("organization", fixture.organizationID)
	source := fixture.input.Manifest.Source
	snapshot := fixture.input.Manifest.Snapshot
	if _, err := database.pool.Exec(context.Background(), `
INSERT INTO organizations (id, external_id, name)
VALUES ($1::uuid, $2, $3)`, organizationID, fixture.organizationID, fixture.organizationID); err != nil {
		t.Fatalf("seed query organization: %v", err)
	}
	if _, err := database.pool.Exec(context.Background(), `
INSERT INTO sources (id, organization_id, external_id, name, source_type)
VALUES ($1::uuid, $2::uuid, $3, $4, $5)`,
		fixture.job.SourceID, organizationID, source.ID, source.Name, source.Type,
	); err != nil {
		t.Fatalf("seed query source: %v", err)
	}
	if _, err := database.pool.Exec(context.Background(), `
INSERT INTO analysis_snapshots (
    id, organization_id, source_id, external_id, source_revision, source_hash,
    analysis_configuration_id, factual_digest, captured_at
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7, $8, $9)`,
		fixture.job.SnapshotID, organizationID, fixture.job.SourceID, snapshot.ID,
		snapshot.Revision, snapshot.Hash, fixture.input.Manifest.Analysis.ConfigurationID,
		fixture.input.Manifest.FactualDigest, snapshot.CapturedAt,
	); err != nil {
		t.Fatalf("seed query snapshot: %v", err)
	}

	queries := persistence.NewPipelineQueryRepository(database.pool)
	question := "which inventory is observed?"
	questionDigest := integrationDigest(question)
	input := domainquery.ExecutionInput{
		Question: question, QuestionKind: domainquery.KnowledgeQuestionInventory,
		SourceID: fixture.job.SourceID, SnapshotID: fixture.job.SnapshotID,
		QuestionDigest: questionDigest,
	}
	started, err := queries.Start(context.Background(), fixture.organizationID, input)
	if err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	if started.State != domainquery.ExecutionStateRunning || started.StartedAt == nil {
		t.Fatalf("started execution = %#v", started)
	}

	scope := domainquery.Scope{
		OrganizationID: identity.CanonicalUUID("organization", fixture.organizationID),
		SourceID:       fixture.job.SourceID,
		SnapshotID:     fixture.job.SnapshotID,
	}
	composition, err := domainquery.ComposeEvidencePackage(context.Background(), domainquery.PackageRequest{Scope: scope})
	if err != nil {
		t.Fatalf("ComposeEvidencePackage() error: %v", err)
	}
	abstention, err := domainquery.EvaluateAbstention(domainquery.AbstentionInput{
		Package: composition.ValidationPackage, QueryID: started.ID, QueryDigest: questionDigest,
		QuestionKind: domainquery.KnowledgeQuestionInventory,
		Support:      domainquery.SupportAssessment{Kind: domainquery.KnowledgeQuestionInventory, Level: domainquery.EvidenceSupportNone},
	})
	if err != nil {
		t.Fatalf("EvaluateAbstention() error: %v", err)
	}
	finishedAt := started.StartedAt.Add(2 * time.Second)
	finished, err := queries.Finish(context.Background(), fixture.organizationID, domainquery.QueryOutcome{
		ExecutionID: started.ID, Input: input, State: domainquery.ExecutionStateAbstained,
		QuestionDigest: questionDigest, PackageDigest: composition.ValidationPackage.Digest,
		Response: abstention.Response, HasResponse: true, StartedAt: *started.StartedAt, FinishedAt: finishedAt,
		Composition: composition,
	})
	if err != nil {
		t.Fatalf("Finish() error: %v", err)
	}
	if finished.State != domainquery.ExecutionStateAbstained || finished.FinishedAt == nil {
		t.Fatalf("finished execution = %#v", finished)
	}

	var packageCreatedAt, packageFinalizedAt time.Time
	if err := database.pool.QueryRow(context.Background(), `
SELECT created_at, finalized_at
FROM evidence_packages
WHERE organization_id = $1::uuid AND query_id = $2::uuid`,
		identity.CanonicalUUID("organization", fixture.organizationID), started.ID,
	).Scan(&packageCreatedAt, &packageFinalizedAt); err != nil {
		t.Fatalf("read persisted evidence package times: %v", err)
	}
	if packageFinalizedAt.Before(packageCreatedAt) || !packageFinalizedAt.Equal(packageCreatedAt) {
		t.Fatalf("evidence package times created=%s finalized=%s; want equal ordered timestamps", packageCreatedAt, packageFinalizedAt)
	}

	if _, err := queries.Finish(context.Background(), fixture.organizationID, domainquery.QueryOutcome{
		ExecutionID: started.ID, Input: input, State: domainquery.ExecutionStateAbstained,
		QuestionDigest: questionDigest, PackageDigest: composition.ValidationPackage.Digest,
		Response: abstention.Response, HasResponse: true, StartedAt: *started.StartedAt, FinishedAt: finishedAt,
		Composition: composition,
	}); err == nil {
		t.Fatal("second Finish() returned success for a terminal query")
	}
	stored, err := queries.Get(context.Background(), fixture.organizationID, started.ID)
	if err != nil {
		t.Fatalf("Get() after rejected second Finish: %v", err)
	}
	if stored.State != domainquery.ExecutionStateAbstained {
		t.Fatalf("stored execution after rejected second Finish = %#v", stored)
	}
}

type postgresIntegrationDatabase struct {
	dsn            string
	schema         string
	migrationConn  *pgx.Conn
	pool           *pgxpool.Pool
	createdVector  bool
	cleanupStarted atomic.Bool
}

func openPostgresIntegrationDatabase(t *testing.T) *postgresIntegrationDatabase {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(integrationPostgresDSNEnv))
	if dsn == "" {
		t.Skip("integration test skipped: MANU_TEST_POSTGRES_DSN is not set; no PostgreSQL is simulated")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to explicit PostgreSQL DSN: %v", err)
	}
	schema := fmt.Sprintf("manu_it_%d", time.Now().UnixNano())
	if !safeIntegrationIdentifier(schema) {
		_ = conn.Close(context.Background())
		t.Fatalf("generated integration schema is unsafe")
	}
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+quoteIntegrationIdentifier(schema)); err != nil {
		_ = conn.Close(context.Background())
		t.Fatalf("create isolated integration schema: %v", err)
	}
	database := &postgresIntegrationDatabase{dsn: dsn, schema: schema, migrationConn: conn}
	t.Cleanup(func() { database.cleanup(t) })
	if err := setIntegrationSearchPath(ctx, conn, schema); err != nil {
		t.Fatalf("set migration search path: %v", err)
	}
	var vectorInstalled bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector')`).Scan(&vectorInstalled); err != nil {
		t.Fatalf("inspect pgvector extension: %v", err)
	}
	if !vectorInstalled {
		if _, err := conn.Exec(ctx, "CREATE EXTENSION vector WITH SCHEMA "+quoteIntegrationIdentifier(schema)); err != nil {
			t.Fatalf("create pgvector in isolated schema: %v", err)
		}
		database.createdVector = true
	}
	runner, err := persistence.NewEmbeddedPGXMigrationRunner(conn)
	if err != nil {
		t.Fatalf("create embedded migration runner: %v", err)
	}
	status, err := runner.Apply(ctx)
	if err != nil {
		t.Fatalf("apply real migrations: %v", err)
	}
	if !status.Ready {
		t.Fatalf("migration status is not ready: %#v", status)
	}
	// A second application proves the real migration history is idempotent.
	repeatedStatus, err := runner.Apply(ctx)
	if err != nil || !repeatedStatus.Ready {
		t.Fatalf("repeat real migrations = %#v, %v", repeatedStatus, err)
	}
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse explicit PostgreSQL DSN for pool: %v", err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatalf("create isolated PostgreSQL pool: %v", err)
	}
	database.pool = pool
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping isolated PostgreSQL pool: %v", err)
	}
	var extensionVersion string
	if err := pool.QueryRow(ctx, `SELECT extversion FROM pg_extension WHERE extname = 'vector'`).Scan(&extensionVersion); err != nil {
		t.Fatalf("confirm pgvector extension: %v", err)
	}
	if strings.TrimSpace(extensionVersion) == "" {
		t.Fatalf("pgvector extension version is empty")
	}
	return database
}

func (d *postgresIntegrationDatabase) cleanup(t *testing.T) {
	if d == nil || d.cleanupStarted.Swap(true) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if d.pool != nil {
		d.pool.Close()
	}
	if d.migrationConn != nil {
		_ = d.migrationConn.Close(ctx)
	}
	conn, err := pgx.Connect(ctx, d.dsn)
	if err != nil {
		t.Errorf("connect for integration cleanup: %v", err)
		return
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "DROP SCHEMA "+quoteIntegrationIdentifier(d.schema)+" CASCADE"); err != nil {
		t.Errorf("drop isolated integration schema: %v", err)
	}
	if d.createdVector {
		// The extension was created in the isolated schema. Dropping the schema
		// first removes only its dependent tables; this final non-CASCADE drop
		// cannot reach objects outside the explicitly created target.
		if _, err := conn.Exec(ctx, "DROP EXTENSION IF EXISTS vector"); err != nil {
			t.Errorf("drop integration-owned pgvector extension: %v", err)
		}
	}
}

func setIntegrationSearchPath(ctx context.Context, conn *pgx.Conn, schema string) error {
	_, err := conn.Exec(ctx, `SELECT set_config('search_path', $1, false)`, schema+",public")
	return err
}

func quoteIntegrationIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

type integrationFixtureData struct {
	input          bundle.Bundle
	job            ingestion.Job
	organizationID string
	sourceID       string
}

func integrationFixture(t *testing.T, name, snapshotID, content string) integrationFixtureData {
	t.Helper()
	organizationID := "integration-org-" + name
	sourceID := "integration-source-" + name
	revision := "revision-" + snapshotID
	artifactHash := integrationDigest(content + ":artifact")
	snapshotHash := integrationDigest(content + ":snapshot")
	source := contract.Source{ID: sourceID, Name: "Integration source " + name, Type: "filesystem", Revision: revision}
	snapshot := contract.Snapshot{ID: snapshotID, SourceID: sourceID, Revision: revision, Hash: snapshotHash, CapturedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)}
	artifact := contract.Artifact{SourceID: sourceID, Path: "src/" + name + ".java", Type: "java", Hash: artifactHash, Size: int64(len(content))}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	contribution := contract.Contribution{
		ArtifactID: artifact.ID, AnalyzerID: "integration-java", AnalyzerVersion: "1", Method: "symbols", Type: "java.symbol",
		Locator: contract.Locator{SourceID: sourceID, ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1, EndLine: 1},
		Value:   json.RawMessage(`{"name":"IntegrationType"}`),
	}
	contribution.ID = contract.ContributionID(contribution.ArtifactID, contribution.AnalyzerID, contribution.AnalyzerVersion, contribution.Method)
	unit := evidence.EvidenceUnit{
		Version: evidence.Version, OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID,
		ArtifactID: artifact.ID, Contribution: evidence.ContributionRef{ID: contribution.ID, ArtifactID: artifact.ID, AnalyzerID: contribution.AnalyzerID, AnalyzerVersion: contribution.AnalyzerVersion, Method: contribution.Method},
		Locator: contribution.Locator, ContentState: evidence.ContentStatePresent, Content: content,
		ContentHash: evidence.ContentDigest(content), ContentBytes: int64(len(content)), ContentCharacters: int64(len(content)),
		Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow, Classification: evidence.ClassificationSafeText,
	}
	unit.ID = evidence.EvidenceID(unit)
	legacy := contract.Manifest{
		ContractVersion: contract.Version, ResultID: "integration-result-" + name, Source: source, Snapshot: snapshot,
		Execution:     contract.ExecutionMetadata{RunID: "integration-run-" + name + "-" + snapshotID, ConfigurationID: "integration-config"},
		ArtifactCount: 1, ContributionCount: 1, Coverage: []contract.Coverage{}, Gaps: []contract.Gap{}, Failures: []contract.Failure{},
	}
	result := contract.Result{Manifest: legacy, Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}}
	digest, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{unit})
	if err != nil {
		t.Fatalf("fixture factual digest: %v", err)
	}
	manifest := bundle.Manifest{
		Version: bundle.Version, Organization: bundle.Organization{ID: organizationID, Name: "Integration organization"}, Manifest: legacy,
		Analysis: bundle.Analysis{ID: "integration-analysis-" + name, ConfigurationID: "integration-config", Revision: "analysis-" + snapshotID}, FactualDigest: digest,
		Files: []bundle.File{
			{Name: bundle.ArtifactsFileName, Bytes: 128, Count: 1, Digest: integrationDigest(artifact.ID)},
			{Name: bundle.ContributionsFileName, Bytes: 256, Count: 1, Digest: integrationDigest(contribution.ID)},
			{Name: bundle.EvidenceFileName, Bytes: int64(len(content)), Count: 1, Digest: integrationDigest(content)},
		},
		Counts:   bundle.Counts{ArtifactCount: 1, ContributionCount: 1, EvidenceUnitCount: 1},
		Limits:   bundle.Limits{MaxBundleBytes: 1 << 20, MaxManifestBytes: 1 << 16, MaxEvidenceBytes: 1 << 16, MaxArtifacts: 10, MaxContributions: 10, MaxEvidenceUnits: 10},
		Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable},
	}
	input := bundle.Bundle{Manifest: manifest, Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}, Evidence: []evidence.EvidenceUnit{unit}}
	if err := input.Validate(); err != nil {
		t.Fatalf("validate integration fixture: %v", err)
	}
	organizationCanonicalID := identity.CanonicalUUID("organization", organizationID)
	sourceCanonicalID := identity.CanonicalUUID("source", organizationID, sourceID)
	snapshotCanonicalID := identity.CanonicalUUID("snapshot", organizationID, sourceID, snapshotID)
	job, err := ingestion.NewJob(ingestion.NewJobInput{
		ID: identity.CanonicalUUID("job", organizationID, sourceID, snapshotID), OrganizationID: organizationCanonicalID,
		OrganizationExternalID: organizationID, OrganizationName: "Integration organization", SourceID: sourceCanonicalID, SnapshotID: snapshotCanonicalID,
		SourceExternalID: sourceID, SnapshotExternalID: snapshotID, FactualDigest: digest, AnalysisConfigurationID: "integration-config",
		CreatedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create integration job fixture: %v", err)
	}
	return integrationFixtureData{input: input, job: job, organizationID: organizationID, sourceID: sourceID}
}

func integrationDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

type integrationBundleLoader struct{ input bundle.Bundle }

func (l integrationBundleLoader) Load(context.Context, ingestion.Job) (bundle.Bundle, error) {
	return l.input, nil
}

type integrationCountingEmbedder struct {
	delegate aigateway.Embedder
	fail     bool
	calls    atomic.Int64
}

func (e *integrationCountingEmbedder) Embed(ctx context.Context, request aigateway.EmbeddingRequest) (aigateway.EmbeddingResult, error) {
	e.calls.Add(1)
	if e.fail {
		return aigateway.EmbeddingResult{}, errors.New("integration provider failure")
	}
	return e.delegate.Embed(ctx, request)
}

func integrationEmbeddingOptions(t *testing.T, database *postgresIntegrationDatabase, repository *persistence.Repository, organizationExternalID string, fail bool) (ingestion.EmbeddingOptions, *integrationCountingEmbedder) {
	t.Helper()
	organizationID := identity.CanonicalUUID("organization", organizationExternalID)
	configuration := json.RawMessage(`{"purpose":"integration"}`)
	digest := sha256.Sum256(configuration)
	profile := retrieval.EmbeddingProfile{
		ID: identity.CanonicalUUID("embedding-profile", organizationExternalID, "integration"), OrganizationID: organizationID,
		Provider: string(aigateway.ProviderSimulated), Model: "integration-embedding", Dimension: 3, Normalization: "none",
		ConfigurationVersion: "v1", ConfigurationDigest: hex.EncodeToString(digest[:]), Configuration: configuration,
	}
	gatewayProfile := aigateway.EmbeddingProfile{Provider: aigateway.ProviderSimulated, Model: profile.Model, Version: aigateway.EmbeddingProfileVersion, Dimension: profile.Dimension, MaxBatchSize: 8}
	backend, err := aigateway.NewSimulatedEmbedder(aigateway.SimulatedEmbedderConfig{Profile: gatewayProfile})
	if err != nil {
		t.Fatalf("create simulated integration embedder: %v", err)
	}
	embedder := &integrationCountingEmbedder{delegate: backend, fail: fail}
	return ingestion.EmbeddingOptions{
		Mode: ingestion.EmbeddingModeEnabled, Profile: profile, GatewayProfile: gatewayProfile, Embedder: embedder,
		Projector: persistence.NewEmbeddingProjectionStore(database.pool), EvidenceSource: persistence.NewIngestionEmbeddingEvidenceSource(repository),
	}, embedder
}

func newIntegrationPipeline(t *testing.T, database *postgresIntegrationDatabase, repository *persistence.Repository, jobs *persistence.JobStore, fixture integrationFixtureData, options ingestion.EmbeddingOptions) *ingestion.Pipeline {
	t.Helper()
	canonical := persistence.NewIngestionCanonicalPersister(repository)
	textProjection := retrieval.NewTextProjection(persistence.NewTextProjectionStore(database.pool))
	pipeline, err := ingestion.NewPipelineWithEmbeddings(jobs, integrationBundleLoader{input: fixture.input}, canonical, textProjection, nil, repository, options)
	if err != nil {
		t.Fatalf("compose integration pipeline: %v", err)
	}
	return pipeline
}

func runIntegrationJob(t *testing.T, jobs *persistence.JobStore, pipeline *ingestion.Pipeline, job ingestion.Job) {
	t.Helper()
	if _, err := jobs.Create(context.Background(), job); err != nil {
		t.Fatalf("create integration job: %v", err)
	}
	executor, err := newIntegrationExecutor(jobs, pipeline, job.OrganizationID, "initial")
	if err != nil {
		t.Fatalf("new integration executor: %v", err)
	}
	claimed, err := executor.RunOnce(context.Background())
	if err != nil || !claimed {
		current, getErr := jobs.Get(context.Background(), job.OrganizationID, job.ID)
		t.Fatalf("initial RunOnce() = claimed %v, err %v, stored=%#v, get_err=%v", claimed, err, current, getErr)
	}
}

func newIntegrationExecutor(jobs *persistence.JobStore, pipeline *ingestion.Pipeline, organizationID, owner string) (*ingestion.Executor, error) {
	return ingestion.NewExecutor(jobs, pipeline.Handler(), ingestion.ExecutorOptions{
		OrganizationID: organizationID, Workers: 1, LeaseDuration: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
		PollInterval: 10 * time.Millisecond, Owner: owner,
	})
}

func assertIntegrationCount(t *testing.T, pool *pgxpool.Pool, table string, expected int) {
	t.Helper()
	if strings.ContainsAny(table, "\x00;'") {
		t.Fatalf("unsafe integration table %q", table)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != expected {
		t.Fatalf("count %s = %d, want %d", table, count, expected)
	}
}

func assertActiveSnapshot(t *testing.T, pool *pgxpool.Pool, organizationExternalID, sourceExternalID, snapshotCanonicalID string) {
	t.Helper()
	organizationID := identity.CanonicalUUID("organization", organizationExternalID)
	var active string
	if err := pool.QueryRow(context.Background(), `
SELECT active_snapshot_id::text
FROM sources
WHERE organization_id = $1 AND external_id = $2`, organizationID, sourceExternalID).Scan(&active); err != nil {
		t.Fatalf("read active snapshot: %v", err)
	}
	if active != snapshotCanonicalID {
		t.Fatalf("active snapshot = %s, want %s", active, snapshotCanonicalID)
	}
}

func assertHistoricalIdentityState(t *testing.T, pool *pgxpool.Pool, organizationExternalID string) {
	t.Helper()
	organizationID := identity.CanonicalUUID("organization", organizationExternalID)
	var active, historical int
	if err := pool.QueryRow(context.Background(), `
SELECT COUNT(*) FILTER (WHERE state = 'active'), COUNT(*) FILTER (WHERE state = 'historical')
FROM factual_identities
WHERE organization_id = $1`, organizationID).Scan(&active, &historical); err != nil {
		t.Fatalf("read factual identity states: %v", err)
	}
	if active != 3 || historical != 3 {
		t.Fatalf("factual identity states = active %d/historical %d, want 3/3", active, historical)
	}
}

func assertSnapshotText(t *testing.T, pool *pgxpool.Pool, snapshotExternalID, expected string) {
	t.Helper()
	var content string
	if err := pool.QueryRow(context.Background(), `
SELECT p.content
FROM textual_evidence_projection p
JOIN analysis_snapshots s ON s.id = p.snapshot_id
WHERE s.id = $1::uuid`, snapshotExternalID).Scan(&content); err != nil {
		t.Fatalf("read textual snapshot %s: %v", snapshotExternalID, err)
	}
	if content != expected {
		t.Fatalf("text for %s = %q, want %q", snapshotExternalID, content, expected)
	}
}

func assertVectorDimension(t *testing.T, pool *pgxpool.Pool, expected int) {
	t.Helper()
	var dimension int
	if err := pool.QueryRow(context.Background(), `SELECT vector_dims(embedding) FROM embedding_items LIMIT 1`).Scan(&dimension); err != nil {
		t.Fatalf("read pgvector dimension: %v", err)
	}
	if dimension != expected {
		t.Fatalf("pgvector dimension = %d, want %d", dimension, expected)
	}
}
