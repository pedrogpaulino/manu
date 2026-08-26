package mcpadapter

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestProtocolContract(t *testing.T) {
	if ProtocolVersion != "2025-11-25" {
		t.Fatalf("ProtocolVersion = %q, want %q", ProtocolVersion, "2025-11-25")
	}

	var transport mcp.Transport = &mcp.StdioTransport{}
	if transport == nil {
		t.Fatal("stdio transport contract must remain available")
	}
}
