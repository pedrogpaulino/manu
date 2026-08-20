package ingestion

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/aigateway"
	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestPipelineRunsOrderedNonVectorStagesAndActivatesWithCanonicalIDs(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	loader := pipelineBundleLoader{input: input}
	canonical := &pipelineCanonicalPersister{}
	events := []string{}
	text := &pipelineTextProjector{events: &events}
	relational := &pipelineRelationalValidator{events: &events}
	activator := &pipelineActivator{events: &events}
	pipeline, err := NewPipeline(store, loader, canonical, text, relational, activator)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, pipeline.Handler(), executorTestOptions(job.OrganizationID, "executor"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, err := executor.RunOnce(context.Background())
	if err != nil || !claimed {
		t.Fatalf("RunOnce() = claimed %v, err %v", claimed, err)
	}
	completed, err := store.Get(context.Background(), job.OrganizationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != JobStateCompleted || completed.Stage != JobStageActivation {
		t.Fatalf("completed job = %#v", completed)
	}
	if completed.ArtifactCount != 1 || completed.ObservationCount != 1 || completed.EvidenceCount != 1 || completed.FailureCount != 0 {
		t.Fatalf("completed counts = %#v", completed.JobCounts)
	}
	wantEvents := []string{"text", "relational", "activate"}
	if !equalStrings(events, []string{"text", "relational", "activate"}) {
		t.Fatalf("stage event order = %#v, want %#v", events, wantEvents)
	}
	if text.organizationID != canonical.result.OrganizationID || text.sourceID != canonical.result.SourceID || text.snapshotID != canonical.result.SnapshotID {
		t.Fatalf("text projection scope = %q/%q/%q, canonical = %#v", text.organizationID, text.sourceID, text.snapshotID, canonical.result)
	}
	if len(text.entries) != 1 || text.entries[0].EvidenceID != canonical.result.EvidenceIDs[input.Evidence[0].ID] {
		t.Fatalf("text entries = %#v, want canonical evidence ID", text.entries)
	}
}

func TestPipelineRejectsJobBundleMismatchBeforePersistence(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	job.SourceExternalID = "different-source"
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	loader := pipelineBundleLoader{input: input}
	canonical := &pipelineCanonicalPersister{}
	pipeline, err := NewPipeline(store, loader, canonical, &pipelineTextProjector{}, nil, &pipelineActivator{})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), job.OrganizationID, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	err = pipeline.Handle(context.Background(), claimed)
	if !errors.Is(err, ErrPipelineMismatch) {
		t.Fatalf("Handle() error = %v, want ErrPipelineMismatch", err)
	}
	if canonical.calls != 0 {
		t.Fatalf("canonical calls = %d, want 0", canonical.calls)
	}
}

func TestPipelineFailureDoesNotActivateOrLeakUnderlyingError(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	text := &pipelineTextProjector{err: errors.New("raw source secret")}
	activator := &pipelineActivator{}
	pipeline, err := NewPipeline(store, pipelineBundleLoader{input: input}, &pipelineCanonicalPersister{}, text, nil, activator)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, pipeline.Handler(), executorTestOptions(job.OrganizationID, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(err, ErrJobProcessing) {
		t.Fatalf("RunOnce() = claimed %v, err %v", claimed, err)
	}
	if strings.Contains(err.Error(), "raw source secret") {
		t.Fatalf("pipeline leaked underlying error: %v", err)
	}
	stored, err := store.Get(context.Background(), job.OrganizationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStateFailed || stored.Stage != JobStageCanonicalPersistence {
		t.Fatalf("failed job = %#v", stored)
	}
	if activator.events != nil && len(*activator.events) != 0 {
		t.Fatalf("activation events = %#v, want none", *activator.events)
	}
}

func TestPipelineCancellationBeforeActivationDoesNotCallActivator(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	activator := &pipelineActivator{}
	pipeline, err := NewPipeline(store, pipelineBundleLoader{input: input}, &pipelineCanonicalPersister{}, &pipelineTextProjector{}, nil, activator)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), job.OrganizationID, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pipeline.Handle(ctx, claimed); !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle() error = %v, want context.Canceled", err)
	}
	if activator.events != nil && len(*activator.events) != 0 {
		t.Fatalf("activation events = %#v, want none", *activator.events)
	}
}

func TestTextEntriesUseSupportedContributionMetadataAndCanonicalEvidenceIDs(t *testing.T) {
	input := pipelineTestBundle(t)
	input.Contributions[0].Type = "java.configuration"
	input.Contributions[0].Value = json.RawMessage(`{"key":"feature.flag"}`)
	result := CanonicalPersistenceResult{
		OrganizationID: identity.CanonicalUUID("organization", input.Manifest.Organization.ID),
		SourceID:       identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID),
		SnapshotID:     identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID),
		EvidenceIDs: map[string]string{
			input.Evidence[0].ID: identity.CanonicalUUID("evidence", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, input.Evidence[0].ID),
		},
	}
	entries, err := textEntries(input, result)
	if err != nil {
		t.Fatalf("textEntries() error = %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("textEntries() length = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.ProjectionKind != "configuration" || entry.ConfigurationKey != "feature.flag" {
		t.Fatalf("metadata projection = %#v", entry)
	}
	if entry.EvidenceID != result.EvidenceIDs[input.Evidence[0].ID] {
		t.Fatalf("evidence ID = %q, want canonical %q", entry.EvidenceID, result.EvidenceIDs[input.Evidence[0].ID])
	}
}

func TestPipelineEmbeddingFailureLeavesSafePartialAfterLocalProjections(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	canonical := &pipelineCanonicalPersister{}
	activator := &pipelineActivator{events: &events}
	projector := &pipelineEmbeddingProjector{}
	pipeline, err := NewPipelineWithEmbeddings(
		store,
		pipelineBundleLoader{input: input},
		canonical,
		&pipelineTextProjector{events: &events},
		&pipelineRelationalValidator{events: &events},
		activator,
		pipelineEmbeddingOptions(t, job.OrganizationID, []EmbeddingEvidence{pipelineEmbeddingEvidence(input)}),
	)
	if err != nil {
		t.Fatal(err)
	}
	pipeline.embedding.Embedder = &pipelineEmbedder{err: errors.New("provider secret content")}
	executor, err := NewExecutor(store, pipeline.Handler(), executorTestOptions(job.OrganizationID, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, runErr := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(runErr, ErrJobPartial) {
		t.Fatalf("RunOnce() = claimed %v, err %v, want partial", claimed, runErr)
	}
	stored, err := store.Get(context.Background(), job.OrganizationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStatePartial || stored.Stage != JobStageEmbeddingProjection || stored.DiagnosticCode != DiagnosticCodeEmbeddingUnavailable {
		t.Fatalf("partial job = %#v", stored)
	}
	if strings.Contains(stored.DiagnosticMessage, "provider secret") || len(projector.rebuilds) != 0 {
		t.Fatalf("unsafe diagnostic or unexpected rebuild: %#v/%d", stored, len(projector.rebuilds))
	}
	if !equalStrings(events, []string{"text", "relational", "activate"}) {
		t.Fatalf("local projection events = %#v", events)
	}
	if activator.events != nil && len(*activator.events) != 3 {
		t.Fatalf("activation events = %#v", *activator.events)
	}
}

func TestPipelineEmbeddingForbiddenDoesNotCallGatewayAndCanResumeFromCanonicalEvidence(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	canonical := &pipelineCanonicalPersister{}
	loader := pipelineBundleLoader{input: input}
	options := pipelineEmbeddingOptions(t, job.OrganizationID, nil)
	options.Mode = EmbeddingModeDisabled
	pipeline, err := NewPipelineWithEmbeddings(store, loader, canonical, &pipelineTextProjector{}, nil, &pipelineActivator{}, options)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, pipeline.Handler(), executorTestOptions(job.OrganizationID, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, runErr := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(runErr, ErrJobPartial) {
		t.Fatalf("forbidden RunOnce() = claimed %v, err %v", claimed, runErr)
	}
	partial, err := store.Get(context.Background(), job.OrganizationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if partial.State != JobStatePartial || partial.DiagnosticCode != DiagnosticCodeEmbeddingForbidden {
		t.Fatalf("forbidden partial = %#v", partial)
	}
	if canonical.calls != 1 {
		t.Fatalf("canonical calls = %d, want one initial ingestion", canonical.calls)
	}

	options.Mode = EmbeddingModeEnabled
	options.EvidenceSource = pipelineEmbeddingSource{units: []EmbeddingEvidence{pipelineEmbeddingEvidenceFromCanonical(input, canonical)}}
	embedder := &pipelineEmbedder{}
	options.Embedder = embedder
	projector := &pipelineEmbeddingProjector{}
	options.Projector = projector
	pipeline.embedding = options
	completed, resumeErr := pipeline.ResumePartial(context.Background(), job.OrganizationID, job.ID, "resume-worker", time.Minute)
	if resumeErr != nil || completed.State != JobStateCompleted {
		t.Fatalf("ResumePartial() = %#v, %v", completed, resumeErr)
	}
	if canonical.calls != 1 {
		t.Fatalf("resume reingested canonical facts: calls = %d", canonical.calls)
	}
	if embedder.calls != 1 || len(projector.rebuilds) != 1 {
		t.Fatalf("resume embedding calls/rebuilds = %d/%d", embedder.calls, len(projector.rebuilds))
	}
	if _, resumeErr = pipeline.ResumePartial(context.Background(), job.OrganizationID, job.ID, "resume-worker-2", time.Minute); !errors.Is(resumeErr, ErrJobState) {
		t.Fatalf("second ResumePartial() error = %v, want ErrJobState", resumeErr)
	}
}

func TestPipelineEmbeddingCacheHitSkipsProviderAndPreservesProjection(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	canonical := &pipelineCanonicalPersister{}
	options := pipelineEmbeddingOptions(t, job.OrganizationID, nil)
	options.EvidenceSource = pipelineEmbeddingSource{units: []EmbeddingEvidence{pipelineEmbeddingEvidenceFromCanonical(input, canonical)}}
	projector := &pipelineEmbeddingProjector{cacheHits: map[string]bool{input.Evidence[0].ContentHash: true}}
	options.Projector = projector
	embedder := &pipelineEmbedder{}
	options.Embedder = embedder
	pipeline, err := NewPipelineWithEmbeddings(store, pipelineBundleLoader{input: input}, canonical, &pipelineTextProjector{}, nil, &pipelineActivator{}, options)
	if err != nil {
		t.Fatal(err)
	}
	// The canonical ID used by the source is known before this test's call;
	// persist the fixture result once to mirror the initial ingestion boundary.
	canonical.Persist(context.Background(), input)
	claimed, ok, runErr := store.Claim(context.Background(), job.OrganizationID, "worker", time.Minute)
	if runErr != nil || !ok || claimed.ID != job.ID {
		t.Fatalf("Claim() = %#v, %v, %v", claimed, ok, runErr)
	}
	if runErr := pipeline.Handle(context.Background(), claimed); runErr != nil {
		t.Fatalf("Handle() error = %v", runErr)
	}
	if embedder.calls != 0 || len(projector.rebuilds) != 1 || len(projector.rebuilds[0]) != 1 || len(projector.rebuilds[0][0].Vector) != 0 {
		t.Fatalf("cache path calls/rebuild = %d/%#v", embedder.calls, projector.rebuilds)
	}
}

func TestPipelineRedactedEmbeddingIsForbiddenWithoutPlaceholderVector(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	unit := input.Evidence[0]
	options := pipelineEmbeddingOptions(t, job.OrganizationID, []EmbeddingEvidence{{
		ID: unitExternalCanonicalID(input, unit.ID), Content: evidence.RedactedContent,
		ContentHash: unit.ContentHash, ExternalTransfer: evidence.DecisionRedact,
	}})
	embedder := &pipelineEmbedder{}
	projector := &pipelineEmbeddingProjector{}
	options.Embedder = embedder
	options.Projector = projector
	activator := &pipelineActivator{}
	pipeline, err := NewPipelineWithEmbeddings(store, pipelineBundleLoader{input: input}, &pipelineCanonicalPersister{}, &pipelineTextProjector{}, nil, activator, options)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(store, pipeline.Handler(), executorTestOptions(job.OrganizationID, "worker"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, runErr := executor.RunOnce(context.Background())
	if !claimed || !errors.Is(runErr, ErrJobPartial) {
		t.Fatalf("RunOnce() = claimed %v, err %v", claimed, runErr)
	}
	stored, err := store.Get(context.Background(), job.OrganizationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DiagnosticCode != DiagnosticCodeEmbeddingForbidden || embedder.calls != 0 || len(projector.rebuilds) != 1 || len(projector.rebuilds[0]) != 0 {
		t.Fatalf("redacted embedding path = job %#v, embedder %d, rebuild %#v", stored, embedder.calls, projector.rebuilds)
	}
}

func TestPipelineEmbeddingCancellationLeavesLeaseRunning(t *testing.T) {
	input := pipelineTestBundle(t)
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	store := NewMemoryStore(MemoryStoreOptions{Now: func() time.Time { return now }})
	job := pipelineTestJob(t, input, now)
	if _, err := store.Create(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	options := pipelineEmbeddingOptions(t, job.OrganizationID, nil)
	options.EvidenceSource = blockingEmbeddingSource{started: make(chan struct{})}
	pipeline, err := NewPipelineWithEmbeddings(store, pipelineBundleLoader{input: input}, &pipelineCanonicalPersister{}, &pipelineTextProjector{}, nil, &pipelineActivator{}, options)
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.Claim(context.Background(), job.OrganizationID, "worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("Claim() = %#v, %v", claimed, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := options.EvidenceSource.(blockingEmbeddingSource).started
	result := make(chan error, 1)
	go func() { result <- pipeline.Handle(ctx, claimed) }()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Handle() error = %v, want context.Canceled", err)
	}
	stored, err := store.Get(context.Background(), job.OrganizationID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != JobStateRunning || stored.DiagnosticCode != "" {
		t.Fatalf("cancelled embedding job = %#v", stored)
	}
}

func pipelineEmbeddingOptions(t *testing.T, organizationID string, units []EmbeddingEvidence) EmbeddingOptions {
	t.Helper()
	configuration := json.RawMessage(`{"purpose":"test"}`)
	digest := sha256.Sum256(configuration)
	profile := retrieval.EmbeddingProfile{
		ID:                   identity.CanonicalUUID("embedding-profile", organizationID, "test"),
		OrganizationID:       organizationID,
		Provider:             "simulated",
		Model:                "embedding-test",
		Dimension:            3,
		Normalization:        "none",
		ConfigurationVersion: "v1",
		ConfigurationDigest:  hexDigest(digest[:]),
		Configuration:        configuration,
	}
	return EmbeddingOptions{
		Mode:           EmbeddingModeEnabled,
		Profile:        profile,
		GatewayProfile: aigateway.EmbeddingProfile{Provider: aigateway.ProviderSimulated, Model: "embedding-test", Version: aigateway.EmbeddingProfileVersion, Dimension: 3, MaxBatchSize: 8},
		Embedder:       &pipelineEmbedder{},
		Projector:      &pipelineEmbeddingProjector{},
		EvidenceSource: pipelineEmbeddingSource{units: units},
	}
}

func pipelineEmbeddingEvidence(input bundle.Bundle) EmbeddingEvidence {
	return EmbeddingEvidenceFromBundle(input, identity.CanonicalUUID("organization", input.Manifest.Organization.ID), identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID), identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID))
}

func pipelineEmbeddingEvidenceFromCanonical(input bundle.Bundle, canonical *pipelineCanonicalPersister) EmbeddingEvidence {
	if canonical.result.OrganizationID == "" {
		canonical.Persist(context.Background(), input)
	}
	unit := input.Evidence[0]
	return EmbeddingEvidence{ID: canonical.result.EvidenceIDs[unit.ID], Content: unit.Content, ContentHash: unit.ContentHash, ExternalTransfer: unit.ExternalTransfer}
}

func unitExternalCanonicalID(input bundle.Bundle, externalID string) string {
	return identity.CanonicalUUID("evidence", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, externalID)
}

// EmbeddingEvidenceFromBundle is a small fixture helper that mirrors the
// canonical evidence adapter without making the production pipeline depend on
// bundle storage for a partial retry.
func EmbeddingEvidenceFromBundle(input bundle.Bundle, organizationID, sourceID, snapshotID string) EmbeddingEvidence {
	unit := input.Evidence[0]
	return EmbeddingEvidence{ID: identity.CanonicalUUID("evidence", organizationID, sourceID, snapshotID, unit.ID), Content: unit.Content, ContentHash: unit.ContentHash, ExternalTransfer: unit.ExternalTransfer}
}

func hexDigest(value []byte) string { return fmt.Sprintf("%x", value) }

type pipelineEmbeddingSource struct{ units []EmbeddingEvidence }

func (s pipelineEmbeddingSource) ListEmbeddingEvidence(context.Context, string, string, string) ([]EmbeddingEvidence, error) {
	return append([]EmbeddingEvidence(nil), s.units...), nil
}

type blockingEmbeddingSource struct{ started chan struct{} }

func (s blockingEmbeddingSource) ListEmbeddingEvidence(ctx context.Context, _, _, _ string) ([]EmbeddingEvidence, error) {
	close(s.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

type pipelineEmbedder struct {
	calls int
	err   error
}

func (e *pipelineEmbedder) Embed(_ context.Context, request aigateway.EmbeddingRequest) (aigateway.EmbeddingResult, error) {
	e.calls++
	if e.err != nil {
		return aigateway.EmbeddingResult{}, e.err
	}
	vectors := make([][]float64, len(request.Items))
	for index := range vectors {
		vectors[index] = []float64{float64(index + 1), 0, math.Pi}
	}
	return aigateway.EmbeddingResult{ExecutionID: request.ExecutionID, RequestID: request.RequestID, Provider: request.Profile.Provider, Model: request.Profile.Model, Vectors: vectors, Usage: aigateway.Usage{InputItems: len(request.Items), OutputItems: len(request.Items)}, Termination: aigateway.TerminationCompleted}, nil
}

type pipelineEmbeddingProjector struct {
	cacheHits map[string]bool
	rebuilds  [][]retrieval.EmbeddingInput
}

func (p *pipelineEmbeddingProjector) EnsureProfile(_ context.Context, profile retrieval.EmbeddingProfile) (retrieval.EmbeddingProfile, error) {
	return profile, nil
}

func (p *pipelineEmbeddingProjector) Lookup(_ context.Context, key retrieval.EmbeddingCacheKey) (retrieval.EmbeddingItem, bool, error) {
	return retrieval.EmbeddingItem{}, p.cacheHits[key.EvidenceContentHash], nil
}

func (p *pipelineEmbeddingProjector) RebuildSnapshot(_ context.Context, profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, inputs []retrieval.EmbeddingInput) (retrieval.EmbeddingRebuildResult, error) {
	p.rebuilds = append(p.rebuilds, append([]retrieval.EmbeddingInput(nil), inputs...))
	return retrieval.EmbeddingRebuildResult{OrganizationID: scope.OrganizationID, ProfileID: profile.ID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID, Requested: len(inputs), Items: nil}, nil
}

type pipelineBundleLoader struct {
	input bundle.Bundle
	err   error
}

func (l pipelineBundleLoader) Load(context.Context, Job) (bundle.Bundle, error) {
	if l.err != nil {
		return bundle.Bundle{}, l.err
	}
	return l.input, nil
}

type pipelineCanonicalPersister struct {
	result CanonicalPersistenceResult
	calls  int
}

func (p *pipelineCanonicalPersister) Persist(_ context.Context, input bundle.Bundle) (CanonicalPersistenceResult, error) {
	p.calls++
	if p.result.OrganizationID != "" {
		return p.result, nil
	}
	p.result = CanonicalPersistenceResult{
		OrganizationID: identity.CanonicalUUID("organization", input.Manifest.Organization.ID),
		SourceID:       identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID),
		SnapshotID:     identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID),
		ArtifactIDs:    make(map[string]string, len(input.Artifacts)),
		ObservationIDs: make(map[string]string, len(input.Contributions)),
		EvidenceIDs:    make(map[string]string, len(input.Evidence)),
	}
	for _, artifact := range input.Artifacts {
		p.result.ArtifactIDs[artifact.ID] = identity.CanonicalUUID("artifact", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, artifact.ID)
	}
	for _, contribution := range input.Contributions {
		p.result.ObservationIDs[contribution.ID] = identity.CanonicalUUID("observation", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, contribution.ID)
	}
	for _, unit := range input.Evidence {
		p.result.EvidenceIDs[unit.ID] = identity.CanonicalUUID("evidence", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, unit.ID)
	}
	return p.result, nil
}

type pipelineTextProjector struct {
	organizationID string
	sourceID       string
	snapshotID     string
	entries        []retrieval.TextEntry
	err            error
	events         *[]string
}

func (p *pipelineTextProjector) RebuildSnapshot(_ context.Context, organizationID, sourceID, snapshotID string, entries []retrieval.TextEntry) error {
	if p.events != nil {
		*p.events = append(*p.events, "text")
	}
	p.organizationID, p.sourceID, p.snapshotID = organizationID, sourceID, snapshotID
	p.entries = append([]retrieval.TextEntry(nil), entries...)
	return p.err
}

type pipelineRelationalValidator struct {
	events *[]string
	err    error
}

func (p *pipelineRelationalValidator) ValidateSnapshot(_ context.Context, _, _, _ string) error {
	if p.events != nil {
		*p.events = append(*p.events, "relational")
	}
	return p.err
}

type pipelineActivator struct {
	events *[]string
}

func (p *pipelineActivator) ActivateSnapshot(_ context.Context, _, _, _ string) error {
	if p.events != nil {
		*p.events = append(*p.events, "activate")
	}
	return nil
}

func pipelineTestJob(t *testing.T, input bundle.Bundle, createdAt time.Time) Job {
	t.Helper()
	job, err := NewJob(NewJobInput{
		ID:                      testJobID,
		OrganizationID:          identity.CanonicalUUID("organization", input.Manifest.Organization.ID),
		OrganizationExternalID:  input.Manifest.Organization.ID,
		SourceID:                identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID),
		SnapshotID:              identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID),
		SourceExternalID:        input.Manifest.Source.ID,
		SnapshotExternalID:      input.Manifest.Snapshot.ID,
		FactualDigest:           input.Manifest.FactualDigest,
		AnalysisConfigurationID: input.Manifest.Analysis.ConfigurationID,
		CreatedAt:               createdAt,
	})
	if err != nil {
		t.Fatalf("NewJob() error = %v", err)
	}
	return job
}

func pipelineTestBundle(t *testing.T) bundle.Bundle {
	t.Helper()
	const organizationID = "organization-1"
	const sourceID = "source-1"
	const revision = "revision-1"
	const configurationID = "configuration-1"
	source := contract.Source{ID: sourceID, Name: "fixture", Type: "filesystem", Revision: revision}
	snapshot := contract.Snapshot{ID: contract.SnapshotID(sourceID, revision, strings.Repeat("b", 64)), SourceID: sourceID, Revision: revision, Hash: strings.Repeat("b", 64)}
	artifact := contract.Artifact{SourceID: sourceID, Path: "src/A.java", Type: "java", Hash: strings.Repeat("a", 64), Size: 12}
	artifact.ID = contract.ArtifactID(artifact.SourceID, artifact.Path, artifact.Hash)
	contribution := contract.Contribution{ArtifactID: artifact.ID, AnalyzerID: "java", AnalyzerVersion: "1", Method: "symbols", Type: "symbol", Locator: contract.Locator{SourceID: sourceID, ArtifactID: artifact.ID, Path: artifact.Path, StartLine: 1, EndLine: 1}}
	contribution.ID = contract.ContributionID(contribution.ArtifactID, contribution.AnalyzerID, contribution.AnalyzerVersion, contribution.Method)
	legacy := contract.Manifest{ContractVersion: contract.Version, ResultID: "result-1", Source: source, Snapshot: snapshot, Execution: contract.ExecutionMetadata{RunID: "run-1", ConfigurationID: configurationID}, ArtifactCount: 1, ContributionCount: 1, Coverage: []contract.Coverage{}, Gaps: []contract.Gap{}, Failures: []contract.Failure{}}
	result := contract.Result{Manifest: legacy, Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}}
	content := "class A {}"
	unit := evidence.EvidenceUnit{Version: evidence.Version, OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshot.ID, ArtifactID: artifact.ID, Contribution: evidence.ContributionRef{ID: contribution.ID, ArtifactID: artifact.ID, AnalyzerID: contribution.AnalyzerID, AnalyzerVersion: contribution.AnalyzerVersion, Method: contribution.Method}, Locator: contribution.Locator, ContentState: evidence.ContentStatePresent, Content: content, ContentHash: evidence.ContentDigest(content), ContentBytes: int64(len(content)), ContentCharacters: int64(len(content)), Persist: evidence.DecisionAllow, ExternalTransfer: evidence.DecisionAllow}
	unit.ID = evidence.EvidenceID(unit)
	digest, err := bundle.FactualDigest(result, []evidence.EvidenceUnit{unit})
	if err != nil {
		t.Fatalf("FactualDigest() error = %v", err)
	}
	return bundle.Bundle{Manifest: bundle.Manifest{Version: bundle.Version, Organization: bundle.Organization{ID: organizationID, Name: "Fixture organization"}, Manifest: legacy, Analysis: bundle.Analysis{ID: "analysis-1", ConfigurationID: configurationID, Revision: "analysis-revision-1"}, FactualDigest: digest, Files: []bundle.File{{Name: bundle.ArtifactsFileName, Bytes: 256, Count: 1, Digest: strings.Repeat("c", 64)}, {Name: bundle.ContributionsFileName, Bytes: 256, Count: 1, Digest: strings.Repeat("c", 64)}, {Name: bundle.EvidenceFileName, Bytes: 256, Count: 1, Digest: strings.Repeat("c", 64)}}, Counts: bundle.Counts{ArtifactCount: 1, ContributionCount: 1, EvidenceUnitCount: 1}, Limits: bundle.Limits{MaxBundleBytes: 1 << 20, MaxManifestBytes: 1 << 16, MaxEvidenceBytes: 1 << 16, MaxArtifacts: 10, MaxContributions: 10, MaxEvidenceUnits: 10}, Evidence: bundle.EvidenceMetadata{State: bundle.EvidenceStateAvailable}}, Artifacts: []contract.Artifact{artifact}, Contributions: []contract.Contribution{contribution}, Evidence: []evidence.EvidenceUnit{unit}}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
