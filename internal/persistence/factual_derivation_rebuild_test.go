package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/fact"
)

func TestRebuildFactualDerivationsReadsBeforeDerivingAndPublishesAdditively(t *testing.T) {
	t.Parallel()

	stored, observed := derivationRebuildStoredSnapshot(t)
	newRule := derivationRebuildRule("2")
	newDerived := validFact(stored.Scope, "derived-v2", fact.Producer{
		ID: "dependency", Version: "2", Method: "dependency",
	}, &fact.Lineage{
		RuleID: "dependency", RuleVersion: "2", InputFactIDs: []string{observed.ID},
	})
	output := []fact.CanonicalFact{observed, newDerived}
	persisted := stored
	persisted.RuleVersions = []RuleVersion{newRule}
	persisted.Facts = output

	readTx := &factualSQLTransaction{}
	configureDerivationRead(t, readTx, stored)
	persistTx := &factualSQLTransaction{}
	configureDerivationPersist(t, persistTx, persisted)
	starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx, persistTx}}
	repository := newRepositoryWithStarter(starter)
	deriver := factualDeriverFunc(func(_ context.Context, input []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
		if starter.beginCalls != 1 {
			t.Fatalf("deriver started with %d transactions, want read transaction only", starter.beginCalls)
		}
		if len(input) != 1 || input[0].ID != observed.ID {
			t.Fatalf("deriver input = %#v, want observed-only input", input)
		}
		return cloneCanonicalFacts(output), nil
	})

	err := repository.RebuildFactualDerivations(
		context.Background(),
		stored.OrganizationID,
		stored.SourceID,
		stored.SnapshotID,
		deriver,
		[]RuleVersion{newRule},
	)
	if err != nil {
		t.Fatalf("RebuildFactualDerivations() error = %v", err)
	}
	if starter.beginCalls != 2 || readTx.commitCalls != 1 || persistTx.commitCalls != 1 {
		t.Fatalf("transactions = begins:%d read commits:%d persist commits:%d, want 2/1/1", starter.beginCalls, readTx.commitCalls, persistTx.commitCalls)
	}
	if readTx.rollbackCalls != 0 || persistTx.rollbackCalls != 0 {
		t.Fatalf("rollbacks = read:%d persist:%d, want zero", readTx.rollbackCalls, persistTx.rollbackCalls)
	}
	for _, query := range persistTx.execs {
		if strings.Contains(query, "DELETE") {
			t.Fatalf("rebuild issued destructive query: %q", query)
		}
	}
}

func TestRebuildFactualDerivationsPreservesPreviousVersionOnRetry(t *testing.T) {
	t.Parallel()

	stored, observed := derivationRebuildStoredSnapshot(t)
	v2 := derivationRebuildRule("2")
	newDerived := validFact(stored.Scope, "derived-v2-retry", fact.Producer{
		ID: "dependency", Version: "2", Method: "dependency",
	}, &fact.Lineage{
		RuleID: "dependency", RuleVersion: "2", InputFactIDs: []string{observed.ID},
	})
	newDerived.Evidence[0].Locator.ArtifactID = "artifact-derived-v2-retry"
	firstPersisted := stored
	firstPersisted.RuleVersions = []RuleVersion{v2}
	firstPersisted.Facts = []fact.CanonicalFact{observed, newDerived}
	secondStored := stored
	secondStored.RuleVersions = append([]RuleVersion(nil), stored.RuleVersions...)
	secondStored.RuleVersions = append(secondStored.RuleVersions, v2)
	secondStored.Facts = append([]fact.CanonicalFact(nil), firstPersisted.Facts...)
	for _, candidate := range stored.Facts {
		if candidate.Lineage != nil {
			secondStored.Facts = append(secondStored.Facts, candidate)
			break
		}
	}
	secondPersisted := firstPersisted

	transactions := make([]*factualSQLTransaction, 0, 4)
	for index, snapshot := range []FactualSnapshotInput{stored, secondStored} {
		readTx := &factualSQLTransaction{}
		configureDerivationRead(t, readTx, snapshot)
		transactions = append(transactions, readTx)
		persistTx := &factualSQLTransaction{}
		configureDerivationPersist(t, persistTx, mapDerivationPersistSnapshot(snapshot, secondPersisted, index == 1))
		transactions = append(transactions, persistTx)
	}
	starter := &derivationRebuildStarter{transactions: transactions}
	repository := newRepositoryWithStarter(starter)
	deriver := factualDeriverFunc(func(_ context.Context, input []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
		if len(input) != 1 || input[0].ID != observed.ID {
			t.Fatalf("retry deriver input = %#v, want one observed fact", input)
		}
		return cloneCanonicalFacts(firstPersisted.Facts), nil
	})

	for attempt := 0; attempt < 2; attempt++ {
		if err := repository.RebuildFactualDerivations(
			context.Background(),
			stored.OrganizationID,
			stored.SourceID,
			stored.SnapshotID,
			deriver,
			[]RuleVersion{v2},
		); err != nil {
			t.Fatalf("retry %d error = %v", attempt+1, err)
		}
	}
	if starter.beginCalls != 4 {
		t.Fatalf("transaction begins = %d, want four independent read/write phases", starter.beginCalls)
	}
	for index, transaction := range transactions {
		if index%2 == 0 && transaction.commitCalls != 1 {
			t.Fatalf("read transaction %d commits = %d, want 1", index, transaction.commitCalls)
		}
		if index%2 == 1 && transaction.commitCalls != 1 {
			t.Fatalf("persist transaction %d commits = %d, want 1", index, transaction.commitCalls)
		}
		for _, query := range transaction.execs {
			if strings.Contains(query, "DELETE") {
				t.Fatalf("retry issued destructive query: %q", query)
			}
		}
	}
}

