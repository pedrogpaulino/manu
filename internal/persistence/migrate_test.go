package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/pedrogpaulino/manu/internal/persistence/migrations"
)

func TestMigrationRunnerApplyIsOrderedAtomicAndIdempotent(t *testing.T) {
	session := newFakeSession()
	runner := newTestRunner(t, session)

	first, err := runner.Apply(context.Background())
	if err != nil {
		t.Fatalf("first Apply() error = %v", err)
	}
	if !first.Ready || first.Current != 2 || first.Latest != 2 || len(first.Applied) != 2 {
		t.Fatalf("first status = %#v, want ready current/latest 2 and two rows", first)
	}
	if session.lockCalls != 1 || session.unlockCalls != 1 || session.lockHeld {
		t.Fatalf("lock lifecycle = %d/%d held=%t, want one acquire/release and no held lock", session.lockCalls, session.unlockCalls, session.lockHeld)
	}
	if session.beginCalls != 2 || len(session.transactions) != 2 {
		t.Fatalf("transactions = %d/%d, want one transaction per migration", session.beginCalls, len(session.transactions))
	}
	for index, tx := range session.transactions {
		if !tx.committed || tx.rolledBack {
			t.Errorf("transaction %d committed=%t rolledBack=%t", index+1, tx.committed, tx.rolledBack)
		}
		if len(tx.execs) != 2 || !strings.HasPrefix(strings.TrimSpace(tx.execs[1]), "INSERT INTO manu_schema_migrations") {
			t.Errorf("transaction %d execs = %q, want migration SQL then history INSERT", index+1, tx.execs)
		}
	}
	if got, want := session.history[0].Version, int64(1); got != want {
		t.Fatalf("first history version = %d, want %d", got, want)
	}
	if got, want := session.history[1].Version, int64(2); got != want {
		t.Fatalf("second history version = %d, want %d", got, want)
	}

	beginCalls := session.beginCalls
	second, err := runner.Apply(context.Background())
	if err != nil {
		t.Fatalf("repeat Apply() error = %v", err)
	}
	if !second.Ready || second.Current != 2 || len(second.Applied) != 2 {
		t.Fatalf("repeat status = %#v, want unchanged ready status", second)
	}
	if session.beginCalls != beginCalls || len(session.history) != 2 {
		t.Fatalf("repeat changed transactions/history: begin=%d history=%d", session.beginCalls, len(session.history))
	}
}

func TestMigrationRunnerStatusReportsPendingSchema(t *testing.T) {
	session := newFakeSession()
	session.historyTableCreated = true
	runner := newTestRunner(t, session)

	status, err := runner.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Ready || status.Current != 0 || status.Latest != 2 || len(status.Applied) != 0 {
		t.Fatalf("status = %#v, want pending empty schema", status)
	}
	if !session.historyTableCreated {
		t.Fatal("test schema unexpectedly lost its history table")
	}
	if session.lockCalls != 1 || session.unlockCalls != 1 {
		t.Fatalf("Status() lock lifecycle = %d/%d, want one acquire/release", session.lockCalls, session.unlockCalls)
	}
}

func TestMigrationRunnerStatusDoesNotBootstrapMissingSchema(t *testing.T) {
	session := newFakeSession()
	runner := newTestRunner(t, session)

	status, err := runner.Status(context.Background())
	if err == nil || !errors.Is(err, ErrMigrationSchemaMissing) {
		t.Fatalf("Status() error = %v, want ErrMigrationSchemaMissing", err)
	}
	if status.Current != 0 || status.Ready || session.historyTableCreated {
		t.Fatalf("missing-schema status = %#v tableCreated=%t, want current 0/not ready/no mutation", status, session.historyTableCreated)
	}
	if strings.Contains(err.Error(), "42P01") || strings.Contains(err.Error(), "schema_migrations") {
		t.Fatalf("missing-schema diagnostic exposes database detail: %v", err)
	}
	if session.lockCalls != 1 || session.unlockCalls != 1 || session.lockHeld {
		t.Fatalf("missing-schema lock lifecycle = %d/%d held=%t, want released lock", session.lockCalls, session.unlockCalls, session.lockHeld)
	}
}

