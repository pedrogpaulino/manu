package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
)

const (
	factualFactKindObserved = "observed"
	factualFactKindDerived  = "derived"
)

var (
	// ErrInvalidFactualSnapshot identifies a factual input that cannot cross
	// the persistence boundary. It intentionally does not contain caller
	// payloads or database diagnostics.
	ErrInvalidFactualSnapshot = fmt.Errorf("%w: invalid factual snapshot", ErrInvalidInput)
)

// RuleVersion identifies one immutable, organization-scoped derivation rule.
// ID is assigned deterministically during preparation from the external rule
// identity and organization scope; callers do not provide database IDs.
type RuleVersion struct {
	RuleID               string
	Version              string
	ImplementationDigest string
	Configuration        json.RawMessage
}

// FactualSnapshotInput is the complete factual portion of one analysis
// snapshot. OrganizationID, SourceID, and SnapshotID are canonical UUIDs
// already known by relational persistence. Scope retains the external
// identities present in the fact contract and is checked exactly against each
// fact before any transaction is opened.
type FactualSnapshotInput struct {
	OrganizationID    string
	SourceID          string
	SnapshotID        string
	Scope             fact.Scope
	FrontendManifests []fact.FrontendManifest
	RuleVersions      []RuleVersion
	Facts             []fact.CanonicalFact
}

// PreparedFactualSnapshot is a detached, deterministic representation ready
// for a future factual transaction. It contains no SQL or transaction state.
// The fields are exported so a repository adapter can consume the result
// without re-validating or re-encoding the input.
type PreparedFactualSnapshot struct {
	OrganizationID    string
	SourceID          string
	SnapshotID        string
	Scope             fact.Scope
	FrontendManifests []PreparedFrontendManifest
	RuleVersions      []PreparedRuleVersion
	Facts             []PreparedCanonicalFact
}

// PreparedFrontendManifest is the canonical manifest row plus its extension
// schema rows. ID is the deterministic relational UUID; ExternalID is the
// manifest's frontend identity.
type PreparedFrontendManifest struct {
	ID               string
	ExternalID       string
	Manifest         fact.FrontendManifest
	CanonicalJSON    json.RawMessage
	Digest           string
	ExtensionSchemas []PreparedExtensionSchema
}

// PreparedExtensionSchema is the deterministic relational identity of one
// schema declared by a frontend manifest.
type PreparedExtensionSchema struct {
	ID         string
	ManifestID string
	Schema     fact.ExtensionSchema
}

// PreparedRuleVersion is a canonical rule row. Configuration is normalized to
// a stable JSON object representation, with an omitted configuration becoming
// an empty object.
type PreparedRuleVersion struct {
	ID                   string
	RuleID               string
	Version              string
	ImplementationDigest string
	Configuration        json.RawMessage
}

// PreparedCanonicalFact is a detached canonical fact and the rows needed to
// persist its qualifiers, evidence links, and derivation inputs. Observed
// facts have FrontendManifestID set and RuleVersionID empty; derived facts
// have the inverse relationship.
type PreparedCanonicalFact struct {
	ID                 string
	ExternalID         string
	IdentityKey        string
	Kind               string
	Fact               fact.CanonicalFact
	CanonicalJSON      json.RawMessage
	Digest             string
	FrontendManifestID string
	RuleVersionID      string
	Qualifiers         []PreparedFactQualifier
	Evidence           []PreparedFactEvidence
	Inputs             []PreparedFactInput
}

// PreparedFactQualifier is one ordered qualifier row.
type PreparedFactQualifier struct {
	ID         string
	FactID     string
	Ordinal    int64
	Name       string
	TypedValue json.RawMessage
}

// PreparedFactEvidence is one ordered link from a fact to an evidence unit.
// EvidenceUnitID is derived from the external evidence identity and snapshot
// scope; the evidence unit itself is persisted by the existing evidence
// repository boundary.
type PreparedFactEvidence struct {
	ID             string
	FactID         string
	EvidenceUnitID string
	ExternalID     string
	Ordinal        int64
}

// PreparedFactInput is one ordered derivation input. All referenced facts are
// checked against the same prepared snapshot before this row is produced.
type PreparedFactInput struct {
	ID            string
	FactID        string
	InputFactID   string
	RuleVersionID string
	Ordinal       int64
}

// PrepareFactualSnapshot validates and canonicalizes one factual snapshot in
// memory. It performs no I/O and must be called before beginning a factual
// persistence transaction. The input and all nested values remain unchanged.
func PrepareFactualSnapshot(input FactualSnapshotInput) (PreparedFactualSnapshot, error) {
	return prepareFactualSnapshot(input)
}

func prepareFactualSnapshot(input FactualSnapshotInput) (PreparedFactualSnapshot, error) {
	if err := validateFactualSnapshotScope(input); err != nil {
		return PreparedFactualSnapshot{}, err
	}

	prepared := PreparedFactualSnapshot{
		OrganizationID: input.OrganizationID,
		SourceID:       input.SourceID,
		SnapshotID:     input.SnapshotID,
		Scope:          input.Scope,
	}

	manifests, manifestByIdentity, err := prepareFactualManifests(input)
	if err != nil {
		return PreparedFactualSnapshot{}, err
	}
	prepared.FrontendManifests = manifests

	rules, ruleByIdentity, err := prepareFactualRules(input)
	if err != nil {
		return PreparedFactualSnapshot{}, err
	}
	prepared.RuleVersions = rules

	facts, err := prepareFactualFacts(input, manifestByIdentity, ruleByIdentity)
	if err != nil {
		return PreparedFactualSnapshot{}, err
	}
	prepared.Facts = facts
	return prepared, nil
}

