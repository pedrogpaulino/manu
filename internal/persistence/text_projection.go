package persistence

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// TextProjectionStore persists only the rebuildable textual projection. The
// canonical Evidence Unit remains the source of truth and is never replaced
// by this adapter.
type TextProjectionStore struct {
	db textProjectionDatabase
}

type textProjectionDatabase interface {
	Begin(context.Context) (textProjectionTransaction, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type textProjectionTransaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

type poolTextProjectionDatabase struct {
	pool *pgxpool.Pool
}

func (d poolTextProjectionDatabase) Begin(ctx context.Context) (textProjectionTransaction, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("%w: text projection database is not configured", ErrInvalidInput)
	}
	return d.pool.Begin(ctx)
}

func (d poolTextProjectionDatabase) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("%w: text projection database is not configured", ErrInvalidInput)
	}
	return d.pool.Query(ctx, query, args...)
}

// NewTextProjectionStore creates a PostgreSQL adapter without opening or
// probing the supplied pool.
func NewTextProjectionStore(pool *pgxpool.Pool) *TextProjectionStore {
	if pool == nil {
		return &TextProjectionStore{}
	}
	return &TextProjectionStore{db: poolTextProjectionDatabase{pool: pool}}
}

func newTextProjectionStore(db textProjectionDatabase) *TextProjectionStore {
	return &TextProjectionStore{db: db}
}

var _ retrieval.TextStore = (*TextProjectionStore)(nil)
var _ retrieval.IncrementalTextStore = (*TextProjectionStore)(nil)

const deleteTextProjectionSQL = `
DELETE FROM textual_evidence_projection
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3`

const insertTextProjectionSQL = `
INSERT INTO textual_evidence_projection (
    evidence_id, organization_id, source_id, snapshot_id, projection_kind,
    content_state, content, content_hash, content_characters, truncated,
    classification, persist_decision, symbol_name, symbol_qualified_name,
    configuration_key, exception_type, exact_terms, search_configuration
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
ON CONFLICT (evidence_id) DO UPDATE SET
    organization_id = EXCLUDED.organization_id,
    source_id = EXCLUDED.source_id,
    snapshot_id = EXCLUDED.snapshot_id,
    projection_kind = EXCLUDED.projection_kind,
    content_state = EXCLUDED.content_state,
    content = EXCLUDED.content,
    content_hash = EXCLUDED.content_hash,
    content_characters = EXCLUDED.content_characters,
    truncated = EXCLUDED.truncated,
    classification = EXCLUDED.classification,
    persist_decision = EXCLUDED.persist_decision,
    symbol_name = EXCLUDED.symbol_name,
    symbol_qualified_name = EXCLUDED.symbol_qualified_name,
    configuration_key = EXCLUDED.configuration_key,
    exception_type = EXCLUDED.exception_type,
    exact_terms = EXCLUDED.exact_terms,
    search_configuration = EXCLUDED.search_configuration,
    updated_at = CURRENT_TIMESTAMP
WHERE textual_evidence_projection.organization_id = EXCLUDED.organization_id
  AND textual_evidence_projection.source_id = EXCLUDED.source_id
  AND textual_evidence_projection.snapshot_id = EXCLUDED.snapshot_id`

// copyTextProjectionSQL copies one compatible row from the previous
// immutable snapshot into the new snapshot. The current evidence identity is
// supplied explicitly because canonical evidence IDs are snapshot-scoped.
// Removed rows never reach this statement: callers pass only current entries.
const copyTextProjectionSQL = `
INSERT INTO textual_evidence_projection (
    evidence_id, organization_id, source_id, snapshot_id, projection_kind,
    content_state, content, content_hash, content_characters, truncated,
    classification, persist_decision, symbol_name, symbol_qualified_name,
    configuration_key, exception_type, exact_terms, search_configuration
)
SELECT $1::uuid, organization_id, source_id, $4::uuid, projection_kind,
       content_state, content, content_hash, content_characters, truncated,
       classification, persist_decision, symbol_name, symbol_qualified_name,
       configuration_key, exception_type, exact_terms, search_configuration
FROM textual_evidence_projection
WHERE organization_id = $2::uuid
  AND source_id = $3::uuid
  AND snapshot_id = $5::uuid
  AND evidence_id = $6::uuid
  AND content_hash = $7
  AND projection_kind = $8
  AND content_state = $9
  AND classification = $10
  AND persist_decision = $11
  AND symbol_name = $12
  AND symbol_qualified_name = $13
  AND configuration_key = $14
  AND exception_type = $15
  AND exact_terms = $16::text[]
  AND truncated = $17`

