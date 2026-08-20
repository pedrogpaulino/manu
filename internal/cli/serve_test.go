package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/config"
)

func TestRunServeHelpAndRouting(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"serve", "--help"}, &stdout, &stderr); code != ExitSuccess {
		t.Fatalf("serve --help code = %d, want %d; stderr=%q", code, ExitSuccess, stderr.String())
	}
	if !strings.Contains(stdout.String(), "usage: manu serve") {
		t.Fatalf("serve help = %q", stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"help"}, &stdout, &stderr); code != ExitSuccess || !strings.Contains(stdout.String(), "serve") {
		t.Fatalf("root help code/output = %d/%q", code, stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"serve", "unexpected"}, &stdout, &stderr); code != ExitUsage {
		t.Fatalf("serve positional code = %d, want %d", code, ExitUsage)
	}
}

func TestRunServeWithInjectedRunner(t *testing.T) {
	want := config.Default()
	var stdout, stderr bytes.Buffer
	called := false
	code := runServeWith(
		context.Background(),
		nil,
		&stdout,
		&stderr,
		func() (config.Config, error) { return want, nil },
		func(ctx context.Context, got config.Config) error {
			called = true
			if got.Server.ListenAddress != want.Server.ListenAddress {
				t.Fatalf("runner config = %#v, want %#v", got.Server, want.Server)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("runner context error = %v", err)
			}
			return nil
		},
	)
	if code != ExitSuccess || !called {
		t.Fatalf("runServeWith code/called = %d/%t, want success/true; stderr=%q", code, called, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("serve success stderr = %q", stderr.String())
	}
}

func TestRunServeWithSafeConfigurationAndRuntimeDiagnostics(t *testing.T) {
	tests := []struct {
		name       string
		load       ServeConfigLoader
		run        ServeRunner
		wantPhrase string
		wantCode   int
		secret     string
	}{
		{
			name: "load failure",
			load: func() (config.Config, error) {
				return config.Config{}, errors.New("password=super-secret dsn=postgres://user:secret@example")
			},
			wantPhrase: "server configuration is invalid",
			wantCode:   ExitTechnical,
			secret:     "super-secret",
		},
		{
			name:       "loopback policy",
			load:       func() (config.Config, error) { return config.Default(), nil },
			run:        func(context.Context, config.Config) error { return api.ErrNonLoopbackListenAddress },
			wantPhrase: "listen address must be loopback",
			wantCode:   ExitTechnical,
			secret:     "192.0.2.9",
		},
		{
			name:       "cancellation",
			load:       func() (config.Config, error) { return config.Default(), nil },
			run:        func(context.Context, config.Config) error { return context.Canceled },
			wantPhrase: "operation canceled",
			wantCode:   ExitTechnical,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runServeWith(context.Background(), nil, &stdout, &stderr, tt.load, tt.run)
			if code != tt.wantCode {
				t.Fatalf("code = %d, want %d; stderr=%q", code, tt.wantCode, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.wantPhrase) {
				t.Fatalf("stderr = %q, want phrase %q", stderr.String(), tt.wantPhrase)
			}
			if tt.secret != "" && strings.Contains(stderr.String(), tt.secret) {
				t.Fatalf("secret leaked in stderr = %q", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty on failure", stdout.String())
			}
		})
	}
}

func TestRunServeWithCanceledContextDoesNotLoadConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	loaded := false
	var stdout, stderr bytes.Buffer
	code := runServeWith(ctx, nil, &stdout, &stderr, func() (config.Config, error) {
		loaded = true
		return config.Default(), nil
	}, func(context.Context, config.Config) error {
		t.Fatal("runner should not be called")
		return nil
	})
	if code != ExitTechnical || loaded || !strings.Contains(stderr.String(), "operation canceled") {
		t.Fatalf("code/loaded/stderr = %d/%t/%q", code, loaded, stderr.String())
	}
}
