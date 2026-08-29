package query

import "github.com/pedrogpaulino/manu/internal/evidence"

func contextPolicyModeRequestValue(mode ContextPolicyMode) ContextPolicyMode {
	if mode == ContextPolicyModeLocal {
		return ContextPolicyModeLocal
	}
	return ""
}

func contextPolicyValidPreparedRepresentationForMode(unit evidence.EvidenceUnit, mode ContextPolicyMode) bool {
	if mode == ContextPolicyModeLocal {
		return contextPolicyValidLocalPreparedRepresentation(unit)
	}
	return contextPolicyValidPreparedRepresentation(unit)
}

// contextPolicyForPersistence builds a policy that preserves both the local
// persistence floor and the canonical external-transfer floor. The latter is
// metadata in a local package; it never changes which locally authorized
// representation is retained.
func contextPolicyForPersistence(
	policy *evidence.Policy,
	persistFloor evidence.Decision,
	externalFloor evidence.Decision,
) evidence.Policy {
	if policy == nil {
		return evidence.Policy{Installation: evidence.PolicyLayer{
			Persist:          persistFloor,
			ExternalTransfer: externalFloor,
		}}
	}

	result := *policy
	if policy.IsZero() {
		result = evidence.DefaultPolicy()
	} else if policy.Classifications != nil {
		result.Classifications = make(map[evidence.Classification]evidence.PolicyLayer, len(policy.Classifications))
		for classification, layer := range policy.Classifications {
			result.Classifications[classification] = layer
		}
	}
	combined, err := evidence.CombinePolicyLayers(
		result.Installation,
		evidence.PolicyLayer{Persist: persistFloor, ExternalTransfer: externalFloor},
	)
	if err != nil {
		result.Installation.Persist = evidence.DecisionDeny
		result.Installation.ExternalTransfer = evidence.DecisionDeny
		return result
	}
	result.Installation = combined
	return result
}

// contextPolicyValidLocalPreparedRepresentation accepts only retained local
// text. Unlike the external representation, a present safe unit may retain
// content while ExternalTransfer is deny.
func contextPolicyValidLocalPreparedRepresentation(unit evidence.EvidenceUnit) bool {
	if unit.ValidatePrepared() != nil || unit.Persist == evidence.DecisionDeny {
		return false
	}
	inspection := evidence.InspectContent(unit.Content)
	if inspection.Classification != evidence.ClassificationSafeText {
		return false
	}
	switch unit.ContentState {
	case evidence.ContentStatePresent:
		return unit.Content != ""
	case evidence.ContentStateRedacted:
		return unit.Content == evidence.RedactedContent
	default:
		return false
	}
}

// contextPolicyFilterLocalDependentItems removes facts, entities and other
// items whose support is not present in the authorized local representation.
// It also requires every provenance and derivation reference to resolve to a
// retained item, so incomplete support cannot be presented as a complete fact.
func contextPolicyFilterLocalDependentItems(
	result *ContextPolicyResult,
	remap *map[string]string,
	originalIDs map[string]struct{},
	initialOutputToOriginal map[string]string,
) bool {
	filtered := false
	for {
		outputToOriginal := make(map[string]string, len(*remap))
		for originalID, outputID := range *remap {
			outputToOriginal[outputID] = originalID
		}
		availableOutputs := make(map[string]struct{}, len(outputToOriginal))
		for outputID := range outputToOriginal {
			availableOutputs[outputID] = struct{}{}
		}
		remove := make(map[string]ContextPolicyItemAuditReason)
		for index := range result.Items {
			item := &result.Items[index]
			originalID, known := outputToOriginal[item.ID]
			if !known {
				continue
			}
			for _, supportID := range item.SupportIDs {
				if remapped, ok := (*remap)[supportID]; ok {
					if remapped == item.ID {
						remove[originalID] = ContextPolicyItemExcludedSupport
						break
					}
					continue
				}
				if _, outputReference := availableOutputs[supportID]; !outputReference {
					remove[originalID] = ContextPolicyItemExcludedSupport
					break
				}
			}
			if _, dependent := remove[originalID]; dependent {
				continue
			}

			for _, reference := range item.Provenance.Evidence {
				originalReferenceID, inputItem := initialOutputToOriginal[reference.ID]
				if !inputItem {
					remove[originalID] = ContextPolicyItemExcludedSupport
					break
				}
				if _, available := (*remap)[originalReferenceID]; !available {
					remove[originalID] = ContextPolicyItemExcludedSupport
					break
				}
			}
			if _, dependent := remove[originalID]; dependent {
				continue
			}
			if item.Provenance.Lineage != nil {
				for _, inputID := range item.Provenance.Lineage.InputFactIDs {
					originalInputID, inputItem := initialOutputToOriginal[inputID]
					if !inputItem {
						remove[originalID] = ContextPolicyItemExcludedSupport
						break
					}
					if _, available := (*remap)[originalInputID]; !available {
						remove[originalID] = ContextPolicyItemExcludedSupport
						break
					}
				}
			}
			if _, dependent := remove[originalID]; dependent {
				continue
			}

			item.SupportIDs = contextPolicyRemapIDs(item.SupportIDs, *remap)
			item.Provenance = contextPolicyRemapLocalProvenance(item.Provenance, *remap)
			if !contextPolicySafeItemRepresentation(*item) {
				remove[originalID] = ContextPolicyItemExcludedInspection
			}
		}
		if len(remove) == 0 {
			return filtered
		}

		filtered = true
		retained := make([]ContextItem, 0, len(result.Items)-len(remove))
		for _, item := range result.Items {
			originalID := outputToOriginal[item.ID]
			if reason, excluded := remove[originalID]; excluded {
				delete(*remap, originalID)
				for index := range result.ItemAudit {
					if result.ItemAudit[index].ItemID == originalID {
						result.ItemAudit[index].OutputID = ""
						result.ItemAudit[index].Included = false
						result.ItemAudit[index].Redacted = false
						result.ItemAudit[index].Reason = reason
						break
					}
				}
				continue
			}
			retained = append(retained, item)
		}
		result.Items = retained
	}
}

func contextPolicyRemapLocalProvenance(provenance ContextProvenance, remap map[string]string) ContextProvenance {
	result := contextPolicyRemapProvenance(provenance, remap)
	if result.Lineage != nil {
		result.Lineage.InputFactIDs = contextPolicyRemapIDs(result.Lineage.InputFactIDs, remap)
	}
	return result
}
