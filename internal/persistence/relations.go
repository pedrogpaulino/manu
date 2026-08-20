package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// RelationProjectionStore reads the rebuildable relational projection directly
// from canonical relationships. It does not create a second source of truth
// or retain a graph cache; rebuilding is therefore a repeatable read of the
// organization/source/snapshot-scoped canonical rows.
type RelationProjectionStore struct {
	db relationProjectionDatabase
}

type relationProjectionDatabase interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type poolRelationProjectionDatabase struct {
	pool *pgxpool.Pool
}

func (d poolRelationProjectionDatabase) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("%w: relation projection database is not configured", ErrInvalidInput)
	}
	return d.pool.Query(ctx, query, args...)
}

// NewRelationProjectionStore creates a SQL-backed relation reader without
// opening or probing the supplied pool.
func NewRelationProjectionStore(pool *pgxpool.Pool) *RelationProjectionStore {
	return &RelationProjectionStore{db: poolRelationProjectionDatabase{pool: pool}}
}

func newRelationProjectionStore(db relationProjectionDatabase) *RelationProjectionStore {
	return &RelationProjectionStore{db: db}
}

var _ retrieval.RelationStore = (*RelationProjectionStore)(nil)

// expandRelationsSQL keeps direction, anchor, scope, and the result limit as
// values. No identifier, direction, or limit is interpolated into SQL.
const expandRelationsSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       external_id, from_entity_id::text, to_entity_id::text,
       relationship_type, attributes
FROM relationships
WHERE organization_id = $1::uuid
  AND source_id = $2::uuid
  AND snapshot_id = $3::uuid
  AND (
      ($4::text = 'outbound' AND from_entity_id = $5::uuid)
      OR ($4::text = 'inbound' AND to_entity_id = $5::uuid)
      OR ($4::text = 'both' AND (from_entity_id = $5::uuid OR to_entity_id = $5::uuid))
  )
ORDER BY from_entity_id ASC, to_entity_id ASC, relationship_type ASC, id ASC
LIMIT $6`

// Expand reads only the requested one-hop scope. The retrieval boundary
// validates the returned provenance and applies the final deterministic
// limit again in case an adapter is broader than this implementation.
func (s *RelationProjectionStore) Expand(ctx context.Context, query retrieval.RelationQuery) ([]retrieval.RelationHit, error) {
	if err := validateRelationStoreContext(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: relation projection store is not configured", ErrInvalidInput)
	}
	normalized, err := query.Normalize()
	if err != nil {
		return nil, fmt.Errorf("%w: relation query: %w", ErrInvalidInput, err)
	}
	if normalized.MaxHops == 0 {
		return []retrieval.RelationHit{}, nil
	}
	rows, err := s.db.Query(ctx, expandRelationsSQL,
		normalized.OrganizationID, normalized.SourceID, normalized.SnapshotID,
		string(normalized.Direction), normalized.AnchorEntityID, normalized.FanOut,
	)
	if err != nil {
		return nil, normalizeRelationProjectionError(ctx, "expand relation projection", err)
	}
	defer rows.Close()

	hits := make([]retrieval.RelationHit, 0, normalized.FanOut)
	for rows.Next() {
		var hit retrieval.RelationHit
		var attributes []byte
		if err := rows.Scan(
			&hit.RelationID, &hit.OrganizationID, &hit.SourceID, &hit.SnapshotID,
			&hit.RelationExternalID, &hit.FromEntityID, &hit.ToEntityID,
			&hit.RelationType, &attributes,
		); err != nil {
			return nil, normalizeRelationProjectionError(ctx, "scan relation projection result", err)
		}
		hit.Attributes = append([]byte(nil), attributes...)
		hit.Hops = 1
		hit.Provenance = retrieval.RelationProvenance{
			OrganizationID:     hit.OrganizationID,
			SourceID:           hit.SourceID,
			SnapshotID:         hit.SnapshotID,
			RelationID:         hit.RelationID,
			RelationExternalID: hit.RelationExternalID,
			FromEntityID:       hit.FromEntityID,
			ToEntityID:         hit.ToEntityID,
		}
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeRelationProjectionError(ctx, "read relation projection results", err)
	}
	return hits, nil
}

func validateRelationStoreContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: relation projection context is nil", ErrInvalidInput)
	}
	return ctx.Err()
}

func normalizeRelationProjectionError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return fmt.Errorf("%w: %s", ErrNotFound, operation)
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, operation)
		default:
			return fmt.Errorf("%w: %s", ErrDatabase, operation)
		}
	}
	return fmt.Errorf("%w: %s", ErrDatabase, operation)
}
