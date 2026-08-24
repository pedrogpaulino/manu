package derivation

import (
	"context"
	"sort"

	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// MembershipRuleID is the stable identity of the definition-to-membership
	// rule. Changing the identity describes a different derivation rule.
	MembershipRuleID = "membership"
	// MembershipRuleVersion is the initial version of MembershipRule.
	MembershipRuleVersion = "1"
	// MembershipRuleMethod identifies the deterministic transformation in the
	// producer metadata of facts emitted by MembershipRule.
	MembershipRuleMethod = "membership"
)

// MembershipRule derives a membership relation from a definition whose
// symbol belongs to exactly one artifact in the current factual view.
//
// The rule is deliberately conservative: it only reads definition facts with
// a symbol subject and artifact object, does not follow membership relations,
// and abstains when one symbol has definitions for multiple artifacts. The
// version is kept on the rule value so a new registered version can produce a
// distinct, inspectable derivation without changing observed facts.
type MembershipRule struct {
	version string
}

var _ Rule = MembershipRule{}

// NewMembershipRule creates the membership rule implementation for one
// explicit registry version.
func NewMembershipRule(version string) MembershipRule {
	return MembershipRule{version: version}
}

// MembershipRuleRegistration returns the initial versioned registration.
func MembershipRuleRegistration() Registration {
	return MembershipRuleRegistrationForVersion(MembershipRuleVersion)
}

// MembershipRuleRegistrationForVersion returns a registration for one
// explicit rule version. The version remains visible to the registry and to
// lineage validation.
func MembershipRuleRegistrationForVersion(version string) Registration {
	return NewRegistration(
		RuleVersion{RuleID: MembershipRuleID, Version: version},
		NewMembershipRule(version),
	)
}

// Apply implements Rule.
func (r MembershipRule) Apply(ctx context.Context, view FactView) ([]fact.CanonicalFact, error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	version := r.version
	if version == "" {
		version = MembershipRuleVersion
	}

	// Keep only the lexically first definition for each symbol/artifact pair.
	// The executor already supplies an ID-ordered view, but comparing IDs here
	// also makes the rule deterministic when used by another FactView creator.
	bySubject := make(map[string]map[string]fact.CanonicalFact)
	for _, candidate := range view.Facts() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !isMembershipDefinition(candidate) {
			continue
		}
		byArtifact := bySubject[candidate.Subject.ID]
		if byArtifact == nil {
			byArtifact = make(map[string]fact.CanonicalFact)
			bySubject[candidate.Subject.ID] = byArtifact
		}
		artifactID := candidate.Object.ID
		current, exists := byArtifact[artifactID]
		if !exists || candidate.ID < current.ID {
			byArtifact[artifactID] = candidate
		}
	}

	subjectIDs := make([]string, 0, len(bySubject))
	for subjectID := range bySubject {
		subjectIDs = append(subjectIDs, subjectID)
	}
	sort.Strings(subjectIDs)

	derived := make([]fact.CanonicalFact, 0, len(subjectIDs))
	for _, subjectID := range subjectIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		byArtifact := bySubject[subjectID]
		if len(byArtifact) != 1 {
			// More than one artifact is an ambiguous definition relation. A
			// conservative rule must not publish either possible membership.
			continue
		}
		for _, definition := range byArtifact {
			candidate, err := membershipFact(definition, version)
			if err != nil {
				return nil, err
			}
			derived = append(derived, candidate)
		}
	}
	sort.SliceStable(derived, func(left, right int) bool {
		return derived[left].ID < derived[right].ID
	})
	return derived, nil
}

func isMembershipDefinition(candidate fact.CanonicalFact) bool {
	return candidate.Predicate == fact.PredicateDefinition &&
		candidate.Subject.Kind == fact.ParticipantSymbol &&
		candidate.Object != nil &&
		candidate.Object.Kind == fact.ParticipantArtifact &&
		candidate.Value == nil
}

func membershipFact(definition fact.CanonicalFact, version string) (fact.CanonicalFact, error) {
	candidate := fact.CanonicalFact{
		Version:   fact.Version,
		Scope:     definition.Scope,
		Predicate: fact.PredicateMembership,
		Subject:   definition.Subject,
		Object: &fact.Participant{
			Kind: fact.ParticipantArtifact,
			ID:   definition.Object.ID,
		},
		Producer: fact.Producer{
			ID:      MembershipRuleID,
			Version: version,
			Method:  MembershipRuleMethod,
		},
		Evidence: sustainingEvidence(definition.Evidence),
		Lineage: &fact.Lineage{
			RuleID:       MembershipRuleID,
			RuleVersion:  version,
			InputFactIDs: []string{definition.ID},
		},
	}

	id, err := fact.FactID(candidate)
	if err != nil {
		// The executor validates every observed input before calling a rule. This
		// guard keeps direct rule use conservative without exposing source data.
		return fact.CanonicalFact{}, ErrInvalidOutput
	}
	candidate.ID = id
	return candidate, nil
}

func sustainingEvidence(evidence []fact.EvidenceRef) []fact.EvidenceRef {
	byID := make(map[string]fact.EvidenceRef, len(evidence))
	for _, reference := range evidence {
		if _, exists := byID[reference.ID]; !exists {
			byID[reference.ID] = reference
		}
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]fact.EvidenceRef, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result
}