const searchTextProjectionSQL = `
SELECT evidence_id::text, organization_id::text, source_id::text, snapshot_id::text,
       projection_kind, content_state, content, content_hash, truncated,
       classification, symbol_name, symbol_qualified_name, configuration_key,
       exception_type, exact_terms,
       ts_rank_cd(search_vector, websearch_to_tsquery('simple', $4))::double precision,
       (exact_terms && $5::text[])
FROM textual_evidence_projection
WHERE organization_id = $1::uuid
  AND ($2::text = '' OR source_id = NULLIF($2::text, '')::uuid)
  AND ($3::text = '' OR snapshot_id = NULLIF($3::text, '')::uuid)
  AND (
      search_vector @@ websearch_to_tsquery('simple', $4)
      OR exact_terms && $5::text[]
  )
ORDER BY (exact_terms && $5::text[]) DESC,
         ts_rank_cd(search_vector, websearch_to_tsquery('simple', $4)) DESC,
         evidence_id ASC
LIMIT $6`

func (s *TextProjectionStore) RebuildSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string, entries []retrieval.TextEntry) error {
	if err := validateTextStoreContext(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: text projection store is not configured", ErrInvalidInput)
	}
	for name, value := range map[string]string{
		"organization_id": organizationID, "source_id": sourceID, "snapshot_id": snapshotID,
	} {
		if err := validateUUID(name, value); err != nil {
			return err
		}
	}
	prepared, err := preparePersistedTextEntries(organizationID, sourceID, snapshotID, entries)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return normalizeTextProjectionError(ctx, "begin textual rebuild", err)
	}
	if tx == nil {
		return fmt.Errorf("%w: textual rebuild transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackTextProjection(ctx, tx)
		}
	}()

	if _, err := tx.Exec(ctx, deleteTextProjectionSQL, organizationID, sourceID, snapshotID); err != nil {
		return normalizeTextProjectionError(ctx, "clear textual snapshot projection", err)
	}
	for _, entry := range prepared {
		tag, err := tx.Exec(ctx, insertTextProjectionSQL,
			entry.EvidenceID, entry.OrganizationID, entry.SourceID, entry.SnapshotID,
			entry.ProjectionKind, string(entry.ContentState), entry.Content, entry.ContentHash,
			int64(utf8.RuneCountInString(entry.Content)), entry.Truncated,
			string(entry.Classification), string(entry.Persist), entry.SymbolName,
			entry.SymbolQualifiedName, entry.ConfigurationKey, entry.ExceptionType,
			entry.ExactTerms, "simple",
		)
		if err != nil {
			return normalizeTextProjectionError(ctx, "write textual evidence projection", err)
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("%w: textual evidence identity conflict", ErrConflict)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return normalizeTextProjectionError(ctx, "commit textual rebuild", err)
	}
	committed = true
	return nil
}

