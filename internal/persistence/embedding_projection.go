package persistence

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/retrieval"
	"github.com/pgvector/pgvector-go"
)

// EmbeddingProjectionStore persists immutable profile metadata, the
// rebuildable embedding cache, and the initial exact-search projection. It
// never owns canonical Evidence Units or an approximate vector index.
type EmbeddingProjectionStore struct {
	db embeddingProjectionDatabase
}

type embeddingProjectionDatabase interface {
	Begin(context.Context) (embeddingProjectionTransaction, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type embeddingProjectionTransaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

type poolEmbeddingProjectionDatabase struct {
	pool *pgxpool.Pool
}

func (d poolEmbeddingProjectionDatabase) Begin(ctx context.Context) (embeddingProjectionTransaction, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("%w: embedding projection database is not configured", ErrInvalidInput)
	}
	return d.pool.Begin(ctx)
}

func (d poolEmbeddingProjectionDatabase) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("%w: embedding projection database is not configured", ErrInvalidInput)
	}
	return d.pool.Query(ctx, query, args...)
}

// NewEmbeddingProjectionStore creates a PostgreSQL adapter without opening
// or probing the supplied pool.
func NewEmbeddingProjectionStore(pool *pgxpool.Pool) *EmbeddingProjectionStore {
	if pool == nil {
		return &EmbeddingProjectionStore{}
	}
	return &EmbeddingProjectionStore{db: poolEmbeddingProjectionDatabase{pool: pool}}
}

func newEmbeddingProjectionStore(db embeddingProjectionDatabase) *EmbeddingProjectionStore {
	return &EmbeddingProjectionStore{db: db}
}

var _ retrieval.EmbeddingStore = (*EmbeddingProjectionStore)(nil)
var _ retrieval.IncrementalEmbeddingStore = (*EmbeddingProjectionStore)(nil)

const insertEmbeddingProfileSQL = `
INSERT INTO embedding_profiles (
    id, organization_id, provider, model, dimension, normalization,
    configuration_version, configuration_digest, configuration
)
VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9::jsonb)
ON CONFLICT (organization_id, id) DO NOTHING`

const selectEmbeddingProfileSQL = `
SELECT id::text, organization_id::text, provider, model, dimension,
       normalization, configuration_version, configuration_digest,
       configuration
FROM embedding_profiles
WHERE organization_id = $1::uuid AND id = $2::uuid`

const lookupEmbeddingCacheSQL = `
SELECT id::text, organization_id::text, profile_id::text, profile_dimension,
       source_id::text, snapshot_id::text, evidence_id::text,
       evidence_content_hash, embedding, state
FROM embedding_items
WHERE organization_id = $1::uuid
  AND profile_id = $2::uuid
  AND evidence_content_hash = $3
  AND state = 'ready'
ORDER BY id ASC
LIMIT 1`

const selectEmbeddingCacheSQL = `
SELECT evidence_id::text, evidence_content_hash, profile_dimension, embedding
FROM embedding_items
WHERE organization_id = $1::uuid
  AND profile_id = $2::uuid
  AND evidence_content_hash = ANY($3::text[])
  AND profile_dimension = $4
  AND state = 'ready'
ORDER BY evidence_content_hash ASC, id ASC`

const deleteEmbeddingSnapshotSQL = `
DELETE FROM embedding_items
WHERE organization_id = $1::uuid
  AND profile_id = $2::uuid
  AND source_id = $3::uuid
  AND snapshot_id = $4::uuid`

const insertEmbeddingItemSQL = `
INSERT INTO embedding_items (
    id, organization_id, profile_id, profile_dimension, source_id,
    snapshot_id, evidence_id, evidence_content_hash, embedding, state
)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5::uuid, $6::uuid, $7::uuid, $8, $9, $10)`

