package repo

import (
	"context"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// DefaultCapabilityView is the class and enforcement-point set a grantable
// tool name gets when it has no catalogue row yet: class "tool" for
// everything except WebFetch ("reach"), enforceable at {spawn, hook} — both
// points see tool calls.
//
// This is the single place that classification lives. SeedCapabilities below
// and pipeline's capabilityViewFor fallback (server/internal/pipeline/spawner.go)
// both call it instead of each holding their own copy of the same literals —
// two independent copies could silently drift apart; one shared function
// turns that into a compile error instead.
func DefaultCapabilityView(name string) capability.CapabilityView {
	class := CapClassTool
	if name == "WebFetch" {
		class = CapClassReach
	}
	return capability.CapabilityView{
		Name:          name,
		Class:         class,
		EnforceableBy: []string{capability.EnforcerSpawn, capability.EnforcerHook},
	}
}

// memoryCapabilityViews are the resource-class capabilities memory access
// needs. permissions.GrantableToolNames does not carry them — they are not
// Claude Code tool names — and DefaultCapabilityView's tool/reach split does
// not apply, so they get their own views here.
//
// Enforceable only at EnforcerServer: memory access arrives through the
// server process and the MCP endpoint it exposes, never through a spawn
// allow-list (written before a process starts, so it has nothing to check
// against) or a PreToolUse hook (which sees tool calls, not resource reads
// or writes).
var memoryCapabilityViews = []capability.CapabilityView{
	{Name: CapabilityMemoryRead, Class: CapClassResource, EnforceableBy: []string{capability.EnforcerServer}},
	{Name: CapabilityMemoryWrite, Class: CapClassResource, EnforceableBy: []string{capability.EnforcerServer}},
}

// SeedCapabilities gives every grantable tool name, plus the memory
// capabilities, a capability catalogue row. Without this, the capabilities
// table stays empty and every lookup resolves to a zero-value
// CapabilityView — an unrecognised class, which defaultEffect sends to deny.
// Seeding is what makes the gate's fail-closed default apply only to
// capabilities that were never named, not to every capability that exists.
//
// Tool names come from permissions.GrantableToolNames, the same set
// permissions.IsAllowedTool reads, so the catalogue cannot drift from the
// allow-list; their class and enforcement come from DefaultCapabilityView.
// The memory capabilities come from memoryCapabilityViews above.
//
// Idempotent: a name that already has a row is left untouched rather than
// upserted, so a human who has since edited a row's class through the
// catalogue is not overwritten on the next boot. Only missing rows are
// created. A name whose insert fails is logged and skipped rather than
// aborting the loop — one bad name must not stop every name ordered after it.
//
// Returns only a count, never an error: every failure path above is
// warn-and-continue, so there is no outcome an error return could carry that
// the returned count and the logged warnings do not already.
func SeedCapabilities(ctx context.Context, capabilities CapabilityRepo) int {
	seeded, skipped := 0, 0
	seedOne := func(view capability.CapabilityView) {
		// "not found" is detected via ent.IsNotFound unwrapping through Get's
		// fmt.Errorf("capability.Get: %w", err) — errors.As traverses that
		// wrap today. If Get's error wrapping ever changes to something
		// ent.IsNotFound can no longer see through, every name silently takes
		// the "skip and warn" branch below instead of being seeded.
		if _, err := capabilities.Get(ctx, view.Name); err == nil {
			return // already catalogued — do not overwrite a class a human may have changed
		} else if !ent.IsNotFound(err) {
			skipped++
			slog.Warn("seed capabilities: skipped", "name", view.Name, "err", err)
			return
		}

		if _, err := capabilities.Upsert(ctx, UpsertCapabilityInput{
			Name:          view.Name,
			Class:         view.Class,
			EnforceableBy: view.EnforceableBy,
		}); err != nil {
			skipped++
			slog.Warn("seed capabilities: skipped", "name", view.Name, "err", err)
			return
		}
		seeded++
	}

	for _, name := range permissions.GrantableToolNames() {
		seedOne(DefaultCapabilityView(name))
	}
	for _, view := range memoryCapabilityViews {
		seedOne(view)
	}

	if skipped > 0 {
		slog.Warn("seed capabilities: some names were not seeded", "seeded", seeded, "skipped", skipped)
	}
	return seeded
}
