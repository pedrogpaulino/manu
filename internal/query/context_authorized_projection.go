package query

import (
	"context"
	"errors"
	"reflect"

	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

var (
	// ErrInvalidContextAuthorizedProjection identifies a malformed candidate
	// projection or an unsafe policy result without exposing its payload.
	ErrInvalidContextAuthorizedProjection = errors.New("query: invalid context authorized projection")
	// ErrInvalidAuthorizedContextProjection is the descriptive spelling kept
	// for callers that name the operation before the projected value.
	ErrInvalidAuthorizedContextProjection = ErrInvalidContextAuthorizedProjection
)

// ContextAuthorizedCandidateProjection is the candidate view after explicit
// authorization and transfer-policy application. Policy contains the
// content-free audit and continuation result produced for the same items.
type ContextAuthorizedCandidateProjection struct {
	Candidates []ContextSelectionCandidate `json:"candidates"`
	Relations  []ContextRelationCandidate  `json:"relations,omitempty"`
	Policy     ContextPolicyResult         `json:"policy"`
}

// AuthorizedContextCandidateProjection is a descriptive alias for the
// policy-authorized candidate projection.
type AuthorizedContextCandidateProjection = ContextAuthorizedCandidateProjection

// Validate checks the policy result, candidate remapping, relation closure
// and cost accounting without consulting a source or mutating any value.
func (p ContextAuthorizedCandidateProjection) Validate() error {
	if err := p.Policy.Validate(); err != nil {
		return ErrInvalidContextAuthorizedProjection
	}
	if len(p.Candidates) > maxContextSelectionCandidates || len(p.Relations) > maxContextRelations {
		return ErrInvalidContextAuthorizedProjection
	}

	policyItems := make(map[string]ContextItem, len(p.Policy.Items))
	for _, item := range p.Policy.Items {
		policyItems[item.ID] = item
	}
	seenCandidates := make(map[string]struct{}, len(p.Candidates))
	for _, candidate := range p.Candidates {
		if err := candidate.Validate(); err != nil {
			return ErrInvalidContextAuthorizedProjection
		}
		if candidate.RedundancyKey != candidate.Item.ID {
			return ErrInvalidContextAuthorizedProjection
		}
		if _, duplicate := seenCandidates[candidate.Item.ID]; duplicate {
			return ErrInvalidContextAuthorizedProjection
		}
		policyItem, included := policyItems[candidate.Item.ID]
		if !included || !reflect.DeepEqual(policyItem, candidate.Item) {
			return ErrInvalidContextAuthorizedProjection
		}
		seenCandidates[candidate.Item.ID] = struct{}{}
	}
	if len(seenCandidates) != len(policyItems) {
		return ErrInvalidContextAuthorizedProjection
	}

	if len(p.Candidates) == 0 {
		if len(p.Policy.Items) != 0 || len(p.Policy.ContinuationIDs) != 0 {
			return ErrInvalidContextAuthorizedProjection
		}
	} else {
		for index, candidate := range p.Candidates {
			if !sameScope(candidate.Item.Scope, p.Policy.Scope) {
				return ErrInvalidContextAuthorizedProjection
			}
			if index >= len(p.Policy.ContinuationIDs) || p.Policy.ContinuationIDs[index] != candidate.Item.ID {
				return ErrInvalidContextAuthorizedProjection
			}
		}
		if len(p.Policy.ContinuationIDs) != len(p.Candidates) {
			return ErrInvalidContextAuthorizedProjection
		}
	}

	policyRelations := make(map[string]ContextRelation, len(p.Policy.Relations))
	for _, relation := range p.Policy.Relations {
		policyRelations[relation.ID] = relation
	}
	seenRelations := make(map[string]struct{}, len(p.Relations))
	for _, candidate := range p.Relations {
		if err := candidate.Validate(); err != nil {
			return ErrInvalidContextAuthorizedProjection
		}
		if _, duplicate := seenRelations[candidate.Relation.ID]; duplicate {
			return ErrInvalidContextAuthorizedProjection
		}
		policyRelation, included := policyRelations[candidate.Relation.ID]
		if !included || !reflect.DeepEqual(policyRelation, candidate.Relation) {
			return ErrInvalidContextAuthorizedProjection
		}
		if !sameScope(candidate.Relation.Scope, p.Policy.Scope) {
			return ErrInvalidContextAuthorizedProjection
		}
		seenRelations[candidate.Relation.ID] = struct{}{}
	}
	if len(seenRelations) != len(policyRelations) {
		return ErrInvalidContextAuthorizedProjection
	}
	return nil
}

// AuthorizeContextCandidateProjection applies the explicit Allow decision to
// every candidate in scope, then lets the transfer policy redact or exclude
// unsafe representations. ItemAudit is used to map original candidate
// metadata to final output IDs; all costs are recomputed from detached output
// values. An empty projection is valid when scope is supplied explicitly.
func AuthorizeContextCandidateProjection(
	ctx context.Context,
	scope Scope,
	projection ContextCandidateProjection,
	policy *evidence.Policy,
	estimator ContextTokenEstimatorConfiguration,
	limits ContextTokenEstimationLimits,
) (ContextAuthorizedCandidateProjection, error) {
	if ctx == nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}
	if err := ctx.Err(); err != nil {
		return ContextAuthorizedCandidateProjection{}, err
	}
	if err := scope.Validate(); err != nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}
	if err := projection.Validate(); err != nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}
	if err := limits.Validate(); err != nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}
	if _, err := estimator.Normalize(); err != nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}

	items := make([]ContextItem, 0, len(projection.Candidates))
	authorizations := make([]ContextItemAuthorization, 0, len(projection.Candidates))
	metadataByID := make(map[string]ContextSelectionCandidate, len(projection.Candidates))
	for _, candidate := range projection.Candidates {
		if !sameScope(candidate.Item.Scope, scope) {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		item := cloneAuthorizedContextItem(candidate.Item)
		items = append(items, item)
		authorizations = append(authorizations, ContextItemAuthorization{
			ItemID:   item.ID,
			Decision: evidence.DecisionAllow,
		})
		metadataByID[item.ID] = cloneContextSelectionCandidate(candidate)
	}

	relations := make([]ContextRelation, 0, len(projection.Relations))
	relationMetadataByID := make(map[string]ContextRelationCandidate, len(projection.Relations))
	for _, candidate := range projection.Relations {
		if !sameScope(candidate.Relation.Scope, scope) {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		relation := cloneAuthorizedContextRelation(candidate.Relation)
		relations = append(relations, relation)
		relationMetadataByID[relation.ID] = cloneContextRelationCandidate(candidate)
	}

	transferPolicy := cloneContextAuthorizedPolicy(policy)
	digest, err := ContextPolicyDigest(transferPolicy, authorizations)
	if err != nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}
	request := ContextPolicyRequest{
		Scope:           scope,
		Items:           items,
		Relations:       relations,
		Authorizations:  authorizations,
		TransferPolicy:  transferPolicy,
		PolicyDigest:    digest,
		ContinuationIDs: contextAuthorizedProjectionIDs(items),
	}
	policyResult, err := ApplyContextPolicy(ctx, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ContextAuthorizedCandidateProjection{}, err
		}
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}

	result := ContextAuthorizedCandidateProjection{
		Candidates: make([]ContextSelectionCandidate, 0, len(policyResult.Items)),
		Relations:  make([]ContextRelationCandidate, 0, len(policyResult.Relations)),
		Policy:     policyResult,
	}
	for _, audit := range policyResult.ItemAudit {
		if err := contextAuthorizedProjectionContextErr(ctx); err != nil {
			return ContextAuthorizedCandidateProjection{}, err
		}
		if !audit.Included {
			continue
		}
		metadata, exists := metadataByID[audit.ItemID]
		if !exists {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		item, exists := contextPolicyFindItem(policyResult.Items, audit.OutputID)
		if !exists {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		metadata.Item = cloneAuthorizedContextItem(item)
		metadata.RedundancyKey = item.ID
		if _, err := EstimateContextSelectionCandidateCosts(ctx, &metadata, estimator, limits); err != nil {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		if err := metadata.Validate(); err != nil {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		result.Candidates = append(result.Candidates, metadata)
	}

	for _, relation := range policyResult.Relations {
		if err := contextAuthorizedProjectionContextErr(ctx); err != nil {
			return ContextAuthorizedCandidateProjection{}, err
		}
		metadata, exists := relationMetadataByID[relation.ID]
		if !exists {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		metadata.Relation = cloneAuthorizedContextRelation(relation)
		if _, err := EstimateContextRelationCandidateCosts(ctx, &metadata, estimator, limits); err != nil {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		if err := metadata.Validate(); err != nil {
			return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
		}
		result.Relations = append(result.Relations, metadata)
	}
	if err := contextAuthorizedProjectionContextErr(ctx); err != nil {
		return ContextAuthorizedCandidateProjection{}, err
	}
	if err := result.Validate(); err != nil {
		return ContextAuthorizedCandidateProjection{}, ErrInvalidContextAuthorizedProjection
	}
	return result, nil
}

func cloneContextSelectionCandidate(candidate ContextSelectionCandidate) ContextSelectionCandidate {
	clone := candidate
	clone.Item = cloneAuthorizedContextItem(candidate.Item)
	clone.Aspects = cloneAuthorizedContextStrings(candidate.Aspects)
	return clone
}

func cloneContextRelationCandidate(candidate ContextRelationCandidate) ContextRelationCandidate {
	clone := candidate
	clone.Relation = cloneAuthorizedContextRelation(candidate.Relation)
	return clone
}

func cloneAuthorizedContextItem(item ContextItem) ContextItem {
	clone := item
	clone.Fact = cloneAuthorizedContextFact(item.Fact)
	if item.Entity != nil {
		entity := *item.Entity
		clone.Entity = &entity
	}
	clone.Evidence = cloneAuthorizedContextEvidence(item.Evidence)
	clone.Provenance = cloneAuthorizedContextProvenance(item.Provenance)
	clone.SupportIDs = cloneAuthorizedContextStrings(item.SupportIDs)
	return clone
}

func cloneAuthorizedContextRelation(relation ContextRelation) ContextRelation {
	clone := relation
	clone.Path = cloneAuthorizedContextStrings(relation.Path)
	clone.SupportIDs = cloneAuthorizedContextStrings(relation.SupportIDs)
	clone.Provenance = cloneAuthorizedContextProvenance(relation.Provenance)
	return clone
}

func cloneAuthorizedContextFact(value *fact.CanonicalFact) *fact.CanonicalFact {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Object = cloneAuthorizedContextParticipant(value.Object)
	if value.Value != nil {
		typedValue := *value.Value
		clone.Value = &typedValue
	}
	clone.Qualifiers = cloneAuthorizedContextQualifiers(value.Qualifiers)
	clone.Evidence = cloneAuthorizedContextEvidenceRefs(value.Evidence)
	if value.Lineage != nil {
		lineage := *value.Lineage
		lineage.InputFactIDs = cloneAuthorizedContextStrings(value.Lineage.InputFactIDs)
		clone.Lineage = &lineage
	}
	return &clone
}

func cloneAuthorizedContextParticipant(value *fact.Participant) *fact.Participant {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneAuthorizedContextEvidence(value *evidence.EvidenceUnit) *evidence.EvidenceUnit {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Findings = cloneAuthorizedContextStrings(value.Findings)
	return &clone
}

func cloneAuthorizedContextProvenance(value ContextProvenance) ContextProvenance {
	clone := value
	if value.Producer != nil {
		producer := *value.Producer
		clone.Producer = &producer
	}
	if value.Lineage != nil {
		lineage := *value.Lineage
		lineage.InputFactIDs = cloneAuthorizedContextStrings(value.Lineage.InputFactIDs)
		clone.Lineage = &lineage
	}
	clone.Evidence = cloneAuthorizedContextEvidenceRefs(value.Evidence)
	return clone
}

func cloneAuthorizedContextQualifiers(values []fact.Qualifier) []fact.Qualifier {
	if values == nil {
		return nil
	}
	clone := make([]fact.Qualifier, len(values))
	copy(clone, values)
	return clone
}

func cloneAuthorizedContextEvidenceRefs(values []fact.EvidenceRef) []fact.EvidenceRef {
	if values == nil {
		return nil
	}
	clone := make([]fact.EvidenceRef, len(values))
	copy(clone, values)
	return clone
}

func cloneAuthorizedContextStrings(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func cloneContextAuthorizedPolicy(policy *evidence.Policy) *evidence.Policy {
	if policy == nil {
		return nil
	}
	clone := *policy
	if policy.Classifications != nil {
		clone.Classifications = make(map[evidence.Classification]evidence.PolicyLayer, len(policy.Classifications))
		for classification, layer := range policy.Classifications {
			clone.Classifications[classification] = layer
		}
	}
	return &clone
}

func contextAuthorizedProjectionIDs(items []ContextItem) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

func contextAuthorizedProjectionContextErr(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContextAuthorizedProjection
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
