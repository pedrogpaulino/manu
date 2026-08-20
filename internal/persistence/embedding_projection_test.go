package persistence

import (
	"context"
	"encoding/json"
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

type embeddingFakeDatabase struct {
	tx         *embeddingFakeTransaction
	beginErr   error
	beginCalls int
	queryRows  pgx.Rows
	queryErr   error
	query      string
	queryArgs  []any
}

func (d *embeddingFakeDatabase) Begin(_ context.Context) (embeddingProjectionTransaction, error) {
	d.beginCalls++
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	return d.tx, nil
}

func (d *embeddingFakeDatabase) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	d.query = query
	d.queryArgs = append([]any(nil), args...)
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	if d.queryRows == nil {
		return &embeddingFakeRows{}, nil
	}
	return d.queryRows, nil
}

type embeddingFakeTransaction struct {
	execs         []fakeExec
	queries       []string
	queryArgs     [][]any
	queryRows     []pgx.Rows
	queryIndex    int
	execErr       error
	execErrAt     int
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (tx *embeddingFakeTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, fakeExec{query: query, args: append([]any(nil), args...)})
	if tx.execErr != nil && (tx.execErrAt == 0 || tx.execErrAt == len(tx.execs)) {
		return pgconn.CommandTag{}, tx.execErr
	}
	return pgconn.NewCommandTag("INSERT 1"), nil
}

func (tx *embeddingFakeTransaction) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.queries = append(tx.queries, query)
	tx.queryArgs = append(tx.queryArgs, append([]any(nil), args...))
	if tx.queryIndex >= len(tx.queryRows) {
		return &embeddingFakeRows{}, nil
	}
	rows := tx.queryRows[tx.queryIndex]
	tx.queryIndex++
	return rows, nil
}

func (tx *embeddingFakeTransaction) Commit(context.Context) error {
	tx.commitCalls++
	return tx.commitErr
}

func (tx *embeddingFakeTransaction) Rollback(context.Context) error {
	tx.rollbackCalls++
	return tx.rollbackErr
}

type embeddingFakeRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func (r *embeddingFakeRows) Close() { r.closed = true }

func (r *embeddingFakeRows) Err() error { return r.err }

func (r *embeddingFakeRows) CommandTag() pgconn.CommandTag { return pgconn.NewCommandTag("SELECT 0") }

func (r *embeddingFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (r *embeddingFakeRows) Next() bool {
	if r.closed || r.index >= len(r.values) {
		r.closed = true
		return false
	}
	return true
}

func (r *embeddingFakeRows) Scan(dest ...any) error {
	if r.closed || r.index >= len(r.values) {
		return errors.New("scan outside embedding fake row")
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
		if err := assignEmbeddingValue(values[index], dest[index]); err != nil {
			return fmt.Errorf("fake scan value %d: %w", index, err)
		}
	}
	return nil
}

func (r *embeddingFakeRows) Values() ([]any, error) {
	if r.closed || r.index >= len(r.values) {
		return nil, errors.New("values outside embedding fake row")
	}
	return r.values[r.index], nil
}

func (r *embeddingFakeRows) RawValues() [][]byte { return nil }

func (r *embeddingFakeRows) Conn() *pgx.Conn { return nil }

func assignEmbeddingValue(value any, dest any) error {
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
	case *[]byte:
		switch converted := value.(type) {
		case []byte:
			*target = append((*target)[:0], converted...)
		case string:
			*target = []byte(converted)
		default:
			return fmt.Errorf("want bytes, got %T", value)
		}
	case *json.RawMessage:
		switch converted := value.(type) {
		case []byte:
			*target = append((*target)[:0], converted...)
		case string:
			*target = append((*target)[:0], converted...)
		default:
			return fmt.Errorf("want JSON, got %T", value)
		}
	case *pgvector.Vector:
		switch converted := value.(type) {
		case pgvector.Vector:
			*target = converted
		case string:
			if err := target.Scan(converted); err != nil {
				return err
			}
		default:
			return fmt.Errorf("want vector, got %T", value)
		}
	default:
		return fmt.Errorf("unsupported fake scan destination %T", dest)
	}
	return nil
}

func embeddingPersistenceUUID(number int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", number)
}

func embeddingPersistenceProfile() retrieval.EmbeddingProfile {
	return retrieval.EmbeddingProfile{
		ID:                   embeddingPersistenceUUID(1),
		OrganizationID:       embeddingPersistenceUUID(2),
		Provider:             "simulated",
		Model:                "embed-v1",
		Dimension:            3,
		Normalization:        "l2",
		ConfigurationVersion: "v1",
		ConfigurationDigest:  evidence.ContentDigest("{}"),
		Configuration:        json.RawMessage(`{}`),
	}
}

