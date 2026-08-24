package query

import (
	"context"
	"errors"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/fact"
)

const (
	// ContextVersion identifies the versioned, consumer-neutral context
	// contract. It is independent from the legacy Evidence Package contract.
	ContextVersion = "v1alpha1"

	// Bounds are representation limits for this contract. They keep malformed
	// requests and packages cheap to reject without supplying defaults.
	maxContextIdentifierBytes int64 = 256
	maxContextRevisionBytes   int64 = 256
	maxContextQuestionBytes   int64 = 16 << 10
	maxContextContinuation    int64 = 4 << 10
	maxContextLocatorBytes    int64 = 4 << 10
	maxContextItems                 = 10_000
	maxContextRelations             = 10_000
	maxContextAudits                = 20_000
	maxContextDegradations          = 256
	maxContextPathNodes             = 1_024
	maxContextTextBytes       int64 = 1 << 20
	maxContextTokens                = 1 << 20
	maxContextCharacters      int64 = 16 << 20
	maxContextBytes           int64 = 16 << 20
)

var (
	// ErrInvalidContext is the stable root sentinel for malformed context
	// requests, packages and supporting values.
	ErrInvalidContext = errors.New("query: invalid context")
	// ErrUnsupportedContextVersion identifies a context representation that is
	// not understood by this package.
	ErrUnsupportedContextVersion = errors.New("query: unsupported context version")
	// ErrInvalidContextScope identifies a missing, malformed or inconsistent
	// organization/source/snapshot boundary.
	ErrInvalidContextScope = errors.New("query: invalid context scope")
	// ErrInvalidContextBudget identifies a non-positive or unrepresentable
	// context budget.
	ErrInvalidContextBudget = errors.New("query: invalid context budget")
	// ErrInvalidContextReference identifies an invalid ID, digest, locator,
	// fact, evidence unit or collection reference.
	ErrInvalidContextReference = errors.New("query: invalid context reference")
	// ErrInvalidContextContinuation identifies an incoherent continuation.
	ErrInvalidContextContinuation = errors.New("query: invalid context continuation")

	// Descriptive aliases keep callers from depending on the internal shape of
	// the context contract while preserving one stable error vocabulary.
	ErrInvalidContextRequest = ErrInvalidContext
	ErrInvalidContextPackage = ErrInvalidContext
)

// IntentKind identifies the supported context targets. A request has exactly
// one compatible target shape; the validator never infers a kind from text.
type IntentKind string

const (
	IntentKindQuestion           IntentKind = "question"
	IntentKindEntity             IntentKind = "entity"
	IntentKindSymbol             IntentKind = "symbol"
	IntentKindPossibleImpact     IntentKind = "possible_impact"
	IntentKindEvidenceInspection IntentKind = "evidence_inspection"

	// Readable aliases for callers using the shorter domain vocabulary.
	IntentQuestion           = IntentKindQuestion
	IntentEntity             = IntentKindEntity
	IntentSymbol             = IntentKindSymbol
	IntentPossibleImpact     = IntentKindPossibleImpact
	IntentEvidenceInspection = IntentKindEvidenceInspection
)

// IntentTargetKind identifies the identity kind accepted by an Intent.
type IntentTargetKind string

const (
	IntentTargetUnknown  IntentTargetKind = ""
	IntentTargetEntity   IntentTargetKind = "entity"
	IntentTargetSymbol   IntentTargetKind = "symbol"
	IntentTargetEvidence IntentTargetKind = "evidence"
)

// IntentTarget is the one opaque identity selected by a non-question intent.
// The ID is not a source path or a query expression.
type IntentTarget struct {
	Kind IntentTargetKind `json:"kind"`
	ID   string           `json:"id"`
}

// Intent is the typed purpose of one context request. Question is used only
// for IntentKindQuestion; all other kinds require exactly one compatible
// IntentTarget and cannot carry free-form question text.
type Intent struct {
	Version  string       `json:"version"`
	Kind     IntentKind   `json:"kind"`
	Question string       `json:"question,omitempty"`
	Target   IntentTarget `json:"target"`
}

// ContextIntent is the descriptive spelling used by application adapters.
type ContextIntent = Intent

