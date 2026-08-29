package query

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

var (
	// ErrInvalidContextPolicy is returned for malformed policy application
	// boundaries. It deliberately carries no request or source detail.
	ErrInvalidContextPolicy = errors.New("query: invalid context policy")
)

// ContextPolicyMode selects the trust-boundary representation produced by a
// policy application. The zero value remains the legacy external mode.
type ContextPolicyMode string

const (
	// ContextPolicyModeExternal prepares items for an external provider.
	ContextPolicyModeExternal ContextPolicyMode = "external"
	// ContextPolicyModeLocal prepares items for the local context package.
	ContextPolicyModeLocal ContextPolicyMode = "local"

	// ContextPolicyExternal and ContextPolicyLocal are concise aliases for
	// callers that use policy as the mode vocabulary.
	ContextPolicyExternal = ContextPolicyModeExternal
	ContextPolicyLocal    = ContextPolicyModeLocal
)

func normalizeContextPolicyMode(mode ContextPolicyMode) (ContextPolicyMode, error) {
	switch mode {
	case "":
		return ContextPolicyModeExternal, nil
	case ContextPolicyModeExternal, ContextPolicyModeLocal:
		return mode, nil
	default:
		return "", ErrInvalidContextPolicy
	}
}

// ContextItemAuthorization is the explicit decision for one input item.
// Every item in a ContextPolicyRequest must have exactly one authorization.
type ContextItemAuthorization struct {
	ItemID   string            `json:"item_id"`
	Decision evidence.Decision `json:"decision"`
}

// Validate checks the bounded identity and closed decision vocabulary.
func (a ContextItemAuthorization) Validate() error {
	if !validContextID(a.ItemID) {
		return ErrInvalidContextReference
	}
	if a.Decision.Validate() != nil {
		return ErrInvalidContextPolicy
	}
	return nil
}

// ContextPolicyItemAuditReason is a closed, content-free item outcome.
type ContextPolicyItemAuditReason string

const (
	ContextPolicyItemIncluded                    ContextPolicyItemAuditReason = "included"
	ContextPolicyItemExcludedAuthorizationDeny   ContextPolicyItemAuditReason = "excluded_authorization_deny"
	ContextPolicyItemExcludedAuthorizationRedact ContextPolicyItemAuditReason = "excluded_authorization_redact"
	ContextPolicyItemExcludedTransferPolicy      ContextPolicyItemAuditReason = "excluded_transfer_policy"
	ContextPolicyItemExcludedPersistence         ContextPolicyItemAuditReason = "excluded_persistence_policy"
	ContextPolicyItemExcludedInspection          ContextPolicyItemAuditReason = "excluded_inspection"
	ContextPolicyItemExcludedSupport             ContextPolicyItemAuditReason = "excluded_support"
	ContextPolicyItemExcludedInvalid             ContextPolicyItemAuditReason = "excluded_invalid"
	ContextPolicyItemExcludedScope               ContextPolicyItemAuditReason = "excluded_scope"
)

// ContextPolicyItemAudit records one policy outcome without payload, locator,
// content hash or free-form reason. OutputID is present only for inclusion.
type ContextPolicyItemAudit struct {
	ItemID   string                       `json:"item_id"`
	OutputID string                       `json:"output_id,omitempty"`
	Included bool                         `json:"included"`
	Redacted bool                         `json:"redacted,omitempty"`
	Reason   ContextPolicyItemAuditReason `json:"reason"`
}

// Validate checks one content-free item audit.
func (a ContextPolicyItemAudit) Validate() error {
	if !validContextID(a.ItemID) {
		return ErrInvalidContextReference
	}
	if a.OutputID != "" && !validContextID(a.OutputID) {
		return ErrInvalidContextReference
	}
	if a.Included {
		if a.Reason != ContextPolicyItemIncluded || !validContextID(a.OutputID) {
			return ErrInvalidContextPolicy
		}
	} else if a.OutputID != "" || a.Redacted || a.Reason == ContextPolicyItemIncluded {
		return ErrInvalidContextPolicy
	}
	switch a.Reason {
	case ContextPolicyItemIncluded,
		ContextPolicyItemExcludedAuthorizationDeny,
		ContextPolicyItemExcludedAuthorizationRedact,
		ContextPolicyItemExcludedTransferPolicy,
		ContextPolicyItemExcludedPersistence,
		ContextPolicyItemExcludedInspection,
		ContextPolicyItemExcludedSupport,
		ContextPolicyItemExcludedInvalid,
		ContextPolicyItemExcludedScope:
		return nil
	default:
		return ErrInvalidContextPolicy
	}
}

// ContextPolicyRelationAuditReason is a closed, content-free relation
// outcome. Relation removal is atomic when any required endpoint, path node
// or support item was filtered.
type ContextPolicyRelationAuditReason string

const (
	ContextPolicyRelationIncluded         ContextPolicyRelationAuditReason = "included"
	ContextPolicyRelationExcludedEndpoint ContextPolicyRelationAuditReason = "excluded_endpoint"
	ContextPolicyRelationExcludedPath     ContextPolicyRelationAuditReason = "excluded_path"
	ContextPolicyRelationExcludedSupport  ContextPolicyRelationAuditReason = "excluded_support"
	ContextPolicyRelationExcludedInvalid  ContextPolicyRelationAuditReason = "excluded_invalid"
	ContextPolicyRelationExcludedScope    ContextPolicyRelationAuditReason = "excluded_scope"
)

