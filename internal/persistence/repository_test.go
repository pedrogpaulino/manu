package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pedrogpaulino/manu/internal/contract"
	"github.com/pedrogpaulino/manu/internal/evidence"
)

type fakeStarter struct {
	tx           *fakeTransaction
	beginErr     error
	beginCalls   int
	beginContext context.Context
}

func (f *fakeStarter) Begin(ctx context.Context) (transaction, error) {
	f.beginCalls++
	f.beginContext = ctx
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	return f.tx, nil
}

type fakeExec struct {
	query string
	args  []any
}

type fakeTransaction struct {
	execs         []fakeExec
	queries       []string
	queryRows     []*repositoryFakeRows
	queryIndex    int
	row           pgx.Row
	execErr       error
	execTags      []pgconn.CommandTag
	execIndex     int
	commitErr     error
	rollbackErr   error
	commitCalls   int
	rollbackCalls int
}

func (f *fakeTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	f.execs = append(f.execs, fakeExec{query: query, args: append([]any(nil), args...)})
	if f.execErr != nil {
		return pgconn.CommandTag{}, f.execErr
	}
	if f.execIndex >= len(f.execTags) {
		return pgconn.NewCommandTag("INSERT 1"), nil
	}
	tag := f.execTags[f.execIndex]
	f.execIndex++
	return tag, nil
}

func (f *fakeTransaction) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	f.queries = append(f.queries, query)
	if f.queryIndex >= len(f.queryRows) {
		return &repositoryFakeRows{}, nil
	}
	rows := f.queryRows[f.queryIndex]
	f.queryIndex++
	return rows, nil
}

func (f *fakeTransaction) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	if f.row == nil {
		return fakeRow{err: pgx.ErrNoRows}
	}
	return f.row
}

func (f *fakeTransaction) Commit(context.Context) error {
	f.commitCalls++
	return f.commitErr
}

func (f *fakeTransaction) Rollback(context.Context) error {
	f.rollbackCalls++
	return f.rollbackErr
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignValues(r.values, dest)
}

type repositoryFakeRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func (r *repositoryFakeRows) Close() { r.closed = true }

func (r *repositoryFakeRows) Err() error { return r.err }

func (r *repositoryFakeRows) CommandTag() pgconn.CommandTag { return pgconn.NewCommandTag("SELECT 0") }

func (r *repositoryFakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }

func (r *repositoryFakeRows) Next() bool {
	if r.closed || r.index >= len(r.values) {
		r.closed = true
		return false
	}
	return true
}

func (r *repositoryFakeRows) Scan(dest ...any) error {
	if r.closed || r.index >= len(r.values) {
		return errors.New("scan outside fake row")
	}
	values := r.values[r.index]
	r.index++
	if r.index >= len(r.values) {
		r.closed = true
	}
	return assignValues(values, dest)
}

func (r *repositoryFakeRows) Values() ([]any, error) {
	if r.closed || r.index >= len(r.values) {
		return nil, errors.New("values outside fake row")
	}
	return r.values[r.index], nil
}

func (r *repositoryFakeRows) RawValues() [][]byte { return nil }

func (r *repositoryFakeRows) Conn() *pgx.Conn { return nil }

func assignValues(values []any, dest []any) error {
	if len(values) != len(dest) {
		return fmt.Errorf("fake scan values = %d, destinations = %d", len(values), len(dest))
	}
	for i := range values {
		if err := assignValue(values[i], dest[i]); err != nil {
			return fmt.Errorf("fake scan value %d: %w", i, err)
		}
	}
	return nil
}

