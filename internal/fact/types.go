package fact

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
)

const (
	// Version is the version of the canonical fact representation.
	Version = "v1alpha1"

	// FactVersion is an explicit alias for callers that use the domain name.
	FactVersion = Version
)

var (
	// ErrInvalid identifies a fact or one of its components that cannot be
	// accepted by the factual substrate.
	ErrInvalid = errors.New("fact: invalid")
	// ErrUnsupportedVersion identifies a representation version that this
	// package cannot validate.
	ErrUnsupportedVersion = errors.New("fact: unsupported version")

	// The component errors are aliases kept for callers that want to describe
	// the failed boundary without making validation errors incomparable.
	ErrInvalidIdentity        = ErrInvalid
	ErrInvalidScope           = ErrInvalid
	ErrInvalidPredicate       = ErrInvalid
	ErrInvalidParticipant     = ErrInvalid
	ErrInvalidValue           = ErrInvalid
	ErrInvalidQualifier       = ErrInvalid
	ErrInvalidProducer        = ErrInvalid
	ErrInvalidEvidence        = ErrInvalid
	ErrInvalidLineage         = ErrInvalid
	ErrInvalidManifest        = ErrInvalid
	ErrInvalidExtensionSchema = ErrInvalid
	ErrInvalidSelection       = ErrInvalid
)

// Predicate identifies the initial vocabulary of canonical facts. The zero
// value is intentionally invalid so an omitted predicate cannot be mistaken
// for a supported semantic assertion.
type Predicate string

const (
	PredicateUnknown       Predicate = ""
	PredicateArtifact      Predicate = "artifact"
	PredicateSymbol        Predicate = "symbol"
	PredicateNamedElement  Predicate = "named_element"
	PredicateDefinition    Predicate = "definition"
	PredicateReference     Predicate = "reference"
	PredicateCall          Predicate = "call"
	PredicateDependency    Predicate = "dependency"
	PredicateConfiguration Predicate = "configuration"
	PredicateEndpoint      Predicate = "endpoint"
	PredicateMessage       Predicate = "message"
	PredicateMembership    Predicate = "membership"

	// PredicateElement is a readable compatibility alias for the named
	// element predicate.
	PredicateElement = PredicateNamedElement
	// PredicateBelongsTo is a readable alias for membership facts.
	PredicateBelongsTo = PredicateMembership
)

// Validate checks whether p belongs to the initial canonical vocabulary.
func (p Predicate) Validate() error {
	switch p {
	case PredicateArtifact, PredicateSymbol, PredicateNamedElement,
		PredicateDefinition, PredicateReference, PredicateCall,
		PredicateDependency, PredicateConfiguration, PredicateEndpoint,
		PredicateMessage, PredicateMembership:
		return nil
	default:
		return fmt.Errorf("%w: predicate %q", ErrInvalidPredicate, p)
	}
}

// ParticipantKind identifies the kind of entity that participates in a fact.
// Kinds are deliberately narrower than arbitrary domain entities in this
// first substrate; new kinds can be added by a versioned vocabulary change.
type ParticipantKind string

const (
	ParticipantUnknown      ParticipantKind = ""
	ParticipantArtifact     ParticipantKind = "artifact"
	ParticipantSymbol       ParticipantKind = "symbol"
	ParticipantNamedElement ParticipantKind = "named_element"

	// ParticipantElement is a readable compatibility alias for a named
	// element participant.
	ParticipantElement = ParticipantNamedElement
)

// Participant identifies a typed subject or object of a fact. The identity
// is supplied by the producer and is only validated here.
type Participant struct {
	Kind ParticipantKind `json:"kind"`
	ID   string          `json:"id"`
}

// Validate checks the participant kind and identity.
func (p Participant) Validate() error {
	switch p.Kind {
	case ParticipantArtifact, ParticipantSymbol, ParticipantNamedElement:
	default:
		return fmt.Errorf("%w: participant kind %q", ErrInvalidParticipant, p.Kind)
	}
	return validateIdentifier("participant id", p.ID)
}

