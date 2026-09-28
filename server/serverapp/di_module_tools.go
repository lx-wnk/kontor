package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// moduleToolTimeout bounds a single module tool call. An agent is waiting on
// the other end, so a module that has stopped answering must fail rather than
// hold the call open.
const moduleToolTimeout = 30 * time.Second

// moduleToolSource exposes the tools loaded modules offer. It reads the live
// registry on every call: a module that stopped or went unhealthy between two
// requests must disappear from the list rather than be listed and then fail.
type moduleToolSource struct {
	registry *plugin.Registry
	client   *http.Client
}

func newModuleToolSource(registry *plugin.Registry) *moduleToolSource {
	return &moduleToolSource{
		registry: registry,
		client:   &http.Client{Timeout: moduleToolTimeout},
	}
}

func (m *moduleToolSource) List(context.Context) []mcp.ModuleTool {
	if m.registry == nil {
		return nil
	}
	var out []mcp.ModuleTool
	for _, entry := range m.registry.All() {
		if !entry.Healthy() {
			continue
		}
		for _, tool := range entry.Descriptor.Tools {
			out = append(out, mcp.ModuleTool{
				ModuleID:    entry.Descriptor.ID,
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
		}
	}
	return out
}

func (m *moduleToolSource) Call(ctx context.Context, moduleID, tool string, args map[string]any) (string, error) {
	entry, ok := m.registry.Lookup(moduleID)
	if !ok || !entry.Healthy() {
		return "", fmt.Errorf("module %q is not available", moduleID)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("module %q tool %q: %w", moduleID, tool, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, entry.BaseURL+"/tools/"+tool, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("module %q tool %q: %w", moduleID, tool, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("module %q tool %q: %w", moduleID, tool, err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("module %q tool %q: %w", moduleID, tool, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("module %q tool %q: HTTP %d: %s", moduleID, tool, resp.StatusCode, string(payload))
	}
	return string(payload), nil
}
