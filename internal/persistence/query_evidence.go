package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/query"
)

// QueryEvidenceUnitRepository resolves the canonical evidence UUID returned
// by textual/vector projections into a bounded Evidence Unit. The returned
// unit uses canonical relational scope IDs so it can safely enter the query
// package, while CanonicalEvidenceID on query.PackageCandidate retains the
// database identity used by the inspection endpoint.
type QueryEvidenceUnitRepository struct {
	repository *Repository
}

// NewQueryEvidenceUnitRepository constructs the organization-scoped resolver
// without opening or probing the supplied pool.
func NewQueryEvidenceUnitRepository(pool *pgxpool.Pool) *QueryEvidenceUnitRepository {
	if pool == nil {
		return &QueryEvidenceUnitRepository{}
	}
	return &QueryEvidenceUnitRepository{repository: NewRepository(pool)}
}

func newQueryEvidenceUnitRepositoryWithStarter(starter transactionStarter) *QueryEvidenceUnitRepository {
	return &QueryEvidenceUnitRepository{repository: newRepositoryWithStarter(starter)}
}

var _ query.EvidenceUnitResolver = (*QueryEvidenceUnitRepository)(nil)

const selectQueryEvidenceUnitSQL = `
SELECT eu.id::text, eu.organization_id::text, eu.source_id::text,
       eu.snapshot_id::text, eu.artifact_id::text, eu.observation_id::text,
       eu.external_id, a.external_id,
       ob.external_id, ob.analyzer_id, ob.analyzer_version, ob.method,
       eu.locator, eu.content_state, eu.content, btrim(eu.content_hash),
       eu.content_bytes, eu.content_characters, eu.truncated,
       eu.classification, eu.findings, eu.persist_decision,
       eu.external_transfer_decision, eu.redaction_reason, eu.provenance
FROM evidence_units eu
JOIN artifacts a
  ON a.organization_id = eu.organization_id
 AND a.source_id = eu.source_id
 AND a.snapshot_id = eu.snapshot_id
 AND a.id = eu.artifact_id
LEFT JOIN observations ob
  ON ob.organization_id = eu.organization_id
 AND ob.source_id = eu.source_id
 AND ob.snapshot_id = eu.snapshot_id
 AND ob.id = eu.observation_id
WHERE eu.organization_id = $1::uuid
  AND eu.source_id = $2::uuid
  AND eu.snapshot_id = $3::uuid
  AND eu.id = $4::uuid`

// Resolve reads only the requested organization/source/snapshot row. It does
// not read source files and it never returns a provider or database error
// message containing row values.
func (r *QueryEvidenceUnitRepository) Resolve(ctx context.Context, scope query.Scope, evidenceID string) (evidence.EvidenceUnit, error) {
	if err := validateContext(ctx); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	if err := scope.Validate(); err != nil {
		return evidence.EvidenceUnit{}, query.ErrQueryScopeRequired
	}
	if err := validateUUID("evidence id", evidenceID); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	if r == nil || r.repository == nil || r.repository.starter == nil {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: evidence resolver is not configured", ErrDatabase)
	}
	tx, err := r.repository.starter.Begin(ctx)
	if err != nil {
		return evidence.EvidenceUnit{}, wrapPersistenceError(ctx, "begin query evidence resolution", err)
	}
	if tx == nil {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: evidence resolver transaction is nil", ErrInconsistent)
	}
	committed := false
	defer func() {
		if !committed {
			cleanupCtx, cancel := rollbackContext(ctx)
			_ = tx.Rollback(cleanupCtx)
			cancel()
		}
	}()
	unit, err := scanQueryEvidenceUnit(tx.QueryRow(ctx, selectQueryEvidenceUnitSQL,
		scope.OrganizationID, scope.SourceID, scope.SnapshotID, evidenceID), scope)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return evidence.EvidenceUnit{}, query.ErrEvidenceUnitNotFound
		}
		return evidence.EvidenceUnit{}, wrapPersistenceError(ctx, "read query evidence unit", err)
	}
	if err := ctx.Err(); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return evidence.EvidenceUnit{}, wrapPersistenceError(ctx, "commit query evidence resolution", err)
	}
	committed = true
	return unit, nil
}

