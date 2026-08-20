package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/persistence"
)

func TestMigrateIsRoutedAndAdvertisedByHelp(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("Run(help) = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "migrate") {
		t.Fatalf("help = %q, missing migrate command", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"migrate", "--format", "invalid"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("Run(migrate invalid format) = %d, want %d; stderr=%q", code, ExitUsage, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"migrate", "--help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("Run(migrate --help) = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: manu migrate") || stderr.Len() != 0 {
		t.Fatalf("migrate help output = stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestMigrateRejectsFlagsAndPositionalArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "positional", args: []string{"migrate", "database"}},
		{name: "unknown flag", args: []string{"migrate", "--unknown"}},
		{name: "invalid format", args: []string{"migrate", "--format", "yaml"}},
		{name: "json with invalid format", args: []string{"migrate", "--json", "--format", "yaml"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(test.args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("Run(%v) = %d, want %d; stdout=%q stderr=%q", test.args, code, ExitUsage, stdout.String(), stderr.String())
			}
		})
	}
}

func TestMigrateInjectedExecutorSupportsHumanJSONAndRepeat(t *testing.T) {
	t.Parallel()

	status := persistence.Status{
		Current: 2,
		Latest:  2,
		Applied: []persistence.AppliedMigration{
			{Version: 1, Name: "0001_canonical_knowledge.up.sql", Checksum: strings.Repeat("a", 64), AppliedAt: time.Unix(1, 0).UTC()},
			{Version: 2, Name: "0002_query_projection.up.sql", Checksum: strings.Repeat("b", 64), AppliedAt: time.Unix(2, 0).UTC()},
		},
		Ready: true,
	}
	loadCalls, executeCalls := 0, 0
	loader := func() (config.Config, error) {
		loadCalls++
		return config.Default(), nil
	}
	executor := func(ctx context.Context, _ config.Config) (persistence.Status, error) {
		executeCalls++
		if ctx == nil {
			t.Fatal("executor received nil context")
		}
		return status, nil
	}

	var humanOut, humanErr bytes.Buffer
	if code := runMigrateWith(context.Background(), nil, &humanOut, &humanErr, loader, executor); code != ExitSuccess {
		t.Fatalf("human migrate code = %d, want %d; stderr=%q", code, ExitSuccess, humanErr.String())
	}
	for _, want := range []string{"migration complete", "current: 2", "latest: 2", "applied: 2", "ready: true"} {
		if !strings.Contains(humanOut.String(), want) {
			t.Fatalf("human output = %q, missing %q", humanOut.String(), want)
		}
	}

	var jsonOut, jsonErr bytes.Buffer
	if code := runMigrateWith(context.Background(), []string{"--json"}, &jsonOut, &jsonErr, loader, executor); code != ExitSuccess {
		t.Fatalf("JSON migrate code = %d, want %d; stderr=%q", code, ExitSuccess, jsonErr.String())
	}
	if !json.Valid(jsonOut.Bytes()) {
		t.Fatalf("JSON output is invalid: %q", jsonOut.String())
	}
	var decoded persistence.Status
	if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil {
		t.Fatalf("decode migration status: %v", err)
	}
	if decoded.Current != status.Current || decoded.Latest != status.Latest || len(decoded.Applied) != len(status.Applied) || !decoded.Ready {
		t.Fatalf("decoded status = %#v, want %#v", decoded, status)
	}
	if loadCalls != 2 || executeCalls != 2 {
		t.Fatalf("loader/executor calls = %d/%d, want 2/2", loadCalls, executeCalls)
	}
}

func TestMigrateDiagnosticsAreSafeForOperationalErrors(t *testing.T) {
	t.Parallel()

	const secret = "postgres://runtime:test-only-password@db.internal:5432/manu"
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "ahead", err: persistence.ErrSchemaAhead, want: "ahead"},
		{name: "incompatible name", err: persistence.ErrMigrationNameMismatch, want: "incompatible"},
		{name: "incompatible checksum", err: persistence.ErrMigrationChecksumMismatch, want: "incompatible"},
		{name: "missing history", err: persistence.ErrMigrationSchemaMissing, want: "missing"},
		{name: "database", err: persistence.ErrMigrationDatabase, want: "database operation failed"},
		{name: "lock", err: persistence.ErrMigrationLock, want: "lock"},
		{name: "cancel", err: context.Canceled, want: "canceled"},
		{name: "unknown driver detail", err: errors.New(secret), want: "migration failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			loader := func() (config.Config, error) { return config.Default(), nil }
			executor := func(context.Context, config.Config) (persistence.Status, error) {
				return persistence.Status{}, test.err
			}
			if code := runMigrateWith(context.Background(), nil, &stdout, &stderr, loader, executor); code != ExitTechnical {
				t.Fatalf("runMigrateWith() = %d, want %d; stderr=%q", code, ExitTechnical, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, missing %q", stderr.String(), test.want)
			}
			if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
				t.Fatalf("migration output echoed secret: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestMigrateLoaderErrorsAreSafe(t *testing.T) {
	t.Parallel()

	const secret = "postgres://runtime:test-only-password@db.internal:5432/manu"
	var stdout, stderr bytes.Buffer
	loader := func() (config.Config, error) { return config.Config{}, errors.New(secret) }
	executor := func(context.Context, config.Config) (persistence.Status, error) {
		t.Fatal("executor called after loader failure")
		return persistence.Status{}, nil
	}
	if code := runMigrateWith(context.Background(), nil, &stdout, &stderr, loader, executor); code != ExitTechnical {
		t.Fatalf("runMigrateWith() = %d, want %d; stderr=%q", code, ExitTechnical, stderr.String())
	}
	if !strings.Contains(stderr.String(), "configuration") || strings.Contains(stderr.String(), secret) {
		t.Fatalf("unsafe loader diagnostic: %q", stderr.String())
	}
}

func TestMigrateCancellationStopsBeforeLoading(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	var stdout, stderr bytes.Buffer
	loader := func() (config.Config, error) {
		called = true
		return config.Default(), nil
	}
	if code := runMigrateWith(ctx, nil, &stdout, &stderr, loader, nil); code != ExitTechnical {
		t.Fatalf("cancelled migrate code = %d, want %d; stderr=%q", code, ExitTechnical, stderr.String())
	}
	if called {
		t.Fatal("loader was called after cancellation")
	}
	if !strings.Contains(stderr.String(), "canceled") {
		t.Fatalf("cancellation diagnostic = %q", stderr.String())
	}
}

func TestBuildPostgresURLEscapesCredentialsAndDatabase(t *testing.T) {
	t.Parallel()

	postgres := config.Default().Postgres
	postgres.Host = "db.internal"
	postgres.User = "runtime@corp"
	postgres.Password = "p@ss:/?#&"
	postgres.Database = "knowledge schema/one"
	raw, err := buildPostgresURL(postgres)
	if err != nil {
		t.Fatalf("buildPostgresURL() error = %v", err)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	if parsed.Host != "db.internal:5432" || parsed.Path != "/knowledge schema/one" {
		t.Fatalf("parsed URL = %#v", parsed)
	}
	if parsed.User.Username() != postgres.User {
		t.Fatalf("parsed username = %q, want %q", parsed.User.Username(), postgres.User)
	}
	password, ok := parsed.User.Password()
	if !ok || password != postgres.Password {
		t.Fatalf("parsed password = %q/%t, want original password", password, ok)
	}
	if strings.Contains(raw, postgres.Password) {
		t.Fatalf("URL contains unescaped password: %q", raw)
	}

	postgres.Host = "user:password@db.internal"
	if _, err := buildPostgresURL(postgres); !errors.Is(err, ErrMigrationConfiguration) {
		t.Fatalf("buildPostgresURL(userinfo host) error = %v, want configuration error", err)
	}
	if strings.Contains(errString(err), "user:password@db.internal") {
		t.Fatal("URL builder error echoed host userinfo")
	}
}

func TestBuildPostgresURLSupportsUnixSocketDirectory(t *testing.T) {
	t.Parallel()

	postgres := config.Default().Postgres
	postgres.Host = "/var/run/postgresql"
	connection, err := postgresConnectionConfig(postgres)
	if err != nil {
		t.Fatalf("postgresConnectionConfig() error = %v", err)
	}
	if connection.Host != postgres.Host || int(connection.Port) != postgres.Port {
		t.Fatalf("socket connection = host %q port %d, want %q/%d", connection.Host, connection.Port, postgres.Host, postgres.Port)
	}
}

func TestPostgresConnectionConfigHidesParserAndDSNErrors(t *testing.T) {
	t.Parallel()

	const secret = "test-only-password"
	postgres := config.Default().Postgres
	postgres.DSN = "postgres://runtime:" + secret + "@%gh&db/manu"
	_, err := postgresConnectionConfig(postgres)
	if !errors.Is(err, ErrMigrationConfiguration) {
		t.Fatalf("postgresConnectionConfig() error = %v, want configuration error", err)
	}
	if strings.Contains(errString(err), secret) || strings.Contains(errString(err), postgres.DSN) {
		t.Fatalf("connection config error echoed secret/DSN: %v", err)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
