package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

const (
	readFactualSnapshotScopeSQL = `
SELECT organization.external_id, source.external_id, snapshot.external_id,
       snapshot.captured_at
FROM analysis_snapshots AS snapshot
JOIN sources AS source
  ON source.organization_id = snapshot.organization_id
 AND source.id = snapshot.source_id
JOIN organizations AS organization
  ON organization.id = snapshot.organization_id
WHERE snapshot.organization_id = $1
  AND snapshot.source_id = $2
  AND snapshot.id = $3`

	readFactualManifestsSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       external_id, manifest_version, frontend_id, version, method,
       execution_profile, manifest, manifest_digest
FROM frontend_manifests
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3
ORDER BY external_id, frontend_id, version, method, id`

	readFactualSchemasSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       frontend_manifest_id::text, schema_id, version, digest
FROM frontend_extension_schemas
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3
ORDER BY frontend_manifest_id, schema_id, version, id`

	readFactualRulesSQL = `
SELECT DISTINCT rv.id::text, rv.organization_id::text, rv.rule_id, rv.version,
       rv.implementation_digest, rv.configuration
FROM rule_versions AS rv
JOIN canonical_facts AS fact
  ON fact.organization_id = rv.organization_id
 AND fact.rule_version_id = rv.id
WHERE fact.organization_id = $1 AND fact.source_id = $2
  AND fact.snapshot_id = $3
ORDER BY rv.rule_id, rv.version, rv.id::text`

	readFactualFactsSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       identity_key, fact_version, fact_kind, predicate,
       subject_kind, subject_id, object_kind, object_id, typed_value,
       frontend_manifest_id::text, producer_id, producer_version,
       producer_method, rule_version_id::text, observed_at
FROM canonical_facts
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3
ORDER BY identity_key, id`

	readFactualQualifiersSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       fact_id::text, ordinal, name, typed_value
FROM canonical_fact_qualifiers
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3
ORDER BY fact_id, ordinal, name, id`

	readFactualEvidenceSQL = `
SELECT link.id::text, link.organization_id::text, link.source_id::text,
       link.snapshot_id::text, link.fact_id::text, link.evidence_unit_id::text,
       link.ordinal, unit.organization_id::text, unit.source_id::text,
       unit.snapshot_id::text, unit.external_id, unit.artifact_id::text,
       artifact.external_id, unit.locator
FROM canonical_fact_evidence AS link
JOIN evidence_units AS unit
  ON unit.organization_id = link.organization_id
 AND unit.source_id = link.source_id
 AND unit.snapshot_id = link.snapshot_id
 AND unit.id = link.evidence_unit_id
JOIN artifacts AS artifact
  ON artifact.organization_id = unit.organization_id
 AND artifact.source_id = unit.source_id
 AND artifact.snapshot_id = unit.snapshot_id
 AND artifact.id = unit.artifact_id
WHERE link.organization_id = $1 AND link.source_id = $2
  AND link.snapshot_id = $3
ORDER BY link.fact_id, link.ordinal, link.evidence_unit_id, link.id`

	readFactualInputsSQL = `
SELECT id::text, organization_id::text, source_id::text, snapshot_id::text,
       fact_id::text, input_fact_id::text, rule_version_id::text,
       fact_kind, ordinal
FROM canonical_fact_inputs
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3
ORDER BY fact_id, ordinal, input_fact_id, id`
)

// ReadFactualSnapshot reconstructs one detached factual snapshot from the
// canonical PostgreSQL substrate. It validates the relational identities,
// manifest JSON, support rows, and deterministic fact identities before
// returning anything to the caller.
func (r *Repository) ReadFactualSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string) (FactualSnapshotInput, error) {
	if err := validateContext(ctx); err != nil {
		return FactualSnapshotInput{}, err
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
			return FactualSnapshotInput{}, err
		}
	}

	var result FactualSnapshotInput
	err := r.WithinTx(ctx, func(u *UnitOfWork) error {
		var err error
		result, err = u.readFactualSnapshot(ctx, organizationID, sourceID, snapshotID)
		return err
	})
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	return result, nil
}

type factualReadScope struct {
	organizationID string
	sourceID       string
	snapshotID     string
	capturedAt     time.Time
}