// ContextPolicyRelationAudit records one relation outcome without source
// material or an unrestricted diagnostic.
type ContextPolicyRelationAudit struct {
	RelationID string                           `json:"relation_id"`
	Included   bool                             `json:"included"`
	Reason     ContextPolicyRelationAuditReason `json:"reason"`
}

// Validate checks one content-free relation audit.
func (a ContextPolicyRelationAudit) Validate() error {
	if !validContextID(a.RelationID) {
		return ErrInvalidContextReference
	}
	if (a.Included && a.Reason != ContextPolicyRelationIncluded) ||
		(!a.Included && a.Reason == ContextPolicyRelationIncluded) {
		return ErrInvalidContextPolicy
	}
	switch a.Reason {
	case ContextPolicyRelationIncluded,
		ContextPolicyRelationExcludedEndpoint,
		ContextPolicyRelationExcludedPath,
		ContextPolicyRelationExcludedSupport,
		ContextPolicyRelationExcludedInvalid,
		ContextPolicyRelationExcludedScope:
		return nil
	default:
		return ErrInvalidContextPolicy
	}
}

// ContextPolicyRequest is the closed input boundary for applying transfer
// policy to already selected context items and relations.
type ContextPolicyRequest struct {
	Scope           Scope                      `json:"scope"`
	Items           []ContextItem              `json:"items"`
	Relations       []ContextRelation          `json:"relations,omitempty"`
	Authorizations  []ContextItemAuthorization `json:"authorizations"`
	Mode            ContextPolicyMode          `json:"mode,omitempty"`
	TransferPolicy  *evidence.Policy           `json:"-"`
	PolicyDigest    string                     `json:"policy_digest"`
	ContinuationIDs []string                   `json:"continuation_ids,omitempty"`
}

// ContextLocalPolicyRequest is the explicit local-policy spelling. It keeps
// the shared request shape while making the trust-boundary purpose visible at
// call sites.
type ContextLocalPolicyRequest = ContextPolicyRequest