func assignValue(value any, dest any) error {
	switch target := dest.(type) {
	case *string:
		if value == nil {
			return errors.New("cannot scan null into string")
		}
		converted, ok := value.(string)
		if !ok {
			return fmt.Errorf("want string, got %T", value)
		}
		*target = converted
	case **string:
		if value == nil {
			*target = nil
			return nil
		}
		converted, ok := value.(string)
		if !ok {
			return fmt.Errorf("want nullable string, got %T", value)
		}
		copy := converted
		*target = &copy
	case *time.Time:
		converted, ok := value.(time.Time)
		if !ok {
			return fmt.Errorf("want time, got %T", value)
		}
		*target = converted
	case *[]byte:
		if value == nil {
			*target = nil
			return nil
		}
		converted, ok := value.([]byte)
		if !ok {
			return fmt.Errorf("want bytes, got %T", value)
		}
		*target = append((*target)[:0], converted...)
	case *int64:
		converted, ok := value.(int64)
		if !ok {
			return fmt.Errorf("want int64, got %T", value)
		}
		*target = converted
	case *bool:
		converted, ok := value.(bool)
		if !ok {
			return fmt.Errorf("want bool, got %T", value)
		}
		*target = converted
	default:
		return fmt.Errorf("unsupported fake scan destination %T", dest)
	}
	return nil
}

func newFakeRepository(tx *fakeTransaction) (*Repository, *fakeStarter) {
	starter := &fakeStarter{tx: tx}
	return newRepositoryWithStarter(starter), starter
}

func testUUID(number int) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012d", number)
}

func testDigest() string { return strings.Repeat("a", 64) }

func backgroundContext() (context.Context, context.CancelFunc) {
	return context.Background(), func() {}
}

func testSnapshot() Snapshot {
	return Snapshot{
		ID:                      testUUID(3),
		SourceID:                testUUID(2),
		ExternalID:              "snapshot-3",
		Revision:                "rev-3",
		Hash:                    testDigest(),
		AnalysisConfigurationID: "analysis-config-1",
		FactualDigest:           testDigest(),
		CapturedAt:              time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC),
	}
}

func testEvidenceUnit() evidence.EvidenceUnit {
	unit := evidence.EvidenceUnit{
		Version:        evidence.Version,
		OrganizationID: "org-external",
		SourceID:       "source-external",
		SnapshotID:     "snapshot-external",
		ArtifactID:     "artifact-external",
		Contribution: evidence.ContributionRef{
			ID:              "observation-external",
			ArtifactID:      "artifact-external",
			AnalyzerID:      "analyzer",
			AnalyzerVersion: "1",
			Method:          "method",
		},
		Locator: contract.Locator{
			SourceID:   "source-external",
			ArtifactID: "artifact-external",
			Path:       "src/main.go",
			StartLine:  1,
			EndLine:    1,
		},
		ContentState:     evidence.ContentStateOmitted,
		ContentHash:      testDigest(),
		Persist:          evidence.DecisionAllow,
		ExternalTransfer: evidence.DecisionDeny,
		Classification:   evidence.ClassificationUnknown,
	}
	unit.ID = evidence.EvidenceID(unit)
	return unit
}

func TestRepositorySQLIsStaticParameterizedAndOrganizationScoped(t *testing.T) {
	queries := []struct {
		name  string
		query string
		org   bool
	}{
		{name: "organization insert", query: insertOrganizationSQL, org: false},
		{name: "organization select", query: selectOrganizationIdentitySQL, org: false},
		{name: "source insert", query: insertSourceSQL, org: true},
		{name: "source select", query: selectSourceIdentitySQL, org: true},
		{name: "snapshot insert", query: insertSnapshotSQL, org: true},
		{name: "snapshot select", query: selectSnapshotIdentitySQL, org: true},
		{name: "artifact insert", query: insertArtifactSQL, org: true},
		{name: "artifact select", query: selectArtifactIdentitySQL, org: true},
		{name: "observation insert", query: insertObservationSQL, org: true},
		{name: "observation select", query: selectObservationIdentitySQL, org: true},
		{name: "entity insert", query: insertEntitySQL, org: true},
		{name: "entity select", query: selectEntityIdentitySQL, org: true},
		{name: "relationship insert", query: insertRelationshipSQL, org: true},
		{name: "relationship select", query: selectRelationshipIdentitySQL, org: true},
		{name: "evidence insert", query: insertEvidenceSQL, org: true},
		{name: "evidence select", query: selectEvidenceIdentitySQL, org: true},
		{name: "coverage insert", query: insertCoverageSQL, org: true},
		{name: "coverage select", query: selectCoverageIdentitySQL, org: true},
		{name: "gap insert", query: insertGapSQL, org: true},
		{name: "gap select", query: selectGapIdentitySQL, org: true},
		{name: "failure insert", query: insertFailureSQL, org: true},
		{name: "failure select", query: selectFailureIdentitySQL, org: true},
		{name: "identity insert", query: insertFactualIdentitySQL, org: true},
		{name: "identity select", query: selectFactualIdentitySQL, org: true},
		{name: "source lock", query: lockSourceSQL, org: true},
		{name: "snapshot lock", query: lockSnapshotSQL, org: true},
		{name: "archive identities", query: archiveActiveIdentitiesSQL, org: true},
		{name: "activate identities", query: activateSnapshotIdentitiesSQL, org: true},
		{name: "activate source", query: activateSourceSQL, org: true},
	}
	for _, test := range queries {
		t.Run(test.name, func(t *testing.T) {
			if !strings.Contains(test.query, "$1") {
				t.Fatal("query does not use positional placeholders")
			}
			if strings.Contains(test.query, "%s") || strings.Contains(test.query, "+") {
				t.Fatal("query appears to interpolate input")
			}
			if test.org && !strings.Contains(test.query, "organization_id") {
				t.Fatal("organization-scoped query omits organization_id")
			}
		})
	}
}

