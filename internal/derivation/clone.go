package derivation

import (
	"sort"

	"github.com/pedrogpaulino/manu/internal/fact"
)

func cloneFacts(input []fact.CanonicalFact) []fact.CanonicalFact {
	cloned := make([]fact.CanonicalFact, len(input))
	for index, candidate := range input {
		cloned[index] = cloneFact(candidate)
	}
	return cloned
}

func cloneFact(candidate fact.CanonicalFact) fact.CanonicalFact {
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
		return clone.Qualifiers[left].Name < clone.Qualifiers[right].Name
	})
	clone.Evidence = append([]fact.EvidenceRef(nil), candidate.Evidence...)
	sort.SliceStable(clone.Evidence, func(left, right int) bool {
		return clone.Evidence[left].ID < clone.Evidence[right].ID
	})
	if candidate.Lineage != nil {
		lineage := *candidate.Lineage
		lineage.InputFactIDs = append([]string(nil), candidate.Lineage.InputFactIDs...)
		sort.Strings(lineage.InputFactIDs)
		clone.Lineage = &lineage
	}
	return clone
}
