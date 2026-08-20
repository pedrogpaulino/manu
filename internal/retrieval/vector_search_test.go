package retrieval

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/pedrogpaulino/manu/internal/evidence"
)

type vectorSearchRecordingStore struct {
	response VectorSearchResponse
	err      error
	query    VectorSearchQuery
	calls    int
}

func (s *vectorSearchRecordingStore) Search(_ context.Context, query VectorSearchQuery) (VectorSearchResponse, error) {
	s.calls++
	s.query = query
	return s.response, s.err
}

func vectorSearchTestQuery() VectorSearchQuery {
	profile := embeddingTestProfile()
	scope := embeddingTestScope()
	return VectorSearchQuery{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Profile:        profile,
		Vector:         []float32{1, 0, 0},
		Limit:          3,
	}
}

func vectorSearchTestHit(number int, distance float64) VectorHit {
	query := vectorSearchTestQuery()
	hash := evidence.ContentDigest("vector-evidence-" + string(rune('a'+number)))
	evidenceID := embeddingTestUUID(500 + number)
	return VectorHit{
		EvidenceID:          evidenceID,
		OrganizationID:      query.OrganizationID,
		SourceID:            query.SourceID,
		SnapshotID:          query.SnapshotID,
		ProfileID:           query.Profile.ID,
		Provider:            query.Profile.Provider,
		Model:               query.Profile.Model,
		ProfileDimension:    query.Profile.Dimension,
		EvidenceContentHash: hash,
		Distance:            distance,
	}
}

func TestVectorSearchQueryRejectsMissingScopeProfileDimensionAndNonFiniteValues(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*VectorSearchQuery)
	}{
		{name: "missing organization", modify: func(query *VectorSearchQuery) { query.OrganizationID = "" }},
		{name: "missing source", modify: func(query *VectorSearchQuery) { query.SourceID = "" }},
		{name: "missing snapshot", modify: func(query *VectorSearchQuery) { query.SnapshotID = "" }},
		{name: "missing profile", modify: func(query *VectorSearchQuery) { query.Profile = EmbeddingProfile{} }},
		{name: "wrong dimension", modify: func(query *VectorSearchQuery) { query.Vector = []float32{1, 0} }},
		{name: "nan", modify: func(query *VectorSearchQuery) { query.Vector[1] = float32(math.NaN()) }},
		{name: "positive infinity", modify: func(query *VectorSearchQuery) { query.Vector[1] = float32(math.Inf(1)) }},
		{name: "negative infinity", modify: func(query *VectorSearchQuery) { query.Vector[1] = float32(math.Inf(-1)) }},
		{name: "zero limit", modify: func(query *VectorSearchQuery) { query.Limit = -1 }},
		{name: "limit too large", modify: func(query *VectorSearchQuery) { query.Limit = MaxVectorSearchLimit + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := vectorSearchTestQuery()
			test.modify(&query)
			if _, err := query.Normalize(); !errors.Is(err, ErrInvalidVectorSearch) &&
				!errors.Is(err, ErrVectorSearchProfileMismatch) {
				t.Fatalf("Normalize() error = %v, want vector search validation error", err)
			}
		})
	}
}

func TestVectorSearchQueryRejectsProfileFromAnotherOrganization(t *testing.T) {
	query := vectorSearchTestQuery()
	query.Profile.OrganizationID = embeddingTestUUID(999)
	query.Profile.ConfigurationDigest = evidence.ContentDigest(string(query.Profile.Configuration))
	if _, err := query.Normalize(); !errors.Is(err, ErrVectorSearchProfileMismatch) {
		t.Fatalf("Normalize() error = %v, want profile mismatch", err)
	}
}

