package ingestion

import (
	"context"
	"errors"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

type incrementalCanonicalFake struct {
	result CanonicalPersistenceResult
	report IncrementalReport
	err    error
	calls  int
	order  *[]string
}

func (f *incrementalCanonicalFake) PersistIncremental(_ context.Context, _ bundle.Bundle, _ bundle.Bundle, _ ...IncrementalOptions) (CanonicalPersistenceResult, IncrementalReport, error) {
	f.calls++
	if f.order != nil {
		*f.order = append(*f.order, "canonical")
	}
	return f.result, f.report, f.err
}

type incrementalTextFake struct {
	entries    []retrieval.IncrementalTextEntry
	previousID string
	currentID  string
	err        error
	order      *[]string
}

func (f *incrementalTextFake) RebuildSnapshotIncremental(_ context.Context, _, _, previousID, currentID string, entries []retrieval.IncrementalTextEntry) error {
	f.previousID = previousID
	f.currentID = currentID
	f.entries = append([]retrieval.IncrementalTextEntry(nil), entries...)
	if f.order != nil {
		*f.order = append(*f.order, "text")
	}
	return f.err
}

type incrementalRelationalFake struct{ order *[]string }

func (f *incrementalRelationalFake) ValidateSnapshot(context.Context, string, string, string) error {
	if f.order != nil {
		*f.order = append(*f.order, "relational")
	}
	return nil
}

type incrementalActivatorFake struct {
	calls int
	order *[]string
}

func (f *incrementalActivatorFake) ActivateSnapshot(context.Context, string, string, string) error {
	f.calls++
	if f.order != nil {
		*f.order = append(*f.order, "activation")
	}
	return nil
}

func TestIncrementalUpdaterAppliesDeltaAndUsesCanonicalPreviousSnapshotID(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	report, err := CompareBundles(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	canonical := canonicalResultForIncrementalBundle(current)
	order := make([]string, 0, 4)
	text := &incrementalTextFake{order: &order}
	activator := &incrementalActivatorFake{order: &order}
	updater, err := NewIncrementalUpdater(
		&incrementalCanonicalFake{result: canonical, report: report, order: &order},
		text, &incrementalRelationalFake{order: &order}, activator,
	)
	if err != nil {
		t.Fatalf("NewIncrementalUpdater() error = %v", err)
	}
	result, err := updater.Apply(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if result.Canonical.SnapshotID != canonical.SnapshotID || result.Textual != 1 {
		t.Fatalf("apply result = %#v", result)
	}
	expectedPreviousSnapshot := identity.CanonicalUUID("snapshot", previous.Manifest.Organization.ID, previous.Manifest.Source.ID, previous.Manifest.Snapshot.ID)
	if text.previousID != expectedPreviousSnapshot || text.currentID != canonical.SnapshotID {
		t.Fatalf("projection snapshot IDs = %q/%q, want %q/%q", text.previousID, text.currentID, expectedPreviousSnapshot, canonical.SnapshotID)
	}
	if len(text.entries) != 1 || !text.entries[0].Reuse {
		t.Fatalf("textual incremental entries = %#v", text.entries)
	}
	expectedPreviousEvidence := identity.CanonicalUUID("evidence", previous.Manifest.Organization.ID, previous.Manifest.Source.ID, previous.Manifest.Snapshot.ID, previous.Evidence[0].ID)
	if text.entries[0].PreviousEvidenceID != expectedPreviousEvidence {
		t.Fatalf("previous evidence ID = %q, want %q", text.entries[0].PreviousEvidenceID, expectedPreviousEvidence)
	}
	if got, want := order, []string{"canonical", "text", "relational", "activation"}; !equalIncrementalStrings(got, want) {
		t.Fatalf("stage order = %#v, want %#v", got, want)
	}
	if activator.calls != 1 {
		t.Fatalf("activation calls = %d, want 1", activator.calls)
	}
}

func TestIncrementalUpdaterDoesNotActivateAfterProjectionFailure(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	report, err := CompareBundles(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	order := make([]string, 0, 2)
	activator := &incrementalActivatorFake{order: &order}
	updater, err := NewIncrementalUpdater(
		&incrementalCanonicalFake{result: canonicalResultForIncrementalBundle(current), report: report, order: &order},
		&incrementalTextFake{err: errors.New("provider payload must not escape"), order: &order}, nil, activator,
	)
	if err != nil {
		t.Fatalf("NewIncrementalUpdater() error = %v", err)
	}
	if _, err := updater.Apply(context.Background(), previous, current); !errors.Is(err, ErrIncrementalApply) {
		t.Fatalf("Apply() error = %v, want ErrIncrementalApply", err)
	}
	if activator.calls != 0 {
		t.Fatalf("activation calls = %d after projection failure, want 0", activator.calls)
	}
}

func TestIncrementalUpdaterCallsEmbeddingOnlyForAffectedEvidence(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	organizationID := identity.CanonicalUUID("organization", current.Manifest.Organization.ID)
	embedding := pipelineEmbeddingOptions(t, organizationID, nil)
	profile := embedding.Profile
	report, err := CompareBundles(context.Background(), previous, current, IncrementalOptions{EmbeddingProfile: &profile})
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	embedder := embedding.Embedder.(*pipelineEmbedder)
	projector := embedding.Projector.(*pipelineEmbeddingProjector)
	order := make([]string, 0, 3)
	activator := &incrementalActivatorFake{order: &order}
	updater, err := NewIncrementalUpdater(&incrementalCanonicalFake{result: canonicalResultForIncrementalBundle(current), report: report, order: &order}, &incrementalTextFake{order: &order}, nil, activator, embedding)
	if err != nil {
		t.Fatalf("NewIncrementalUpdater() error = %v", err)
	}
	if _, err := updater.Apply(context.Background(), previous, current); err != nil {
		t.Fatalf("unchanged Apply() error = %v", err)
	}
	if embedder.calls != 0 || len(projector.rebuilds) != 1 || len(projector.rebuilds[0]) != 1 {
		t.Fatalf("unchanged embedding work = provider %d rebuilds %#v", embedder.calls, projector.rebuilds)
	}
	if !report.EmbeddingProfileCompatible {
		t.Fatalf("profile unexpectedly incompatible: %#v", report)
	}

	changed := incrementalSnapshotCopy(t, previous, "snapshot-3")
	changed.Evidence[0].Content = "changed content"
	changed.Evidence[0].ContentHash = evidence.ContentDigest(changed.Evidence[0].Content)
	changed.Evidence[0].ContentBytes = int64(len(changed.Evidence[0].Content))
	changed.Evidence[0].ContentCharacters = int64(len(changed.Evidence[0].Content))
	changed.Evidence[0].ID = evidence.EvidenceID(changed.Evidence[0])
	changed = refreshIncrementalDigest(t, changed)
	changedReport, err := CompareBundles(context.Background(), previous, changed, IncrementalOptions{EmbeddingProfile: &profile})
	if err != nil {
		t.Fatalf("changed CompareBundles() error = %v", err)
	}
	changedCanonical := canonicalResultForIncrementalBundle(changed)
	changedEmbedder := &pipelineEmbedder{}
	changedProjector := &pipelineEmbeddingProjector{}
	changedEmbedding := embedding
	changedEmbedding.Embedder = changedEmbedder
	changedEmbedding.Projector = changedProjector
	changedUpdater, err := NewIncrementalUpdater(&incrementalCanonicalFake{result: changedCanonical, report: changedReport}, &incrementalTextFake{}, nil, &incrementalActivatorFake{}, changedEmbedding)
	if err != nil {
		t.Fatalf("NewIncrementalUpdater(changed) error = %v", err)
	}
	if _, err := changedUpdater.Apply(context.Background(), previous, changed); err != nil {
		t.Fatalf("changed Apply() error = %v", err)
	}
	if changedEmbedder.calls != 1 || len(changedProjector.rebuilds) != 1 || len(changedProjector.rebuilds[0]) != 1 || len(changedProjector.rebuilds[0][0].Vector) != 3 {
		t.Fatalf("changed embedding work = provider %d rebuilds %#v", changedEmbedder.calls, changedProjector.rebuilds)
	}
}

func TestIncrementalUpdaterMarksEmbeddingPolicyAsPartialAfterLocalActivation(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	current.Evidence[0].ExternalTransfer = evidence.DecisionDeny
	current.Evidence[0].ID = evidence.EvidenceID(current.Evidence[0])
	current = refreshIncrementalDigest(t, current)
	organizationID := identity.CanonicalUUID("organization", current.Manifest.Organization.ID)
	embedding := pipelineEmbeddingOptions(t, organizationID, nil)
	profile := embedding.Profile
	report, err := CompareBundles(context.Background(), previous, current, IncrementalOptions{EmbeddingProfile: &profile})
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	activator := &incrementalActivatorFake{}
	updater, err := NewIncrementalUpdater(&incrementalCanonicalFake{result: canonicalResultForIncrementalBundle(current), report: report}, &incrementalTextFake{}, nil, activator, embedding)
	if err != nil {
		t.Fatalf("NewIncrementalUpdater() error = %v", err)
	}
	result, err := updater.Apply(context.Background(), previous, current)
	if !errors.Is(err, ErrIncrementalPartial) || !result.Partial || result.PartialReason != "embedding_forbidden" {
		t.Fatalf("partial result/error = %#v / %v", result, err)
	}
	if activator.calls != 1 {
		t.Fatalf("partial activation calls = %d, want 1", activator.calls)
	}
}

func TestIncrementalUpdaterRebuildsAllEligibleEmbeddingsWhenProfileChanges(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	organizationID := identity.CanonicalUUID("organization", current.Manifest.Organization.ID)
	embedding := pipelineEmbeddingOptions(t, organizationID, nil)
	previousProfile := testIncrementalProfile(t, "previous-profile", "embedding-old")
	currentProfile := embedding.Profile
	report, err := CompareBundles(context.Background(), previous, current, IncrementalOptions{PreviousEmbeddingProfile: &previousProfile, CurrentEmbeddingProfile: &currentProfile})
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	if report.EmbeddingProfileCompatible {
		t.Fatalf("profile unexpectedly compatible: %#v", report)
	}
	embedder := &pipelineEmbedder{}
	projector := &pipelineEmbeddingProjector{}
	embedding.Embedder = embedder
	embedding.Projector = projector
	updater, err := NewIncrementalUpdater(&incrementalCanonicalFake{result: canonicalResultForIncrementalBundle(current), report: report}, &incrementalTextFake{}, nil, &incrementalActivatorFake{}, embedding)
	if err != nil {
		t.Fatalf("NewIncrementalUpdater() error = %v", err)
	}
	if _, err := updater.Apply(context.Background(), previous, current); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if embedder.calls != 1 || len(projector.rebuilds) != 1 || len(projector.rebuilds[0]) != 1 || projector.rebuilds[0][0].Vector == nil {
		t.Fatalf("profile-change embedding work = provider %d rebuilds %#v", embedder.calls, projector.rebuilds)
	}
}

func canonicalResultForIncrementalBundle(input bundle.Bundle) CanonicalPersistenceResult {
	result := CanonicalPersistenceResult{
		OrganizationID: identity.CanonicalUUID("organization", input.Manifest.Organization.ID),
		SourceID:       identity.CanonicalUUID("source", input.Manifest.Organization.ID, input.Manifest.Source.ID),
		SnapshotID:     identity.CanonicalUUID("snapshot", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID),
		ArtifactIDs:    make(map[string]string, len(input.Artifacts)), ObservationIDs: make(map[string]string, len(input.Contributions)), EvidenceIDs: make(map[string]string, len(input.Evidence)),
	}
	for _, artifact := range input.Artifacts {
		result.ArtifactIDs[artifact.ID] = identity.CanonicalUUID("artifact", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, artifact.ID)
	}
	for _, contribution := range input.Contributions {
		result.ObservationIDs[contribution.ID] = identity.CanonicalUUID("observation", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, contribution.ID)
	}
	for _, unit := range input.Evidence {
		result.EvidenceIDs[unit.ID] = identity.CanonicalUUID("evidence", input.Manifest.Organization.ID, input.Manifest.Source.ID, input.Manifest.Snapshot.ID, unit.ID)
	}
	return result
}

func equalIncrementalStrings(left, right []string) bool {
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