const copyEmbeddingItemSQL = `
INSERT INTO embedding_items (
    id, organization_id, profile_id, profile_dimension, source_id,
    snapshot_id, evidence_id, evidence_content_hash, embedding, state
)
SELECT $1::uuid, organization_id, profile_id, profile_dimension, source_id,
       $6::uuid, $7::uuid, evidence_content_hash, embedding, state
FROM embedding_items
WHERE organization_id = $2::uuid
  AND profile_id = $3::uuid
  AND profile_dimension = $4
  AND source_id = $5::uuid
  AND snapshot_id = $8::uuid
  AND evidence_id = $9::uuid
  AND evidence_content_hash = $10
  AND state = 'ready'`

func (s *EmbeddingProjectionStore) EnsureProfile(ctx context.Context, profile retrieval.EmbeddingProfile) (retrieval.EmbeddingProfile, error) {
	if err := validateEmbeddingStoreContext(ctx); err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	if s == nil || s.db == nil {
		return retrieval.EmbeddingProfile{}, fmt.Errorf("%w: embedding projection store is not configured", ErrInvalidInput)
	}
	normalized, err := profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return retrieval.EmbeddingProfile{}, normalizeEmbeddingProjectionError(ctx, "begin embedding profile transaction", err)
	}
	if tx == nil {
		return retrieval.EmbeddingProfile{}, fmt.Errorf("%w: embedding profile transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackEmbeddingProjection(ctx, tx)
		}
	}()
	if _, err := tx.Exec(ctx, insertEmbeddingProfileSQL,
		normalized.ID, normalized.OrganizationID, normalized.Provider, normalized.Model,
		normalized.Dimension, normalized.Normalization, normalized.ConfigurationVersion,
		normalized.ConfigurationDigest, []byte(normalized.Configuration),
	); err != nil {
		return retrieval.EmbeddingProfile{}, normalizeEmbeddingProjectionError(ctx, "insert embedding profile", err)
	}
	existing, err := readEmbeddingProfile(ctx, tx, normalized.OrganizationID, normalized.ID)
	if err != nil {
		return retrieval.EmbeddingProfile{}, normalizeEmbeddingProjectionError(ctx, "read embedding profile", err)
	}
	if !sameEmbeddingProfile(existing, normalized) {
		return retrieval.EmbeddingProfile{}, fmt.Errorf("%w: %w: immutable embedding profile differs", ErrConflict, retrieval.ErrEmbeddingProfileConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return retrieval.EmbeddingProfile{}, normalizeEmbeddingProjectionError(ctx, "commit embedding profile", err)
	}
	committed = true
	return existing, nil
}

func (s *EmbeddingProjectionStore) Lookup(ctx context.Context, key retrieval.EmbeddingCacheKey) (retrieval.EmbeddingItem, bool, error) {
	if err := validateEmbeddingStoreContext(ctx); err != nil {
		return retrieval.EmbeddingItem{}, false, err
	}
	if s == nil || s.db == nil {
		return retrieval.EmbeddingItem{}, false, fmt.Errorf("%w: embedding projection store is not configured", ErrInvalidInput)
	}
	normalized, err := key.Normalize()
	if err != nil {
		return retrieval.EmbeddingItem{}, false, err
	}
	rows, err := s.db.Query(ctx, lookupEmbeddingCacheSQL,
		normalized.OrganizationID, normalized.ProfileID, normalized.EvidenceContentHash,
	)
	if err != nil {
		return retrieval.EmbeddingItem{}, false, normalizeEmbeddingProjectionError(ctx, "lookup embedding cache", err)
	}
	if rows == nil {
		return retrieval.EmbeddingItem{}, false, fmt.Errorf("%w: embedding cache rows are nil", ErrInconsistent)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return retrieval.EmbeddingItem{}, false, normalizeEmbeddingProjectionError(ctx, "read embedding cache", err)
		}
		return retrieval.EmbeddingItem{}, false, nil
	}
	item, err := scanEmbeddingItem(rows)
	if err != nil {
		return retrieval.EmbeddingItem{}, false, normalizeEmbeddingProjectionError(ctx, "scan embedding cache", err)
	}
	if item.OrganizationID != normalized.OrganizationID || item.ProfileID != normalized.ProfileID || item.EvidenceContentHash != normalized.EvidenceContentHash {
		return retrieval.EmbeddingItem{}, false, fmt.Errorf("%w: cache row scope or identity mismatch", ErrInconsistent)
	}
	if err := validateStoredEmbeddingItem(item); err != nil {
		return retrieval.EmbeddingItem{}, false, err
	}
	if err := rows.Err(); err != nil {
		return retrieval.EmbeddingItem{}, false, normalizeEmbeddingProjectionError(ctx, "read embedding cache", err)
	}
	return item, true, nil
}

