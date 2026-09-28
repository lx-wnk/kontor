package mcpapps

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lx-wnk/kontor/server/internal/version"
)

// callToolTimeout bounds one operator-initiated tool call, matching the
// catalogue refresh's own bound.
const callToolTimeout = 30 * time.Second

// CallTool runs one tool on an application's own MCP server and returns the
// raw JSON of its result.
//
// This is an operator action — a human clicked something in the UI — so no
// grant is consulted; grants gate what an *agent* may call. Callers document
// that where they expose it.
//
// A server may answer with structured content or with text content holding
// JSON; the published mail server does the latter, so both are accepted and
// the text body is returned verbatim.
func CallTool(ctx context.Context, transport mcp.Transport, name string, args map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, callToolTimeout)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "agent-dashboard", Version: version.Version}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpapps.CallTool %s: connect: %w", name, err)
	}
	defer func() { _ = session.Close() }()

	// An empty object, never nil: a nil map marshals to "arguments": null, and
	// a server validating its input against a schema rejects that outright —
	// the published mail server answers "expected record, received null".
	if args == nil {
		args = map[string]any{}
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return nil, fmt.Errorf("mcpapps.CallTool %s: %w", name, err)
	}
	if res.IsError {
		return nil, fmt.Errorf("mcpapps.CallTool %s: %s", name, firstText(res))
	}
	if res.StructuredContent != nil {
		encoded, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return nil, fmt.Errorf("mcpapps.CallTool %s: encode structured content: %w", name, err)
		}
		return encoded, nil
	}
	text := firstText(res)
	if text == "" {
		return nil, fmt.Errorf("mcpapps.CallTool %s: the server returned no content", name)
	}
	return json.RawMessage(text), nil
}

func firstText(res *mcp.CallToolResult) string {
	for _, c := range res.Content {
		if t, ok := c.(*mcp.TextContent); ok && t.Text != "" {
			return t.Text
		}
	}
	return ""
}
