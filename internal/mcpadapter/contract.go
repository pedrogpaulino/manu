// Package mcpadapter is the boundary for the official MCP SDK used by Manu.
//
// MCP-specific types stay in this adapter and do not cross the application
// port. The adapter is intentionally limited to the pinned protocol and
// stdio transport contract; the MCP server and its tools are implemented by
// later tasks.
package mcpadapter

import "github.com/modelcontextprotocol/go-sdk/mcp"

const (
	// ProtocolVersion is the MCP protocol version declared by the Manu adapter.
	ProtocolVersion = "2025-11-25"
)

// Keep the selected SDK transport contract checked at compile time. If the
// SDK changes the stdio transport API, the adapter must be reviewed before a
// server can be added.
var _ mcp.Transport = (*mcp.StdioTransport)(nil)