func TestMigrationRunnerRejectsIncompatibleHistory(t *testing.T) {
	tests := []struct {
		name      string
		history   []AppliedMigration
		wantError error
	}{
		{
			name: "renamed migration",
			history: []AppliedMigration{{
				Version: 1, Name: "renamed.up.sql", Checksum: testMigrationChecksum(t, 1),
			}},
			wantError: ErrMigrationNameMismatch,
		},
		{
			name: "changed checksum",
			history: []AppliedMigration{{
				Version: 1, Name: "0001_first.up.sql", Checksum: strings.Repeat("f", 64),
			}},
			wantError: ErrMigrationChecksumMismatch,
		},
		{
			name: "missing earlier version",
			history: []AppliedMigration{{
				Version: 2, Name: "0002_second.up.sql", Checksum: testMigrationChecksum(t, 2),
			}},
			wantError: ErrMigrationGap,
		},
		{
			name: "database is ahead",
			history: []AppliedMigration{{
				Version: 3, Name: "0003_third.up.sql", Checksum: strings.Repeat("3", 64),
			}},
			wantError: ErrSchemaAhead,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newFakeSession()
			session.historyTableCreated = true
			session.history = append([]AppliedMigration(nil), test.history...)
			runner := newTestRunner(t, session)
			status, err := runner.Status(context.Background())
			if err == nil {
				t.Fatal("Status() error = nil, want incompatibility")
			}
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Status() error = %v, want errors.Is(..., %v)", err, test.wantError)
			}
			if status.Ready {
				t.Fatalf("incompatible status = %#v, want not ready", status)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "postgres://") {
				t.Fatalf("diagnostic exposes sensitive detail: %v", err)
			}
		})
	}
}

func TestMigrationRunnerFailureRollsBackAndDoesNotRecordVersion(t *testing.T) {
	session := newFakeSession()
	session.failMigrationVersion = 2
	session.failure = errors.New("SECRET SQL postgres://user:pass@example.invalid/db")
	runner := newTestRunner(t, session)

	status, err := runner.Apply(context.Background())
	if err == nil {
		t.Fatal("Apply() error = nil, want migration failure")
	}
	if !errors.Is(err, ErrMigrationDatabase) {
		t.Fatalf("Apply() error = %v, want ErrMigrationDatabase", err)
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "postgres://") {
		t.Fatalf("diagnostic exposes driver detail: %v", err)
	}
	if len(session.history) != 1 || session.history[0].Version != 1 {
		t.Fatalf("history after rollback = %#v, want only version 1", session.history)
	}
	if len(session.transactions) != 2 || !session.transactions[1].rolledBack || session.transactions[1].committed {
		t.Fatalf("failed transaction state = %#v, want rollback without commit", session.transactions[1])
	}
	if status.Ready || status.Current != 1 || len(status.Applied) != 1 || status.Applied[0].Version != 1 {
		t.Fatalf("failure status = %#v, want not-ready report with committed version 1", status)
	}
}

func TestMigrationRunnerFailureStatusRetainsLatest(t *testing.T) {
	tests := []struct {
		name             string
		lockFailure      bool
		bootstrapFailure bool
		wantError        error
	}{
		{name: "lock", lockFailure: true, wantError: ErrMigrationLock},
		{name: "bootstrap", bootstrapFailure: true, wantError: ErrMigrationDatabase},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newFakeSession()
			if test.lockFailure {
				session.lockFailure = errors.New("SECRET lock DSN")
			}
			if test.bootstrapFailure {
				session.bootstrapFailure = errors.New("SECRET bootstrap SQL")
			}
			runner := newTestRunner(t, session)

			status, err := runner.Apply(context.Background())
			if err == nil || !errors.Is(err, test.wantError) {
				t.Fatalf("Apply() error = %v, want %v", err, test.wantError)
			}
			if status.Latest != 2 || status.Current != 0 || status.Ready {
				t.Fatalf("failure status = %#v, want latest 2/current 0/not ready", status)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "DSN") {
				t.Fatalf("diagnostic exposes sensitive detail: %v", err)
			}
		})
	}
}

