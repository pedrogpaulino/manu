package derivation

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// DependencyRuleID identifies the initial transitive dependency rule. The
	// rule only composes already observed or derived binary dependency facts; it
	// does not resolve source dependencies by itself.
	DependencyRuleID = "dependency"
	// DependencyRuleVersion identifies the first implementation of
	// DependencyRuleID.
	DependencyRuleVersion = "1"
	// DependencyRuleMethod identifies the derivation method recorded in a fact
	// producer.
	DependencyRuleMethod = "transitive"
)

// DependencyRule composes binary dependency facts. Its version is carried by
// both the derived producer and lineage, so a new registration can be rebuilt
// and distinguished without changing the input facts.
type DependencyRule struct {
	version string
}

var _ Rule = DependencyRule{}

// NewDependencyRule creates the transitive dependency rule for one explicit
// version. The version is carried by both derived producer and lineage.
func NewDependencyRule(version string) DependencyRule {
	return DependencyRule{version: version}
}

// DependencyRuleRegistration returns the initial versioned registration.
func DependencyRuleRegistration() Registration {
	return NewRegistration(
		RuleVersion{RuleID: DependencyRuleID, Version: DependencyRuleVersion},
		NewDependencyRule(DependencyRuleVersion),
	)
}

// DependencyRuleRegistrationForVersion returns a registration for one
// explicit dependency rule version, useful when rebuilding a snapshot with a
// changed implementation.
func DependencyRuleRegistrationForVersion(version string) Registration {
	return NewRegistration(
		RuleVersion{RuleID: DependencyRuleID, Version: version},
		NewDependencyRule(version),
	)
}

func (r DependencyRule) Apply(ctx context.Context, view FactView) ([]fact.CanonicalFact, error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	facts := view.Facts()
	derived := make([]fact.CanonicalFact, 0)
	seen := make(map[string]struct{})
	for leftIndex, left := range facts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if left.Predicate != fact.PredicateDependency || left.Object == nil {
			continue
		}
		for rightIndex, right := range facts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if leftIndex == rightIndex || right.Predicate != fact.PredicateDependency || right.Object == nil {
				continue
			}
			if !sameParticipant(*left.Object, right.Subject) || sameParticipant(left.Subject, *right.Object) {
				continue
			}

			candidate, err := r.derive(left, right)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[candidate.ID]; exists {
				continue
			}
			seen[candidate.ID] = struct{}{}
			derived = append(derived, candidate)
		}
	}

	sort.SliceStable(derived, func(left, right int) bool {
		return derived[left].ID < derived[right].ID
	})
	return derived, nil
}

func (r DependencyRule) derive(left, right fact.CanonicalFact) (fact.CanonicalFact, error) {
	object := *right.Object
	inputIDs := []string{left.ID, right.ID}
	sort.Strings(inputIDs)
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     left.Scope,
		Predicate: fact.PredicateDependency,
		Subject:   left.Subject,
		Object:    &object,
		Producer: fact.Producer{
			ID:      DependencyRuleID,
			Version: r.version,
			Method:  DependencyRuleMethod,
		},
		Evidence: mergeDependencyEvidence(left.Evidence, right.Evidence),
		Lineage: &fact.Lineage{
			RuleID:       DependencyRuleID,
			RuleVersion:  r.version,
			InputFactIDs: inputIDs,
		},
	}
	identifier, err := fact.FactID(candidate)
	if err != nil {
		return fact.CanonicalFact{}, fmt.Errorf("dependency transitive fact identity: %w", err)
	}
	candidate.ID = identifier
	return candidate, nil
}

func sameParticipant(left, right fact.Participant) bool {
	return left.Kind == right.Kind && left.ID == right.ID
}

func mergeDependencyEvidence(groups ...[]fact.EvidenceRef) []fact.EvidenceRef {
	byID := make(map[string]fact.EvidenceRef)
	for _, group := range groups {
		for _, evidence := range group {
			current, exists := byID[evidence.ID]
			if !exists || dependencyEvidenceKey(evidence) < dependencyEvidenceKey(current) {
				byID[evidence.ID] = evidence
			}
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	merged := make([]fact.EvidenceRef, 0, len(ids))
	for _, id := range ids {
		merged = append(merged, byID[id])
	}
	return merged
}

func dependencyEvidenceKey(evidence fact.EvidenceRef) string {
	locator, _ := json.Marshal(evidence.Locator)
	return evidence.ID + "\x00" + string(locator)
}