func validateFactualSnapshotScope(input FactualSnapshotInput) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "organization id", value: input.OrganizationID},
		{name: "source id", value: input.SourceID},
		{name: "snapshot id", value: input.SnapshotID},
	} {
		if err := validateUUID(field.name, field.value); err != nil {
			return invalidFactualInput()
		}
	}
	if err := input.Scope.Validate(); err != nil {
		return invalidFactualInput()
	}
	if input.OrganizationID != identity.CanonicalUUID("organization", input.Scope.OrganizationID) ||
		input.SourceID != identity.CanonicalUUID("source", input.Scope.OrganizationID, input.Scope.SourceID) ||
		input.SnapshotID != identity.CanonicalUUID("snapshot", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID) {
		return invalidFactualInput()
	}
	for _, value := range []string{
		input.Scope.OrganizationID,
		input.Scope.SourceID,
		input.Scope.SnapshotID,
	} {
		if err := validateText("factual scope", value); err != nil {
			return invalidFactualInput()
		}
	}
	return nil
}

func prepareFactualManifests(input FactualSnapshotInput) ([]PreparedFrontendManifest, map[string]string, error) {
	prepared := make([]PreparedFrontendManifest, 0, len(input.FrontendManifests))
	manifestByIdentity := make(map[string]string, len(input.FrontendManifests))
	seenIDs := make(map[string]struct{}, len(input.FrontendManifests))

	for index, manifest := range input.FrontendManifests {
		canonical, err := fact.CanonicalFrontendManifest(manifest)
		if err != nil {
			return nil, nil, invalidFactualComponent("frontend manifest", index)
		}
		if err := validateFrontendManifestTexts(canonical); err != nil {
			return nil, nil, invalidFactualComponent("frontend manifest", index)
		}
		if _, exists := seenIDs[canonical.ID]; exists {
			return nil, nil, invalidFactualDuplicate("frontend manifest")
		}
		seenIDs[canonical.ID] = struct{}{}

		encoded, err := fact.CanonicalFrontendManifestBytes(canonical)
		if err != nil {
			return nil, nil, invalidFactualComponent("frontend manifest", index)
		}
		digest, err := fact.FrontendManifestDigest(canonical)
		if err != nil {
			return nil, nil, invalidFactualComponent("frontend manifest", index)
		}
		manifestID := identity.CanonicalUUID(
			"frontend-manifest", input.Scope.OrganizationID, input.Scope.SourceID,
			input.Scope.SnapshotID, canonical.ID, canonical.Version, canonical.Method,
		)
		preparedManifest := PreparedFrontendManifest{
			ID:            manifestID,
			ExternalID:    canonical.ID,
			Manifest:      canonical,
			CanonicalJSON: append(json.RawMessage(nil), encoded...),
			Digest:        digest,
		}

		seenSchemas := make(map[string]struct{}, len(canonical.Extensions))
		for schemaIndex, schema := range canonical.Extensions {
			if err := schema.Validate(); err != nil {
				return nil, nil, invalidFactualComponent("frontend extension schema", schemaIndex)
			}
			key := schema.ID + "\x00" + schema.Version
			if _, exists := seenSchemas[key]; exists {
				return nil, nil, invalidFactualDuplicate("frontend extension schema")
			}
			seenSchemas[key] = struct{}{}
			preparedManifest.ExtensionSchemas = append(preparedManifest.ExtensionSchemas, PreparedExtensionSchema{
				ID:         identity.CanonicalUUID("extension-schema", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, canonical.ID, schema.ID, schema.Version),
				ManifestID: manifestID,
				Schema:     schema,
			})
		}
		sort.Slice(preparedManifest.ExtensionSchemas, func(left, right int) bool {
			leftSchema := preparedManifest.ExtensionSchemas[left].Schema
			rightSchema := preparedManifest.ExtensionSchemas[right].Schema
			return extensionSchemaSortKey(leftSchema) < extensionSchemaSortKey(rightSchema)
		})
		prepared = append(prepared, preparedManifest)
		manifestByIdentity[frontendManifestIdentity(canonical)] = manifestID
	}

	sort.Slice(prepared, func(left, right int) bool {
		return frontendManifestSortKey(prepared[left].Manifest) < frontendManifestSortKey(prepared[right].Manifest)
	})
	return prepared, manifestByIdentity, nil
}

func prepareFactualRules(input FactualSnapshotInput) ([]PreparedRuleVersion, map[string]string, error) {
	prepared := make([]PreparedRuleVersion, 0, len(input.RuleVersions))
	ruleByIdentity := make(map[string]string, len(input.RuleVersions))
	seen := make(map[string]struct{}, len(input.RuleVersions))
	for index, rule := range input.RuleVersions {
		if err := validateText("rule id", rule.RuleID); err != nil {
			return nil, nil, invalidFactualComponent("rule version", index)
		}
		if err := validateText("rule version", rule.Version); err != nil {
			return nil, nil, invalidFactualComponent("rule version", index)
		}
		if err := validateDigest("rule implementation digest", rule.ImplementationDigest); err != nil {
			return nil, nil, invalidFactualComponent("rule version", index)
		}
		configuration, err := canonicalJSONObject("rule configuration", rule.Configuration)
		if err != nil {
			return nil, nil, invalidFactualComponent("rule version", index)
		}
		key := rule.RuleID + "\x00" + rule.Version
		if _, exists := seen[key]; exists {
			return nil, nil, invalidFactualDuplicate("rule version")
		}
		seen[key] = struct{}{}
		id := identity.CanonicalUUID("rule-version", input.Scope.OrganizationID, rule.RuleID, rule.Version)
		prepared = append(prepared, PreparedRuleVersion{
			ID:                   id,
			RuleID:               rule.RuleID,
			Version:              rule.Version,
			ImplementationDigest: rule.ImplementationDigest,
			Configuration:        append(json.RawMessage(nil), configuration...),
		})
		ruleByIdentity[key] = id
	}
	sort.Slice(prepared, func(left, right int) bool {
		if prepared[left].RuleID != prepared[right].RuleID {
			return prepared[left].RuleID < prepared[right].RuleID
		}
		return prepared[left].Version < prepared[right].Version
	})
	return prepared, ruleByIdentity, nil
}