func TestRebuildFactualDerivationsRejectsChangedObservedWithoutPublishing(t *testing.T) {
	t.Parallel()

	stored, observed := derivationRebuildStoredSnapshot(t)
	readTx := &factualSQLTransaction{}
	configureDerivationRead(t, readTx, stored)
	persistTx := &factualSQLTransaction{}
	starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx, persistTx}}
	repository := newRepositoryWithStarter(starter)
	changed := cloneCanonicalFact(observed)
	changed.Lineage = &fact.Lineage{RuleID: "dependency", RuleVersion: "2", InputFactIDs: []string{observed.ID}}
	deriver := factualDeriverFunc(func(_ context.Context, _ []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
		return []fact.CanonicalFact{changed}, nil
	})

	err := repository.RebuildFactualDerivations(
		context.Background(),
		stored.OrganizationID,
		stored.SourceID,
		stored.SnapshotID,
		deriver,
		[]RuleVersion{derivationRebuildRule("2")},
	)
	if !errors.Is(err, ErrInvalidDerivationOutput) || starter.beginCalls != 1 {
		t.Fatalf("error/begins = %v/%d, want invalid output and read-only phase", err, starter.beginCalls)
	}
	if persistTx.commitCalls != 0 || persistTx.rollbackCalls != 0 || len(persistTx.execs) != 0 {
		t.Fatalf("persist transaction was touched after invalid output: %#v", persistTx)
	}
}

func TestRebuildFactualDerivationsSanitizesFailureAndCancellation(t *testing.T) {
	t.Parallel()

	stored, _ := derivationRebuildStoredSnapshot(t)
	t.Run("deriver failure", func(t *testing.T) {
		readTx := &factualSQLTransaction{}
		configureDerivationRead(t, readTx, stored)
		persistTx := &factualSQLTransaction{}
		starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx, persistTx}}
		repository := newRepositoryWithStarter(starter)
		secret := "secret-factual-derivation-output"
		deriver := factualDeriverFunc(func(context.Context, []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
			return nil, errors.New(secret)
		})
		err := repository.RebuildFactualDerivations(
			context.Background(), stored.OrganizationID, stored.SourceID, stored.SnapshotID,
			deriver, []RuleVersion{derivationRebuildRule("2")},
		)
		if !errors.Is(err, ErrInvalidDerivation) || strings.Contains(err.Error(), secret) {
			t.Fatalf("sanitized error = %v", err)
		}
		if starter.beginCalls != 1 || persistTx.commitCalls != 0 {
			t.Fatalf("failure transactions = begins:%d persist commits:%d, want 1/0", starter.beginCalls, persistTx.commitCalls)
		}
	})

	t.Run("canceled before read", func(t *testing.T) {
		readTx := &factualSQLTransaction{}
		starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx}}
		repository := newRepositoryWithStarter(starter)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := repository.RebuildFactualDerivations(
			ctx, stored.OrganizationID, stored.SourceID, stored.SnapshotID,
			factualDeriverFunc(func(context.Context, []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
				t.Fatal("deriver called after cancellation")
				return nil, nil
			}), []RuleVersion{derivationRebuildRule("2")},
		)
		if !errors.Is(err, context.Canceled) || starter.beginCalls != 0 {
			t.Fatalf("canceled rebuild = %v, begins %d; want canceled/0", err, starter.beginCalls)
		}
	})

	t.Run("canceled by deriver", func(t *testing.T) {
		readTx := &factualSQLTransaction{}
		configureDerivationRead(t, readTx, stored)
		persistTx := &factualSQLTransaction{}
		starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx, persistTx}}
		repository := newRepositoryWithStarter(starter)
		ctx, cancel := context.WithCancel(context.Background())
		deriver := factualDeriverFunc(func(context.Context, []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
			cancel()
			return nil, nil
		})
		err := repository.RebuildFactualDerivations(
			ctx, stored.OrganizationID, stored.SourceID, stored.SnapshotID,
			deriver, []RuleVersion{derivationRebuildRule("2")},
		)
		if !errors.Is(err, context.Canceled) || starter.beginCalls != 1 || persistTx.commitCalls != 0 {
			t.Fatalf("deriver cancellation = %v, begins:%d persist commits:%d; want canceled/1/0", err, starter.beginCalls, persistTx.commitCalls)
		}
	})
}

