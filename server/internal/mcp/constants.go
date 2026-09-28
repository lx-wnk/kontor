package mcp

// ServerName is the MCP server identity returned in initialize/serverInfo and
// used by clients to register this server. Single source of truth.
const ServerName = "kontor-tasks"

// LegacyServerName is what this server was registered as before the rename. It
// stays named here because it must remain reserved: a user-scope server under
// the old name would otherwise be merged into a spawn and hand an agent the
// broad credential the per-stage-run key deliberately omits.
const LegacyServerName = "dashboard-tasks"

// EndpointPath is the HTTP route the MCP JSON-RPC handler is mounted on.
const EndpointPath = "/api/mcp"