// ValueKind identifies the scalar value carried by a fact or qualifier.
// Values are tagged explicitly instead of using an untyped interface so
// malformed or ambiguous payloads are rejected at the boundary.
type ValueKind string

const (
	ValueUnknown ValueKind = ""
	ValueString  ValueKind = "string"
	ValueInteger ValueKind = "integer"
	ValueNumber  ValueKind = "number"
	ValueBoolean ValueKind = "boolean"
	ValueNull    ValueKind = "null"
)

// TypedValue is a small tagged union for values that cannot be represented by
// a participant. Exactly one kind is selected; the kind determines which
// scalar field is meaningful. Zero values such as false and 0 remain valid
// for their corresponding kinds.
type TypedValue struct {
	Kind    ValueKind `json:"kind"`
	String  string    `json:"string,omitempty"`
	Integer int64     `json:"integer,omitempty"`
	Number  float64   `json:"number,omitempty"`
	Boolean bool      `json:"boolean,omitempty"`
}

// Value is the concise domain spelling for TypedValue.
type Value = TypedValue

// Validate checks the tagged value and rejects fields that belong to another
// kind.
func (v TypedValue) Validate() error {
	if !validValueKind(v.Kind) {
		return fmt.Errorf("%w: value kind %q", ErrInvalidValue, v.Kind)
	}
	switch v.Kind {
	case ValueString:
		if v.Integer != 0 || v.Number != 0 || v.Boolean {
			return fmt.Errorf("%w: string value carries another scalar", ErrInvalidValue)
		}
	case ValueInteger:
		if v.String != "" || v.Number != 0 || v.Boolean {
			return fmt.Errorf("%w: integer value carries another scalar", ErrInvalidValue)
		}
	case ValueNumber:
		if v.String != "" || v.Integer != 0 || v.Boolean || math.IsNaN(v.Number) || math.IsInf(v.Number, 0) {
			return fmt.Errorf("%w: number value is malformed", ErrInvalidValue)
		}
	case ValueBoolean:
		if v.String != "" || v.Integer != 0 || v.Number != 0 {
			return fmt.Errorf("%w: boolean value carries another scalar", ErrInvalidValue)
		}
	case ValueNull:
		if v.String != "" || v.Integer != 0 || v.Number != 0 || v.Boolean {
			return fmt.Errorf("%w: null value carries a scalar", ErrInvalidValue)
		}
	}
	return nil
}

func validValueKind(kind ValueKind) bool {
	switch kind {
	case ValueString, ValueInteger, ValueNumber, ValueBoolean, ValueNull:
		return true
	default:
		return false
	}
}

// Qualifier is an independently named, typed property of a fact. A
// qualifier is intentionally not a scalar confidence score: callers can
// retain separate origin, behavior, temporal, coverage, or contestation
// dimensions without collapsing them into one number.
type Qualifier struct {
	Name  string     `json:"name"`
	Value TypedValue `json:"value"`
}

// Common qualifier names are vocabulary guidance, not a confidence model.
const (
	QualifierOrigin       = "origin"
	QualifierMethod       = "method"
	QualifierBehavior     = "behavior"
	QualifierCoverage     = "coverage"
	QualifierTemporal     = "temporal"
	QualifierContestation = "contestation"
)

// Validate checks the name and typed value of a qualifier.
func (q Qualifier) Validate() error {
	if err := validateIdentifier("qualifier name", q.Name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidQualifier, err)
	}
	if err := q.Value.Validate(); err != nil {
		return fmt.Errorf("%w: qualifier %q: %v", ErrInvalidQualifier, q.Name, err)
	}
	return nil
}

// Scope bounds every fact and its linked evidence to one organization, one
// source, and one analysis snapshot.
type Scope struct {
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	SnapshotID     string `json:"snapshot_id"`
}

// Validate checks that all scope identities are present and well formed.
func (s Scope) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "organization id", value: s.OrganizationID},
		{name: "source id", value: s.SourceID},
		{name: "snapshot id", value: s.SnapshotID},
	} {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidScope, err)
		}
	}
	return nil
}

// Producer identifies the frontend or normalizer that emitted a fact.
// Version and Method are part of the supplied provenance and are not inferred
// from the fact or from an analyzer-specific payload.
type Producer struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Method  string `json:"method"`
}

