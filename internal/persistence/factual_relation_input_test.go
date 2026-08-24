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
	"github.com/pedrogpaulino/manu/internal/identity"
	"github.com/pedrogpaulino/manu/internal/query"
	"github.com/pedrogpaulino/manu/internal/retrieval"
)

type factualRelationInputFakeDatabase struct {
	rows       []pgx.Rows
	queryErrAt int
	queryErr   error
	nilRowsAt  int
	queries    []string
	args       [][]any
}

func (d *factualRelationInputFakeDatabase) Query(_ context.Context, statement string, args ...any) (pgx.Rows, error) {
	d.queries = append(d.queries, statement)
	d.args = append(d.args, append([]any(nil), args...))
	if d.queryErr != nil && (d.queryErrAt == 0 || d.queryErrAt == len(d.queries)) {
		return nil, d.queryErr
	}
	if d.nilRowsAt == len(d.queries) {
		return nil, nil
	}
	index := len(d.queries) - 1
	if index >= len(d.rows) || d.rows[index] == nil {
		return &factualRelationInputFakeRows{}, nil
	}
	return d.rows[index], nil
}

type factualRelationInputFakeRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func (r *factualRelationInputFakeRows) Close() { r.closed = true }

func (r *factualRelationInputFakeRows) Err() error { return r.err }

func (r *factualRelationInputFakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.NewCommandTag("SELECT 0")
}

