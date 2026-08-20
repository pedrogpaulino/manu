package persistence

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/persistence/migrations"
)

const (
	// AdvisoryLockKey is stable across Manu processes so only one process can
	// bootstrap, inspect, or apply the schema at a time.
	AdvisoryLockKey int64 = 0x4d414e555f6d6967

	// cleanupTimeout bounds best-effort rollback and advisory-lock release even
	// when the request context has already been cancelled.
	cleanupTimeout = 5 * time.Second
)

const (
	createHistoryTableSQL = `
CREATE TABLE IF NOT EXISTS manu_schema_migrations (
    version bigint PRIMARY KEY CHECK (version > 0),
    name text NOT NULL CHECK (btrim(name) <> ''),
    checksum char(64) NOT NULL CHECK (checksum ~ '^[0-9a-f]{64}$'),
    applied_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
)`
	selectHistorySQL = `
SELECT version, name, checksum, applied_at
FROM manu_schema_migrations
ORDER BY version`
	insertHistorySQL = `
INSERT INTO manu_schema_migrations (version, name, checksum)
VALUES ($1, $2, $3)`
	lockSQL   = `SELECT pg_advisory_lock($1)`
	unlockSQL = `SELECT pg_advisory_unlock($1)`
)

var (
	// ErrInvalidMigrationCatalog identifies a catalog that cannot be applied.
	ErrInvalidMigrationCatalog = errors.New("migration: invalid catalog")
	// ErrSchemaAhead identifies database history newer than this binary.
	ErrSchemaAhead = errors.New("migration: schema ahead")
	// ErrMigrationGap identifies a missing or out-of-order history version.
	ErrMigrationGap = errors.New("migration: history gap")
	// ErrMigrationNameMismatch identifies a renamed migration at an applied
	// version.
	ErrMigrationNameMismatch = errors.New("migration: name mismatch")
	// ErrMigrationChecksumMismatch identifies changed SQL at an applied version.
	ErrMigrationChecksumMismatch = errors.New("migration: checksum mismatch")
	// ErrMigrationDatabase identifies a database operation that failed. Driver
	// diagnostics are deliberately not exposed through the returned error.
	ErrMigrationDatabase = errors.New("migration: database operation failed")
	// ErrMigrationLock identifies advisory-lock acquisition or release failure.
	ErrMigrationLock = errors.New("migration: advisory lock failed")
	// ErrMigrationSchemaMissing identifies a database without the history table.
	ErrMigrationSchemaMissing = errors.New("migration: schema history is missing")
)

// Rows is the small result-set surface required by the runner. It mirrors the
// subset needed from pgx.Rows and is intentionally easy to fake in unit tests.
type Rows interface {
	Close()
	Err() error
	Next() bool
	Scan(dest ...any) error
}

// Tx is the transaction surface required by one migration. The migration SQL
// and its history INSERT are always executed through the same Tx.
type Tx interface {
	Exec(ctx context.Context, query string, args ...any) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Session is a single PostgreSQL session adapter. Advisory locks are
// session-scoped, so a pool must not implement this interface by dispatching
// each method to an arbitrary connection. A production adapter can wrap
// *pgx.Conn; tests use a deterministic fake without a database.
type Session interface {
	Exec(ctx context.Context, query string, args ...any) error
	Query(ctx context.Context, query string, args ...any) (Rows, error)
	Begin(ctx context.Context) (Tx, error)
}

// AppliedMigration is the durable history row used in status reports.
type AppliedMigration struct {
	Version   int64     `json:"version"`
	Name      string    `json:"name"`
	Checksum  string    `json:"checksum"`
	AppliedAt time.Time `json:"applied_at"`
}

// Status reports compatibility and progress without exposing SQL or driver
// diagnostics.
type Status struct {
	Current int64              `json:"current"`
	Latest  int64              `json:"latest"`
	Applied []AppliedMigration `json:"applied"`
	Ready   bool               `json:"ready"`
}

// Runner applies a validated forward-only migration catalog to one database
// session. It holds no mutable global state and is safe to construct per
// process lifecycle.
type Runner struct {
	session Session
	catalog migrations.Catalog
}

// NewMigrationRunner validates dependencies and returns an explicit runner.
func NewMigrationRunner(session Session, catalog migrations.Catalog) (*Runner, error) {
	if session == nil {
		return nil, fmt.Errorf("%w: session is required", ErrInvalidMigrationCatalog)
	}
	if err := catalog.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMigrationCatalog, safeCatalogDetail(err))
	}
	return &Runner{session: session, catalog: catalog}, nil
}

// NewEmbeddedMigrationRunner loads the catalog compiled into the binary and
// constructs a runner for the supplied single PostgreSQL session.
func NewEmbeddedMigrationRunner(session Session) (*Runner, error) {
	catalog, err := migrations.EmbeddedCatalog()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMigrationCatalog, safeCatalogDetail(err))
	}
	return NewMigrationRunner(session, catalog)
}