func TestMigrationRunnerCancellationStillReleasesLockAndRollsBack(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := newFakeSession()
	session.failMigrationVersion = 1
	session.cancelDuringMigration = cancel
	session.failure = context.Canceled
	runner := newTestRunner(t, session)

	_, err := runner.Apply(ctx)
	if err == nil {
		t.Fatal("Apply() error = nil, want cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply() error = %v, want context.Canceled in chain", err)
	}
	if session.lockHeld || session.unlockCalls != 1 {
		t.Fatalf("lock after cancellation held=%t unlocks=%d, want released", session.lockHeld, session.unlockCalls)
	}
	if len(session.transactions) != 1 || !session.transactions[0].rolledBack {
		t.Fatalf("transaction after cancellation = %#v, want rollback", session.transactions)
	}
	if session.unlockContextErr != nil {
		t.Fatalf("unlock used canceled context: %v", session.unlockContextErr)
	}
}

func TestMigrationRunnerLockFailuresAreSafe(t *testing.T) {
	tests := []struct {
		name       string
		failUnlock bool
		wantError  error
	}{
		{name: "acquire", wantError: ErrMigrationLock},
		{name: "release", failUnlock: true, wantError: ErrMigrationLock},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := newFakeSession()
			if test.failUnlock {
				session.unlockFailure = errors.New("SECRET unlock DSN")
			} else {
				session.lockFailure = errors.New("SECRET lock DSN")
			}
			runner := newTestRunner(t, session)
			status, err := runner.Status(context.Background())
			if err == nil || !errors.Is(err, test.wantError) {
				t.Fatalf("Status() error = %v, want %v", err, test.wantError)
			}
			if status.Ready {
				t.Fatalf("lock failure status = %#v, want not ready", status)
			}
			if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "DSN") {
				t.Fatalf("lock diagnostic exposes sensitive detail: %v", err)
			}
			if !test.failUnlock && session.historyTableCreated {
				t.Fatal("bootstrap ran after lock acquisition failure")
			}
		})
	}
}

func newTestRunner(t *testing.T, session *fakeSession) *Runner {
	t.Helper()
	catalog, err := migrations.LoadCatalog(fstest.MapFS{
		"0002_second.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE second (id integer);\n")},
		"0001_first.up.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	runner, err := NewMigrationRunner(session, catalog)
	if err != nil {
		t.Fatalf("NewMigrationRunner() error = %v", err)
	}
	return runner
}

func testMigrationChecksum(t *testing.T, version int64) string {
	t.Helper()
	if version == 1 {
		catalog, err := migrations.LoadCatalog(fstest.MapFS{
			"0001_first.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
		})
		if err != nil {
			t.Fatalf("LoadCatalog() error = %v", err)
		}
		migration, ok := catalog.Lookup(version)
		if !ok {
			t.Fatalf("Lookup(%d) did not find test migration", version)
		}
		return migration.Checksum
	}
	catalog, err := migrations.LoadCatalog(fstest.MapFS{
		"0001_first.up.sql":  &fstest.MapFile{Data: []byte("CREATE TABLE first (id integer);\n")},
		"0002_second.up.sql": &fstest.MapFile{Data: []byte("CREATE TABLE second (id integer);\n")},
	})
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}
	migration, ok := catalog.Lookup(version)
	if !ok {
		t.Fatalf("Lookup(%d) did not find test migration", version)
	}
	return migration.Checksum
}

type fakeSession struct {
	history               []AppliedMigration
	historyTableCreated   bool
	lockHeld              bool
	lockCalls             int
	unlockCalls           int
	beginCalls            int
	transactions          []*fakeTx
	lockFailure           error
	unlockFailure         error
	bootstrapFailure      error
	failure               error
	failMigrationVersion  int64
	cancelDuringMigration context.CancelFunc
	unlockContextErr      error
}

func newFakeSession() *fakeSession { return &fakeSession{} }