type factualReadManifest struct {
	id              string
	organizationID  string
	sourceID        string
	snapshotID      string
	externalID      string
	manifestVersion string
	frontendID      string
	version         string
	method          string
	execution       string
	manifestJSON    []byte
	digest          string
	manifest        fact.FrontendManifest
}

type factualReadSchema struct {
	id             string
	organizationID string
	sourceID       string
	snapshotID     string
	manifestID     string
	schema         fact.ExtensionSchema
}

type factualReadFact struct {
	id             string
	organizationID string
	sourceID       string
	snapshotID     string
	identityKey    string
	factVersion    string
	kind           string
	predicate      string
	subjectKind    string
	subjectID      string
	objectKind     *string
	objectID       *string
	typedValue     []byte
	frontendID     *string
	producerID     string
	producerVer    string
	producerMethod string
	ruleID         *string
	observedAt     time.Time
	value          fact.CanonicalFact
}

type factualReadRule struct {
	id             string
	organizationID string
	rule           RuleVersion
}

type factualReadQualifier struct {
	id             string
	organizationID string
	sourceID       string
	snapshotID     string
	factID         string
	ordinal        int64
	name           string
	typedValue     json.RawMessage
}

type factualReadEvidence struct {
	id                 string
	organizationID     string
	sourceID           string
	snapshotID         string
	factID             string
	evidenceID         string
	ordinal            int64
	unitOrgID          string
	unitSourceID       string
	unitSnapshotID     string
	externalID         string
	artifactID         string
	artifactExternalID string
	locatorJSON        []byte
}

type factualReadInput struct {
	id             string
	organizationID string
	sourceID       string
	snapshotID     string
	factID         string
	inputFactID    string
	ruleID         string
	factKind       string
	ordinal        int64
}

func (u *UnitOfWork) readFactualSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string) (FactualSnapshotInput, error) {
	if u == nil || u.tx == nil {
		return FactualSnapshotInput{}, fmt.Errorf("%w: unit of work is not configured", ErrInvalidInput)
	}
	scope, err := u.readFactualScope(ctx, organizationID, sourceID, snapshotID)
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	manifests, manifestsByID, err := u.readFactualManifests(ctx, scope)
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	if err := u.readFactualSchemas(ctx, scope, manifests, manifestsByID); err != nil {
		return FactualSnapshotInput{}, err
	}
	facts, factsByID, err := u.readFactualFacts(ctx, scope)
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	qualifiers, err := u.readStoredFactualQualifiers(ctx, scope)
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	evidenceRows, err := u.readFactualEvidence(ctx, scope)
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	inputs, err := u.readStoredFactualInputs(ctx, scope)
	if err != nil {
		return FactualSnapshotInput{}, err
	}
	rules, rulesByID, err := u.readFactualRules(ctx, scope)
	if err != nil {
		return FactualSnapshotInput{}, err
	}

	if err := attachFactualReadSupports(scope, factsByID, qualifiers, evidenceRows, inputs, rulesByID); err != nil {
		return FactualSnapshotInput{}, err
	}
	if err := validateFactualReadRows(scope, manifests, facts, rules, factsByID, rulesByID); err != nil {
		return FactualSnapshotInput{}, err
	}

	input := FactualSnapshotInput{
		OrganizationID:    organizationID,
		SourceID:          sourceID,
		SnapshotID:        snapshotID,
		Scope:             fact.Scope{OrganizationID: scope.organizationID, SourceID: scope.sourceID, SnapshotID: scope.snapshotID},
		FrontendManifests: make([]fact.FrontendManifest, 0, len(manifests)),
		RuleVersions:      make([]RuleVersion, 0, len(rules)),
		Facts:             make([]fact.CanonicalFact, 0, len(facts)),
	}
	for _, manifest := range manifests {
		input.FrontendManifests = append(input.FrontendManifests, manifest.manifest)
	}
	for _, rule := range rules {
		input.RuleVersions = append(input.RuleVersions, rule.rule)
	}
	for _, item := range facts {
		input.Facts = append(input.Facts, item.value)
	}

	// Reuse the established pure preparation boundary so the read API returns
	// exactly the same detached/canonical shape accepted by the writer.
	prepared, err := prepareFactualSnapshot(input)
	if err != nil {
		return FactualSnapshotInput{}, factualReadInconsistent()
	}
	return factualInputFromPrepared(prepared), nil
}

