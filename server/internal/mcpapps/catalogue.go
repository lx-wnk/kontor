package mcpapps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/schema"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/version"
)

// CapabilityName is the tool name exactly as Claude Code spells it in
// --allowedTools and --disallowedTools, so a decision renders without mapping.
func CapabilityName(serverName, toolName string) string {
	return "mcp__" + serverName + "__" + toolName
}

// IsApplicationTool reports whether tool names a tool of an attached MCP
// application — mcp__<server>__<tool> with a server the dashboard does not
// reserve for itself.
func IsApplicationTool(tool string) bool {
	rest, ok := strings.CutPrefix(tool, "mcp__")
	if !ok {
		return false
	}
	server, name, ok := strings.Cut(rest, "__")
	if !ok || server == "" || name == "" {
		return false
	}
	return !channelconfig.IsReservedServerName(server)
}

func ListTools(ctx context.Context, transport mcp.Transport) ([]schema.CatalogueTool, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "agent-dashboard", Version: version.Version}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer func() { _ = session.Close() }()

	var out []schema.CatalogueTool
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		entry := schema.CatalogueTool{Name: tool.Name, Description: tool.Description}
		if tool.Annotations != nil {
			entry.ReadOnlyHint = tool.Annotations.ReadOnlyHint
			entry.DestructiveHint = tool.Annotations.DestructiveHint
		}
		out = append(out, entry)
	}
	return out, nil
}

func StdioTransport(entry ServerEntry, env map[string]string) (mcp.Transport, error) {
	if !entry.IsStdio() {
		return nil, fmt.Errorf("the tool catalogue supports stdio servers only, this one is %q", entry.Type)
	}
	// #nosec G204 -- entry comes from the operator's user-scope ~/.claude.json, the file Claude Code itself reads to launch this
	// same server for every session; the dashboard runs exactly that command, as the same OS user, and exec.Command uses no shell.
	// Anyone able to write that file already runs as this user and could start the command directly.
	cmd := exec.Command(entry.Command, entry.Args...)
	cmd.Env = os.Environ()
	for k, v := range entry.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return &mcp.CommandTransport{Command: cmd}, nil
}

type Refresher struct {
	Apps         repo.MCPApplicationRepo
	Secrets      repo.ApplicationSecretRepo
	Capabilities repo.CapabilityRepo
	Transport    func(entry ServerEntry, env map[string]string) (mcp.Transport, error)
	Now          func() time.Time
}

func (r Refresher) Refresh(ctx context.Context, resourceID string) ([]schema.CatalogueTool, error) {
	app, err := r.Apps.GetByResourceID(ctx, resourceID)
	if err != nil {
		return nil, fmt.Errorf("mcpapps.Refresh: %w", err)
	}
	tools, listErr := r.list(ctx, app)
	msg := ""
	if listErr != nil {
		msg = listErr.Error()
	}
	if err := r.Apps.RecordCatalogue(ctx, resourceID, tools, msg, r.Now()); err != nil {
		return nil, fmt.Errorf("mcpapps.Refresh: %w", err)
	}
	if listErr != nil {
		return nil, listErr
	}
	for _, tool := range tools {
		name := CapabilityName(app.ServerName, tool.Name)
		if _, err := r.Capabilities.Get(ctx, name); err == nil {
			continue
		} else if !ent.IsNotFound(err) {
			return nil, fmt.Errorf("mcpapps.Refresh: %w", err)
		}
		if _, err := r.Capabilities.Upsert(ctx, repo.UpsertCapabilityInput{
			Name:          name,
			Class:         repo.CapClassTool,
			EnforceableBy: []string{capability.EnforcerSpawn},
			Description:   tool.Description,
		}); err != nil {
			return nil, fmt.Errorf("mcpapps.Refresh: %w", err)
		}
	}
	return tools, nil
}

func (r Refresher) list(ctx context.Context, app *ent.MCPApplication) ([]schema.CatalogueTool, error) {
	entry, err := ParseEntry(app.Entry)
	if err != nil {
		return nil, err
	}
	env, err := r.Secrets.Values(ctx, app.ResourceID)
	if err != nil {
		return nil, err
	}
	transport, err := r.Transport(entry, env)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return ListTools(ctx, transport)
}