type factualDeriverFunc func(context.Context, []fact.CanonicalFact) ([]fact.CanonicalFact, error)

func (f factualDeriverFunc) Derive(ctx context.Context, facts []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
	return f(ctx, facts)
}

type derivationRebuildStarter struct {
	transactions []*factualSQLTransaction
	beginCalls   int
}

func (s *derivationRebuildStarter) Begin(context.Context) (transaction, error) {
	if s.beginCalls >= len(s.transactions) {
		return nil, errors.New("test transaction sequence exhausted")
	}
	result := s.transactions[s.beginCalls]
	s.beginCalls++
	return result, nil
}

func derivationRebuildStoredSnapshot(t *testing.T) (FactualSnapshotInput, fact.CanonicalFact) {
	t.Helper()
	fixture := factualSnapshotFixture(t)
	fixture.RuleVersions = []RuleVersion{fixture.RuleVersions[1]}
	for index := range fixture.Facts {
		for evidenceIndex := range fixture.Facts[index].Evidence {
			fixture.Facts[index].Evidence[evidenceIndex].Locator.ArtifactID = "artifact-" + fixture.Facts[index].Subject.ID
		}
	}
	prepared, err := PrepareFactualSnapshot(fixture)
	if err != nil {
		t.Fatalf("prepare stored derivation fixture: %v", err)
	}
	fixture = factualInputFromPrepared(prepared)
	for _, candidate := range fixture.Facts {
		if candidate.Lineage == nil {
			return fixture, candidate
		}
	}
	t.Fatal("factual fixture has no observed fact")
	return FactualSnapshotInput{}, fact.CanonicalFact{}
}

func derivationRebuildRule(version string) RuleVersion {
	return RuleVersion{
		RuleID:               "dependency",
		Version:              version,
		ImplementationDigest: strings.Repeat(version, 64),
		Configuration:        json.RawMessage(`{"mode":"test"}`),
	}
}

func mapDerivationPersistSnapshot(snapshot, expected FactualSnapshotInput, second bool) FactualSnapshotInput {
	if second {
		return expected
	}
	result := snapshot
	result.RuleVersions = expected.RuleVersions
	result.Facts = expected.Facts
	return result
}