func (s *EmbeddingProjectionStore) RebuildSnapshot(ctx context.Context, profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, inputs []retrieval.EmbeddingInput) (retrieval.EmbeddingRebuildResult, error) {
	if err := validateEmbeddingStoreContext(ctx); err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	if s == nil || s.db == nil {
		return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: embedding projection store is not configured", ErrInvalidInput)
	}
	normalizedProfile, normalizedScope, prepared, err := preparePersistedEmbeddingInputs(profile, scope, inputs)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "begin embedding rebuild", err)
	}
	if tx == nil {
		return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: embedding rebuild transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackEmbeddingProjection(ctx, tx)
		}
	}()

	cached, err := readEmbeddingCacheForRebuild(ctx, tx, normalizedProfile, prepared)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "read embedding rebuild cache", err)
	}
	if _, err := tx.Exec(ctx, deleteEmbeddingSnapshotSQL,
		normalizedScope.OrganizationID, normalizedProfile.ID,
		normalizedScope.SourceID, normalizedScope.SnapshotID,
	); err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "clear embedding snapshot projection", err)
	}

	result := retrieval.EmbeddingRebuildResult{
		OrganizationID: normalizedScope.OrganizationID,
		ProfileID:      normalizedProfile.ID,
		SourceID:       normalizedScope.SourceID,
		SnapshotID:     normalizedScope.SnapshotID,
		Requested:      len(prepared),
		Items:          make([]retrieval.EmbeddingItem, 0, len(prepared)),
		Missing:        make([]retrieval.EmbeddingMissing, 0),
	}
	for _, input := range prepared {
		vector, hit := cached[input.EvidenceContentHash]
		if hit {
			result.CacheHits++
		} else if len(input.Vector) != 0 {
			vector = input.Vector
			result.Inserted++
		} else {
			result.Missing = append(result.Missing, retrieval.EmbeddingMissing{
				EvidenceID: input.EvidenceID, EvidenceContentHash: input.EvidenceContentHash,
			})
			continue
		}
		item := retrieval.EmbeddingItem{
			ID: input.ID, OrganizationID: normalizedScope.OrganizationID,
			ProfileID: normalizedProfile.ID, ProfileDimension: normalizedProfile.Dimension,
			SourceID: normalizedScope.SourceID, SnapshotID: normalizedScope.SnapshotID,
			EvidenceID: input.EvidenceID, EvidenceContentHash: input.EvidenceContentHash,
			Vector: append([]float32(nil), vector...), State: "ready",
		}
		if err := validateFiniteEmbeddingVector(item.Vector, normalizedProfile.Dimension); err != nil {
			return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: embedding vector is inconsistent", ErrInconsistent)
		}
		tag, err := tx.Exec(ctx, insertEmbeddingItemSQL,
			item.ID, item.OrganizationID, item.ProfileID, item.ProfileDimension,
			item.SourceID, item.SnapshotID, item.EvidenceID, item.EvidenceContentHash,
			pgvector.NewVector(item.Vector), item.State,
		)
		if err != nil {
			return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "write embedding projection", err)
		}
		if tag.RowsAffected() != 1 {
			return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: embedding projection identity conflict", ErrConflict)
		}
		result.Items = append(result.Items, item)
	}
	if err := tx.Commit(ctx); err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "commit embedding rebuild", err)
	}
	committed = true
	return result, nil
}

