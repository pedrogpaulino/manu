package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

const (
	// migrationConnectTimeout bounds only the PostgreSQL startup handshake. The
	// migration runner receives the command context unchanged after connecting.
	migrationConnectTimeout = 10 * time.Second
	// migrationCleanupTimeout bounds best-effort connection cleanup after an
	// interrupted command.
	migrationCleanupTimeout = 5 * time.Second
)

var (
	// ErrMigrationConfiguration is a safe category for malformed connection
	// configuration. It deliberately carries no parser or credential details.
	ErrMigrationConfiguration = errors.New("migration: invalid configuration")
	// ErrMigrationConnection is a safe category for PostgreSQL startup and
	// cleanup failures. Driver diagnostics are never returned to the CLI.
	ErrMigrationConnection = errors.New("migration: connection failed")
)

// MigrationConfigLoader and MigrationExecutor are the two seams used by the
// command. Production uses config.Load and executeMigrations; tests can inject
// deterministic values without opening a database connection.
type MigrationConfigLoader func() (config.Config, error)
type MigrationExecutor func(context.Context, config.Config) (persistence.Status, error)

// runMigrate binds process signals to the command context and delegates the
// actual command to an injectable implementation.
func runMigrate(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	return runMigrateWith(ctx, args, stdout, stderr, config.Load, executeMigrations)
}

func runMigrateWith(ctx context.Context, args []string, stdout, stderr io.Writer, load MigrationConfigLoader, execute MigrationExecutor) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	flagSet := newFlagSet("migrate", stderr)
	format := flagSet.String("format", "human", "output format: human or json")
	jsonOutput := flagSet.Bool("json", false, "emit migration status as JSON")
	flagSet.Usage = func() {
		_, _ = fmt.Fprintln(stdout, "usage: manu migrate [--format human|json] [--json]")
	}
	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu migrate: positional arguments are not supported")
		return ExitUsage
	}
	selectedFormat, err := migrationOutputFormat(*format, *jsonOutput)
	if err != nil {
		fmt.Fprintln(stderr, "manu migrate:", err)
		return ExitUsage
	}
	if err := ctx.Err(); err != nil {
		writeMigrationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if load == nil {
		load = config.Load
	}
	if execute == nil {
		execute = executeMigrations
	}
	configuration, err := load()
	if err != nil {
		writeMigrationDiagnostic(stderr, ErrMigrationConfiguration)
		return ExitTechnical
	}
	status, err := execute(ctx, configuration)
	if err != nil {
		writeMigrationDiagnostic(stderr, err)
		return ExitTechnical
	}
	if selectedFormat == "json" {
		if err := writeJSON(stdout, status); err != nil {
			fmt.Fprintln(stderr, "manu migrate: unable to write status")
			return ExitTechnical
		}
		return ExitSuccess
	}
	if err := writeMigrationSummary(stdout, status); err != nil {
		return ExitTechnical
	}
	return ExitSuccess
}

func migrationOutputFormat(format string, jsonOutput bool) (string, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "human", "":
		if jsonOutput {
			return "json", nil
		}
		return "human", nil
	case "json":
		return "json", nil
	default:
		return "", errors.New("invalid output format")
	}
}

func writeMigrationSummary(w io.Writer, status persistence.Status) error {
	if _, err := fmt.Fprintln(w, "migration complete"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "current: %d\n", status.Current); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "latest: %d\n", status.Latest); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "applied: %d\n", len(status.Applied)); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "ready: %t\n", status.Ready)
	return err
}

func writeMigrationDiagnostic(w io.Writer, err error) {
	_, _ = fmt.Fprintln(w, "manu migrate:", migrationDiagnostic(err))
}

