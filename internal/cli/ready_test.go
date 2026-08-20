package cli

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pedrogpaulino/manu/internal/config"
)

func TestRunReadyProbesTheConfiguredReadinessContract(t *testing.T) {
	called := ""
	load := func() (config.Config, error) {
		configuration := config.Default()
		configuration.Server.ListenAddress = "127.0.0.1:9090"
		return configuration, nil
	}
	probe := func(_ context.Context, address string) error {
		called = address
		return nil
	}
	var output strings.Builder
	if got := runReadyWith(context.Background(), nil, &output, io.Discard, load, probe); got != ExitSuccess {
		t.Fatalf("runReadyWith() = %d, want success", got)
	}
	if called != "127.0.0.1:9090" || output.String() != "ready\n" {
		t.Fatalf("probe/output = %q/%q", called, output.String())
	}
}

func TestRunReadyFailsClosedWhenReadinessProbeFails(t *testing.T) {
	load := func() (config.Config, error) { return config.Default(), nil }
	probe := func(context.Context, string) error { return errors.New("not ready") }
	if got := runReadyWith(context.Background(), nil, io.Discard, io.Discard, load, probe); got != ExitTechnical {
		t.Fatalf("runReadyWith() = %d, want technical failure", got)
	}
}