func (u *UnitOfWork) readFactualScope(ctx context.Context, organizationID, sourceID, snapshotID string) (factualReadScope, error) {
	var result factualReadScope
	err := u.tx.QueryRow(ctx, readFactualSnapshotScopeSQL, organizationID, sourceID, snapshotID).Scan(
		&result.organizationID, &result.sourceID, &result.snapshotID, &result.capturedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return factualReadScope{}, ErrNotFound
		}
		return factualReadScope{}, wrapPersistenceError(ctx, "read factual snapshot scope", err)
	}
	if result.capturedAt.IsZero() || result.organizationID == "" || result.sourceID == "" || result.snapshotID == "" {
		return factualReadScope{}, factualReadInconsistent()
	}
	if err := (fact.Scope{OrganizationID: result.organizationID, SourceID: result.sourceID, SnapshotID: result.snapshotID}).Validate(); err != nil {
		return factualReadScope{}, factualReadInconsistent()
	}
	if organizationID != identity.CanonicalUUID("organization", result.organizationID) ||
		sourceID != identity.CanonicalUUID("source", result.organizationID, result.sourceID) ||
		snapshotID != identity.CanonicalUUID("snapshot", result.organizationID, result.sourceID, result.snapshotID) {
		return factualReadScope{}, factualReadInconsistent()
	}
	return result, nil
}

func (u *UnitOfWork) readFactualManifests(ctx context.Context, scope factualReadScope) ([]factualReadManifest, map[string]*factualReadManifest, error) {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualManifestsSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, nil, wrapPersistenceError(ctx, "read factual frontend manifests", err)
	}
	defer rows.Close()
	result := make([]factualReadManifest, 0)
	byID := make(map[string]*factualReadManifest)
	for rows.Next() {
		var row factualReadManifest
		if err := rows.Scan(&row.id, &row.organizationID, &row.sourceID, &row.snapshotID, &row.externalID,
			&row.manifestVersion, &row.frontendID, &row.version, &row.method, &row.execution,
			&row.manifestJSON, &row.digest); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if _, exists := byID[row.id]; exists {
			return nil, nil, factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID, row.sourceID, row.snapshotID); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.sourceID != sourceID || row.snapshotID != snapshotID {
			return nil, nil, factualReadInconsistent()
		}
		if err := decodeReadManifest(&row, scope); err != nil {
			return nil, nil, err
		}
		rowCopy := row
		result = append(result, rowCopy)
		byID[row.id] = &rowCopy
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapPersistenceError(ctx, "read factual frontend manifests", err)
	}
	return result, byID, nil
}

func decodeReadManifest(row *factualReadManifest, scope factualReadScope) error {
	if row == nil {
		return factualReadInconsistent()
	}
	if row.externalID == "" || row.manifestVersion == "" || row.frontendID == "" ||
		row.version == "" || row.method == "" || row.execution == "" {
		return factualReadInconsistent()
	}
	if err := validateDigest("frontend manifest digest", row.digest); err != nil {
		return factualReadInconsistent()
	}
	if len(bytes.TrimSpace(row.manifestJSON)) == 0 || !json.Valid(row.manifestJSON) {
		return factualReadInconsistent()
	}
	if err := json.Unmarshal(row.manifestJSON, &row.manifest); err != nil {
		return factualReadInconsistent()
	}
	canonical, err := fact.CanonicalFrontendManifest(row.manifest)
	if err != nil {
		return factualReadInconsistent()
	}
	encoded, err := fact.CanonicalFrontendManifestBytes(canonical)
	if err != nil {
		return factualReadInconsistent()
	}
	digest, err := fact.FrontendManifestDigest(canonical)
	if err != nil || digest != row.digest || !jsonEqual(row.manifestJSON, encoded) {
		return factualReadInconsistent()
	}
	if row.externalID != canonical.ID || row.manifestVersion != canonical.ManifestVersion ||
		row.frontendID != canonical.ID || row.version != canonical.Version ||
		row.method != canonical.Method || row.execution != string(canonical.Execution) {
		return factualReadInconsistent()
	}
	if row.id != identity.CanonicalUUID("frontend-manifest", scope.organizationID, scope.sourceID, scope.snapshotID, canonical.ID, canonical.Version, canonical.Method) {
		return factualReadInconsistent()
	}
	row.manifest = canonical
	return nil
}