func scanQueryEvidenceUnit(row pgx.Row, scope query.Scope) (evidence.EvidenceUnit, error) {
	var (
		id, organizationID, sourceID, snapshotID, artifactID string
		observationID                                        *string
		externalID, artifactExternalID                       string
		observationExternalID, analyzerID, analyzerVersion   string
		method                                               string
		locatorJSON, findingsJSON, provenanceJSON            []byte
		contentState, contentHash, persistDecision           string
		transferDecision                                     string
		content, classification, redactionReason             *string
		contentBytes, contentCharacters                      int64
		truncated                                            bool
	)
	if err := row.Scan(
		&id, &organizationID, &sourceID, &snapshotID, &artifactID, &observationID,
		&externalID, &artifactExternalID, &observationExternalID, &analyzerID,
		&analyzerVersion, &method, &locatorJSON, &contentState, &content,
		&contentHash, &contentBytes, &contentCharacters, &truncated, &classification,
		&findingsJSON, &persistDecision, &transferDecision, &redactionReason,
		&provenanceJSON,
	); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	if organizationID != scope.OrganizationID || sourceID != scope.SourceID || snapshotID != scope.SnapshotID {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: query evidence scope mismatch", ErrInconsistent)
	}
	if externalID == "" || artifactExternalID == "" || observationExternalID == "" ||
		analyzerID == "" || analyzerVersion == "" || method == "" {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: query evidence provenance is incomplete", ErrInconsistent)
	}
	if err := validateStoredEvidence(id, sourceID, snapshotID, artifactID, observationID, locatorJSON,
		contentState, content, &contentHash, contentBytes, contentCharacters, classification,
		findingsJSON, persistDecision, transferDecision, redactionReason); err != nil {
		return evidence.EvidenceUnit{}, err
	}
	var locator contract.Locator
	if err := json.Unmarshal(locatorJSON, &locator); err != nil {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: query evidence locator", ErrInconsistent)
	}
	findings := []string(nil)
	if len(findingsJSON) != 0 {
		if err := json.Unmarshal(findingsJSON, &findings); err != nil {
			return evidence.EvidenceUnit{}, fmt.Errorf("%w: query evidence findings", ErrInconsistent)
		}
	}
	trimmedProvenance := bytes.TrimSpace(provenanceJSON)
	var provenanceObject map[string]json.RawMessage
	if len(trimmedProvenance) == 0 || json.Unmarshal(trimmedProvenance, &provenanceObject) != nil || provenanceObject == nil {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: query evidence provenance", ErrInconsistent)
	}
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		ID:             externalID,
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		ArtifactID:     artifactID,
		Contribution: evidence.ContributionRef{
			ID:              observationExternalID,
			ArtifactID:      artifactID,
			AnalyzerID:      analyzerID,
			AnalyzerVersion: analyzerVersion,
			Method:          method,
		},
		Locator:           locator,
		ContentState:      evidence.ContentState(contentState),
		Content:           optionalString(content),
		ContentHash:       contentHash,
		Truncated:         truncated,
		RedactionReason:   optionalString(redactionReason),
		ContentBytes:      contentBytes,
		ContentCharacters: contentCharacters,
		Persist:           evidence.Decision(persistDecision),
		ExternalTransfer:  evidence.Decision(transferDecision),
		Classification:    evidence.Classification(optionalString(classification)),
		Findings:          findings,
	}
	// The relational locator uses canonical source/artifact IDs at this
	// boundary. Preserve only its safe structural coordinates from storage.
	unit.Locator.SourceID = scope.SourceID
	unit.Locator.ArtifactID = artifactID
	unit.ID = evidence.EvidenceID(unit)
	if err := unit.ValidatePrepared(); err != nil {
		return evidence.EvidenceUnit{}, fmt.Errorf("%w: query evidence unit", ErrInconsistent)
	}
	return unit, nil
}