// Validate checks the version, kind, target shape and bounded question text.
func (i Intent) Validate() error {
	if i.Version != ContextVersion {
		return ErrUnsupportedContextVersion
	}

	switch i.Kind {
	case IntentKindQuestion:
		if i.Target != (IntentTarget{}) || !validContextText(i.Question, maxContextQuestionBytes, true) {
			return ErrInvalidContext
		}
	case IntentKindEntity:
		if i.Question != "" || !validIntentTarget(i.Target, IntentTargetEntity) {
			return ErrInvalidContextReference
		}
	case IntentKindSymbol:
		if i.Question != "" || !validIntentTarget(i.Target, IntentTargetSymbol) {
			return ErrInvalidContextReference
		}
	case IntentKindPossibleImpact:
		if i.Question != "" || !validImpactTarget(i.Target) {
			return ErrInvalidContextReference
		}
	case IntentKindEvidenceInspection:
		if i.Question != "" || !validIntentTarget(i.Target, IntentTargetEvidence) {
			return ErrInvalidContextReference
		}
	default:
		return ErrInvalidContext
	}
	return nil
}

func validIntentTarget(target IntentTarget, expected IntentTargetKind) bool {
	return target.Kind == expected && validContextID(target.ID)
}

func validImpactTarget(target IntentTarget) bool {
	if target.Kind != IntentTargetEntity && target.Kind != IntentTargetSymbol {
		return false
	}
	return validContextID(target.ID)
}

// ContextLimits is the explicit budget applied to one context package. Every
// field is required and strictly positive; zero does not mean "use default".
type ContextLimits struct {
	MaxTokens     int   `json:"max_tokens"`
	MaxItems      int   `json:"max_items"`
	MaxCharacters int64 `json:"max_characters"`
	MaxBytes      int64 `json:"max_bytes"`
}

// ContextBudget is a descriptive alias for ContextLimits.
type ContextBudget = ContextLimits

// Validate rejects zero, negative and unrepresentably large budgets.
func (l ContextLimits) Validate() error {
	if l.MaxTokens < 1 || l.MaxTokens > maxContextTokens ||
		l.MaxItems < 1 || l.MaxItems > maxContextItems ||
		l.MaxCharacters < 1 || l.MaxCharacters > maxContextCharacters ||
		l.MaxBytes < 1 || l.MaxBytes > maxContextBytes {
		return ErrInvalidContextBudget
	}
	return nil
}

// ContextContinuation is an opaque continuation handle. The continuation
// codec signs its complete binding and sequence position; public metadata is
// repeated here so a package can expose the binding without opening the token.
type ContextContinuation struct {
	Token            string `json:"token"`
	Scope            *Scope `json:"scope,omitempty"`
	SnapshotRevision string `json:"snapshot_revision,omitempty"`
	IntentDigest     string `json:"intent_digest,omitempty"`
	PolicyDigest     string `json:"policy_digest,omitempty"`
	AlgorithmVersion string `json:"algorithm_version,omitempty"`
	Ordering         string `json:"ordering,omitempty"`
}

// Continuation is a concise alias for ContextContinuation.
type Continuation = ContextContinuation

// Validate checks the opaque handle and optional, non-secret binding shape.
func (c ContextContinuation) Validate() error {
	if !validContextString(c.Token, maxContextContinuation) {
		return ErrInvalidContextContinuation
	}
	if c.Scope != nil {
		if err := c.Scope.Validate(); err != nil {
			return ErrInvalidContextScope
		}
	}
	if c.SnapshotRevision != "" && !validContextString(c.SnapshotRevision, maxContextRevisionBytes) {
		return ErrInvalidContextContinuation
	}
	if c.IntentDigest != "" && !isSHA256(c.IntentDigest) {
		return ErrInvalidContextContinuation
	}
	if c.PolicyDigest != "" && !isSHA256(c.PolicyDigest) {
		return ErrInvalidContextContinuation
	}
	if c.AlgorithmVersion != "" && !validContextID(c.AlgorithmVersion) {
		return ErrInvalidContextContinuation
	}
	if c.Ordering != "" && !validContextID(c.Ordering) {
		return ErrInvalidContextContinuation
	}
	return nil
}

// ContextRequest is the sole input boundary for assembling a context
// package. It contains no backend selector or authorization implementation.
type ContextRequest struct {
	Version      string               `json:"version"`
	Scope        Scope                `json:"scope"`
	Intent       Intent               `json:"intent"`
	Limits       ContextLimits        `json:"limits"`
	Continuation *ContextContinuation `json:"continuation,omitempty"`
}

// Validate checks the request without resolving aliases or applying defaults.
func (r ContextRequest) Validate() error {
	if r.Version != ContextVersion {
		return ErrUnsupportedContextVersion
	}
	if err := r.Scope.Validate(); err != nil {
		return ErrInvalidContextScope
	}
	if err := r.Intent.Validate(); err != nil {
		return err
	}
	if err := r.Limits.Validate(); err != nil {
		return err
	}
	if r.Continuation != nil {
		if err := r.Continuation.Validate(); err != nil {
			return err
		}
		if r.Continuation.Scope != nil && !sameScope(*r.Continuation.Scope, r.Scope) {
			return ErrInvalidContextContinuation
		}
	}
	return nil
}