func (u *UnitOfWork) readFactualSchemas(ctx context.Context, scope factualReadScope, manifests []factualReadManifest, manifestsByID map[string]*factualReadManifest) error {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualSchemasSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return wrapPersistenceError(ctx, "read factual extension schemas", err)
	}
	defer rows.Close()
	seen := make(map[string]map[string]struct{}, len(manifests))
	for _, manifest := range manifests {
		seen[manifest.id] = make(map[string]struct{}, len(manifest.manifest.Extensions))
	}
	for rows.Next() {
		var row factualReadSchema
		var schemaID, version, digest string
		if err := rows.Scan(&row.id, &row.organizationID, &row.sourceID, &row.snapshotID,
			&row.manifestID, &schemaID, &version, &digest); err != nil {
			return factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID, row.sourceID, row.snapshotID, row.manifestID); err != nil {
			return factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.sourceID != sourceID || row.snapshotID != snapshotID {
			return factualReadInconsistent()
		}
		manifest, exists := manifestsByID[row.manifestID]
		if !exists || manifest == nil {
			return factualReadInconsistent()
		}
		row.schema = fact.ExtensionSchema{ID: schemaID, Version: version, Digest: digest}
		if err := row.schema.Validate(); err != nil {
			return factualReadInconsistent()
		}
		key := row.schema.ID + "\x00" + row.schema.Version
		if _, duplicate := seen[row.manifestID][key]; duplicate {
			return factualReadInconsistent()
		}
		seen[row.manifestID][key] = struct{}{}
		expectedSchemaID := identity.CanonicalUUID("extension-schema", scope.organizationID, scope.sourceID, scope.snapshotID, manifest.manifest.ID, row.schema.ID, row.schema.Version)
		if row.id != expectedSchemaID {
			return factualReadInconsistent()
		}
		var found bool
		for _, declared := range manifest.manifest.Extensions {
			if declared.ID == row.schema.ID && declared.Version == row.schema.Version {
				found = declared.Digest == row.schema.Digest
				break
			}
		}
		if !found {
			return factualReadInconsistent()
		}
	}
	if err := rows.Err(); err != nil {
		return wrapPersistenceError(ctx, "read factual extension schemas", err)
	}
	for _, manifest := range manifests {
		declared := make(map[string]fact.ExtensionSchema, len(manifest.manifest.Extensions))
		for _, schema := range manifest.manifest.Extensions {
			key := schema.ID + "\x00" + schema.Version
			if _, duplicate := declared[key]; duplicate {
				return factualReadInconsistent()
			}
			declared[key] = schema
		}
		if len(declared) != len(seen[manifest.id]) {
			return factualReadInconsistent()
		}
	}
	return nil
}

func (u *UnitOfWork) readFactualFacts(ctx context.Context, scope factualReadScope) ([]*factualReadFact, map[string]*factualReadFact, error) {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualFactsSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, nil, wrapPersistenceError(ctx, "read canonical facts", err)
	}
	defer rows.Close()
	result := make([]*factualReadFact, 0)
	byID := make(map[string]*factualReadFact)
	seenIdentity := make(map[string]struct{})
	for rows.Next() {
		row := &factualReadFact{}
		if err := rows.Scan(&row.id, &row.organizationID, &row.sourceID, &row.snapshotID,
			&row.identityKey, &row.factVersion, &row.kind, &row.predicate,
			&row.subjectKind, &row.subjectID, &row.objectKind, &row.objectID,
			&row.typedValue, &row.frontendID, &row.producerID, &row.producerVer,
			&row.producerMethod, &row.ruleID, &row.observedAt); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID, row.sourceID, row.snapshotID); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.sourceID != sourceID || row.snapshotID != snapshotID || row.observedAt.IsZero() {
			return nil, nil, factualReadInconsistent()
		}
		if row.identityKey == "" {
			return nil, nil, factualReadInconsistent()
		}
		if _, duplicate := seenIdentity[row.identityKey]; duplicate {
			return nil, nil, factualReadInconsistent()
		}
		seenIdentity[row.identityKey] = struct{}{}
		if row.id != identity.CanonicalUUID("canonical-fact", scope.organizationID, scope.sourceID, scope.snapshotID, row.identityKey) {
			return nil, nil, factualReadInconsistent()
		}
		if row.kind != factualFactKindObserved && row.kind != factualFactKindDerived {
			return nil, nil, factualReadInconsistent()
		}
		if (row.objectKind == nil) != (row.objectID == nil) || (row.frontendID == nil) == (row.ruleID == nil) {
			return nil, nil, factualReadInconsistent()
		}
		if row.kind == factualFactKindObserved && (row.frontendID == nil || row.ruleID != nil) {
			return nil, nil, factualReadInconsistent()
		}
		if row.kind == factualFactKindDerived && (row.frontendID != nil || row.ruleID == nil) {
			return nil, nil, factualReadInconsistent()
		}
		if row.kind == factualFactKindObserved && row.observedAt.IsZero() {
			return nil, nil, factualReadInconsistent()
		}
		if err := populateFactualReadFact(row, scope); err != nil {
			return nil, nil, err
		}
		result = append(result, row)
		byID[row.id] = row
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapPersistenceError(ctx, "read canonical facts", err)
	}
	return result, byID, nil
}