func prepareFactualFacts(input FactualSnapshotInput, manifests map[string]string, rules map[string]string) ([]PreparedCanonicalFact, error) {
	prepared := make([]PreparedCanonicalFact, 0, len(input.Facts))
	seenIDs := make(map[string]struct{}, len(input.Facts))
	for index, candidate := range input.Facts {
		if err := candidate.Validate(); err != nil {
			return nil, invalidFactualComponent("canonical fact", index)
		}
		if candidate.Scope != input.Scope {
			return nil, invalidFactualScope("canonical fact")
		}
		if err := validateCanonicalFactTexts(candidate); err != nil {
			return nil, invalidFactualComponent("canonical fact", index)
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return nil, invalidFactualDuplicate("canonical fact")
		}
		seenIDs[candidate.ID] = struct{}{}

		canonicalJSON, err := fact.CanonicalBytes(candidate)
		if err != nil {
			return nil, invalidFactualComponent("canonical fact", index)
		}
		digest, err := fact.CanonicalDigest(candidate)
		if err != nil {
			return nil, invalidFactualComponent("canonical fact", index)
		}
		preparedFact := PreparedCanonicalFact{
			ID:            identity.CanonicalUUID("canonical-fact", input.Scope.OrganizationID, input.Scope.SourceID, input.Scope.SnapshotID, candidate.ID),
			ExternalID:    candidate.ID,
			IdentityKey:   candidate.ID,
			Kind:          factualFactKindObserved,
			Fact:          cloneCanonicalFact(candidate),
			CanonicalJSON: append(json.RawMessage(nil), canonicalJSON...),
			Digest:        digest,
		}

		if candidate.Lineage == nil {
			manifestID, exists := manifests[frontendManifestIdentityForProducer(candidate.Producer)]
			if !exists {
				return nil, invalidFactualReference("canonical fact frontend manifest")
			}
			preparedFact.FrontendManifestID = manifestID
		} else {
			preparedFact.Kind = factualFactKindDerived
			ruleID, exists := rules[candidate.Lineage.RuleID+"\x00"+candidate.Lineage.RuleVersion]
			if !exists {
				return nil, invalidFactualReference("canonical fact rule version")
			}
			preparedFact.RuleVersionID = ruleID
		}

		prepareFactRows(&preparedFact, input.Scope)
		prepared = append(prepared, preparedFact)
	}

	for index := range prepared {
		if prepared[index].Fact.Lineage == nil {
			continue
		}
		for _, inputID := range prepared[index].Fact.Lineage.InputFactIDs {
			if inputID == prepared[index].ExternalID {
				return nil, invalidFactualReference("canonical fact lineage")
			}
			if _, exists := seenIDs[inputID]; !exists {
				return nil, invalidFactualReference("canonical fact lineage")
			}
		}
	}

	sort.Slice(prepared, func(left, right int) bool {
		return prepared[left].ExternalID < prepared[right].ExternalID
	})
	return prepared, nil
}

func prepareFactRows(prepared *PreparedCanonicalFact, scope fact.Scope) {
	qualifiers := append([]fact.Qualifier(nil), prepared.Fact.Qualifiers...)
	sort.SliceStable(qualifiers, func(left, right int) bool {
		return qualifierSortKey(qualifiers[left]) < qualifierSortKey(qualifiers[right])
	})
	for ordinal, qualifier := range qualifiers {
		value, _ := json.Marshal(qualifier.Value)
		prepared.Qualifiers = append(prepared.Qualifiers, PreparedFactQualifier{
			ID:         identity.CanonicalUUID("fact-qualifier", scope.OrganizationID, scope.SourceID, scope.SnapshotID, prepared.ExternalID, qualifier.Name),
			FactID:     prepared.ID,
			Ordinal:    int64(ordinal),
			Name:       qualifier.Name,
			TypedValue: append(json.RawMessage(nil), value...),
		})
	}

	evidence := append([]fact.EvidenceRef(nil), prepared.Fact.Evidence...)
	sort.SliceStable(evidence, func(left, right int) bool {
		return evidenceSortKey(evidence[left]) < evidenceSortKey(evidence[right])
	})
	for ordinal, reference := range evidence {
		prepared.Evidence = append(prepared.Evidence, PreparedFactEvidence{
			ID:             identity.CanonicalUUID("fact-evidence", scope.OrganizationID, scope.SourceID, scope.SnapshotID, prepared.ExternalID, reference.ID),
			FactID:         prepared.ID,
			EvidenceUnitID: identity.CanonicalUUID("evidence", scope.OrganizationID, scope.SourceID, scope.SnapshotID, reference.ID),
			ExternalID:     reference.ID,
			Ordinal:        int64(ordinal),
		})
	}

	if prepared.Fact.Lineage == nil {
		return
	}
	inputs := append([]string(nil), prepared.Fact.Lineage.InputFactIDs...)
	sort.Strings(inputs)
	for ordinal, inputID := range inputs {
		prepared.Inputs = append(prepared.Inputs, PreparedFactInput{
			ID:            identity.CanonicalUUID("fact-input", scope.OrganizationID, scope.SourceID, scope.SnapshotID, prepared.ExternalID, inputID),
			FactID:        prepared.ID,
			InputFactID:   identity.CanonicalUUID("canonical-fact", scope.OrganizationID, scope.SourceID, scope.SnapshotID, inputID),
			RuleVersionID: prepared.RuleVersionID,
			Ordinal:       int64(ordinal),
		})
	}
}

