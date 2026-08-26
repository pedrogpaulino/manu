package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/query"
)

const (
	readContextSnapshotRevisionSQL = `
SELECT COALESCE(NULLIF(btrim(source_revision), ''), btrim(source_hash))
FROM analysis_snapshots
WHERE organization_id=$1::uuid AND source_id=$2::uuid AND id=$3::uuid`

	readContextSnapshotCoverageSQL = `
SELECT coverage.external_id, coverage.dimension, coverage.scope, coverage.state,
       coverage.analyzer_id, coverage.message, coverage.locator, artifact.id::text
FROM analysis_coverage AS coverage
LEFT JOIN artifacts AS artifact
  ON artifact.organization_id = coverage.organization_id
 AND artifact.source_id = coverage.source_id
 AND artifact.snapshot_id = coverage.snapshot_id
 AND artifact.external_id = coverage.locator->>'artifact_id'
WHERE coverage.organization_id=$1::uuid
  AND coverage.source_id=$2::uuid
  AND coverage.snapshot_id=$3::uuid
ORDER BY coverage.external_id, coverage.id`

	readContextSnapshotGapsSQL = `
SELECT gap.external_id, gap.code, gap.dimension, gap.scope,
       gap.message, gap.analyzer_id, gap.locator, artifact.id::text
FROM explicit_gaps AS gap
LEFT JOIN artifacts AS artifact
  ON artifact.organization_id = gap.organization_id
 AND artifact.source_id = gap.source_id
 AND artifact.snapshot_id = gap.snapshot_id
 AND artifact.external_id = gap.locator->>'artifact_id'
WHERE gap.organization_id=$1::uuid
  AND gap.source_id=$2::uuid
  AND gap.snapshot_id=$3::uuid
ORDER BY gap.external_id, gap.id`
)

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

	var (
		revision string
		coverage []contract.Coverage
		gaps     []contract.Gap
	)
	if err := r.WithinTx(ctx, func(u *UnitOfWork) error {
		var revisionText string
		err := u.tx.QueryRow(ctx, readContextSnapshotRevisionSQL,
			scope.OrganizationID, scope.SourceID, scope.SnapshotID,
		).Scan(&revisionText)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return wrapPersistenceError(ctx, "read context snapshot revision", err)
		}
		revision = revisionText

		coverage, err = u.readContextCoverage(ctx, scope, factual.Scope.SourceID)
		if err != nil {
			return err
		}
		gaps, err = u.readContextGaps(ctx, scope, factual.Scope.SourceID)
		if err != nil {
			return err
		}
		return nil
	}); err != nil {
		return query.ContextSnapshot{}, err
	}

	snapshot := query.ContextSnapshot{
		Scope:    scope,
		Revision: revision,
		Facts:    factual.Facts,
		Coverage: coverage,
		Gaps:     gaps,
	}
	if err := snapshot.Validate(); err != nil {
		return query.ContextSnapshot{}, fmt.Errorf("%w: context snapshot", ErrInconsistent)
	}
	return snapshot.Clone(), nil
}

func (u *UnitOfWork) readContextCoverage(ctx context.Context, scope query.Scope, factualSourceID string) ([]contract.Coverage, error) {
	rows, err := u.tx.Query(ctx, readContextSnapshotCoverageSQL,
		scope.OrganizationID, scope.SourceID, scope.SnapshotID,
	)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read context snapshot coverage", err)
	}
	defer rows.Close()

	result := make([]contract.Coverage, 0)
	for rows.Next() {
		var (
			externalID, dimension, state     string
			storedScope, analyzerID, message *string
			locatorJSON                      []byte
			artifactID                       *string
		)
		if err := rows.Scan(&externalID, &dimension, &storedScope, &state,
			&analyzerID, &message, &locatorJSON, &artifactID); err != nil {
			return nil, contextSnapshotInconsistent("coverage")
		}

		locator, err := projectContextSnapshotLocator(locatorJSON, artifactID, factualSourceID, scope.SourceID)
		if err != nil {
			return nil, contextSnapshotInconsistent("coverage locator")
		}
		value := contract.Coverage{
			ID:         externalID,
			Dimension:  dimension,
			Scope:      optionalString(storedScope),
			State:      contract.CoverageState(state),
			AnalyzerID: optionalString(analyzerID),
			Message:    optionalString(message),
			Locator:    locator,
		}
		if err := value.Validate(); err != nil {
			return nil, contextSnapshotInconsistent("coverage")
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read context snapshot coverage", err)
	}
	return result, nil
}

func (u *UnitOfWork) readContextGaps(ctx context.Context, scope query.Scope, factualSourceID string) ([]contract.Gap, error) {
	rows, err := u.tx.Query(ctx, readContextSnapshotGapsSQL,
		scope.OrganizationID, scope.SourceID, scope.SnapshotID,
	)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read context snapshot gaps", err)
	}
	defer rows.Close()

	result := make([]contract.Gap, 0)
	for rows.Next() {
		var (
			externalID, code                            string
			dimension, storedScope, message, analyzerID *string
			locatorJSON                                 []byte
			artifactID                                  *string
		)
		if err := rows.Scan(&externalID, &code, &dimension, &storedScope,
			&message, &analyzerID, &locatorJSON, &artifactID); err != nil {
			return nil, contextSnapshotInconsistent("gaps")
		}

		locator, err := projectContextSnapshotLocator(locatorJSON, artifactID, factualSourceID, scope.SourceID)
		if err != nil {
			return nil, contextSnapshotInconsistent("gap locator")
		}
		value := contract.Gap{
			ID:         externalID,
			Code:       code,
			Dimension:  optionalString(dimension),
			Scope:      optionalString(storedScope),
			Message:    optionalString(message),
			AnalyzerID: optionalString(analyzerID),
			Locator:    locator,
		}
		if err := value.Validate(); err != nil {
			return nil, contextSnapshotInconsistent("gaps")
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read context snapshot gaps", err)
	}
	return result, nil
}

func projectContextSnapshotLocator(raw []byte, artifactID *string, factualSourceID, canonicalSourceID string) (*contract.Locator, error) {
	if len(raw) == 0 {
		if artifactID != nil {
			return nil, ErrInconsistent
		}
		return nil, nil
	}

	locator, err := decodeReadLocator(raw)
	if err != nil {
		return nil, err
	}
	if locator.SourceID != "" {
		if locator.SourceID != factualSourceID {
			return nil, ErrInconsistent
		}
		locator.SourceID = canonicalSourceID
	}
	if locator.ArtifactID != "" {
		if artifactID == nil || *artifactID == "" || validateUUID("context snapshot artifact", *artifactID) != nil {
			return nil, ErrInconsistent
		}
		locator.ArtifactID = *artifactID
	} else if artifactID != nil {
		return nil, ErrInconsistent
	}
	return &locator, nil
}

func contextSnapshotInconsistent(field string) error {
	return fmt.Errorf("%w: context snapshot %s", ErrInconsistent, field)
}
