package mcpadapter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerImplementationUsesStableIdentityAndDevelopmentFallback(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{name: "injected version", version: "v1.2.3", want: "v1.2.3"},
		{name: "empty version", version: "", want: DefaultVersion},
		{name: "whitespace version", version: "  ", want: DefaultVersion},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := ServerImplementation(test.version)
			if info.Name != ProgramName || info.Title != ProductTitle || info.Version != test.want {
				t.Fatalf("ServerImplementation(%q) = %#v, want name=%q title=%q version=%q", test.version, info, ProgramName, ProductTitle, test.want)
			}
		})
	}
}

func TestRunRejectsNilTransport(t *testing.T) {
	if err := Run(context.Background(), nil); !errors.Is(err, ErrNilTransport) {
		t.Fatalf("Run(nil transport) error = %v, want %v", err, ErrNilTransport)
	}
}

func TestRunNegotiatesServerContractAndClosesOnPeerEOF(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}

	result := session.InitializeResult()
	if result == nil {
		t.Fatal("InitializeResult() = nil")
	}
	if result.ProtocolVersion != ProtocolVersion {
		t.Fatalf("protocol version = %q, want %q", result.ProtocolVersion, ProtocolVersion)
	}
	if result.ServerInfo == nil {
		t.Fatal("ServerInfo = nil")
	}
	wantInfo := ServerImplementation("")
	if result.ServerInfo.Name != wantInfo.Name || result.ServerInfo.Title != wantInfo.Title || result.ServerInfo.Version != wantInfo.Version {
		t.Fatalf("ServerInfo = %#v, want %#v", *result.ServerInfo, wantInfo)
	}
	assertFeatureFreeCapabilities(t, result.Capabilities)

	if err := session.Ping(ctx, nil); err != nil {
		t.Fatalf("session.Ping() error = %v", err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("session.Close() error = %v", err)
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("Run() after peer EOF error = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatalf("Run() did not finish after peer EOF: %v", ctx.Err())
	}
}

func TestRunReturnsContextCancellationWithoutHanging(t *testing.T) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect() error = %v", err)
	}
	defer session.Close()

	cancel()
	select {
	case err := <-serverDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() after cancellation error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run() did not finish after context cancellation")
	}
}

func assertFeatureFreeCapabilities(t *testing.T, capabilities *mcp.ServerCapabilities) {
	t.Helper()
	if capabilities == nil {
		t.Fatal("server capabilities = nil")
	}
	if capabilities.Experimental != nil || capabilities.Extensions != nil ||
		capabilities.Completions != nil || capabilities.Logging != nil ||
		capabilities.Prompts != nil || capabilities.Resources != nil || capabilities.Tools != nil {
		t.Fatalf("server capabilities unexpectedly announce features: %#v", capabilities)
	}
}
