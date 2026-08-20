package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pedrogpaulino/manu/internal/evidence"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

type textProjectionFakeDatabase struct {
	tx         *textProjectionFakeTransaction
	beginErr   error
	beginCalls int
	queryRows  pgx.Rows
	queryErr   error
	query      string
	queryArgs  []any
}

func (d *textProjectionFakeDatabase) Begin(_ context.Context) (textProjectionTransaction, error) {
	d.beginCalls++
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	return d.tx, nil
}

func (d *textProjectionFakeDatabase) Query(_ context.Context, query string, args ...any) (pgx.Rows, error) {
	d.query = query
	d.queryArgs = append([]any(nil), args...)
	if d.queryErr != nil {
		return nil, d.queryErr
	}
	if d.queryRows == nil {
		return &textProjectionFakeRows{}, nil
	}
	return d.queryRows, nil
}

type textProjectionFakeTransaction struct {
	execs         []fakeExec
	execErr       error
	execErrAt     int
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (tx *textProjectionFakeTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.execs = append(tx.execs, fakeExec{query: query, args: append([]any(nil), args...)})
	if tx.execErr != nil && (tx.execErrAt == 0 || tx.execErrAt == len(tx.execs)) {
		return pgconn.CommandTag{}, tx.execErr
	}
	return pgconn.NewCommandTag("INSERT 1"), nil
}

func (tx *textProjectionFakeTransaction) Commit(context.Context) error {
	tx.commitCalls++
	return tx.commitErr
}

func (tx *textProjectionFakeTransaction) Rollback(context.Context) error {
	tx.rollbackCalls++
	return tx.rollbackErr
}

type textProjectionFakeRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func (r *textProjectionFakeRows) Close() { r.closed = true }

func (r *textProjectionFakeRows) Err() error { return r.err }

func (r *textProjectionFakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.NewCommandTag("SELECT 0")
}

func (r *textProjectionFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (r *textProjectionFakeRows) Next() bool {
	if r.closed || r.index >= len(r.values) {
		r.closed = true
		return false
	}
	return true
}

func (r *textProjectionFakeRows) Scan(dest ...any) error {
	if r.closed || r.index >= len(r.values) {
		return errors.New("scan outside text projection fake row")
	}
	values := r.values[r.index]
	r.index++
	if r.index >= len(r.values) {
		r.closed = true
	}
	return assignTextProjectionValues(values, dest)
}

func (r *textProjectionFakeRows) Values() ([]any, error) {
	if r.closed || r.index >= len(r.values) {
		return nil, errors.New("values outside text projection fake row")
	}
	return r.values[r.index], nil
}

func (r *textProjectionFakeRows) RawValues() [][]byte { return nil }

func (r *textProjectionFakeRows) Conn() *pgx.Conn { return nil }

func assignTextProjectionValues(values []any, dest []any) error {
	if len(values) != len(dest) {
		return fmt.Errorf("fake scan values = %d, destinations = %d", len(values), len(dest))
	}
	for index := range values {
		switch target := dest[index].(type) {
		case *[]string:
			if values[index] == nil {
				*target = nil
				continue
			}
			converted, ok := values[index].([]string)
			if !ok {
				return fmt.Errorf("want []string, got %T", values[index])
			}
			*target = append((*target)[:0], converted...)
		case *float64:
			converted, ok := values[index].(float64)
			if !ok {
				return fmt.Errorf("want float64, got %T", values[index])
			}
			*target = converted
		default:
			if err := assignValue(values[index], dest[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func textProjectionEntry(id int) retrieval.TextEntry {
	content := fmt.Sprintf("OrderService.create %d", id)
	return retrieval.TextEntry{
		EvidenceID:          testUUID(id),
		OrganizationID:      testUUID(100),
		SourceID:            testUUID(101),
		SnapshotID:          testUUID(102),
		ProjectionKind:      "symbol",
		ContentState:        evidence.ContentStatePresent,
		Content:             content,
		ContentHash:         evidence.ContentDigest(content),
		Classification:      evidence.ClassificationSafeText,
		Persist:             evidence.DecisionAllow,
		SymbolName:          "create",
		SymbolQualifiedName: "OrderService.create",
	}
}

func TestTextProjectionStoreRebuildIsScopedAndAtomic(t *testing.T) {
	tx := &textProjectionFakeTransaction{}
	db := &textProjectionFakeDatabase{tx: tx}
	store := newTextProjectionStore(db)
	entry := textProjectionEntry(2)
	omitted := textProjectionEntry(3)
	omitted.ContentState = evidence.ContentStateOmitted
	omitted.Content = ""
	omitted.ContentHash = evidence.ContentDigest("")

	err := store.RebuildSnapshot(context.Background(), testUUID(100), testUUID(101), testUUID(102), []retrieval.TextEntry{omitted, entry})
	if err != nil {
		t.Fatalf("RebuildSnapshot() error = %v", err)
	}
	if db.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("transaction calls = begin %d, commit %d, rollback %d", db.beginCalls, tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.execs) != 2 {
		t.Fatalf("exec count = %d, want scoped clear plus one insert", len(tx.execs))
	}
	if !strings.Contains(tx.execs[0].query, "organization_id = $1") || !strings.Contains(tx.execs[0].query, "source_id = $2") || !strings.Contains(tx.execs[0].query, "snapshot_id = $3") {
		t.Fatalf("clear query is not fully scoped: %s", tx.execs[0].query)
	}
	if strings.Contains(tx.execs[1].query, entry.Content) || !strings.Contains(tx.execs[1].query, "$1") {
		t.Fatal("insert query interpolates content or lacks placeholders")
	}
	if got := tx.execs[1].args[0]; got != entry.EvidenceID {
		t.Fatalf("insert evidence ID arg = %v, want %q", got, entry.EvidenceID)
	}
	terms, ok := tx.execs[1].args[16].([]string)
	if !ok || !reflect.DeepEqual(terms, []string{"create", "orderservice.create"}) {
		t.Fatalf("insert exact terms = %#v", tx.execs[1].args[16])
	}
}

func TestTextProjectionStoreIncrementalCopiesOnlyMatchingCurrentIdentity(t *testing.T) {
	previousSnapshotID := testUUID(102)
	currentSnapshotID := testUUID(103)
	entry := textProjectionEntry(2)
	entry.SnapshotID = currentSnapshotID
	previousEvidenceID := testUUID(202)
	tx := &textProjectionFakeTransaction{}
	store := newTextProjectionStore(&textProjectionFakeDatabase{tx: tx})
	err := store.RebuildSnapshotIncremental(context.Background(), testUUID(100), testUUID(101), previousSnapshotID, currentSnapshotID, []retrieval.IncrementalTextEntry{{
		StableKey: "evidence-reused", PreviousEvidenceID: previousEvidenceID, Reuse: true, Entry: entry,
	}})
	if err != nil {
		t.Fatalf("RebuildSnapshotIncremental() error = %v", err)
	}
	if len(tx.execs) != 2 || !strings.Contains(tx.execs[1].query, "content_hash = $7") || !strings.Contains(tx.execs[1].query, "projection_kind = $8") {
		t.Fatalf("incremental textual writes = %#v", tx.execs)
	}
	if tx.execs[1].args[5] != previousEvidenceID || tx.execs[1].args[6] != entry.ContentHash {
		t.Fatalf("copy previous identity/hash args = %#v", tx.execs[1].args)
	}
	if strings.Contains(tx.execs[1].query, entry.ContentHash) {
		t.Fatal("incremental textual copy interpolated content hash")
	}
}

func TestTextProjectionStoreRejectsInvalidScopeBeforeTransaction(t *testing.T) {
	db := &textProjectionFakeDatabase{tx: &textProjectionFakeTransaction{}}
	store := newTextProjectionStore(db)
	entry := textProjectionEntry(1)
	entry.SourceID = testUUID(999)
	if err := store.RebuildSnapshot(context.Background(), testUUID(100), testUUID(101), testUUID(102), []retrieval.TextEntry{entry}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RebuildSnapshot() error = %v, want ErrInvalidInput", err)
	}
	if db.beginCalls != 0 {
		t.Fatalf("begin calls = %d, want 0", db.beginCalls)
	}
}

func TestNewTextProjectionStoreNilIsNotConfigured(t *testing.T) {
	store := NewTextProjectionStore(nil)
	err := store.RebuildSnapshot(context.Background(), testUUID(100), testUUID(101), testUUID(102), nil)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RebuildSnapshot() error = %v, want ErrInvalidInput", err)
	}
	if _, err := store.Search(context.Background(), retrieval.SearchOptions{OrganizationID: testUUID(100), Query: "term"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Search() error = %v, want ErrInvalidInput", err)
	}
}

func TestTextProjectionStoreRollbackOnWriteFailure(t *testing.T) {
	tx := &textProjectionFakeTransaction{
		execErr:   &pgconn.PgError{Code: "23514", Message: "check failed", Detail: "secret-content"},
		execErrAt: 2,
	}
	db := &textProjectionFakeDatabase{tx: tx}
	store := newTextProjectionStore(db)
	err := store.RebuildSnapshot(context.Background(), testUUID(100), testUUID(101), testUUID(102), []retrieval.TextEntry{textProjectionEntry(1)})
	if !errors.Is(err, ErrDatabase) {
		t.Fatalf("RebuildSnapshot() error = %v, want ErrDatabase", err)
	}
	if strings.Contains(err.Error(), "secret-content") {
		t.Fatalf("database error exposed diagnostic content: %v", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestTextProjectionStoreSearchUsesScopedParameterizedQuery(t *testing.T) {
	organizationID, sourceID, snapshotID := testUUID(100), testUUID(101), testUUID(102)
	rows := &textProjectionFakeRows{values: [][]any{{
		testUUID(1), organizationID, sourceID, snapshotID, "symbol", "present",
		"OrderService.create", evidence.ContentDigest("OrderService.create"), false,
		"safe_text", "create", "orderservice.create", "", "", []string{"create", "orderservice.create"},
		0.75, true,
	}}}
	db := &textProjectionFakeDatabase{queryRows: rows}
	store := newTextProjectionStore(db)
	hits, err := store.Search(context.Background(), retrieval.SearchOptions{
		OrganizationID: organizationID,
		SourceID:       sourceID,
		SnapshotID:     snapshotID,
		Query:          "OrderService.create",
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(hits) != 1 || !hits[0].ExactMatch || hits[0].Rank != 0.75 {
		t.Fatalf("Search() hits = %#v", hits)
	}
	if !strings.Contains(db.query, "organization_id = $1") || !strings.Contains(db.query, "LIMIT $6") || !strings.Contains(db.query, "ORDER BY") {
		t.Fatalf("search query lacks scope/limit/deterministic ordering: %s", db.query)
	}
	if strings.Contains(db.query, "OrderService.create") {
		t.Fatal("search query interpolated user query")
	}
	if len(db.queryArgs) != 6 || db.queryArgs[0] != organizationID || db.queryArgs[1] != sourceID || db.queryArgs[2] != snapshotID || db.queryArgs[3] != "OrderService.create" || db.queryArgs[5] != 10 {
		t.Fatalf("search args = %#v", db.queryArgs)
	}
	terms, ok := db.queryArgs[4].([]string)
	if !ok || !reflect.DeepEqual(terms, []string{"create", "orderservice", "orderservice.create"}) {
		t.Fatalf("search exact-term args = %#v", db.queryArgs[4])
	}
}

func TestTextProjectionStoreSearchSanitizesDatabaseDiagnostics(t *testing.T) {
	secret := "authorization=Bearer secret-value"
	db := &textProjectionFakeDatabase{queryErr: &pgconn.PgError{Code: "XX000", Message: "failed", Detail: secret}}
	store := newTextProjectionStore(db)
	_, err := store.Search(context.Background(), retrieval.SearchOptions{OrganizationID: testUUID(100), Query: "term"})
	if !errors.Is(err, ErrDatabase) {
		t.Fatalf("Search() error = %v, want ErrDatabase", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Bearer") {
		t.Fatalf("Search() exposed database diagnostic: %v", err)
	}
}