func TestVectorProjectionSortsTiesPreservesProvenanceAndTelemetry(t *testing.T) {
	query := vectorSearchTestQuery()
	first := vectorSearchTestHit(2, 0.25)
	second := vectorSearchTestHit(1, 0.25)
	third := vectorSearchTestHit(3, 0.75)
	store := &vectorSearchRecordingStore{response: VectorSearchResponse{Hits: []VectorHit{third, first, second}}}
	projection := NewVectorProjection(store)
	response, err := projection.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if got := []string{response.Hits[0].EvidenceID, response.Hits[1].EvidenceID, response.Hits[2].EvidenceID}; got[0] != second.EvidenceID || got[1] != first.EvidenceID || got[2] != third.EvidenceID {
		t.Fatalf("ordered evidence IDs = %v", got)
	}
	if response.Hits[0].Rank != 1 || response.Hits[1].Rank != 2 || response.Hits[2].Rank != 3 {
		t.Fatalf("ranks = %d/%d/%d", response.Hits[0].Rank, response.Hits[1].Rank, response.Hits[2].Rank)
	}
	if response.Hits[0].Similarity != 0.75 || response.Hits[0].Provenance.EvidenceID != second.EvidenceID {
		t.Fatalf("normalized result = %+v", response.Hits[0])
	}
	if response.Hits[0].Profile.ID != query.Profile.ID || response.Hits[0].Provider != query.Profile.Provider || response.Hits[0].Model != query.Profile.Model {
		t.Fatalf("profile provenance = %+v", response.Hits[0])
	}
	if response.Telemetry.ResultCount != 3 || response.Telemetry.RequestedLimit != query.Limit || response.Telemetry.ProfileID != query.Profile.ID || response.Telemetry.Latency < 0 {
		t.Fatalf("telemetry = %+v", response.Telemetry)
	}
	if store.calls != 1 || len(store.query.Vector) != len(query.Vector) {
		t.Fatalf("store query calls/vector = %d/%v", store.calls, store.query.Vector)
	}
}

func TestVectorProjectionRejectsOutOfScopeProfileAndMalformedResults(t *testing.T) {
	query := vectorSearchTestQuery()
	tests := []struct {
		name   string
		modify func(*VectorHit)
		want   error
	}{
		{name: "wrong scope", modify: func(hit *VectorHit) { hit.SourceID = embeddingTestUUID(999) }, want: ErrVectorSearchScopeMismatch},
		{name: "wrong profile", modify: func(hit *VectorHit) { hit.ProfileID = embeddingTestUUID(999) }, want: ErrVectorSearchProfileMismatch},
		{name: "wrong provider", modify: func(hit *VectorHit) { hit.Provider = "other" }, want: ErrVectorSearchProfileMismatch},
		{name: "nan distance", modify: func(hit *VectorHit) { hit.Distance = math.NaN() }, want: ErrInvalidVectorSearch},
		{name: "infinite distance", modify: func(hit *VectorHit) { hit.Distance = math.Inf(1) }, want: ErrInvalidVectorSearch},
		{name: "distance outside cosine range", modify: func(hit *VectorHit) { hit.Distance = 2.1 }, want: ErrInvalidVectorSearch},
		{name: "provenance mismatch", modify: func(hit *VectorHit) { hit.Provenance.EvidenceID = embeddingTestUUID(1000) }, want: ErrVectorSearchScopeMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hit := vectorSearchTestHit(1, 0.5)
			if test.name == "provenance mismatch" {
				hit.Provenance = VectorProvenance{EvidenceID: embeddingTestUUID(1000)}
			}
			test.modify(&hit)
			store := &vectorSearchRecordingStore{response: VectorSearchResponse{Hits: []VectorHit{hit}}}
			_, err := NewVectorProjection(store).Search(context.Background(), query)
			if !errors.Is(err, test.want) {
				t.Fatalf("Search() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestVectorProjectionRejectsDuplicateOrExcessResults(t *testing.T) {
	query := vectorSearchTestQuery()
	duplicate := vectorSearchTestHit(1, 0.1)
	store := &vectorSearchRecordingStore{response: VectorSearchResponse{Hits: []VectorHit{duplicate, duplicate}}}
	if _, err := NewVectorProjection(store).Search(context.Background(), query); !errors.Is(err, ErrInvalidVectorSearch) {
		t.Fatalf("duplicate result error = %v, want invalid vector search", err)
	}
	query.Limit = 1
	store.response = VectorSearchResponse{Hits: []VectorHit{vectorSearchTestHit(1, 0.1), vectorSearchTestHit(2, 0.2)}}
	if _, err := NewVectorProjection(store).Search(context.Background(), query); !errors.Is(err, ErrInvalidVectorSearch) {
		t.Fatalf("excess result error = %v, want invalid vector search", err)
	}
}

func TestVectorProjectionRejectsNilContextAndMissingStore(t *testing.T) {
	query := vectorSearchTestQuery()
	projection := NewVectorProjection(&vectorSearchRecordingStore{})
	if _, err := projection.Search(nil, query); !errors.Is(err, ErrInvalidVectorSearch) {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := NewVectorProjection(nil).Search(context.Background(), query); !errors.Is(err, ErrVectorSearchNotConfigured) {
		t.Fatalf("nil store error = %v", err)
	}
}
