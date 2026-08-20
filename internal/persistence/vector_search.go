package persistence

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/pedrogpaulino/manu/internal/retrieval"
	"github.com/pgvector/pgvector-go"
)

// searchExactEmbeddingSQL intentionally uses the exact pgvector cosine
// operator. There is no approximate index, planner hint, or SQL fragment
// assembled from caller input in this first projection.
const searchExactEmbeddingSQL = `
SELECT ei.evidence_id::text, ei.organization_id::text, ei.source_id::text,
       ei.snapshot_id::text, ei.profile_id::text, ep.provider, ep.model,
       ei.profile_dimension, ei.evidence_content_hash,
       ei.embedding <=> $6::vector AS cosine_distance
FROM embedding_items AS ei
JOIN embedding_profiles AS ep
  ON ep.organization_id = ei.organization_id
 AND ep.id = ei.profile_id
 AND ep.dimension = ei.profile_dimension
WHERE ei.organization_id = $1::uuid
  AND ei.source_id = $2::uuid
  AND ei.snapshot_id = $3::uuid
  AND ei.profile_id = $4::uuid
  AND ei.profile_dimension = $5
  AND ei.state = 'ready'
ORDER BY cosine_distance ASC, ei.evidence_id ASC
LIMIT $7`

// Search implements exact cosine retrieval over the rebuildable embedding
// projection. The canonical Evidence Unit remains the source of truth; this
// adapter returns only bounded identity/provenance and ranking metadata.
func (s *EmbeddingProjectionStore) Search(ctx context.Context, query retrieval.VectorSearchQuery) (retrieval.VectorSearchResponse, error) {
	if err := validateEmbeddingStoreContext(ctx); err != nil {
		return retrieval.VectorSearchResponse{}, err
	}
	if s == nil || s.db == nil {
		return retrieval.VectorSearchResponse{}, fmt.Errorf("%w: embedding projection store is not configured", ErrInvalidInput)
	}
	normalized, err := query.Normalize()
	if err != nil {
		return retrieval.VectorSearchResponse{}, err
	}
	started := time.Now()
	rows, err := s.db.Query(ctx, searchExactEmbeddingSQL,
		normalized.OrganizationID,
		normalized.SourceID,
		normalized.SnapshotID,
		normalized.Profile.ID,
		normalized.Profile.Dimension,
		pgvector.NewVector(normalized.Vector),
		normalized.Limit,
	)
	if err != nil {
		return retrieval.VectorSearchResponse{}, normalizeEmbeddingProjectionError(ctx, "search exact embedding projection", err)
	}
	if rows == nil {
		return retrieval.VectorSearchResponse{}, fmt.Errorf("%w: exact embedding search rows are nil", ErrInconsistent)
	}
	defer rows.Close()

	hits := make([]retrieval.VectorHit, 0, normalized.Limit)
	for rows.Next() {
		var hit retrieval.VectorHit
		var profileID, provider, model, hash string
		var dimension int
		var distance float64
		if err := rows.Scan(
			&hit.EvidenceID, &hit.OrganizationID, &hit.SourceID, &hit.SnapshotID,
			&profileID, &provider, &model, &dimension, &hash, &distance,
		); err != nil {
			return retrieval.VectorSearchResponse{}, normalizeEmbeddingProjectionError(ctx, "scan exact embedding search result", err)
		}
		hit.ProfileID = profileID
		hit.Provider = provider
		hit.Model = model
		hit.ProfileDimension = dimension
		hit.EvidenceContentHash = hash
		hit.Distance = distance
		hit.Profile = normalized.Profile
		normalizedHit, err := hit.Normalize(normalized)
		if err != nil {
			// This is a safe, bounded diagnostic from row metadata; it never
			// includes vector values or database driver details.
			return retrieval.VectorSearchResponse{}, err
		}
		hits = append(hits, normalizedHit)
	}
	if err := rows.Err(); err != nil {
		return retrieval.VectorSearchResponse{}, normalizeEmbeddingProjectionError(ctx, "read exact embedding search results", err)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Distance != hits[j].Distance {
			return hits[i].Distance < hits[j].Distance
		}
		return hits[i].EvidenceID < hits[j].EvidenceID
	})
	for index := range hits {
		hits[index].Rank = index + 1
	}
	return retrieval.VectorSearchResponse{
		Hits: hits,
		Telemetry: retrieval.VectorSearchTelemetry{
			Latency:        time.Since(started),
			ResultCount:    len(hits),
			RequestedLimit: normalized.Limit,
			ProfileID:      normalized.Profile.ID,
		},
	}, nil
}

// ExactSearch is an operation-named alias for Search. Keeping it on the same
// adapter prevents a caller from accidentally selecting an approximate
// projection before one is explicitly designed and benchmarked.
func (s *EmbeddingProjectionStore) ExactSearch(ctx context.Context, query retrieval.VectorSearchQuery) (retrieval.VectorSearchResponse, error) {
	return s.Search(ctx, query)
}

var _ retrieval.VectorSearchStore = (*EmbeddingProjectionStore)(nil)
