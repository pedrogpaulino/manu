package retrieval

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

const (
	// MaxTextProjectionCharacters bounds one derived row. Source files are
	// never accepted as a substitute for bounded Evidence Units.
	MaxTextProjectionCharacters = 16 * 1024
	DefaultTextSearchLimit      = 20
	MaxTextSearchLimit          = 1000
)

var (
	ErrInvalidTextProjection        = errors.New("retrieval: invalid text projection input")
	ErrTextNotPersistible           = errors.New("retrieval: evidence is not persistible as text")
	ErrTextProjectionNotConfigured  = errors.New("retrieval: text projection store is not configured")
	ErrIncrementalTextNotConfigured = errors.New("retrieval: incremental text projection store is not configured")
)

// TextEntry is the bounded, authorized input to the textual projection. Its
// IDs are canonical UUIDs; external contract identities remain in the
// canonical Evidence Unit and are not substituted for relational IDs.
type TextEntry struct {
	EvidenceID     string
	OrganizationID string
	SourceID       string
	SnapshotID     string

	ProjectionKind string
	ContentState   evidence.ContentState
	Content        string
	ContentHash    string
	Truncated      bool
	Classification evidence.Classification
	Persist        evidence.Decision

	SymbolName          string
	SymbolQualifiedName string
	ConfigurationKey    string
	ExceptionType       string
	ExactTerms          []string
}

// IncrementalTextEntry carries one current-snapshot textual row and, when
// Reuse is true, the previous canonical Evidence Unit identity from which the
// row may be copied forward. StableKey is a content-free provenance key from
// the incremental comparison; it is never used as a SQL identifier.
type IncrementalTextEntry struct {
	StableKey          string
	PreviousEvidenceID string
	Entry              TextEntry
	Reuse              bool
}

// Validate checks structural and authorization metadata without retaining or
// returning Content in an error. Omitted or denied entries are valid inputs
// and are filtered before they reach a projection store.
func (e TextEntry) Validate() error {
	for name, value := range map[string]string{
		"evidence_id": e.EvidenceID, "organization_id": e.OrganizationID,
		"source_id": e.SourceID, "snapshot_id": e.SnapshotID,
	} {
		if err := validateTextUUID(name, value); err != nil {
			return err
		}
	}
	if err := e.ContentState.Validate(); err != nil {
		return fmt.Errorf("%w: content state", ErrInvalidTextProjection)
	}
	if err := e.Persist.Validate(); err != nil {
		return fmt.Errorf("%w: persist decision", ErrInvalidTextProjection)
	}
	if err := e.Classification.Validate(); err != nil {
		return fmt.Errorf("%w: classification", ErrInvalidTextProjection)
	}
	if !isLowerSHA256(e.ContentHash) {
		return fmt.Errorf("%w: content hash", ErrInvalidTextProjection)
	}
	if !utf8.ValidString(e.Content) {
		return fmt.Errorf("%w: content encoding", ErrInvalidTextProjection)
	}
	if utf8.RuneCountInString(e.Content) > MaxTextProjectionCharacters {
		return fmt.Errorf("%w: content exceeds bounded projection size", ErrInvalidTextProjection)
	}
	switch e.ContentState {
	case evidence.ContentStatePresent:
		if strings.TrimSpace(e.Content) == "" {
			return fmt.Errorf("%w: present content is empty", ErrInvalidTextProjection)
		}
		if e.Persist != evidence.DecisionAllow {
			return fmt.Errorf("%w: present content is not authorized for persistence", ErrInvalidTextProjection)
		}
		if e.Classification != evidence.ClassificationUnknown && e.Classification != evidence.ClassificationSafeText {
			return fmt.Errorf("%w: present content has restricted classification", ErrInvalidTextProjection)
		}
		if evidence.ContentDigest(e.Content) != e.ContentHash {
			return fmt.Errorf("%w: content hash does not match content", ErrInvalidTextProjection)
		}
	case evidence.ContentStateRedacted:
		if e.Content != evidence.RedactedContent {
			return fmt.Errorf("%w: redacted representation is invalid", ErrInvalidTextProjection)
		}
		if e.Persist == evidence.DecisionDeny {
			return fmt.Errorf("%w: denied content cannot be redacted into the projection", ErrInvalidTextProjection)
		}
	case evidence.ContentStateOmitted:
		if e.Content != "" {
			return fmt.Errorf("%w: omitted content is not empty", ErrInvalidTextProjection)
		}
	}
	if e.Persist == evidence.DecisionDeny && (e.ContentState != evidence.ContentStateOmitted || e.Content != "") {
		return fmt.Errorf("%w: denied persistence cannot carry content", ErrInvalidTextProjection)
	}
	if (e.Classification == evidence.ClassificationBinary || e.Classification == evidence.ClassificationInvalid || e.Classification == evidence.ClassificationProhibited) && e.ContentState != evidence.ContentStateOmitted {
		return fmt.Errorf("%w: restricted content must be omitted", ErrInvalidTextProjection)
	}
	if e.ProjectionKind != "" {
		switch e.ProjectionKind {
		case "generic", "symbol", "configuration", "exception":
		default:
			return fmt.Errorf("%w: projection kind", ErrInvalidTextProjection)
		}
	}
	for _, term := range append(append(append(append(append([]string{}, e.ExactTerms...), e.SymbolName), e.SymbolQualifiedName), e.ConfigurationKey), e.ExceptionType) {
		if !utf8.ValidString(term) {
			return fmt.Errorf("%w: exact term encoding", ErrInvalidTextProjection)
		}
		if strings.ContainsRune(term, '\x00') {
			return fmt.Errorf("%w: exact term contains a NUL", ErrInvalidTextProjection)
		}
	}
	return nil
}