// ContextKnowledgeKind distinguishes the epistemic source of an item or
// relation. Derived is technical rule output; it is not a fifth knowledge
// state.
type ContextKnowledgeKind string

const (
	ContextKnowledgeObserved  ContextKnowledgeKind = "observed"
	ContextKnowledgeDerived   ContextKnowledgeKind = "derived"
	ContextKnowledgeGenerated ContextKnowledgeKind = "generated"
	ContextKnowledgeCurated   ContextKnowledgeKind = "curated"

	KnowledgeObserved  = ContextKnowledgeObserved
	KnowledgeDerived   = ContextKnowledgeDerived
	KnowledgeGenerated = ContextKnowledgeGenerated
	KnowledgeCurated   = ContextKnowledgeCurated
)

// ContextOrigin is a readable alias for ContextKnowledgeKind.
type ContextOrigin = ContextKnowledgeKind

func validKnowledgeKind(value ContextKnowledgeKind) bool {
	switch value {
	case ContextKnowledgeObserved, ContextKnowledgeDerived, ContextKnowledgeGenerated, ContextKnowledgeCurated:
		return true
	default:
		return false
	}
}

// ContextProvenance reuses canonical producer, lineage and evidence
// references. It carries no source payload.
type ContextProvenance struct {
	Producer *fact.Producer     `json:"producer,omitempty"`
	Lineage  *fact.Lineage      `json:"lineage,omitempty"`
	Evidence []fact.EvidenceRef `json:"evidence,omitempty"`
}

// Provenance is a descriptive alias.
type Provenance = ContextProvenance

func (p ContextProvenance) validate(scope Scope) error {
	factScope := fact.Scope{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
	}
	if len(p.Evidence) > maxContextItems {
		return ErrInvalidContextBudget
	}
	if p.Producer != nil {
		if err := p.Producer.Validate(); err != nil {
			return ErrInvalidContextReference
		}
	}
	if p.Lineage != nil {
		if err := p.Lineage.Validate(); err != nil {
			return ErrInvalidContextReference
		}
	}
	seen := make(map[string]struct{}, len(p.Evidence))
	for _, reference := range p.Evidence {
		if err := reference.Validate(factScope); err != nil || !validContextID(reference.ID) {
			return ErrInvalidContextReference
		}
		if _, exists := seen[reference.ID]; exists {
			return ErrInvalidContextReference
		}
		seen[reference.ID] = struct{}{}
	}
	return nil
}

// Validate checks provenance against its enclosing context scope.
func (p ContextProvenance) Validate(scope Scope) error {
	if err := scope.Validate(); err != nil {
		return ErrInvalidContextScope
	}
	return p.validate(scope)
}

// ContextItemKind identifies one of the three item payloads carried by a
// package. The payload matching Kind must be present exactly once.
type ContextItemKind string

const (
	ContextItemFact     ContextItemKind = "fact"
	ContextItemEntity   ContextItemKind = "entity"
	ContextItemEvidence ContextItemKind = "evidence"

	ItemKindFact     = ContextItemFact
	ItemKindEntity   = ContextItemEntity
	ItemKindEvidence = ContextItemEvidence
)

// ContextItem is one selected fact, entity or evidence unit. Canonical facts
// and evidence remain owned by their respective packages; this type only
// adds context selection metadata and a bounded locator.
type ContextItem struct {
	ID         string                 `json:"id"`
	Kind       ContextItemKind        `json:"kind"`
	Origin     ContextKnowledgeKind   `json:"origin"`
	Scope      Scope                  `json:"scope"`
	Fact       *fact.CanonicalFact    `json:"fact,omitempty"`
	Entity     *fact.Participant      `json:"entity,omitempty"`
	Evidence   *evidence.EvidenceUnit `json:"evidence,omitempty"`
	Locator    contract.Locator       `json:"locator"`
	Provenance ContextProvenance      `json:"provenance"`
	SupportIDs []string               `json:"support_ids,omitempty"`
}

