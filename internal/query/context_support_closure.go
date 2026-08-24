package query

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
)

// ContextRelationCandidate carries a relation and the explicit costs needed
// to add that relation to a selected context. Costs are supplied by the
// caller; this boundary does not estimate tokens or content sizes.
type ContextRelationCandidate struct {
	Relation      ContextRelation `json:"relation"`
	Score         float64         `json:"score"`
	Rank          int             `json:"rank"`
	TokenCost     int             `json:"token_cost"`
	CharacterCost int64           `json:"character_cost"`
	ByteCost      int64           `json:"byte_cost"`
	Tokens        int             `json:"tokens,omitempty"`
	Characters    int64           `json:"characters,omitempty"`
	Bytes         int64           `json:"bytes,omitempty"`
}

// RelationCandidate is the concise spelling for ContextRelationCandidate.
type RelationCandidate = ContextRelationCandidate

// ContextSupportClosureRequest supplies the original selection request and
// result together with the relations that may be closed over selected items.
type ContextSupportClosureRequest struct {
	Request   ContextSelectionRequest    `json:"request"`
	Base      ContextSelectionResult     `json:"base"`
	Relations []ContextRelationCandidate `json:"relations"`
}

// ContextClosureRequest is a concise alias for ContextSupportClosureRequest.
type ContextClosureRequest = ContextSupportClosureRequest

// ContextRelationClosureRequest is a descriptive alias for the closure
// request.
type ContextRelationClosureRequest = ContextSupportClosureRequest

// ContextRelationAuditReason records why one relation was or was not closed.
type ContextRelationAuditReason string

const (
	ContextRelationIncluded               ContextRelationAuditReason = "included"
	ContextRelationExcludedMissingSupport ContextRelationAuditReason = "excluded_missing_support"
	ContextRelationExcludedBudget         ContextRelationAuditReason = "excluded_budget"
	ContextRelationExcludedInvalid        ContextRelationAuditReason = "excluded_invalid"
	ContextRelationExcludedScope          ContextRelationAuditReason = "excluded_scope"
)

// RelationAuditReason is a concise alias for ContextRelationAuditReason.
type RelationAuditReason = ContextRelationAuditReason

// ContextRelationAudit is a content-free decision for one relation
// candidate. MissingIDs is populated only when required support could not be
// resolved from valid selection candidates.
type ContextRelationAudit struct {
	RelationID    string                     `json:"relation_id"`
	Included      bool                       `json:"included"`
	Reason        ContextRelationAuditReason `json:"reason"`
	Rank          int                        `json:"rank"`
	Score         float64                    `json:"score"`
	TokenCost     int                        `json:"token_cost"`
	CharacterCost int64                      `json:"character_cost"`
	ByteCost      int64                      `json:"byte_cost"`
	MissingIDs    []string                   `json:"missing_ids,omitempty"`
}

// RelationAudit is a concise alias for ContextRelationAudit.
type RelationAudit = ContextRelationAudit

// ContextSupportClosureResult contains the selection after atomic relation
// closure, the included relations, relation audits and global accounting.
// TokenEstimate, CharactersUsed and BytesUsed include both items and
// relations; ItemCount and RelationCount keep their respective cardinalities.
type ContextSupportClosureResult struct {
	Selection         ContextSelectionResult `json:"selection"`
	Relations         []ContextRelation      `json:"relations,omitempty"`
	RelationAudit     []ContextRelationAudit `json:"relation_audit"`
	ItemCount         int                    `json:"item_count"`
	RelationCount     int                    `json:"relation_count"`
	TotalCount        int                    `json:"total_count"`
	TokenEstimate     int                    `json:"token_estimate"`
	CharactersUsed    int64                  `json:"characters_used"`
	BytesUsed         int64                  `json:"bytes_used"`
	SupportIncomplete bool                   `json:"support_incomplete"`
	BudgetExhausted   bool                   `json:"budget_exhausted"`
}

