package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/config"
)

func TestRunMCPHelpAndRouting(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"mcp", "--help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("Run(mcp --help) = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if stdout.String() != "usage: manu mcp\n" || stderr.Len() != 0 {
		t.Fatalf("mcp help output = stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"help"}, &stdout, &stderr); code != ExitSuccess || !strings.Contains(stdout.String(), "mcp") {
		t.Fatalf("root help code/output = %d/%q", code, stdout.String())
	}
}

func TestRunMCPRejectsArgumentsAndFlags(t *testing.T) {
	for _, args := range [][]string{{"mcp", "unexpected"}, {"mcp", "--unknown"}} {
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != ExitUsage {
			t.Fatalf("Run(%v) = %d, want %d; stdout=%q stderr=%q", args, code, ExitUsage, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("Run(%v) stdout = %q, want empty", args, stdout.String())
		}
	}
}

func TestRunMCPWithConfigurationFailures(t *testing.T) {
	tests := []struct {
		name       string
		load       MCPConfigLoader
		wantPhrase string
	}{
		{
			name: "invalid configuration",
			load: func() (config.Config, error) {
				return config.Config{}, errors.New("password=secret dsn=postgres://user:secret@example")
			},
			wantPhrase: "configuration is invalid",
		},
		{
			name:       "disabled",
			load:       func() (config.Config, error) { return config.Default(), nil },
			wantPhrase: "MCP is disabled",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			code := runMCPWith(context.Background(), nil, &stdout, &stderr, test.load, func(_ context.Context, _ config.Config, _ io.Writer) error {
				called = true
				return nil
			})
			if code != ExitTechnical || called {
				t.Fatalf("code/called = %d/%t, want technical/false; stderr=%q", code, called, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.wantPhrase) {
				t.Fatalf("stderr = %q, want phrase %q", stderr.String(), test.wantPhrase)
			}
			if strings.Contains(stderr.String(), "secret") || stdout.Len() != 0 {
				t.Fatalf("unsafe or unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunMCPWithEnabledRunnerReceivesContextAndWritesNoOutput(t *testing.T) {
	configuration := config.Default()
	configuration.MCP.Enabled = true
	var stdout, stderr bytes.Buffer
	called := false
	code := runMCPWith(
		context.Background(),
		nil,
		&stdout,
		&stderr,
		func() (config.Config, error) { return configuration, nil },
		func(ctx context.Context, got config.Config, gotWriter io.Writer) error {
			called = true
			if got.MCP != configuration.MCP {
				t.Fatalf("runner configuration = %#v, want %#v", got.MCP, configuration.MCP)
			}
			if ctx == nil || ctx.Err() != nil {
				t.Fatalf("runner context = %v, want active context", ctx)
			}
			if gotWriter != &stderr {
				t.Fatalf("runner audit writer = %T, want stderr writer", gotWriter)
			}
			return nil
		},
	)
	if code != ExitSuccess || !called || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("code/called/stdout/stderr = %d/%t/%q/%q", code, called, stdout.String(), stderr.String())
	}
}

func TestRunMCPWithRunnerFailuresAreSafe(t *testing.T) {
	configuration := config.Default()
	configuration.MCP.Enabled = true
	const secret = "sql=select password from credentials"
	var stdout, stderr bytes.Buffer
	code := runMCPWith(
		context.Background(),
		nil,
		&stdout,
		&stderr,
		func() (config.Config, error) { return configuration, nil },
		func(context.Context, config.Config, io.Writer) error { return errors.New(secret) },
	)
	if code != ExitTechnical || stdout.Len() != 0 || !strings.Contains(stderr.String(), "server failed") || strings.Contains(stderr.String(), secret) {
		t.Fatalf("code/stdout/stderr = %d/%q/%q", code, stdout.String(), stderr.String())
	}
}

func TestRunMCPWithLifecycleCancellationIsClean(t *testing.T) {
	configuration := config.Default()
	configuration.MCP.Enabled = true
	for _, lifecycleError := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(lifecycleError.Error(), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			called := false
			code := runMCPWith(
				context.Background(),
				nil,
				&stdout,
				&stderr,
				func() (config.Config, error) { return configuration, nil },
				func(ctx context.Context, _ config.Config, _ io.Writer) error {
					called = true
					return lifecycleError
				},
			)
			if code != ExitSuccess || !called || stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("code/called/stdout/stderr = %d/%t/%q/%q", code, called, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunMCPWithCanceledContextSkipsSession(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaded := false
	var stdout, stderr bytes.Buffer
	code := runMCPWith(ctx, nil, &stdout, &stderr, func() (config.Config, error) {
		loaded = true
		return config.Default(), nil
	}, func(_ context.Context, _ config.Config, _ io.Writer) error {
		t.Fatal("runner should not be called")
		return nil
	})
	if code != ExitSuccess || loaded || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("code/loaded/stdout/stderr = %d/%t/%q/%q", code, loaded, stdout.String(), stderr.String())
	}
}
