package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// MaxRelationHops is the hard safety boundary for relation expansion. The
	// relational projection deliberately does not implement graph traversal.
	MaxRelationHops = 1

	// DefaultRelationFanOut is used when a query leaves FanOut at zero.
	DefaultRelationFanOut = 25
	// MaxRelationFanOut prevents a caller from turning one-hop expansion into
	// an unbounded result set. Deployments may choose a smaller configured
	// value, but never a larger one.
	MaxRelationFanOut = 1000
)

var (
	// ErrInvalidRelationProjection identifies malformed projection data or a
	// query that cannot be scoped safely.
	ErrInvalidRelationProjection = errors.New("retrieval: invalid relation projection input")
	// ErrRelationScopeMismatch identifies an attempt to mix organizations,
	// sources, or snapshots in one projection operation.
	ErrRelationScopeMismatch = errors.New("retrieval: relation projection scope mismatch")
	// ErrRelationProjectionNotConfigured identifies a missing persistence port.
	ErrRelationProjectionNotConfigured = errors.New("retrieval: relation projection store is not configured")
	// ErrRelationTraversalLimit identifies an unsupported traversal request.
	ErrRelationTraversalLimit = errors.New("retrieval: relation traversal limit exceeded")
)

// RelationDirection chooses the directed edges adjacent to an anchor.
// Direction is never inferred from a relationship type.
type RelationDirection string

const (
	RelationDirectionOutbound RelationDirection = "outbound"
	RelationDirectionInbound  RelationDirection = "inbound"
	RelationDirectionBoth     RelationDirection = "both"
)

// RelationScope is mandatory for every relational projection operation.
// OrganizationID is deliberately explicit; it is not inferred from a path,
// source name, or database connection.
type RelationScope struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
}

// Validate checks the relational UUID scope without exposing values in an
// error. UUIDs are normalized to lower case by Normalize.
func (s RelationScope) Validate() error {
	for name, value := range map[string]string{
		"organization_id": s.OrganizationID,
		"source_id":       s.SourceID,
		"snapshot_id":     s.SnapshotID,
	} {
		if err := validateRelationUUID(name, value); err != nil {
			return err
		}
	}
	return nil
}

// Normalize validates and canonicalizes the scope used for comparisons and
// SQL parameters.
func (s RelationScope) Normalize() (RelationScope, error) {
	s.OrganizationID = strings.ToLower(strings.TrimSpace(s.OrganizationID))
	s.SourceID = strings.ToLower(strings.TrimSpace(s.SourceID))
	s.SnapshotID = strings.ToLower(strings.TrimSpace(s.SnapshotID))
	if err := s.Validate(); err != nil {
		return RelationScope{}, err
	}
	return s, nil
}

// RelationQuery requests at most one directed expansion around AnchorEntityID.
// A zero MaxHops explicitly disables expansion; one is the only supported
// expansion depth. A zero FanOut selects DefaultRelationFanOut.
type RelationQuery struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
	AnchorEntityID string
	Direction      RelationDirection
	MaxHops        int
	FanOut         int
}

// Scope returns the explicit query scope.
func (q RelationQuery) Scope() RelationScope {
	return RelationScope{
		OrganizationID: q.OrganizationID,
		SourceID:       q.SourceID,
		SnapshotID:     q.SnapshotID,
	}
}