// ContextClosureResult is a concise alias for ContextSupportClosureResult.
type ContextClosureResult = ContextSupportClosureResult

// ContextRelationClosureResult is a descriptive alias for the closure
// result.
type ContextRelationClosureResult = ContextSupportClosureResult

// Validate checks the structural identity, relation support and global
// accounting of a closure result. Request-dependent checks belong to
// ValidateAgainst.
func (r ContextSupportClosureResult) Validate() error {
	if err := r.Selection.Validate(); err != nil {
		return err
	}
	if len(r.Relations) > maxContextRelations || len(r.RelationAudit) > maxContextRelations {
		return fmt.Errorf("%w: closure result relation bounds", ErrInvalidContextSelection)
	}
	if r.ItemCount != len(r.Selection.Items) || r.RelationCount != len(r.Relations) ||
		r.TotalCount != r.ItemCount+r.RelationCount || r.TokenEstimate < 0 ||
		r.CharactersUsed < 0 || r.BytesUsed < 0 {
		return fmt.Errorf("%w: closure result counts", ErrInvalidContextSelection)
	}

	itemIDs := make(map[string]struct{}, len(r.Selection.Items))
	for _, item := range r.Selection.Items {
		itemIDs[item.ID] = struct{}{}
	}
	seenIDs := make(map[string]struct{}, len(r.Relations))
	var selectionScope Scope
	if len(r.Selection.Items) > 0 {
		selectionScope = r.Selection.Items[0].Scope
	}
	for _, relation := range r.Relations {
		if err := relation.validate(itemIDs); err != nil {
			return err
		}
		if len(r.Selection.Items) > 0 && !sameScope(relation.Scope, selectionScope) {
			return ErrInvalidContextScope
		}
		if _, exists := seenIDs[relation.ID]; exists {
			return fmt.Errorf("%w: duplicate closure relation", ErrInvalidContextSelection)
		}
		if _, exists := itemIDs[relation.ID]; exists {
			return fmt.Errorf("%w: relation collides with selected item", ErrInvalidContextSelection)
		}
		seenIDs[relation.ID] = struct{}{}
	}

	seenAudits := make(map[string]struct{}, len(r.RelationAudit))
	includedRelations := make(map[string]ContextRelationAudit, len(r.RelationAudit))
	var relationTokens int64
	var relationCharacters, relationBytes int64
	supportIncomplete := false
	budgetExhausted := r.Selection.BudgetExhausted
	for _, audit := range r.RelationAudit {
		if err := audit.Validate(); err != nil {
			return err
		}
		if _, exists := seenAudits[audit.RelationID]; exists {
			return fmt.Errorf("%w: duplicate relation audit", ErrInvalidContextSelection)
		}
		seenAudits[audit.RelationID] = struct{}{}
		if audit.Included {
			if _, exists := seenIDs[audit.RelationID]; !exists {
				return fmt.Errorf("%w: included relation has no relation", ErrInvalidContextSelection)
			}
			includedRelations[audit.RelationID] = audit
			relationTokens += int64(audit.TokenCost)
			relationCharacters += audit.CharacterCost
			relationBytes += audit.ByteCost
		} else {
			if _, exists := seenIDs[audit.RelationID]; exists {
				return fmt.Errorf("%w: excluded relation is selected", ErrInvalidContextSelection)
			}
			if audit.Reason == ContextRelationExcludedMissingSupport {
				supportIncomplete = true
			}
			if audit.Reason == ContextRelationExcludedBudget {
				budgetExhausted = true
			}
		}
	}
	if len(includedRelations) != len(r.Relations) {
		return fmt.Errorf("%w: selected relation has no included audit", ErrInvalidContextSelection)
	}
	if r.SupportIncomplete != supportIncomplete || r.BudgetExhausted != budgetExhausted {
		return fmt.Errorf("%w: closure status differs from audits", ErrInvalidContextSelection)
	}
	if r.TokenEstimate != r.Selection.TokenEstimate+int(relationTokens) ||
		r.CharactersUsed != r.Selection.CharactersUsed+relationCharacters ||
		r.BytesUsed != r.Selection.BytesUsed+relationBytes {
		return fmt.Errorf("%w: closure accounting differs", ErrInvalidContextSelection)
	}
	return nil
}