// ContextPolicyDigest computes the required SHA-256 identity of a transfer
// policy and its explicit authorizations. A nil policy is represented by the
// canonical JSON value null. Authorizations are sorted in a copy before the
// digest is computed.
func ContextPolicyDigest(policy *evidence.Policy, authorizations []ContextItemAuthorization) (string, error) {
	if policy != nil && policy.Validate() != nil {
		return "", ErrInvalidContextPolicy
	}
	authorizations = append([]ContextItemAuthorization(nil), authorizations...)
	seenAuthorizations := make(map[string]struct{}, len(authorizations))
	for _, authorization := range authorizations {
		if authorization.Validate() != nil {
			return "", ErrInvalidContextPolicy
		}
		if _, duplicate := seenAuthorizations[authorization.ItemID]; duplicate {
			return "", ErrInvalidContextPolicy
		}
		seenAuthorizations[authorization.ItemID] = struct{}{}
	}
	sort.SliceStable(authorizations, func(left, right int) bool {
		if authorizations[left].ItemID != authorizations[right].ItemID {
			return authorizations[left].ItemID < authorizations[right].ItemID
		}
		return authorizations[left].Decision < authorizations[right].Decision
	})
	encoded, err := json.Marshal(struct {
		Policy         *evidence.Policy           `json:"policy"`
		Authorizations []ContextItemAuthorization `json:"authorizations"`
	}{Policy: policy, Authorizations: authorizations})
	if err != nil {
		return "", ErrInvalidContextPolicy
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// ContextPolicyDigestForMode computes a mode-bound policy identity. External
// mode delegates to ContextPolicyDigest to preserve its established digest;
// local mode uses domain separation so local and external continuations and
// packages cannot collide.
func ContextPolicyDigestForMode(mode ContextPolicyMode, policy *evidence.Policy, authorizations []ContextItemAuthorization) (string, error) {
	normalized, err := normalizeContextPolicyMode(mode)
	if err != nil {
		return "", err
	}
	if normalized == ContextPolicyModeExternal {
		return ContextPolicyDigest(policy, authorizations)
	}
	if policy != nil && policy.Validate() != nil {
		return "", ErrInvalidContextPolicy
	}
	authorizations = append([]ContextItemAuthorization(nil), authorizations...)
	seenAuthorizations := make(map[string]struct{}, len(authorizations))
	for _, authorization := range authorizations {
		if authorization.Validate() != nil {
			return "", ErrInvalidContextPolicy
		}
		if _, duplicate := seenAuthorizations[authorization.ItemID]; duplicate {
			return "", ErrInvalidContextPolicy
		}
		seenAuthorizations[authorization.ItemID] = struct{}{}
	}
	sort.SliceStable(authorizations, func(left, right int) bool {
		if authorizations[left].ItemID != authorizations[right].ItemID {
			return authorizations[left].ItemID < authorizations[right].ItemID
		}
		return authorizations[left].Decision < authorizations[right].Decision
	})

	var localPolicy *contextLocalPolicyDigestMaterial
	if policy != nil {
		localPolicy = &contextLocalPolicyDigestMaterial{
			InstallationPersist: policy.Installation.Persist,
			SourcePersist:       policy.Source.Persist,
		}
		if policy.Classifications != nil {
			localPolicy.Classifications = make(map[evidence.Classification]evidence.Decision, len(policy.Classifications))
			for classification, layer := range policy.Classifications {
				localPolicy.Classifications[classification] = layer.Persist
			}
		}
	}
	encoded, err := json.Marshal(struct {
		Mode           ContextPolicyMode                 `json:"mode"`
		Policy         *contextLocalPolicyDigestMaterial `json:"policy"`
		Authorizations []ContextItemAuthorization        `json:"authorizations"`
	}{
		Mode:           ContextPolicyModeLocal,
		Policy:         localPolicy,
		Authorizations: authorizations,
	})
	if err != nil {
		return "", ErrInvalidContextPolicy
	}
	encoded = append([]byte("context-policy-local-v1\x00"), encoded...)
	localDigest := sha256.Sum256(encoded)
	return hex.EncodeToString(localDigest[:]), nil
}

type contextLocalPolicyDigestMaterial struct {
	InstallationPersist evidence.Decision                             `json:"installation_persist"`
	SourcePersist       evidence.Decision                             `json:"source_persist"`
	Classifications     map[evidence.Classification]evidence.Decision `json:"classifications,omitempty"`
}

// ContextLocalPolicyDigest computes the mode-bound identity for a local
// context package.
func ContextLocalPolicyDigest(policy *evidence.Policy, authorizations []ContextItemAuthorization) (string, error) {
	return ContextPolicyDigestForMode(ContextPolicyModeLocal, policy, authorizations)
}

// Validate checks scope, collection bounds, explicit one-to-one
// authorizations, policy identity and continuation identities.
func (r ContextPolicyRequest) Validate() error {
	mode, err := normalizeContextPolicyMode(r.Mode)
	if err != nil {
		return err
	}
	if r.Scope.Validate() != nil {
		return ErrInvalidContextScope
	}
	if len(r.Items) > maxContextItems || len(r.Relations) > maxContextRelations ||
		len(r.Authorizations) > maxContextItems || len(r.ContinuationIDs) > maxContextItems {
		return ErrInvalidContextBudget
	}

	itemIDs := make(map[string]struct{}, len(r.Items))
	for _, item := range r.Items {
		if err := item.Validate(); err != nil {
			if errors.Is(err, ErrInvalidContextScope) {
				return ErrInvalidContextScope
			}
			return ErrInvalidContextReference
		}
		if !sameScope(item.Scope, r.Scope) {
			return ErrInvalidContextScope
		}
		if _, exists := itemIDs[item.ID]; exists {
			return ErrInvalidContextReference
		}
		itemIDs[item.ID] = struct{}{}
	}
	for _, item := range r.Items {
		for _, supportID := range item.SupportIDs {
			if supportID == item.ID {
				return ErrInvalidContextPolicy
			}
			if _, exists := itemIDs[supportID]; !exists {
				return ErrInvalidContextPolicy
			}
		}
	}

	seenRelations := make(map[string]struct{}, len(r.Relations))
	for _, relation := range r.Relations {
		if err := relation.validate(itemIDs); err != nil {
			if errors.Is(err, ErrInvalidContextScope) {
				return ErrInvalidContextScope
			}
			return ErrInvalidContextReference
		}
		if !sameScope(relation.Scope, r.Scope) {
			return ErrInvalidContextScope
		}
		if _, exists := seenRelations[relation.ID]; exists {
			return ErrInvalidContextReference
		}
		if _, collides := itemIDs[relation.ID]; collides {
			return ErrInvalidContextReference
		}
		seenRelations[relation.ID] = struct{}{}
	}

	if len(r.Authorizations) != len(r.Items) {
		return ErrInvalidContextPolicy
	}
	seenAuthorizations := make(map[string]struct{}, len(r.Authorizations))
	for _, authorization := range r.Authorizations {
		if authorization.Validate() != nil {
			return ErrInvalidContextPolicy
		}
		if _, exists := itemIDs[authorization.ItemID]; !exists {
			return ErrInvalidContextPolicy
		}
		if _, duplicate := seenAuthorizations[authorization.ItemID]; duplicate {
			return ErrInvalidContextPolicy
		}
		seenAuthorizations[authorization.ItemID] = struct{}{}
	}
	if len(seenAuthorizations) != len(itemIDs) {
		return ErrInvalidContextPolicy
	}

	if !isSHA256(r.PolicyDigest) {
		return ErrInvalidContextPolicy
	}
	policyDigest, err := ContextPolicyDigestForMode(mode, r.TransferPolicy, r.Authorizations)
	if err != nil || policyDigest != r.PolicyDigest {
		return ErrInvalidContextPolicy
	}

	seenContinuation := make(map[string]struct{}, len(r.ContinuationIDs))
	for _, id := range r.ContinuationIDs {
		if !validContextID(id) {
			return ErrInvalidContextContinuation
		}
		if _, exists := itemIDs[id]; !exists {
			return ErrInvalidContextContinuation
		}
		if _, duplicate := seenContinuation[id]; duplicate {
			return ErrInvalidContextContinuation
		}
		seenContinuation[id] = struct{}{}
	}
	return nil
}

// ContextPolicyResult is the post-policy context projection. ContinuationIDs
// contains only output item identities and is the sole sequence accepted by a
// continuation codec.
type ContextPolicyResult struct {
	Scope           Scope                        `json:"scope"`
	Items           []ContextItem                `json:"items"`
	Relations       []ContextRelation            `json:"relations,omitempty"`
	ItemAudit       []ContextPolicyItemAudit     `json:"item_audit"`
	RelationAudit   []ContextPolicyRelationAudit `json:"relation_audit"`
	ContinuationIDs []string                     `json:"continuation_ids,omitempty"`
	Mode            ContextPolicyMode            `json:"mode,omitempty"`
	PolicyDigest    string                       `json:"policy_digest"`
	PolicyFiltered  bool                         `json:"policy_filtered"`
	Degradations    []ContextDegradation         `json:"degradations,omitempty"`
}

// Validate checks result scope, identity uniqueness, relation closure,
// content-free audits, post-policy continuation IDs and degradation state.
func (r ContextPolicyResult) Validate() error {
	mode, err := normalizeContextPolicyMode(r.Mode)
	if err != nil {
		return err
	}
	if r.Scope.Validate() != nil {
		return ErrInvalidContextScope
	}
	if len(r.Items) > maxContextItems || len(r.Relations) > maxContextRelations ||
		len(r.ItemAudit) > maxContextAudits || len(r.RelationAudit) > maxContextRelations ||
		len(r.ContinuationIDs) > maxContextItems || len(r.Degradations) > maxContextDegradations {
		return ErrInvalidContextBudget
	}
	if !isSHA256(r.PolicyDigest) {
		return ErrInvalidContextPolicy
	}

	itemIDs := make(map[string]struct{}, len(r.Items))
	for _, item := range r.Items {
		if err := item.Validate(); err != nil {
			if errors.Is(err, ErrInvalidContextScope) {
				return ErrInvalidContextScope
			}
			return ErrInvalidContextReference
		}
		if item.Kind == ContextItemEvidence {
			if item.Evidence == nil || item.Evidence.ValidatePrepared() != nil || !contextPolicyValidPreparedRepresentationForMode(*item.Evidence, mode) {
				return ErrInvalidContextPolicy
			}
		} else if !contextPolicySafeItemRepresentation(item) {
			return ErrInvalidContextPolicy
		}
		if !sameScope(item.Scope, r.Scope) {
			return ErrInvalidContextScope
		}
		if _, exists := itemIDs[item.ID]; exists {
			return ErrInvalidContextReference
		}
		itemIDs[item.ID] = struct{}{}
	}
	for _, item := range r.Items {
		for _, supportID := range item.SupportIDs {
			if supportID == item.ID {
				return ErrInvalidContextPolicy
			}
			if _, exists := itemIDs[supportID]; !exists {
				return ErrInvalidContextPolicy
			}
		}
	}

	seenIDs := make(map[string]struct{}, len(r.Items)+len(r.Relations))
	for id := range itemIDs {
		seenIDs[id] = struct{}{}
	}
	for _, relation := range r.Relations {
		if err := relation.validate(itemIDs); err != nil {
			if errors.Is(err, ErrInvalidContextScope) {
				return ErrInvalidContextScope
			}
			return ErrInvalidContextReference
		}
		if !sameScope(relation.Scope, r.Scope) {
			return ErrInvalidContextScope
		}
		if _, exists := seenIDs[relation.ID]; exists {
			return ErrInvalidContextReference
		}
		seenIDs[relation.ID] = struct{}{}
	}

	seenItemAudits := make(map[string]struct{}, len(r.ItemAudit))
	includedItemIDs := make(map[string]struct{}, len(r.ItemAudit))
	for _, audit := range r.ItemAudit {
		if audit.Validate() != nil {
			return ErrInvalidContextPolicy
		}
		if _, duplicate := seenItemAudits[audit.ItemID]; duplicate {
			return ErrInvalidContextPolicy
		}
		seenItemAudits[audit.ItemID] = struct{}{}
		if audit.Included {
			item, exists := contextPolicyFindItem(r.Items, audit.OutputID)
			if !exists {
				return ErrInvalidContextPolicy
			}
			if audit.Redacted != (item.Evidence != nil && item.Evidence.ContentState == evidence.ContentStateRedacted) {
				return ErrInvalidContextPolicy
			}
			includedItemIDs[audit.OutputID] = struct{}{}
		}
	}
	for id := range itemIDs {
		if _, audited := includedItemIDs[id]; !audited {
			return ErrInvalidContextPolicy
		}
	}

	seenRelationAudits := make(map[string]struct{}, len(r.RelationAudit))
	includedRelationIDs := make(map[string]struct{}, len(r.RelationAudit))
	for _, audit := range r.RelationAudit {
		if audit.Validate() != nil {
			return ErrInvalidContextPolicy
		}
		if _, duplicate := seenRelationAudits[audit.RelationID]; duplicate {
			return ErrInvalidContextPolicy
		}
		seenRelationAudits[audit.RelationID] = struct{}{}
		if audit.Included {
			if _, exists := seenIDs[audit.RelationID]; !exists {
				return ErrInvalidContextPolicy
			}
			includedRelationIDs[audit.RelationID] = struct{}{}
		}
	}
	for _, relation := range r.Relations {
		if _, audited := includedRelationIDs[relation.ID]; !audited {
			return ErrInvalidContextPolicy
		}
	}

	seenContinuation := make(map[string]struct{}, len(r.ContinuationIDs))
	for _, id := range r.ContinuationIDs {
		if !validContextID(id) {
			return ErrInvalidContextContinuation
		}
		if _, exists := itemIDs[id]; !exists {
			return ErrInvalidContextContinuation
		}
		if _, duplicate := seenContinuation[id]; duplicate {
			return ErrInvalidContextContinuation
		}
		seenContinuation[id] = struct{}{}
	}

	policyFiltered := false
	for _, degradation := range r.Degradations {
		if degradation.Validate() != nil {
			return ErrInvalidContextPolicy
		}
		if degradation.Code == ContextDegradationPolicyFiltered {
			policyFiltered = true
		}
	}
	if r.PolicyFiltered != policyFiltered {
		return ErrInvalidContextPolicy
	}
	return nil
}

// ValidateAgainst checks that a result is bound to the request's scope and
// policy digest. Full semantic recomputation is performed by ApplyContextPolicy.
func (r ContextPolicyResult) ValidateAgainst(request ContextPolicyRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	requestMode, err := normalizeContextPolicyMode(request.Mode)
	if err != nil {
		return err
	}
	resultMode, err := normalizeContextPolicyMode(r.Mode)
	if err != nil || requestMode != resultMode || !sameScope(r.Scope, request.Scope) || r.PolicyDigest != request.PolicyDigest {
		return ErrInvalidContextPolicy
	}
	var expected ContextPolicyResult
	if requestMode == ContextPolicyModeLocal {
		expected, err = ApplyLocalContextPolicy(context.Background(), request)
	} else {
		expected, err = ApplyContextPolicy(context.Background(), request)
	}
	if err != nil || !reflect.DeepEqual(r, expected) {
		return ErrInvalidContextPolicy
	}
	return nil
}

// ApplyContextPolicy applies the explicit item authorizations and transfer
// policy without retaining or exposing denied representations. All returned
// collections preserve their input order; maps are used only for bounded
// identity lookups and remapping.
func ApplyContextPolicy(ctx context.Context, request ContextPolicyRequest) (ContextPolicyResult, error) {
	if request.Mode == ContextPolicyModeLocal {
		return ContextPolicyResult{}, ErrInvalidContextPolicy
	}
	return applyContextPolicy(ctx, request, ContextPolicyModeExternal)
}

// ApplyLocalContextPolicy applies local persistence policy to an authorized
// candidate set. External transfer remains an independent decision recorded
// on each evidence unit; it is never used to remove safe local content.
func ApplyLocalContextPolicy(ctx context.Context, request ContextLocalPolicyRequest) (ContextPolicyResult, error) {
	if request.Mode == ContextPolicyModeExternal {
		return ContextPolicyResult{}, ErrInvalidContextPolicy
	}
	request.Mode = ContextPolicyModeLocal
	return applyContextPolicy(ctx, request, ContextPolicyModeLocal)
}

// ApplyContextLocalPolicy is a descriptive alias for ApplyLocalContextPolicy.
func ApplyContextLocalPolicy(ctx context.Context, request ContextLocalPolicyRequest) (ContextPolicyResult, error) {
	return ApplyLocalContextPolicy(ctx, request)
}

func applyContextPolicy(ctx context.Context, request ContextPolicyRequest, mode ContextPolicyMode) (ContextPolicyResult, error) {
	if ctx == nil {
		return ContextPolicyResult{}, ErrInvalidContextPolicy
	}
	if err := ctx.Err(); err != nil {
		return ContextPolicyResult{}, err
	}
	requestedMode := request.Mode
	if requestedMode != "" && requestedMode != mode {
		return ContextPolicyResult{}, ErrInvalidContextPolicy
	}
	request.Mode = mode
	if err := request.Validate(); err != nil {
		return ContextPolicyResult{}, err
	}

	authorizations := make(map[string]evidence.Decision, len(request.Authorizations))
	for _, authorization := range request.Authorizations {
		authorizations[authorization.ItemID] = authorization.Decision
	}
	originalIDs := make(map[string]struct{}, len(request.Items))
	for _, item := range request.Items {
		originalIDs[item.ID] = struct{}{}
	}

	result := ContextPolicyResult{
		Scope:         request.Scope,
		Mode:          requestedMode,
		PolicyDigest:  request.PolicyDigest,
		Items:         make([]ContextItem, 0, len(request.Items)),
		ItemAudit:     make([]ContextPolicyItemAudit, 0, len(request.Items)),
		RelationAudit: make([]ContextPolicyRelationAudit, 0, len(request.Relations)),
	}
	if request.ContinuationIDs != nil {
		result.ContinuationIDs = make([]string, 0, len(request.ContinuationIDs))
	}

	idRemap := make(map[string]string, len(request.Items))
	includedIDs := make(map[string]struct{}, len(request.Items))
	policyFiltered := false
	localStructuredDecision := evidence.DecisionAllow
	if mode == ContextPolicyModeLocal {
		var localPolicyErr error
		localStructuredDecision, localPolicyErr = contextPolicyLocalStructuredDecision(request.TransferPolicy)
		if localPolicyErr != nil {
			return ContextPolicyResult{}, localPolicyErr
		}
	}
	for _, input := range request.Items {
		if err := contextPolicyContextErr(ctx); err != nil {
			return ContextPolicyResult{}, err
		}
		authorization := authorizations[input.ID]
		if authorization == evidence.DecisionDeny {
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, Included: false,
				Reason: ContextPolicyItemExcludedAuthorizationDeny,
			})
			policyFiltered = true
			continue
		}

		if input.Kind == ContextItemFact || input.Kind == ContextItemEntity {
			if authorization == evidence.DecisionRedact {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedAuthorizationRedact,
				})
				policyFiltered = true
				continue
			}
			if mode == ContextPolicyModeLocal {
				if localStructuredDecision != evidence.DecisionAllow {
					result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
						ItemID: input.ID, Included: false,
						Reason: ContextPolicyItemExcludedPersistence,
					})
					policyFiltered = true
					continue
				}
				output := cloneContextItem(input)
				if !contextPolicySafeItemRepresentation(output) {
					result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
						ItemID: input.ID, Included: false,
						Reason: ContextPolicyItemExcludedInspection,
					})
					policyFiltered = true
					continue
				}
				if _, collision := includedIDs[output.ID]; collision {
					return ContextPolicyResult{}, ErrInvalidContextPolicy
				}
				result.Items = append(result.Items, output)
				idRemap[input.ID] = output.ID
				includedIDs[output.ID] = struct{}{}
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, OutputID: output.ID, Included: true,
					Reason: ContextPolicyItemIncluded, Redacted: false,
				})
				continue
			}
			if !contextPolicySafeItemRepresentation(input) {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedInspection,
				})
				policyFiltered = true
				continue
			}
			finalDecision, decisionErr := contextPolicyFinalDecision(request.TransferPolicy, authorization, evidence.ClassificationSafeText)
			if decisionErr != nil {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedInvalid,
				})
				policyFiltered = true
				continue
			}
			if finalDecision != evidence.DecisionAllow {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedTransferPolicy,
				})
				policyFiltered = true
				continue
			}
			output := cloneContextItem(input)
			if !contextPolicySafeItemRepresentation(output) {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedInspection,
				})
				policyFiltered = true
				continue
			}
			if _, collision := includedIDs[output.ID]; collision {
				return ContextPolicyResult{}, ErrInvalidContextPolicy
			}
			result.Items = append(result.Items, output)
			idRemap[input.ID] = output.ID
			includedIDs[output.ID] = struct{}{}
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, OutputID: output.ID, Included: true,
				Reason: ContextPolicyItemIncluded, Redacted: false,
			})
			continue
		}

		if input.Evidence == nil || input.Evidence.ValidatePrepared() != nil {
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, Included: false,
				Reason: ContextPolicyItemExcludedInvalid,
			})
			policyFiltered = true
			continue
		}
		if authorization != evidence.DecisionAllow && authorization != evidence.DecisionRedact {
			return ContextPolicyResult{}, ErrInvalidContextPolicy
		}
		if mode == ContextPolicyModeLocal {
			persistFloor, floorErr := contextPolicyCombinedFloor(input.Evidence.Persist, authorization)
			if floorErr != nil {
				return ContextPolicyResult{}, ErrInvalidContextPolicy
			}
			policy := contextPolicyForPersistence(request.TransferPolicy, persistFloor, input.Evidence.ExternalTransfer)
			prepared, prepareErr := evidence.PrepareForPersistence(*input.Evidence, policy)
			if prepareErr != nil || prepared.ValidatePrepared() != nil {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedInvalid,
				})
				policyFiltered = true
				continue
			}
			transferFloor, transferErr := contextPolicyCombinedFloor(input.Evidence.ExternalTransfer, prepared.ExternalTransfer)
			if transferErr != nil {
				return ContextPolicyResult{}, ErrInvalidContextPolicy
			}
			prepared.ExternalTransfer = transferFloor
			if prepared.Persist == evidence.DecisionDeny || !contextPolicyValidLocalPreparedRepresentation(prepared) {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedPersistence,
				})
				policyFiltered = true
				continue
			}

			output := cloneContextItem(input)
			output.ID = prepared.ID
			output.Evidence = &prepared
			if !contextPolicySafeItemRepresentation(output) {
				result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
					ItemID: input.ID, Included: false,
					Reason: ContextPolicyItemExcludedInspection,
				})
				policyFiltered = true
				continue
			}
			if _, collision := originalIDs[output.ID]; collision && output.ID != input.ID {
				return ContextPolicyResult{}, ErrInvalidContextPolicy
			}
			if _, collision := includedIDs[output.ID]; collision {
				return ContextPolicyResult{}, ErrInvalidContextPolicy
			}
			result.Items = append(result.Items, output)
			idRemap[input.ID] = output.ID
			includedIDs[output.ID] = struct{}{}
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, OutputID: output.ID, Included: true,
				Reason:   ContextPolicyItemIncluded,
				Redacted: prepared.ContentState == evidence.ContentStateRedacted,
			})
			continue
		}

		floor, floorErr := contextPolicyCombinedFloor(input.Evidence.ExternalTransfer, authorization)
		if floorErr != nil {
			return ContextPolicyResult{}, ErrInvalidContextPolicy
		}
		policy := contextPolicyForTransfer(request.TransferPolicy, floor)
		prepared, prepareErr := evidence.PrepareForExternalTransfer(*input.Evidence, policy)
		if prepareErr != nil || prepared.ValidatePrepared() != nil {
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, Included: false,
				Reason: ContextPolicyItemExcludedInvalid,
			})
			policyFiltered = true
			continue
		}
		if contextPolicyDecisionRank(prepared.ExternalTransfer) < contextPolicyDecisionRank(floor) ||
			prepared.ExternalTransfer == evidence.DecisionDeny {
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, Included: false,
				Reason: ContextPolicyItemExcludedTransferPolicy,
			})
			policyFiltered = true
			continue
		}
		if !contextPolicyValidPreparedRepresentation(prepared) {
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, Included: false,
				Reason: ContextPolicyItemExcludedTransferPolicy,
			})
			policyFiltered = true
			continue
		}

		output := cloneContextItem(input)
		output.ID = prepared.ID
		output.Evidence = &prepared
		if !contextPolicySafeItemRepresentation(output) {
			result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
				ItemID: input.ID, Included: false,
				Reason: ContextPolicyItemExcludedInspection,
			})
			policyFiltered = true
			continue
		}
		if _, collision := originalIDs[output.ID]; collision && output.ID != input.ID {
			return ContextPolicyResult{}, ErrInvalidContextPolicy
		}
		if _, collision := includedIDs[output.ID]; collision {
			return ContextPolicyResult{}, ErrInvalidContextPolicy
		}
		result.Items = append(result.Items, output)
		idRemap[input.ID] = output.ID
		includedIDs[output.ID] = struct{}{}
		result.ItemAudit = append(result.ItemAudit, ContextPolicyItemAudit{
			ItemID: input.ID, OutputID: output.ID, Included: true,
			Reason:   ContextPolicyItemIncluded,
			Redacted: prepared.ContentState == evidence.ContentStateRedacted,
		})
	}
	initialOutputToOriginal := contextPolicyInitialOutputToOriginal(idRemap, originalIDs)
	filteredDependencies := contextPolicyFilterDependentItems
	if mode == ContextPolicyModeLocal {
		filteredDependencies = contextPolicyFilterLocalDependentItems
	}
	if filteredDependencies(&result, &idRemap, originalIDs, initialOutputToOriginal) {
		policyFiltered = true
	}
	if err := ctx.Err(); err != nil {
		return ContextPolicyResult{}, err
	}
	includedIDs = make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		includedIDs[item.ID] = struct{}{}
	}

	for _, relation := range request.Relations {
		if err := contextPolicyContextErr(ctx); err != nil {
			return ContextPolicyResult{}, err
		}
		output, reason, include := contextPolicyRelationOutput(relation, idRemap, includedIDs, originalIDs, initialOutputToOriginal)
		if !include {
			result.RelationAudit = append(result.RelationAudit, ContextPolicyRelationAudit{
				RelationID: relation.ID, Included: false, Reason: reason,
			})
			policyFiltered = true
			continue
		}
		if mode == ContextPolicyModeLocal {
			if !contextPolicyLocalRelationReferencesRetained(output, idRemap, initialOutputToOriginal) {
				result.RelationAudit = append(result.RelationAudit, ContextPolicyRelationAudit{
					RelationID: relation.ID, Included: false,
					Reason: ContextPolicyRelationExcludedSupport,
				})
				policyFiltered = true
				continue
			}
			output.Provenance = contextPolicyRemapLocalProvenance(output.Provenance, idRemap)
		}
		if _, collision := includedIDs[output.ID]; collision || !contextPolicyRelationRepresentationSafe(output) {
			result.RelationAudit = append(result.RelationAudit, ContextPolicyRelationAudit{
				RelationID: relation.ID, Included: false,
				Reason: ContextPolicyRelationExcludedInvalid,
			})
			policyFiltered = true
			continue
		}
		result.Relations = append(result.Relations, output)
		result.RelationAudit = append(result.RelationAudit, ContextPolicyRelationAudit{
			RelationID: relation.ID, Included: true,
			Reason: ContextPolicyRelationIncluded,
		})
	}

	for _, id := range request.ContinuationIDs {
		if remapped, ok := idRemap[id]; ok {
			result.ContinuationIDs = append(result.ContinuationIDs, remapped)
		}
	}
	if policyFiltered {
		result.PolicyFiltered = true
		result.Degradations = []ContextDegradation{{Code: ContextDegradationPolicyFiltered}}
	}
	if err := result.Validate(); err != nil {
		return ContextPolicyResult{}, ErrInvalidContextPolicy
	}
	return result, nil
}

