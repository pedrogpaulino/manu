package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/pedrogpaulino/manu/internal/analysis"
	"github.com/pedrogpaulino/manu/internal/api"
	"github.com/pedrogpaulino/manu/internal/config"
)

const readyProbeTimeout = 2 * time.Second

// runReady is the scratch-image healthcheck command. It probes the actual
// HTTP readiness contract, so a running binary is not considered healthy
// until its PostgreSQL/schema dependency reports ready.
func runReady(runContext analysis.RunContext, args []string, stdout, stderr io.Writer) int {
	ctx, stop := contextWithSignals(runContext)
	defer stop()
	return runReadyWith(ctx, args, stdout, stderr, config.Load, probeReady)
}

type readyConfigLoader func() (config.Config, error)
type readyProbe func(context.Context, string) error

func runReadyWith(ctx context.Context, args []string, stdout, stderr io.Writer, load readyConfigLoader, probe readyProbe) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	flagSet := newFlagSet("ready", stderr)
	flagSet.Usage = func() { _, _ = fmt.Fprintln(stdout, "usage: manu ready") }
	if err := flagSet.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitSuccess
		}
		return ExitUsage
	}
	if flagSet.NArg() != 0 {
		fmt.Fprintln(stderr, "manu ready: positional arguments are not supported")
		return ExitUsage
	}
	if err := ctx.Err(); err != nil {
		fmt.Fprintln(stderr, "manu ready: operation canceled")
		return ExitTechnical
	}
	if load == nil {
		load = config.Load
	}
	if probe == nil {
		probe = probeReady
	}
	configuration, err := load()
	if err != nil {
		fmt.Fprintln(stderr, "manu ready: configuration is invalid")
		return ExitTechnical
	}
	if err := probe(ctx, configuration.Server.ListenAddress); err != nil {
		fmt.Fprintln(stderr, "manu ready: dependency is not ready")
		return ExitTechnical
	}
	_, _ = fmt.Fprintln(stdout, "ready")
	return ExitSuccess
}

func probeReady(ctx context.Context, listenAddress string) error {
	if err := api.ValidateListenAddress(listenAddress); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, readyProbeTimeout)
	defer cancel()
	requestURL := url.URL{Scheme: "http", Host: listenAddress, Path: api.ReadinessPath}
	request, err := http.NewRequestWithContext(probeCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Timeout: readyProbeTimeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("readiness endpoint is not ready")
	}
	return nil
}