func embeddingPersistenceScope() retrieval.EmbeddingScope {
	return retrieval.EmbeddingScope{OrganizationID: embeddingPersistenceUUID(2), SourceID: embeddingPersistenceUUID(3), SnapshotID: embeddingPersistenceUUID(4)}
}

func embeddingPersistenceInput(number int, vector []float32) retrieval.EmbeddingInput {
	return retrieval.EmbeddingInput{
		ID:                  embeddingPersistenceUUID(100 + number),
		EvidenceID:          embeddingPersistenceUUID(200 + number),
		EvidenceContentHash: evidence.ContentDigest(fmt.Sprintf("evidence-%d", number)),
		Vector:              vector,
	}
}

func embeddingProfileRow(profile retrieval.EmbeddingProfile) []any {
	return []any{profile.ID, profile.OrganizationID, profile.Provider, profile.Model,
		profile.Dimension, profile.Normalization, profile.ConfigurationVersion,
		profile.ConfigurationDigest, []byte(profile.Configuration)}
}

func embeddingCacheRow(evidenceID, hash string, dimension int, vector []float32) []any {
	return []any{evidenceID, hash, dimension, pgvector.NewVector(vector)}
}

func embeddingItemRow(item retrieval.EmbeddingItem) []any {
	return []any{item.ID, item.OrganizationID, item.ProfileID, item.ProfileDimension,
		item.SourceID, item.SnapshotID, item.EvidenceID, item.EvidenceContentHash,
		pgvector.NewVector(item.Vector), item.State}
}

func TestEmbeddingProjectionStoreEnsuresImmutableProfile(t *testing.T) {
	profile := embeddingPersistenceProfile()
	tx := &embeddingFakeTransaction{queryRows: []pgx.Rows{
		&embeddingFakeRows{values: [][]any{embeddingProfileRow(profile)}},
	}}
	db := &embeddingFakeDatabase{tx: tx}
	store := newEmbeddingProjectionStore(db)
	got, err := store.EnsureProfile(context.Background(), profile)
	if err != nil {
		t.Fatalf("EnsureProfile() error = %v", err)
	}
	if got.ConfigurationDigest != profile.ConfigurationDigest || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("profile/transaction = %+v, commit %d, rollback %d", got, tx.commitCalls, tx.rollbackCalls)
	}

	conflict := profile
	conflict.Model = "different-model"
	tx2 := &embeddingFakeTransaction{queryRows: []pgx.Rows{
		&embeddingFakeRows{values: [][]any{embeddingProfileRow(profile)}},
	}}
	store2 := newEmbeddingProjectionStore(&embeddingFakeDatabase{tx: tx2})
	if _, err := store2.EnsureProfile(context.Background(), conflict); !errors.Is(err, ErrConflict) {
		t.Fatalf("EnsureProfile(conflict) error = %v, want ErrConflict", err)
	}
}

func TestEmbeddingProjectionStoreRebuildReusesHashCacheAndReportsMissing(t *testing.T) {
	profile := embeddingPersistenceProfile()
	scope := embeddingPersistenceScope()
	hit := embeddingPersistenceInput(1, nil)
	newItem := embeddingPersistenceInput(2, []float32{2, 3, 4})
	missing := embeddingPersistenceInput(3, nil)
	cacheRows := &embeddingFakeRows{values: [][]any{embeddingCacheRow(embeddingPersistenceUUID(999), hit.EvidenceContentHash, profile.Dimension, []float32{1, 2, 3})}}
	tx := &embeddingFakeTransaction{queryRows: []pgx.Rows{cacheRows}}
	db := &embeddingFakeDatabase{tx: tx}
	store := newEmbeddingProjectionStore(db)
	result, err := store.RebuildSnapshot(context.Background(), profile, scope, []retrieval.EmbeddingInput{missing, newItem, hit})
	if err != nil {
		t.Fatalf("RebuildSnapshot() error = %v", err)
	}
	if result.CacheHits != 1 || result.Inserted != 1 || len(result.Missing) != 1 || result.Missing[0].EvidenceID != missing.EvidenceID || len(result.Items) != 2 {
		t.Fatalf("rebuild result = %+v", result)
	}
	if result.Complete() || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("rebuild completion/transactions = %v/%d/%d", result.Complete(), tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.execs) != 3 || !strings.Contains(tx.execs[0].query, "DELETE FROM embedding_items") {
		t.Fatalf("exec order/count = %#v", tx.execs)
	}
	for _, predicate := range []string{"organization_id = $1::uuid", "profile_id = $2::uuid", "source_id = $3::uuid", "snapshot_id = $4::uuid"} {
		if !strings.Contains(tx.execs[0].query, predicate) {
			t.Fatalf("scoped rebuild clear lacks %q: %s", predicate, tx.execs[0].query)
		}
	}
	if strings.Contains(tx.execs[1].query, hit.EvidenceContentHash) || !strings.Contains(tx.execs[1].query, "$1::uuid") {
		t.Fatal("embedding insert interpolated identity or omitted placeholders")
	}
	if tx.execs[1].args[6] != hit.EvidenceID {
		t.Fatalf("cache hit moved the old evidence identity: inserted evidence = %v, want %q", tx.execs[1].args[6], hit.EvidenceID)
	}
	if strings.Contains(tx.queries[0], "<=>") || strings.Contains(tx.queries[0], "<->") {
		t.Fatal("rebuild cache query performed vector search")
	}
	if len(tx.queryArgs) != 1 || tx.queryArgs[0][0] != profile.OrganizationID || tx.queryArgs[0][1] != profile.ID {
		t.Fatalf("cache query crossed organization/profile scope: %#v", tx.queryArgs)
	}
	if got := tx.execs[1].args[8].(pgvector.Vector).Slice(); !reflect.DeepEqual(got, []float32{1, 2, 3}) {
		t.Fatalf("cached vector = %#v", got)
	}
}

