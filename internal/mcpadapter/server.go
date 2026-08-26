package mcpadapter

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pedrogpaulino/manu/internal/buildinfo"
)

const (
	// ProgramName is the stable programmatic identity exposed by the adapter.
	ProgramName = "manu"
	// ProductTitle is the human-readable identity exposed by the adapter.
	ProductTitle = "Manu"
	// DefaultVersion identifies builds without injected version metadata.
	DefaultVersion = "dev"
)

var (
	// ErrNilTransport reports an invalid adapter boundary before the SDK is
	// invoked. It prevents a typed transport failure from becoming a panic.
	ErrNilTransport = errors.New("mcpadapter: transport must not be nil")
)

// ServerImplementation returns the identity advertised during MCP
// initialization. Empty or whitespace-only build metadata uses the
// deterministic development version.
func ServerImplementation(version string) mcp.Implementation {
	version = strings.TrimSpace(version)
	if version == "" {
		version = DefaultVersion
	}
	return mcp.Implementation{
		Name:    ProgramName,
		Title:   ProductTitle,
		Version: version,
	}
}

// NewServer creates the feature-free MCP server for the current build. Tools,
// resources, and prompts are intentionally added by later adapter changes.
func NewServer() *mcp.Server {
	return newServer(buildinfo.Current().Version)
}

func newServer(version string) *mcp.Server {
	implementation := ServerImplementation(version)
	return mcp.NewServer(
		&implementation,
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{}},
	)
}

// Run serves one MCP session over transport until the peer closes it or ctx
// is cancelled. The transport is injected so protocol lifecycle tests do not
// need a subprocess.
func Run(ctx context.Context, transport mcp.Transport) error {
	if transport == nil {
		return ErrNilTransport
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return newServer(buildinfo.Current().Version).Run(ctx, transport)
}

// RunStdio serves one MCP session over the process stdin/stdout transport.
// Diagnostics are kept out of the protocol stream by the SDK's discard logger.
func RunStdio(ctx context.Context) error {
	return Run(ctx, &mcp.StdioTransport{})
}