func (i ContextItem) validate() error {
	if !validContextID(i.ID) || !validKnowledgeKind(i.Origin) {
		return ErrInvalidContextReference
	}
	if err := i.Scope.Validate(); err != nil {
		return ErrInvalidContextScope
	}
	if err := i.Provenance.validate(i.Scope); err != nil {
		return err
	}
	if !validateContextIDs(i.SupportIDs, maxContextItems) {
		return ErrInvalidContextReference
	}

	payloads := 0
	switch i.Kind {
	case ContextItemFact:
		if i.Fact == nil || i.Entity != nil || i.Evidence != nil {
			return ErrInvalidContextReference
		}
		payloads = 1
		if i.Fact.ID != i.ID || !sameFactScope(i.Fact.Scope, i.Scope) || i.Fact.Validate() != nil {
			return ErrInvalidContextReference
		}
		if i.Fact.Lineage != nil && i.Origin != ContextKnowledgeDerived {
			return ErrInvalidContextReference
		}
	case ContextItemEntity:
		if i.Entity == nil || i.Fact != nil || i.Evidence != nil {
			return ErrInvalidContextReference
		}
		payloads = 1
		if i.Entity.ID != i.ID || i.Entity.Validate() != nil {
			return ErrInvalidContextReference
		}
	case ContextItemEvidence:
		if i.Evidence == nil || i.Fact != nil || i.Entity != nil {
			return ErrInvalidContextReference
		}
		payloads = 1
		if i.Evidence.ID != i.ID || !sameEvidenceScope(*i.Evidence, i.Scope) || i.Evidence.Validate() != nil {
			return ErrInvalidContextReference
		}
	default:
		return ErrInvalidContextReference
	}
	if payloads != 1 || !validContextLocator(i.locatorForValidation(), i.Scope) {
		return ErrInvalidContextReference
	}
	return nil
}

// Validate checks one item independently of package-level collection
// references.
func (i ContextItem) Validate() error { return i.validate() }

func (i ContextItem) locatorForValidation() contract.Locator {
	if !isZeroLocator(i.Locator) {
		return i.Locator
	}
	if i.Evidence != nil {
		return i.Evidence.Locator
	}
	if i.Fact != nil && len(i.Fact.Evidence) > 0 {
		return i.Fact.Evidence[0].Locator
	}
	return i.Locator
}

// ContextRelation is a supported relationship or path between selected
// items. FromID and ToID identify endpoints; Path contains endpoint-to-
// endpoint item IDs when a path was selected. SupportIDs must identify items
// that sustain the relation.
type ContextRelation struct {
	ID         string               `json:"id"`
	Predicate  fact.Predicate       `json:"predicate"`
	Origin     ContextKnowledgeKind `json:"origin"`
	Scope      Scope                `json:"scope"`
	FromID     string               `json:"from_id"`
	ToID       string               `json:"to_id"`
	Path       []string             `json:"path,omitempty"`
	SupportIDs []string             `json:"support_ids"`
	Provenance ContextProvenance    `json:"provenance"`
}

// Relation is a concise alias for ContextRelation.
type Relation = ContextRelation

func (r ContextRelation) validate(itemIDs map[string]struct{}) error {
	if !validContextID(r.ID) || !validKnowledgeKind(r.Origin) {
		return ErrInvalidContextReference
	}
	if err := r.Scope.Validate(); err != nil {
		return ErrInvalidContextScope
	}
	if err := r.Predicate.Validate(); err != nil {
		return ErrInvalidContextReference
	}
	if err := r.Provenance.validate(r.Scope); err != nil {
		return err
	}
	if !validContextID(r.FromID) || !validContextID(r.ToID) {
		return ErrInvalidContextReference
	}
	if _, ok := itemIDs[r.FromID]; !ok {
		return ErrInvalidContextReference
	}
	if _, ok := itemIDs[r.ToID]; !ok {
		return ErrInvalidContextReference
	}
	if len(r.SupportIDs) == 0 || len(r.SupportIDs) > maxContextItems || !validateContextIDs(r.SupportIDs, maxContextItems) {
		return ErrInvalidContextReference
	}
	for _, id := range r.SupportIDs {
		if _, ok := itemIDs[id]; !ok {
			return ErrInvalidContextReference
		}
	}
	if len(r.Path) > maxContextPathNodes || !validateContextIDs(r.Path, maxContextPathNodes) {
		return ErrInvalidContextReference
	}
	if len(r.Path) > 0 {
		if r.Path[0] != r.FromID || r.Path[len(r.Path)-1] != r.ToID {
			return ErrInvalidContextReference
		}
		for _, id := range r.Path {
			if _, ok := itemIDs[id]; !ok {
				return ErrInvalidContextReference
			}
		}
	}
	return nil
}