// NewPGXMigrationRunner adapts one *pgx.Conn to the runner's small session
// boundary. A single connection is required because PostgreSQL advisory locks
// belong to the session that acquired them.
func NewPGXMigrationRunner(conn *pgx.Conn, catalog migrations.Catalog) (*Runner, error) {
	if conn == nil {
		return nil, fmt.Errorf("%w: postgres connection is required", ErrInvalidMigrationCatalog)
	}
	return NewMigrationRunner(pgxSession{conn: conn}, catalog)
}

// NewEmbeddedPGXMigrationRunner adapts one *pgx.Conn and the embedded catalog.
func NewEmbeddedPGXMigrationRunner(conn *pgx.Conn) (*Runner, error) {
	catalog, err := migrations.EmbeddedCatalog()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMigrationCatalog, safeCatalogDetail(err))
	}
	return NewPGXMigrationRunner(conn, catalog)
}

// Status acquires the global lock and inspects compatibility without mutating
// the database. Inspection never bootstraps the history table, so a missing
// schema remains visibly not ready for the caller to diagnose.
func (r *Runner) Status(ctx context.Context) (Status, error) {
	report := makeStatus(nil, r.catalog)
	if err := contextError(ctx); err != nil {
		return report, err
	}
	if err := r.withAdvisoryLock(ctx, func() error {
		applied, err := r.readApplied(ctx)
		if err != nil {
			return err
		}
		report = makeStatus(applied, r.catalog)
		if err := validateApplied(applied, r.catalog); err != nil {
			return err
		}
		report.Ready = len(applied) == len(r.catalog.Migrations())
		return nil
	}); err != nil {
		report.Ready = false
		return report, err
	}
	return report, nil
}

// Inspect is an explicit spelling for Status used by readiness callers.
func (r *Runner) Inspect(ctx context.Context) (Status, error) {
	return r.Status(ctx)
}

// Apply bootstraps the history table, validates all existing rows, and applies
// each pending migration in order. Every migration and its history row share
// one transaction; a failure leaves that version unrecorded.
func (r *Runner) Apply(ctx context.Context) (Status, error) {
	report := makeStatus(nil, r.catalog)
	if err := contextError(ctx); err != nil {
		return report, err
	}
	if err := r.withAdvisoryLock(ctx, func() error {
		if err := r.bootstrap(ctx); err != nil {
			return err
		}
		applied, err := r.readApplied(ctx)
		if err != nil {
			return err
		}
		report = makeStatus(applied, r.catalog)
		if err := validateApplied(applied, r.catalog); err != nil {
			return err
		}

		appliedVersions := make(map[int64]struct{}, len(applied))
		for _, row := range applied {
			appliedVersions[row.Version] = struct{}{}
		}
		for _, migration := range r.catalog.Migrations() {
			if _, exists := appliedVersions[migration.Version]; exists {
				continue
			}
			if err := contextError(ctx); err != nil {
				return err
			}
			if err := r.applyOne(ctx, migration); err != nil {
				return err
			}
			applied = append(applied, AppliedMigration{
				Version:  migration.Version,
				Name:     migration.Name,
				Checksum: migration.Checksum,
			})
			appliedVersions[migration.Version] = struct{}{}
			report = makeStatus(applied, r.catalog)
		}

		applied, err = r.readApplied(ctx)
		if err != nil {
			return err
		}
		report = makeStatus(applied, r.catalog)
		if err := validateApplied(applied, r.catalog); err != nil {
			return err
		}
		report.Ready = true
		return nil
	}); err != nil {
		report.Ready = false
		return report, err
	}
	return report, nil
}

func (r *Runner) bootstrap(ctx context.Context) error {
	if err := r.session.Exec(ctx, createHistoryTableSQL); err != nil {
		return newMigrationError(ErrMigrationDatabase, err)
	}
	return nil
}

func (r *Runner) readApplied(ctx context.Context) ([]AppliedMigration, error) {
	rows, err := r.session.Query(ctx, selectHistorySQL)
	if err != nil {
		if isMissingSchema(err) {
			return nil, newMigrationError(ErrMigrationSchemaMissing, err)
		}
		return nil, newMigrationError(ErrMigrationDatabase, err)
	}
	defer rows.Close()

	applied := make([]AppliedMigration, 0)
	for rows.Next() {
		var row AppliedMigration
		if err := rows.Scan(&row.Version, &row.Name, &row.Checksum, &row.AppliedAt); err != nil {
			return nil, newMigrationError(ErrMigrationDatabase, err)
		}
		applied = append(applied, row)
	}
	if err := rows.Err(); err != nil {
		return nil, newMigrationError(ErrMigrationDatabase, err)
	}
	sort.Slice(applied, func(i, j int) bool { return applied[i].Version < applied[j].Version })
	return applied, nil
}

