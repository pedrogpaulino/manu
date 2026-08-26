package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/query"
)

const readContextSnapshotRevisionSQL = `
SELECT COALESCE(NULLIF(btrim(source_revision), ''), btrim(source_hash))
FROM analysis_snapshots
WHERE organization_id=$1::uuid AND source_id=$2::uuid AND id=$3::uuid`

var _ query.ContextSnapshotReader = (*Repository)(nil)

// ReadContextSnapshot combines append-only snapshot and factual rows. Because
// ingestion is transactional, the two reads cannot mix the requested
// snapshot's revision with facts from another snapshot.
func (r *Repository) ReadContextSnapshot(ctx context.Context, scope query.Scope) (query.ContextSnapshot, error) {
	if err := validateContext(ctx); err != nil {
		return query.ContextSnapshot{}, err
	}
	if err := scope.Validate(); err != nil {
		return query.ContextSnapshot{}, query.ErrInvalidContextScope
	}

	factual, err := r.ReadFactualSnapshot(ctx, scope.OrganizationID, scope.SourceID, scope.SnapshotID)
	if err != nil {
		return query.ContextSnapshot{}, err
	}

	var revision string
	if err := r.WithinTx(ctx, func(u *UnitOfWork) error {
		err := u.tx.QueryRow(ctx, readContextSnapshotRevisionSQL,
			scope.OrganizationID, scope.SourceID, scope.SnapshotID,
		).Scan(&revision)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return wrapPersistenceError(ctx, "read context snapshot revision", err)
		}
		return nil
	}); err != nil {
		return query.ContextSnapshot{}, err
	}

	snapshot := query.ContextSnapshot{
		Scope:    scope,
		Revision: revision,
		Facts:    factual.Facts,
	}
	if err := snapshot.Validate(); err != nil {
		return query.ContextSnapshot{}, fmt.Errorf("%w: context snapshot", ErrInconsistent)
	}
	return snapshot.Clone(), nil
}