func contextPolicyContextErr(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContextPolicy
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func contextPolicySafeItemRepresentation(item ContextItem) bool {
	encoded, err := CanonicalContextItemJSON(item)
	if err != nil {
		return false
	}
	inspection := evidence.InspectContent(string(encoded))
	return inspection.Classification == evidence.ClassificationSafeText
}

func contextPolicyValidPreparedRepresentation(unit evidence.EvidenceUnit) bool {
	inspection := evidence.InspectContent(unit.Content)
	switch unit.ContentState {
	case evidence.ContentStatePresent:
		return unit.ExternalTransfer == evidence.DecisionAllow && unit.Content != "" &&
			inspection.Classification == evidence.ClassificationSafeText
	case evidence.ContentStateRedacted:
		return unit.ExternalTransfer != evidence.DecisionDeny &&
			unit.Content == evidence.RedactedContent &&
			inspection.Classification == evidence.ClassificationSafeText
	default:
		return false
	}
}

func contextPolicyFinalDecision(policy *evidence.Policy, authorization evidence.Decision, classification evidence.Classification) (evidence.Decision, error) {
	if authorization.Validate() != nil {
		return evidence.DecisionDeny, ErrInvalidContextPolicy
	}
	if policy == nil {
		return authorization, nil
	}
	resolved, err := policy.Resolve(classification)
	if err != nil {
		return evidence.DecisionDeny, ErrInvalidContextPolicy
	}
	combined, err := evidence.CombinePolicyLayers(resolved, evidence.PolicyLayer{ExternalTransfer: authorization})
	if err != nil {
		return evidence.DecisionDeny, ErrInvalidContextPolicy
	}
	return combined.ExternalTransfer, nil
}

func contextPolicyFilterDependentItems(result *ContextPolicyResult, remap *map[string]string, originalIDs map[string]struct{}, initialOutputToOriginal map[string]string) bool {
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
					originalReferenceID, inputItem = reference.ID, false
					_, inputItem = originalIDs[reference.ID]
				}
				if !inputItem {
					continue
				}
				if _, available := (*remap)[originalReferenceID]; !available {
					remove[originalID] = ContextPolicyItemExcludedSupport
					break
				}
			}
			if _, dependent := remove[originalID]; dependent {
				continue
			}
			item.SupportIDs = contextPolicyRemapIDs(item.SupportIDs, *remap)
			item.Provenance = contextPolicyRemapProvenance(item.Provenance, *remap)
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