func validateFrontendManifestTexts(manifest fact.FrontendManifest) error {
	for _, value := range []string{manifest.ID, manifest.Version, manifest.Method} {
		if err := validateText("frontend manifest field", value); err != nil {
			return err
		}
	}
	for _, values := range [][]string{manifest.SourceTypes, manifest.Families, manifest.Versions, manifest.Limitations} {
		for _, value := range values {
			if err := validateText("frontend manifest declaration", value); err != nil {
				return err
			}
		}
	}
	for _, capability := range manifest.Capabilities {
		if err := validateText("frontend manifest capability", string(capability)); err != nil {
			return err
		}
	}
	for _, predicate := range manifest.Predicates {
		if err := validateText("frontend manifest predicate", string(predicate)); err != nil {
			return err
		}
	}
	return nil
}

func validateCanonicalFactTexts(candidate fact.CanonicalFact) error {
	for _, value := range []string{
		candidate.Scope.OrganizationID,
		candidate.Scope.SourceID,
		candidate.Scope.SnapshotID,
		candidate.Version,
		string(candidate.Predicate),
		candidate.Subject.ID,
		candidate.Producer.ID,
		candidate.Producer.Version,
		candidate.Producer.Method,
	} {
		if err := validateText("canonical fact field", value); err != nil {
			return err
		}
	}
	for _, participant := range []*fact.Participant{candidate.Object} {
		if participant != nil {
			if err := validateText("canonical fact participant", participant.ID); err != nil {
				return err
			}
		}
	}
	for _, qualifier := range candidate.Qualifiers {
		if err := validateText("canonical fact qualifier", qualifier.Name); err != nil {
			return err
		}
	}
	for _, evidence := range candidate.Evidence {
		if err := validateText("canonical fact evidence", evidence.ID); err != nil {
			return err
		}
	}
	return nil
}

func frontendManifestIdentity(manifest fact.FrontendManifest) string {
	return manifest.ID + "\x00" + manifest.Version + "\x00" + manifest.Method
}

func frontendManifestIdentityForProducer(producer fact.Producer) string {
	return producer.ID + "\x00" + producer.Version + "\x00" + producer.Method
}

func frontendManifestSortKey(manifest fact.FrontendManifest) string {
	return frontendManifestIdentity(manifest)
}

func extensionSchemaSortKey(schema fact.ExtensionSchema) string {
	return schema.ID + "\x00" + schema.Version + "\x00" + schema.Digest
}

func qualifierSortKey(qualifier fact.Qualifier) string {
	encoded, _ := json.Marshal(qualifier)
	return qualifier.Name + "\x00" + string(encoded)
}

func evidenceSortKey(reference fact.EvidenceRef) string {
	encoded, _ := json.Marshal(reference)
	return reference.ID + "\x00" + string(encoded)
}

func canonicalJSONObject(field string, raw json.RawMessage) ([]byte, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return []byte("{}"), nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s is invalid", field)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, fmt.Errorf("%s has trailing data", field)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", field)
	}
	encoded, err := json.Marshal(object)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid", field)
	}
	return encoded, nil
}

func cloneCanonicalFact(candidate fact.CanonicalFact) fact.CanonicalFact {
	clone := candidate
	if candidate.Object != nil {
		object := *candidate.Object
		clone.Object = &object
	}
	if candidate.Value != nil {
		value := *candidate.Value
		clone.Value = &value
	}
	clone.Qualifiers = append([]fact.Qualifier(nil), candidate.Qualifiers...)
	sort.SliceStable(clone.Qualifiers, func(left, right int) bool {
		return qualifierSortKey(clone.Qualifiers[left]) < qualifierSortKey(clone.Qualifiers[right])
	})
	clone.Evidence = append([]fact.EvidenceRef(nil), candidate.Evidence...)
	sort.SliceStable(clone.Evidence, func(left, right int) bool {
		return evidenceSortKey(clone.Evidence[left]) < evidenceSortKey(clone.Evidence[right])
	})
	if candidate.Lineage != nil {
		lineage := *candidate.Lineage
		lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
		sort.Strings(lineage.InputFactIDs)
		clone.Lineage = &lineage
	}
	return clone
}

func invalidFactualInput() error {
	return fmt.Errorf("%w", ErrInvalidFactualSnapshot)
}

func invalidFactualComponent(component string, _ int) error {
	return fmt.Errorf("%w: invalid %s", ErrInvalidFactualSnapshot, component)
}

func invalidFactualDuplicate(component string) error {
	return fmt.Errorf("%w: duplicate %s", ErrInvalidFactualSnapshot, component)
}

func invalidFactualReference(component string) error {
	return fmt.Errorf("%w: invalid %s reference", ErrInvalidFactualSnapshot, component)
}

func invalidFactualScope(component string) error {
	return fmt.Errorf("%w: %s scope mismatch", ErrInvalidFactualSnapshot, component)
}