// Normalize validates a query and applies only bounded defaults.
func (q RelationQuery) Normalize() (RelationQuery, error) {
	scope, err := q.Scope().Normalize()
	if err != nil {
		return RelationQuery{}, err
	}
	q.OrganizationID, q.SourceID, q.SnapshotID = scope.OrganizationID, scope.SourceID, scope.SnapshotID
	q.AnchorEntityID = strings.ToLower(strings.TrimSpace(q.AnchorEntityID))
	if err := validateRelationUUID("anchor_entity_id", q.AnchorEntityID); err != nil {
		return RelationQuery{}, err
	}
	if q.Direction == "" {
		q.Direction = RelationDirectionBoth
	}
	switch q.Direction {
	case RelationDirectionOutbound, RelationDirectionInbound, RelationDirectionBoth:
	default:
		return RelationQuery{}, fmt.Errorf("%w: unsupported relation direction", ErrInvalidRelationProjection)
	}
	if q.MaxHops < 0 {
		return RelationQuery{}, fmt.Errorf("%w: negative relation hops", ErrInvalidRelationProjection)
	}
	if q.MaxHops > MaxRelationHops {
		return RelationQuery{}, fmt.Errorf("%w: %w", ErrRelationTraversalLimit, ErrInvalidRelationProjection)
	}
	if q.FanOut == 0 {
		q.FanOut = DefaultRelationFanOut
	}
	if q.FanOut < 1 || q.FanOut > MaxRelationFanOut {
		return RelationQuery{}, fmt.Errorf("%w: relation fan-out is invalid", ErrInvalidRelationProjection)
	}
	return q, nil
}

// RelationRecord is the bounded directed row consumed by the relational
// projection. Persistence adapters map canonical persistence.Relationship
// rows to this DTO while retaining the canonical row as the source of truth;
// the retrieval package intentionally does not duplicate that persistence
// model or own a database.
type RelationRecord struct {
	ID             string
	OrganizationID string
	SourceID       string
	SnapshotID     string
	ExternalID     string
	FromEntityID   string
	ToEntityID     string
	Type           string
	Attributes     json.RawMessage
}

// DirectedRelation and RelationEdge are descriptive aliases for callers that
// use either the canonical relation or graph terminology.
type DirectedRelation = RelationRecord
type RelationEdge = RelationRecord

// Normalize validates a canonical relation projection row for scope and
// returns a defensive, deterministic representation. Attributes must be a
// JSON object, matching the canonical relationships table.
func (r RelationRecord) Normalize(scope RelationScope) (RelationRecord, error) {
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return RelationRecord{}, err
	}
	r.ID = strings.ToLower(strings.TrimSpace(r.ID))
	r.OrganizationID = strings.ToLower(strings.TrimSpace(r.OrganizationID))
	r.SourceID = strings.ToLower(strings.TrimSpace(r.SourceID))
	r.SnapshotID = strings.ToLower(strings.TrimSpace(r.SnapshotID))
	r.ExternalID = strings.TrimSpace(r.ExternalID)
	r.FromEntityID = strings.ToLower(strings.TrimSpace(r.FromEntityID))
	r.ToEntityID = strings.ToLower(strings.TrimSpace(r.ToEntityID))
	r.Type = strings.TrimSpace(r.Type)
	for name, value := range map[string]string{
		"relation_id": r.ID, "organization_id": r.OrganizationID,
		"source_id": r.SourceID, "snapshot_id": r.SnapshotID,
		"from_entity_id": r.FromEntityID, "to_entity_id": r.ToEntityID,
	} {
		if err := validateRelationUUID(name, value); err != nil {
			return RelationRecord{}, err
		}
	}
	if r.OrganizationID != normalizedScope.OrganizationID || r.SourceID != normalizedScope.SourceID || r.SnapshotID != normalizedScope.SnapshotID {
		return RelationRecord{}, fmt.Errorf("%w: relation row is outside requested scope", ErrRelationScopeMismatch)
	}
	if r.ExternalID == "" || r.Type == "" {
		return RelationRecord{}, fmt.Errorf("%w: relation identity is incomplete", ErrInvalidRelationProjection)
	}
	attributes, err := normalizeRelationAttributes(r.Attributes)
	if err != nil {
		return RelationRecord{}, err
	}
	r.Attributes = attributes
	return r, nil
}

// Validate checks a relation without changing its representation. Use
// Normalize when a canonical form and defensive attribute copy are needed.
func (r RelationRecord) Validate() error {
	_, err := r.Normalize(RelationScope{
		OrganizationID: r.OrganizationID,
		SourceID:       r.SourceID,
		SnapshotID:     r.SnapshotID,
	})
	return err
}