func contextPolicyRemapIDs(ids []string, remap map[string]string) []string {
	remapped := make([]string, 0, len(ids))
	for _, id := range ids {
		if outputID, ok := remap[id]; ok {
			remapped = append(remapped, outputID)
		} else {
			remapped = append(remapped, id)
		}
	}
	return remapped
}

func contextPolicyForTransfer(policy *evidence.Policy, floor evidence.Decision) evidence.Policy {
	if policy == nil {
		result := evidence.Policy{
			Installation: evidence.PolicyLayer{ExternalTransfer: evidence.DecisionAllow},
		}
		result.Installation.ExternalTransfer = floor
		return result
	}
	zeroPolicy := policy.IsZero()
	result := *policy
	if zeroPolicy {
		result = evidence.DefaultPolicy()
	}
	if !zeroPolicy && policy.Classifications != nil {
		result.Classifications = make(map[evidence.Classification]evidence.PolicyLayer, len(policy.Classifications))
		for classification, layer := range policy.Classifications {
			result.Classifications[classification] = layer
		}
	}
	combined, err := evidence.CombinePolicyLayers(result.Installation, evidence.PolicyLayer{ExternalTransfer: floor})
	if err == nil {
		result.Installation.ExternalTransfer = combined.ExternalTransfer
	} else {
		result.Installation.ExternalTransfer = evidence.DecisionDeny
	}
	return result
}