const (
	lockFactualSnapshotSQL = `
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
  AND snapshot.id = $3
FOR UPDATE OF snapshot`

	insertFactualFrontendManifestSQL = `
INSERT INTO frontend_manifests (
    id, organization_id, source_id, snapshot_id, external_id,
    manifest_version, frontend_id, version, method, execution_profile,
    manifest, manifest_digest
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualFrontendManifestSQL = `
	SELECT source_id, snapshot_id, external_id, manifest_version, frontend_id, version, method,
	       execution_profile, manifest, manifest_digest
	FROM frontend_manifests
	WHERE organization_id = $1 AND id = $2`

	insertFactualExtensionSchemaSQL = `
INSERT INTO frontend_extension_schemas (
    id, organization_id, source_id, snapshot_id, frontend_manifest_id,
    schema_id, version, digest
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualExtensionSchemaSQL = `
SELECT source_id, snapshot_id, frontend_manifest_id, schema_id, version, digest
FROM frontend_extension_schemas
WHERE organization_id = $1 AND id = $2`

	insertFactualRuleVersionSQL = `
INSERT INTO rule_versions (
    id, organization_id, rule_id, version, implementation_digest, configuration
)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualRuleVersionSQL = `
SELECT rule_id, version, implementation_digest, configuration
FROM rule_versions
WHERE organization_id = $1 AND id = $2`

	insertFactualCanonicalFactSQL = `
INSERT INTO canonical_facts (
    id, organization_id, source_id, snapshot_id, identity_key, fact_version,
    fact_kind, predicate, subject_kind, subject_id, object_kind, object_id,
    typed_value, frontend_manifest_id, producer_id, producer_version,
    producer_method, rule_version_id, observed_at
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
        $16, $17, $18, $19)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualCanonicalFactSQL = `
SELECT source_id, snapshot_id, identity_key, fact_version, fact_kind,
       predicate, subject_kind, subject_id, object_kind, object_id,
       typed_value, frontend_manifest_id, producer_id, producer_version,
       producer_method, rule_version_id, observed_at
FROM canonical_facts
WHERE organization_id = $1 AND id = $2`

	insertFactualQualifierSQL = `
INSERT INTO canonical_fact_qualifiers (
    id, organization_id, source_id, snapshot_id, fact_id, ordinal, name,
    typed_value
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualQualifierSQL = `
SELECT source_id, snapshot_id, fact_id, ordinal, name, typed_value
FROM canonical_fact_qualifiers
WHERE organization_id = $1 AND id = $2`
	selectFactualQualifiersSQL = `
SELECT fact_id, ordinal, name, typed_value
FROM canonical_fact_qualifiers
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3 AND fact_id = $4
ORDER BY ordinal`

	insertFactualEvidenceLinkSQL = `
INSERT INTO canonical_fact_evidence (
    id, organization_id, source_id, snapshot_id, fact_id, evidence_unit_id,
    ordinal
)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualEvidenceLinkSQL = `
SELECT source_id, snapshot_id, fact_id, evidence_unit_id, ordinal
FROM canonical_fact_evidence
WHERE organization_id = $1 AND id = $2`
	selectFactualEvidenceLinksSQL = `
SELECT fact_id, evidence_unit_id, ordinal
FROM canonical_fact_evidence
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3 AND fact_id = $4
ORDER BY ordinal`

	insertFactualInputSQL = `
INSERT INTO canonical_fact_inputs (
    id, organization_id, source_id, snapshot_id, fact_id, input_fact_id,
    rule_version_id, fact_kind, ordinal
)
VALUES ($1, $2, $3, $4, $5, $6, $7, 'derived', $8)
ON CONFLICT (organization_id, id) DO NOTHING`
	selectFactualInputSQL = `
SELECT source_id, snapshot_id, fact_id, input_fact_id, rule_version_id,
       fact_kind, ordinal
FROM canonical_fact_inputs
WHERE organization_id = $1 AND id = $2`
	selectFactualInputsSQL = `
SELECT fact_id, input_fact_id, rule_version_id, fact_kind, ordinal
FROM canonical_fact_inputs
WHERE organization_id = $1 AND source_id = $2 AND snapshot_id = $3 AND fact_id = $4
ORDER BY ordinal`
)

// PersistFactualSnapshot validates the complete factual slice before opening
// a transaction. Every factual table is then written in one transaction; a
// failure in any row causes WithinTx to roll back all preceding writes.
func (r *Repository) PersistFactualSnapshot(ctx context.Context, input FactualSnapshotInput) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	prepared, err := prepareFactualSnapshot(input)
	if err != nil {
		return err
	}
	return r.WithinTx(ctx, func(u *UnitOfWork) error {
		capturedAt, err := u.lockFactualSnapshot(ctx, prepared)
		if err != nil {
			return err
		}
		return u.persistPreparedFactualSnapshot(ctx, prepared, capturedAt)
	})
}

func (u *UnitOfWork) lockFactualSnapshot(ctx context.Context, prepared PreparedFactualSnapshot) (time.Time, error) {
	if u == nil || u.tx == nil {
		return time.Time{}, fmt.Errorf("%w: unit of work is not configured", ErrInvalidInput)
	}
	var organizationExternalID, sourceExternalID, snapshotExternalID string
	var capturedAt time.Time
	err := u.tx.QueryRow(ctx, lockFactualSnapshotSQL,
		prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID,
	).Scan(&organizationExternalID, &sourceExternalID, &snapshotExternalID, &capturedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, ErrNotFound
		}
		return time.Time{}, wrapPersistenceError(ctx, "read factual snapshot", err)
	}
	if organizationExternalID != prepared.Scope.OrganizationID ||
		sourceExternalID != prepared.Scope.SourceID ||
		snapshotExternalID != prepared.Scope.SnapshotID || capturedAt.IsZero() {
		return time.Time{}, ErrConflict
	}
	return capturedAt, nil
}