// RebuildSnapshotIncremental copies compatible vectors from the previous
// snapshot and reconstructs affected rows from the current input/cache. The
// current profile and content hash are part of the copy predicate, so a
// caller cannot silently mix vector spaces or stale representations.
func (s *EmbeddingProjectionStore) RebuildSnapshotIncremental(ctx context.Context, profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, previousSnapshotID string, inputs []retrieval.IncrementalEmbeddingInput) (retrieval.EmbeddingRebuildResult, error) {
	if err := validateEmbeddingStoreContext(ctx); err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	if s == nil || s.db == nil {
		return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: embedding projection store is not configured", ErrInvalidInput)
	}
	normalizedProfile, normalizedScope, prepared, err := preparePersistedIncrementalEmbeddingInputs(profile, scope, previousSnapshotID, inputs)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "begin incremental embedding rebuild", err)
	}
	if tx == nil {
		return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: incremental embedding transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackEmbeddingProjection(ctx, tx)
		}
	}()
	baseInputs := make([]retrieval.EmbeddingInput, 0, len(prepared))
	for _, candidate := range prepared {
		baseInputs = append(baseInputs, candidate.Input)
	}
	cached, err := readEmbeddingCacheForRebuild(ctx, tx, normalizedProfile, baseInputs)
	if err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "read incremental embedding cache", err)
	}
	if _, err := tx.Exec(ctx, deleteEmbeddingSnapshotSQL,
		normalizedScope.OrganizationID, normalizedProfile.ID,
		normalizedScope.SourceID, normalizedScope.SnapshotID,
	); err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "clear incremental embedding snapshot projection", err)
	}
	result := retrieval.EmbeddingRebuildResult{
		OrganizationID: normalizedScope.OrganizationID, ProfileID: normalizedProfile.ID,
		SourceID: normalizedScope.SourceID, SnapshotID: normalizedScope.SnapshotID,
		Requested: len(prepared), Items: make([]retrieval.EmbeddingItem, 0, len(prepared)),
		Missing: make([]retrieval.EmbeddingMissing, 0),
	}
	for _, candidate := range prepared {
		if err := ctx.Err(); err != nil {
			return retrieval.EmbeddingRebuildResult{}, err
		}
		input := candidate.Input
		vector, cacheHit := cached[input.EvidenceContentHash]
		copied := false
		if candidate.Reuse {
			tag, copyErr := tx.Exec(ctx, copyEmbeddingItemSQL,
				input.ID, normalizedScope.OrganizationID, normalizedProfile.ID,
				normalizedProfile.Dimension, normalizedScope.SourceID, normalizedScope.SnapshotID,
				input.EvidenceID, previousSnapshotID, candidate.PreviousEvidenceID,
				input.EvidenceContentHash,
			)
			if copyErr != nil {
				return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "copy incremental embedding projection", copyErr)
			}
			if tag.RowsAffected() > 1 {
				return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: copied embedding identity is ambiguous", ErrInconsistent)
			}
			if tag.RowsAffected() == 1 {
				if !cacheHit {
					return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: copied embedding vector is not readable", ErrInconsistent)
				}
				result.CacheHits++
				copied = true
			} else {
				// The previous row may have been pruned while the content-hash
				// cache remains. Reconstruct from that compatible cache entry.
				cacheHit = false
			}
		}
		if copied {
			item := incrementalEmbeddingItem(input, normalizedProfile, normalizedScope, vector)
			if err := validateFiniteEmbeddingVector(item.Vector, normalizedProfile.Dimension); err != nil {
				return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: copied embedding vector is inconsistent", ErrInconsistent)
			}
			result.Items = append(result.Items, item)
			continue
		}
		if !cacheHit && len(input.Vector) != 0 {
			vector = input.Vector
			result.Inserted++
		} else if cacheHit {
			result.CacheHits++
		} else {
			result.Missing = append(result.Missing, retrieval.EmbeddingMissing{EvidenceID: input.EvidenceID, EvidenceContentHash: input.EvidenceContentHash})
			continue
		}
		item := incrementalEmbeddingItem(input, normalizedProfile, normalizedScope, vector)
		if err := validateFiniteEmbeddingVector(item.Vector, normalizedProfile.Dimension); err != nil {
			return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: incremental embedding vector is inconsistent", ErrInconsistent)
		}
		tag, err := tx.Exec(ctx, insertEmbeddingItemSQL,
			item.ID, item.OrganizationID, item.ProfileID, item.ProfileDimension,
			item.SourceID, item.SnapshotID, item.EvidenceID, item.EvidenceContentHash,
			pgvector.NewVector(item.Vector), item.State,
		)
		if err != nil {
			return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "write incremental embedding projection", err)
		}
		if tag.RowsAffected() != 1 {
			return retrieval.EmbeddingRebuildResult{}, fmt.Errorf("%w: incremental embedding identity conflict", ErrConflict)
		}
		result.Items = append(result.Items, item)
	}
	if err := ctx.Err(); err != nil {
		return retrieval.EmbeddingRebuildResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return retrieval.EmbeddingRebuildResult{}, normalizeEmbeddingProjectionError(ctx, "commit incremental embedding rebuild", err)
	}
	committed = true
	return result, nil
}

