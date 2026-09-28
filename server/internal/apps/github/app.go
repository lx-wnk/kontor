// Package github is the GitHub Application. Like apps/obsidian it is an
// in-server module rather than a subprocess plugin or a client to a foreign
// MCP server: a plugin hop adds no isolation (plugins already run in this
// machine's trust domain) and a foreign MCP server would ask the capability
// gate to rule on tool names this project does not define. The registry entry
// Register writes is what makes this an Application rather than ordinary
// server code.
package github

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Slug identifies the GitHub application resource in the registry.
const Slug = "github"

// Capability names, per spec §4.2 (D2).
const (
	CapabilityRead    = "github.read"
	CapabilitySearch  = "github.search"
	CapabilityComment = "github.comment"
	CapabilityMerge   = "github.merge"
)

// CapabilityDecl is one row Register writes. Exported, unlike Obsidian's
// private equivalent, because the surface-parity test in
// internal/mcp/tools iterates it: it asserts that every capability declared
// here is reachable both over HTTP and as an MCP tool. A private list would
// force that test to retype the four names, which is exactly how a surface
// gets wired on one side only.
type CapabilityDecl struct {
	Name       string
	Class      string
	Reversible bool
}

// capabilityDecls declares the four GitHub capabilities.
//
// read, search and comment are class "reach": data leaves this machine
// toward a third party, or arrives from one — the same reasoning that puts
// the obsidian.* capabilities and WebFetch in "reach" rather than "tool".
//
// merge is class "spend", and that is the whole point of the class choice.
// capability.defaultEffect (internal/capability/decide.go:233-242) sends
// "spend" to EffectDeny and "reach" to EffectAsk, so with no grant a merge
// is refused outright rather than surfaced as a prompt a tired human clicks
// through at the end of a run. There is deliberately no "hold and prompt"
// fallback to rely on here.
//
// comment carries Reversible: false for the reason obsidian.write does: the
// comment is public the moment it posts, and deleting it afterwards does not
// unsend it. merge is irreversible for the obvious reason.
//
// Reversible is written to the catalogue and read by nothing today —
// capability.CapabilityView (decide.go:60-64) carries only Name, Class and
// EnforceableBy, and Decide never consults reversibility. It is recorded
// here anyway, same as apps/obsidian does, so the fact is stored where a
// future "a preset alone may not satisfy an irreversible capability" rule
// will look for it.
var capabilityDecls = []CapabilityDecl{
	{CapabilityRead, repo.CapClassReach, true},
	{CapabilitySearch, repo.CapClassReach, true},
	{CapabilityComment, repo.CapClassReach, false},
	{CapabilityMerge, repo.CapClassSpend, false},
}

// Capabilities returns a copy of the declarations, so a caller iterating them
// cannot reorder or mutate the catalogue this package is authoritative over.
func Capabilities() []CapabilityDecl {
	out := make([]CapabilityDecl, len(capabilityDecls))
	copy(out, capabilityDecls)
	return out
}

// Register gives GitHub its registry identity and catalogues its four
// capabilities. Idempotent — both Upsert calls resolve on conflict — so it is
// safe to run on every boot.
//
// Origin is Builtin, not Local: this ships in the server binary rather than
// being discovered on disk like a third-party plugin, so ResourceRepo refuses
// to let it be deleted. Scope is global: a personal access token is a
// machine-wide credential, not a per-project one.
func Register(ctx context.Context, resources repo.ResourceRepo, caps repo.CapabilityRepo) error {
	if _, err := resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind:   repo.ResourceKindApplication,
		Slug:   Slug,
		Name:   "GitHub",
		Origin: repo.ResourceOriginBuiltin,
		Scope:  repo.GlobalScope(),
	}); err != nil {
		return fmt.Errorf("github.Register: %w", err)
	}

	for _, decl := range capabilityDecls {
		if _, err := caps.Upsert(ctx, repo.UpsertCapabilityInput{
			Name:          decl.Name,
			Class:         decl.Class,
			EnforceableBy: []string{capability.EnforcerServer},
			Reversible:    decl.Reversible,
		}); err != nil {
			return fmt.Errorf("github.Register: %w", err)
		}
	}
	return nil
}