// Persistible reports whether this unit has an authorized representation that
// can be searched locally. Omitted and denied units are intentionally absent.
func (e TextEntry) Persistible() bool {
	switch e.ContentState {
	case evidence.ContentStatePresent:
		return e.Persist == evidence.DecisionAllow && e.Content != ""
	case evidence.ContentStateRedacted:
		return (e.Persist == evidence.DecisionAllow || e.Persist == evidence.DecisionRedact) && e.Content == evidence.RedactedContent
	default:
		return false
	}
}

// Normalize returns the deterministic representation used by SQL writes.
// Technical punctuation is retained in exact terms, while case and surrounding
// whitespace are normalized for reproducible matching.
func (e TextEntry) Normalize() (TextEntry, error) {
	if err := e.Validate(); err != nil {
		return TextEntry{}, err
	}
	if !e.Persistible() {
		return TextEntry{}, ErrTextNotPersistible
	}
	if e.ProjectionKind == "" {
		e.ProjectionKind = "generic"
	}
	e.SymbolName = normalizeExactField(e.SymbolName)
	e.SymbolQualifiedName = normalizeExactField(e.SymbolQualifiedName)
	e.ConfigurationKey = normalizeExactField(e.ConfigurationKey)
	e.ExceptionType = normalizeExactField(e.ExceptionType)
	terms := make([]string, 0, len(e.ExactTerms)+4)
	terms = append(terms, e.ExactTerms...)
	terms = append(terms, e.SymbolName, e.SymbolQualifiedName, e.ConfigurationKey, e.ExceptionType)
	for index, term := range terms {
		terms[index] = normalizeExactField(term)
	}
	sort.Strings(terms)
	terms = terms[:compactExactTerms(terms)]
	e.ExactTerms = terms
	return e, nil
}

func normalizeExactField(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func compactExactTerms(terms []string) int {
	write := 0
	for _, term := range terms {
		if term == "" {
			continue
		}
		if write == 0 || terms[write-1] != term {
			terms[write] = term
			write++
		}
	}
	return write
}

// SearchOptions carries the required organization scope and optional source
// and snapshot filters. Query text and the limit are always parameters to the
// adapter; no SQL fragment is built from them.
type SearchOptions struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
	Query          string
	Limit          int
}