func TestEmbeddingProjectionStoreRebuildIdempotentlyReusesExistingProfileCache(t *testing.T) {
	profile := embeddingPersistenceProfile()
	scope := embeddingPersistenceScope()
	first := embeddingPersistenceInput(1, []float32{1, 2, 3})
	second := embeddingPersistenceInput(2, []float32{2, 3, 4})
	cacheRows := &embeddingFakeRows{values: [][]any{
		embeddingCacheRow(embeddingPersistenceUUID(998), first.EvidenceContentHash, profile.Dimension, first.Vector),
		embeddingCacheRow(embeddingPersistenceUUID(997), second.EvidenceContentHash, profile.Dimension, second.Vector),
	}}
	tx := &embeddingFakeTransaction{queryRows: []pgx.Rows{cacheRows}}
	store := newEmbeddingProjectionStore(&embeddingFakeDatabase{tx: tx})
	result, err := store.RebuildSnapshot(context.Background(), profile, scope, []retrieval.EmbeddingInput{
		{ID: first.ID, EvidenceID: first.EvidenceID, EvidenceContentHash: first.EvidenceContentHash},
		{ID: second.ID, EvidenceID: second.EvidenceID, EvidenceContentHash: second.EvidenceContentHash},
	})
	if err != nil {
		t.Fatalf("RebuildSnapshot() error = %v", err)
	}
	if result.CacheHits != 2 || result.Inserted != 0 || !result.Complete() {
		t.Fatalf("idempotent result = %+v", result)
	}
}

func TestEmbeddingProjectionStoreIncrementalCopiesCompatibleRowsAndRebuildsAffected(t *testing.T) {
	profile := embeddingPersistenceProfile()
	scope := embeddingPersistenceScope()
	previousSnapshotID := embeddingPersistenceUUID(5)
	reused := embeddingPersistenceInput(1, nil)
	affected := embeddingPersistenceInput(2, []float32{2, 3, 4})
	cacheRows := &embeddingFakeRows{values: [][]any{
		embeddingCacheRow(embeddingPersistenceUUID(999), reused.EvidenceContentHash, profile.Dimension, []float32{1, 2, 3}),
	}}
	tx := &embeddingFakeTransaction{queryRows: []pgx.Rows{cacheRows}}
	store := newEmbeddingProjectionStore(&embeddingFakeDatabase{tx: tx})
	result, err := store.RebuildSnapshotIncremental(context.Background(), profile, scope, previousSnapshotID, []retrieval.IncrementalEmbeddingInput{
		{StableKey: "evidence-reused", Reuse: true, PreviousEvidenceID: reused.EvidenceID, Input: reused},
		{StableKey: "evidence-affected", Input: affected},
	})
	if err != nil {
		t.Fatalf("RebuildSnapshotIncremental() error = %v", err)
	}
	if result.Requested != 2 || result.CacheHits != 1 || result.Inserted != 1 || !result.Complete() || len(result.Items) != 2 {
		t.Fatalf("incremental result = %+v", result)
	}
	if len(tx.execs) != 3 || !strings.Contains(tx.execs[0].query, "DELETE FROM embedding_items") || !strings.Contains(tx.execs[1].query, "FROM embedding_items") {
		t.Fatalf("incremental execs = %#v", tx.execs)
	}
	copyArgs := tx.execs[1].args
	if copyArgs[7] != previousSnapshotID || copyArgs[8] != reused.EvidenceID || copyArgs[9] != reused.EvidenceContentHash {
		t.Fatalf("copy scope/hash args = %#v", copyArgs)
	}
	if strings.Contains(tx.execs[1].query, reused.EvidenceContentHash) {
		t.Fatal("copy query interpolated content hash")
	}
}