func preparePersistedIncrementalEmbeddingInputs(profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, previousSnapshotID string, inputs []retrieval.IncrementalEmbeddingInput) (retrieval.EmbeddingProfile, retrieval.EmbeddingScope, []retrieval.IncrementalEmbeddingInput, error) {
	normalizedProfile, err := profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
	}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
	}
	if normalizedProfile.OrganizationID != normalizedScope.OrganizationID {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: profile organization differs from scope", retrieval.ErrEmbeddingScopeMismatch)
	}
	if err := validateUUID("previous snapshot id", previousSnapshotID); err != nil {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
	}
	if previousSnapshotID == normalizedScope.SnapshotID {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: incremental snapshots must differ", ErrInvalidInput)
	}
	prepared := make([]retrieval.IncrementalEmbeddingInput, 0, len(inputs))
	seenKeys := make(map[string]struct{}, len(inputs))
	seenEvidence := make(map[string]struct{}, len(inputs))
	for _, candidate := range inputs {
		if candidate.StableKey == "" {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: incremental stable key is required", ErrInvalidInput)
		}
		if _, exists := seenKeys[candidate.StableKey]; exists {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: duplicate incremental stable key", ErrConflict)
		}
		normalized, err := candidate.Input.Normalize(normalizedProfile, normalizedScope)
		if err != nil {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
		}
		if _, exists := seenEvidence[normalized.EvidenceID]; exists {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: duplicate incremental evidence identity", ErrConflict)
		}
		if candidate.Reuse {
			if err := validateUUID("previous evidence id", candidate.PreviousEvidenceID); err != nil {
				return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
			}
		}
		candidate.Input = normalized
		prepared = append(prepared, candidate)
		seenKeys[candidate.StableKey] = struct{}{}
		seenEvidence[normalized.EvidenceID] = struct{}{}
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].Input.EvidenceID < prepared[j].Input.EvidenceID })
	return normalizedProfile, normalizedScope, prepared, nil
}

func incrementalEmbeddingItem(input retrieval.EmbeddingInput, profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, vector []float32) retrieval.EmbeddingItem {
	return retrieval.EmbeddingItem{
		ID: input.ID, OrganizationID: scope.OrganizationID, ProfileID: profile.ID,
		ProfileDimension: profile.Dimension, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID,
		EvidenceID: input.EvidenceID, EvidenceContentHash: input.EvidenceContentHash,
		Vector: append([]float32(nil), vector...), State: "ready",
	}
}