func (o SearchOptions) Normalize() (SearchOptions, error) {
	if err := validateTextUUID("organization_id", o.OrganizationID); err != nil {
		return SearchOptions{}, err
	}
	if o.SourceID != "" {
		if err := validateTextUUID("source_id", o.SourceID); err != nil {
			return SearchOptions{}, err
		}
	}
	if o.SnapshotID != "" {
		if err := validateTextUUID("snapshot_id", o.SnapshotID); err != nil {
			return SearchOptions{}, err
		}
	}
	o.Query = strings.TrimSpace(o.Query)
	if o.Query == "" {
		return SearchOptions{}, fmt.Errorf("%w: query is required", ErrInvalidTextProjection)
	}
	if utf8.RuneCountInString(o.Query) > MaxTextProjectionCharacters {
		return SearchOptions{}, fmt.Errorf("%w: query is too long", ErrInvalidTextProjection)
	}
	if o.Limit == 0 {
		o.Limit = DefaultTextSearchLimit
	}
	if o.Limit < 1 || o.Limit > MaxTextSearchLimit {
		return SearchOptions{}, fmt.Errorf("%w: search limit is invalid", ErrInvalidTextProjection)
	}
	return o, nil
}

// ExactTermsForQuery returns lower-case whole and component technical terms.
// For example, OrderService.create yields both orderservice.create and its
// components, allowing exact symbol/configuration matches without SQL syntax.
func ExactTermsForQuery(query string) []string {
	terms := make([]string, 0, 4)
	for _, field := range strings.Fields(query) {
		whole := normalizeExactField(field)
		if whole == "" {
			continue
		}
		terms = append(terms, whole)
		for _, part := range strings.FieldsFunc(whole, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-'
		}) {
			if part != whole {
				terms = append(terms, part)
			}
		}
	}
	sort.Strings(terms)
	terms = terms[:compactExactTerms(terms)]
	return terms
}

// TextHit is a bounded result row returned by the textual projection.
type TextHit struct {
	EvidenceID          string
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	ProjectionKind      string
	ContentState        evidence.ContentState
	Content             string
	ContentHash         string
	Truncated           bool
	Classification      evidence.Classification
	SymbolName          string
	SymbolQualifiedName string
	ConfigurationKey    string
	ExceptionType       string
	ExactTerms          []string
	Rank                float64
	ExactMatch          bool
}

// TextStore is the narrow persistence port used by the retrieval layer.
type TextStore interface {
	RebuildSnapshot(context.Context, string, string, string, []TextEntry) error
	Search(context.Context, SearchOptions) ([]TextHit, error)
}

// IncrementalTextStore is the optional localized-update port. An adapter
// must delete/rebuild only the current snapshot while preserving the previous
// snapshot; omitted current entries represent removals and must not be copied
// forward.
type IncrementalTextStore interface {
	RebuildSnapshotIncremental(context.Context, string, string, string, string, []IncrementalTextEntry) error
}

// TextProjection validates retrieval inputs before delegating to a
// rebuildable persistence adapter. It has no source or database access itself.
type TextProjection struct {
	store TextStore
}

func NewTextProjection(store TextStore) *TextProjection {
	return &TextProjection{store: store}
}

func (p *TextProjection) RebuildSnapshot(ctx context.Context, organizationID, sourceID, snapshotID string, entries []TextEntry) error {
	if err := validateTextContext(ctx); err != nil {
		return err
	}
	if p == nil || p.store == nil {
		return ErrTextProjectionNotConfigured
	}
	if err := validateTextScope(organizationID, sourceID, snapshotID); err != nil {
		return err
	}
	prepared, err := prepareTextEntries(organizationID, sourceID, snapshotID, entries)
	if err != nil {
		return err
	}
	return p.store.RebuildSnapshot(ctx, organizationID, sourceID, snapshotID, prepared)
}

// RebuildSnapshotIncremental applies a deterministic localized update through
// an adapter that explicitly supports copy-forward. It refuses to silently
// fall back to a full rebuild because that could turn removed or incompatible
// identities into current knowledge.
func (p *TextProjection) RebuildSnapshotIncremental(ctx context.Context, organizationID, sourceID, previousSnapshotID, snapshotID string, entries []IncrementalTextEntry) error {
	if err := validateTextContext(ctx); err != nil {
		return err
	}
	if p == nil || p.store == nil {
		return ErrIncrementalTextNotConfigured
	}
	if err := validateTextScope(organizationID, sourceID, previousSnapshotID); err != nil {
		return err
	}
	if err := validateTextScope(organizationID, sourceID, snapshotID); err != nil {
		return err
	}
	if previousSnapshotID == snapshotID {
		return fmt.Errorf("%w: snapshots must be distinct", ErrInvalidTextProjection)
	}
	store, ok := p.store.(IncrementalTextStore)
	if !ok {
		return ErrIncrementalTextNotConfigured
	}
	prepared, err := prepareIncrementalTextEntries(organizationID, sourceID, snapshotID, entries)
	if err != nil {
		return err
	}
	return store.RebuildSnapshotIncremental(ctx, organizationID, sourceID, previousSnapshotID, snapshotID, prepared)
}