func migrationDiagnostic(err error) string {
	switch {
	case err == nil:
		return "migration failed"
	case errors.Is(err, context.Canceled):
		return "operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, persistence.ErrSchemaAhead):
		return "database schema is ahead of this binary"
	case errors.Is(err, persistence.ErrMigrationNameMismatch),
		errors.Is(err, persistence.ErrMigrationChecksumMismatch),
		errors.Is(err, persistence.ErrMigrationGap):
		return "database schema is incompatible with this binary"
	case errors.Is(err, persistence.ErrMigrationSchemaMissing):
		return "database schema history is missing"
	case errors.Is(err, persistence.ErrMigrationLock):
		return "could not acquire the migration lock"
	case errors.Is(err, persistence.ErrMigrationDatabase):
		return "database operation failed"
	case errors.Is(err, persistence.ErrInvalidMigrationCatalog):
		return "embedded migration catalog is invalid"
	case errors.Is(err, ErrMigrationConfiguration), errors.Is(err, config.ErrInvalid):
		return "database configuration is invalid"
	case errors.Is(err, ErrMigrationConnection):
		return "database connection failed"
	default:
		return "migration failed"
	}
}

func executeMigrations(ctx context.Context, configuration config.Config) (status persistence.Status, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	connectionConfig, err := postgresConnectionConfig(configuration.Postgres)
	if err != nil {
		return status, err
	}
	connectCtx, cancel := context.WithTimeout(ctx, migrationConnectTimeout)
	defer cancel()
	conn, err := pgx.ConnectConfig(connectCtx, connectionConfig)
	if err != nil {
		if contextErr := connectCtx.Err(); contextErr != nil {
			return status, contextErr
		}
		return status, ErrMigrationConnection
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), migrationCleanupTimeout)
		defer cleanupCancel()
		if closeErr := conn.Close(cleanupCtx); closeErr != nil && err == nil {
			err = ErrMigrationConnection
		}
	}()

	runner, err := persistence.NewEmbeddedPGXMigrationRunner(conn)
	if err != nil {
		return status, err
	}
	return runner.Apply(ctx)
}

func postgresConnectionConfig(postgres config.PostgresConfig) (*pgx.ConnConfig, error) {
	if err := postgres.Validate(); err != nil {
		return nil, ErrMigrationConfiguration
	}
	dsn := strings.TrimSpace(postgres.DSN)
	if dsn == "" {
		var err error
		dsn, err = buildPostgresURL(postgres)
		if err != nil {
			return nil, err
		}
	}
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, ErrMigrationConfiguration
	}
	return connectionConfig, nil
}

func buildPostgresURL(postgres config.PostgresConfig) (string, error) {
	if strings.TrimSpace(postgres.DSN) != "" {
		return "", ErrMigrationConfiguration
	}
	if err := postgres.Validate(); err != nil {
		return "", ErrMigrationConfiguration
	}
	host := strings.TrimSpace(postgres.Host)
	if host == "" || strings.IndexFunc(host, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return "", ErrMigrationConfiguration
	}
	if !strings.HasPrefix(host, "/") && strings.ContainsAny(host, "@/?#") {
		return "", ErrMigrationConfiguration
	}
	database := postgres.Database
	if strings.TrimSpace(database) == "" || strings.IndexFunc(database, unicode.IsControl) >= 0 {
		return "", ErrMigrationConfiguration
	}
	escapedDatabase := url.PathEscape(database)
	connectionURL := url.URL{
		Scheme:  "postgres",
		Host:    net.JoinHostPort(host, strconv.Itoa(postgres.Port)),
		Path:    "/" + database,
		RawPath: "/" + escapedDatabase,
		User:    url.UserPassword(postgres.User, postgres.Password),
	}
	query := url.Values{}
	if strings.HasPrefix(host, "/") {
		// pgx accepts an absolute Unix-socket directory through the host query
		// parameter. Keep the URL authority synthetic so a slash cannot be
		// interpreted as a network hostname or leak into userinfo/path parsing.
		if host == "/" || strings.ContainsAny(host, "@?#") {
			return "", ErrMigrationConfiguration
		}
		connectionURL.Host = "localhost"
		query.Set("host", host)
		query.Set("port", strconv.Itoa(postgres.Port))
	}
	query.Set("sslmode", postgres.SSLMode)
	connectionURL.RawQuery = query.Encode()
	return connectionURL.String(), nil
}