// Validate checks the relation shape. Package validation additionally checks
// that endpoints, path nodes and support IDs exist in the selected items.
func (r ContextRelation) Validate() error {
	itemIDs := make(map[string]struct{}, 1+len(r.Path)+len(r.SupportIDs))
	itemIDs[r.FromID] = struct{}{}
	itemIDs[r.ToID] = struct{}{}
	for _, id := range r.Path {
		itemIDs[id] = struct{}{}
	}
	for _, id := range r.SupportIDs {
		itemIDs[id] = struct{}{}
	}
	return r.validate(itemIDs)
}

// ContextSelectionReason is the content-free vocabulary for selection audit.
type ContextSelectionReason string

const (
	ContextSelectionIncluded           ContextSelectionReason = "included"
	ContextSelectionExcludedBudget     ContextSelectionReason = "excluded_budget"
	ContextSelectionExcludedRedundancy ContextSelectionReason = "excluded_redundancy"
	ContextSelectionExcludedPolicy     ContextSelectionReason = "excluded_policy"
	ContextSelectionExcludedInvalid    ContextSelectionReason = "excluded_invalid"
	ContextSelectionExcludedScope      ContextSelectionReason = "excluded_scope"
)

// ContextSelectionAudit records a selection decision without source text or
// candidate payload. Costs are estimates supplied by the compositor.
type ContextSelectionAudit struct {
	ItemID        string                 `json:"item_id"`
	Included      bool                   `json:"included"`
	Reason        ContextSelectionReason `json:"reason"`
	Rank          int                    `json:"rank,omitempty"`
	Score         float64                `json:"score,omitempty"`
	TokenEstimate int                    `json:"token_estimate,omitempty"`
	Characters    int64                  `json:"characters,omitempty"`
	Bytes         int64                  `json:"bytes,omitempty"`
}

// SelectionAudit is a concise alias.
type SelectionAudit = ContextSelectionAudit

func (a ContextSelectionAudit) validate() error {
	if !validContextID(a.ItemID) || a.Rank < 0 || a.TokenEstimate < 0 || a.Characters < 0 || a.Bytes < 0 ||
		math.IsNaN(a.Score) || math.IsInf(a.Score, 0) {
		return ErrInvalidContextReference
	}
	if (a.Included && a.Reason != ContextSelectionIncluded) ||
		(!a.Included && a.Reason == ContextSelectionIncluded) {
		return ErrInvalidContextReference
	}
	switch a.Reason {
	case ContextSelectionIncluded, ContextSelectionExcludedBudget,
		ContextSelectionExcludedRedundancy, ContextSelectionExcludedPolicy,
		ContextSelectionExcludedInvalid, ContextSelectionExcludedScope:
		return nil
	default:
		return ErrInvalidContext
	}
}

// Validate checks one content-free selection decision.
func (a ContextSelectionAudit) Validate() error { return a.validate() }

// ContextDegradationCode identifies an unavailable optional signal or a
// controlled limitation. It is deliberately a closed, payload-free enum.
type ContextDegradationCode string

const (
	ContextDegradationVectorUnavailable   ContextDegradationCode = "vector_unavailable"
	ContextDegradationTextUnavailable     ContextDegradationCode = "text_unavailable"
	ContextDegradationRelationUnavailable ContextDegradationCode = "relation_unavailable"
	ContextDegradationExactUnavailable    ContextDegradationCode = "exact_unavailable"
	ContextDegradationSupportIncomplete   ContextDegradationCode = "support_incomplete"
	ContextDegradationBudgetExhausted     ContextDegradationCode = "budget_exhausted"
	ContextDegradationCoverageIncomplete  ContextDegradationCode = "coverage_incomplete"
	ContextDegradationPolicyFiltered      ContextDegradationCode = "policy_filtered"
)

// ContextDegradation describes a controlled limitation without diagnostics or
// source payload.
type ContextDegradation struct {
	Code ContextDegradationCode `json:"code"`
}

// Degradation is a concise alias.
type Degradation = ContextDegradation

func (d ContextDegradation) validate() error {
	switch d.Code {
	case ContextDegradationVectorUnavailable, ContextDegradationTextUnavailable,
		ContextDegradationRelationUnavailable, ContextDegradationExactUnavailable,
		ContextDegradationSupportIncomplete, ContextDegradationBudgetExhausted,
		ContextDegradationCoverageIncomplete, ContextDegradationPolicyFiltered:
		return nil
	default:
		return ErrInvalidContext
	}
}

// Validate checks one controlled degradation code.
func (d ContextDegradation) Validate() error { return d.validate() }