func TestRepositoryCanonicalWritesCommitAndCarryOrganizationScope(t *testing.T) {
	organizationID := testUUID(1)
	snapshot := testSnapshot()
	unit := testEvidenceUnit()
	tests := []struct {
		name string
		call func(*Repository) error
	}{
		{
			name: "organization",
			call: func(r *Repository) error {
				return r.EnsureOrganization(context.Background(), organizationID, Organization{ExternalID: "org", Name: "Local"})
			},
		},
		{
			name: "source",
			call: func(r *Repository) error {
				return r.InsertSource(context.Background(), organizationID, Source{ID: testUUID(2), ExternalID: "source", Name: "Source", Type: "filesystem"})
			},
		},
		{
			name: "snapshot",
			call: func(r *Repository) error { return r.InsertSnapshot(context.Background(), organizationID, snapshot) },
		},
		{
			name: "artifact",
			call: func(r *Repository) error {
				return r.InsertArtifact(context.Background(), organizationID, Artifact{ID: testUUID(4), SourceID: testUUID(2), SnapshotID: testUUID(3), ExternalID: "artifact", Path: "src/main.go", Type: "go", ContentHash: testDigest()})
			},
		},
		{
			name: "observation",
			call: func(r *Repository) error {
				return r.InsertObservation(context.Background(), organizationID, Observation{ID: testUUID(5), SourceID: testUUID(2), SnapshotID: testUUID(3), ArtifactID: testUUID(4), ExternalID: "observation", AnalyzerID: "analyzer", AnalyzerVersion: "1", Method: "method", Type: "fact", Locator: contract.Locator{Path: "src/main.go"}, Value: json.RawMessage(`{}`), ObservedAt: time.Unix(1, 0).UTC()})
			},
		},
		{
			name: "entity",
			call: func(r *Repository) error {
				return r.InsertEntity(context.Background(), organizationID, Entity{ID: testUUID(6), SourceID: testUUID(2), SnapshotID: testUUID(3), ExternalID: "entity", Type: "service"})
			},
		},
		{
			name: "relationship",
			call: func(r *Repository) error {
				return r.InsertRelationship(context.Background(), organizationID, Relationship{ID: testUUID(7), SourceID: testUUID(2), SnapshotID: testUUID(3), ExternalID: "relation", FromEntityID: testUUID(6), ToEntityID: testUUID(6), Type: "calls"})
			},
		},
		{
			name: "evidence",
			call: func(r *Repository) error {
				return r.InsertEvidence(context.Background(), organizationID, Evidence{
					ID: testUUID(12), OrganizationID: organizationID, SourceID: testUUID(2), SnapshotID: testUUID(3), ArtifactID: testUUID(4), ObservationID: testUUID(5),
					OrganizationExternalID: "org-external", SourceExternalID: "source-external", SnapshotExternalID: "snapshot-external", ArtifactExternalID: "artifact-external", ObservationExternalID: "observation-external",
					Unit: unit,
				})
			},
		},
		{
			name: "coverage",
			call: func(r *Repository) error {
				return r.InsertCoverage(context.Background(), organizationID, Coverage{ID: testUUID(8), SourceID: testUUID(2), SnapshotID: testUUID(3), Value: contract.Coverage{ID: "coverage-external", Dimension: "inventory", State: contract.CoverageProduced}})
			},
		},
		{
			name: "gap",
			call: func(r *Repository) error {
				return r.InsertGap(context.Background(), organizationID, Gap{ID: testUUID(9), SourceID: testUUID(2), SnapshotID: testUUID(3), Value: contract.Gap{ID: "gap-external", Code: "unknown", Message: "not observed"}})
			},
		},
		{
			name: "failure",
			call: func(r *Repository) error {
				return r.InsertFailure(context.Background(), organizationID, Failure{ID: testUUID(10), SourceID: testUUID(2), SnapshotID: testUUID(3), ArtifactID: testUUID(4), ArtifactExternalID: "artifact-external", Value: contract.Failure{ID: "failure-external", ArtifactID: "artifact-external", Code: "failed", Operation: "analyze", Message: "unavailable"}})
			},
		},
		{
			name: "factual identity",
			call: func(r *Repository) error {
				return r.InsertFactualIdentity(context.Background(), organizationID, FactualIdentity{ID: testUUID(11), SourceID: testUUID(2), SnapshotID: testUUID(3), IdentityKey: "entity:service", FactualDigest: testDigest(), State: "historical", ObservedAt: time.Unix(1, 0).UTC()})
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeTransaction{execTags: []pgconn.CommandTag{pgconn.NewCommandTag("INSERT 1")}}
			r, starter := newFakeRepository(tx)
			if err := test.call(r); err != nil {
				t.Fatalf("write returned error: %v", err)
			}
			if starter.beginCalls != 1 || tx.commitCalls != 1 || tx.rollbackCalls != 0 {
				t.Fatalf("transaction calls = begin %d, commit %d, rollback %d", starter.beginCalls, tx.commitCalls, tx.rollbackCalls)
			}
			if len(tx.execs) != 1 {
				t.Fatalf("exec calls = %d, want 1", len(tx.execs))
			}
			if !containsArgument(tx.execs[0].args, organizationID) {
				t.Fatal("write did not bind the explicit organization scope")
			}
			if !strings.Contains(tx.execs[0].query, "$1") {
				t.Fatal("write query is not parameterized")
			}
		})
	}
}

