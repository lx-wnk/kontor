package memory_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// TestRoutineGrantIsInertWithoutItsContext pins the defect this file's
// sibling test fixes: capability.Decide has ranked "routine" since the gate
// landed, and the grant API accepts a routine-scoped grant, but no caller
// ever put a routine context into a request — and Decide drops every grant
// whose context is not in the request's own chain. A grant that the UI shows
// as active therefore decided nothing.
//
// Passing no extra context reproduces that state exactly. If this test ever
// starts passing, the routine context leaked into Contexts' default chain,
// which would make a routine grant apply to work that routine never started.
func TestRoutineGrantIsInertWithoutItsContext(t *testing.T) {
	gate, ctx := newAuthorizeGateForTest(t, nil)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextRoutine, "sched-1"),
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope())
	require.Error(t, err, "a routine grant must not decide a request that names no routine")
}

// TestRoutineGrantAppliesToItsOwnRoutine is the same grant, reached by a
// caller that names the routine. This is the whole point of threading
// task.routine_id through to the gate.
func TestRoutineGrantAppliesToItsOwnRoutine(t *testing.T) {
	gate, ctx := newAuthorizeGateForTest(t, nil)
	_, err := gate.Grants.Create(ctx, repo.CreateGrantInput{
		CapabilityName: repo.CapabilityMemoryRead,
		Context:        repo.GrantContextFor(repo.GrantContextRoutine, "sched-1"),
		Mode:           repo.GrantModeAllow,
		GrantedBy:      "test",
	})
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope(),
		memory.RoutineContext("sched-1")...)
	require.NoError(t, err)

	err = gate.Authorize(ctx, repo.CapabilityMemoryRead, "", repo.GlobalScope(),
		memory.RoutineContext("sched-2")...)
	require.Error(t, err, "another routine's grant must not decide this request")
}

// TestRoutineGrantOutranksProjectGrant fixes the specificity order in a test
// rather than leaving it to contextRank alone: a routine is narrower than the
// project it runs in, so a routine-level deny must survive a project-level
// allow. Without this, reordering contextRank would silently widen every
// routine grant.
func TestRoutineGrantOutranksProjectGrant(t *testing.T) {
	gate, ctx := newAuthorizeGateForTest(t, nil)
	for _, in := range []repo.CreateGrantInput{
		{
			CapabilityName: repo.CapabilityMemoryRead,
			Context:        repo.GrantContextFor(repo.GrantContextProject, "/repo"),
			Mode:           repo.GrantModeAllow,
			GrantedBy:      "test",
		},
		{
			CapabilityName: repo.CapabilityMemoryRead,
			Context:        repo.GrantContextFor(repo.GrantContextRoutine, "sched-1"),
			Mode:           repo.GrantModeDeny,
			GrantedBy:      "test",
		},
	} {
		_, err := gate.Grants.Create(ctx, in)
		require.NoError(t, err)
	}

	scope := repo.ProjectScope("/repo")
	require.NoError(t, gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope),
		"the project grant decides when no routine is named")
	require.Error(t, gate.Authorize(ctx, repo.CapabilityMemoryRead, "", scope,
		memory.RoutineContext("sched-1")...),
		"the routine deny is more specific than the project allow")
}

// TestRoutineContextEmptyYieldsNoContext guards the case every human-created
// task takes. An empty ref would produce Context{Kind: "routine"}, which
// matches any grant stored with an empty ContextRef — the widening this
// helper exists to prevent.
func TestRoutineContextEmptyYieldsNoContext(t *testing.T) {
	require.Empty(t, memory.RoutineContext(""))
	require.Equal(t,
		[]capability.Context{{Kind: repo.GrantContextRoutine, Ref: "sched-1"}},
		memory.RoutineContext("sched-1"))
}