// ContextPackage is the versioned, bounded and consumer-neutral context
// representation. It contains only selected canonical material and metadata;
// it does not provide source or persistence access.
type ContextPackage struct {
	Version        string                  `json:"version"`
	ID             string                  `json:"id"`
	Digest         string                  `json:"digest"`
	Revision       string                  `json:"revision"`
	Scope          Scope                   `json:"scope"`
	Intent         Intent                  `json:"intent"`
	Limits         ContextLimits           `json:"limits"`
	Items          []ContextItem           `json:"items"`
	Relations      []ContextRelation       `json:"relations,omitempty"`
	Coverage       []contract.Coverage     `json:"coverage,omitempty"`
	Gaps           []contract.Gap          `json:"gaps,omitempty"`
	Degradations   []ContextDegradation    `json:"degradations,omitempty"`
	Audit          []ContextSelectionAudit `json:"audit,omitempty"`
	TokenEstimate  int                     `json:"token_estimate"`
	CharactersUsed int64                   `json:"characters_used"`
	BytesUsed      int64                   `json:"bytes_used"`
	Truncated      bool                    `json:"truncated"`
	Continuation   *ContextContinuation    `json:"continuation,omitempty"`
}

// Package is the descriptive alias for ContextPackage.
type Context = ContextPackage

// Validate checks all package boundaries, payload identity, scope coherence,
// collection references, applied accounting and continuation state.
func (p ContextPackage) Validate() error {
	if p.Version != ContextVersion {
		return ErrUnsupportedContextVersion
	}
	if !validContextID(p.ID) || !isSHA256(p.Digest) || !validContextString(p.Revision, maxContextRevisionBytes) {
		return ErrInvalidContextReference
	}
	if err := p.Scope.Validate(); err != nil {
		return ErrInvalidContextScope
	}
	if err := p.Intent.Validate(); err != nil {
		return err
	}
	if err := p.Limits.Validate(); err != nil {
		return err
	}
	if len(p.Items) > p.Limits.MaxItems || len(p.Items) > maxContextItems || len(p.Relations) > maxContextRelations ||
		len(p.Coverage) > maxContextItems || len(p.Gaps) > maxContextItems ||
		len(p.Audit) > maxContextAudits || len(p.Degradations) > maxContextDegradations {
		return ErrInvalidContextBudget
	}
	if p.TokenEstimate < 0 || p.TokenEstimate > p.Limits.MaxTokens ||
		p.CharactersUsed < 0 || p.CharactersUsed > p.Limits.MaxCharacters ||
		p.BytesUsed < 0 || p.BytesUsed > p.Limits.MaxBytes {
		return ErrInvalidContextBudget
	}

	itemIDs := make(map[string]struct{}, len(p.Items))
	for _, item := range p.Items {
		if err := item.validate(); err != nil {
			return err
		}
		if !sameScope(item.Scope, p.Scope) {
			return ErrInvalidContextScope
		}
		if _, exists := itemIDs[item.ID]; exists {
			return ErrInvalidContextReference
		}
		itemIDs[item.ID] = struct{}{}
	}
	for _, item := range p.Items {
		for _, supportID := range item.SupportIDs {
			if supportID == item.ID {
				return ErrInvalidContextReference
			}
			if _, exists := itemIDs[supportID]; !exists {
				return ErrInvalidContextReference
			}
		}
	}

	seenIDs := make(map[string]struct{}, len(p.Items)+len(p.Relations))
	for id := range itemIDs {
		seenIDs[id] = struct{}{}
	}
	for _, relation := range p.Relations {
		if err := relation.validate(itemIDs); err != nil {
			return err
		}
		if !sameScope(relation.Scope, p.Scope) {
			return ErrInvalidContextScope
		}
		if _, exists := seenIDs[relation.ID]; exists {
			return ErrInvalidContextReference
		}
		seenIDs[relation.ID] = struct{}{}
	}

	if err := validateContextCoverageAndGaps(p.Coverage, p.Gaps, p.Scope); err != nil {
		return err
	}
	seenDegradations := make(map[ContextDegradationCode]struct{}, len(p.Degradations))
	for _, degradation := range p.Degradations {
		if err := degradation.validate(); err != nil {
			return err
		}
		if _, exists := seenDegradations[degradation.Code]; exists {
			return ErrInvalidContextReference
		}
		seenDegradations[degradation.Code] = struct{}{}
	}
	seenAudit := make(map[string]struct{}, len(p.Audit))
	includedAuditIDs := make(map[string]struct{}, len(p.Items))
	for _, audit := range p.Audit {
		if err := audit.validate(); err != nil {
			return err
		}
		if audit.Included {
			if _, exists := itemIDs[audit.ItemID]; !exists {
				return ErrInvalidContextReference
			}
			includedAuditIDs[audit.ItemID] = struct{}{}
		} else if _, selected := itemIDs[audit.ItemID]; selected {
			return ErrInvalidContextReference
		}
		if _, exists := seenAudit[audit.ItemID]; exists {
			return ErrInvalidContextReference
		}
		seenAudit[audit.ItemID] = struct{}{}
	}
	for itemID := range itemIDs {
		if _, included := includedAuditIDs[itemID]; !included {
			return ErrInvalidContextReference
		}
	}

	if p.Continuation == nil {
		if p.Truncated {
			return ErrInvalidContextContinuation
		}
	} else {
		if !p.Truncated || p.Continuation.Validate() != nil {
			return ErrInvalidContextContinuation
		}
		if p.Continuation.Scope != nil && !sameScope(*p.Continuation.Scope, p.Scope) {
			return ErrInvalidContextContinuation
		}
		if p.Continuation.SnapshotRevision != "" && p.Continuation.SnapshotRevision != p.Revision {
			return ErrInvalidContextContinuation
		}
	}
	return nil
}