func configureDerivationRead(t *testing.T, tx *factualSQLTransaction, input FactualSnapshotInput) {
	t.Helper()
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("prepare derivation read fixture: %v", err)
	}
	capturedAt := time.Date(2026, time.August, 24, 1, 2, 3, 0, time.UTC)
	organizationID, sourceID, snapshotID := input.OrganizationID, input.SourceID, input.SnapshotID
	tx.queryRowList = []pgx.Row{factualSnapshotLockRow(input, capturedAt)}
	tx.queryRows = make(map[string][]pgx.Rows)
	tx.queryRows[readFactualManifestsSQL] = []pgx.Rows{&repositoryFakeRows{values: factualProjectionManifestRows(prepared, organizationID, sourceID, snapshotID)}}
	tx.queryRows[readFactualFactsSQL] = []pgx.Rows{&repositoryFakeRows{values: factualProjectionFactRows(prepared, organizationID, sourceID, snapshotID, capturedAt)}}
	tx.queryRows[readFactualQualifiersSQL] = []pgx.Rows{&factualDerivationQualifierRowsReader{repositoryFakeRows: &repositoryFakeRows{values: factualDerivationQualifierRows(prepared, organizationID, sourceID, snapshotID)}}}
	tx.queryRows[readFactualEvidenceSQL] = []pgx.Rows{&repositoryFakeRows{values: factualProjectionEvidenceRows(prepared, input.Scope)}}
	tx.queryRows[readFactualInputsSQL] = []pgx.Rows{&repositoryFakeRows{values: factualDerivationInputRows(prepared, organizationID, sourceID, snapshotID)}}
	tx.queryRows[readFactualRulesSQL] = []pgx.Rows{&repositoryFakeRows{values: factualDerivationRuleRows(prepared, organizationID)}}
}

func configureDerivationPersist(t *testing.T, tx *factualSQLTransaction, input FactualSnapshotInput) {
	t.Helper()
	prepared, err := PrepareFactualSnapshot(input)
	if err != nil {
		t.Fatalf("prepare derivation persist fixture: %v", err)
	}
	tx.queryRowList = []pgx.Row{factualSnapshotLockRow(input, time.Date(2026, time.August, 24, 1, 2, 3, 0, time.UTC))}
	tx.queryRows = factualSupportRows(prepared)
}

func factualDerivationQualifierRows(prepared PreparedFactualSnapshot, organizationID, sourceID, snapshotID string) [][]any {
	rows := make([][]any, 0)
	for _, item := range prepared.Facts {
		for _, qualifier := range item.Qualifiers {
			rows = append(rows, []any{
				qualifier.ID, organizationID, sourceID, snapshotID,
				qualifier.FactID, qualifier.Ordinal, qualifier.Name, []byte(qualifier.TypedValue),
			})
		}
	}
	sort.Slice(rows, func(left, right int) bool {
		leftRow, rightRow := rows[left], rows[right]
		if leftRow[4].(string) != rightRow[4].(string) {
			return leftRow[4].(string) < rightRow[4].(string)
		}
		if leftRow[5].(int64) != rightRow[5].(int64) {
			return leftRow[5].(int64) < rightRow[5].(int64)
		}
		if leftRow[6].(string) != rightRow[6].(string) {
			return leftRow[6].(string) < rightRow[6].(string)
		}
		return leftRow[0].(string) < rightRow[0].(string)
	})
	return rows
}

func factualDerivationInputRows(prepared PreparedFactualSnapshot, organizationID, sourceID, snapshotID string) [][]any {
	rows := make([][]any, 0)
	for _, item := range prepared.Facts {
		for _, input := range item.Inputs {
			rows = append(rows, []any{
				input.ID, organizationID, sourceID, snapshotID,
				input.FactID, input.InputFactID, input.RuleVersionID, factualFactKindDerived, input.Ordinal,
			})
		}
	}
	sort.Slice(rows, func(left, right int) bool {
		leftRow, rightRow := rows[left], rows[right]
		if leftRow[4].(string) != rightRow[4].(string) {
			return leftRow[4].(string) < rightRow[4].(string)
		}
		if leftRow[8].(int64) != rightRow[8].(int64) {
			return leftRow[8].(int64) < rightRow[8].(int64)
		}
		if leftRow[5].(string) != rightRow[5].(string) {
			return leftRow[5].(string) < rightRow[5].(string)
		}
		return leftRow[0].(string) < rightRow[0].(string)
	})
	return rows
}

func factualDerivationRuleRows(prepared PreparedFactualSnapshot, organizationID string) [][]any {
	rows := make([][]any, 0, len(prepared.RuleVersions))
	for _, rule := range prepared.RuleVersions {
		rows = append(rows, []any{
			rule.ID, organizationID, rule.RuleID, rule.Version,
			rule.ImplementationDigest, []byte(rule.Configuration),
		})
	}
	sort.Slice(rows, func(left, right int) bool {
		leftRow, rightRow := rows[left], rows[right]
		if leftRow[2].(string) != rightRow[2].(string) {
			return leftRow[2].(string) < rightRow[2].(string)
		}
		if leftRow[3].(string) != rightRow[3].(string) {
			return leftRow[3].(string) < rightRow[3].(string)
		}
		return leftRow[0].(string) < rightRow[0].(string)
	})
	return rows
}

