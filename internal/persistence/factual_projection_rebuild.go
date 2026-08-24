package persistence

import (
	"context"
	"fmt"
)

const (
	deleteFactualRelationshipsSQL = `
DELETE FROM relationships
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3`
	deleteFactualEntitiesSQL = `
DELETE FROM entities
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3`
)

// FactualProjectionRebuildResult reports the replacement rows written for one
// snapshot. Entities and relationships are disposable projections; canonical
// facts, qualifiers, evidence links, and lineage inputs are never deleted by
// this operation.
type FactualProjectionRebuildResult struct {
	EntityCount       int
	RelationshipCount int
}

// RebuildFactualProjection replaces the entities and relationships projection
// for one snapshot from canonical facts stored in PostgreSQL. The operation
// is transactional and scoped by organization, source, and snapshot; it does
// not read source artifacts or delete canonical factual/support rows.
func (r *Repository) RebuildFactualProjection(ctx context.Context, organizationID, sourceID, snapshotID string) (FactualProjectionRebuildResult, error) {
	if err := validateContext(ctx); err != nil {
		return FactualProjectionRebuildResult{}, err
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "organization_id", value: organizationID},
		{name: "source_id", value: sourceID},
		{name: "snapshot_id", value: snapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return FactualProjectionRebuildResult{}, err
		}
	}

	var result FactualProjectionRebuildResult
	err := r.WithinTx(ctx, func(u *UnitOfWork) error {
		input, err := u.readFactualSnapshot(ctx, organizationID, sourceID, snapshotID)
		if err != nil {
			return err
		}
		prepared, err := PrepareFactualProjection(input)
		if err != nil {
			return err
		}
		if err := u.replaceFactualProjection(ctx, organizationID, sourceID, snapshotID, prepared); err != nil {
			return err
		}
		result = FactualProjectionRebuildResult{
			EntityCount:       len(prepared.Entities),
			RelationshipCount: len(prepared.Relationships),
		}
		return nil
	})
	if err != nil {
		return FactualProjectionRebuildResult{}, err
	}
	return result, nil
}

func (u *UnitOfWork) replaceFactualProjection(ctx context.Context, organizationID, sourceID, snapshotID string, prepared PreparedFactualProjection) error {
	if err := deleteFactualProjectionRows(ctx, u, deleteFactualRelationshipsSQL, "delete factual relationships", organizationID, sourceID, snapshotID); err != nil {
		return err
	}
	if err := deleteFactualProjectionRows(ctx, u, deleteFactualEntitiesSQL, "delete factual entities", organizationID, sourceID, snapshotID); err != nil {
		return err
	}
	for _, entity := range prepared.Entities {
		if err := u.InsertEntity(ctx, organizationID, entity); err != nil {
			return err
		}
	}
	for _, relationship := range prepared.Relationships {
		if err := u.InsertRelationship(ctx, organizationID, relationship); err != nil {
			return err
		}
	}
	return nil
}

func deleteFactualProjectionRows(ctx context.Context, u *UnitOfWork, query, operation, organizationID, sourceID, snapshotID string) error {
	if u == nil || u.tx == nil {
		return fmt.Errorf("%w: unit of work is not configured", ErrInvalidInput)
	}
	tag, err := u.tx.Exec(ctx, query, organizationID, sourceID, snapshotID)
	if err != nil {
		return wrapPersistenceError(ctx, operation, err)
	}
	if tag.RowsAffected() < 0 {
		return fmt.Errorf("%w: %s affected invalid row count", ErrInconsistent, operation)
	}
	return nil
}