// RelationProvenance makes the source of every returned edge inspectable.
// The identifiers are copied from the canonical relation and explicit query
// scope; no path or opaque database handle is used as provenance.
type RelationProvenance struct {
	OrganizationID     string
	SourceID           string
	SnapshotID         string
	RelationID         string
	RelationExternalID string
	FromEntityID       string
	ToEntityID         string
}

// RelationHit is one directed edge returned by an expansion. Hops is always
// one for a non-empty result; the projection never returns transitive edges.
type RelationHit struct {
	RelationID         string
	OrganizationID     string
	SourceID           string
	SnapshotID         string
	RelationExternalID string
	FromEntityID       string
	ToEntityID         string
	RelationType       string
	Attributes         json.RawMessage
	Hops               int
	Provenance         RelationProvenance
}

// Edge is a concise alias for RelationHit.
type Edge = RelationHit

// RelationStore is the narrow persistence port for a rebuildable relational
// projection. Implementations read canonical entities/relationships and must
// apply all supplied scope and limit parameters; no in-memory production store
// is provided by this package.
type RelationStore interface {
	Expand(context.Context, RelationQuery) ([]RelationHit, error)
}

// RelationProjection validates query and result boundaries around a store.
// The projection itself retains no state, so rebuilding means re-reading the
// canonical relations through the injected store.
type RelationProjection struct {
	store RelationStore
}

// NewRelationProjection creates a stateless relational projection boundary.
func NewRelationProjection(store RelationStore) *RelationProjection {
	return &RelationProjection{store: store}
}

// Expand performs a bounded, directed, one-hop expansion through the store.
func (p *RelationProjection) Expand(ctx context.Context, query RelationQuery) ([]RelationHit, error) {
	if err := validateRelationContext(ctx); err != nil {
		return nil, err
	}
	if p == nil || p.store == nil {
		return nil, ErrRelationProjectionNotConfigured
	}
	normalized, err := query.Normalize()
	if err != nil {
		return nil, err
	}
	if normalized.MaxHops == 0 {
		return []RelationHit{}, nil
	}
	hits, err := p.store.Expand(ctx, normalized)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareRelationHits(normalized, hits)
	if err != nil {
		return nil, err
	}
	if len(prepared) > normalized.FanOut {
		prepared = prepared[:normalized.FanOut]
	}
	return prepared, nil
}

// ExpandRelations applies the same bounded algorithm to canonical relation
// rows supplied by a caller. It is useful for deterministic rebuild tests and
// for adapters that already loaded a snapshot; it does not persist a cache.
func ExpandRelations(ctx context.Context, query RelationQuery, relations []RelationRecord) ([]RelationHit, error) {
	if err := validateRelationContext(ctx); err != nil {
		return nil, err
	}
	normalized, err := query.Normalize()
	if err != nil {
		return nil, err
	}
	prepared, err := prepareRelationRecords(normalized.Scope(), relations)
	if err != nil {
		return nil, err
	}
	if normalized.MaxHops == 0 {
		return []RelationHit{}, nil
	}
	hits := make([]RelationHit, 0, len(prepared))
	seen := make(map[string]struct{}, len(prepared))
	for _, relation := range prepared {
		if !relationMatchesAnchor(normalized, relation) {
			continue
		}
		if _, exists := seen[relation.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate relation identity", ErrInvalidRelationProjection)
		}
		seen[relation.ID] = struct{}{}
		hits = append(hits, relationHitFromRecord(relation))
	}
	sortRelationHits(hits)
	if len(hits) > normalized.FanOut {
		hits = hits[:normalized.FanOut]
	}
	return hits, nil
}

func prepareRelationRecords(scope RelationScope, relations []RelationRecord) ([]RelationRecord, error) {
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return nil, err
	}
	prepared := make([]RelationRecord, 0, len(relations))
	seen := make(map[string]struct{}, len(relations))
	seenDirected := make(map[string]struct{}, len(relations))
	for _, relation := range relations {
		normalized, err := relation.Normalize(normalizedScope)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate relation identity", ErrInvalidRelationProjection)
		}
		seen[normalized.ID] = struct{}{}
		directedKey := normalized.FromEntityID + "\x00" + normalized.ToEntityID + "\x00" + normalized.Type
		if _, exists := seenDirected[directedKey]; exists {
			return nil, fmt.Errorf("%w: duplicate directed relation", ErrInvalidRelationProjection)
		}
		seenDirected[directedKey] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.Slice(prepared, func(i, j int) bool {
		return relationRecordLess(prepared[i], prepared[j])
	})
	return prepared, nil
}

