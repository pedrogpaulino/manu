package fact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

const conflictIdentityDomain = "manu:fact:conflict:v1alpha1\x00"

var (
	// ErrInvalidConflict identifies invalid input to DetectConflicts or an
	// invalid Conflict value. It aliases the fact boundary error so callers
	// can handle all malformed factual values uniformly.
	ErrInvalidConflict = ErrInvalid
	// ErrInvalidConflictInput is a descriptive alias for ErrInvalidConflict.
	ErrInvalidConflictInput = ErrInvalidConflict
	// ErrDuplicateFactID identifies a repeated fact identity in one detection
	// request. A duplicate is rejected even when the repeated facts agree.
	ErrDuplicateFactID = errors.New("fact: duplicate fact id")
)

// Conflict is a deterministic, non-adjudicating record that groups
// incompatible singular claims for one factual slot. Facts are retained in
// full, including each producer, evidence reference, qualifier, and lineage;
// no winner is selected and no support metadata is merged.
//
// Facts is the flattened, ordered view of all assertions and their
// supporters. Assertions provide the alternative claim values and the facts
// that support each alternative.
type Conflict struct {
	ID         string              `json:"id"`
	Scope      Scope               `json:"scope"`
	Predicate  Predicate           `json:"predicate"`
	Subject    Participant         `json:"subject"`
	Assertions []ConflictAssertion `json:"assertions"`
	Facts      []CanonicalFact     `json:"facts"`
}

// ConflictAssertion is one distinct object-or-value assertion within a
// conflict. Facts with the same assertion are supporters of that alternative
// and remain separate records.
type ConflictAssertion struct {
	Object *Participant    `json:"object,omitempty"`
	Value  *TypedValue     `json:"value,omitempty"`
	Facts  []CanonicalFact `json:"facts"`
}