func (u *UnitOfWork) readStoredFactualQualifiers(ctx context.Context, scope factualReadScope) ([]factualReadQualifier, error) {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualQualifiersSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read factual qualifiers", err)
	}
	defer rows.Close()
	result := make([]factualReadQualifier, 0)
	for rows.Next() {
		var row factualReadQualifier
		if err := rows.Scan(&row.id, &row.organizationID, &row.sourceID, &row.snapshotID,
			&row.factID, &row.ordinal, &row.name, &row.typedValue); err != nil {
			return nil, factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID, row.sourceID, row.snapshotID, row.factID); err != nil {
			return nil, factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.sourceID != sourceID || row.snapshotID != snapshotID || row.ordinal < 0 || row.name == "" {
			return nil, factualReadInconsistent()
		}
		if len(bytes.TrimSpace(row.typedValue)) == 0 || !json.Valid(row.typedValue) {
			return nil, factualReadInconsistent()
		}
		if _, err := decodeReadTypedValue(row.typedValue); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read factual qualifiers", err)
	}
	return result, nil
}

func populateFactualReadFact(row *factualReadFact, scope factualReadScope) error {
	if row == nil {
		return factualReadInconsistent()
	}
	value := fact.CanonicalFact{
		Version: row.factVersion,
		ID:      row.identityKey,
		Scope: fact.Scope{
			OrganizationID: scope.organizationID,
			SourceID:       scope.sourceID,
			SnapshotID:     scope.snapshotID,
		},
		Predicate: fact.Predicate(row.predicate),
		Subject: fact.Participant{
			Kind: fact.ParticipantKind(row.subjectKind),
			ID:   row.subjectID,
		},
		Producer: fact.Producer{
			ID:      row.producerID,
			Version: row.producerVer,
			Method:  row.producerMethod,
		},
	}
	if row.objectKind != nil {
		value.Object = &fact.Participant{Kind: fact.ParticipantKind(*row.objectKind), ID: *row.objectID}
	}
	if len(bytes.TrimSpace(row.typedValue)) != 0 {
		typedValue, err := decodeReadTypedValue(row.typedValue)
		if err != nil {
			return err
		}
		value.Value = &typedValue
	}
	row.value = value
	return nil
}