func preparePersistedEmbeddingInputs(profile retrieval.EmbeddingProfile, scope retrieval.EmbeddingScope, inputs []retrieval.EmbeddingInput) (retrieval.EmbeddingProfile, retrieval.EmbeddingScope, []retrieval.EmbeddingInput, error) {
	normalizedProfile, err := profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
	}
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
	}
	if normalizedProfile.OrganizationID != normalizedScope.OrganizationID {
		return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: profile organization differs from scope", retrieval.ErrEmbeddingScopeMismatch)
	}
	prepared := make([]retrieval.EmbeddingInput, 0, len(inputs))
	seenIDs := make(map[string]struct{}, len(inputs))
	seenEvidence := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		normalized, err := input.Normalize(normalizedProfile, normalizedScope)
		if err != nil {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, err
		}
		if _, exists := seenIDs[normalized.ID]; exists {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: duplicate embedding identity", retrieval.ErrInvalidEmbeddingProjection)
		}
		if _, exists := seenEvidence[normalized.EvidenceID]; exists {
			return retrieval.EmbeddingProfile{}, retrieval.EmbeddingScope{}, nil, fmt.Errorf("%w: duplicate evidence identity", retrieval.ErrInvalidEmbeddingProjection)
		}
		seenIDs[normalized.ID] = struct{}{}
		seenEvidence[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.Slice(prepared, func(i, j int) bool {
		if prepared[i].EvidenceID != prepared[j].EvidenceID {
			return prepared[i].EvidenceID < prepared[j].EvidenceID
		}
		return prepared[i].ID < prepared[j].ID
	})
	return normalizedProfile, normalizedScope, prepared, nil
}

