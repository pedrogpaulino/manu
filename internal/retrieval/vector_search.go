package retrieval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultVectorSearchLimit is the bounded default for an exact vector
	// search. The exact projection deliberately does not expose an unbounded
	// query path.
	DefaultVectorSearchLimit = 20
	// MaxVectorSearchLimit prevents a caller from turning one query into an
	// unbounded transfer of evidence identities.
	MaxVectorSearchLimit = 1000
)

var (
	// ErrInvalidVectorSearch identifies malformed search input or a malformed
	// row returned by a vector projection adapter.
	ErrInvalidVectorSearch = errors.New("retrieval: invalid vector search input")
	// ErrVectorSearchProfileMismatch identifies a result or query that mixes
	// immutable embedding profiles.
	ErrVectorSearchProfileMismatch = errors.New("retrieval: vector search profile mismatch")
	// ErrVectorSearchScopeMismatch identifies a result outside the explicit
	// organization/source/snapshot boundary.
	ErrVectorSearchScopeMismatch = errors.New("retrieval: vector search scope mismatch")
	// ErrVectorSearchNotConfigured identifies a missing exact-search port.
	ErrVectorSearchNotConfigured = errors.New("retrieval: vector search store is not configured")
)

// VectorSearchQuery is the complete, immutable scope of one exact cosine
// search. All three scope identifiers are mandatory. Profile is a value (not
// a provider/model string) so a query cannot accidentally combine vectors
// from more than one immutable EmbeddingProfile.
type VectorSearchQuery struct {
	OrganizationID string
	SourceID       string
	SnapshotID     string
	Profile        EmbeddingProfile
	Vector         []float32
	Limit          int
}

// VectorSearchOptions is an explicit alias for callers that use the options
// terminology used by the textual projection.
type VectorSearchOptions = VectorSearchQuery

// Normalize validates and defensively copies a query before it crosses the
// retrieval/persistence boundary. Query vectors are never included in an
// error or telemetry record.
func (q VectorSearchQuery) Normalize() (VectorSearchQuery, error) {
	scope, err := (EmbeddingScope{
		OrganizationID: q.OrganizationID,
		SourceID:       q.SourceID,
		SnapshotID:     q.SnapshotID,
	}).Normalize()
	if err != nil {
		return VectorSearchQuery{}, fmt.Errorf("%w: scope: %v", ErrInvalidVectorSearch, err)
	}
	profile, err := q.Profile.Normalize()
	if err != nil {
		return VectorSearchQuery{}, fmt.Errorf("%w: profile: %v", ErrInvalidVectorSearch, err)
	}
	if profile.OrganizationID != scope.OrganizationID {
		return VectorSearchQuery{}, fmt.Errorf("%w: profile organization differs from scope", ErrVectorSearchProfileMismatch)
	}
	if err := validateEmbeddingVector(q.Vector, profile.Dimension); err != nil {
		return VectorSearchQuery{}, fmt.Errorf("%w: query vector: %v", ErrInvalidVectorSearch, err)
	}
	if q.Limit == 0 {
		q.Limit = DefaultVectorSearchLimit
	}
	if q.Limit < 1 || q.Limit > MaxVectorSearchLimit {
		return VectorSearchQuery{}, fmt.Errorf("%w: search limit is invalid", ErrInvalidVectorSearch)
	}
	q.OrganizationID = scope.OrganizationID
	q.SourceID = scope.SourceID
	q.SnapshotID = scope.SnapshotID
	q.Profile = profile
	q.Vector = append([]float32(nil), q.Vector...)
	return q, nil
}

// VectorProvenance is the stable scope and Evidence Unit identity carried by
// a vector hit. It intentionally contains no source content.
type VectorProvenance struct {
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	EvidenceID          string
	EvidenceContentHash string
}

// VectorHit is a ranked result of an exact cosine search. The full immutable
// profile is retained alongside the provider/model fields selected by the
// adapter so downstream ranking and evidence packaging can audit precisely
// which projection produced the result.
type VectorHit struct {
	EvidenceID          string
	OrganizationID      string
	SourceID            string
	SnapshotID          string
	ProfileID           string
	Profile             EmbeddingProfile
	Provider            string
	Model               string
	ProfileDimension    int
	EvidenceContentHash string
	Distance            float64
	Similarity          float64
	Rank                int
	Provenance          VectorProvenance
}