func (r *Runner) applyOne(ctx context.Context, migration migrations.Migration) (err error) {
	tx, err := r.session.Begin(ctx)
	if err != nil {
		return newMigrationError(ErrMigrationDatabase, err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if rollbackErr := tx.Rollback(cleanupCtx); rollbackErr != nil {
			rollbackFailure := newMigrationError(ErrMigrationDatabase, rollbackErr)
			if err == nil {
				err = rollbackFailure
				return
			}
			err = errors.Join(err, rollbackFailure)
		}
	}()

	if err := tx.Exec(ctx, string(migration.SQL)); err != nil {
		return newMigrationError(ErrMigrationDatabase, err)
	}
	if err := tx.Exec(ctx, insertHistorySQL, migration.Version, migration.Name, migration.Checksum); err != nil {
		return newMigrationError(ErrMigrationDatabase, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return newMigrationError(ErrMigrationDatabase, err)
	}
	committed = true
	return nil
}

func (r *Runner) withAdvisoryLock(ctx context.Context, fn func() error) (err error) {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := r.session.Exec(ctx, lockSQL, AdvisoryLockKey); err != nil {
		return newMigrationError(ErrMigrationLock, err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if unlockErr := r.session.Exec(cleanupCtx, unlockSQL, AdvisoryLockKey); unlockErr != nil {
			unlockErr = newMigrationError(ErrMigrationLock, unlockErr)
			if err == nil {
				err = unlockErr
				return
			}
			err = errors.Join(err, unlockErr)
		}
	}()
	return fn()
}

func validateApplied(applied []AppliedMigration, catalog migrations.Catalog) error {
	latest, ok := catalog.Latest()
	if !ok {
		return fmt.Errorf("%w: catalog is empty", ErrInvalidMigrationCatalog)
	}
	seen := make(map[int64]struct{}, len(applied))
	previous := int64(0)
	for _, row := range applied {
		if row.Version <= 0 {
			return fmt.Errorf("%w: invalid history version", ErrMigrationGap)
		}
		if _, exists := seen[row.Version]; exists {
			return fmt.Errorf("%w: duplicate history version %d", ErrMigrationGap, row.Version)
		}
		seen[row.Version] = struct{}{}
		if row.Version > latest.Version {
			return fmt.Errorf("%w: database version %d exceeds binary version %d", ErrSchemaAhead, row.Version, latest.Version)
		}
		if row.Version != previous+1 {
			return fmt.Errorf("%w: expected version %d, got %d", ErrMigrationGap, previous+1, row.Version)
		}
		migration, exists := catalog.Lookup(row.Version)
		if !exists {
			return fmt.Errorf("%w: history version %d is absent from binary", ErrMigrationGap, row.Version)
		}
		if row.Name != migration.Name {
			return fmt.Errorf("%w: version %d", ErrMigrationNameMismatch, row.Version)
		}
		if row.Checksum != migration.Checksum {
			return fmt.Errorf("%w: version %d", ErrMigrationChecksumMismatch, row.Version)
		}
		previous = row.Version
	}
	return nil
}

func makeStatus(applied []AppliedMigration, catalog migrations.Catalog) Status {
	status := Status{Applied: append([]AppliedMigration(nil), applied...)}
	if latest, ok := catalog.Latest(); ok {
		status.Latest = latest.Version
	}
	if len(applied) > 0 {
		status.Current = applied[len(applied)-1].Version
	}
	return status
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

type migrationError struct {
	kind  error
	cause error
}

func newMigrationError(kind, cause error) error {
	return &migrationError{kind: kind, cause: cause}
}

func (e *migrationError) Error() string { return e.kind.Error() }

func (e *migrationError) Unwrap() error {
	// Context errors are safe and useful to callers. Do not unwrap arbitrary
	// driver errors: pgx diagnostics can contain SQL, DSNs, or credentials.
	if errors.Is(e.cause, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return nil
}

func (e *migrationError) Is(target error) bool {
	return target == e.kind || errors.Is(e.cause, target)
}

func safeCatalogDetail(err error) string {
	if errors.Is(err, migrations.ErrInvalidCatalog) {
		return "catalog validation failed"
	}
	return "catalog validation failed"
}

func isMissingSchema(err error) bool {
	var state interface{ SQLState() string }
	return errors.As(err, &state) && state.SQLState() == "42P01"
}

type pgxSession struct {
	conn *pgx.Conn
}

func (s pgxSession) Exec(ctx context.Context, query string, args ...any) error {
	_, err := s.conn.Exec(ctx, query, args...)
	return err
}

func (s pgxSession) Query(ctx context.Context, query string, args ...any) (Rows, error) {
	return s.conn.Query(ctx, query, args...)
}

func (s pgxSession) Begin(ctx context.Context) (Tx, error) {
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgxTx{tx: tx}, nil
}

type pgxTx struct {
	tx pgx.Tx
}

func (t pgxTx) Exec(ctx context.Context, query string, args ...any) error {
	_, err := t.tx.Exec(ctx, query, args...)
	return err
}

func (t pgxTx) Commit(ctx context.Context) error { return t.tx.Commit(ctx) }

func (t pgxTx) Rollback(ctx context.Context) error { return t.tx.Rollback(ctx) }