func readEmbeddingCacheForRebuild(ctx context.Context, tx embeddingProjectionTransaction, profile retrieval.EmbeddingProfile, inputs []retrieval.EmbeddingInput) (map[string][]float32, error) {
	hashes := make([]string, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		if _, exists := seen[input.EvidenceContentHash]; exists {
			continue
		}
		seen[input.EvidenceContentHash] = struct{}{}
		hashes = append(hashes, input.EvidenceContentHash)
	}
	result := make(map[string][]float32, len(hashes))
	if len(hashes) == 0 {
		return result, nil
	}
	rows, err := tx.Query(ctx, selectEmbeddingCacheSQL,
		profile.OrganizationID, profile.ID, hashes, profile.Dimension,
	)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return nil, fmt.Errorf("%w: embedding cache rows are nil", ErrInconsistent)
	}
	defer rows.Close()
	for rows.Next() {
		var cachedEvidenceID string
		var hash string
		var dimension int
		var vector pgvector.Vector
		if err := rows.Scan(&cachedEvidenceID, &hash, &dimension, &vector); err != nil {
			return nil, err
		}
		if err := validateUUID("cached_evidence_id", cachedEvidenceID); err != nil {
			return nil, fmt.Errorf("%w: cached embedding identity is invalid", ErrInconsistent)
		}
		hash = strings.ToLower(strings.TrimSpace(hash))
		if !isEmbeddingSHA256(hash) || dimension != profile.Dimension {
			return nil, fmt.Errorf("%w: cached embedding metadata is inconsistent", ErrInconsistent)
		}
		values := append([]float32(nil), vector.Slice()...)
		if err := validateFiniteEmbeddingVector(values, profile.Dimension); err != nil {
			return nil, fmt.Errorf("%w: cached embedding vector is inconsistent", ErrInconsistent)
		}
		if _, exists := result[hash]; !exists {
			result[hash] = values
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func readEmbeddingProfile(ctx context.Context, tx embeddingProjectionTransaction, organizationID, profileID string) (retrieval.EmbeddingProfile, error) {
	rows, err := tx.Query(ctx, selectEmbeddingProfileSQL, organizationID, profileID)
	if err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	if rows == nil {
		return retrieval.EmbeddingProfile{}, fmt.Errorf("%w: embedding profile rows are nil", ErrInconsistent)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return retrieval.EmbeddingProfile{}, err
		}
		return retrieval.EmbeddingProfile{}, ErrNotFound
	}
	var profile retrieval.EmbeddingProfile
	var configuration []byte
	if err := rows.Scan(
		&profile.ID, &profile.OrganizationID, &profile.Provider, &profile.Model,
		&profile.Dimension, &profile.Normalization, &profile.ConfigurationVersion,
		&profile.ConfigurationDigest, &configuration,
	); err != nil {
		return retrieval.EmbeddingProfile{}, err
	}
	profile.Configuration = append(json.RawMessage(nil), configuration...)
	normalized, err := profile.Normalize()
	if err != nil {
		return retrieval.EmbeddingProfile{}, fmt.Errorf("%w: stored embedding profile is invalid", ErrInconsistent)
	}
	return normalized, nil
}

func sameEmbeddingProfile(left, right retrieval.EmbeddingProfile) bool {
	return left.ID == right.ID && left.OrganizationID == right.OrganizationID &&
		left.Provider == right.Provider && left.Model == right.Model &&
		left.Dimension == right.Dimension && left.Normalization == right.Normalization &&
		left.ConfigurationVersion == right.ConfigurationVersion &&
		left.ConfigurationDigest == right.ConfigurationDigest &&
		bytes.Equal(left.Configuration, right.Configuration)
}

func scanEmbeddingItem(rows pgx.Rows) (retrieval.EmbeddingItem, error) {
	var item retrieval.EmbeddingItem
	var vector pgvector.Vector
	if err := rows.Scan(
		&item.ID, &item.OrganizationID, &item.ProfileID, &item.ProfileDimension,
		&item.SourceID, &item.SnapshotID, &item.EvidenceID, &item.EvidenceContentHash,
		&vector, &item.State,
	); err != nil {
		return retrieval.EmbeddingItem{}, err
	}
	item.ID = strings.ToLower(strings.TrimSpace(item.ID))
	item.OrganizationID = strings.ToLower(strings.TrimSpace(item.OrganizationID))
	item.ProfileID = strings.ToLower(strings.TrimSpace(item.ProfileID))
	item.SourceID = strings.ToLower(strings.TrimSpace(item.SourceID))
	item.SnapshotID = strings.ToLower(strings.TrimSpace(item.SnapshotID))
	item.EvidenceID = strings.ToLower(strings.TrimSpace(item.EvidenceID))
	item.EvidenceContentHash = strings.ToLower(strings.TrimSpace(item.EvidenceContentHash))
	item.Vector = append([]float32(nil), vector.Slice()...)
	return item, nil
}

func validateStoredEmbeddingItem(item retrieval.EmbeddingItem) error {
	for name, value := range map[string]string{
		"embedding_id": item.ID, "organization_id": item.OrganizationID,
		"profile_id": item.ProfileID, "source_id": item.SourceID,
		"snapshot_id": item.SnapshotID, "evidence_id": item.EvidenceID,
	} {
		if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			return fmt.Errorf("%w: stored %s is invalid", ErrInconsistent, name)
		}
	}
	if !isEmbeddingSHA256(item.EvidenceContentHash) || item.ProfileDimension <= 0 || (item.State != "ready" && item.State != "stale") {
		return fmt.Errorf("%w: stored embedding metadata is invalid", ErrInconsistent)
	}
	if err := validateFiniteEmbeddingVector(item.Vector, item.ProfileDimension); err != nil {
		return fmt.Errorf("%w: stored embedding vector is invalid", ErrInconsistent)
	}
	return nil
}

func isEmbeddingSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateFiniteEmbeddingVector(vector []float32, dimension int) error {
	if len(vector) != dimension {
		return fmt.Errorf("embedding vector dimension is invalid")
	}
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return fmt.Errorf("embedding vector contains non-finite value")
		}
	}
	return nil
}

func validateEmbeddingStoreContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: embedding projection context is nil", ErrInvalidInput)
	}
	return ctx.Err()
}

func rollbackEmbeddingProjection(ctx context.Context, tx embeddingProjectionTransaction) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}

func normalizeEmbeddingProjectionError(ctx context.Context, operation string, err error) error {
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
		case "22P02", "22000", "23514":
			return fmt.Errorf("%w: %s", ErrInvalidInput, operation)
		default:
			return fmt.Errorf("%w: %s", ErrDatabase, operation)
		}
	}
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidInput) || errors.Is(err, ErrInconsistent) {
		return err
	}
	return fmt.Errorf("%w: %s", ErrDatabase, operation)
}
