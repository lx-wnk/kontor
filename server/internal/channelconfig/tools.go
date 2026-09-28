package channelconfig

// ServerName is the MCP server name the bridge registers under, and the middle
// segment of the `mcp__<server>__<tool>` identifier the spawning CLI expects.
// One value, not two: this is the same name the bridge is registered under in
// channelconfig.go. Held apart, a rename moves one and leaves the other, and a
// spawn's allow-list then names tools the bridge does not offer — which nothing
// reports, because an unavailable tool looks exactly like one that was never
// offered.
const ServerName = ChannelServerName

// Tool names the bridge registers. They are declared here rather than at the
// registration site because a spawn runs with --allowedTools: a tool that the
// bridge offers but the allow-list omits is not callable, and nothing reports
// that — the agent simply sees it as unavailable. Keeping both sides on this
// one list is what stops the two from drifting apart.
const (
	ToolDashboardReply    = "dashboard_reply"
	ToolRequestPermission = "request_permission"
	ToolSetStageOutput    = "set_stage_output"
)

// ToolNames is every tool the bridge registers, in registration order.
var ToolNames = []string{ToolDashboardReply, ToolRequestPermission, ToolSetStageOutput}

// AllowListEntries returns ToolNames rendered as the `mcp__<server>__<tool>`
// identifiers a spawn's --allowedTools takes.
func AllowListEntries() []string {
	entries := make([]string, len(ToolNames))
	for i, name := range ToolNames {
		entries[i] = "mcp__" + ServerName + "__" + name
	}
	return entries
}

// EnvMCPToken is the environment variable the bridge reads its dashboard
// credential from. Declared here because two packages depend on the exact
// spelling: channelconfig writes it into the spawn's MCP config, and the bridge
// reads it at startup. A mismatch would leave the bridge unauthenticated with no
// error anywhere — the callback simply comes back 401.
const EnvMCPToken = "DASHBOARD_MCP_TOKEN"