func TestEmbeddingProjectionStoreRollbackSanitizesErrorAndRejectsBadInput(t *testing.T) {
	profile := embeddingPersistenceProfile()
	scope := embeddingPersistenceScope()
	secret := "Bearer super-secret"
	tx := &embeddingFakeTransaction{execErr: &pgconn.PgError{Code: "23514", Message: "check", Detail: secret}, execErrAt: 2}
	store := newEmbeddingProjectionStore(&embeddingFakeDatabase{tx: tx})
	_, err := store.RebuildSnapshot(context.Background(), profile, scope, []retrieval.EmbeddingInput{embeddingPersistenceInput(1, []float32{1, 2, 3})})
	if !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("sanitized rebuild error = %v", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("rollback/commit = %d/%d", tx.rollbackCalls, tx.commitCalls)
	}

	bad := embeddingPersistenceInput(2, []float32{1, 2, 3})
	bad.Vector[1] = float32(math.NaN())
	db := &embeddingFakeDatabase{tx: &embeddingFakeTransaction{}}
	if _, err := newEmbeddingProjectionStore(db).RebuildSnapshot(context.Background(), profile, scope, []retrieval.EmbeddingInput{bad}); !errors.Is(err, retrieval.ErrInvalidEmbeddingProjection) {
		t.Fatalf("bad vector error = %v, want invalid embedding", err)
	}
	if db.beginCalls != 0 {
		t.Fatal("bad vector started a transaction")
	}
}

func TestEmbeddingProjectionStoreLookupIsScopedByProfileAndHash(t *testing.T) {
	profile := embeddingPersistenceProfile()
	item := retrieval.EmbeddingItem{
		ID: embeddingPersistenceUUID(101), OrganizationID: profile.OrganizationID, ProfileID: profile.ID,
		ProfileDimension: profile.Dimension, SourceID: embeddingPersistenceUUID(3), SnapshotID: embeddingPersistenceUUID(4),
		EvidenceID: embeddingPersistenceUUID(201), EvidenceContentHash: evidence.ContentDigest("lookup"),
		Vector: []float32{1, 2, 3}, State: "ready",
	}
	db := &embeddingFakeDatabase{queryRows: &embeddingFakeRows{values: [][]any{embeddingItemRow(item)}}}
	store := newEmbeddingProjectionStore(db)
	got, hit, err := store.Lookup(context.Background(), retrieval.EmbeddingCacheKey{OrganizationID: profile.OrganizationID, ProfileID: profile.ID, EvidenceContentHash: item.EvidenceContentHash})
	if err != nil || !hit || got.EvidenceID != item.EvidenceID {
		t.Fatalf("Lookup() = %+v, hit %v, error %v", got, hit, err)
	}
	if strings.Contains(db.query, item.EvidenceContentHash) || len(db.queryArgs) != 3 || db.queryArgs[0] != profile.OrganizationID || db.queryArgs[1] != profile.ID || db.queryArgs[2] != item.EvidenceContentHash {
		t.Fatalf("lookup query/args = %q/%#v", db.query, db.queryArgs)
	}

	dbMissing := &embeddingFakeDatabase{queryRows: &embeddingFakeRows{}}
	missingStore := newEmbeddingProjectionStore(dbMissing)
	if _, hit, err := missingStore.Lookup(context.Background(), retrieval.EmbeddingCacheKey{OrganizationID: profile.OrganizationID, ProfileID: embeddingPersistenceUUID(9), EvidenceContentHash: item.EvidenceContentHash}); err != nil || hit {
		t.Fatalf("cache miss = hit %v, error %v", hit, err)
	}
}

func TestNewEmbeddingProjectionStoreNilIsNotConfigured(t *testing.T) {
	store := NewEmbeddingProjectionStore(nil)
	if _, err := store.EnsureProfile(context.Background(), embeddingPersistenceProfile()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("EnsureProfile(nil) error = %v, want ErrInvalidInput", err)
	}
}
