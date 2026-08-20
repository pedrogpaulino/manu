package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

func TestCompareBundlesReusesUnchangedFactsAcrossImmutableSnapshots(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")

	report, err := CompareBundles(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	if report.PreviousSnapshotID == report.CurrentSnapshotID || !report.ConfigurationCompatible {
		t.Fatalf("snapshot/configuration compatibility = %#v", report)
	}
	if len(report.Reused) != 3 || len(report.Changed) != 0 || len(report.Added) != 0 || len(report.Removed) != 0 {
		t.Fatalf("unchanged delta = %#v", report)
	}
	if report.WorkSaved.Facts != 3 || report.WorkSaved.Textual != 1 || report.WorkSaved.Relational != 2 {
		t.Fatalf("work saved = %#v", report.WorkSaved)
	}
	if report.WorkSaved.Embeddings != 0 {
		t.Fatalf("embedding work saved without a profile = %#v", report.WorkSaved)
	}
	if err := report.Validate(); err != nil {
		t.Fatalf("report.Validate() error = %v", err)
	}
}

func TestCompareBundlesClassifiesLocalizedChangeAdditionAndRemoval(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")

	// Alter the bounded evidence and its contribution value without changing
	// the stable artifact/location keys. The artifact remains factual and is
	// therefore reusable; the evidence and observation are changed.
	current.Contributions[0].Value = []byte(`{"name":"changed"}`)
	current.Evidence[0].Content = "class Changed {}"
	current.Evidence[0].ContentHash = evidence.ContentDigest(current.Evidence[0].Content)
	current.Evidence[0].ContentBytes = int64(len(current.Evidence[0].Content))
	current.Evidence[0].ContentCharacters = int64(len(current.Evidence[0].Content))
	current.Evidence[0].ID = evidence.EvidenceID(current.Evidence[0])
	current = refreshIncrementalDigest(t, current)

	// Add a second artifact/contribution/evidence unit and remove the original
	// coverage-free shape only through the changed evidence; this exercises the
	// add path independently from changed facts.
	addedArtifact := current.Artifacts[0]
	addedArtifact.Path = "src/B.java"
	addedArtifact.Hash = strings.Repeat("d", 64)
	addedArtifact.ID = contract.ArtifactID(addedArtifact.SourceID, addedArtifact.Path, addedArtifact.Hash)
	current.Artifacts = append(current.Artifacts, addedArtifact)
	addedContribution := current.Contributions[0]
	addedContribution.ArtifactID = addedArtifact.ID
	addedContribution.Locator.ArtifactID = addedArtifact.ID
	addedContribution.Locator.Path = addedArtifact.Path
	addedContribution.ID = contract.ContributionID(addedContribution.ArtifactID, addedContribution.AnalyzerID, addedContribution.AnalyzerVersion, "second")
	addedContribution.Method = "second"
	current.Contributions = append(current.Contributions, addedContribution)
	addedUnit := current.Evidence[0]
	addedUnit.ArtifactID = addedArtifact.ID
	addedUnit.Contribution = evidence.ContributionRef{ID: addedContribution.ID, ArtifactID: addedArtifact.ID, AnalyzerID: addedContribution.AnalyzerID, AnalyzerVersion: addedContribution.AnalyzerVersion, Method: addedContribution.Method}
	addedUnit.Locator = addedContribution.Locator
	addedUnit.Content = "class B {}"
	addedUnit.ContentHash = evidence.ContentDigest(addedUnit.Content)
	addedUnit.ContentBytes = int64(len(addedUnit.Content))
	addedUnit.ContentCharacters = int64(len(addedUnit.Content))
	addedUnit.ID = evidence.EvidenceID(addedUnit)
	current.Evidence = append(current.Evidence, addedUnit)
	current.Manifest.Manifest.ArtifactCount = len(current.Artifacts)
	current.Manifest.Manifest.ContributionCount = len(current.Contributions)
	current.Manifest.Counts.ArtifactCount = int64(len(current.Artifacts))
	current.Manifest.Counts.ContributionCount = int64(len(current.Contributions))
	current.Manifest.Counts.EvidenceUnitCount = int64(len(current.Evidence))
	current.Manifest.Files = append([]bundle.File(nil), current.Manifest.Files...)
	for index := range current.Manifest.Files {
		switch current.Manifest.Files[index].Name {
		case bundle.ArtifactsFileName, bundle.ContributionsFileName, bundle.EvidenceFileName:
			current.Manifest.Files[index].Count = int64(len(current.Artifacts))
			if current.Manifest.Files[index].Name == bundle.ContributionsFileName {
				current.Manifest.Files[index].Count = int64(len(current.Contributions))
			}
			if current.Manifest.Files[index].Name == bundle.EvidenceFileName {
				current.Manifest.Files[index].Count = int64(len(current.Evidence))
			}
		}
	}
	current = refreshIncrementalDigest(t, current)
	if err := current.Validate(); err != nil {
		t.Fatalf("current localized bundle invalid: %v", err)
	}

	report, err := CompareBundles(context.Background(), previous, current)
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	if len(report.Changed) != 2 || len(report.Added) != 3 || len(report.Removed) != 0 || len(report.Reused) != 1 {
		t.Fatalf("localized delta = %#v", report)
	}
	if !hasFactKind(report.Changed, IncrementalFactObservation) || !hasFactKind(report.Changed, IncrementalFactEvidence) {
		t.Fatalf("changed facts = %#v", report.Changed)
	}
	if !hasFactKind(report.Added, IncrementalFactArtifact) || !hasFactKind(report.Added, IncrementalFactObservation) || !hasFactKind(report.Added, IncrementalFactEvidence) {
		t.Fatalf("added facts = %#v", report.Added)
	}
	textImpact := impactFor(report, IncrementalProjectionTextual)
	if !containsString(textImpact.Affected, report.Changed[0].Key) && len(textImpact.Affected) < 2 {
		t.Fatalf("textual impact = %#v", textImpact)
	}
}

func TestCompareBundlesBlocksReuseWhenEmbeddingProfileChanges(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	left := testIncrementalProfile(t, "profile-1", "model-1")
	right := testIncrementalProfile(t, "profile-2", "model-2")
	report, err := CompareBundles(context.Background(), previous, current, IncrementalOptions{
		PreviousEmbeddingProfile: &left,
		CurrentEmbeddingProfile:  &right,
	})
	if err != nil {
		t.Fatalf("CompareBundles() error = %v", err)
	}
	if !report.EmbeddingProfileConfigured || report.EmbeddingProfileCompatible {
		t.Fatalf("profile compatibility = %#v", report)
	}
	if report.WorkSaved.Embeddings != 0 {
		t.Fatalf("profile changed but embedding work was saved = %#v", report.WorkSaved)
	}
	impact := impactFor(report, IncrementalProjectionEmbedding)
	if len(impact.Affected) != 1 || len(impact.Reusable) != 0 {
		t.Fatalf("embedding impact = %#v", impact)
	}
}

func TestCompareBundlesRejectsScopeReuseAndSameSnapshot(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	current.Manifest.Organization.ID = "other-organization"
	for index := range current.Evidence {
		current.Evidence[index].OrganizationID = current.Manifest.Organization.ID
		current.Evidence[index].ID = evidence.EvidenceID(current.Evidence[index])
	}
	current = refreshIncrementalDigest(t, current)
	if _, err := CompareBundles(context.Background(), previous, current); !errors.Is(err, ErrIncrementalScopeMismatch) {
		t.Fatalf("scope error = %v, want ErrIncrementalScopeMismatch", err)
	}
	if _, err := CompareBundles(context.Background(), previous, previous); !errors.Is(err, ErrIncrementalSnapshotImmutable) {
		t.Fatalf("same snapshot error = %v, want ErrIncrementalSnapshotImmutable", err)
	}
}

func TestCompareBundlesHonorsCancellation(t *testing.T) {
	previous := pipelineTestBundle(t)
	current := incrementalSnapshotCopy(t, previous, "snapshot-2")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CompareBundles(ctx, previous, current); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled comparison error = %v, want context.Canceled", err)
	}
}

