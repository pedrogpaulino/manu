package persistence

import (
	"context"
	"fmt"

	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/ingestion"
)

// IngestionEmbeddingEvidenceSource reads the already-persisted canonical
// Evidence Unit boundary needed by a partial embedding retry. It returns
// canonical UUIDs and stored policy/content hashes directly; it never reads
// the original bundle or a source filesystem.
type IngestionEmbeddingEvidenceSource struct {
	repository *Repository
}

// NewIngestionEmbeddingEvidenceSource composes a canonical repository with
// the consumer-side ingestion source port. Construction does not contact the
// database.
func NewIngestionEmbeddingEvidenceSource(repository *Repository) *IngestionEmbeddingEvidenceSource {
	return &IngestionEmbeddingEvidenceSource{repository: repository}
}

var _ ingestion.EmbeddingEvidenceSource = (*IngestionEmbeddingEvidenceSource)(nil)

const listEmbeddingEvidenceSQL = `
SELECT id::text, content, content_hash, content_state, external_transfer_decision
FROM evidence_units
WHERE organization_id = $1::uuid AND source_id = $2::uuid AND snapshot_id = $3::uuid
ORDER BY id`

func (s *IngestionEmbeddingEvidenceSource) ListEmbeddingEvidence(ctx context.Context, organizationID, sourceID, snapshotID string) ([]ingestion.EmbeddingEvidence, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	for name, value := range map[string]string{
		"organization_id": organizationID, "source_id": sourceID, "snapshot_id": snapshotID,
	} {
		if err := validateUUID(name, value); err != nil {
			return nil, fmt.Errorf("%w: embedding evidence scope is invalid", ErrInvalidInput)
		}
	}
	if s == nil || s.repository == nil || s.repository.starter == nil {
		return nil, fmt.Errorf("%w: canonical repository is not configured", ErrInvalidInput)
	}
	tx, err := s.repository.starter.Begin(ctx)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "begin embedding evidence read", err)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()
	rows, err := tx.Query(ctx, listEmbeddingEvidenceSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read embedding evidence", err)
	}
	if rows == nil {
		return nil, fmt.Errorf("%w: embedding evidence rows are nil", ErrInconsistent)
	}
	defer rows.Close()
	units := make([]ingestion.EmbeddingEvidence, 0)
	for rows.Next() {
		var (
			id, contentHash, contentState, transfer string
			content                                 *string
		)
		if err := rows.Scan(&id, &content, &contentHash, &contentState, &transfer); err != nil {
			return nil, wrapPersistenceError(ctx, "scan embedding evidence", err)
		}
		value := ""
		if content != nil {
			value = *content
		}
		decision := evidence.Decision(transfer)
		if contentState != string(evidence.ContentStatePresent) {
			// Omitted/redacted rows remain visible to the local installation but
			// are not eligible for an embedding request.
			decision = evidence.DecisionDeny
		}
		units = append(units, ingestion.EmbeddingEvidence{
			ID: id, Content: value, ContentHash: contentHash, ExternalTransfer: decision,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read embedding evidence", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapPersistenceError(ctx, "commit embedding evidence read", err)
	}
	committed = true
	return units, nil
}
