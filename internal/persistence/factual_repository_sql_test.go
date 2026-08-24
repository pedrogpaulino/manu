package persistence

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type factualSQLStarter struct {
	tx         *factualSQLTransaction
	beginCalls int
}

func (s *factualSQLStarter) Begin(context.Context) (transaction, error) {
	s.beginCalls++
	return s.tx, nil
}

type factualSQLTransaction struct {
	execs         []string
	execArgs      [][]any
	execNumber    int
	execErrorAt   int
	execRows      []int64
	queries       []string
	queryRows     map[string][]pgx.Rows
	queryRowList  []pgx.Row
	commitCalls   int
	rollbackCalls int
}

func (tx *factualSQLTransaction) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.execNumber++
	tx.execs = append(tx.execs, query)
	tx.execArgs = append(tx.execArgs, append([]any(nil), args...))
	if tx.execErrorAt > 0 && tx.execNumber == tx.execErrorAt {
		return pgconn.CommandTag{}, errors.New("secret factual database detail")
	}
	rows := int64(1)
	if tx.execNumber <= len(tx.execRows) {
		rows = tx.execRows[tx.execNumber-1]
	}
	return pgconn.NewCommandTag("INSERT " + strconv.FormatInt(rows, 10)), nil
}

func (tx *factualSQLTransaction) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	tx.queries = append(tx.queries, query)
	rows := tx.queryRows[query]
	if len(rows) == 0 {
		return &repositoryFakeRows{}, nil
	}
	result := rows[0]
	tx.queryRows[query] = rows[1:]
	return result, nil
}

func (tx *factualSQLTransaction) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row {
	if len(tx.queryRowList) == 0 {
		return fakeRow{err: pgx.ErrNoRows}
	}
	result := tx.queryRowList[0]
	tx.queryRowList = tx.queryRowList[1:]
	return result
}

func (tx *factualSQLTransaction) Commit(context.Context) error {
	tx.commitCalls++
	return nil
}

func (tx *factualSQLTransaction) Rollback(context.Context) error {
	tx.rollbackCalls++
	return nil
}

func newFactualSQLRepository() (*Repository, *factualSQLStarter) {
	tx := &factualSQLTransaction{queryRows: make(map[string][]pgx.Rows)}
	starter := &factualSQLStarter{tx: tx}
	return newRepositoryWithStarter(starter), starter
}

func factualSnapshotLockRow(input FactualSnapshotInput, capturedAt time.Time) pgx.Row {
	return fakeRow{values: []any{
		input.Scope.OrganizationID,
		input.Scope.SourceID,
		input.Scope.SnapshotID,
		capturedAt,
	}}
}

func factualSupportRows(input PreparedFactualSnapshot) map[string][]pgx.Rows {
	rows := make(map[string][]pgx.Rows)
	for _, item := range input.Facts {
		qualifierValues := make([][]any, 0, len(item.Qualifiers))
		for _, qualifier := range item.Qualifiers {
			qualifierValues = append(qualifierValues, []any{qualifier.FactID, qualifier.Ordinal, qualifier.Name, []byte(qualifier.TypedValue)})
		}
		rows[selectFactualQualifiersSQL] = append(rows[selectFactualQualifiersSQL], &repositoryFakeRows{values: qualifierValues})

		evidenceValues := make([][]any, 0, len(item.Evidence))
		for _, evidence := range item.Evidence {
			evidenceValues = append(evidenceValues, []any{evidence.FactID, evidence.EvidenceUnitID, evidence.Ordinal})
		}
		rows[selectFactualEvidenceLinksSQL] = append(rows[selectFactualEvidenceLinksSQL], &repositoryFakeRows{values: evidenceValues})

		inputValues := make([][]any, 0, len(item.Inputs))
		for _, lineageInput := range item.Inputs {
			inputValues = append(inputValues, []any{lineageInput.FactID, lineageInput.InputFactID, lineageInput.RuleVersionID, factualFactKindDerived, lineageInput.Ordinal})
		}
		rows[selectFactualInputsSQL] = append(rows[selectFactualInputsSQL], &repositoryFakeRows{values: inputValues})
	}
	return rows
}

func TestPersistFactualSnapshotValidatesBeforeBegin(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualSnapshotFixture(t)
	input.Scope.SourceID = "other-source"

	err := repository.PersistFactualSnapshot(context.Background(), input)
	if !errors.Is(err, ErrInvalidFactualSnapshot) {
		t.Fatalf("error = %v, want invalid factual snapshot", err)
	}
	if starter.beginCalls != 0 {
		t.Fatalf("Begin calls = %d, want 0", starter.beginCalls)
	}
}

