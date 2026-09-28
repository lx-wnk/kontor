package tools

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/mcp"
)

// A module may act on the routines it brought and on nothing else. Without
// this a module that was allowed to manage schedules could disable the
// operator's — a far larger permission than "may bring routines along".
func TestEnsureModuleOwnsRoutine(t *testing.T) {
	asModule := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k", ModuleID: "obsidian"})
	asAgent := mcp.ContextWithAuth(context.Background(), &mcp.MCPAuthInfo{KeyID: "k"})

	if err := ensureModuleOwnsRoutine(asModule, "obsidian"); err != nil {
		t.Errorf("a module must reach its own routine: %v", err)
	}
	if err := ensureModuleOwnsRoutine(asModule, "github"); err == nil {
		t.Error("a module must not reach another module's routine")
	}
	if err := ensureModuleOwnsRoutine(asModule, ""); err == nil {
		t.Error("a module must not reach a routine a human created")
	}
	if err := ensureModuleOwnsRoutine(asAgent, "obsidian"); err != nil {
		t.Errorf("a caller that is not a module is governed by the ordinary rules: %v", err)
	}
}
