package serverapp

import (
	"context"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// moduleToolGate decides which module tools a caller may see and use.
//
// The two questions are answered differently on purpose. Visibility is asked on
// every tools/list, so it is one query that records nothing: Gate.Authorize
// books usage against the grant's rate limit, and asking it once per listed
// tool would spend on a list what exists to bound real calls. A call goes
// through the full gate, which is where recording usage and asking a human
// belong.
type moduleToolGate struct {
	gate   memory.Gate
	grants repo.GrantRepo
}

func (m moduleToolGate) Visible(ctx context.Context, capabilities []string) map[string]bool {
	out := make(map[string]bool, len(capabilities))
	rows, err := m.grants.ListForCapabilities(ctx, capabilities)
	if err != nil {
		// Fail closed: an unreadable grant store means nothing is shown.
		slog.Warn("module tools: grant lookup failed, listing none", "err", err)
		return out
	}
	for _, row := range rows {
		if row.RevokedAt != nil {
			continue
		}
		out[row.CapabilityName] = true
	}
	return out
}

func (m moduleToolGate) Authorize(ctx context.Context, capability string) error {
	// Global scope for now: the caller's task or routine identity is not yet
	// carried on an MCP request, so a grant is awarded to the installation
	// rather than to one task. Narrowing it is what task-scoped credentials
	// will make possible.
	return m.gate.Authorize(ctx, capability, "", repo.GlobalScope())
}