func validateContextCoverageAndGaps(coverage []contract.Coverage, gaps []contract.Gap, scope Scope) error {
	seenCoverage := make(map[string]struct{}, len(coverage))
	for _, entry := range coverage {
		if entry.Validate() != nil || !validContextOptionalID(entry.ID) || !validContextText(entry.Dimension, maxContextIdentifierBytes, false) ||
			!validContextText(entry.Message, maxContextTextBytes, false) || !validContextOptionalLocator(entry.Locator, scope) {
			return ErrInvalidContextReference
		}
		if entry.ID != "" {
			if _, exists := seenCoverage[entry.ID]; exists {
				return ErrInvalidContextReference
			}
			seenCoverage[entry.ID] = struct{}{}
		}
	}
	seenGaps := make(map[string]struct{}, len(gaps))
	for _, gap := range gaps {
		if gap.Validate() != nil || !validContextOptionalID(gap.ID) || !validContextText(gap.Code, maxContextIdentifierBytes, false) ||
			!validContextText(gap.Message, maxContextTextBytes, true) || !validContextOptionalLocator(gap.Locator, scope) {
			return ErrInvalidContextReference
		}
		if gap.ID != "" {
			if _, exists := seenGaps[gap.ID]; exists {
				return ErrInvalidContextReference
			}
			seenGaps[gap.ID] = struct{}{}
		}
	}
	return nil
}

// ContextService is the sole application port for assembling context. The
// implementation is intentionally absent here; retrieval, ranking and
// authorization are supplied by later adapters behind this boundary.
type ContextService interface {
	BuildContext(context.Context, ContextRequest) (ContextPackage, error)
}

// Clone returns a detached package. It is safe for callers to mutate the
// result without changing the package returned by a service.
func (p ContextPackage) Clone() ContextPackage {
	clone := p
	clone.Items = make([]ContextItem, len(p.Items))
	for index, item := range p.Items {
		clone.Items[index] = cloneContextItem(item)
	}
	clone.Relations = make([]ContextRelation, len(p.Relations))
	for index, relation := range p.Relations {
		clone.Relations[index] = cloneContextRelation(relation)
	}
	clone.Coverage = cloneContractCoverage(p.Coverage)
	clone.Gaps = cloneContractGaps(p.Gaps)
	clone.Degradations = append([]ContextDegradation(nil), p.Degradations...)
	clone.Audit = append([]ContextSelectionAudit(nil), p.Audit...)
	clone.Continuation = cloneContextContinuation(p.Continuation)
	return clone
}

// Clone returns a detached request, including its continuation binding.
func (r ContextRequest) Clone() ContextRequest {
	clone := r
	clone.Continuation = cloneContextContinuation(r.Continuation)
	return clone
}

func cloneContextItem(item ContextItem) ContextItem {
	clone := item
	clone.Fact = cloneContextFact(item.Fact)
	if item.Entity != nil {
		entity := *item.Entity
		clone.Entity = &entity
	}
	clone.Evidence = cloneContextEvidence(item.Evidence)
	clone.Provenance = cloneContextProvenance(item.Provenance)
	clone.SupportIDs = append([]string(nil), item.SupportIDs...)
	return clone
}

func cloneContextRelation(relation ContextRelation) ContextRelation {
	clone := relation
	clone.Path = append([]string(nil), relation.Path...)
	clone.SupportIDs = append([]string(nil), relation.SupportIDs...)
	clone.Provenance = cloneContextProvenance(relation.Provenance)
	return clone
}

