package capability_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
)

func TestSpawnEnforcerEmitsOnlyAllowedEntries(t *testing.T) {
	e := capability.SpawnEnforcer{}
	decisions := []capability.Decision{
		{Effect: capability.EffectAllow, Enforceable: []string{capability.EnforcerSpawn}},
		{Effect: capability.EffectDeny, Enforceable: []string{capability.EnforcerSpawn}},
		{Effect: capability.EffectAsk, Enforceable: []string{capability.EnforcerSpawn}},
	}
	entries := []capability.AllowEntry{
		{Tool: "Bash", Pattern: "git status*"},
		{Tool: "Bash", Pattern: "curl evil"},
		{Tool: "WebFetch", Pattern: "domain:docs.example.com"},
	}

	got := e.AllowList(decisions, entries)
	want := []string{"Bash(git status*)"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("AllowList = %v, want %v — only an allow becomes a settings entry", got, want)
	}
}

func TestSpawnEnforcerAskIsNotAnAllow(t *testing.T) {
	e := capability.SpawnEnforcer{}
	got := e.AllowList(
		[]capability.Decision{{Effect: capability.EffectAsk, Enforceable: []string{capability.EnforcerSpawn}}},
		[]capability.AllowEntry{{Tool: "Bash", Pattern: "rm -rf"}},
	)
	if len(got) != 0 {
		t.Errorf("AllowList = %v, want empty — the spawn point cannot ask, so ask means not allowed here", got)
	}
}

// TestSpawnEnforcerRequiresSpawnInEnforceable proves an allow decision whose
// capability is not enforceable at the spawn point is excluded from the
// allow-list, even though its Effect is allow. Without the Enforceable
// check, this capability would end up spawn-enforced anyway, defeating the
// point of naming an enforcement point at all.
func TestSpawnEnforcerRequiresSpawnInEnforceable(t *testing.T) {
	e := capability.SpawnEnforcer{}
	got := e.AllowList(
		[]capability.Decision{
			{Effect: capability.EffectAllow, Enforceable: []string{capability.EnforcerHook}},
			{Effect: capability.EffectAllow, Enforceable: []string{capability.EnforcerSpawn, capability.EnforcerHook}},
			{Effect: capability.EffectAllow},
		},
		[]capability.AllowEntry{
			{Tool: "Mail", Pattern: ""},
			{Tool: "Bash", Pattern: "pnpm test"},
			{Tool: "Read", Pattern: ""},
		},
	)
	want := []string{"Bash(pnpm test)"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("AllowList = %v, want %v — only the entry naming EnforcerSpawn survives", got, want)
	}
}

func TestSpawnEnforcerPoint(t *testing.T) {
	if got := (capability.SpawnEnforcer{}).Point(); got != capability.EnforcerSpawn {
		t.Errorf("Point() = %q, want %q", got, capability.EnforcerSpawn)
	}
}