// DetectConflicts validates and indexes facts, returning only conflicts for
// singular claim predicates. It is pure: the input is never modified, and an
// invalid input returns no partial result.
//
// A conflict requires at least two distinct Producer values (the complete
// ID/version/method tuple) and at least two distinct object-or-value
// assertions in the same Scope, Predicate, and Subject slot. Reference,
// call, dependency, configuration, endpoint, message, and membership
// predicates are intentionally multivalued and never produce automatic
// conflicts.
func DetectConflicts(facts []CanonicalFact) ([]Conflict, error) {
	validated := make([]CanonicalFact, len(facts))
	seenIDs := make(map[string]struct{}, len(facts))
	for index, candidate := range facts {
		if err := candidate.Validate(); err != nil {
			return nil, fmt.Errorf("%w: fact %d: %v", ErrInvalidConflict, index, err)
		}
		if _, exists := seenIDs[candidate.ID]; exists {
			return nil, fmt.Errorf("%w: %w", ErrInvalidConflict, ErrDuplicateFactID)
		}
		seenIDs[candidate.ID] = struct{}{}
		validated[index] = cloneCanonicalFact(candidate)
	}

	type slotGroup struct {
		scope     Scope
		predicate Predicate
		subject   Participant
		facts     []CanonicalFact
	}
	groups := make(map[string]*slotGroup)
	for _, candidate := range validated {
		if !isConflictPredicate(candidate.Predicate) {
			continue
		}
		slot := conflictSlot{
			scope:     candidate.Scope,
			predicate: candidate.Predicate,
			subject:   candidate.Subject,
		}
		key := conflictSlotKey(slot)
		group := groups[key]
		if group == nil {
			group = &slotGroup{
				scope:     slot.scope,
				predicate: slot.predicate,
				subject:   slot.subject,
			}
			groups[key] = group
		}
		group.facts = append(group.facts, candidate)
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	result := make([]Conflict, 0)
	for _, key := range keys {
		group := groups[key]
		assertions := make(map[string][]CanonicalFact)
		producers := make(map[string]struct{})
		for _, candidate := range group.facts {
			assertions[conflictAssertionKey(candidate.Object, candidate.Value)] = append(assertions[conflictAssertionKey(candidate.Object, candidate.Value)], candidate)
			producers[conflictProducerKey(candidate.Producer)] = struct{}{}
		}
		if len(assertions) < 2 || len(producers) < 2 {
			continue
		}

		facts := sortAndCloneFacts(group.facts)
		orderedAssertions := make([]ConflictAssertion, 0, len(assertions))
		assertionKeys := make([]string, 0, len(assertions))
		for assertionKey := range assertions {
			assertionKeys = append(assertionKeys, assertionKey)
		}
		sort.Strings(assertionKeys)
		for _, assertionKey := range assertionKeys {
			members := assertions[assertionKey]
			orderedMembers := sortAndCloneFacts(members)
			orderedAssertions = append(orderedAssertions, ConflictAssertion{
				Object: cloneParticipant(orderedMembers[0].Object),
				Value:  cloneValue(orderedMembers[0].Value),
				Facts:  orderedMembers,
			})
		}

		conflict := Conflict{
			Scope:      group.scope,
			Predicate:  group.predicate,
			Subject:    group.subject,
			Assertions: orderedAssertions,
			Facts:      facts,
		}
		conflict.ID = conflictID(conflict)
		result = append(result, conflict)
	}

	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// Validate checks the public conflict representation and its derived
// identity. It is useful at serialization or persistence boundaries when a
// Conflict was obtained from an external caller rather than
// DetectConflicts.
func (c Conflict) Validate() error {
	if err := validateIdentifier("conflict id", c.ID); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConflict, err)
	}
	if err := c.Scope.Validate(); err != nil {
		return fmt.Errorf("%w: scope: %v", ErrInvalidConflict, err)
	}
	if err := c.Predicate.Validate(); err != nil {
		return fmt.Errorf("%w: predicate: %v", ErrInvalidConflict, err)
	}
	if !isConflictPredicate(c.Predicate) {
		return fmt.Errorf("%w: predicate %q is multivalued", ErrInvalidConflict, c.Predicate)
	}
	if err := c.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: subject: %v", ErrInvalidConflict, err)
	}
	if len(c.Assertions) < 2 {
		return fmt.Errorf("%w: at least two assertions are required", ErrInvalidConflict)
	}
	if len(c.Facts) == 0 {
		return fmt.Errorf("%w: at least one fact is required", ErrInvalidConflict)
	}

	factByID := make(map[string]CanonicalFact, len(c.Facts))
	for index, candidate := range c.Facts {
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("%w: fact %d: %v", ErrInvalidConflict, index, err)
		}
		if _, exists := factByID[candidate.ID]; exists {
			return fmt.Errorf("%w: %w", ErrInvalidConflict, ErrDuplicateFactID)
		}
		if candidate.Scope != c.Scope || candidate.Predicate != c.Predicate || candidate.Subject != c.Subject {
			return fmt.Errorf("%w: fact %d is outside the conflict slot", ErrInvalidConflict, index)
		}
		factByID[candidate.ID] = candidate
	}

	assertionFactIDs := make(map[string]struct{}, len(c.Facts))
	seenAssertions := make(map[string]struct{}, len(c.Assertions))
	producerIDs := make(map[string]struct{}, len(c.Facts))
	for index, assertion := range c.Assertions {
		if err := validateAssertionValue(assertion.Object, assertion.Value); err != nil {
			return fmt.Errorf("%w: assertion %d: %v", ErrInvalidConflict, index, err)
		}
		assertionKey := conflictAssertionKey(assertion.Object, assertion.Value)
		if _, exists := seenAssertions[assertionKey]; exists {
			return fmt.Errorf("%w: duplicate assertion", ErrInvalidConflict)
		}
		seenAssertions[assertionKey] = struct{}{}
		if len(assertion.Facts) == 0 {
			return fmt.Errorf("%w: assertion %d has no facts", ErrInvalidConflict, index)
		}
		for factIndex, candidate := range assertion.Facts {
			if err := candidate.Validate(); err != nil {
				return fmt.Errorf("%w: assertion %d fact %d: %v", ErrInvalidConflict, index, factIndex, err)
			}
			factFromFlat, exists := factByID[candidate.ID]
			if !exists || !sameCanonicalFact(factFromFlat, candidate) {
				return fmt.Errorf("%w: assertion %d fact %d is not in the flattened facts", ErrInvalidConflict, index, factIndex)
			}
			if conflictAssertionKey(candidate.Object, candidate.Value) != assertionKey {
				return fmt.Errorf("%w: assertion %d contains a different claim", ErrInvalidConflict, index)
			}
			if _, exists := assertionFactIDs[candidate.ID]; exists {
				return fmt.Errorf("%w: fact appears in multiple assertions", ErrInvalidConflict)
			}
			assertionFactIDs[candidate.ID] = struct{}{}
			producerIDs[conflictProducerKey(candidate.Producer)] = struct{}{}
		}
	}
	if len(assertionFactIDs) != len(factByID) {
		return fmt.Errorf("%w: assertions do not cover all facts", ErrInvalidConflict)
	}
	if len(producerIDs) < 2 {
		return fmt.Errorf("%w: at least two producers are required", ErrInvalidConflict)
	}
	expectedID := conflictID(c)
	if c.ID != expectedID {
		return fmt.Errorf("%w: conflict id does not match deterministic identity", ErrInvalidConflict)
	}
	return nil
}