func cloneContextProvenance(provenance ContextProvenance) ContextProvenance {
	clone := provenance
	if provenance.Producer != nil {
		producer := *provenance.Producer
		clone.Producer = &producer
	}
	if provenance.Lineage != nil {
		lineage := *provenance.Lineage
		lineage.InputFactIDs = append([]string(nil), provenance.Lineage.InputFactIDs...)
		clone.Lineage = &lineage
	}
	clone.Evidence = append([]fact.EvidenceRef(nil), provenance.Evidence...)
	return clone
}

func cloneContextFact(value *fact.CanonicalFact) *fact.CanonicalFact {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Object = cloneContextParticipant(value.Object)
	clone.Value = cloneContextValue(value.Value)
	clone.Qualifiers = append([]fact.Qualifier(nil), value.Qualifiers...)
	clone.Evidence = append([]fact.EvidenceRef(nil), value.Evidence...)
	if value.Lineage != nil {
		lineage := *value.Lineage
		lineage.InputFactIDs = append([]string(nil), value.Lineage.InputFactIDs...)
		clone.Lineage = &lineage
	}
	return &clone
}

func cloneContextParticipant(value *fact.Participant) *fact.Participant {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneContextValue(value *fact.TypedValue) *fact.TypedValue {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneContextEvidence(value *evidence.EvidenceUnit) *evidence.EvidenceUnit {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Findings = append([]string(nil), value.Findings...)
	return &clone
}

func cloneContextContinuation(value *ContextContinuation) *ContextContinuation {
	if value == nil {
		return nil
	}
	clone := *value
	if value.Scope != nil {
		scope := *value.Scope
		clone.Scope = &scope
	}
	return &clone
}

func cloneContractCoverage(values []contract.Coverage) []contract.Coverage {
	clone := append([]contract.Coverage(nil), values...)
	for index := range clone {
		if values[index].Locator != nil {
			locator := *values[index].Locator
			clone[index].Locator = &locator
		}
	}
	return clone
}

func cloneContractGaps(values []contract.Gap) []contract.Gap {
	clone := append([]contract.Gap(nil), values...)
	for index := range clone {
		if values[index].Locator != nil {
			locator := *values[index].Locator
			clone[index].Locator = &locator
		}
	}
	return clone
}

func validContextID(value string) bool {
	return validateOpaqueID("context id", value, maxContextIdentifierBytes) == nil
}

func validContextOptionalID(value string) bool {
	return value == "" || validContextID(value)
}

func validContextString(value string, maxBytes int64) bool {
	return value != "" && utf8.ValidString(value) && int64(len([]byte(value))) <= maxBytes &&
		!containsControl(value) && !containsCredentialPattern(value) && strings.TrimSpace(value) == value
}

func validContextText(value string, maxBytes int64, required bool) bool {
	if value == "" {
		return !required
	}
	if !utf8.ValidString(value) || int64(len([]byte(value))) > maxBytes || containsCredentialPattern(value) {
		return false
	}
	for _, character := range value {
		if character == '\r' || character == '\t' || character == '\n' {
			continue
		}
		if unicodeControl(character) {
			return false
		}
	}
	return strings.TrimSpace(value) != ""
}

func unicodeControl(value rune) bool {
	return value < 0x20 || value == 0x7f
}

func validateContextIDs(values []string, max int) bool {
	if len(values) > max {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validContextID(value) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validContextLocator(locator contract.Locator, scope Scope) bool {
	return validContextOptionalLocator(&locator, scope)
}

func validContextOptionalLocator(locator *contract.Locator, scope Scope) bool {
	if locator == nil {
		return true
	}
	if locator.Validate() != nil || !sameLocatorScope(*locator, scope) {
		return false
	}
	return validateCitationLocator(*locator, scope.SourceID, maxContextLocatorBytes) == nil
}

func sameLocatorScope(locator contract.Locator, scope Scope) bool {
	return locator.SourceID == "" || strings.EqualFold(locator.SourceID, scope.SourceID)
}

func sameFactScope(left fact.Scope, right Scope) bool {
	return strings.EqualFold(left.OrganizationID, right.OrganizationID) &&
		strings.EqualFold(left.SourceID, right.SourceID) &&
		strings.EqualFold(left.SnapshotID, right.SnapshotID)
}

func sameEvidenceScope(value evidence.EvidenceUnit, scope Scope) bool {
	return strings.EqualFold(value.OrganizationID, scope.OrganizationID) &&
		strings.EqualFold(value.SourceID, scope.SourceID) &&
		strings.EqualFold(value.SnapshotID, scope.SnapshotID)
}

func isZeroLocator(locator contract.Locator) bool {
	return locator == (contract.Locator{})
}
