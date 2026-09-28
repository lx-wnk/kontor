package pipeline_test

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// bridgeToolNameRE matches the string literal or shared constant each
// mcp.AddTool call registers under in channel/bridge.go.
var bridgeToolNameRE = regexp.MustCompile(`(?m)^\s*Name:\s+(?:channelconfig\.(Tool\w+)|"([a-z_]+)"),`)

// TestChannelAllowList_CoversEveryBridgeTool is the guard against the defect
// that made this list wrong in the first place. A spawn runs with
// --allowedTools, so a tool the bridge registers but the allow-list omits is
// not callable — and nothing anywhere reports that. The agent simply sees the
// tool as unavailable and takes whatever fallback its prompt offers.
// set_stage_output was missing exactly that way, which meant every stage result
// had to travel the fenced-json fallback, and a stage whose final message did
// not carry a well-formed fence failed outright.
//
// The check reads bridge.go rather than trusting channelconfig.ToolNames,
// because the failure mode is a tool registered at the bridge and forgotten
// everywhere else. Asserting ToolNames against the allow-list would only
// compare the shared list to itself.
func TestChannelAllowList_CoversEveryBridgeTool(t *testing.T) {
	src, err := os.ReadFile("../channel/bridge.go")
	require.NoError(t, err, "the bridge source is the authority on what is registered")

	matches := bridgeToolNameRE.FindAllStringSubmatch(string(src), -1)
	registered := map[string]bool{}
	for _, m := range matches {
		switch {
		case m[1] != "": // channelconfig.ToolX — resolve through the shared list
			for _, name := range channelconfig.ToolNames {
				if strings.EqualFold(strings.ReplaceAll(name, "_", ""), strings.ToLower(strings.TrimPrefix(m[1], "Tool"))) {
					registered[name] = true
				}
			}
		case m[2] != "":
			registered[m[2]] = true
		}
	}
	require.NotEmpty(t, registered, "found no tool registrations in bridge.go — the regex has drifted from the source")

	allowed := map[string]bool{}
	for _, entry := range pipeline.ChannelAllowForTest() {
		allowed[strings.TrimPrefix(entry, "mcp__"+channelconfig.ServerName+"__")] = true
	}

	var missing []string
	for name := range registered {
		if !allowed[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	require.Empty(t, missing,
		"every tool the bridge registers must be in the spawn allow-list, else the agent cannot call it and silently falls back")
}

// TestChannelAllowList_UsesTheNamespacedForm pins the identifier shape the
// spawning CLI expects. A bare tool name in this list matches nothing.
func TestChannelAllowList_UsesTheNamespacedForm(t *testing.T) {
	entries := pipeline.ChannelAllowForTest()
	require.NotEmpty(t, entries)
	for _, e := range entries {
		require.True(t, strings.HasPrefix(e, "mcp__"+channelconfig.ServerName+"__"),
			"allow-list entries must be mcp__<server>__<tool>, got %q", e)
	}
}