func TestPersistFactualSnapshotMetricsRejectedBeforeBegin(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualSnapshotFixture(t)
	input.Scope.SourceID = "other-source"
	var records []FactualMetricsRecord
	repository.factualMetricsRecorder = FactualMetricsRecorderFunc(func(_ context.Context, record FactualMetricsRecord) {
		records = append(records, record)
	})

	err := repository.PersistFactualSnapshot(context.Background(), input)
	if !errors.Is(err, ErrInvalidFactualSnapshot) {
		t.Fatalf("error = %v, want invalid factual snapshot", err)
	}
	if starter.beginCalls != 0 {
		t.Fatalf("Begin calls = %d, want 0", starter.beginCalls)
	}
	if len(records) != 1 {
		t.Fatalf("metrics records = %d, want 1", len(records))
	}
	if records[0].Operation != FactualMetricsOperationPersistFactualSnapshot || records[0].Outcome != FactualMetricsOutcomeRejected ||
		records[0].Metrics != (FactualMetrics{Rejected: int64(len(input.Facts))}) {
		t.Fatalf("rejected metrics = %#v, want %d rejected", records[0], len(input.Facts))
	}
}

func TestPersistFactualSnapshotMetricsArePublishedAfterRollback(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualSnapshotFixture(t)
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	capturedAt := time.Date(2026, time.August, 23, 19, 20, 21, 0, time.UTC)
	starter.tx.queryRowList = []pgx.Row{factualSnapshotLockRow(input, capturedAt)}
	starter.tx.queryRows = factualSupportRows(prepared)
	starter.tx.execErrorAt = 2
	var rollbackAtRecord int
	var records []FactualMetricsRecord
	repository.factualMetricsRecorder = FactualMetricsRecorderFunc(func(_ context.Context, record FactualMetricsRecord) {
		rollbackAtRecord = starter.tx.rollbackCalls
		records = append(records, record)
	})

	err = repository.PersistFactualSnapshot(context.Background(), input)
	if !errors.Is(err, ErrDatabase) {
		t.Fatalf("error = %v, want database error", err)
	}
	if starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
		t.Fatalf("commit/rollback = %d/%d, want 0/1", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
	if rollbackAtRecord != 1 {
		t.Fatalf("rollback calls observed by recorder = %d, want 1", rollbackAtRecord)
	}
	if len(records) != 1 {
		t.Fatalf("metrics records = %d, want 1", len(records))
	}
	want := FactualMetrics{Rejected: int64(len(input.Facts))}
	if records[0].Operation != FactualMetricsOperationPersistFactualSnapshot || records[0].Outcome != FactualMetricsOutcomeRejected || records[0].Metrics != want {
		t.Fatalf("rollback metrics = %#v, want %#v", records[0], want)
	}
}

func TestPersistFactualSnapshotLocksSnapshotUsesCapturedAtAndOrdersWrites(t *testing.T) {
	repository, starter := newFactualSQLRepository()
	input := factualSnapshotFixture(t)
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("prepare fixture: %v", err)
	}
	capturedAt := time.Date(2026, time.August, 23, 19, 20, 21, 0, time.UTC)
	starter.tx.queryRowList = []pgx.Row{factualSnapshotLockRow(input, capturedAt)}
	starter.tx.queryRows = factualSupportRows(prepared)
	var records []FactualMetricsRecord
	repository.factualMetricsRecorder = FactualMetricsRecorderFunc(func(_ context.Context, record FactualMetricsRecord) {
		records = append(records, record)
	})

	if err := repository.PersistFactualSnapshot(context.Background(), input); err != nil {
		t.Fatalf("PersistFactualSnapshot() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("metrics records = %d, want 1", len(records))
	}
	wantMetrics := FactualMetrics{Accepted: 2, Derived: 1}
	if records[0].Operation != FactualMetricsOperationPersistFactualSnapshot || records[0].Outcome != FactualMetricsOutcomeCommitted || records[0].Metrics != wantMetrics {
		t.Fatalf("committed metrics = %#v, want %#v", records[0], wantMetrics)
	}
	if starter.tx.commitCalls != 1 || starter.tx.rollbackCalls != 0 {
		t.Fatalf("commit/rollback = %d/%d, want 1/0", starter.tx.commitCalls, starter.tx.rollbackCalls)
	}
	if len(starter.tx.execs) == 0 {
		t.Fatal("no factual writes recorded")
	}
	firstManifest, firstSchema, firstRule, firstFact, firstQualifier := -1, -1, -1, -1, -1
	for index, query := range starter.tx.execs {
		switch {
		case strings.Contains(query, "INSERT INTO frontend_manifests") && firstManifest < 0:
			firstManifest = index
		case strings.Contains(query, "INSERT INTO frontend_extension_schemas") && firstSchema < 0:
			firstSchema = index
		case strings.Contains(query, "INSERT INTO rule_versions") && firstRule < 0:
			firstRule = index
		case strings.Contains(query, "INSERT INTO canonical_facts") && firstFact < 0:
			firstFact = index
		case strings.Contains(query, "INSERT INTO canonical_fact_qualifiers") && firstQualifier < 0:
			firstQualifier = index
		}
	}
	if firstManifest < 0 || firstRule < 0 || firstFact < 0 || firstQualifier < 0 {
		t.Fatalf("write classes missing: manifest=%d rule=%d fact=%d qualifier=%d", firstManifest, firstRule, firstFact, firstQualifier)
	}
	if firstSchema >= 0 && firstManifest > firstSchema {
		t.Fatal("extension schema was written before its manifest")
	}
	if firstSchema >= 0 && firstSchema > firstRule {
		t.Fatal("rule version was written before extension schemas")
	}
	if firstRule > firstFact || firstFact > firstQualifier {
		t.Fatal("factual write order is not manifest/schema/rule/core/support")
	}
	for _, args := range starter.tx.execArgs {
		for _, arg := range args {
			if timestamp, ok := arg.(time.Time); ok && timestamp.Equal(capturedAt) {
				return
			}
		}
	}
	t.Fatal("captured snapshot timestamp was not used in factual writes")
}

func TestPersistFactualSnapshotSnapshotErrorsAndRollback(t *testing.T) {
	tests := []struct {
		name        string
		row         pgx.Row
		want        error
		execErrorAt int
		wantSecret  bool
	}{
		{name: "missing snapshot", row: fakeRow{err: pgx.ErrNoRows}, want: ErrNotFound},
		{name: "scope mismatch", row: fakeRow{values: []any{"wrong-org", "source-app", "snapshot-1", time.Unix(1, 0).UTC()}}, want: ErrConflict},
		{name: "intermediate write failure", row: nil, execErrorAt: 2, want: ErrDatabase, wantSecret: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository, starter := newFactualSQLRepository()
			input := factualSnapshotFixture(t)
			if tt.row == nil {
				prepared, err := PrepareFactualSnapshot(input)
				if err != nil {
					t.Fatalf("prepare fixture: %v", err)
				}
				starter.tx.queryRows = factualSupportRows(prepared)
				starter.tx.execErrorAt = tt.execErrorAt
			}
			starter.tx.queryRowList = []pgx.Row{factualSnapshotLockRow(input, time.Unix(1, 0).UTC())}
			if tt.row != nil {
				starter.tx.queryRowList[0] = tt.row
			}

			err := repository.PersistFactualSnapshot(context.Background(), input)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if starter.tx.commitCalls != 0 || starter.tx.rollbackCalls != 1 {
				t.Fatalf("commit/rollback = %d/%d, want 0/1", starter.tx.commitCalls, starter.tx.rollbackCalls)
			}
			if tt.wantSecret && strings.Contains(err.Error(), "secret factual database detail") {
				t.Fatalf("database detail leaked: %v", err)
			}
		})
	}
}