func (p *TextProjection) Search(ctx context.Context, options SearchOptions) ([]TextHit, error) {
	if err := validateTextContext(ctx); err != nil {
		return nil, err
	}
	if p == nil || p.store == nil {
		return nil, ErrTextProjectionNotConfigured
	}
	normalized, err := options.Normalize()
	if err != nil {
		return nil, err
	}
	return p.store.Search(ctx, normalized)
}

func prepareTextEntries(organizationID, sourceID, snapshotID string, entries []TextEntry) ([]TextEntry, error) {
	prepared := make([]TextEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		if entry.OrganizationID != organizationID || entry.SourceID != sourceID || entry.SnapshotID != snapshotID {
			return nil, fmt.Errorf("%w: entry scope mismatch", ErrInvalidTextProjection)
		}
		if !entry.Persistible() {
			continue
		}
		normalized, err := entry.Normalize()
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized.EvidenceID]; exists {
			return nil, fmt.Errorf("%w: duplicate evidence id", ErrInvalidTextProjection)
		}
		seen[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].EvidenceID < prepared[j].EvidenceID })
	return prepared, nil
}

func prepareIncrementalTextEntries(organizationID, sourceID, snapshotID string, entries []IncrementalTextEntry) ([]IncrementalTextEntry, error) {
	prepared := make([]IncrementalTextEntry, 0, len(entries))
	seenKeys := make(map[string]struct{}, len(entries))
	seenEvidence := make(map[string]struct{}, len(entries))
	for _, candidate := range entries {
		if strings.TrimSpace(candidate.StableKey) == "" {
			return nil, fmt.Errorf("%w: incremental stable key is required", ErrInvalidTextProjection)
		}
		if _, exists := seenKeys[candidate.StableKey]; exists {
			return nil, fmt.Errorf("%w: duplicate incremental stable key", ErrInvalidTextProjection)
		}
		seenKeys[candidate.StableKey] = struct{}{}
		entry := candidate.Entry
		if err := entry.Validate(); err != nil {
			return nil, err
		}
		if entry.OrganizationID != organizationID || entry.SourceID != sourceID || entry.SnapshotID != snapshotID {
			return nil, fmt.Errorf("%w: entry scope mismatch", ErrInvalidTextProjection)
		}
		if !entry.Persistible() {
			continue
		}
		normalized, err := entry.Normalize()
		if err != nil {
			return nil, err
		}
		if _, exists := seenEvidence[normalized.EvidenceID]; exists {
			return nil, fmt.Errorf("%w: duplicate evidence id", ErrInvalidTextProjection)
		}
		if candidate.Reuse {
			if err := validateTextUUID("previous_evidence_id", candidate.PreviousEvidenceID); err != nil {
				return nil, err
			}
		} else if candidate.PreviousEvidenceID != "" {
			if err := validateTextUUID("previous_evidence_id", candidate.PreviousEvidenceID); err != nil {
				return nil, err
			}
		}
		candidate.Entry = normalized
		prepared = append(prepared, candidate)
		seenEvidence[normalized.EvidenceID] = struct{}{}
	}
	sort.Slice(prepared, func(i, j int) bool { return prepared[i].Entry.EvidenceID < prepared[j].Entry.EvidenceID })
	return prepared, nil
}

func validateTextScope(organizationID, sourceID, snapshotID string) error {
	for name, value := range map[string]string{
		"organization_id": organizationID, "source_id": sourceID, "snapshot_id": snapshotID,
	} {
		if err := validateTextUUID(name, value); err != nil {
			return err
		}
	}
	return nil
}

func validateTextUUID(field, value string) error {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return fmt.Errorf("%w: %s must be a uuid", ErrInvalidTextProjection, field)
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return fmt.Errorf("%w: %s must be a uuid", ErrInvalidTextProjection, field)
		}
	}
	return nil
}

func isLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateTextContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidTextProjection)
	}
	return ctx.Err()
}
