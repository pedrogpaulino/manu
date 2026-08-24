package persistence

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

// FactualRelationInputProvider reads the bounded factual links needed to
// start one-hop relation retrieval. It never loads a snapshot or source
// content; all returned identities are canonical relational Evidence Units.
type FactualRelationInputProvider struct {
	db factualRelationInputDatabase
}

type factualRelationInputDatabase interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type poolFactualRelationInputDatabase struct {
	pool *pgxpool.Pool
}

func (d poolFactualRelationInputDatabase) Query(ctx context.Context, statement string, args ...any) (pgx.Rows, error) {
	if d.pool == nil {
		return nil, fmt.Errorf("%w: factual relation input database is not configured", ErrInvalidInput)
	}
	return d.pool.Query(ctx, statement, args...)
}

// NewFactualRelationInputProvider creates a PostgreSQL-backed relation input
// provider without opening or probing the supplied pool.
func NewFactualRelationInputProvider(pool *pgxpool.Pool) *FactualRelationInputProvider {
	return &FactualRelationInputProvider{db: poolFactualRelationInputDatabase{pool: pool}}
}

func newFactualRelationInputProvider(db factualRelationInputDatabase) *FactualRelationInputProvider {
	return &FactualRelationInputProvider{db: db}
}

var _ query.RelationInputProvider = (*FactualRelationInputProvider)(nil)

// factualRelationSeedsSQL resolves only the selected Evidence Unit IDs to
// factual participants. External scope identities are selected explicitly so
// the computed entity ID remains identical to PrepareFactualProjection.
const factualRelationSeedsSQL = `
SELECT cfe.evidence_unit_id::text, btrim(eu.content_hash),
       o.external_id, s.external_id, sn.external_id,
       cf.subject_kind, cf.subject_id, cf.object_kind, cf.object_id
FROM canonical_fact_evidence cfe
JOIN canonical_facts cf
  ON cf.organization_id = cfe.organization_id
 AND cf.source_id = cfe.source_id
 AND cf.snapshot_id = cfe.snapshot_id
 AND cf.id = cfe.fact_id
JOIN evidence_units eu
  ON eu.organization_id = cfe.organization_id
 AND eu.source_id = cfe.source_id
 AND eu.snapshot_id = cfe.snapshot_id
 AND eu.id = cfe.evidence_unit_id
JOIN organizations o
  ON o.id = cfe.organization_id
JOIN sources s
  ON s.organization_id = cfe.organization_id
 AND s.id = cfe.source_id
JOIN analysis_snapshots sn
  ON sn.organization_id = cfe.organization_id
 AND sn.source_id = cfe.source_id
 AND sn.id = cfe.snapshot_id
WHERE cfe.organization_id = $1::uuid
  AND cfe.source_id = $2::uuid
  AND cfe.snapshot_id = $3::uuid
  AND cfe.evidence_unit_id = ANY($4::uuid[])
ORDER BY cfe.evidence_unit_id ASC, cf.subject_kind ASC, cf.subject_id ASC,
         cf.object_kind ASC NULLS FIRST, cf.object_id ASC NULLS FIRST,
         cfe.ordinal ASC, cfe.fact_id ASC
LIMIT $5`