// ValidateAgainst checks the closure result against its request, including
// relation candidate identity, deterministic audit order and all four limits.
func (r ContextSupportClosureResult) ValidateAgainst(request ContextSupportClosureRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := r.Selection.ValidateAgainst(request.Request); err != nil {
		return err
	}
	if r.ItemCount > request.Request.Limits.MaxItems || r.TokenEstimate > request.Request.Limits.MaxTokens ||
		r.CharactersUsed > request.Request.Limits.MaxCharacters || r.BytesUsed > request.Request.Limits.MaxBytes {
		return ErrInvalidContextBudget
	}

	ordered := sortedRelationCandidates(request.Relations)
	if len(r.RelationAudit) != len(ordered) {
		return fmt.Errorf("%w: closure relation audit is incomplete", ErrInvalidContextSelection)
	}
	candidateByID := make(map[string]ContextRelationCandidate, len(ordered))
	for _, candidate := range ordered {
		candidateByID[candidate.Relation.ID] = candidate
	}
	seenAudit := make(map[string]struct{}, len(r.RelationAudit))
	seenRelations := make(map[string]struct{}, len(r.Relations))
	relationOutputIndex := 0
	for index, audit := range r.RelationAudit {
		candidate, exists := candidateByID[audit.RelationID]
		if !exists {
			return fmt.Errorf("%w: relation audit is not a request candidate", ErrInvalidContextSelection)
		}
		if audit.RelationID != ordered[index].Relation.ID {
			return fmt.Errorf("%w: relation audit order is not deterministic", ErrInvalidContextSelection)
		}
		if _, duplicate := seenAudit[audit.RelationID]; duplicate {
			return fmt.Errorf("%w: duplicate relation audit", ErrInvalidContextSelection)
		}
		seenAudit[audit.RelationID] = struct{}{}
		tokens, characters, bytes, err := candidate.costs()
		if err != nil {
			return err
		}
		if audit.Rank != candidate.Rank || audit.Score != candidate.Score || audit.TokenCost != tokens ||
			audit.CharacterCost != characters || audit.ByteCost != bytes {
			return fmt.Errorf("%w: relation audit metadata differs", ErrInvalidContextSelection)
		}
		if audit.Included {
			if _, duplicate := seenRelations[audit.RelationID]; duplicate {
				return fmt.Errorf("%w: duplicate included relation", ErrInvalidContextSelection)
			}
			seenRelations[audit.RelationID] = struct{}{}
			if !containsContextRelation(r.Relations, candidate.Relation.ID) {
				return fmt.Errorf("%w: included relation is missing", ErrInvalidContextSelection)
			}
			if relationOutputIndex >= len(r.Relations) || r.Relations[relationOutputIndex].ID != candidate.Relation.ID {
				return fmt.Errorf("%w: included relation order is not deterministic", ErrInvalidContextSelection)
			}
			relationOutputIndex++
			if !sameContextRelation(findContextRelation(r.Relations, candidate.Relation.ID), candidate.Relation) {
				return fmt.Errorf("%w: included relation differs from candidate", ErrInvalidContextSelection)
			}
			if len(audit.MissingIDs) != 0 {
				return fmt.Errorf("%w: included relation has missing support audit", ErrInvalidContextSelection)
			}
		} else if containsContextRelation(r.Relations, candidate.Relation.ID) {
			return fmt.Errorf("%w: excluded relation is selected", ErrInvalidContextSelection)
		}
	}
	if len(seenRelations) != len(r.Relations) {
		return fmt.Errorf("%w: selected relation is not audited", ErrInvalidContextSelection)
	}
	return nil
}