func prepareRelationHits(query RelationQuery, hits []RelationHit) ([]RelationHit, error) {
	scope := query.Scope()
	prepared := make([]RelationHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	seenDirected := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		normalized, err := normalizeRelationHit(query, hit)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized.RelationID]; exists {
			return nil, fmt.Errorf("%w: duplicate relation result", ErrInvalidRelationProjection)
		}
		seen[normalized.RelationID] = struct{}{}
		directedKey := normalized.FromEntityID + "\x00" + normalized.ToEntityID + "\x00" + normalized.RelationType
		if _, exists := seenDirected[directedKey]; exists {
			return nil, fmt.Errorf("%w: duplicate directed relation result", ErrInvalidRelationProjection)
		}
		seenDirected[directedKey] = struct{}{}
		// A store may be broader than this boundary, but it may not bypass
		// the caller's anchor and direction filters.
		if !relationHitMatchesAnchor(query, normalized) {
			return nil, fmt.Errorf("%w: relation result is outside requested direction", ErrRelationScopeMismatch)
		}
		if normalized.OrganizationID != scope.OrganizationID || normalized.SourceID != scope.SourceID || normalized.SnapshotID != scope.SnapshotID {
			return nil, fmt.Errorf("%w: relation result is outside requested scope", ErrRelationScopeMismatch)
		}
		prepared = append(prepared, normalized)
	}
	sortRelationHits(prepared)
	return prepared, nil
}

func normalizeRelationHit(query RelationQuery, hit RelationHit) (RelationHit, error) {
	scope := query.Scope()
	normalizedScope, err := scope.Normalize()
	if err != nil {
		return RelationHit{}, err
	}
	hit.RelationID = strings.ToLower(strings.TrimSpace(hit.RelationID))
	hit.OrganizationID = strings.ToLower(strings.TrimSpace(hit.OrganizationID))
	hit.SourceID = strings.ToLower(strings.TrimSpace(hit.SourceID))
	hit.SnapshotID = strings.ToLower(strings.TrimSpace(hit.SnapshotID))
	hit.RelationExternalID = strings.TrimSpace(hit.RelationExternalID)
	hit.FromEntityID = strings.ToLower(strings.TrimSpace(hit.FromEntityID))
	hit.ToEntityID = strings.ToLower(strings.TrimSpace(hit.ToEntityID))
	hit.RelationType = strings.TrimSpace(hit.RelationType)
	for name, value := range map[string]string{
		"relation_id": hit.RelationID, "organization_id": hit.OrganizationID,
		"source_id": hit.SourceID, "snapshot_id": hit.SnapshotID,
		"from_entity_id": hit.FromEntityID, "to_entity_id": hit.ToEntityID,
	} {
		if err := validateRelationUUID(name, value); err != nil {
			return RelationHit{}, err
		}
	}
	if hit.OrganizationID != normalizedScope.OrganizationID || hit.SourceID != normalizedScope.SourceID || hit.SnapshotID != normalizedScope.SnapshotID {
		return RelationHit{}, fmt.Errorf("%w: relation result is outside requested scope", ErrRelationScopeMismatch)
	}
	if hit.RelationExternalID == "" || hit.RelationType == "" {
		return RelationHit{}, fmt.Errorf("%w: relation result identity is incomplete", ErrInvalidRelationProjection)
	}
	attributes, err := normalizeRelationAttributes(hit.Attributes)
	if err != nil {
		return RelationHit{}, err
	}
	hit.Attributes = attributes
	if hit.Hops == 0 {
		hit.Hops = 1
	}
	if hit.Hops != 1 {
		return RelationHit{}, fmt.Errorf("%w: relation result is not one hop", ErrRelationTraversalLimit)
	}
	expected := RelationProvenance{
		OrganizationID:     hit.OrganizationID,
		SourceID:           hit.SourceID,
		SnapshotID:         hit.SnapshotID,
		RelationID:         hit.RelationID,
		RelationExternalID: hit.RelationExternalID,
		FromEntityID:       hit.FromEntityID,
		ToEntityID:         hit.ToEntityID,
	}
	if hit.Provenance == (RelationProvenance{}) {
		hit.Provenance = expected
	} else if hit.Provenance != expected {
		return RelationHit{}, fmt.Errorf("%w: relation provenance is inconsistent", ErrInvalidRelationProjection)
	}
	return hit, nil
}