// Normalize validates a result against the query's single profile and full
// scope. A zero provenance is filled from the canonical result identity; a
// non-zero provenance must agree exactly. Similarity is derived from the
// database's cosine distance rather than trusted from a caller.
func (h VectorHit) Normalize(query VectorSearchQuery) (VectorHit, error) {
	normalizedQuery, err := query.Normalize()
	if err != nil {
		return VectorHit{}, err
	}
	h.EvidenceID = strings.ToLower(strings.TrimSpace(h.EvidenceID))
	h.OrganizationID = strings.ToLower(strings.TrimSpace(h.OrganizationID))
	h.SourceID = strings.ToLower(strings.TrimSpace(h.SourceID))
	h.SnapshotID = strings.ToLower(strings.TrimSpace(h.SnapshotID))
	h.ProfileID = strings.ToLower(strings.TrimSpace(h.ProfileID))
	h.Provider = strings.TrimSpace(h.Provider)
	h.Model = strings.TrimSpace(h.Model)
	h.EvidenceContentHash = strings.ToLower(strings.TrimSpace(h.EvidenceContentHash))
	for name, value := range map[string]string{
		"evidence_id":     h.EvidenceID,
		"organization_id": h.OrganizationID,
		"source_id":       h.SourceID,
		"snapshot_id":     h.SnapshotID,
		"profile_id":      h.ProfileID,
	} {
		if err := validateEmbeddingUUID(name, value); err != nil {
			return VectorHit{}, fmt.Errorf("%w: %v", ErrInvalidVectorSearch, err)
		}
	}
	if !isEmbeddingSHA256(h.EvidenceContentHash) {
		return VectorHit{}, fmt.Errorf("%w: evidence content hash is invalid", ErrInvalidVectorSearch)
	}
	if h.OrganizationID != normalizedQuery.OrganizationID ||
		h.SourceID != normalizedQuery.SourceID ||
		h.SnapshotID != normalizedQuery.SnapshotID {
		return VectorHit{}, fmt.Errorf("%w: result is outside requested scope", ErrVectorSearchScopeMismatch)
	}
	if h.ProfileID != normalizedQuery.Profile.ID || h.ProfileDimension != normalizedQuery.Profile.Dimension {
		return VectorHit{}, fmt.Errorf("%w: result profile or dimension differs", ErrVectorSearchProfileMismatch)
	}
	if h.Provider != normalizedQuery.Profile.Provider || h.Model != normalizedQuery.Profile.Model {
		return VectorHit{}, fmt.Errorf("%w: result model differs", ErrVectorSearchProfileMismatch)
	}
	if !isFiniteFloat64(h.Distance) || h.Distance < 0 || h.Distance > 2 {
		return VectorHit{}, fmt.Errorf("%w: cosine distance is invalid", ErrInvalidVectorSearch)
	}
	h.Profile = normalizedQuery.Profile
	h.Similarity = 1 - h.Distance
	if !isFiniteFloat64(h.Similarity) {
		return VectorHit{}, fmt.Errorf("%w: cosine similarity is invalid", ErrInvalidVectorSearch)
	}
	expectedProvenance := VectorProvenance{
		OrganizationID:      h.OrganizationID,
		SourceID:            h.SourceID,
		SnapshotID:          h.SnapshotID,
		EvidenceID:          h.EvidenceID,
		EvidenceContentHash: h.EvidenceContentHash,
	}
	if h.Provenance == (VectorProvenance{}) {
		h.Provenance = expectedProvenance
	} else {
		h.Provenance.OrganizationID = strings.ToLower(strings.TrimSpace(h.Provenance.OrganizationID))
		h.Provenance.SourceID = strings.ToLower(strings.TrimSpace(h.Provenance.SourceID))
		h.Provenance.SnapshotID = strings.ToLower(strings.TrimSpace(h.Provenance.SnapshotID))
		h.Provenance.EvidenceID = strings.ToLower(strings.TrimSpace(h.Provenance.EvidenceID))
		h.Provenance.EvidenceContentHash = strings.ToLower(strings.TrimSpace(h.Provenance.EvidenceContentHash))
		if h.Provenance != expectedProvenance {
			return VectorHit{}, fmt.Errorf("%w: result provenance differs", ErrVectorSearchScopeMismatch)
		}
	}
	if h.Rank < 0 {
		return VectorHit{}, fmt.Errorf("%w: result rank is invalid", ErrInvalidVectorSearch)
	}
	return h, nil
}