// Validate checks the request before any closure work. The base selection is
// required to be a valid result for the original request.
func (r ContextSupportClosureRequest) Validate() error {
	if err := r.Request.Validate(); err != nil {
		return err
	}
	if err := r.Base.ValidateAgainst(r.Request); err != nil {
		return err
	}
	if len(r.Relations) > maxContextRelations {
		return fmt.Errorf("%w: too many relation candidates", ErrInvalidContextSelection)
	}
	requestIDs := make(map[string]struct{}, len(r.Request.Candidates))
	for _, candidate := range r.Request.Candidates {
		requestIDs[candidate.Item.ID] = struct{}{}
	}
	seenRelations := make(map[string]struct{}, len(r.Relations))
	for _, candidate := range r.Relations {
		if err := candidate.validateMetadata(); err != nil {
			return err
		}
		if _, exists := seenRelations[candidate.Relation.ID]; exists {
			return fmt.Errorf("%w: duplicate relation candidate id", ErrInvalidContextSelection)
		}
		if _, collides := requestIDs[candidate.Relation.ID]; collides {
			return fmt.Errorf("%w: relation candidate collides with item candidate", ErrInvalidContextSelection)
		}
		seenRelations[candidate.Relation.ID] = struct{}{}
	}
	return nil
}

// Validate checks one relation audit independently of a request.
func (a ContextRelationAudit) Validate() error {
	if !validContextID(a.RelationID) || a.Rank < 0 || a.Rank > maxContextSelectionCandidates ||
		math.IsNaN(a.Score) || math.IsInf(a.Score, 0) || a.Score < 0 || a.Score > maxContextSelectionScore ||
		a.TokenCost < 0 || a.CharacterCost < 0 || a.ByteCost < 0 ||
		a.TokenCost > maxContextTokens || a.CharacterCost > maxContextCharacters || a.ByteCost > maxContextBytes {
		return ErrInvalidContextReference
	}
	if !validateContextIDs(a.MissingIDs, maxContextItems) {
		return ErrInvalidContextReference
	}
	seenMissing := make(map[string]struct{}, len(a.MissingIDs))
	for _, id := range a.MissingIDs {
		if _, exists := seenMissing[id]; exists {
			return ErrInvalidContextReference
		}
		seenMissing[id] = struct{}{}
	}
	if (a.Included && a.Reason != ContextRelationIncluded) ||
		(!a.Included && a.Reason == ContextRelationIncluded) {
		return ErrInvalidContextReference
	}
	switch a.Reason {
	case ContextRelationIncluded:
		if len(a.MissingIDs) != 0 {
			return ErrInvalidContextReference
		}
	case ContextRelationExcludedMissingSupport, ContextRelationExcludedBudget,
		ContextRelationExcludedInvalid, ContextRelationExcludedScope:
	default:
		return ErrInvalidContextReference
	}
	return nil
}

