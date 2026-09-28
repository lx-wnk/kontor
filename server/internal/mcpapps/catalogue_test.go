package mcpapps_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

type noArgs struct{}

// fakeMailServer returns a client-side transport to an in-memory MCP server
// exposing a read-only search tool and a send tool.
func fakeMailServer(t *testing.T) mcp.Transport {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-mail", Version: "0"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "search_emails",
		Description: "Search a mailbox",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "send_email", Description: "Send mail"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	serverT, clientT := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Run(ctx, serverT) }()
	return clientT
}

func TestListTools_ReadsNamesAndHints(t *testing.T) {
	tools, err := mcpapps.ListTools(context.Background(), fakeMailServer(t))
	require.NoError(t, err)
	require.Len(t, tools, 2)
	byName := map[string]bool{}
	for _, tool := range tools {
		byName[tool.Name] = tool.ReadOnlyHint
	}
	require.True(t, byName["search_emails"])
	require.False(t, byName["send_email"])
}

func TestRefresh_SeedsCapabilitiesAsAskAndNeverDowngradesAnExistingClass(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	apps := repo.NewMCPApplicationRepo(bundle.Client)
	caps := repo.NewCapabilityRepo(bundle.Client)
	app, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{
		ResourceID: "res-mail", ServerName: "mail", Entry: json.RawMessage(`{"command":"unused"}`),
	})
	require.NoError(t, err)

	_, err = caps.Upsert(ctx, repo.UpsertCapabilityInput{
		Name: mcpapps.CapabilityName("mail", "send_email"), Class: repo.CapClassSpend,
		EnforceableBy: []string{capability.EnforcerSpawn},
	})
	require.NoError(t, err)

	transport := fakeMailServer(t)
	r := mcpapps.Refresher{
		Apps:         apps,
		Secrets:      repo.NewApplicationSecretRepo(bundle.Client, nil),
		Capabilities: caps,
		Transport:    func(mcpapps.ServerEntry, map[string]string) (mcp.Transport, error) { return transport, nil },
		Now:          time.Now,
	}
	tools, err := r.Refresh(ctx, app.ResourceID)
	require.NoError(t, err)
	require.Len(t, tools, 2)

	search, err := caps.Get(ctx, "mcp__mail__search_emails")
	require.NoError(t, err)
	require.Equal(t, repo.CapClassTool, search.Class, "a new tool is never silently allowed")
	require.Equal(t, []string{capability.EnforcerSpawn}, search.EnforceableBy)

	send, err := caps.Get(ctx, "mcp__mail__send_email")
	require.NoError(t, err)
	require.Equal(t, repo.CapClassSpend, send.Class, "a refresh must not undo a stricter class")

	stored, err := apps.GetByResourceID(ctx, app.ResourceID)
	require.NoError(t, err)
	require.Len(t, stored.Catalogue, 2)
	require.Empty(t, stored.CatalogueError)
}

func TestRefresh_NonStdioEntryFailsWithExistingMessage(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	ctx := context.Background()

	apps := repo.NewMCPApplicationRepo(bundle.Client)
	caps := repo.NewCapabilityRepo(bundle.Client)
	app, err := apps.Upsert(ctx, repo.UpsertMCPApplicationInput{
		ResourceID: "res-mail", ServerName: "mail",
		Entry: json.RawMessage(`{"type":"http","url":"https://example.com"}`),
	})
	require.NoError(t, err)

	r := mcpapps.Refresher{
		Apps:         apps,
		Secrets:      repo.NewApplicationSecretRepo(bundle.Client, nil),
		Capabilities: caps,
		Transport:    mcpapps.StdioTransport,
		Now:          time.Now,
	}
	_, err = r.Refresh(ctx, app.ResourceID)
	require.ErrorContains(t, err, "supports stdio servers only")

	stored, err := apps.GetByResourceID(ctx, app.ResourceID)
	require.NoError(t, err)
	require.Contains(t, stored.CatalogueError, "supports stdio servers only")
}