func (s *fakeSession) Exec(ctx context.Context, query string, args ...any) error {
	if strings.HasPrefix(strings.TrimSpace(query), "SELECT pg_advisory_lock") {
		s.lockCalls++
		if s.lockFailure != nil {
			return s.lockFailure
		}
		s.lockHeld = true
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(query), "SELECT pg_advisory_unlock") {
		s.unlockCalls++
		if err := ctx.Err(); err != nil {
			s.unlockContextErr = err
		}
		if s.unlockFailure != nil {
			return s.unlockFailure
		}
		s.lockHeld = false
		return nil
	}
	if strings.HasPrefix(strings.TrimSpace(query), "CREATE TABLE IF NOT EXISTS manu_schema_migrations") {
		if s.bootstrapFailure != nil {
			return s.bootstrapFailure
		}
		s.historyTableCreated = true
		return nil
	}
	return fmt.Errorf("unexpected session query: %s", query)
}

func (s *fakeSession) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	if !strings.HasPrefix(strings.TrimSpace(query), "SELECT version, name, checksum, applied_at") {
		return nil, fmt.Errorf("unexpected history query: %s", query)
	}
	if !s.historyTableCreated {
		return nil, fakeSQLStateError{code: "42P01"}
	}
	rows := make([]AppliedMigration, len(s.history))
	copy(rows, s.history)
	return &migrationFakeRows{rows: rows}, nil
}

type fakeSQLStateError struct {
	code string
}

func (e fakeSQLStateError) Error() string { return "relation does not exist" }

func (e fakeSQLStateError) SQLState() string { return e.code }

func (s *fakeSession) Begin(ctx context.Context) (Tx, error) {
	s.beginCalls++
	tx := &fakeTx{session: s}
	s.transactions = append(s.transactions, tx)
	return tx, nil
}

type migrationFakeRows struct {
	rows   []AppliedMigration
	index  int
	closed bool
	err    error
}

func (r *migrationFakeRows) Close() { r.closed = true }

func (r *migrationFakeRows) Err() error { return r.err }

func (r *migrationFakeRows) Next() bool {
	if r.index >= len(r.rows) {
		r.closed = true
		return false
	}
	r.index++
	return true
}

func (r *migrationFakeRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.rows) || len(dest) != 4 {
		return errors.New("invalid fake row scan")
	}
	row := r.rows[r.index-1]
	version, ok := dest[0].(*int64)
	if !ok {
		return errors.New("version destination is not *int64")
	}
	name, ok := dest[1].(*string)
	if !ok {
		return errors.New("name destination is not *string")
	}
	checksum, ok := dest[2].(*string)
	if !ok {
		return errors.New("checksum destination is not *string")
	}
	appliedAt, ok := dest[3].(*time.Time)
	if !ok {
		return errors.New("applied_at destination is not *time.Time")
	}
	*version, *name, *checksum, *appliedAt = row.Version, row.Name, row.Checksum, row.AppliedAt
	return nil
}

type fakeTx struct {
	session    *fakeSession
	execs      []string
	staged     *AppliedMigration
	committed  bool
	rolledBack bool
}

func (tx *fakeTx) Exec(ctx context.Context, query string, args ...any) error {
	tx.execs = append(tx.execs, query)
	if strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO manu_schema_migrations") {
		if tx.session.failure != nil && tx.session.failMigrationVersion == 0 {
			return tx.session.failure
		}
		version, ok := args[0].(int64)
		if !ok {
			return errors.New("version argument is not int64")
		}
		name, ok := args[1].(string)
		if !ok {
			return errors.New("name argument is not string")
		}
		checksum, ok := args[2].(string)
		if !ok {
			return errors.New("checksum argument is not string")
		}
		tx.staged = &AppliedMigration{Version: version, Name: name, Checksum: checksum, AppliedAt: time.Unix(0, 0).UTC()}
		return nil
	}
	if tx.session.failMigrationVersion > 0 && len(tx.session.transactions) == int(tx.session.failMigrationVersion) {
		if tx.session.cancelDuringMigration != nil {
			tx.session.cancelDuringMigration()
		}
		if tx.session.failure != nil {
			return tx.session.failure
		}
	}
	return nil
}

func (tx *fakeTx) Commit(ctx context.Context) error {
	if tx.staged != nil {
		tx.session.history = append(tx.session.history, *tx.staged)
	}
	tx.committed = true
	return nil
}

func (tx *fakeTx) Rollback(ctx context.Context) error {
	tx.rolledBack = true
	tx.staged = nil
	return nil
}
