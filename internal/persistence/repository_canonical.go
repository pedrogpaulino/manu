package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

const (
	insertOrganizationSQL = `
INSERT INTO organizations (id, external_id, name)
VALUES ($1, $2, $3)
ON CONFLICT (id) DO NOTHING`
	selectOrganizationIdentitySQL = `
SELECT external_id, name
FROM organizations
WHERE id = $1`

	insertSourceSQL = `
INSERT INTO sources (id, organization_id, external_id, name, source_type, root)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectSourceIdentitySQL = `
SELECT external_id, name, source_type, root
FROM sources
WHERE organization_id = $1 AND id = $2`

	insertSnapshotSQL = `
INSERT INTO analysis_snapshots (
    id, organization_id, source_id, external_id, source_revision, source_hash,
    analysis_configuration_id, factual_digest, captured_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectSnapshotIdentitySQL = `
SELECT external_id, source_revision, source_hash,
       analysis_configuration_id, factual_digest, captured_at
FROM analysis_snapshots
WHERE organization_id = $1 AND source_id = $2 AND id = $3`

	insertArtifactSQL = `
INSERT INTO artifacts (
    id, organization_id, source_id, snapshot_id, external_id, path,
    artifact_type, content_hash, content_size
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectArtifactIdentitySQL = `
SELECT source_id, snapshot_id, external_id, path, artifact_type,
       content_hash, content_size
FROM artifacts
WHERE organization_id = $1 AND id = $2`
	insertObservationSQL = `
INSERT INTO observations (
    id, organization_id, source_id, snapshot_id, artifact_id, external_id,
    analyzer_id, analyzer_version, method, observation_type, locator, value,
    observed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectObservationIdentitySQL = `
SELECT source_id, snapshot_id, artifact_id, external_id, analyzer_id,
       analyzer_version, method, observation_type, locator, value, observed_at
FROM observations
WHERE organization_id = $1 AND id = $2`
	insertEntitySQL = `
INSERT INTO entities (
    id, organization_id, source_id, snapshot_id, external_id, entity_type,
    name, attributes
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectEntityIdentitySQL = `
SELECT source_id, snapshot_id, external_id, entity_type, name, attributes
FROM entities
WHERE organization_id = $1 AND id = $2`
	insertRelationshipSQL = `
INSERT INTO relationships (
    id, organization_id, source_id, snapshot_id, external_id,
    from_entity_id, to_entity_id, relationship_type, attributes
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectRelationshipIdentitySQL = `
SELECT source_id, snapshot_id, external_id, from_entity_id, to_entity_id,
       relationship_type, attributes
FROM relationships
WHERE organization_id = $1 AND id = $2`
	insertEvidenceSQL = `
INSERT INTO evidence_units (
    id, organization_id, source_id, snapshot_id, artifact_id, observation_id,
    external_id, locator, content_state, content, content_hash, content_bytes,
    content_characters, truncated, classification, findings, persist_decision,
    external_transfer_decision, redaction_reason, provenance
)
VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
    $17, $18, $19, $20
)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectEvidenceIdentitySQL = `
SELECT source_id, snapshot_id, artifact_id, observation_id, external_id,
       locator, content_state, content, content_hash, content_bytes,
       content_characters, truncated, classification, findings,
       persist_decision, external_transfer_decision, redaction_reason,
       provenance
FROM evidence_units
WHERE organization_id = $1 AND id = $2`
	insertCoverageSQL = `
INSERT INTO analysis_coverage (
    id, organization_id, source_id, snapshot_id, external_id, dimension,
    scope, state, analyzer_id, message, locator
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectCoverageIdentitySQL = `
SELECT source_id, snapshot_id, external_id, dimension, scope, state,
       analyzer_id, message, locator
FROM analysis_coverage
WHERE organization_id = $1 AND id = $2`
	insertGapSQL = `
INSERT INTO explicit_gaps (
    id, organization_id, source_id, snapshot_id, external_id, code, dimension,
    scope, message, analyzer_id, locator
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectGapIdentitySQL = `
SELECT source_id, snapshot_id, external_id, code, dimension, scope, message,
       analyzer_id, locator
FROM explicit_gaps
WHERE organization_id = $1 AND id = $2`
	insertFailureSQL = `
INSERT INTO analysis_failures (
    id, organization_id, source_id, snapshot_id, artifact_id, external_id,
    code, operation, message, analyzer_id, partial, locator
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFailureIdentitySQL = `
SELECT source_id, snapshot_id, artifact_id, external_id, code, operation,
       message, analyzer_id, partial, locator
FROM analysis_failures
WHERE organization_id = $1 AND id = $2`
	insertFactualIdentitySQL = `
INSERT INTO factual_identities (
    id, organization_id, source_id, snapshot_id, identity_key,
    factual_digest, state, observed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualIdentitySQL = `
SELECT source_id, snapshot_id, identity_key, factual_digest, state, observed_at
FROM factual_identities
WHERE organization_id = $1 AND id = $2`

	lockSourceSQL = `
SELECT id
FROM sources
WHERE organization_id = $1 AND id = $2
FOR UPDATE`
	lockSnapshotSQL = `
SELECT id
FROM analysis_snapshots
WHERE organization_id = $1 AND source_id = $2 AND id = $3
FOR SHARE`
	archiveActiveIdentitiesSQL = `
UPDATE factual_identities
SET state = 'historical'
WHERE organization_id = $1 AND source_id = $2 AND state = 'active'`
	activateSnapshotIdentitiesSQL = `
UPDATE factual_identities
SET state = 'active'
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3`
	activateSourceSQL = `
UPDATE sources
SET active_snapshot_id = $3
WHERE organization_id = $1 AND id = $2`
)

func (u *UnitOfWork) EnsureOrganization(ctx context.Context, organizationID string, organization Organization) error {
	if err := validateOrganizationScope(organizationID, organization.ID); err != nil {
		return err
	}
	if err := validateText("organization external_id", organization.ExternalID); err != nil {
		return err
	}
	if err := validateText("organization name", organization.Name); err != nil {
		return err
	}
	tag, err := u.tx.Exec(ctx, insertOrganizationSQL, organizationID, organization.ExternalID, organization.Name)
	if err != nil {
		return wrapPersistenceError(ctx, "insert organization", err)
	}
	if tag.RowsAffected() != 0 {
		return checkedRowsAffected(tag, 1, "organization insert")
	}

	var externalID, name string
	if err := u.tx.QueryRow(ctx, selectOrganizationIdentitySQL, organizationID).Scan(&externalID, &name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: organization identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read organization identity", err)
	}
	if externalID != organization.ExternalID || name != organization.Name {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertSource(ctx context.Context, organizationID string, source Source) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	if err := validateUUID("source id", source.ID); err != nil {
		return err
	}
	if err := validateText("source external_id", source.ExternalID); err != nil {
		return err
	}
	if err := validateText("source name", source.Name); err != nil {
		return err
	}
	if err := validateText("source type", source.Type); err != nil {
		return err
	}
	if err := validateOptionalText("source root", source.Root); err != nil {
		return err
	}
	tag, err := u.tx.Exec(ctx, insertSourceSQL, source.ID, organizationID, source.ExternalID, source.Name, source.Type, nullableText(source.Root))
	if err != nil {
		return wrapPersistenceError(ctx, "insert source", err)
	}
	if tag.RowsAffected() != 0 {
		return checkedRowsAffected(tag, 1, "source insert")
	}

	var externalID, name, sourceType string
	var root *string
	if err := u.tx.QueryRow(ctx, selectSourceIdentitySQL, organizationID, source.ID).Scan(&externalID, &name, &sourceType, &root); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: source identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read source identity", err)
	}
	if externalID != source.ExternalID || name != source.Name || sourceType != source.Type || optionalString(root) != source.Root {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertSnapshot(ctx context.Context, organizationID string, snapshot Snapshot) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	if err := validateUUID("snapshot id", snapshot.ID); err != nil {
		return err
	}
	if err := validateUUID("snapshot source_id", snapshot.SourceID); err != nil {
		return err
	}
	if err := validateText("snapshot external_id", snapshot.ExternalID); err != nil {
		return err
	}
	if err := validateOptionalText("snapshot revision", snapshot.Revision); err != nil {
		return err
	}
	if snapshot.Hash != "" {
		if err := validateDigest("snapshot source_hash", snapshot.Hash); err != nil {
			return err
		}
	}
	if snapshot.Revision == "" && snapshot.Hash == "" {
		return fmt.Errorf("%w: snapshot source identity is required", ErrInvalidInput)
	}
	if err := validateText("snapshot analysis_configuration_id", snapshot.AnalysisConfigurationID); err != nil {
		return err
	}
	if err := validateDigest("snapshot factual_digest", snapshot.FactualDigest); err != nil {
		return err
	}
	if snapshot.CapturedAt.IsZero() {
		return fmt.Errorf("%w: snapshot captured_at is required", ErrInvalidInput)
	}
	tag, err := u.tx.Exec(ctx, insertSnapshotSQL,
		snapshot.ID, organizationID, snapshot.SourceID, snapshot.ExternalID,
		nullableText(snapshot.Revision), nullableText(snapshot.Hash),
		snapshot.AnalysisConfigurationID, snapshot.FactualDigest, snapshot.CapturedAt,
	)
	if err != nil {
		return wrapPersistenceError(ctx, "insert snapshot", err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	if tag.RowsAffected() > 1 {
		return fmt.Errorf("%w: snapshot insert affected too many rows", ErrInconsistent)
	}
	return u.existingSnapshotMatches(ctx, organizationID, snapshot)
}

func (u *UnitOfWork) InsertArtifact(ctx context.Context, organizationID string, artifact Artifact) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	if err := validateUUID("artifact id", artifact.ID); err != nil {
		return err
	}
	if err := validateUUID("artifact source_id", artifact.SourceID); err != nil {
		return err
	}
	if err := validateUUID("artifact snapshot_id", artifact.SnapshotID); err != nil {
		return err
	}
	if err := validateText("artifact external_id", artifact.ExternalID); err != nil {
		return err
	}
	if err := validateText("artifact path", artifact.Path); err != nil {
		return err
	}
	if err := validateText("artifact type", artifact.Type); err != nil {
		return err
	}
	if err := validateDigest("artifact content_hash", artifact.ContentHash); err != nil {
		return err
	}
	if artifact.ContentSize < 0 {
		return fmt.Errorf("%w: artifact content_size is negative", ErrInvalidInput)
	}
	tag, err := u.execInsertTag(ctx, insertArtifactSQL, "insert artifact", artifact.ID, organizationID, artifact.SourceID, artifact.SnapshotID, artifact.ExternalID, artifact.Path, artifact.Type, artifact.ContentHash, artifact.ContentSize)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingArtifactMatches(ctx, organizationID, artifact)
}

func (u *UnitOfWork) existingArtifactMatches(ctx context.Context, organizationID string, artifact Artifact) error {
	var (
		sourceID, snapshotID, externalID, path, artifactType, contentHash string
		contentSize                                                       int64
	)
	err := u.tx.QueryRow(ctx, selectArtifactIdentitySQL, organizationID, artifact.ID).Scan(
		&sourceID, &snapshotID, &externalID, &path, &artifactType, &contentHash, &contentSize,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: artifact identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read artifact identity", err)
	}
	if sourceID != artifact.SourceID || snapshotID != artifact.SnapshotID || externalID != artifact.ExternalID ||
		path != artifact.Path || artifactType != artifact.Type || contentHash != artifact.ContentHash || contentSize != artifact.ContentSize {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertObservation(ctx context.Context, organizationID string, observation Observation) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"observation id", observation.ID}, {"observation source_id", observation.SourceID},
		{"observation snapshot_id", observation.SnapshotID}, {"observation artifact_id", observation.ArtifactID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct{ name, value string }{
		{"observation external_id", observation.ExternalID}, {"observation analyzer_id", observation.AnalyzerID},
		{"observation analyzer_version", observation.AnalyzerVersion}, {"observation method", observation.Method},
		{"observation type", observation.Type},
	} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	if observation.ObservedAt.IsZero() {
		return fmt.Errorf("%w: observation observed_at is required", ErrInvalidInput)
	}
	locator, err := marshalLocator(observation.Locator)
	if err != nil {
		return err
	}
	if err := validateJSON("observation value", observation.Value); err != nil {
		return err
	}
	tag, err := u.execInsertTag(ctx, insertObservationSQL, "insert observation",
		observation.ID, organizationID, observation.SourceID, observation.SnapshotID,
		observation.ArtifactID, observation.ExternalID, observation.AnalyzerID,
		observation.AnalyzerVersion, observation.Method, observation.Type, locator,
		nullableJSON(observation.Value), observation.ObservedAt,
	)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingObservationMatches(ctx, organizationID, observation)
}

func (u *UnitOfWork) existingObservationMatches(ctx context.Context, organizationID string, observation Observation) error {
	var (
		sourceID, snapshotID, artifactID, externalID, analyzerID string
		analyzerVersion, method, observationType                 string
		locator, value                                           []byte
		observedAt                                               time.Time
	)
	err := u.tx.QueryRow(ctx, selectObservationIdentitySQL, organizationID, observation.ID).Scan(
		&sourceID, &snapshotID, &artifactID, &externalID, &analyzerID,
		&analyzerVersion, &method, &observationType, &locator, &value, &observedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: observation identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read observation identity", err)
	}
	expectedLocator, marshalErr := marshalLocator(observation.Locator)
	if marshalErr != nil {
		return marshalErr
	}
	if sourceID != observation.SourceID || snapshotID != observation.SnapshotID || artifactID != observation.ArtifactID ||
		externalID != observation.ExternalID || analyzerID != observation.AnalyzerID || analyzerVersion != observation.AnalyzerVersion ||
		method != observation.Method || observationType != observation.Type || !jsonEqual(locator, expectedLocator) ||
		!jsonEqual(value, observation.Value) || !observedAt.Equal(observation.ObservedAt) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertEntity(ctx context.Context, organizationID string, entity Entity) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"entity id", entity.ID}, {"entity source_id", entity.SourceID}, {"entity snapshot_id", entity.SnapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct{ name, value string }{
		{"entity external_id", entity.ExternalID}, {"entity type", entity.Type},
	} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateOptionalText("entity name", entity.Name); err != nil {
		return err
	}
	attributes, err := validateJSONObject("entity attributes", entity.Attributes)
	if err != nil {
		return err
	}
	tag, err := u.execInsertTag(ctx, insertEntitySQL, "insert entity", entity.ID, organizationID, entity.SourceID, entity.SnapshotID, entity.ExternalID, entity.Type, nullableText(entity.Name), attributes)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingEntityMatches(ctx, organizationID, entity)
}

func (u *UnitOfWork) existingEntityMatches(ctx context.Context, organizationID string, entity Entity) error {
	var sourceID, snapshotID, externalID, entityType string
	var name *string
	var attributes []byte
	err := u.tx.QueryRow(ctx, selectEntityIdentitySQL, organizationID, entity.ID).Scan(
		&sourceID, &snapshotID, &externalID, &entityType, &name, &attributes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: entity identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read entity identity", err)
	}
	expectedName := entity.Name
	if sourceID != entity.SourceID || snapshotID != entity.SnapshotID || externalID != entity.ExternalID || entityType != entity.Type || optionalString(name) != expectedName || !jsonEqual(attributes, normalizedJSONObject(entity.Attributes)) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertRelationship(ctx context.Context, organizationID string, relationship Relationship) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"relationship id", relationship.ID}, {"relationship source_id", relationship.SourceID},
		{"relationship snapshot_id", relationship.SnapshotID}, {"relationship from_entity_id", relationship.FromEntityID},
		{"relationship to_entity_id", relationship.ToEntityID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct{ name, value string }{
		{"relationship external_id", relationship.ExternalID}, {"relationship type", relationship.Type},
	} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	attributes, err := validateJSONObject("relationship attributes", relationship.Attributes)
	if err != nil {
		return err
	}
	tag, err := u.execInsertTag(ctx, insertRelationshipSQL, "insert relationship",
		relationship.ID, organizationID, relationship.SourceID, relationship.SnapshotID,
		relationship.ExternalID, relationship.FromEntityID, relationship.ToEntityID,
		relationship.Type, attributes,
	)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingRelationshipMatches(ctx, organizationID, relationship)
}

func (u *UnitOfWork) existingRelationshipMatches(ctx context.Context, organizationID string, relationship Relationship) error {
	var sourceID, snapshotID, externalID, fromEntityID, toEntityID, relationshipType string
	var attributes []byte
	err := u.tx.QueryRow(ctx, selectRelationshipIdentitySQL, organizationID, relationship.ID).Scan(
		&sourceID, &snapshotID, &externalID, &fromEntityID, &toEntityID, &relationshipType, &attributes,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: relationship identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read relationship identity", err)
	}
	if sourceID != relationship.SourceID || snapshotID != relationship.SnapshotID || externalID != relationship.ExternalID ||
		fromEntityID != relationship.FromEntityID || toEntityID != relationship.ToEntityID || relationshipType != relationship.Type ||
		!jsonEqual(attributes, normalizedJSONObject(relationship.Attributes)) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertEvidence(ctx context.Context, organizationID string, item Evidence) error {
	if err := validateOrganizationScope(organizationID, item.OrganizationID); err != nil {
		return err
	}
	if err := validateUUID("evidence id", item.ID); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"evidence source_id", item.SourceID}, {"evidence snapshot_id", item.SnapshotID},
		{"evidence artifact_id", item.ArtifactID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	if item.ObservationID != "" {
		if err := validateUUID("evidence observation_id", item.ObservationID); err != nil {
			return err
		}
	}
	for _, field := range []struct{ name, value string }{
		{"evidence organization external_id", item.OrganizationExternalID},
		{"evidence source external_id", item.SourceExternalID},
		{"evidence snapshot external_id", item.SnapshotExternalID},
		{"evidence artifact external_id", item.ArtifactExternalID},
		{"evidence observation external_id", item.ObservationExternalID},
	} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	if item.Unit.OrganizationID != item.OrganizationExternalID || item.Unit.SourceID != item.SourceExternalID ||
		item.Unit.SnapshotID != item.SnapshotExternalID || item.Unit.ArtifactID != item.ArtifactExternalID ||
		item.Unit.Contribution.ArtifactID != item.ArtifactExternalID || item.Unit.Contribution.ID != item.ObservationExternalID {
		return fmt.Errorf("%w: evidence external identity mismatch", ErrInvalidInput)
	}
	// ValidatePrepared is the security boundary: callers cannot persist an
	// arbitrary content state merely by constructing a row-shaped value.
	if err := item.Unit.ValidatePrepared(); err != nil {
		return fmt.Errorf("%w: evidence is not prepared", ErrInvalidInput)
	}
	if item.Unit.ContentState == evidence.ContentStatePresent && item.Unit.Classification == evidence.ClassificationUnknown {
		return fmt.Errorf("%w: present evidence requires classification", ErrInvalidInput)
	}
	if item.Unit.ExternalTransfer == evidence.DecisionAllow && item.Unit.Classification == evidence.ClassificationUnknown {
		return fmt.Errorf("%w: transferable evidence requires classification", ErrInvalidInput)
	}
	provenance, err := validateJSONObject("evidence provenance", item.Provenance)
	if err != nil {
		return err
	}
	locator, err := marshalLocator(item.Unit.Locator)
	if err != nil {
		return err
	}
	findings := []byte("[]")
	if item.Unit.Findings != nil {
		findings, err = json.Marshal(item.Unit.Findings)
		if err != nil {
			return fmt.Errorf("%w: evidence findings cannot be encoded", ErrInvalidInput)
		}
	}
	var content any
	if item.Unit.ContentState != evidence.ContentStateOmitted {
		content = item.Unit.Content
	}
	var redactionReason any
	if item.Unit.RedactionReason != "" {
		redactionReason = item.Unit.RedactionReason
	}
	var observationID any
	if item.ObservationID != "" {
		observationID = item.ObservationID
	}
	tag, err := u.execInsertTag(ctx, insertEvidenceSQL, "insert evidence",
		item.ID, organizationID, item.SourceID, item.SnapshotID,
		item.ArtifactID, observationID, item.Unit.ID, locator,
		string(item.Unit.ContentState), content, item.Unit.ContentHash,
		item.Unit.ContentBytes, item.Unit.ContentCharacters, item.Unit.Truncated,
		string(item.Unit.Classification), findings, string(item.Unit.Persist),
		string(item.Unit.ExternalTransfer), redactionReason, provenance,
	)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingEvidenceMatches(ctx, organizationID, item, locator, findings, provenance)
}

func (u *UnitOfWork) existingEvidenceMatches(ctx context.Context, organizationID string, item Evidence, locator, findings, provenance []byte) error {
	var (
		sourceID, snapshotID, artifactID, externalID, contentState     string
		contentHash, classification, persistDecision, transferDecision string
		observationID, content, redactionReason                        *string
		storedLocator, storedFindings, storedProvenance                []byte
		contentBytes, contentCharacters                                int64
		truncated                                                      bool
	)
	err := u.tx.QueryRow(ctx, selectEvidenceIdentitySQL, organizationID, item.ID).Scan(
		&sourceID, &snapshotID, &artifactID, &observationID, &externalID,
		&storedLocator, &contentState, &content, &contentHash, &contentBytes,
		&contentCharacters, &truncated, &classification, &storedFindings,
		&persistDecision, &transferDecision, &redactionReason, &storedProvenance,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: evidence identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read evidence identity", err)
	}
	var expectedContent *string
	if item.Unit.ContentState != evidence.ContentStateOmitted {
		value := item.Unit.Content
		expectedContent = &value
	}
	var expectedRedaction *string
	if item.Unit.RedactionReason != "" {
		value := item.Unit.RedactionReason
		expectedRedaction = &value
	}
	if sourceID != item.SourceID || snapshotID != item.SnapshotID || artifactID != item.ArtifactID ||
		optionalString(observationID) != item.ObservationID || externalID != item.Unit.ID ||
		!jsonEqual(storedLocator, locator) || contentState != string(item.Unit.ContentState) ||
		optionalString(content) != optionalString(expectedContent) || contentHash != item.Unit.ContentHash ||
		contentBytes != item.Unit.ContentBytes || contentCharacters != item.Unit.ContentCharacters ||
		truncated != item.Unit.Truncated || classification != string(item.Unit.Classification) ||
		!jsonEqual(storedFindings, findings) || persistDecision != string(item.Unit.Persist) ||
		transferDecision != string(item.Unit.ExternalTransfer) || optionalString(redactionReason) != optionalString(expectedRedaction) ||
		!jsonEqual(storedProvenance, provenance) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertCoverage(ctx context.Context, organizationID string, coverage Coverage) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"coverage id", coverage.ID}, {"coverage source_id", coverage.SourceID}, {"coverage snapshot_id", coverage.SnapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	value := coverage.Value
	if err := value.Validate(); err != nil {
		return fmt.Errorf("%w: coverage is invalid", ErrInvalidInput)
	}
	if err := validateText("coverage external id", value.ID); err != nil {
		return err
	}
	if err := validateText("coverage dimension", value.Dimension); err != nil {
		return err
	}
	if err := validateText("coverage state", string(value.State)); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"coverage scope", value.Scope}, {"coverage analyzer_id", value.AnalyzerID}, {"coverage message", value.Message},
	} {
		if err := validateOptionalText(field.name, field.value); err != nil {
			return err
		}
	}
	locator, err := marshalOptionalLocator(value.Locator)
	if err != nil {
		return err
	}
	tag, err := u.execInsertTag(ctx, insertCoverageSQL, "insert coverage", coverage.ID, organizationID, coverage.SourceID, coverage.SnapshotID, value.ID, value.Dimension, nullableText(value.Scope), string(value.State), nullableText(value.AnalyzerID), nullableText(value.Message), locator)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingCoverageMatches(ctx, organizationID, coverage, locator)
}

func (u *UnitOfWork) existingCoverageMatches(ctx context.Context, organizationID string, coverage Coverage, locator []byte) error {
	var sourceID, snapshotID, externalID, dimension, state string
	var scope, analyzerID, message *string
	var storedLocator []byte
	err := u.tx.QueryRow(ctx, selectCoverageIdentitySQL, organizationID, coverage.ID).Scan(&sourceID, &snapshotID, &externalID, &dimension, &scope, &state, &analyzerID, &message, &storedLocator)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: coverage identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read coverage identity", err)
	}
	value := coverage.Value
	if sourceID != coverage.SourceID || snapshotID != coverage.SnapshotID || externalID != value.ID || dimension != value.Dimension ||
		optionalString(scope) != value.Scope || state != string(value.State) || optionalString(analyzerID) != value.AnalyzerID ||
		optionalString(message) != value.Message || !jsonEqual(storedLocator, locator) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertGap(ctx context.Context, organizationID string, gap Gap) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"gap id", gap.ID}, {"gap source_id", gap.SourceID}, {"gap snapshot_id", gap.SnapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	value := gap.Value
	if err := value.Validate(); err != nil {
		return fmt.Errorf("%w: gap is invalid", ErrInvalidInput)
	}
	if err := validateText("gap external id", value.ID); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{{"gap code", value.Code}, {"gap message", value.Message}} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct{ name, value string }{{"gap dimension", value.Dimension}, {"gap scope", value.Scope}, {"gap analyzer_id", value.AnalyzerID}} {
		if err := validateOptionalText(field.name, field.value); err != nil {
			return err
		}
	}
	locator, err := marshalOptionalLocator(value.Locator)
	if err != nil {
		return err
	}
	tag, err := u.execInsertTag(ctx, insertGapSQL, "insert gap", gap.ID, organizationID, gap.SourceID, gap.SnapshotID, value.ID, value.Code, nullableText(value.Dimension), nullableText(value.Scope), value.Message, nullableText(value.AnalyzerID), locator)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingGapMatches(ctx, organizationID, gap, locator)
}

func (u *UnitOfWork) existingGapMatches(ctx context.Context, organizationID string, gap Gap, locator []byte) error {
	var sourceID, snapshotID, externalID, code string
	var dimension, scope, message, analyzerID *string
	var storedLocator []byte
	err := u.tx.QueryRow(ctx, selectGapIdentitySQL, organizationID, gap.ID).Scan(&sourceID, &snapshotID, &externalID, &code, &dimension, &scope, &message, &analyzerID, &storedLocator)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: gap identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read gap identity", err)
	}
	value := gap.Value
	if sourceID != gap.SourceID || snapshotID != gap.SnapshotID || externalID != value.ID || code != value.Code || optionalString(dimension) != value.Dimension ||
		optionalString(scope) != value.Scope || optionalString(message) != value.Message || optionalString(analyzerID) != value.AnalyzerID || !jsonEqual(storedLocator, locator) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertFailure(ctx context.Context, organizationID string, failure Failure) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{{"failure id", failure.ID}, {"failure source_id", failure.SourceID}, {"failure snapshot_id", failure.SnapshotID}} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	value := failure.Value
	if err := value.Validate(); err != nil {
		return fmt.Errorf("%w: failure is invalid", ErrInvalidInput)
	}
	if err := validateText("failure external id", value.ID); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{{"failure code", value.Code}, {"failure operation", value.Operation}, {"failure message", value.Message}} {
		if err := validateText(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateOptionalText("failure analyzer_id", value.AnalyzerID); err != nil {
		return err
	}
	if err := validateOptionalText("failure artifact external_id", value.ArtifactID); err != nil {
		return err
	}
	if err := validateOptionalText("failure artifact external_id", failure.ArtifactExternalID); err != nil {
		return err
	}
	if failure.ArtifactExternalID != "" && failure.ArtifactExternalID != value.ArtifactID {
		return fmt.Errorf("%w: failure artifact external identity mismatch", ErrInvalidInput)
	}
	if failure.ArtifactID != "" {
		if err := validateUUID("failure artifact_id", failure.ArtifactID); err != nil {
			return err
		}
	}
	var artifactID any
	if failure.ArtifactID != "" {
		artifactID = failure.ArtifactID
	}
	locator, err := marshalOptionalLocator(value.Locator)
	if err != nil {
		return err
	}
	tag, err := u.execInsertTag(ctx, insertFailureSQL, "insert failure", failure.ID, organizationID, failure.SourceID, failure.SnapshotID, artifactID, value.ID, value.Code, value.Operation, value.Message, nullableText(value.AnalyzerID), value.Partial, locator)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingFailureMatches(ctx, organizationID, failure, locator)
}

func (u *UnitOfWork) existingFailureMatches(ctx context.Context, organizationID string, failure Failure, locator []byte) error {
	var sourceID, snapshotID, externalID, code, operation, message string
	var artifactID, analyzerID *string
	var partial bool
	var storedLocator []byte
	err := u.tx.QueryRow(ctx, selectFailureIdentitySQL, organizationID, failure.ID).Scan(&sourceID, &snapshotID, &artifactID, &externalID, &code, &operation, &message, &analyzerID, &partial, &storedLocator)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: failure identity", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read failure identity", err)
	}
	value := failure.Value
	if sourceID != failure.SourceID || snapshotID != failure.SnapshotID || optionalString(artifactID) != failure.ArtifactID || externalID != value.ID ||
		code != value.Code || operation != value.Operation || message != value.Message || optionalString(analyzerID) != value.AnalyzerID ||
		partial != value.Partial || !jsonEqual(storedLocator, locator) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) InsertFactualIdentity(ctx context.Context, organizationID string, identity FactualIdentity) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"factual identity id", identity.ID}, {"factual identity source_id", identity.SourceID}, {"factual identity snapshot_id", identity.SnapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	if err := validateText("factual identity key", identity.IdentityKey); err != nil {
		return err
	}
	if err := validateDigest("factual identity digest", identity.FactualDigest); err != nil {
		return err
	}
	if identity.State != "" && identity.State != "historical" {
		return fmt.Errorf("%w: factual identity inserts must be historical", ErrInvalidInput)
	}
	if identity.ObservedAt.IsZero() {
		return fmt.Errorf("%w: factual identity observed_at is required", ErrInvalidInput)
	}
	tag, err := u.execInsertTag(ctx, insertFactualIdentitySQL, "insert factual identity", identity.ID, organizationID, identity.SourceID, identity.SnapshotID, identity.IdentityKey, identity.FactualDigest, "historical", identity.ObservedAt)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	return u.existingFactualIdentityMatches(ctx, organizationID, identity)
}

func (u *UnitOfWork) existingFactualIdentityMatches(ctx context.Context, organizationID string, identity FactualIdentity) error {
	var sourceID, snapshotID, identityKey, factualDigest, state string
	var observedAt time.Time
	err := u.tx.QueryRow(ctx, selectFactualIdentitySQL, organizationID, identity.ID).Scan(&sourceID, &snapshotID, &identityKey, &factualDigest, &state, &observedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: factual identity conflict", ErrConflict)
		}
		return wrapPersistenceError(ctx, "read factual identity", err)
	}
	if state != "active" && state != "historical" {
		return fmt.Errorf("%w: factual identity state is invalid", ErrInconsistent)
	}
	if sourceID != identity.SourceID || snapshotID != identity.SnapshotID || identityKey != identity.IdentityKey ||
		factualDigest != identity.FactualDigest || !observedAt.Equal(identity.ObservedAt) {
		return ErrConflict
	}
	return nil
}

// ActivateSnapshot atomically changes the active view for one source. The
// source lock is acquired before validating the target snapshot; every query
// carries organization and source scope, and all updates run in the same
// UnitOfWork transaction.
func (u *UnitOfWork) ActivateSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string) error {
	if err := validateOrganizationScope(organizationID, ""); err != nil {
		return err
	}
	for _, field := range []struct{ name, value string }{
		{"activation source_id", sourceID}, {"activation snapshot_id", snapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return err
		}
	}
	if err := expectOneRow(ctx, u.tx, lockSourceSQL, "source", organizationID, sourceID); err != nil {
		return err
	}
	if err := expectOneRow(ctx, u.tx, lockSnapshotSQL, "snapshot", organizationID, sourceID, snapshotID); err != nil {
		return err
	}
	if _, err := u.tx.Exec(ctx, archiveActiveIdentitiesSQL, organizationID, sourceID); err != nil {
		return wrapPersistenceError(ctx, "archive active identities", err)
	}
	if _, err := u.tx.Exec(ctx, activateSnapshotIdentitiesSQL, organizationID, sourceID, snapshotID); err != nil {
		return wrapPersistenceError(ctx, "activate snapshot identities", err)
	}
	tag, err := u.tx.Exec(ctx, activateSourceSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return wrapPersistenceError(ctx, "activate source snapshot", err)
	}
	return checkedRowsAffected(tag, 1, "source activation")
}

func expectOneRow(ctx context.Context, tx transaction, query, name string, args ...any) error {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return wrapPersistenceError(ctx, "lock "+name, err)
	}
	count := 0
	var id string
	for rows.Next() {
		count++
		if count > 1 {
			rows.Close()
			return fmt.Errorf("%w: multiple %s rows", ErrInconsistent, name)
		}
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return wrapPersistenceError(ctx, "scan "+name, err)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return wrapPersistenceError(ctx, "read "+name, err)
	}
	if count == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if id == "" {
		return fmt.Errorf("%w: %s id is empty", ErrInconsistent, name)
	}
	return nil
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return []byte(raw)
}