func (u *UnitOfWork) readFactualEvidence(ctx context.Context, scope factualReadScope) ([]factualReadEvidence, error) {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualEvidenceSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read factual evidence", err)
	}
	defer rows.Close()
	result := make([]factualReadEvidence, 0)
	for rows.Next() {
		var row factualReadEvidence
		if err := rows.Scan(&row.id, &row.organizationID, &row.sourceID, &row.snapshotID,
			&row.factID, &row.evidenceID, &row.ordinal, &row.unitOrgID,
			&row.unitSourceID, &row.unitSnapshotID, &row.externalID, &row.artifactID,
			&row.artifactExternalID, &row.locatorJSON); err != nil {
			return nil, factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID, row.sourceID, row.snapshotID,
			row.factID, row.evidenceID, row.unitOrgID, row.unitSourceID, row.unitSnapshotID, row.artifactID); err != nil {
			return nil, factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.sourceID != sourceID || row.snapshotID != snapshotID ||
			row.unitOrgID != organizationID || row.unitSourceID != sourceID || row.unitSnapshotID != snapshotID ||
			row.ordinal < 0 || row.externalID == "" || row.artifactExternalID == "" {
			return nil, factualReadInconsistent()
		}
		if row.evidenceID != identity.CanonicalUUID("evidence", scope.organizationID, scope.sourceID, scope.snapshotID, row.externalID) {
			return nil, factualReadInconsistent()
		}
		locator, err := decodeReadLocator(row.locatorJSON)
		if err != nil {
			return nil, err
		}
		if locator.SourceID != "" && locator.SourceID != scope.sourceID ||
			locator.ArtifactID != "" && locator.ArtifactID != row.artifactExternalID {
			return nil, factualReadInconsistent()
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read factual evidence", err)
	}
	return result, nil
}

func (u *UnitOfWork) readStoredFactualInputs(ctx context.Context, scope factualReadScope) ([]factualReadInput, error) {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualInputsSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read factual lineage inputs", err)
	}
	defer rows.Close()
	result := make([]factualReadInput, 0)
	for rows.Next() {
		var row factualReadInput
		if err := rows.Scan(&row.id, &row.organizationID, &row.sourceID, &row.snapshotID,
			&row.factID, &row.inputFactID, &row.ruleID, &row.factKind, &row.ordinal); err != nil {
			return nil, factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID, row.sourceID, row.snapshotID, row.factID, row.inputFactID, row.ruleID); err != nil {
			return nil, factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.sourceID != sourceID || row.snapshotID != snapshotID ||
			row.ordinal < 0 || row.factKind != factualFactKindDerived || row.factID == row.inputFactID {
			return nil, factualReadInconsistent()
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read factual lineage inputs", err)
	}
	return result, nil
}

func (u *UnitOfWork) readFactualRules(ctx context.Context, scope factualReadScope) ([]factualReadRule, map[string]*factualReadRule, error) {
	organizationID := identity.CanonicalUUID("organization", scope.organizationID)
	sourceID := identity.CanonicalUUID("source", scope.organizationID, scope.sourceID)
	snapshotID := identity.CanonicalUUID("snapshot", scope.organizationID, scope.sourceID, scope.snapshotID)
	rows, err := u.tx.Query(ctx, readFactualRulesSQL, organizationID, sourceID, snapshotID)
	if err != nil {
		return nil, nil, wrapPersistenceError(ctx, "read factual rule versions", err)
	}
	defer rows.Close()
	result := make([]factualReadRule, 0)
	byID := make(map[string]*factualReadRule)
	for rows.Next() {
		var row factualReadRule
		var ruleID, version, digest string
		var configuration []byte
		if err := rows.Scan(&row.id, &row.organizationID, &ruleID, &version, &digest, &configuration); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if err := validateReadUUIDs(row.id, row.organizationID); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if row.organizationID != organizationID || row.id != identity.CanonicalUUID("rule-version", scope.organizationID, ruleID, version) {
			return nil, nil, factualReadInconsistent()
		}
		canonicalConfiguration, err := canonicalJSONObject("rule configuration", configuration)
		if err != nil {
			return nil, nil, factualReadInconsistent()
		}
		if err := validateDigest("rule implementation digest", digest); err != nil {
			return nil, nil, factualReadInconsistent()
		}
		row.rule = RuleVersion{RuleID: ruleID, Version: version, ImplementationDigest: digest, Configuration: append(json.RawMessage(nil), canonicalConfiguration...)}
		if _, duplicate := byID[row.id]; duplicate {
			return nil, nil, factualReadInconsistent()
		}
		rowCopy := row
		result = append(result, rowCopy)
		byID[row.id] = &rowCopy
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrapPersistenceError(ctx, "read factual rule versions", err)
	}
	return result, byID, nil
}

func attachFactualReadSupports(
	scope factualReadScope,
	factsByID map[string]*factualReadFact,
	qualifiers []factualReadQualifier,
	evidenceRows []factualReadEvidence,
	inputs []factualReadInput,
	rulesByID map[string]*factualReadRule,
) error {
	nextQualifierOrdinal := make(map[string]int64)
	qualifierNames := make(map[string]map[string]struct{})
	for _, row := range qualifiers {
		item, exists := factsByID[row.factID]
		if !exists || item == nil {
			return factualReadInconsistent()
		}
		if row.id != identity.CanonicalUUID("fact-qualifier", scope.organizationID, scope.sourceID, scope.snapshotID, item.identityKey, row.name) {
			return factualReadInconsistent()
		}
		if row.ordinal != nextQualifierOrdinal[row.factID] {
			return factualReadInconsistent()
		}
		nextQualifierOrdinal[row.factID]++
		if qualifierNames[row.factID] == nil {
			qualifierNames[row.factID] = make(map[string]struct{})
		}
		if _, duplicate := qualifierNames[row.factID][row.name]; duplicate {
			return factualReadInconsistent()
		}
		qualifierNames[row.factID][row.name] = struct{}{}
		value, err := decodeReadTypedValue(row.typedValue)
		if err != nil {
			return err
		}
		item.value.Qualifiers = append(item.value.Qualifiers, fact.Qualifier{Name: row.name, Value: value})
	}

	nextEvidenceOrdinal := make(map[string]int64)
	evidenceIDs := make(map[string]map[string]struct{})
	for _, row := range evidenceRows {
		item, exists := factsByID[row.factID]
		if !exists || item == nil {
			return factualReadInconsistent()
		}
		if row.id != identity.CanonicalUUID("fact-evidence", scope.organizationID, scope.sourceID, scope.snapshotID, item.identityKey, row.externalID) {
			return factualReadInconsistent()
		}
		if row.ordinal != nextEvidenceOrdinal[row.factID] {
			return factualReadInconsistent()
		}
		nextEvidenceOrdinal[row.factID]++
		if evidenceIDs[row.factID] == nil {
			evidenceIDs[row.factID] = make(map[string]struct{})
		}
		if _, duplicate := evidenceIDs[row.factID][row.externalID]; duplicate {
			return factualReadInconsistent()
		}
		evidenceIDs[row.factID][row.externalID] = struct{}{}
		locator, err := decodeReadLocator(row.locatorJSON)
		if err != nil {
			return err
		}
		item.value.Evidence = append(item.value.Evidence, fact.EvidenceRef{ID: row.externalID, Locator: locator})
	}

	nextInputOrdinal := make(map[string]int64)
	inputIDs := make(map[string]map[string]struct{})
	for _, row := range inputs {
		item, exists := factsByID[row.factID]
		inputFact, inputExists := factsByID[row.inputFactID]
		rule, ruleExists := rulesByID[row.ruleID]
		if !exists || item == nil || !inputExists || inputFact == nil || !ruleExists || rule == nil {
			return factualReadInconsistent()
		}
		if item.kind != factualFactKindDerived || item.ruleID == nil || *item.ruleID != row.ruleID ||
			row.id != identity.CanonicalUUID("fact-input", scope.organizationID, scope.sourceID, scope.snapshotID, item.identityKey, inputFact.identityKey) {
			return factualReadInconsistent()
		}
		if row.ordinal != nextInputOrdinal[row.factID] {
			return factualReadInconsistent()
		}
		nextInputOrdinal[row.factID]++
		if inputIDs[row.factID] == nil {
			inputIDs[row.factID] = make(map[string]struct{})
		}
		if _, duplicate := inputIDs[row.factID][row.inputFactID]; duplicate {
			return factualReadInconsistent()
		}
		inputIDs[row.factID][row.inputFactID] = struct{}{}
		if item.value.Lineage == nil {
			item.value.Lineage = &fact.Lineage{RuleID: rule.rule.RuleID, RuleVersion: rule.rule.Version}
		} else if item.value.Lineage.RuleID != rule.rule.RuleID || item.value.Lineage.RuleVersion != rule.rule.Version {
			return factualReadInconsistent()
		}
		item.value.Lineage.InputFactIDs = append(item.value.Lineage.InputFactIDs, inputFact.identityKey)
	}
	return nil
}

func validateFactualReadRows(
	scope factualReadScope,
	manifests []factualReadManifest,
	facts []*factualReadFact,
	rules []factualReadRule,
	factsByID map[string]*factualReadFact,
	rulesByID map[string]*factualReadRule,
) error {
	manifestsByID := make(map[string]*factualReadManifest, len(manifests))
	for index := range manifests {
		manifest := &manifests[index]
		if _, duplicate := manifestsByID[manifest.id]; duplicate {
			return factualReadInconsistent()
		}
		manifestsByID[manifest.id] = manifest
	}
	referencedRules := make(map[string]struct{}, len(rules))
	for _, item := range facts {
		if item == nil || item.observedAt.IsZero() || !item.observedAt.Equal(scope.capturedAt) {
			return factualReadInconsistent()
		}
		if item.value.ID != item.identityKey || item.value.Scope != (fact.Scope{OrganizationID: scope.organizationID, SourceID: scope.sourceID, SnapshotID: scope.snapshotID}) {
			return factualReadInconsistent()
		}
		if item.kind == factualFactKindObserved {
			if item.frontendID == nil || item.ruleID != nil || item.value.Lineage != nil {
				return factualReadInconsistent()
			}
			manifest, exists := manifestsByID[*item.frontendID]
			if !exists || manifest == nil || manifest.manifest.ID != item.producerID || manifest.manifest.Version != item.producerVer || manifest.manifest.Method != item.producerMethod {
				return factualReadInconsistent()
			}
		} else if item.kind == factualFactKindDerived {
			if item.frontendID != nil || item.ruleID == nil || item.value.Lineage == nil || len(item.value.Lineage.InputFactIDs) == 0 {
				return factualReadInconsistent()
			}
			rule, exists := rulesByID[*item.ruleID]
			if !exists || rule == nil || item.value.Lineage.RuleID != rule.rule.RuleID || item.value.Lineage.RuleVersion != rule.rule.Version {
				return factualReadInconsistent()
			}
			referencedRules[*item.ruleID] = struct{}{}
		} else {
			return factualReadInconsistent()
		}
		if err := item.value.Validate(); err != nil {
			return factualReadInconsistent()
		}
	}
	for _, rule := range rules {
		if _, exists := referencedRules[rule.id]; !exists {
			return factualReadInconsistent()
		}
	}
	if len(referencedRules) != len(rules) {
		return factualReadInconsistent()
	}
	if len(factsByID) != len(facts) {
		return factualReadInconsistent()
	}
	return nil
}

func decodeReadTypedValue(raw []byte) (fact.TypedValue, error) {
	var value fact.TypedValue
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &value) != nil {
		return fact.TypedValue{}, factualReadInconsistent()
	}
	if err := value.Validate(); err != nil {
		return fact.TypedValue{}, factualReadInconsistent()
	}
	encoded, err := json.Marshal(value)
	if err != nil || !jsonEqual(raw, encoded) {
		return fact.TypedValue{}, factualReadInconsistent()
	}
	return value, nil
}

func decodeReadLocator(raw []byte) (contract.Locator, error) {
	var locator contract.Locator
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &locator) != nil {
		return contract.Locator{}, factualReadInconsistent()
	}
	if err := locator.Validate(); err != nil {
		return contract.Locator{}, factualReadInconsistent()
	}
	encoded, err := marshalLocator(locator)
	if err != nil || !jsonEqual(raw, encoded) {
		return contract.Locator{}, factualReadInconsistent()
	}
	return locator, nil
}

