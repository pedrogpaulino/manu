package persistence

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

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