// RebuildSnapshotIncremental materializes a new snapshot from the comparison
// result. Compatible rows are copied from the previous snapshot; affected
// rows are reconstructed from current bounded TextEntry values. Only the new
// snapshot is cleared, so history remains queryable and removals are absent.
func (s *TextProjectionStore) RebuildSnapshotIncremental(ctx context.Context, organizationID, sourceID, previousSnapshotID, snapshotID string, entries []retrieval.IncrementalTextEntry) error {
	if err := validateTextStoreContext(ctx); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("%w: text projection store is not configured", ErrInvalidInput)
	}
	for name, value := range map[string]string{
		"organization_id": organizationID, "source_id": sourceID,
		"previous_snapshot_id": previousSnapshotID, "snapshot_id": snapshotID,
	} {
		if err := validateUUID(name, value); err != nil {
			return err
		}
	}
	if previousSnapshotID == snapshotID {
		return fmt.Errorf("%w: incremental snapshots must differ", ErrInvalidInput)
	}
	prepared, err := preparePersistedIncrementalTextEntries(organizationID, sourceID, snapshotID, entries)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return normalizeTextProjectionError(ctx, "begin incremental textual rebuild", err)
	}
	if tx == nil {
		return fmt.Errorf("%w: incremental textual rebuild transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackTextProjection(ctx, tx)
		}
	}()
	if _, err := tx.Exec(ctx, deleteTextProjectionSQL, organizationID, sourceID, snapshotID); err != nil {
		return normalizeTextProjectionError(ctx, "clear incremental textual snapshot projection", err)
	}
	for _, candidate := range prepared {
		if err := ctx.Err(); err != nil {
			return err
		}
		copied := false
		if candidate.Reuse {
			tag, copyErr := tx.Exec(ctx, copyTextProjectionSQL,
				candidate.Entry.EvidenceID, organizationID, sourceID, snapshotID,
				previousSnapshotID, candidate.PreviousEvidenceID, candidate.Entry.ContentHash,
				candidate.Entry.ProjectionKind, string(candidate.Entry.ContentState),
				string(candidate.Entry.Classification), string(candidate.Entry.Persist),
				candidate.Entry.SymbolName, candidate.Entry.SymbolQualifiedName,
				candidate.Entry.ConfigurationKey, candidate.Entry.ExceptionType,
				candidate.Entry.ExactTerms, candidate.Entry.Truncated,
			)
			if copyErr != nil {
				return normalizeTextProjectionError(ctx, "copy textual evidence projection", copyErr)
			}
			if tag.RowsAffected() > 1 {
				return fmt.Errorf("%w: copied textual evidence identity is ambiguous", ErrInconsistent)
			}
			copied = tag.RowsAffected() == 1
		}
		if copied {
			continue
		}
		if err := insertPreparedTextEntry(ctx, tx, candidate.Entry); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return normalizeTextProjectionError(ctx, "commit incremental textual rebuild", err)
	}
	committed = true
	return nil
}

func preparePersistedIncrementalTextEntries(organizationID, sourceID, snapshotID string, entries []retrieval.IncrementalTextEntry) ([]retrieval.IncrementalTextEntry, error) {
	prepared := make([]retrieval.IncrementalTextEntry, 0, len(entries))
	seenKeys := make(map[string]struct{}, len(entries))
	seenEvidence := make(map[string]struct{}, len(entries))
	for _, candidate := range entries {
		if candidate.StableKey == "" {
			return nil, fmt.Errorf("%w: incremental stable key is required", ErrInvalidInput)
		}
		if _, exists := seenKeys[candidate.StableKey]; exists {
			return nil, fmt.Errorf("%w: duplicate incremental stable key", ErrConflict)
		}
		seenKeys[candidate.StableKey] = struct{}{}
		entry := candidate.Entry
		if entry.OrganizationID != organizationID || entry.SourceID != sourceID || entry.SnapshotID != snapshotID {
			return nil, fmt.Errorf("%w: incremental textual evidence scope mismatch", ErrInvalidInput)
		}
		if err := entry.Validate(); err != nil {
			return nil, fmt.Errorf("%w: incremental textual evidence is invalid", ErrInvalidInput)
		}
		if !entry.Persistible() {
			continue
		}
		normalized, err := entry.Normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: incremental textual evidence is not prepared", ErrInvalidInput)
		}
		if _, exists := seenEvidence[normalized.EvidenceID]; exists {
			return nil, fmt.Errorf("%w: duplicate incremental textual evidence identity", ErrConflict)
		}
		if candidate.Reuse {
			if err := validateUUID("previous evidence id", candidate.PreviousEvidenceID); err != nil {
				return nil, err
			}
		}
		candidate.Entry = normalized
		prepared = append(prepared, candidate)
		seenEvidence[normalized.EvidenceID] = struct{}{}
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].Entry.EvidenceID < prepared[j].Entry.EvidenceID })
	return prepared, nil
}