func (u *UnitOfWork) persistPreparedFactualSnapshot(ctx context.Context, prepared PreparedFactualSnapshot, capturedAt time.Time) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if u == nil || u.tx == nil {
		return fmt.Errorf("%w: unit of work is not configured", ErrInvalidInput)
	}
	for _, manifest := range prepared.FrontendManifests {
		if err := u.insertFactualFrontendManifest(ctx, prepared, manifest); err != nil {
			return err
		}
	}
	for _, manifest := range prepared.FrontendManifests {
		for _, schema := range manifest.ExtensionSchemas {
			if err := u.insertFactualExtensionSchema(ctx, prepared, schema); err != nil {
				return err
			}
		}
	}
	for _, rule := range prepared.RuleVersions {
		if err := u.insertFactualRuleVersion(ctx, prepared, rule); err != nil {
			return err
		}
	}
	for _, canonicalFact := range prepared.Facts {
		if err := u.insertFactualCanonicalFact(ctx, prepared, canonicalFact, capturedAt); err != nil {
			return err
		}
	}
	for _, canonicalFact := range prepared.Facts {
		if canonicalFact.Kind == factualFactKindDerived && len(canonicalFact.Inputs) == 0 {
			return fmt.Errorf("%w: derived fact has no inputs", ErrInvalidInput)
		}
		for _, qualifier := range canonicalFact.Qualifiers {
			if err := u.insertFactualQualifier(ctx, prepared, qualifier); err != nil {
				return err
			}
		}
		for _, evidence := range canonicalFact.Evidence {
			if err := u.insertFactualEvidenceLink(ctx, prepared, evidence); err != nil {
				return err
			}
		}
		for _, input := range canonicalFact.Inputs {
			if err := u.insertFactualInput(ctx, prepared, input); err != nil {
				return err
			}
		}
		if err := u.compareFactualSupportSets(ctx, prepared, canonicalFact); err != nil {
			return err
		}
	}
	return nil
}

func factualExecInsert(ctx context.Context, u *UnitOfWork, query, operation string, args ...any) (int64, error) {
	if u == nil || u.tx == nil {
		return 0, fmt.Errorf("%w: unit of work is not configured", ErrInvalidInput)
	}
	tag, err := u.tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, wrapPersistenceError(ctx, operation, err)
	}
	if tag.RowsAffected() > 1 {
		return 0, fmt.Errorf("%w: %s affected too many rows", ErrInconsistent, operation)
	}
	return tag.RowsAffected(), nil
}