func factualInputFromPrepared(prepared PreparedFactualSnapshot) FactualSnapshotInput {
	result := FactualSnapshotInput{
		OrganizationID:    prepared.OrganizationID,
		SourceID:          prepared.SourceID,
		SnapshotID:        prepared.SnapshotID,
		Scope:             prepared.Scope,
		FrontendManifests: make([]fact.FrontendManifest, 0, len(prepared.FrontendManifests)),
		RuleVersions:      make([]RuleVersion, 0, len(prepared.RuleVersions)),
		Facts:             make([]fact.CanonicalFact, 0, len(prepared.Facts)),
	}
	for _, manifest := range prepared.FrontendManifests {
		result.FrontendManifests = append(result.FrontendManifests, manifest.Manifest)
	}
	for _, rule := range prepared.RuleVersions {
		result.RuleVersions = append(result.RuleVersions, RuleVersion{
			RuleID: rule.RuleID, Version: rule.Version,
			ImplementationDigest: rule.ImplementationDigest,
			Configuration:        append(json.RawMessage(nil), rule.Configuration...),
		})
	}
	for _, item := range prepared.Facts {
		result.Facts = append(result.Facts, item.Fact)
	}
	return result
}

func validateReadUUIDs(values ...string) error {
	for _, value := range values {
		if err := validateUUID("stored factual identity", value); err != nil {
			return factualReadInconsistent()
		}
	}
	return nil
}

func factualReadInconsistent() error {
	return fmt.Errorf("%w: factual snapshot is inconsistent", ErrInconsistent)
}