// CloseContextSupport atomically adds the valid support required by each
// relation that fits the remaining budgets. Relations are considered in score
// descending, rank ascending and ID ascending order.
func CloseContextSupport(ctx context.Context, request ContextSupportClosureRequest) (ContextSupportClosureResult, error) {
	if ctx == nil {
		return ContextSupportClosureResult{}, ErrInvalidContextSelection
	}
	if err := ctx.Err(); err != nil {
		return ContextSupportClosureResult{}, err
	}
	if err := request.Validate(); err != nil {
		return ContextSupportClosureResult{}, err
	}

	selection := cloneContextSelectionResult(request.Base)
	selectionAuditByID := make(map[string]*ContextSelectionAudit, len(selection.Audit))
	for index := range selection.Audit {
		selectionAuditByID[selection.Audit[index].ItemID] = &selection.Audit[index]
	}
	selectedIDs := make(map[string]struct{}, len(selection.Items))
	for _, item := range selection.Items {
		selectedIDs[item.ID] = struct{}{}
	}
	candidatesByID := make(map[string]ContextSelectionCandidate, len(request.Request.Candidates))
	for _, candidate := range request.Request.Candidates {
		candidatesByID[candidate.Item.ID] = candidate
	}

	ordered := sortedRelationCandidates(request.Relations)
	relations := make([]ContextRelation, 0, len(ordered))
	relationAudits := make([]ContextRelationAudit, 0, len(ordered))
	for _, candidate := range ordered {
		if err := ctx.Err(); err != nil {
			return ContextSupportClosureResult{}, err
		}
		tokens, characters, bytes, costErr := candidate.costs()
		audit := ContextRelationAudit{
			RelationID:    candidate.Relation.ID,
			Rank:          candidate.Rank,
			Score:         candidate.Score,
			TokenCost:     tokens,
			CharacterCost: characters,
			ByteCost:      bytes,
		}
		if costErr != nil {
			audit.Reason = ContextRelationExcludedInvalid
			relationAudits = append(relationAudits, audit)
			continue
		}
		if err := candidate.Validate(); err != nil {
			if errors.Is(err, ErrInvalidContextScope) {
				audit.Reason = ContextRelationExcludedScope
			} else {
				audit.Reason = ContextRelationExcludedInvalid
			}
			relationAudits = append(relationAudits, audit)
			continue
		}
		if !sameScope(candidate.Relation.Scope, request.Request.Scope) {
			audit.Reason = ContextRelationExcludedScope
			relationAudits = append(relationAudits, audit)
			continue
		}

		requiredIDs := relationRequiredIDs(candidate.Relation)
		missingIDs := make([]string, 0, len(requiredIDs))
		missingItems := make([]ContextSelectionCandidate, 0, len(requiredIDs))
		missingSupport := false
		scopeFailure := false
		for _, id := range requiredIDs {
			if err := ctx.Err(); err != nil {
				return ContextSupportClosureResult{}, err
			}
			if _, selected := selectedIDs[id]; selected {
				continue
			}
			candidateItem, exists := candidatesByID[id]
			if !exists {
				missingSupport = true
				missingIDs = append(missingIDs, id)
				continue
			}
			if candidateItem.Item.Scope.Validate() != nil || !sameScope(candidateItem.Item.Scope, request.Request.Scope) {
				scopeFailure = true
				missingIDs = append(missingIDs, id)
				continue
			}
			if err := candidateItem.Item.Validate(); err != nil {
				missingSupport = true
				missingIDs = append(missingIDs, id)
				continue
			}
			missingItems = append(missingItems, candidateItem)
		}
		if scopeFailure {
			audit.Reason = ContextRelationExcludedScope
			audit.MissingIDs = append([]string(nil), missingIDs...)
			relationAudits = append(relationAudits, audit)
			continue
		}
		if missingSupport {
			audit.Reason = ContextRelationExcludedMissingSupport
			audit.MissingIDs = append([]string(nil), missingIDs...)
			relationAudits = append(relationAudits, audit)
			continue
		}

		missingTokens, missingCharacters, missingBytes := 0, int64(0), int64(0)
		for _, itemCandidate := range missingItems {
			itemTokens, itemCharacters, itemBytes, err := itemCandidate.costs()
			if err != nil {
				audit.Reason = ContextRelationExcludedInvalid
				relationAudits = append(relationAudits, audit)
				missingSupport = true
				break
			}
			if !addContextClosureCost(&missingTokens, &missingCharacters, &missingBytes, itemTokens, itemCharacters, itemBytes) {
				audit.Reason = ContextRelationExcludedBudget
				break
			}
		}
		if missingSupport || audit.Reason == ContextRelationExcludedInvalid {
			continue
		}
		if audit.Reason == ContextRelationExcludedBudget {
			relationAudits = append(relationAudits, audit)
			continue
		}
		if selection.ItemCount+len(missingItems) > request.Request.Limits.MaxItems ||
			!fitsClosureBudget(selection.TokenEstimate, selection.CharactersUsed, selection.BytesUsed,
				missingTokens+tokens, missingCharacters+characters, missingBytes+bytes, request.Request.Limits) {
			audit.Reason = ContextRelationExcludedBudget
			relationAudits = append(relationAudits, audit)
			continue
		}

		for _, itemCandidate := range missingItems {
			selection.Items = append(selection.Items, cloneContextItem(itemCandidate.Item))
			selectedIDs[itemCandidate.Item.ID] = struct{}{}
			auditForItem := selectionAuditByID[itemCandidate.Item.ID]
			if auditForItem == nil {
				return ContextSupportClosureResult{}, fmt.Errorf("%w: support candidate has no selection audit", ErrInvalidContextSelection)
			}
			itemTokens, itemCharacters, itemBytes, _ := itemCandidate.costs()
			auditForItem.Included = true
			auditForItem.Reason = ContextSelectionIncluded
			auditForItem.TokenEstimate = itemTokens
			auditForItem.Characters = itemCharacters
			auditForItem.Bytes = itemBytes
			selection.TokenEstimate += itemTokens
			selection.CharactersUsed += itemCharacters
			selection.BytesUsed += itemBytes
		}
		selection.ItemCount = len(selection.Items)
		audit.Included = true
		audit.Reason = ContextRelationIncluded
		relations = append(relations, cloneContextRelation(candidate.Relation))
		relationAudits = append(relationAudits, audit)
	}

	selection.BudgetExhausted = selectionBudgetExhausted(selection.Audit)
	if err := selection.ValidateAgainst(request.Request); err != nil {
		return ContextSupportClosureResult{}, err
	}
	result := ContextSupportClosureResult{
		Selection:      selection,
		Relations:      relations,
		RelationAudit:  relationAudits,
		ItemCount:      len(selection.Items),
		RelationCount:  len(relations),
		TotalCount:     len(selection.Items) + len(relations),
		TokenEstimate:  selection.TokenEstimate,
		CharactersUsed: selection.CharactersUsed,
		BytesUsed:      selection.BytesUsed,
	}
	for _, audit := range relationAudits {
		if audit.Included {
			result.TokenEstimate += audit.TokenCost
			result.CharactersUsed += audit.CharacterCost
			result.BytesUsed += audit.ByteCost
		}
		if audit.Reason == ContextRelationExcludedMissingSupport {
			result.SupportIncomplete = true
		}
		if audit.Reason == ContextRelationExcludedBudget {
			result.BudgetExhausted = true
		}
	}
	result.BudgetExhausted = result.BudgetExhausted || selection.BudgetExhausted
	if err := result.ValidateAgainst(request); err != nil {
		return ContextSupportClosureResult{}, err
	}
	return result, nil
}

