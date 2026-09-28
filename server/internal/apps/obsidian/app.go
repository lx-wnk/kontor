// Package obsidian is the Obsidian vault Application. It is an in-server
// module rather than a subprocess plugin: Obsidian's Local REST API is on
// the same machine, so a subprocess would add a hop without adding
// isolation. The registry entry Register writes is what makes this an
// Application rather than ordinary server code.
package obsidian

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Slug identifies the Obsidian application resource in the registry.
const Slug = "obsidian"

// Capability names, per spec §4.2.
const (
	CapabilityRead   = "obsidian.read"
	CapabilitySearch = "obsidian.search"
	CapabilityWrite  = "obsidian.write"
	CapabilityDelete = "obsidian.delete"
)

// capabilityDecls declares the four Obsidian capabilities. All are class
// "reach" — vault content leaves the vault toward the model, same reasoning
// that puts WebFetch in "reach" rather than "tool" — and enforceable only at
// the server: the vault is reached through this in-process client, never
// through a spawn allow-list (nothing to check before a process starts) or a
// PreToolUse hook (which sees tool calls, not resource operations).
//
// write and delete carry Reversible: false. Writing over a note destroys its
// prior content as thoroughly as deleting it, so a model that guarded only
// delete would give false comfort.
//
// Reversible is written to the catalogue and nothing reads it yet:
// capability.CapabilityView carries only Name, Class and EnforceableBy;
// capability.Decide never references reversibility; and the runtime
// catalogue built in serverapp/di.go drops it too. Consuming it — refusing a
// preset alone to satisfy an irreversible capability — needs two design
// decisions nobody has made: extending Decide's contract with
// reversibility, and a way to tell a preset-applied grant from an explicit
// human one, which the grant schema cannot express today. The column is
// still the right place to record the fact, so it is written here and left
// for that future decision to consume.
var capabilityDecls = []struct {
	Name       string
	Reversible bool
}{
	{CapabilityRead, true},
	{CapabilitySearch, true},
	{CapabilityWrite, false},
	{CapabilityDelete, false},
}

// Register gives the Obsidian vault its registry identity and catalogues its
// four capabilities. Idempotent — both Upsert calls resolve on conflict — so
// it is safe to run on every boot.
//
// Unlike repo.SeedCapabilities, this does not skip a capability that already
// has a row: these four names are a small, fixed, spec-defined set that this
// function is authoritative over, not a broad catalogue a human might have
// hand-edited, so refreshing them to match the code on every boot is the
// correct behaviour rather than a risk.
func Register(ctx context.Context, resources repo.ResourceRepo, caps repo.CapabilityRepo) error {
	if _, err := resources.Upsert(ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindApplication,
		Slug: Slug,
		Name: "Obsidian",
		// Builtin, not Local: this ships in the server binary rather than
		// being discovered on disk like a third-party plugin, so ResourceRepo
		// refuses to let it be deleted.
		Origin: repo.ResourceOriginBuiltin,
		Scope:  repo.GlobalScope(),
	}); err != nil {
		return fmt.Errorf("obsidian.Register: %w", err)
	}

	for _, decl := range capabilityDecls {
		if _, err := caps.Upsert(ctx, repo.UpsertCapabilityInput{
			Name:          decl.Name,
			Class:         repo.CapClassReach,
			EnforceableBy: []string{capability.EnforcerServer},
			Reversible:    decl.Reversible,
		}); err != nil {
			return fmt.Errorf("obsidian.Register: %w", err)
		}
	}
	return nil
}