type conflictSlot struct {
	scope     Scope
	predicate Predicate
	subject   Participant
}

type conflictSlotEncoding struct {
	Scope     Scope       `json:"scope"`
	Predicate Predicate   `json:"predicate"`
	Subject   Participant `json:"subject"`
}

type conflictIdentityEncoding struct {
	Slot    conflictSlotEncoding `json:"slot"`
	FactIDs []string             `json:"fact_ids"`
}

func isConflictPredicate(predicate Predicate) bool {
	switch predicate {
	case PredicateArtifact, PredicateSymbol, PredicateNamedElement, PredicateDefinition:
		return true
	default:
		return false
	}
}

func conflictSlotKey(slot conflictSlot) string {
	encoded, _ := json.Marshal(conflictSlotEncoding{
		Scope:     slot.scope,
		Predicate: slot.predicate,
		Subject:   slot.subject,
	})
	return string(encoded)
}

func conflictAssertionKey(object *Participant, value *TypedValue) string {
	encoded, _ := json.Marshal(struct {
		Object *Participant `json:"object"`
		Value  *TypedValue  `json:"value"`
	}{Object: object, Value: value})
	return string(encoded)
}

func conflictProducerKey(producer Producer) string {
	encoded, _ := json.Marshal(producer)
	return string(encoded)
}

func conflictID(conflict Conflict) string {
	ids := make([]string, len(conflict.Facts))
	for index, candidate := range conflict.Facts {
		ids[index] = candidate.ID
	}
	sort.Strings(ids)
	encoded, _ := json.Marshal(conflictIdentityEncoding{
		Slot: conflictSlotEncoding{
			Scope:     conflict.Scope,
			Predicate: conflict.Predicate,
			Subject:   conflict.Subject,
		},
		FactIDs: ids,
	})
	digest := sha256.New()
	_, _ = digest.Write([]byte(conflictIdentityDomain))
	_, _ = digest.Write(encoded)
	return "conflict-" + hex.EncodeToString(digest.Sum(nil))
}

func sortAndCloneFacts(facts []CanonicalFact) []CanonicalFact {
	ordered := make([]CanonicalFact, len(facts))
	for index, candidate := range facts {
		ordered[index] = cloneCanonicalFact(candidate)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	return ordered
}

func cloneCanonicalFact(candidate CanonicalFact) CanonicalFact {
	clone := candidate
	clone.Object = cloneParticipant(candidate.Object)
	clone.Value = cloneValue(candidate.Value)
	clone.Qualifiers = append([]Qualifier(nil), candidate.Qualifiers...)
	sort.SliceStable(clone.Qualifiers, func(left, right int) bool {
		return qualifierKey(clone.Qualifiers[left]) < qualifierKey(clone.Qualifiers[right])
	})
	clone.Evidence = append([]EvidenceRef(nil), candidate.Evidence...)
	sort.SliceStable(clone.Evidence, func(left, right int) bool {
		return evidenceKey(clone.Evidence[left]) < evidenceKey(clone.Evidence[right])
	})
	if candidate.Lineage != nil {
		lineage := *candidate.Lineage
		lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
		sort.Strings(lineage.InputFactIDs)
		clone.Lineage = &lineage
	}
	return clone
}

func validateAssertionValue(object *Participant, value *TypedValue) error {
	if object != nil && value != nil {
		return fmt.Errorf("%w: assertion cannot carry both object and value", ErrInvalidConflict)
	}
	if object != nil {
		if err := object.Validate(); err != nil {
			return fmt.Errorf("object: %v", err)
		}
	}
	if value != nil {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("value: %v", err)
		}
	}
	return nil
}

func sameCanonicalFact(left, right CanonicalFact) bool {
	leftEncoded, leftErr := CanonicalBytes(left)
	rightEncoded, rightErr := CanonicalBytes(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return string(leftEncoded) == string(rightEncoded)
}