// Validate checks producer identity, version, and method.
func (p Producer) Validate() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "producer id", value: p.ID},
		{name: "producer version", value: p.Version},
		{name: "producer method", value: p.Method},
	} {
		if err := validateIdentifier(field.name, field.value); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidProducer, err)
		}
	}
	return nil
}

// EvidenceRef links a fact to an independently validated evidence unit by
// stable identity and source locator. It intentionally does not import the
// evidence package, avoiding a cycle and keeping the factual contract small.
type EvidenceRef struct {
	ID      string           `json:"id"`
	Locator contract.Locator `json:"locator"`
}

// Evidence is the concise domain spelling for EvidenceRef.
type Evidence = EvidenceRef

// Validate checks evidence identity and locator shape. When an expected scope
// is supplied, the locator's optional source identity must match it. The
// variadic form keeps the component useful on its own while allowing the
// fact validator to enforce the enclosing scope.
func (e EvidenceRef) Validate(scopes ...Scope) error {
	if err := validateIdentifier("evidence id", e.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	if err := e.Locator.Validate(); err != nil {
		return fmt.Errorf("%w: locator: %v", ErrInvalidEvidence, err)
	}
	if len(scopes) > 1 {
		return fmt.Errorf("%w: evidence validation accepts at most one scope", ErrInvalidEvidence)
	}
	if len(scopes) == 1 && e.Locator.SourceID != "" && e.Locator.SourceID != scopes[0].SourceID {
		return fmt.Errorf("%w: locator source id %q does not match scope source id %q", ErrInvalidEvidence, e.Locator.SourceID, scopes[0].SourceID)
	}
	if err := validateOptionalIdentifier("locator source id", e.Locator.SourceID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	if err := validateOptionalIdentifier("locator artifact id", e.Locator.ArtifactID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidEvidence, err)
	}
	return nil
}

// Lineage records the rule and input fact identities that support a derived
// fact. Input identities are references only; existence is checked when the
// surrounding graph or repository is available.
type Lineage struct {
	RuleID       string   `json:"rule_id"`
	RuleVersion  string   `json:"rule_version"`
	InputFactIDs []string `json:"input_fact_ids"`
}

// Validate checks a complete derivation record. A derived fact must name at
// least one input and cannot list the same input twice or refer to itself.
// Supplying the derived fact identity enables the self-reference check.
func (l Lineage) Validate(factIDs ...string) error {
	if len(factIDs) > 1 {
		return fmt.Errorf("%w: lineage validation accepts at most one fact id", ErrInvalidLineage)
	}
	if err := validateIdentifier("lineage rule id", l.RuleID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLineage, err)
	}
	if err := validateIdentifier("lineage rule version", l.RuleVersion); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidLineage, err)
	}
	if len(l.InputFactIDs) == 0 {
		return fmt.Errorf("%w: lineage needs at least one input fact", ErrInvalidLineage)
	}
	seen := make(map[string]struct{}, len(l.InputFactIDs))
	for _, inputID := range l.InputFactIDs {
		if err := validateIdentifier("lineage input fact id", inputID); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidLineage, err)
		}
		if len(factIDs) == 1 && inputID == factIDs[0] {
			return fmt.Errorf("%w: fact cannot be its own lineage input", ErrInvalidLineage)
		}
		if _, exists := seen[inputID]; exists {
			return fmt.Errorf("%w: duplicate lineage input fact id %q", ErrInvalidLineage, inputID)
		}
		seen[inputID] = struct{}{}
	}
	return nil
}

// CanonicalFact is one typed, producer-neutral assertion in an analysis
// snapshot. ID is the deterministic identity derived from the semantic fields
// by the explicit API in identity.go; it is required on final facts and is
// never assigned as a side effect of structural validation.
type CanonicalFact struct {
	Version    string        `json:"version"`
	ID         string        `json:"id"`
	Scope      Scope         `json:"scope"`
	Predicate  Predicate     `json:"predicate"`
	Subject    Participant   `json:"subject"`
	Object     *Participant  `json:"object,omitempty"`
	Value      *TypedValue   `json:"value,omitempty"`
	Qualifiers []Qualifier   `json:"qualifiers,omitempty"`
	Producer   Producer      `json:"producer"`
	Evidence   []EvidenceRef `json:"evidence"`
	Lineage    *Lineage      `json:"lineage,omitempty"`
}