func (r *factualRelationInputFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (r *factualRelationInputFakeRows) Next() bool {
	if r.closed || r.index >= len(r.values) {
		r.closed = true
		return false
	}
	return true
}

func (r *factualRelationInputFakeRows) Scan(dest ...any) error {
	if r.closed || r.index >= len(r.values) {
		return errors.New("scan outside factual relation input fake row")
	}
	values := r.values[r.index]
	r.index++
	if r.index >= len(r.values) {
		r.closed = true
	}
	return assignValues(values, dest)
}

func (r *factualRelationInputFakeRows) Values() ([]any, error) {
	if r.closed || r.index >= len(r.values) {
		return nil, errors.New("values outside factual relation input fake row")
	}
	return r.values[r.index], nil
}

func (r *factualRelationInputFakeRows) RawValues() [][]byte { return nil }

func (r *factualRelationInputFakeRows) Conn() *pgx.Conn { return nil }

func factualRelationInputScope() query.Scope {
	return query.Scope{
		OrganizationID: testUUID(100),
		SourceID:       testUUID(101),
		SnapshotID:     testUUID(102),
	}
}

func factualRelationInputQueryInput() query.QueryRetrievalInput {
	return query.QueryRetrievalInput{
		Question:     "which service calls the target?",
		QuestionKind: query.KnowledgeQuestionInventory,
		Scope:        factualRelationInputScope(),
		Limit:        4,
	}
}

func factualRelationInputSeedRow(evidenceID, subjectKind, subjectID string, objectKind, objectID any) []any {
	return []any{
		evidenceID, testDigest(), "organization-local", "source-app", "snapshot-1",
		subjectKind, subjectID, objectKind, objectID,
	}
}

func factualRelationInputReferenceRow(from, to, evidenceID string) []any {
	return []any{from, to, evidenceID, testDigest()}
}

func TestFactualRelationInputProviderUsesScopedBoundedCanonicalQueries(t *testing.T) {
	scope := factualRelationInputScope()
	selected := testUUID(200)
	db := &factualRelationInputFakeDatabase{
		rows: []pgx.Rows{
			&factualRelationInputFakeRows{values: [][]any{
				factualRelationInputSeedRow(selected, "symbol", "Service", "named_element", "Target"),
			}},
			&factualRelationInputFakeRows{values: [][]any{
				factualRelationInputReferenceRow(
					factualProjectionEntityIDForTest("symbol", "Service"),
					factualProjectionEntityIDForTest("named_element", "Target"),
					testUUID(201),
				),
			}},
		},
	}
	provider := newFactualRelationInputProvider(db)
	textHit := retrieval.TextHit{
		EvidenceID: selected, OrganizationID: scope.OrganizationID,
		SourceID: scope.SourceID, SnapshotID: scope.SnapshotID,
	}
	seeds, references, err := provider.RelationInputs(context.Background(), factualRelationInputQueryInput(), []retrieval.TextHit{textHit}, nil)
	if err != nil {
		t.Fatalf("RelationInputs() error = %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("seed count = %d, want subject and object anchors", len(seeds))
	}
	if len(db.queries) != 2 {
		t.Fatalf("query count = %d, want two bounded reads", len(db.queries))
	}
	for index, statement := range db.queries {
		for _, fragment := range []string{
			"organization_id = $1::uuid", "source_id = $2::uuid", "snapshot_id = $3::uuid",
			"ANY($4::uuid[])", "LIMIT $5", "ORDER BY",
		} {
			if !strings.Contains(statement, fragment) {
				t.Errorf("query %d omits %q: %s", index, fragment, statement)
			}
		}
	}
	if got := db.args[0][0]; got != scope.OrganizationID {
		t.Fatalf("seed organization argument = %v, want %q", got, scope.OrganizationID)
	}
	if got := db.args[0][1]; got != scope.SourceID {
		t.Fatalf("seed source argument = %v, want %q", got, scope.SourceID)
	}
	if got := db.args[0][2]; got != scope.SnapshotID {
		t.Fatalf("seed snapshot argument = %v, want %q", got, scope.SnapshotID)
	}
	ids, ok := db.args[0][3].([]string)
	if !ok || !reflect.DeepEqual(ids, []string{selected}) {
		t.Fatalf("selected evidence argument = %#v, want [%q]", db.args[0][3], selected)
	}
	if got := db.args[0][4]; got != 4 {
		t.Fatalf("seed limit argument = %v, want 4", got)
	}
	if got := db.args[1][3].([]string); len(got) != 2 {
		t.Fatalf("anchor argument = %#v, want two canonical entity IDs", got)
	}
	if len(references) != 2 {
		t.Fatalf("reference entity count = %d, want both adjacent entities", len(references))
	}
}

func TestFactualRelationInputProviderIsDeterministicDeduplicatedAndUsesCanonicalEvidenceIDs(t *testing.T) {
	selected := testUUID(210)
	related := testUUID(211)
	anchorSubject := factualProjectionEntityIDForTest("symbol", "Service")
	anchorObject := factualProjectionEntityIDForTest("named_element", "Target")
	db := &factualRelationInputFakeDatabase{
		rows: []pgx.Rows{
			&factualRelationInputFakeRows{values: [][]any{
				factualRelationInputSeedRow(selected, "symbol", "Service", "named_element", "Target"),
				factualRelationInputSeedRow(selected, "symbol", "Service", "named_element", "Target"),
			}},
			&factualRelationInputFakeRows{values: [][]any{
				factualRelationInputReferenceRow(anchorObject, anchorSubject, related),
				factualRelationInputReferenceRow(anchorSubject, anchorObject, related),
				factualRelationInputReferenceRow(anchorSubject, anchorObject, related),
			}},
		},
	}
	provider := newFactualRelationInputProvider(db)
	seeds, references, err := provider.RelationInputs(context.Background(), factualRelationInputQueryInput(), []retrieval.TextHit{{EvidenceID: selected}}, []retrieval.VectorHit{{EvidenceID: selected}})
	if err != nil {
		t.Fatalf("RelationInputs() error = %v", err)
	}
	wantSeeds := []retrieval.RelationSeed{
		{EvidenceID: selected, EntityID: anchorSubject},
		{EvidenceID: selected, EntityID: anchorObject},
	}
	if wantSeeds[0].EntityID > wantSeeds[1].EntityID {
		wantSeeds[0], wantSeeds[1] = wantSeeds[1], wantSeeds[0]
	}
	if !reflect.DeepEqual(seeds, wantSeeds) {
		t.Fatalf("seeds = %#v, want %#v", seeds, wantSeeds)
	}
	if len(references) != 2 {
		t.Fatalf("reference entity count = %d, want two adjacent entities", len(references))
	}
	for entityID, values := range references {
		if len(values) != 1 || values[0].EvidenceID != related || values[0].EvidenceID == "fact-external-1" {
			t.Fatalf("references[%q] = %#v, want one canonical evidence UUID", entityID, values)
		}
		if values[0].OrganizationID != factualRelationInputScope().OrganizationID || values[0].EvidenceContentHash != testDigest() {
			t.Fatalf("references[%q] provenance = %#v, want query scope and canonical hash", entityID, values[0])
		}
	}
	wantObjectID := identity.CanonicalUUID("factual-projection-entity", "organization-local", "source-app", "snapshot-1", "named_element", "Target")
	if seeds[0].EntityID != wantObjectID && seeds[1].EntityID != wantObjectID {
		t.Fatalf("seed entity IDs = %q/%q, want projection identity %q", seeds[0].EntityID, seeds[1].EntityID, wantObjectID)
	}
}

func TestFactualRelationInputProviderReturnsEmptyWithoutSelectedHits(t *testing.T) {
	db := &factualRelationInputFakeDatabase{}
	seeds, references, err := newFactualRelationInputProvider(db).RelationInputs(context.Background(), factualRelationInputQueryInput(), nil, nil)
	if err != nil {
		t.Fatalf("RelationInputs() error = %v", err)
	}
	if len(seeds) != 0 || len(references) != 0 || len(db.queries) != 0 {
		t.Fatalf("empty input result = seeds:%#v references:%#v queries:%d", seeds, references, len(db.queries))
	}
}

func TestFactualRelationInputProviderPreservesCancellationAndNormalizesFailures(t *testing.T) {
	input := factualRelationInputQueryInput()
	hits := []retrieval.TextHit{{EvidenceID: testUUID(220)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := &factualRelationInputFakeDatabase{}
	_, _, err := newFactualRelationInputProvider(db).RelationInputs(ctx, input, hits, nil)
	if !errors.Is(err, context.Canceled) || len(db.queries) != 0 {
		t.Fatalf("canceled result error = %v, queries = %d, want context canceled without query", err, len(db.queries))
	}

	db = &factualRelationInputFakeDatabase{queryErr: errors.New("secret database failure")}
	_, _, err = newFactualRelationInputProvider(db).RelationInputs(context.Background(), input, hits, nil)
	if !errors.Is(err, ErrDatabase) || strings.Contains(err.Error(), "secret database failure") {
		t.Fatalf("database error = %v, want redacted ErrDatabase", err)
	}

	db = &factualRelationInputFakeDatabase{rows: []pgx.Rows{
		&factualRelationInputFakeRows{err: errors.New("row failure")},
	}}
	_, _, err = newFactualRelationInputProvider(db).RelationInputs(context.Background(), input, hits, nil)
	if !errors.Is(err, ErrDatabase) || strings.Contains(err.Error(), "row failure") {
		t.Fatalf("rows error = %v, want redacted ErrDatabase", err)
	}
}

func TestFactualRelationInputProviderRejectsInvalidBoundsAndNilRows(t *testing.T) {
	input := factualRelationInputQueryInput()
	input.Limit = retrieval.MaxTextSearchLimit + 1
	_, _, err := newFactualRelationInputProvider(&factualRelationInputFakeDatabase{}).RelationInputs(context.Background(), input, []retrieval.TextHit{{EvidenceID: testUUID(230)}}, nil)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid limit error = %v, want ErrInvalidInput", err)
	}

	input = factualRelationInputQueryInput()
	db := &factualRelationInputFakeDatabase{nilRowsAt: 1}
	_, _, err = newFactualRelationInputProvider(db).RelationInputs(context.Background(), input, []retrieval.TextHit{{EvidenceID: testUUID(231)}}, nil)
	if !errors.Is(err, ErrInconsistent) {
		t.Fatalf("nil rows error = %v, want ErrInconsistent", err)
	}
}

func factualProjectionEntityIDForTest(kind, id string) string {
	return identity.CanonicalUUID("factual-projection-entity", "organization-local", "source-app", "snapshot-1", kind, id)
}

func TestFactualRelationInputSQLDoesNotInterpolateSelectedIdentity(t *testing.T) {
	for name, statement := range map[string]string{
		"seeds":      factualRelationSeedsSQL,
		"references": factualRelationReferencesSQL,
	} {
		if strings.Contains(statement, fmt.Sprintf("%s", testUUID(999))) || strings.Contains(statement, "fact-external-1") {
			t.Errorf("%s SQL contains a selected identity literal", name)
		}
		for _, fragment := range []string{"canonical_fact_evidence", "canonical_facts", "evidence_units", "organization_id = $1::uuid", "source_id = $2::uuid", "snapshot_id = $3::uuid", "LIMIT $5"} {
			if !strings.Contains(statement, fragment) {
				t.Errorf("%s SQL omits %q", name, fragment)
			}
		}
	}
}