// repositoryFakeRows models the pgx driver closely enough for most factual
// reads, but qualifiers scan their JSON column into json.RawMessage. Keep the
// adapter local to this rebuild test instead of changing shared test helpers.
type factualDerivationQualifierRowsReader struct {
	*repositoryFakeRows
}

func (r *factualDerivationQualifierRowsReader) Scan(dest ...any) error {
	if len(dest) != 8 {
		return r.repositoryFakeRows.Scan(dest...)
	}
	if r.closed || r.index >= len(r.values) {
		return errors.New("scan outside fake qualifier row")
	}
	values := r.values[r.index]
	if len(values) != len(dest) {
		return fmt.Errorf("fake qualifier scan values = %d, destinations = %d", len(values), len(dest))
	}
	for index := 0; index < len(dest)-1; index++ {
		if err := assignValue(values[index], dest[index]); err != nil {
			return fmt.Errorf("fake qualifier scan value %d: %w", index, err)
		}
	}
	typedValue, ok := dest[len(dest)-1].(*json.RawMessage)
	if !ok {
		return fmt.Errorf("fake qualifier destination = %T", dest[len(dest)-1])
	}
	encoded, ok := values[len(values)-1].([]byte)
	if !ok {
		return fmt.Errorf("fake qualifier value = %T", values[len(values)-1])
	}
	*typedValue = append((*typedValue)[:0], encoded...)
	r.index++
	if r.index >= len(r.values) {
		r.closed = true
	}
	return nil
}

func TestRebuildFactualDerivationsUsesCanonicalPersistedScope(t *testing.T) {
	t.Parallel()

	stored, _ := derivationRebuildStoredSnapshot(t)
	readTx := &factualSQLTransaction{}
	configureDerivationRead(t, readTx, stored)
	starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx}}
	repository := newRepositoryWithStarter(starter)
	deriver := factualDeriverFunc(func(_ context.Context, input []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
		return cloneCanonicalFacts(input), nil
	})
	invalidRule := RuleVersion{RuleID: "dependency", Version: "2", ImplementationDigest: "secret-invalid-digest"}
	err := repository.RebuildFactualDerivations(
		context.Background(), stored.OrganizationID, stored.SourceID, stored.SnapshotID,
		deriver, []RuleVersion{invalidRule},
	)
	if !errors.Is(err, ErrInvalidDerivation) || strings.Contains(err.Error(), "secret-invalid-digest") {
		t.Fatalf("invalid rule error = %v, want sanitized invalid derivation", err)
	}
	if starter.beginCalls != 1 {
		t.Fatalf("invalid rule begins = %d, want read only", starter.beginCalls)
	}
}

func TestRebuildFactualDerivationsKeepsObservedInputDetached(t *testing.T) {
	t.Parallel()

	stored, observed := derivationRebuildStoredSnapshot(t)
	readTx := &factualSQLTransaction{}
	configureDerivationRead(t, readTx, stored)
	persisted := stored
	persisted.RuleVersions = []RuleVersion{derivationRebuildRule("2")}
	persisted.Facts = []fact.CanonicalFact{observed}
	persistTx := &factualSQLTransaction{}
	configureDerivationPersist(t, persistTx, persisted)
	starter := &derivationRebuildStarter{transactions: []*factualSQLTransaction{readTx, persistTx}}
	repository := newRepositoryWithStarter(starter)
	deriver := factualDeriverFunc(func(_ context.Context, input []fact.CanonicalFact) ([]fact.CanonicalFact, error) {
		input[0].Evidence[0].ID = "mutated-by-deriver"
		return []fact.CanonicalFact{observed}, nil
	})
	if err := repository.RebuildFactualDerivations(
		context.Background(), stored.OrganizationID, stored.SourceID, stored.SnapshotID,
		deriver, []RuleVersion{derivationRebuildRule("2")},
	); err != nil {
		t.Fatalf("detached rebuild error = %v", err)
	}
	if persistTx.commitCalls != 1 {
		t.Fatalf("persist commits = %d, want 1", persistTx.commitCalls)
	}
}

var _ FactualDeriver = factualDeriverFunc(nil)