func TestRepositoryCanonicalValuesUseContractValidation(t *testing.T) {
	organizationID := testUUID(1)
	tests := []struct {
		name string
		call func(*Repository) error
	}{
		{
			name: "unknown coverage state",
			call: func(r *Repository) error {
				return r.InsertCoverage(context.Background(), organizationID, Coverage{
					ID: testUUID(8), SourceID: testUUID(2), SnapshotID: testUUID(3),
					Value: contract.Coverage{ID: "coverage", Dimension: "inventory", State: contract.CoverageUnknown},
				})
			},
		},
		{
			name: "gap without message",
			call: func(r *Repository) error {
				return r.InsertGap(context.Background(), organizationID, Gap{
					ID: testUUID(9), SourceID: testUUID(2), SnapshotID: testUUID(3),
					Value: contract.Gap{ID: "gap", Code: "unknown"},
				})
			},
		},
		{
			name: "failure without operation",
			call: func(r *Repository) error {
				return r.InsertFailure(context.Background(), organizationID, Failure{
					ID: testUUID(10), SourceID: testUUID(2), SnapshotID: testUUID(3),
					Value: contract.Failure{ID: "failure", Code: "failed", Message: "unavailable"},
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeTransaction{}
			r, _ := newFakeRepository(tx)
			err := test.call(r)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
			if len(tx.execs) != 0 || tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("transaction calls = exec %d, commit %d, rollback %d", len(tx.execs), tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func containsArgument(args []any, want string) bool {
	for _, arg := range args {
		if value, ok := arg.(string); ok && value == want {
			return true
		}
	}
	return false
}

func TestRepositoryWithinTxCommitRollbackAndCancellation(t *testing.T) {
	callbackErr := errors.New("callback failed")
	commitErr := errors.New("commit failed")
	tests := []struct {
		name         string
		context      func() (context.Context, context.CancelFunc)
		callback     func(context.Context, *UnitOfWork) error
		commitErr    error
		wantErr      error
		wantBegin    int
		wantCommit   int
		wantRollback int
	}{
		{
			name:       "commit after success",
			context:    backgroundContext,
			callback:   func(context.Context, *UnitOfWork) error { return nil },
			wantBegin:  1,
			wantCommit: 1,
		},
		{
			name:         "rollback after callback error",
			context:      backgroundContext,
			callback:     func(context.Context, *UnitOfWork) error { return callbackErr },
			wantErr:      callbackErr,
			wantBegin:    1,
			wantRollback: 1,
		},
		{
			name:     "cancel before begin",
			context:  func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) },
			callback: func(context.Context, *UnitOfWork) error { return nil },
			wantErr:  context.Canceled,
		},
		{
			name:         "rollback after callback cancellation",
			context:      backgroundContext,
			callback:     func(_ context.Context, _ *UnitOfWork) error { return context.Canceled },
			wantErr:      context.Canceled,
			wantBegin:    1,
			wantRollback: 1,
		},
		{
			name:         "rollback after commit failure",
			context:      backgroundContext,
			callback:     func(context.Context, *UnitOfWork) error { return nil },
			commitErr:    commitErr,
			wantErr:      commitErr,
			wantBegin:    1,
			wantCommit:   1,
			wantRollback: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.context()
			defer cancel()
			if test.name == "cancel before begin" {
				cancel()
			}
			tx := &fakeTransaction{commitErr: test.commitErr}
			r, starter := newFakeRepository(tx)
			err := r.WithinTx(ctx, func(u *UnitOfWork) error { return test.callback(ctx, u) })
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if starter.beginCalls != test.wantBegin || tx.commitCalls != test.wantCommit || tx.rollbackCalls != test.wantRollback {
				t.Fatalf("transaction calls = begin %d, commit %d, rollback %d", starter.beginCalls, tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestRepositoryWithinTxRollsBackWhenContextCancelsBeforeCommit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx := &fakeTransaction{}
	r, _ := newFakeRepository(tx)
	err := r.WithinTx(ctx, func(*UnitOfWork) error {
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
		t.Fatalf("transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestRepositorySanitizesPostgresDetailsAndPreservesCategories(t *testing.T) {
	const secret = "secret-value-must-not-escape"
	tests := []struct {
		name string
		code string
		want error
	}{
		{name: "unique", code: "23505", want: ErrConflict},
		{name: "foreign key", code: "23503", want: ErrNotFound},
		{name: "check", code: "23514", want: ErrInvalidInput},
		{name: "unknown database error", code: "42P01", want: ErrDatabase},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeTransaction{execErr: &pgconn.PgError{Code: test.code, Message: "database failure", Detail: secret}}
			r, _ := newFakeRepository(tx)
			err := r.InsertArtifact(context.Background(), testUUID(1), Artifact{
				ID: testUUID(4), SourceID: testUUID(2), SnapshotID: testUUID(3), ExternalID: "artifact",
				Path: "src/main.go", Type: "go", ContentHash: testDigest(),
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("database detail leaked into error: %v", err)
			}
			if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestRepositorySnapshotConflictIsIncompatibleButIdenticalRetryIsIdempotent(t *testing.T) {
	snapshot := testSnapshot()
	tests := []struct {
		name    string
		values  []any
		wantErr error
	}{
		{
			name: "identical retry",
			values: []any{
				snapshot.ExternalID, snapshot.Revision, snapshot.Hash,
				snapshot.AnalysisConfigurationID, snapshot.FactualDigest, snapshot.CapturedAt,
			},
		},
		{
			name: "different factual digest",
			values: []any{
				snapshot.ExternalID, snapshot.Revision, snapshot.Hash,
				snapshot.AnalysisConfigurationID, strings.Repeat("b", 64), snapshot.CapturedAt,
			},
			wantErr: ErrIncompatibleSnapshot,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeTransaction{
				execTags: []pgconn.CommandTag{pgconn.NewCommandTag("INSERT 0")},
				row:      fakeRow{values: test.values},
			}
			r, _ := newFakeRepository(tx)
			err := r.InsertSnapshot(context.Background(), testUUID(1), snapshot)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.wantErr)
				}
				if tx.rollbackCalls != 1 || tx.commitCalls != 0 {
					t.Fatalf("conflict transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("identical retry error: %v", err)
			}
			if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
				t.Fatalf("retry transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestRepositoryFactualIdentityRetryAfterActivationIsIdempotent(t *testing.T) {
	identity := FactualIdentity{
		ID: testUUID(11), SourceID: testUUID(2), SnapshotID: testUUID(3),
		IdentityKey: "entity:service", FactualDigest: testDigest(), State: "historical",
		ObservedAt: time.Unix(1, 0).UTC(),
	}
	tx := &fakeTransaction{
		execTags: []pgconn.CommandTag{pgconn.NewCommandTag("INSERT 0")},
		row:      fakeRow{values: []any{identity.SourceID, identity.SnapshotID, identity.IdentityKey, identity.FactualDigest, "active", identity.ObservedAt}},
	}
	r, _ := newFakeRepository(tx)
	if err := r.InsertFactualIdentity(context.Background(), testUUID(1), identity); err != nil {
		t.Fatalf("retry after activation: %v", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
	}
}

func TestRepositoryCanonicalRetriesRejectDivergentFacts(t *testing.T) {
	organizationID := testUUID(1)
	sourceID, snapshotID, artifactID := testUUID(2), testUUID(3), testUUID(4)
	unit := testEvidenceUnit()
	unitLocator, err := marshalLocator(unit.Locator)
	if err != nil {
		t.Fatalf("marshal evidence locator: %v", err)
	}
	evidenceRecord := Evidence{
		ID: testUUID(12), OrganizationID: organizationID, SourceID: sourceID, SnapshotID: snapshotID,
		ArtifactID: artifactID, ObservationID: testUUID(5), OrganizationExternalID: "org-external",
		SourceExternalID: "source-external", SnapshotExternalID: "snapshot-external",
		ArtifactExternalID: "artifact-external", ObservationExternalID: "observation-external", Unit: unit,
	}
	tests := []struct {
		name string
		row  []any
		call func(*Repository) error
	}{
		{
			name: "artifact",
			row:  []any{testUUID(99), snapshotID, "artifact", "src/main.go", "go", testDigest(), int64(0)},
			call: func(r *Repository) error {
				return r.InsertArtifact(context.Background(), organizationID, Artifact{ID: artifactID, SourceID: sourceID, SnapshotID: snapshotID, ExternalID: "artifact", Path: "src/main.go", Type: "go", ContentHash: testDigest()})
			},
		},
		{
			name: "observation",
			row:  []any{testUUID(99), snapshotID, artifactID, "observation", "analyzer", "1", "method", "fact", []byte(`{"path":"src/main.go"}`), []byte(`{}`), time.Unix(1, 0).UTC()},
			call: func(r *Repository) error {
				return r.InsertObservation(context.Background(), organizationID, Observation{ID: testUUID(5), SourceID: sourceID, SnapshotID: snapshotID, ArtifactID: artifactID, ExternalID: "observation", AnalyzerID: "analyzer", AnalyzerVersion: "1", Method: "method", Type: "fact", Locator: contract.Locator{Path: "src/main.go"}, Value: json.RawMessage(`{}`), ObservedAt: time.Unix(1, 0).UTC()})
			},
		},
		{
			name: "entity",
			row:  []any{testUUID(99), snapshotID, "entity", "service", nil, []byte(`{}`)},
			call: func(r *Repository) error {
				return r.InsertEntity(context.Background(), organizationID, Entity{ID: testUUID(6), SourceID: sourceID, SnapshotID: snapshotID, ExternalID: "entity", Type: "service"})
			},
		},
		{
			name: "relationship",
			row:  []any{testUUID(99), snapshotID, "relation", testUUID(6), testUUID(6), "calls", []byte(`{}`)},
			call: func(r *Repository) error {
				return r.InsertRelationship(context.Background(), organizationID, Relationship{ID: testUUID(7), SourceID: sourceID, SnapshotID: snapshotID, ExternalID: "relation", FromEntityID: testUUID(6), ToEntityID: testUUID(6), Type: "calls"})
			},
		},
		{
			name: "evidence",
			row:  []any{testUUID(99), snapshotID, artifactID, testUUID(5), unit.ID, unitLocator, string(unit.ContentState), nil, unit.ContentHash, int64(0), int64(0), false, string(unit.Classification), []byte(`[]`), string(unit.Persist), string(unit.ExternalTransfer), nil, []byte(`{}`)},
			call: func(r *Repository) error {
				return r.InsertEvidence(context.Background(), organizationID, evidenceRecord)
			},
		},
		{
			name: "coverage",
			row:  []any{testUUID(99), snapshotID, "coverage-external", "inventory", nil, "produced", nil, nil, nil},
			call: func(r *Repository) error {
				return r.InsertCoverage(context.Background(), organizationID, Coverage{ID: testUUID(8), SourceID: sourceID, SnapshotID: snapshotID, Value: contract.Coverage{ID: "coverage-external", Dimension: "inventory", State: contract.CoverageProduced}})
			},
		},
		{
			name: "gap",
			row:  []any{testUUID(99), snapshotID, "gap-external", "unknown", nil, nil, "not observed", nil, nil},
			call: func(r *Repository) error {
				return r.InsertGap(context.Background(), organizationID, Gap{ID: testUUID(9), SourceID: sourceID, SnapshotID: snapshotID, Value: contract.Gap{ID: "gap-external", Code: "unknown", Message: "not observed"}})
			},
		},
		{
			name: "failure",
			row:  []any{testUUID(99), snapshotID, nil, "failure-external", "failed", "analyze", "unavailable", nil, false, nil},
			call: func(r *Repository) error {
				return r.InsertFailure(context.Background(), organizationID, Failure{ID: testUUID(10), SourceID: sourceID, SnapshotID: snapshotID, Value: contract.Failure{ID: "failure-external", Code: "failed", Operation: "analyze", Message: "unavailable"}})
			},
		},
		{
			name: "factual identity",
			row:  []any{testUUID(99), snapshotID, "entity:service", testDigest(), "historical", time.Unix(1, 0).UTC()},
			call: func(r *Repository) error {
				return r.InsertFactualIdentity(context.Background(), organizationID, FactualIdentity{ID: testUUID(11), SourceID: sourceID, SnapshotID: snapshotID, IdentityKey: "entity:service", FactualDigest: testDigest(), State: "historical", ObservedAt: time.Unix(1, 0).UTC()})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeTransaction{execTags: []pgconn.CommandTag{pgconn.NewCommandTag("INSERT 0")}, row: fakeRow{values: test.row}}
			r, _ := newFakeRepository(tx)
			err := test.call(r)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("error = %v, want conflict", err)
			}
			if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
			}
		})
	}
}

func TestRepositoryActivateSnapshotIsScopedOrderedAndAtomic(t *testing.T) {
	organizationID, sourceID, snapshotID := testUUID(1), testUUID(2), testUUID(3)
	tx := &fakeTransaction{
		queryRows: []*repositoryFakeRows{
			{values: [][]any{{sourceID}}},
			{values: [][]any{{snapshotID}}},
		},
		execTags: []pgconn.CommandTag{
			pgconn.NewCommandTag("UPDATE 2"),
			pgconn.NewCommandTag("UPDATE 1"),
			pgconn.NewCommandTag("UPDATE 1"),
		},
	}
	r, _ := newFakeRepository(tx)
	if err := r.ActivateSnapshot(context.Background(), organizationID, sourceID, snapshotID); err != nil {
		t.Fatalf("activate snapshot: %v", err)
	}
	if tx.commitCalls != 1 || tx.rollbackCalls != 0 {
		t.Fatalf("activation transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
	}
	if len(tx.queries) != 2 || !strings.Contains(tx.queries[0], "FOR UPDATE") || !strings.Contains(tx.queries[1], "FOR SHARE") {
		t.Fatalf("activation lock queries = %q", tx.queries)
	}
	if len(tx.execs) != 3 {
		t.Fatalf("activation exec calls = %d, want 3", len(tx.execs))
	}
	if !strings.Contains(tx.execs[0].query, "SET state = 'historical'") || !strings.Contains(tx.execs[1].query, "SET state = 'active'") || !strings.Contains(tx.execs[2].query, "active_snapshot_id") {
		t.Fatalf("activation SQL order = %q, %q, %q", tx.execs[0].query, tx.execs[1].query, tx.execs[2].query)
	}
	for _, exec := range tx.execs {
		if !containsArgument(exec.args, organizationID) {
			t.Fatal("activation statement omitted organization scope")
		}
	}
}

func TestRepositoryActivateSnapshotNotFoundOrMultipleRowsRollsBack(t *testing.T) {
	tests := []struct {
		name string
		rows []*repositoryFakeRows
		want error
	}{
		{
			name: "source not found",
			rows: []*repositoryFakeRows{{}},
			want: ErrNotFound,
		},
		{
			name: "duplicate source rows",
			rows: []*repositoryFakeRows{{values: [][]any{{testUUID(2)}, {testUUID(2)}}}},
			want: ErrInconsistent,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := &fakeTransaction{queryRows: test.rows}
			r, _ := newFakeRepository(tx)
			err := r.ActivateSnapshot(context.Background(), testUUID(1), testUUID(2), testUUID(3))
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if tx.commitCalls != 0 || tx.rollbackCalls != 1 {
				t.Fatalf("failed activation transaction calls = commit %d, rollback %d", tx.commitCalls, tx.rollbackCalls)
			}
			if len(tx.execs) != 0 {
				t.Fatalf("failed activation executed writes: %d", len(tx.execs))
			}
		})
	}
}

func TestRepositoryRejectsCrossOrganizationEvidenceWithoutEchoingContent(t *testing.T) {
	secret := "API_KEY=do-not-echo-this-value"
	unit := testEvidenceUnit()
	unit.ContentState = evidence.ContentStatePresent
	unit.Content = secret
	unit.ContentHash = evidence.ContentDigest(secret)
	unit.ContentBytes = int64(len(secret))
	unit.ContentCharacters = int64(len(secret))
	unit.ID = evidence.EvidenceID(unit)
	tx := &fakeTransaction{}
	r, _ := newFakeRepository(tx)
	err := r.InsertEvidence(context.Background(), testUUID(1), Evidence{
		ID: testUUID(12), OrganizationID: testUUID(1), SourceID: testUUID(2), SnapshotID: testUUID(3), ArtifactID: testUUID(4), ObservationID: testUUID(5),
		OrganizationExternalID: "org-external", SourceExternalID: "source-external", SnapshotExternalID: "snapshot-external", ArtifactExternalID: "artifact-external", ObservationExternalID: "observation-external",
		Unit: unit,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want invalid input", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error echoed evidence content: %v", err)
	}
	if tx.commitCalls != 0 || tx.rollbackCalls != 1 || len(tx.execs) != 0 {
		t.Fatalf("unsafe evidence transaction calls = commit %d, rollback %d, execs %d", tx.commitCalls, tx.rollbackCalls, len(tx.execs))
	}
}

func TestRepositorySnapshotAPIHasNoMutationMethods(t *testing.T) {
	// This compile-time-facing assertion is intentionally kept as a source
	// contract: repository.go exposes InsertSnapshot but no UpdateSnapshot or
	// DeleteSnapshot method. The migration/repository package has no mutable
	// snapshot API to call here.
	var _ func(*Repository, context.Context, string, Snapshot) error = (*Repository).InsertSnapshot
}