func (u *UnitOfWork) insertFactualFrontendManifest(ctx context.Context, prepared PreparedFactualSnapshot, manifest PreparedFrontendManifest) error {
	rows, err := factualExecInsert(ctx, u, insertFactualFrontendManifestSQL, "insert factual frontend manifest",
		manifest.ID, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID,
		manifest.ExternalID, manifest.Manifest.ManifestVersion, manifest.Manifest.ID,
		manifest.Manifest.Version, manifest.Manifest.Method, string(manifest.Manifest.Execution),
		manifest.CanonicalJSON, manifest.Digest,
	)
	if err != nil || rows == 1 {
		return err
	}
	var sourceID, snapshotID, externalID, manifestVersion, frontendID, version, method, executionProfile, digest string
	var manifestJSON []byte
	err = u.tx.QueryRow(ctx, selectFactualFrontendManifestSQL, prepared.OrganizationID, manifest.ID).Scan(
		&sourceID, &snapshotID, &externalID, &manifestVersion, &frontendID, &version, &method,
		&executionProfile, &manifestJSON, &digest,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual frontend manifest", err)
	}
	if sourceID != prepared.SourceID || snapshotID != prepared.SnapshotID || externalID != manifest.ExternalID ||
		manifestVersion != manifest.Manifest.ManifestVersion ||
		frontendID != manifest.Manifest.ID || version != manifest.Manifest.Version || method != manifest.Manifest.Method ||
		executionProfile != string(manifest.Manifest.Execution) || digest != manifest.Digest ||
		!jsonEqual(manifestJSON, manifest.CanonicalJSON) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) insertFactualExtensionSchema(ctx context.Context, prepared PreparedFactualSnapshot, schema PreparedExtensionSchema) error {
	rows, err := factualExecInsert(ctx, u, insertFactualExtensionSchemaSQL, "insert factual extension schema",
		schema.ID, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID,
		schema.ManifestID, schema.Schema.ID, schema.Schema.Version, schema.Schema.Digest,
	)
	if err != nil || rows == 1 {
		return err
	}
	var sourceID, snapshotID, manifestID, schemaID, version, digest string
	err = u.tx.QueryRow(ctx, selectFactualExtensionSchemaSQL, prepared.OrganizationID, schema.ID).Scan(
		&sourceID, &snapshotID, &manifestID, &schemaID, &version, &digest,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual extension schema", err)
	}
	if sourceID != prepared.SourceID || snapshotID != prepared.SnapshotID || manifestID != schema.ManifestID ||
		schemaID != schema.Schema.ID || version != schema.Schema.Version || digest != schema.Schema.Digest {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) insertFactualRuleVersion(ctx context.Context, prepared PreparedFactualSnapshot, rule PreparedRuleVersion) error {
	rows, err := factualExecInsert(ctx, u, insertFactualRuleVersionSQL, "insert factual rule version",
		rule.ID, prepared.OrganizationID, rule.RuleID, rule.Version, rule.ImplementationDigest, rule.Configuration,
	)
	if err != nil || rows == 1 {
		return err
	}
	var ruleID, version, implementationDigest string
	var configuration []byte
	err = u.tx.QueryRow(ctx, selectFactualRuleVersionSQL, prepared.OrganizationID, rule.ID).Scan(
		&ruleID, &version, &implementationDigest, &configuration,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual rule version", err)
	}
	if ruleID != rule.RuleID || version != rule.Version || implementationDigest != rule.ImplementationDigest ||
		!jsonEqual(configuration, rule.Configuration) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) insertFactualCanonicalFact(ctx context.Context, prepared PreparedFactualSnapshot, item PreparedCanonicalFact, capturedAt time.Time) error {
	objectKind, objectID := any(nil), any(nil)
	if item.Fact.Object != nil {
		objectKind, objectID = item.Fact.Object.Kind, item.Fact.Object.ID
	}
	typedValue := any(nil)
	if item.Fact.Value != nil {
		value, valueErr := factualFactValueJSON(item.Fact)
		if valueErr != nil {
			return fmt.Errorf("%w: canonical fact value", ErrInvalidInput)
		}
		typedValue = value
	}
	frontendManifestID := any(nil)
	ruleVersionID := any(nil)
	if item.Kind == factualFactKindObserved {
		frontendManifestID = item.FrontendManifestID
	} else {
		ruleVersionID = item.RuleVersionID
	}
	rows, err := factualExecInsert(ctx, u, insertFactualCanonicalFactSQL, "insert factual canonical fact",
		item.ID, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID, item.IdentityKey,
		item.Fact.Version, item.Kind, string(item.Fact.Predicate), string(item.Fact.Subject.Kind),
		item.Fact.Subject.ID, objectKind, objectID, typedValue, frontendManifestID,
		item.Fact.Producer.ID, item.Fact.Producer.Version, item.Fact.Producer.Method,
		ruleVersionID, capturedAt,
	)
	if err != nil || rows == 1 {
		return err
	}
	return u.existingFactualCanonicalFactMatches(ctx, prepared, item, capturedAt)
}

func (u *UnitOfWork) existingFactualCanonicalFactMatches(ctx context.Context, prepared PreparedFactualSnapshot, item PreparedCanonicalFact, capturedAt time.Time) error {
	var sourceID, snapshotID, identityKey, factVersion, kind, predicate, subjectKind, subjectID string
	var producerID, producerVersion, producerMethod string
	var objectKind, objectID, frontendManifestID, ruleVersionID *string
	var typedValue []byte
	var observedAt time.Time
	err := u.tx.QueryRow(ctx, selectFactualCanonicalFactSQL, prepared.OrganizationID, item.ID).Scan(
		&sourceID, &snapshotID, &identityKey, &factVersion, &kind, &predicate, &subjectKind, &subjectID,
		&objectKind, &objectID, &typedValue, &frontendManifestID, &producerID, &producerVersion,
		&producerMethod, &ruleVersionID, &observedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual canonical fact", err)
	}
	expectedValue, err := factualFactValueJSON(item.Fact)
	if err != nil {
		return fmt.Errorf("%w: canonical fact value", ErrInvalidInput)
	}
	expectedObjectKind, expectedObjectID := "", ""
	if item.Fact.Object != nil {
		expectedObjectKind, expectedObjectID = string(item.Fact.Object.Kind), item.Fact.Object.ID
	}
	expectedFrontend, expectedRule := item.FrontendManifestID, item.RuleVersionID
	if sourceID != prepared.SourceID || snapshotID != prepared.SnapshotID || identityKey != item.IdentityKey ||
		factVersion != item.Fact.Version || kind != item.Kind || predicate != string(item.Fact.Predicate) ||
		subjectKind != string(item.Fact.Subject.Kind) || subjectID != item.Fact.Subject.ID ||
		factualOptionalString(objectKind) != expectedObjectKind || factualOptionalString(objectID) != expectedObjectID ||
		!jsonEqual(typedValue, expectedValue) || factualOptionalString(frontendManifestID) != expectedFrontend ||
		producerID != item.Fact.Producer.ID || producerVersion != item.Fact.Producer.Version ||
		producerMethod != item.Fact.Producer.Method || factualOptionalString(ruleVersionID) != expectedRule ||
		!observedAt.Equal(capturedAt) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) insertFactualQualifier(ctx context.Context, prepared PreparedFactualSnapshot, qualifier PreparedFactQualifier) error {
	rows, err := factualExecInsert(ctx, u, insertFactualQualifierSQL, "insert factual qualifier",
		qualifier.ID, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID,
		qualifier.FactID, qualifier.Ordinal, qualifier.Name, qualifier.TypedValue,
	)
	if err != nil || rows == 1 {
		return err
	}
	var sourceID, snapshotID, factID, name string
	var ordinal int64
	var typedValue []byte
	err = u.tx.QueryRow(ctx, selectFactualQualifierSQL, prepared.OrganizationID, qualifier.ID).Scan(
		&sourceID, &snapshotID, &factID, &ordinal, &name, &typedValue,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual qualifier", err)
	}
	if sourceID != prepared.SourceID || snapshotID != prepared.SnapshotID || factID != qualifier.FactID ||
		ordinal != qualifier.Ordinal || name != qualifier.Name || !jsonEqual(typedValue, qualifier.TypedValue) {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) insertFactualEvidenceLink(ctx context.Context, prepared PreparedFactualSnapshot, evidence PreparedFactEvidence) error {
	rows, err := factualExecInsert(ctx, u, insertFactualEvidenceLinkSQL, "insert factual evidence link",
		evidence.ID, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID,
		evidence.FactID, evidence.EvidenceUnitID, evidence.Ordinal,
	)
	if err != nil || rows == 1 {
		return err
	}
	var sourceID, snapshotID, factID, evidenceUnitID string
	var ordinal int64
	err = u.tx.QueryRow(ctx, selectFactualEvidenceLinkSQL, prepared.OrganizationID, evidence.ID).Scan(
		&sourceID, &snapshotID, &factID, &evidenceUnitID, &ordinal,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual evidence link", err)
	}
	if sourceID != prepared.SourceID || snapshotID != prepared.SnapshotID || factID != evidence.FactID ||
		evidenceUnitID != evidence.EvidenceUnitID || ordinal != evidence.Ordinal {
		return ErrConflict
	}
	return nil
}

func (u *UnitOfWork) insertFactualInput(ctx context.Context, prepared PreparedFactualSnapshot, input PreparedFactInput) error {
	rows, err := factualExecInsert(ctx, u, insertFactualInputSQL, "insert factual lineage input",
		input.ID, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID,
		input.FactID, input.InputFactID, input.RuleVersionID, input.Ordinal,
	)
	if err != nil || rows == 1 {
		return err
	}
	var sourceID, snapshotID, factID, inputFactID, ruleVersionID, kind string
	var ordinal int64
	err = u.tx.QueryRow(ctx, selectFactualInputSQL, prepared.OrganizationID, input.ID).Scan(
		&sourceID, &snapshotID, &factID, &inputFactID, &ruleVersionID, &kind, &ordinal,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConflict
		}
		return wrapPersistenceError(ctx, "read factual lineage input", err)
	}
	if sourceID != prepared.SourceID || snapshotID != prepared.SnapshotID || factID != input.FactID ||
		inputFactID != input.InputFactID || ruleVersionID != input.RuleVersionID || kind != factualFactKindDerived ||
		ordinal != input.Ordinal {
		return ErrConflict
	}
	return nil
}

type factualQualifierSupport struct {
	factID     string
	ordinal    int64
	name       string
	typedValue []byte
}

type factualEvidenceSupport struct {
	factID         string
	evidenceUnitID string
	ordinal        int64
}

type factualInputSupport struct {
	factID        string
	inputFactID   string
	ruleVersionID string
	kind          string
	ordinal       int64
}

func (u *UnitOfWork) compareFactualSupportSets(ctx context.Context, prepared PreparedFactualSnapshot, item PreparedCanonicalFact) error {
	qualifiers, err := u.readFactualQualifiers(ctx, prepared, item.ID)
	if err != nil {
		return err
	}
	if len(qualifiers) != len(item.Qualifiers) {
		return ErrConflict
	}
	for index, actual := range qualifiers {
		expected := item.Qualifiers[index]
		if actual.factID != expected.FactID || actual.ordinal != expected.Ordinal || actual.name != expected.Name ||
			!jsonEqual(actual.typedValue, expected.TypedValue) {
			return ErrConflict
		}
	}

	evidence, err := u.readFactualEvidenceLinks(ctx, prepared, item.ID)
	if err != nil {
		return err
	}
	if len(evidence) != len(item.Evidence) {
		return ErrConflict
	}
	for index, actual := range evidence {
		expected := item.Evidence[index]
		if actual.factID != expected.FactID || actual.evidenceUnitID != expected.EvidenceUnitID || actual.ordinal != expected.Ordinal {
			return ErrConflict
		}
	}

	inputs, err := u.readFactualInputs(ctx, prepared, item.ID)
	if err != nil {
		return err
	}
	if len(inputs) != len(item.Inputs) {
		return ErrConflict
	}
	for index, actual := range inputs {
		expected := item.Inputs[index]
		if actual.factID != expected.FactID || actual.inputFactID != expected.InputFactID ||
			actual.ruleVersionID != expected.RuleVersionID || actual.kind != factualFactKindDerived || actual.ordinal != expected.Ordinal {
			return ErrConflict
		}
	}
	return nil
}

func (u *UnitOfWork) readFactualQualifiers(ctx context.Context, prepared PreparedFactualSnapshot, factID string) ([]factualQualifierSupport, error) {
	rows, err := u.tx.Query(ctx, selectFactualQualifiersSQL, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID, factID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read factual qualifier set", err)
	}
	defer rows.Close()
	result := make([]factualQualifierSupport, 0)
	for rows.Next() {
		var item factualQualifierSupport
		if err := rows.Scan(&item.factID, &item.ordinal, &item.name, &item.typedValue); err != nil {
			return nil, wrapPersistenceError(ctx, "scan factual qualifier set", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read factual qualifier set", err)
	}
	return result, nil
}

func (u *UnitOfWork) readFactualEvidenceLinks(ctx context.Context, prepared PreparedFactualSnapshot, factID string) ([]factualEvidenceSupport, error) {
	rows, err := u.tx.Query(ctx, selectFactualEvidenceLinksSQL, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID, factID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read factual evidence set", err)
	}
	defer rows.Close()
	result := make([]factualEvidenceSupport, 0)
	for rows.Next() {
		var item factualEvidenceSupport
		if err := rows.Scan(&item.factID, &item.evidenceUnitID, &item.ordinal); err != nil {
			return nil, wrapPersistenceError(ctx, "scan factual evidence set", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read factual evidence set", err)
	}
	return result, nil
}

func (u *UnitOfWork) readFactualInputs(ctx context.Context, prepared PreparedFactualSnapshot, factID string) ([]factualInputSupport, error) {
	rows, err := u.tx.Query(ctx, selectFactualInputsSQL, prepared.OrganizationID, prepared.SourceID, prepared.SnapshotID, factID)
	if err != nil {
		return nil, wrapPersistenceError(ctx, "read factual lineage set", err)
	}
	defer rows.Close()
	result := make([]factualInputSupport, 0)
	for rows.Next() {
		var item factualInputSupport
		if err := rows.Scan(&item.factID, &item.inputFactID, &item.ruleVersionID, &item.kind, &item.ordinal); err != nil {
			return nil, wrapPersistenceError(ctx, "scan factual lineage set", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapPersistenceError(ctx, "read factual lineage set", err)
	}
	return result, nil
}

func factualFactValueJSON(candidate fact.CanonicalFact) ([]byte, error) {
	if candidate.Value == nil {
		return nil, nil
	}
	if err := candidate.Value.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(candidate.Value)
}

func factualOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
