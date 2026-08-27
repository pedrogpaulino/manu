package query

import (
	"context"
	"errors"
	"math"
	"reflect"
	"sort"

	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

var (
	// ErrInvalidContextCandidateProjection identifies malformed or conflicting
	// input to the deterministic context candidate adapter.
	ErrInvalidContextCandidateProjection = errors.New("query: invalid context candidate projection")
	// ErrInvalidContextProjection is a concise compatibility alias.
	ErrInvalidContextProjection = ErrInvalidContextCandidateProjection
)

// ContextCandidateProjectionRequest supplies canonical facts and the
// retrieval candidates that may support them. The estimator is applied only
// to copies made by ProjectContextCandidates.
type ContextCandidateProjectionRequest struct {
	Scope            Scope
	Facts            []fact.CanonicalFact
	Retrieval        QueryRetrievalResult
	Estimator        ContextTokenEstimatorConfiguration
	EstimationLimits ContextTokenEstimationLimits
}

// ContextCandidateProjection is the deterministic candidate view consumed by
// context selection and support closure.
type ContextCandidateProjection struct {
	Candidates        []ContextSelectionCandidate
	Relations         []ContextRelationCandidate
	SupportIncomplete bool
}

// Validate checks every projected candidate and relation, including their
// cross-item support references.
func (p ContextCandidateProjection) Validate() error {
	if len(p.Candidates) > maxContextSelectionCandidates || len(p.Relations) > maxContextRelations {
		return ErrInvalidContextCandidateProjection
	}
	itemIDs := make(map[string]struct{}, len(p.Candidates))
	for _, candidate := range p.Candidates {
		if err := candidate.Validate(); err != nil {
			return ErrInvalidContextCandidateProjection
		}
		if _, exists := itemIDs[candidate.Item.ID]; exists {
			return ErrInvalidContextCandidateProjection
		}
		itemIDs[candidate.Item.ID] = struct{}{}
	}
	relationIDs := make(map[string]struct{}, len(p.Relations))
	var itemScope Scope
	if len(p.Candidates) > 0 {
		itemScope = p.Candidates[0].Item.Scope
	}
	for _, candidate := range p.Relations {
		if err := candidate.Validate(); err != nil {
			return ErrInvalidContextCandidateProjection
		}
		if len(p.Candidates) > 0 && !sameScope(candidate.Relation.Scope, itemScope) {
			return ErrInvalidContextCandidateProjection
		}
		if _, exists := relationIDs[candidate.Relation.ID]; exists {
			return ErrInvalidContextCandidateProjection
		}
		if _, exists := itemIDs[candidate.Relation.ID]; exists {
			return ErrInvalidContextCandidateProjection
		}
		relationIDs[candidate.Relation.ID] = struct{}{}
		for _, id := range append(append([]string{}, candidate.Relation.Path...), candidate.Relation.SupportIDs...) {
			if _, exists := itemIDs[id]; !exists {
				return ErrInvalidContextCandidateProjection
			}
		}
	}
	return nil
}

// ProjectContextCandidates adapts recovered evidence and facts into validated,
// deterministic selection and relation candidates. Inputs remain untouched.
func ProjectContextCandidates(ctx context.Context, request ContextCandidateProjectionRequest) (ContextCandidateProjection, error) {
	if ctx == nil {
		return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
	}
	if err := ctx.Err(); err != nil {
		return ContextCandidateProjection{}, err
	}
	if err := request.Scope.Validate(); err != nil {
		return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
	}
	if err := validateContextProjectionFacts(request.Facts); err != nil {
		return ContextCandidateProjection{}, err
	}
	evidenceCandidates, err := normalizeContextProjectionRetrieval(ctx, request.Scope, request.Retrieval)
	if err != nil {
		return ContextCandidateProjection{}, err
	}
	if err := request.EstimationLimits.Validate(); err != nil {
		return ContextCandidateProjection{}, errors.Join(ErrInvalidContextCandidateProjection, err)
	}
	if _, err := request.Estimator.Normalize(); err != nil {
		return ContextCandidateProjection{}, errors.Join(ErrInvalidContextCandidateProjection, err)
	}

	projectedByOriginal := make(map[string]*projectedContextFact, len(request.Facts))
	projectedFacts := make([]*projectedContextFact, 0, len(request.Facts))
	supportIncomplete := false
	for _, original := range request.Facts {
		if err := contextProjectionContextErr(ctx); err != nil {
			return ContextCandidateProjection{}, err
		}
		matched := make([]*contextProjectionEvidence, 0, len(original.Evidence))
		for _, reference := range original.Evidence {
			candidate, exists := evidenceCandidates.byExternalID[reference.ID]
			if !exists {
				candidate, exists = evidenceCandidates.byUnitID[reference.ID]
			}
			if exists {
				matched = append(matched, candidate)
			}
		}
		if len(matched) == 0 {
			continue
		}
		if len(matched) != len(original.Evidence) {
			supportIncomplete = true
			continue
		}
		projected, err := projectContextFact(request.Scope, original, matched)
		if err != nil {
			return ContextCandidateProjection{}, err
		}
		if previous, exists := projectedByOriginal[original.ID]; exists {
			if !reflect.DeepEqual(previous.fact, projected.fact) {
				return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
			}
			continue
		}
		projectedByOriginal[original.ID] = projected
		projectedFacts = append(projectedFacts, projected)
	}

	projectedIDs := make(map[string]string, len(projectedByOriginal))
	for originalID, projected := range projectedByOriginal {
		projectedIDs[originalID] = projected.fact.ID
	}
	for _, projected := range projectedFacts {
		if projected.fact.Lineage == nil {
			continue
		}
		lineage := projected.fact.Lineage
		for index, inputID := range lineage.InputFactIDs {
			projectedID, exists := projectedIDs[inputID]
			if !exists {
				supportIncomplete = true
				continue
			}
			lineage.InputFactIDs[index] = projectedID
		}
		if err := projected.fact.Validate(); err != nil {
			return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
		}
	}

	projection := ContextCandidateProjection{
		Candidates:        make([]ContextSelectionCandidate, 0),
		Relations:         make([]ContextRelationCandidate, 0),
		SupportIncomplete: supportIncomplete,
	}
	itemIDs := make(map[string]struct{})
	if err := appendContextEvidenceCandidates(ctx, &projection, itemIDs, evidenceCandidates, request); err != nil {
		return ContextCandidateProjection{}, err
	}
	entityRecords := make(map[string]*contextProjectionEntity)
	factCandidates := make(map[string]ContextSelectionCandidate)
	relationCandidates := make(map[string]ContextRelationCandidate)
	for _, projected := range projectedFacts {
		if err := contextProjectionContextErr(ctx); err != nil {
			return ContextCandidateProjection{}, err
		}
		candidate, err := contextFactCandidate(projected, request)
		if err != nil {
			return ContextCandidateProjection{}, err
		}
		if previous, exists := factCandidates[candidate.Item.ID]; exists {
			if !reflect.DeepEqual(previous.Item, candidate.Item) {
				return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
			}
			factCandidates[candidate.Item.ID] = betterContextSelectionCandidate(previous, candidate)
		} else {
			factCandidates[candidate.Item.ID] = candidate
		}
		if err := collectContextFactEntities(entityRecords, projected); err != nil {
			return ContextCandidateProjection{}, err
		}
		if projected.fact.Object != nil && projected.fact.Subject.ID != projected.fact.Object.ID {
			relation, err := contextFactRelationCandidate(projected, request)
			if err != nil {
				return ContextCandidateProjection{}, err
			}
			if _, err := EstimateContextRelationCandidateCosts(ctx, &relation, request.Estimator, request.EstimationLimits); err != nil {
				return ContextCandidateProjection{}, errors.Join(ErrInvalidContextCandidateProjection, err)
			}
			if err := relation.Validate(); err != nil {
				return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
			}
			if previous, exists := relationCandidates[relation.Relation.ID]; exists {
				if !reflect.DeepEqual(previous.Relation, relation.Relation) {
					return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
				}
				if relation.Rank < previous.Rank || relation.Rank == previous.Rank && relation.Score > previous.Score {
					relationCandidates[relation.Relation.ID] = relation
				}
			} else {
				relationCandidates[relation.Relation.ID] = relation
			}
		}
	}

	for _, candidate := range sortedContextSelectionCandidates(factCandidates) {
		if err := appendContextSelectionCandidate(ctx, &projection, itemIDs, candidate, request); err != nil {
			return ContextCandidateProjection{}, err
		}
	}
	for _, entity := range sortedContextProjectionEntities(entityRecords) {
		candidate, err := contextEntityCandidate(entity, request)
		if err != nil {
			return ContextCandidateProjection{}, err
		}
		if err := appendContextSelectionCandidate(ctx, &projection, itemIDs, candidate, request); err != nil {
			return ContextCandidateProjection{}, err
		}
	}
	for _, relation := range relationCandidates {
		projection.Relations = append(projection.Relations, relation)
	}

	sort.SliceStable(projection.Relations, func(left, right int) bool {
		if projection.Relations[left].Rank != projection.Relations[right].Rank {
			return projection.Relations[left].Rank < projection.Relations[right].Rank
		}
		return projection.Relations[left].Relation.ID < projection.Relations[right].Relation.ID
	})
	sort.SliceStable(projection.Candidates, func(left, right int) bool {
		if projection.Candidates[left].Rank != projection.Candidates[right].Rank {
			return projection.Candidates[left].Rank < projection.Candidates[right].Rank
		}
		if projection.Candidates[left].Item.Kind != projection.Candidates[right].Item.Kind {
			return projection.Candidates[left].Item.Kind < projection.Candidates[right].Item.Kind
		}
		return projection.Candidates[left].Item.ID < projection.Candidates[right].Item.ID
	})
	if err := projection.Validate(); err != nil {
		return ContextCandidateProjection{}, ErrInvalidContextCandidateProjection
	}
	return projection, nil
}

type contextProjectionEvidence struct {
	externalID string
	unit       evidence.EvidenceUnit
	fusion     retrieval.FusionCandidate
	relation   bool
}

type contextProjectionEvidenceSet struct {
	byUnitID     map[string]*contextProjectionEvidence
	byExternalID map[string]*contextProjectionEvidence
}

type projectedContextFact struct {
	fact     *fact.CanonicalFact
	evidence []*contextProjectionEvidence
	best     contextProjectionEvidence
	relation bool
}

type contextProjectionEntity struct {
	participant fact.Participant
	fact        *fact.CanonicalFact
	evidence    []fact.EvidenceRef
	best        contextProjectionEvidence
	relation    bool
}

func validateContextProjectionFacts(facts []fact.CanonicalFact) error {
	if len(facts) > maxContextItems {
		return ErrInvalidContextCandidateProjection
	}
	seen := make(map[string]fact.CanonicalFact, len(facts))
	var scope *fact.Scope
	for _, current := range facts {
		if err := current.Validate(); err != nil {
			return ErrInvalidContextCandidateProjection
		}
		if scope == nil {
			copyOfScope := current.Scope
			scope = &copyOfScope
		} else if current.Scope != *scope {
			return ErrInvalidContextCandidateProjection
		}
		if previous, exists := seen[current.ID]; exists && !reflect.DeepEqual(previous, current) {
			return ErrInvalidContextCandidateProjection
		}
		seen[current.ID] = current
	}
	return nil
}

func normalizeContextProjectionRetrieval(ctx context.Context, scope Scope, retrievalResult QueryRetrievalResult) (contextProjectionEvidenceSet, error) {
	if len(retrievalResult.Candidates) > maxContextSelectionCandidates {
		return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
	}
	set := contextProjectionEvidenceSet{
		byUnitID:     make(map[string]*contextProjectionEvidence, len(retrievalResult.Candidates)),
		byExternalID: make(map[string]*contextProjectionEvidence, len(retrievalResult.Candidates)),
	}
	byFusionID := make(map[string]string, len(retrievalResult.Candidates))
	for _, candidate := range retrievalResult.Candidates {
		if err := contextProjectionContextErr(ctx); err != nil {
			return contextProjectionEvidenceSet{}, err
		}
		fusion := candidate.Fusion
		if !validContextID(fusion.EvidenceID) || fusion.OrganizationID != scope.OrganizationID ||
			fusion.SourceID != scope.SourceID || fusion.SnapshotID != scope.SnapshotID ||
			fusion.Rank < 0 || fusion.Rank > maxContextSelectionCandidates ||
			math.IsNaN(fusion.Score) || math.IsInf(fusion.Score, 0) {
			return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
		}
		if candidate.CanonicalEvidenceID != "" && candidate.CanonicalEvidenceID != fusion.EvidenceID {
			return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
		}
		unit := candidate.Unit
		if unit.ValidatePrepared() != nil || !validContextID(unit.ID) ||
			unit.OrganizationID != scope.OrganizationID || unit.SourceID != scope.SourceID || unit.SnapshotID != scope.SnapshotID {
			return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
		}
		externalID := candidate.ExternalEvidenceID
		if externalID != "" && !validContextID(externalID) {
			return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
		}
		if previousFusionID, exists := byFusionID[fusion.EvidenceID]; exists && previousFusionID != unit.ID {
			return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
		}
		byFusionID[fusion.EvidenceID] = unit.ID
		current := &contextProjectionEvidence{
			externalID: externalID,
			unit:       unit,
			fusion:     fusion,
			relation:   len(fusion.RelationSignals) > 0,
		}
		previous, exists := set.byUnitID[unit.ID]
		if exists {
			if !reflect.DeepEqual(previous.unit, unit) || previous.externalID != "" && externalID != "" && previous.externalID != externalID {
				return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
			}
			resolvedExternalID := previous.externalID
			if resolvedExternalID == "" {
				resolvedExternalID = externalID
			}
			resolvedRelation := previous.relation || current.relation
			if betterContextProjectionEvidence(*current, *previous) {
				current.externalID = resolvedExternalID
				current.relation = resolvedRelation
				set.byUnitID[unit.ID] = current
				previous = current
			} else {
				previous.externalID = resolvedExternalID
				previous.relation = resolvedRelation
			}
			current = previous
		} else {
			set.byUnitID[unit.ID] = current
			previous = current
		}
		if externalID != "" {
			if prior, exists := set.byExternalID[externalID]; exists && prior.unit.ID != unit.ID {
				return contextProjectionEvidenceSet{}, ErrInvalidContextCandidateProjection
			}
			set.byExternalID[externalID] = previous
		}
		if previous.externalID != "" {
			set.byExternalID[previous.externalID] = previous
		}
	}
	return set, nil
}

func projectContextFact(scope Scope, original fact.CanonicalFact, evidenceCandidates []*contextProjectionEvidence) (*projectedContextFact, error) {
	projected := cloneContextFact(&original)
	projected.Scope = fact.Scope{OrganizationID: scope.OrganizationID, SourceID: scope.SourceID, SnapshotID: scope.SnapshotID}
	projected.Evidence = make([]fact.EvidenceRef, 0, len(evidenceCandidates))
	for _, candidate := range evidenceCandidates {
		projected.Evidence = append(projected.Evidence, fact.EvidenceRef{ID: candidate.unit.ID, Locator: candidate.unit.Locator})
	}
	sort.SliceStable(projected.Evidence, func(left, right int) bool {
		return projected.Evidence[left].ID < projected.Evidence[right].ID
	})
	projected.ID = ""
	projectedID, err := fact.FactID(*projected)
	if err != nil {
		return nil, ErrInvalidContextCandidateProjection
	}
	projected.ID = projectedID
	if err := projected.Validate(); err != nil {
		return nil, ErrInvalidContextCandidateProjection
	}
	best := *evidenceCandidates[0]
	relation := false
	for _, candidate := range evidenceCandidates[1:] {
		if betterContextProjectionEvidence(*candidate, best) {
			best = *candidate
		}
	}
	for _, candidate := range evidenceCandidates {
		relation = relation || candidate.relation
	}
	return &projectedContextFact{fact: projected, evidence: evidenceCandidates, best: best, relation: relation}, nil
}

func appendContextEvidenceCandidates(ctx context.Context, projection *ContextCandidateProjection, itemIDs map[string]struct{}, evidenceCandidates contextProjectionEvidenceSet, request ContextCandidateProjectionRequest) error {
	ordered := make([]*contextProjectionEvidence, 0, len(evidenceCandidates.byUnitID))
	for _, candidate := range evidenceCandidates.byUnitID {
		ordered = append(ordered, candidate)
	}
	sort.SliceStable(ordered, func(left, right int) bool { return ordered[left].unit.ID < ordered[right].unit.ID })
	for _, candidate := range ordered {
		item := ContextItem{
			ID:       candidate.unit.ID,
			Kind:     ContextItemEvidence,
			Origin:   ContextKnowledgeObserved,
			Scope:    request.Scope,
			Evidence: cloneContextEvidence(&candidate.unit),
			Locator:  candidate.unit.Locator,
			Provenance: ContextProvenance{Evidence: []fact.EvidenceRef{{
				ID: candidate.unit.ID, Locator: candidate.unit.Locator,
			}}},
		}
		selectionCandidate := ContextSelectionCandidate{
			Item:          item,
			Relevance:     clampContextProjectionRelevance(candidate.fusion.Score),
			Rank:          candidate.fusion.Rank,
			Aspects:       contextProjectionAspects(ContextItemEvidence, "", fact.ParticipantUnknown, candidate.relation),
			RedundancyKey: item.ID,
		}
		if err := appendContextSelectionCandidate(ctx, projection, itemIDs, selectionCandidate, request); err != nil {
			return err
		}
	}
	return nil
}

func contextFactCandidate(projected *projectedContextFact, request ContextCandidateProjectionRequest) (ContextSelectionCandidate, error) {
	factValue := cloneContextFact(projected.fact)
	producer := factValue.Producer
	provenance := ContextProvenance{
		Producer: &producer,
		Evidence: append([]fact.EvidenceRef(nil), factValue.Evidence...),
	}
	if factValue.Lineage != nil {
		lineage := *factValue.Lineage
		lineage.InputFactIDs = append([]string(nil), factValue.Lineage.InputFactIDs...)
		provenance.Lineage = &lineage
	}
	locator := factValue.Evidence[0].Locator
	item := ContextItem{
		ID:         factValue.ID,
		Kind:       ContextItemFact,
		Origin:     contextProjectionOrigin(factValue.Lineage),
		Scope:      request.Scope,
		Fact:       factValue,
		Locator:    locator,
		Provenance: provenance,
	}
	return ContextSelectionCandidate{
		Item:          item,
		Relevance:     clampContextProjectionRelevance(projected.best.fusion.Score),
		Rank:          projected.best.fusion.Rank,
		Aspects:       contextProjectionAspects(ContextItemFact, factValue.Predicate, factValue.Subject.Kind, projected.relation),
		RedundancyKey: item.ID,
	}, nil
}

func collectContextFactEntities(records map[string]*contextProjectionEntity, projected *projectedContextFact) error {
	participants := []fact.Participant{projected.fact.Subject}
	if projected.fact.Object != nil {
		participants = append(participants, *projected.fact.Object)
	}
	for _, participant := range participants {
		if err := participant.Validate(); err != nil || !validContextID(participant.ID) {
			return ErrInvalidContextCandidateProjection
		}
		current := &contextProjectionEntity{
			participant: participant,
			fact:        projected.fact,
			evidence:    append([]fact.EvidenceRef(nil), projected.fact.Evidence...),
			best:        projected.best,
			relation:    projected.relation,
		}
		previous, exists := records[participant.ID]
		if !exists {
			records[participant.ID] = current
			continue
		}
		if previous.participant != participant {
			return ErrInvalidContextCandidateProjection
		}
		previous.relation = previous.relation || current.relation
		if betterContextProjectionEvidence(current.best, previous.best) {
			current.relation = current.relation || previous.relation
			records[participant.ID] = current
		}
	}
	return nil
}

func contextEntityCandidate(entity *contextProjectionEntity, request ContextCandidateProjectionRequest) (ContextSelectionCandidate, error) {
	producer := entity.fact.Producer
	provenance := ContextProvenance{
		Producer: &producer,
		Evidence: append([]fact.EvidenceRef(nil), entity.evidence...),
	}
	if entity.fact.Lineage != nil {
		lineage := *entity.fact.Lineage
		lineage.InputFactIDs = append([]string(nil), entity.fact.Lineage.InputFactIDs...)
		provenance.Lineage = &lineage
	}
	locator := entity.evidence[0].Locator
	item := ContextItem{
		ID:         entity.participant.ID,
		Kind:       ContextItemEntity,
		Origin:     contextProjectionOrigin(entity.fact.Lineage),
		Scope:      request.Scope,
		Entity:     cloneContextParticipant(&entity.participant),
		Locator:    locator,
		Provenance: provenance,
	}
	return ContextSelectionCandidate{
		Item:          item,
		Relevance:     clampContextProjectionRelevance(entity.best.fusion.Score),
		Rank:          entity.best.fusion.Rank,
		Aspects:       contextProjectionAspects(ContextItemEntity, "", entity.participant.Kind, entity.relation),
		RedundancyKey: item.ID,
	}, nil
}

func contextFactRelationCandidate(projected *projectedContextFact, request ContextCandidateProjectionRequest) (ContextRelationCandidate, error) {
	producer := projected.fact.Producer
	provenance := ContextProvenance{
		Producer: &producer,
		Evidence: append([]fact.EvidenceRef(nil), projected.fact.Evidence...),
	}
	if projected.fact.Lineage != nil {
		lineage := *projected.fact.Lineage
		lineage.InputFactIDs = append([]string(nil), projected.fact.Lineage.InputFactIDs...)
		provenance.Lineage = &lineage
	}
	fromID := projected.fact.Subject.ID
	toID := projected.fact.Object.ID
	relation := ContextRelation{
		ID:         identity.CanonicalUUID("relation", request.Scope.OrganizationID, request.Scope.SourceID, request.Scope.SnapshotID, projected.fact.ID),
		Predicate:  projected.fact.Predicate,
		Origin:     contextProjectionOrigin(projected.fact.Lineage),
		Scope:      request.Scope,
		FromID:     fromID,
		ToID:       toID,
		Path:       []string{fromID, toID},
		SupportIDs: contextProjectionSupportIDs(projected.fact),
		Provenance: provenance,
	}
	candidate := ContextRelationCandidate{
		Relation: relation,
		Score:    clampContextProjectionRelevance(projected.best.fusion.Score),
		Rank:     projected.best.fusion.Rank,
	}
	if err := candidate.Relation.Validate(); err != nil {
		return ContextRelationCandidate{}, ErrInvalidContextCandidateProjection
	}
	return candidate, nil
}

func contextProjectionSupportIDs(projected *fact.CanonicalFact) []string {
	ids := make([]string, 0, 1+len(projected.Evidence))
	ids = append(ids, projected.ID)
	for _, reference := range projected.Evidence {
		ids = append(ids, reference.ID)
	}
	sort.Strings(ids[1:])
	return ids
}

func appendContextSelectionCandidate(ctx context.Context, projection *ContextCandidateProjection, itemIDs map[string]struct{}, candidate ContextSelectionCandidate, request ContextCandidateProjectionRequest) error {
	if err := contextProjectionContextErr(ctx); err != nil {
		return err
	}
	if _, exists := itemIDs[candidate.Item.ID]; exists {
		return ErrInvalidContextCandidateProjection
	}
	if candidate.Item.Scope != request.Scope {
		return ErrInvalidContextCandidateProjection
	}
	if _, err := EstimateContextSelectionCandidateCosts(ctx, &candidate, request.Estimator, request.EstimationLimits); err != nil {
		return errors.Join(ErrInvalidContextCandidateProjection, err)
	}
	if err := candidate.Validate(); err != nil {
		return ErrInvalidContextCandidateProjection
	}
	itemIDs[candidate.Item.ID] = struct{}{}
	projection.Candidates = append(projection.Candidates, candidate)
	return nil
}

func sortedContextSelectionCandidates(candidates map[string]ContextSelectionCandidate) []ContextSelectionCandidate {
	ordered := make([]ContextSelectionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		ordered = append(ordered, candidate)
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].Rank != ordered[right].Rank {
			return ordered[left].Rank < ordered[right].Rank
		}
		if ordered[left].Item.Kind != ordered[right].Item.Kind {
			return ordered[left].Item.Kind < ordered[right].Item.Kind
		}
		return ordered[left].Item.ID < ordered[right].Item.ID
	})
	return ordered
}

