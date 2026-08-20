package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/config"
)

// ServeConfigLoader and ServeRunner are the seams around configuration and
// the blocking server lifecycle. Tests can inject both without opening a
// socket or loading credentials.
type ServeConfigLoader func() (config.Config, error)
type ServeRunner func(context.Context, config.Config) error

// runServe folds process signals into the command context and delegates to an
// injectable implementation.
func runServe(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	return runServeWith(ctx, args, stdout, stderr, config.Load, executeServe)
}

func runServeWith(ctx context.Context, args []string, stdout, stderr io.Writer, load ServeConfigLoader, run ServeRunner) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	flagSet := newFlagSet("serve", stderr)
	flagSet.Usage = func() {
		_, _ = fmt.Fprintln(stdout, "usage: manu serve")
	}
	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu serve: positional arguments are not supported")
		return ExitUsage
	}
	if err := ctx.Err(); err != nil {
		writeServeDiagnostic(stderr, err)
		return ExitTechnical
	}
	if load == nil {
		load = config.Load
	}
	if run == nil {
		run = executeServe
	}

	configuration, err := load()
	if err != nil {
		writeServeDiagnostic(stderr, api.ErrInvalidServerConfig)
		return ExitTechnical
	}
	if err := run(ctx, configuration); err != nil {
		writeServeDiagnostic(stderr, err)
		return ExitTechnical
	}
	return ExitSuccess
}

func writeServeDiagnostic(w io.Writer, err error) {
	_, _ = fmt.Fprintln(w, "manu serve:", serveDiagnostic(err))
}

func serveDiagnostic(err error) string {
	switch {
	case err == nil:
		return "server failed"
	case errors.Is(err, context.Canceled):
		return "operation canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "operation timed out"
	case errors.Is(err, api.ErrNonLoopbackListenAddress), errors.Is(err, api.ErrNonLoopbackAddress):
		return "listen address must be loopback"
	case errors.Is(err, api.ErrInvalidListenAddress), errors.Is(err, api.ErrInvalidServerConfig), errors.Is(err, config.ErrInvalid):
		return "server configuration is invalid"
	case errors.Is(err, api.ErrListen):
		return "could not start server listener"
	case errors.Is(err, api.ErrShutdown):
		return "graceful shutdown failed"
	case errors.Is(err, api.ErrServerRunning):
		return "server is already running"
	case errors.Is(err, api.ErrServe):
		return "server loop failed"
	case errors.Is(err, ErrServeRuntimeConfiguration), errors.Is(err, ErrServeRuntimeNotConfigured), errors.Is(err, config.ErrInvalid):
		return "server configuration is invalid"
	case errors.Is(err, ErrServeRuntimeDatabase), errors.Is(err, ErrMigrationConnection):
		return "database connection failed"
	default:
		return "server failed"
	}
}