// Fact is the concise domain spelling for CanonicalFact.
type Fact = CanonicalFact

// Validate checks every final fact boundary without rewriting any identity.
// A final fact must carry the deterministic ID produced by FactID. Use the
// explicit derivation API with a structurally valid fact when the ID has not
// been assigned yet.
func (f CanonicalFact) Validate() error {
	if err := f.validateStructure(); err != nil {
		return err
	}
	if err := validateIdentifier("fact id", f.ID); err != nil {
		return err
	}
	expected, err := FactID(f)
	if err != nil {
		return err
	}
	if f.ID != expected {
		return fmt.Errorf("%w: supplied fact id %q does not match deterministic identity %q", ErrInvalidIdentity, f.ID, expected)
	}
	return nil
}

// validateStructure checks fields needed to derive a fact identity. The ID is
// optional here because FactID must accept a new fact before its identity is
// assigned. It is intentionally private so transport and persistence callers
// cannot accidentally bypass final identity validation.
func (f CanonicalFact) validateStructure() error {
	if f.Version != Version {
		return fmt.Errorf("%w: got %q, want %q", ErrUnsupportedVersion, f.Version, Version)
	}
	if f.ID != "" {
		if err := validateIdentifier("fact id", f.ID); err != nil {
			return err
		}
	}
	if err := f.Scope.Validate(); err != nil {
		return err
	}
	if err := f.Predicate.Validate(); err != nil {
		return err
	}
	if err := f.Subject.Validate(); err != nil {
		return fmt.Errorf("subject: %w", err)
	}
	if f.Object != nil {
		if err := f.Object.Validate(); err != nil {
			return fmt.Errorf("object: %w", err)
		}
	}
	if f.Object != nil && f.Value != nil {
		return fmt.Errorf("%w: fact cannot carry both object and value", ErrInvalid)
	}
	if f.Value != nil {
		if err := f.Value.Validate(); err != nil {
			return fmt.Errorf("value: %w", err)
		}
	}
	if err := validateQualifiers(f.Qualifiers); err != nil {
		return err
	}
	if err := f.Producer.Validate(); err != nil {
		return err
	}
	if len(f.Evidence) == 0 {
		return fmt.Errorf("%w: fact needs at least one evidence reference", ErrInvalidEvidence)
	}
	seenEvidence := make(map[string]struct{}, len(f.Evidence))
	for i, evidence := range f.Evidence {
		if err := evidence.Validate(f.Scope); err != nil {
			return fmt.Errorf("evidence %d: %w", i, err)
		}
		if _, exists := seenEvidence[evidence.ID]; exists {
			return fmt.Errorf("%w: duplicate evidence id %q", ErrInvalidEvidence, evidence.ID)
		}
		seenEvidence[evidence.ID] = struct{}{}
	}
	if f.Lineage != nil {
		if err := f.Lineage.Validate(f.ID); err != nil {
			return err
		}
	}
	return nil
}

func validateQualifiers(qualifiers []Qualifier) error {
	seen := make(map[string]struct{}, len(qualifiers))
	for i, qualifier := range qualifiers {
		if err := qualifier.Validate(); err != nil {
			return fmt.Errorf("qualifier %d: %w", i, err)
		}
		if _, exists := seen[qualifier.Name]; exists {
			return fmt.Errorf("%w: duplicate qualifier %q", ErrInvalidQualifier, qualifier.Name)
		}
		seen[qualifier.Name] = struct{}{}
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", ErrInvalidIdentity, name)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s is not valid utf-8", ErrInvalidIdentity, name)
	}
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("%w: %s contains whitespace or control characters", ErrInvalidIdentity, name)
		}
	}
	return nil
}

func validateOptionalIdentifier(name, value string) error {
	if value == "" {
		return nil
	}
	return validateIdentifier(name, value)
}
