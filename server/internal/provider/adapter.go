package provider

import "github.com/lx-wnk/kontor/server/internal/parser"

// Adapter parses providers whose sessions are not file-per-session JSONL
// (IDE-embedded: Cursor, Copilot-in-VSCode, Windsurf). No adapter is registered
// in this plan; the seam exists so descriptors with source "custom:<id>" load
// without error and route here once an adapter is added.
type Adapter interface {
	// ConfigDirs returns existing config-dir paths for this provider.
	ConfigDirs() []string
	// ResolveSession returns the newest session for cwd, or (nil, false).
	ResolveSession(cwd string) (*parser.SessionData, bool)
}

var adapters = map[string]Adapter{}

// RegisterAdapter binds an adapter to a custom-source id. Unused this plan.
func RegisterAdapter(id string, a Adapter) { adapters[id] = a }