func TestPersistFactualSnapshotExistingManifestComparison(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PreparedFrontendManifest)
		want   error
	}{
		{name: "identical retry"},
		{name: "different immutable value", mutate: func(manifest *PreparedFrontendManifest) { manifest.Digest = strings.Repeat("f", 64) }, want: ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repository, starter := newFactualSQLRepository()
			input := factualSnapshotFixture(t)
			input.Facts = nil
			prepared, err := PrepareFactualSnapshot(input)
			if err != nil {
				t.Fatalf("prepare fixture: %v", err)
			}
			stored := prepared.FrontendManifests[0]
			if tt.mutate != nil {
				tt.mutate(&stored)
			}
			starter.tx.queryRowList = []pgx.Row{
				factualSnapshotLockRow(input, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
				fakeRow{values: []any{
					input.SourceID, input.SnapshotID, stored.ExternalID, stored.Manifest.ManifestVersion, stored.Manifest.ID,
					stored.Manifest.Version, stored.Manifest.Method, string(stored.Manifest.Execution),
					[]byte(stored.CanonicalJSON), stored.Digest,
				}},
			}
			if tt.mutate == nil {
				// The stored row must represent the unmodified prepared value.
				starter.tx.queryRowList[1] = fakeRow{values: []any{
					input.SourceID, input.SnapshotID,
					prepared.FrontendManifests[0].ExternalID,
					prepared.FrontendManifests[0].Manifest.ManifestVersion,
					prepared.FrontendManifests[0].Manifest.ID,
					prepared.FrontendManifests[0].Manifest.Version,
					prepared.FrontendManifests[0].Manifest.Method,
					string(prepared.FrontendManifests[0].Manifest.Execution),
					[]byte(prepared.FrontendManifests[0].CanonicalJSON),
					prepared.FrontendManifests[0].Digest,
				}}
			}
			starter.tx.execRows = []int64{0}

			err = repository.PersistFactualSnapshot(context.Background(), input)
			if tt.want == nil && err != nil {
				t.Fatalf("identical retry error = %v", err)
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if tt.want == nil && starter.tx.commitCalls != 1 {
				t.Fatalf("commit calls = %d, want 1", starter.tx.commitCalls)
			}
		})
	}
}