// VectorSearchTelemetry contains only aggregate, non-sensitive measurements.
// It deliberately omits the question, vector, content, credentials and SQL
// diagnostics.
type VectorSearchTelemetry struct {
	Latency        time.Duration
	ResultCount    int
	RequestedLimit int
	ProfileID      string
}

// VectorSearchResponse is the exact-search result plus safe aggregate
// telemetry. Hits are sorted by ascending cosine distance and evidence ID.
type VectorSearchResponse struct {
	Hits      []VectorHit
	Telemetry VectorSearchTelemetry
}

// VectorSearchStore is the narrow persistence port for exact vector search.
type VectorSearchStore interface {
	Search(context.Context, VectorSearchQuery) (VectorSearchResponse, error)
}

// VectorStore is a compatibility alias for callers that name the projection
// by its storage role.
type VectorStore = VectorSearchStore

// VectorProjection validates the exact-search boundary and does not know the
// source filesystem, SQL, providers or AI models.
type VectorProjection struct {
	store VectorSearchStore
}

// NewVectorProjection creates a retrieval boundary around an exact vector
// store.
func NewVectorProjection(store VectorSearchStore) *VectorProjection {
	return &VectorProjection{store: store}
}

// NewVectorSearch is an explicit constructor alias for callers that prefer
// the operation name.
func NewVectorSearch(store VectorSearchStore) *VectorProjection {
	return NewVectorProjection(store)
}

// Search executes and validates one exact vector search. It normalizes result
// ordering and fills telemetry fields even when a test or alternate adapter
// does not provide them.
func (p *VectorProjection) Search(ctx context.Context, query VectorSearchQuery) (VectorSearchResponse, error) {
	if err := validateVectorSearchContext(ctx); err != nil {
		return VectorSearchResponse{}, err
	}
	if p == nil || p.store == nil {
		return VectorSearchResponse{}, ErrVectorSearchNotConfigured
	}
	normalized, err := query.Normalize()
	if err != nil {
		return VectorSearchResponse{}, err
	}
	started := time.Now()
	response, err := p.store.Search(ctx, normalized)
	if err != nil {
		return VectorSearchResponse{}, err
	}
	hits, err := normalizeVectorHits(normalized, response.Hits)
	if err != nil {
		return VectorSearchResponse{}, err
	}
	response.Hits = hits
	response.Telemetry.Latency = time.Since(started)
	response.Telemetry.ResultCount = len(hits)
	response.Telemetry.RequestedLimit = normalized.Limit
	response.Telemetry.ProfileID = normalized.Profile.ID
	return response, nil
}

// SearchHits is a convenience view for consumers that only need ranked hits.
func (p *VectorProjection) SearchHits(ctx context.Context, query VectorSearchQuery) ([]VectorHit, error) {
	response, err := p.Search(ctx, query)
	if err != nil {
		return nil, err
	}
	return response.Hits, nil
}

func normalizeVectorHits(query VectorSearchQuery, hits []VectorHit) ([]VectorHit, error) {
	if len(hits) > query.Limit {
		return nil, fmt.Errorf("%w: store returned more hits than requested", ErrInvalidVectorSearch)
	}
	prepared := make([]VectorHit, 0, len(hits))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		normalized, err := hit.Normalize(query)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[normalized.EvidenceID]; exists {
			return nil, fmt.Errorf("%w: duplicate evidence result", ErrInvalidVectorSearch)
		}
		seen[normalized.EvidenceID] = struct{}{}
		prepared = append(prepared, normalized)
	}
	sort.SliceStable(prepared, func(i, j int) bool {
		if prepared[i].Distance != prepared[j].Distance {
			return prepared[i].Distance < prepared[j].Distance
		}
		return prepared[i].EvidenceID < prepared[j].EvidenceID
	})
	for index := range prepared {
		prepared[index].Rank = index + 1
	}
	return prepared, nil
}

func validateVectorSearchContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is nil", ErrInvalidVectorSearch)
	}
	return ctx.Err()
}

func isFiniteFloat64(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