// factualRelationReferencesSQL follows only adjacent canonical relationship
// rows. Their attributes.fact_ids are external factual identities, so the
// query joins them back to canonical facts and evidence links before exposing
// the relational evidence UUID and its canonical content hash.
const factualRelationReferencesSQL = `
SELECT r.from_entity_id::text, r.to_entity_id::text,
       cfe.evidence_unit_id::text, btrim(eu.content_hash)
FROM relationships r
CROSS JOIN LATERAL jsonb_array_elements_text(r.attributes->'fact_ids') AS fact_ref(fact_external_id)
JOIN canonical_facts cf
  ON cf.organization_id = r.organization_id
 AND cf.source_id = r.source_id
 AND cf.snapshot_id = r.snapshot_id
 AND cf.identity_key = fact_ref.fact_external_id
JOIN canonical_fact_evidence cfe
  ON cfe.organization_id = cf.organization_id
 AND cfe.source_id = cf.source_id
 AND cfe.snapshot_id = cf.snapshot_id
 AND cfe.fact_id = cf.id
JOIN evidence_units eu
  ON eu.organization_id = cfe.organization_id
 AND eu.source_id = cfe.source_id
 AND eu.snapshot_id = cfe.snapshot_id
 AND eu.id = cfe.evidence_unit_id
WHERE r.organization_id = $1::uuid
  AND r.source_id = $2::uuid
  AND r.snapshot_id = $3::uuid
  AND (r.from_entity_id = ANY($4::uuid[]) OR r.to_entity_id = ANY($4::uuid[]))
ORDER BY r.from_entity_id ASC, r.to_entity_id ASC,
         r.relationship_type ASC, r.id ASC,
         cfe.evidence_unit_id ASC, cfe.ordinal ASC
LIMIT $5`

type factualRelationSeedRow struct {
	evidenceID     string
	contentHash    string
	organizationID string
	sourceID       string
	snapshotID     string
	subjectKind    string
	subjectID      string
	objectKind     *string
	objectID       *string
}

type factualRelationReferenceRow struct {
	fromEntityID string
	toEntityID   string
	evidenceID   string
	contentHash  string
}

// RelationInputs resolves selected textual/vector evidence to factual entity
// anchors, then resolves adjacent relationship support to canonical evidence
// references. Every SQL operation is scoped and bounded by the retrieval
// input; no snapshot-wide read is performed.
func (p *FactualRelationInputProvider) RelationInputs(ctx context.Context, input query.QueryRetrievalInput, textHits []retrieval.TextHit, vectorHits []retrieval.VectorHit) ([]retrieval.RelationSeed, map[string][]retrieval.FusionEvidenceReference, error) {
	if err := validateFactualRelationInputContext(ctx); err != nil {
		return nil, nil, err
	}
	if err := input.Scope.Validate(); err != nil {
		return nil, nil, fmt.Errorf("%w: factual relation input scope", ErrInvalidInput)
	}
	if p == nil || p.db == nil {
		return nil, nil, fmt.Errorf("%w: factual relation input provider is not configured", ErrInvalidInput)
	}
	evidenceIDs := selectedCanonicalEvidenceIDs(input, textHits, vectorHits)
	if len(evidenceIDs) == 0 {
		return []retrieval.RelationSeed{}, map[string][]retrieval.FusionEvidenceReference{}, nil
	}
	limit, err := factualRelationInputLimit(input.Limit)
	if err != nil {
		return nil, nil, err
	}

	rows, err := p.db.Query(ctx, factualRelationSeedsSQL,
		input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID,
		evidenceIDs, limit,
	)
	if err != nil {
		return nil, nil, normalizeFactualRelationInputError(ctx, "read factual relation seeds", err)
	}
	if rows == nil {
		return nil, nil, fmt.Errorf("%w: factual relation seed rows are nil", ErrInconsistent)
	}
	seedRows, err := readFactualRelationSeedRows(ctx, rows)
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	seeds, anchors := factualRelationSeeds(input, seedRows)
	if len(anchors) == 0 {
		return seeds, map[string][]retrieval.FusionEvidenceReference{}, nil
	}
	anchorIDs := make([]string, 0, len(anchors))
	for entityID := range anchors {
		anchorIDs = append(anchorIDs, entityID)
	}
	sort.Strings(anchorIDs)
	rows, err = p.db.Query(ctx, factualRelationReferencesSQL,
		input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID,
		anchorIDs, limit,
	)
	if err != nil {
		return nil, nil, normalizeFactualRelationInputError(ctx, "read factual relation references", err)
	}
	if rows == nil {
		return nil, nil, fmt.Errorf("%w: factual relation reference rows are nil", ErrInconsistent)
	}
	referenceRows, err := readFactualRelationReferenceRows(ctx, rows)
	if err != nil {
		return nil, nil, err
	}
	return seeds, factualRelationReferences(input, anchors, referenceRows), nil
}

func validateFactualRelationInputContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: factual relation input context is nil", ErrInvalidInput)
	}
	return ctx.Err()
}

func factualRelationInputLimit(requested int) (int, error) {
	if requested == 0 {
		requested = retrieval.DefaultTextSearchLimit
	}
	if requested < 1 || requested > retrieval.MaxTextSearchLimit {
		return 0, fmt.Errorf("%w: factual relation input limit is invalid", ErrInvalidInput)
	}
	if requested > retrieval.MaxFusionRelationBudget {
		return retrieval.MaxFusionRelationBudget, nil
	}
	return requested, nil
}

func selectedCanonicalEvidenceIDs(input query.QueryRetrievalInput, textHits []retrieval.TextHit, vectorHits []retrieval.VectorHit) []string {
	seen := make(map[string]struct{}, len(textHits)+len(vectorHits))
	for _, hit := range textHits {
		addSelectedEvidenceID(seen, input, hit.EvidenceID, hit.OrganizationID, hit.SourceID, hit.SnapshotID)
	}
	for _, hit := range vectorHits {
		addSelectedEvidenceID(seen, input, hit.EvidenceID, hit.OrganizationID, hit.SourceID, hit.SnapshotID)
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func addSelectedEvidenceID(seen map[string]struct{}, input query.QueryRetrievalInput, evidenceID, organizationID, sourceID, snapshotID string) {
	evidenceID = strings.ToLower(strings.TrimSpace(evidenceID))
	if err := validateUUID("selected evidence id", evidenceID); err != nil {
		return
	}
	if organizationID != "" && !sameFactualRelationScope(input.Scope, organizationID, sourceID, snapshotID) {
		return
	}
	seen[evidenceID] = struct{}{}
}

func sameFactualRelationScope(scope query.Scope, organizationID, sourceID, snapshotID string) bool {
	return strings.EqualFold(strings.TrimSpace(scope.OrganizationID), strings.TrimSpace(organizationID)) &&
		strings.EqualFold(strings.TrimSpace(scope.SourceID), strings.TrimSpace(sourceID)) &&
		strings.EqualFold(strings.TrimSpace(scope.SnapshotID), strings.TrimSpace(snapshotID))
}

func readFactualRelationSeedRows(ctx context.Context, rows pgx.Rows) ([]factualRelationSeedRow, error) {
	defer rows.Close()
	result := make([]factualRelationSeedRow, 0)
	for rows.Next() {
		var item factualRelationSeedRow
		if err := rows.Scan(
			&item.evidenceID, &item.contentHash,
			&item.organizationID, &item.sourceID, &item.snapshotID,
			&item.subjectKind, &item.subjectID, &item.objectKind, &item.objectID,
		); err != nil {
			return nil, normalizeFactualRelationInputError(ctx, "scan factual relation seed", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeFactualRelationInputError(ctx, "read factual relation seeds", err)
	}
	return result, nil
}

func readFactualRelationReferenceRows(ctx context.Context, rows pgx.Rows) ([]factualRelationReferenceRow, error) {
	defer rows.Close()
	result := make([]factualRelationReferenceRow, 0)
	for rows.Next() {
		var item factualRelationReferenceRow
		if err := rows.Scan(&item.fromEntityID, &item.toEntityID, &item.evidenceID, &item.contentHash); err != nil {
			return nil, normalizeFactualRelationInputError(ctx, "scan factual relation reference", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, normalizeFactualRelationInputError(ctx, "read factual relation references", err)
	}
	return result, nil
}

func factualRelationSeeds(input query.QueryRetrievalInput, rows []factualRelationSeedRow) ([]retrieval.RelationSeed, map[string]map[string]struct{}) {
	seeds := make([]retrieval.RelationSeed, 0, len(rows)*2)
	seenSeeds := make(map[string]struct{}, len(rows)*2)
	anchors := make(map[string]map[string]struct{}, len(rows)*2)
	for _, row := range rows {
		evidenceID := strings.ToLower(strings.TrimSpace(row.evidenceID))
		if err := validateUUID("factual relation evidence id", evidenceID); err != nil || !isEmbeddingSHA256(row.contentHash) {
			continue
		}
		scope := fact.Scope{
			OrganizationID: row.organizationID,
			SourceID:       row.sourceID,
			SnapshotID:     row.snapshotID,
		}
		participants := []fact.Participant{{Kind: fact.ParticipantKind(row.subjectKind), ID: row.subjectID}}
		if row.objectKind != nil && row.objectID != nil && strings.TrimSpace(*row.objectKind) != "" && strings.TrimSpace(*row.objectID) != "" {
			participants = append(participants, fact.Participant{Kind: fact.ParticipantKind(*row.objectKind), ID: *row.objectID})
		}
		for _, participant := range participants {
			entityID := factualProjectionEntityID(scope, participant)
			if _, exists := anchors[entityID]; !exists {
				anchors[entityID] = make(map[string]struct{})
			}
			anchors[entityID][evidenceID] = struct{}{}
			key := evidenceID + "\x00" + entityID
			if _, exists := seenSeeds[key]; exists {
				continue
			}
			seenSeeds[key] = struct{}{}
			seeds = append(seeds, retrieval.RelationSeed{EvidenceID: evidenceID, EntityID: entityID})
		}
	}
	sort.Slice(seeds, func(i, j int) bool {
		if seeds[i].EvidenceID != seeds[j].EvidenceID {
			return seeds[i].EvidenceID < seeds[j].EvidenceID
		}
		return seeds[i].EntityID < seeds[j].EntityID
	})
	return seeds, anchors
}

func factualRelationReferences(input query.QueryRetrievalInput, anchors map[string]map[string]struct{}, rows []factualRelationReferenceRow) map[string][]retrieval.FusionEvidenceReference {
	result := make(map[string][]retrieval.FusionEvidenceReference)
	seen := make(map[string]map[string]string)
	for _, row := range rows {
		fromID := strings.ToLower(strings.TrimSpace(row.fromEntityID))
		toID := strings.ToLower(strings.TrimSpace(row.toEntityID))
		evidenceID := strings.ToLower(strings.TrimSpace(row.evidenceID))
		contentHash := strings.ToLower(strings.TrimSpace(row.contentHash))
		if validateUUID("factual relation from entity id", fromID) != nil ||
			validateUUID("factual relation to entity id", toID) != nil ||
			validateUUID("factual relation reference evidence id", evidenceID) != nil ||
			!isEmbeddingSHA256(contentHash) {
			continue
		}
		for anchor := range anchors {
			target := ""
			if anchor == fromID {
				target = toID
			} else if anchor == toID {
				target = fromID
			}
			if target == "" {
				continue
			}
			if _, exists := seen[target]; !exists {
				seen[target] = make(map[string]string)
			}
			if previous, exists := seen[target][evidenceID]; exists {
				if previous != contentHash {
					continue
				}
				continue
			}
			seen[target][evidenceID] = contentHash
			result[target] = append(result[target], retrieval.FusionEvidenceReference{
				EvidenceID: evidenceID, OrganizationID: input.Scope.OrganizationID,
				SourceID: input.Scope.SourceID, SnapshotID: input.Scope.SnapshotID,
				EvidenceContentHash: contentHash,
			})
		}
	}
	for entityID, references := range result {
		sort.Slice(references, func(i, j int) bool {
			if references[i].EvidenceID != references[j].EvidenceID {
				return references[i].EvidenceID < references[j].EvidenceID
			}
			return references[i].EvidenceContentHash < references[j].EvidenceContentHash
		})
		result[entityID] = references
	}
	return result
}

func normalizeFactualRelationInputError(ctx context.Context, operation string, err error) error {
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