func sortedContextProjectionEntities(records map[string]*contextProjectionEntity) []*contextProjectionEntity {
	ordered := make([]*contextProjectionEntity, 0, len(records))
	for _, record := range records {
		ordered = append(ordered, record)
	}
	sort.SliceStable(ordered, func(left, right int) bool {
		if ordered[left].best.fusion.Rank != ordered[right].best.fusion.Rank {
			return ordered[left].best.fusion.Rank < ordered[right].best.fusion.Rank
		}
		return ordered[left].participant.ID < ordered[right].participant.ID
	})
	return ordered
}

func betterContextSelectionCandidate(left, right ContextSelectionCandidate) ContextSelectionCandidate {
	if left.Relevance > right.Relevance || left.Relevance == right.Relevance && left.Rank < right.Rank {
		return left
	}
	return right
}

func betterContextProjectionEvidence(left, right contextProjectionEvidence) bool {
	if left.fusion.Score != right.fusion.Score {
		return left.fusion.Score > right.fusion.Score
	}
	if left.fusion.Rank != right.fusion.Rank {
		return left.fusion.Rank < right.fusion.Rank
	}
	return left.fusion.EvidenceID < right.fusion.EvidenceID
}

func clampContextProjectionRelevance(value float64) float64 {
	if value < 0.01 {
		return 0.01
	}
	if value > 1 {
		return 1
	}
	return value
}

func contextProjectionOrigin(lineage *fact.Lineage) ContextKnowledgeKind {
	if lineage != nil {
		return ContextKnowledgeDerived
	}
	return ContextKnowledgeObserved
}

func contextProjectionAspects(kind ContextItemKind, predicate fact.Predicate, participantKind fact.ParticipantKind, relation bool) []string {
	var aspects []string
	switch kind {
	case ContextItemEvidence:
		aspects = []string{"evidence", "source"}
	case ContextItemFact:
		if predicate == fact.PredicateDependency {
			aspects = []string{"dependency", "evidence"}
		} else {
			aspects = []string{"fact", "evidence"}
		}
	case ContextItemEntity:
		switch participantKind {
		case fact.ParticipantArtifact:
			aspects = []string{"artifact", "locator"}
		case fact.ParticipantNamedElement:
			aspects = []string{"named_element", "locator"}
		default:
			aspects = []string{"symbol", "locator"}
		}
	default:
		aspects = []string{"provenance", "locator"}
	}
	if relation {
		aspects = append(aspects, "relation")
	}
	return aspects
}

func contextProjectionContextErr(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContextCandidateProjection
	}
	return ctx.Err()
}