func incrementalSnapshotCopy(t *testing.T, input bundle.Bundle, snapshotID string) bundle.Bundle {
	t.Helper()
	copyOf := input
	copyOf.Manifest.Manifest.Snapshot.ID = snapshotID
	copyOf.Manifest.Snapshot.ID = snapshotID
	copyOf.Manifest.Manifest.Snapshot.SourceID = copyOf.Manifest.Source.ID
	copyOf.Manifest.Manifest.Snapshot.Revision = input.Manifest.Snapshot.Revision
	copyOf.Manifest.Manifest.Snapshot.Hash = input.Manifest.Snapshot.Hash
	copyOf.Manifest.Snapshot.SourceID = copyOf.Manifest.Source.ID
	copyOf.Artifacts = append([]contract.Artifact(nil), input.Artifacts...)
	copyOf.Contributions = append([]contract.Contribution(nil), input.Contributions...)
	copyOf.Evidence = append([]evidence.EvidenceUnit(nil), input.Evidence...)
	for index := range copyOf.Evidence {
		copyOf.Evidence[index].SnapshotID = snapshotID
		copyOf.Evidence[index].ID = evidence.EvidenceID(copyOf.Evidence[index])
	}
	return refreshIncrementalDigest(t, copyOf)
}

func refreshIncrementalDigest(t *testing.T, input bundle.Bundle) bundle.Bundle {
	t.Helper()
	result := contract.Result{Manifest: input.Manifest.Manifest, Artifacts: input.Artifacts, Contributions: input.Contributions}
	digest, err := bundle.FactualDigest(result, input.Evidence)
	if err != nil {
		t.Fatalf("refresh bundle digest: %v", err)
	}
	input.Manifest.FactualDigest = digest
	return input
}

func hasFactKind(values []FactReference, kind IncrementalFactKind) bool {
	for _, value := range values {
		if value.Kind == kind {
			return true
		}
	}
	return false
}

func impactFor(report IncrementalReport, projection IncrementalProjection) ProjectionImpact {
	for _, impact := range report.Impacts {
		if impact.Projection == projection {
			return impact
		}
	}
	return ProjectionImpact{Projection: projection}
}

func testIncrementalProfile(t *testing.T, id, model string) retrieval.EmbeddingProfile {
	t.Helper()
	// This fixture deliberately reuses the pipeline helper's canonical profile
	// shape; profile identity changes are the only variable under test.
	profile := pipelineEmbeddingOptions(t, identity.CanonicalUUID("organization", "organization-1"), nil).Profile
	profile.ID = identity.CanonicalUUID("embedding-profile", id)
	profile.Model = model
	return profile
}
