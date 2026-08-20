package persistence

import (
	"context"

	"github.com/pedrogpaulino/manu/internal/bundle"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

// IngestionCanonicalPersister adapts the canonical repository to the
// consumer-side ingestion port. Keeping this translation here prevents the
// ingestion orchestrator from depending on PostgreSQL DTOs while preserving
// the canonical IDs needed by rebuildable projections.
type IngestionCanonicalPersister struct {
	repository *Repository
}

// NewIngestionCanonicalPersister creates a canonical persistence adapter. It
// does not open a connection or perform I/O; the repository owns that
// lifecycle.
func NewIngestionCanonicalPersister(repository *Repository) *IngestionCanonicalPersister {
	return &IngestionCanonicalPersister{repository: repository}
}

// Persist stores one validated bundle through the repository and exposes only
// the IDs needed by the ingestion projections.
func (p *IngestionCanonicalPersister) Persist(ctx context.Context, input bundle.Bundle) (ingestion.CanonicalPersistenceResult, error) {
	if p == nil || p.repository == nil {
		return ingestion.CanonicalPersistenceResult{}, ErrInvalidInput
	}
	result, err := p.repository.PersistBundle(ctx, input)
	if err != nil {
		return ingestion.CanonicalPersistenceResult{}, err
	}
	return ingestion.CanonicalPersistenceResult{
		OrganizationID: result.OrganizationID,
		SourceID:       result.SourceID,
		SnapshotID:     result.SnapshotID,
		ArtifactIDs:    result.ArtifactIDs,
		ObservationIDs: result.ObservationIDs,
		EvidenceIDs:    result.EvidenceIDs,
	}, nil
}

// PersistIncremental adapts the repository's snapshot/report result to the
// consumer-side localized-update port. The adapter exposes only canonical
// IDs; SQL and provider diagnostics never cross into ingestion.
func (p *IngestionCanonicalPersister) PersistIncremental(ctx context.Context, previous, current bundle.Bundle, options ...ingestion.IncrementalOptions) (ingestion.CanonicalPersistenceResult, ingestion.IncrementalReport, error) {
	if p == nil || p.repository == nil {
		return ingestion.CanonicalPersistenceResult{}, ingestion.IncrementalReport{}, ErrInvalidInput
	}
	result, report, err := p.repository.PersistBundleIncremental(ctx, previous, current, options...)
	if err != nil {
		return ingestion.CanonicalPersistenceResult{}, ingestion.IncrementalReport{}, err
	}
	return ingestion.CanonicalPersistenceResult{
		OrganizationID: result.OrganizationID,
		SourceID:       result.SourceID,
		SnapshotID:     result.SnapshotID,
		ArtifactIDs:    result.ArtifactIDs,
		ObservationIDs: result.ObservationIDs,
		EvidenceIDs:    result.EvidenceIDs,
	}, report, nil
}

var _ ingestion.CanonicalPersister = (*IngestionCanonicalPersister)(nil)
var _ ingestion.IncrementalCanonicalPersister = (*IngestionCanonicalPersister)(nil)
