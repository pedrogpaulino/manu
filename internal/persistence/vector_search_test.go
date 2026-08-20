package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
	"github.com/pgvector/pgvector-go"
)

type vectorSearchFakeDatabase struct {
	rows     pgx.Rows
	query    string
	args     []any
	queryErr error
}

func (d *vectorSearchFakeDatabase) Begin(context.Context) (embeddingProjectionTransaction, error) {
	return nil, errors.New("vector search fake does not support writes")
}

func (d *vectorSearchFakeDatabase) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	d.query = query
	d.args = append([]any(nil), args...)
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	return d.rows, nil
}

type vectorSearchFakeRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func (r *vectorSearchFakeRows) Close() { r.closed = true }

func (r *vectorSearchFakeRows) Err() error { return r.err }

func (r *vectorSearchFakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.NewCommandTag("SELECT 0")
}

func (r *vectorSearchFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (r *vectorSearchFakeRows) Next() bool {
	if r.closed || r.index >= len(r.values) {
		r.closed = true
		return false
	}
	return true
}

func (r *vectorSearchFakeRows) Scan(dest ...any) error {
	if r.closed || r.index >= len(r.values) {
		return errors.New("scan outside vector search fake row")
	}
	values := r.values[r.index]
	r.index++
	if r.index >= len(r.values) {
		r.closed = true
	}
	if len(values) != len(dest) {
		return fmt.Errorf("fake scan values = %d, destinations = %d", len(values), len(dest))
	}
	for index := range values {
		if err := assignVectorSearchValue(values[index], dest[index]); err != nil {
			return fmt.Errorf("fake scan value %d: %w", index, err)
		}
	}
	return nil
}

func (r *vectorSearchFakeRows) Values() ([]any, error) {
	if r.closed || r.index >= len(r.values) {
		return nil, errors.New("values outside vector search fake row")
	}
	return r.values[r.index], nil
}

func (r *vectorSearchFakeRows) RawValues() [][]byte { return nil }

func (r *vectorSearchFakeRows) Conn() *pgx.Conn { return nil }

func assignVectorSearchValue(value any, dest any) error {
	switch target := dest.(type) {
	case *string:
		converted, ok := value.(string)
		if !ok {
			return fmt.Errorf("want string, got %T", value)
		}
		*target = converted
	case *int:
		converted, ok := value.(int)
		if !ok {
			return fmt.Errorf("want int, got %T", value)
		}
		*target = converted
	case *float64:
		converted, ok := value.(float64)
		if !ok {
			return fmt.Errorf("want float64, got %T", value)
		}
		*target = converted
	default:
		return fmt.Errorf("unsupported fake scan destination %T", dest)
	}
	return nil
}

func vectorSearchPersistenceQuery() retrieval.VectorSearchQuery {
	profile := embeddingPersistenceProfile()
	scope := embeddingPersistenceScope()
	return retrieval.VectorSearchQuery{
		OrganizationID: scope.OrganizationID,
		SourceID:       scope.SourceID,
		SnapshotID:     scope.SnapshotID,
		Profile:        profile,
		Vector:         []float32{1, 0, 0},
		Limit:          2,
	}
}

func vectorSearchPersistenceRow(query retrieval.VectorSearchQuery, evidenceNumber int, distance float64) []any {
	return []any{
		embeddingPersistenceUUID(200 + evidenceNumber),
		query.OrganizationID,
		query.SourceID,
		query.SnapshotID,
		query.Profile.ID,
		query.Profile.Provider,
		query.Profile.Model,
		query.Profile.Dimension,
		evidence.ContentDigest(fmt.Sprintf("vector-search-%d", evidenceNumber)),
		distance,
	}
}

func TestEmbeddingProjectionStoreSearchUsesExactCosineSQLAndFullScope(t *testing.T) {
	query := vectorSearchPersistenceQuery()
	db := &vectorSearchFakeDatabase{rows: &vectorSearchFakeRows{values: [][]any{
		vectorSearchPersistenceRow(query, 1, 0.125),
		vectorSearchPersistenceRow(query, 2, 0.125),
	}}}
	store := newEmbeddingProjectionStore(db)
	response, err := store.Search(context.Background(), query)
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(response.Hits) != 2 || response.Hits[0].Rank != 1 || response.Hits[1].Rank != 2 {
		t.Fatalf("response hits = %+v", response.Hits)
	}
	if response.Hits[0].EvidenceID >= response.Hits[1].EvidenceID {
		t.Fatalf("tie ordering = %q >= %q", response.Hits[0].EvidenceID, response.Hits[1].EvidenceID)
	}
	if response.Hits[0].Provenance.OrganizationID != query.OrganizationID || response.Hits[0].Provenance.EvidenceID != response.Hits[0].EvidenceID {
		t.Fatalf("provenance = %+v", response.Hits[0].Provenance)
	}
	if response.Hits[0].Profile.ID != query.Profile.ID || response.Hits[0].Provider != query.Profile.Provider || response.Hits[0].Model != query.Profile.Model {
		t.Fatalf("profile metadata = %+v", response.Hits[0])
	}
	if response.Telemetry.ResultCount != 2 || response.Telemetry.RequestedLimit != query.Limit || response.Telemetry.ProfileID != query.Profile.ID || response.Telemetry.Latency < 0 {
		t.Fatalf("telemetry = %+v", response.Telemetry)
	}
	for _, predicate := range []string{
		"ei.organization_id = $1::uuid",
		"ei.source_id = $2::uuid",
		"ei.snapshot_id = $3::uuid",
		"ei.profile_id = $4::uuid",
		"ei.profile_dimension = $5",
		"ei.state = 'ready'",
		"<=> $6::vector",
		"LIMIT $7",
		"ei.evidence_id ASC",
	} {
		if !strings.Contains(db.query, predicate) {
			t.Fatalf("SQL lacks %q: %s", predicate, db.query)
		}
	}
	if strings.Contains(strings.ToLower(db.query), "hnsw") || strings.Contains(strings.ToLower(db.query), "ivfflat") {
		t.Fatalf("exact query selected an approximate index: %s", db.query)
	}
	if len(db.args) != 7 || db.args[0] != query.OrganizationID || db.args[1] != query.SourceID || db.args[2] != query.SnapshotID || db.args[3] != query.Profile.ID || db.args[4] != query.Profile.Dimension || db.args[6] != query.Limit {
		t.Fatalf("SQL args = %#v", db.args)
	}
	vector, ok := db.args[5].(pgvector.Vector)
	if !ok || !reflect.DeepEqual(vector.Slice(), query.Vector) {
		t.Fatalf("query vector argument = %#v", db.args[5])
	}
	if strings.Contains(db.query, query.OrganizationID) || strings.Contains(db.query, query.SourceID) || strings.Contains(db.query, query.SnapshotID) {
		t.Fatal("scope was interpolated into SQL")
	}
}

func TestEmbeddingProjectionStoreSearchRejectsMalformedOrMixedRows(t *testing.T) {
	query := vectorSearchPersistenceQuery()
	tests := []struct {
		name   string
		modify func([]any)
		want   error
	}{
		{name: "wrong organization", modify: func(row []any) { row[1] = embeddingPersistenceUUID(999) }, want: retrieval.ErrVectorSearchScopeMismatch},
		{name: "wrong profile", modify: func(row []any) { row[4] = embeddingPersistenceUUID(999) }, want: retrieval.ErrVectorSearchProfileMismatch},
		{name: "wrong provider", modify: func(row []any) { row[5] = "other-provider" }, want: retrieval.ErrVectorSearchProfileMismatch},
		{name: "wrong dimension", modify: func(row []any) { row[7] = query.Profile.Dimension + 1 }, want: retrieval.ErrVectorSearchProfileMismatch},
		{name: "nan distance", modify: func(row []any) { row[9] = math.NaN() }, want: retrieval.ErrInvalidVectorSearch},
		{name: "infinite distance", modify: func(row []any) { row[9] = math.Inf(1) }, want: retrieval.ErrInvalidVectorSearch},
		{name: "bad hash", modify: func(row []any) { row[8] = "not-a-hash" }, want: retrieval.ErrInvalidVectorSearch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := vectorSearchPersistenceRow(query, 1, 0.25)
			test.modify(row)
			db := &vectorSearchFakeDatabase{rows: &vectorSearchFakeRows{values: [][]any{row}}}
			_, err := newEmbeddingProjectionStore(db).Search(context.Background(), query)
			if !errors.Is(err, test.want) {
				t.Fatalf("Search() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEmbeddingProjectionStoreSearchSanitizesDatabaseErrorAndContext(t *testing.T) {
	query := vectorSearchPersistenceQuery()
	secret := "Bearer vector-secret"
	db := &vectorSearchFakeDatabase{queryErr: fmt.Errorf("driver detail: %s", secret)}
	_, err := newEmbeddingProjectionStore(db).Search(context.Background(), query)
	if !errors.Is(err, ErrDatabase) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "driver detail") {
		t.Fatalf("sanitized database error = %v", err)
	}
	if _, err := newEmbeddingProjectionStore(db).Search(nil, query); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("nil context error = %v", err)
	}
}