// ApplyContextSupportClosure is a descriptive alias for CloseContextSupport.
func ApplyContextSupportClosure(ctx context.Context, request ContextSupportClosureRequest) (ContextSupportClosureResult, error) {
	return CloseContextSupport(ctx, request)
}

// CloseContextRelations is a descriptive alias for CloseContextSupport.
func CloseContextRelations(ctx context.Context, request ContextSupportClosureRequest) (ContextSupportClosureResult, error) {
	return CloseContextSupport(ctx, request)
}

func (c ContextRelationCandidate) Validate() error {
	if err := c.validateMetadata(); err != nil {
		return err
	}
	if err := c.Relation.Validate(); err != nil {
		return err
	}
	return nil
}

func (c ContextRelationCandidate) validateMetadata() error {
	if !validContextID(c.Relation.ID) {
		return ErrInvalidContextReference
	}
	if math.IsNaN(c.Score) || math.IsInf(c.Score, 0) || c.Score < 0 || c.Score > maxContextSelectionScore {
		return fmt.Errorf("%w: relation candidate score", ErrInvalidContextSelection)
	}
	if c.Rank < 0 || c.Rank > maxContextSelectionCandidates {
		return fmt.Errorf("%w: relation candidate rank", ErrInvalidContextSelection)
	}
	tokens, characters, bytes, err := c.costs()
	if err != nil {
		return err
	}
	if tokens > maxContextTokens || characters > maxContextCharacters || bytes > maxContextBytes {
		return fmt.Errorf("%w: relation candidate costs exceed bounds", ErrInvalidContextSelection)
	}
	return nil
}