func relationHitFromRecord(relation RelationRecord) RelationHit {
	return RelationHit{
		RelationID:         relation.ID,
		OrganizationID:     relation.OrganizationID,
		SourceID:           relation.SourceID,
		SnapshotID:         relation.SnapshotID,
		RelationExternalID: relation.ExternalID,
		FromEntityID:       relation.FromEntityID,
		ToEntityID:         relation.ToEntityID,
		RelationType:       relation.Type,
		Attributes:         append(json.RawMessage(nil), relation.Attributes...),
		Hops:               1,
		Provenance: RelationProvenance{
			OrganizationID:     relation.OrganizationID,
			SourceID:           relation.SourceID,
			SnapshotID:         relation.SnapshotID,
			RelationID:         relation.ID,
			RelationExternalID: relation.ExternalID,
			FromEntityID:       relation.FromEntityID,
			ToEntityID:         relation.ToEntityID,
		},
	}
}

func relationMatchesAnchor(query RelationQuery, relation RelationRecord) bool {
	switch query.Direction {
	case RelationDirectionOutbound:
		return relation.FromEntityID == query.AnchorEntityID
	case RelationDirectionInbound:
		return relation.ToEntityID == query.AnchorEntityID
	case RelationDirectionBoth:
		return relation.FromEntityID == query.AnchorEntityID || relation.ToEntityID == query.AnchorEntityID
	default:
		return false
	}
}

func relationHitMatchesAnchor(query RelationQuery, hit RelationHit) bool {
	switch query.Direction {
	case RelationDirectionOutbound:
		return hit.FromEntityID == query.AnchorEntityID
	case RelationDirectionInbound:
		return hit.ToEntityID == query.AnchorEntityID
	case RelationDirectionBoth:
		return hit.FromEntityID == query.AnchorEntityID || hit.ToEntityID == query.AnchorEntityID
	default:
		return false
	}
}

func relationRecordLess(left, right RelationRecord) bool {
	for _, pair := range [][2]string{
		{left.FromEntityID, right.FromEntityID},
		{left.ToEntityID, right.ToEntityID},
		{left.Type, right.Type},
		{left.ID, right.ID},
	} {
		if pair[0] != pair[1] {
			return pair[0] < pair[1]
		}
	}
	return false
}

func sortRelationHits(hits []RelationHit) {
	sort.Slice(hits, func(i, j int) bool {
		left, right := hits[i], hits[j]
		for _, pair := range [][2]string{
			{left.FromEntityID, right.FromEntityID},
			{left.ToEntityID, right.ToEntityID},
			{left.RelationType, right.RelationType},
			{left.RelationID, right.RelationID},
		} {
			if pair[0] != pair[1] {
				return pair[0] < pair[1]
			}
		}
		return false
	})
}

func normalizeRelationAttributes(attributes json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(attributes)
	if len(trimmed) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(trimmed) || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: relation attributes must be a JSON object", ErrInvalidRelationProjection)
	}
	return append(json.RawMessage(nil), trimmed...), nil
}

func validateRelationUUID(field, value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return fmt.Errorf("%w: %s must be a uuid", ErrInvalidRelationProjection, field)
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return fmt.Errorf("%w: %s must be a uuid", ErrInvalidRelationProjection, field)
		}
	}
	return nil
}

func validateRelationContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidRelationProjection)
	}
	return ctx.Err()
}