func contextPolicyCombinedFloor(left, right evidence.Decision) (evidence.Decision, error) {
	combined, err := evidence.CombinePolicyLayers(
		evidence.PolicyLayer{ExternalTransfer: left},
		evidence.PolicyLayer{ExternalTransfer: right},
	)
	if err != nil {
		return evidence.DecisionDeny, ErrInvalidContextPolicy
	}
	return combined.ExternalTransfer, nil
}

func contextPolicyDecisionRank(decision evidence.Decision) int {
	switch decision {
	case evidence.DecisionAllow:
		return 0
	case evidence.DecisionRedact:
		return 1
	case evidence.DecisionDeny:
		return 2
	default:
		return -1
	}
}

func contextPolicyRelationOutput(relation ContextRelation, remap map[string]string, included, originalIDs map[string]struct{}, initialOutputToOriginal map[string]string) (ContextRelation, ContextPolicyRelationAuditReason, bool) {
	output := cloneContextRelation(relation)
	from, fromOK := remap[relation.FromID]
	to, toOK := remap[relation.ToID]
	if !fromOK || !toOK {
		return ContextRelation{}, ContextPolicyRelationExcludedEndpoint, false
	}
	output.FromID = from
	output.ToID = to
	for index, id := range relation.Path {
		remapped, ok := remap[id]
		if !ok {
			return ContextRelation{}, ContextPolicyRelationExcludedPath, false
		}
		output.Path[index] = remapped
	}
	for index, id := range relation.SupportIDs {
		remapped, ok := remap[id]
		if !ok {
			return ContextRelation{}, ContextPolicyRelationExcludedSupport, false
		}
		output.SupportIDs[index] = remapped
	}
	for _, reference := range output.Provenance.Evidence {
		originalID, tracked := initialOutputToOriginal[reference.ID]
		if !tracked {
			_, tracked = originalIDs[reference.ID]
			originalID = reference.ID
		}
		if tracked {
			if _, available := remap[originalID]; !available {
				return ContextRelation{}, ContextPolicyRelationExcludedSupport, false
			}
		}
	}
	output.Provenance = contextPolicyRemapProvenance(output.Provenance, remap)
	itemIDs := make(map[string]struct{}, len(included))
	for id := range included {
		itemIDs[id] = struct{}{}
	}
	if output.validate(itemIDs) != nil {
		return ContextRelation{}, ContextPolicyRelationExcludedInvalid, false
	}
	return output, ContextPolicyRelationIncluded, true
}

func contextPolicyRelationRepresentationSafe(relation ContextRelation) bool {
	encoded, err := CanonicalContextRelationJSON(relation)
	if err != nil {
		return false
	}
	inspection := evidence.InspectContent(string(encoded))
	return inspection.Classification == evidence.ClassificationSafeText
}

func contextPolicyInitialOutputToOriginal(remap map[string]string, originalIDs map[string]struct{}) map[string]string {
	result := make(map[string]string, len(originalIDs)+len(remap))
	for originalID := range originalIDs {
		result[originalID] = originalID
	}
	for originalID, outputID := range remap {
		result[outputID] = originalID
	}
	return result
}

func contextPolicyRemapProvenance(provenance ContextProvenance, remap map[string]string) ContextProvenance {
	result := cloneContextProvenance(provenance)
	for index, reference := range result.Evidence {
		if remapped, ok := remap[reference.ID]; ok {
			result.Evidence[index].ID = remapped
		}
	}
	return result
}

func contextPolicyFindItem(items []ContextItem, id string) (ContextItem, bool) {
	for _, item := range items {
		if item.ID == id {
			return item, true
		}
	}
	return ContextItem{}, false
}