func (c ContextRelationCandidate) costs() (int, int64, int64, error) {
	tokens, err := resolveSelectionCost(c.TokenCost, c.Tokens, "relation token")
	if err != nil {
		return 0, 0, 0, err
	}
	characters, err := resolveSelectionInt64Cost(c.CharacterCost, c.Characters, "relation character")
	if err != nil {
		return 0, 0, 0, err
	}
	bytes, err := resolveSelectionInt64Cost(c.ByteCost, c.Bytes, "relation byte")
	if err != nil {
		return 0, 0, 0, err
	}
	return tokens, characters, bytes, nil
}

func sortedRelationCandidates(candidates []ContextRelationCandidate) []ContextRelationCandidate {
	ordered := append([]ContextRelationCandidate(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		if ordered[i].Rank != ordered[j].Rank {
			return ordered[i].Rank < ordered[j].Rank
		}
		return ordered[i].Relation.ID < ordered[j].Relation.ID
	})
	return ordered
}

func relationRequiredIDs(relation ContextRelation) []string {
	required := make([]string, 0, 2+len(relation.Path)+len(relation.SupportIDs))
	seen := make(map[string]struct{}, cap(required))
	appendRequired := func(id string) {
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		required = append(required, id)
	}
	appendRequired(relation.FromID)
	appendRequired(relation.ToID)
	for _, id := range relation.Path {
		appendRequired(id)
	}
	for _, id := range relation.SupportIDs {
		appendRequired(id)
	}
	return required
}

func addContextClosureCost(tokens *int, characters, bytes *int64, addTokens int, addCharacters, addBytes int64) bool {
	if addTokens < 0 || addCharacters < 0 || addBytes < 0 || *tokens > maxContextTokens-addTokens ||
		*characters > maxContextCharacters-addCharacters || *bytes > maxContextBytes-addBytes {
		return false
	}
	*tokens += addTokens
	*characters += addCharacters
	*bytes += addBytes
	return true
}

func fitsClosureBudget(currentTokens int, currentCharacters, currentBytes int64, addTokens int, addCharacters, addBytes int64, limits ContextLimits) bool {
	if currentTokens > limits.MaxTokens || currentCharacters > limits.MaxCharacters || currentBytes > limits.MaxBytes ||
		addTokens < 0 || addCharacters < 0 || addBytes < 0 {
		return false
	}
	return addTokens <= limits.MaxTokens-currentTokens && addCharacters <= limits.MaxCharacters-currentCharacters &&
		addBytes <= limits.MaxBytes-currentBytes
}

func selectionBudgetExhausted(audits []ContextSelectionAudit) bool {
	for _, audit := range audits {
		if !audit.Included && audit.Reason == ContextSelectionExcludedBudget {
			return true
		}
	}
	return false
}

func containsContextRelation(relations []ContextRelation, id string) bool {
	for _, relation := range relations {
		if relation.ID == id {
			return true
		}
	}
	return false
}

func findContextRelation(relations []ContextRelation, id string) ContextRelation {
	for _, relation := range relations {
		if relation.ID == id {
			return relation
		}
	}
	return ContextRelation{}
}

func sameContextRelation(left, right ContextRelation) bool {
	return reflect.DeepEqual(left, right)
}

func cloneContextSelectionResult(result ContextSelectionResult) ContextSelectionResult {
	clone := result
	clone.Items = make([]ContextItem, len(result.Items))
	for index, item := range result.Items {
		clone.Items[index] = cloneContextItem(item)
	}
	clone.Audit = append([]ContextSelectionAudit(nil), result.Audit...)
	return clone
}