func insertPreparedTextEntry(ctx context.Context, tx textProjectionTransaction, entry retrieval.TextEntry) error {
	tag, err := tx.Exec(ctx, insertTextProjectionSQL,
		entry.EvidenceID, entry.OrganizationID, entry.SourceID, entry.SnapshotID,
		entry.ProjectionKind, string(entry.ContentState), entry.Content, entry.ContentHash,
		int64(utf8.RuneCountInString(entry.Content)), entry.Truncated,
		string(entry.Classification), string(entry.Persist), entry.SymbolName,
		entry.SymbolQualifiedName, entry.ConfigurationKey, entry.ExceptionType,
		entry.ExactTerms, "simple",
	)
	if err != nil {
		return normalizeTextProjectionError(ctx, "write incremental textual evidence projection", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: incremental textual evidence identity conflict", ErrConflict)
	}
	return nil
}

func preparePersistedTextEntries(organizationID, sourceID, snapshotID string, entries []retrieval.TextEntry) ([]retrieval.TextEntry, error) {
	prepared := make([]retrieval.TextEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.OrganizationID != organizationID || entry.SourceID != sourceID || entry.SnapshotID != snapshotID {
			return nil, fmt.Errorf("%w: textual evidence scope mismatch", ErrInvalidInput)
		}
		if err := entry.Validate(); err != nil {
			return nil, fmt.Errorf("%w: textual evidence is invalid", ErrInvalidInput)
		}
		if !entry.Persistible() {
			continue
		}
		normalized, err := entry.Normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: textual evidence is not prepared", ErrInvalidInput)
		}
		if _, exists := seen[normalized.EvidenceID]; exists {
			return nil, fmt.Errorf("%w: duplicate textual evidence identity", ErrConflict)
		}
		seen[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	// TextProjection already sorts entries. Keeping this second boundary
	// deterministic protects direct adapter callers as well without making
	// rebuild cost quadratic for a large snapshot.
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].EvidenceID < prepared[j].EvidenceID })
	return prepared, nil
}

func (s *TextProjectionStore) Search(ctx context.Context, options retrieval.SearchOptions) ([]retrieval.TextHit, error) {
	if err := validateTextStoreContext(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("%w: text projection store is not configured", ErrInvalidInput)
	}
	normalized, err := options.Normalize()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, searchTextProjectionSQL,
		normalized.OrganizationID, normalized.SourceID, normalized.SnapshotID,
		normalized.Query, retrieval.ExactTermsForQuery(normalized.Query), normalized.Limit,
	)
	if err != nil {
		return nil, normalizeTextProjectionError(ctx, "search textual projection", err)
	}
	if rows == nil {
		return nil, fmt.Errorf("%w: textual projection rows are nil", ErrInconsistent)
	}
	defer rows.Close()

	hits := make([]retrieval.TextHit, 0)
	for rows.Next() {
		var hit retrieval.TextHit
		var contentState, classification string
		if err := rows.Scan(
			&hit.EvidenceID, &hit.OrganizationID, &hit.SourceID, &hit.SnapshotID,
			&hit.ProjectionKind, &contentState, &hit.Content, &hit.ContentHash,
			&hit.Truncated, &classification, &hit.SymbolName, &hit.SymbolQualifiedName,
			&hit.ConfigurationKey, &hit.ExceptionType, &hit.ExactTerms, &hit.Rank,
			&hit.ExactMatch,
		); err != nil {
			return nil, normalizeTextProjectionError(ctx, "scan textual projection result", err)
		}
		hit.ContentState = evidence.ContentState(contentState)
		hit.Classification = evidence.Classification(classification)
		hits = append(hits, hit)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeTextProjectionError(ctx, "read textual projection results", err)
	}
	return hits, nil
}

func validateTextStoreContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidInput)
	}
	return ctx.Err()
}

func rollbackTextProjection(ctx context.Context, tx textProjectionTransaction) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}

func normalizeTextProjectionError(ctx context.Context, operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: %w", operation, ctxErr)
		}
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: %s", ErrConflict, operation)
		case "23503":
			return fmt.Errorf("%w: %s", ErrNotFound, operation)
		default:
			return fmt.Errorf("%w: %s", ErrDatabase, operation)
		}
	}
	return fmt.Errorf("%w: %s", ErrDatabase, operation)
}
