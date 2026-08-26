package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/config"
	"github.com/pedrogpaulino/manu/internal/mcpadapter"
)

// MCPConfigLoader and MCPRunner are the seams around configuration and the
// blocking MCP server lifecycle. Tests can inject both without opening a
// stdio session or loading process configuration.
type MCPConfigLoader func() (config.Config, error)
type MCPRunner func(context.Context) error

// runMCP folds process signals into the MCP context and delegates the command
// to an injectable implementation.
func runMCP(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	return runMCPWith(ctx, args, stdout, stderr, config.Load, mcpadapter.RunStdio)
}

func runMCPWith(ctx context.Context, args []string, stdout, stderr io.Writer, load MCPConfigLoader, run MCPRunner) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, _ = fmt.Fprintln(stdout, "usage: manu mcp")
		return ExitSuccess
	}

	flagSet := newFlagSet("mcp", stderr)
	flagSet.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: manu mcp")
	}
	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, _ = fmt.Fprintln(stdout, "usage: manu mcp")
			return ExitSuccess
		}
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu mcp: positional arguments are not supported")
		return ExitUsage
	}
	if err := ctx.Err(); err != nil {
		return ExitSuccess
	}
	if load == nil {
		load = config.Load
	}
	if run == nil {
		run = mcpadapter.RunStdio
	}

	configuration, err := load()
	if err != nil {
		writeMCPDiagnostic(stderr, "configuration is invalid")
		return ExitTechnical
	}
	if !configuration.MCP.Enabled {
		writeMCPDiagnostic(stderr, "MCP is disabled")
		return ExitTechnical
	}
	if err := run(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ExitSuccess
		}
		writeMCPDiagnostic(stderr, "server failed")
		return ExitTechnical
	}
	return ExitSuccess
}

func writeMCPDiagnostic(w io.Writer, message string) {
	_, _ = fmt.Fprintln(w, "manu mcp:", message)
}
