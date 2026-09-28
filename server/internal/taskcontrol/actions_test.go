package taskcontrol_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// primaryAction returns the single action with Primary:true, or fails if none/many.
func primaryAction(t *testing.T, actions []taskcontrol.Action) taskcontrol.Action {
	t.Helper()
	var primaries []taskcontrol.Action
	for _, a := range actions {
		if a.Primary {
			primaries = append(primaries, a)
		}
	}
	if len(primaries) == 0 {
		return taskcontrol.Action{} // terminal — no primary
	}
	require.Len(t, primaries, 1, "exactly one action must have Primary:true")
	return primaries[0]
}

// findAction returns the first action with the given name, or an empty Action.
func findAction(actions []taskcontrol.Action, name string) taskcontrol.Action {
	for _, a := range actions {
		if a.Action == name {
			return a
		}
	}
	return taskcontrol.Action{}
}

// TestComputeActions covers the main state-machine transitions required
// by the sub-project spec.
func TestComputeActions(t *testing.T) {
	tests := []struct {
		name         string
		state        taskcontrol.TaskState
		wantPrimary  string // "" means no primary (terminal)
		wantEnabled  []string
		wantDisabled []string
		wantReasonIn map[string]string // action → substring that must appear in Reason
	}{
		{
			name:  "backlog stage, refining in progress",
			state: taskcontrol.FromFields("backlog", "", "refining", 0, false),
			// Backlog is running refine — primary is advance (spec flowing)
			wantPrimary:  "advance",
			wantEnabled:  []string{"advance", "cancel"},
			wantDisabled: []string{"approve_spec", "retry", "resume"},
		},
		{
			name:  "backlog stage, draft ready, no run",
			state: taskcontrol.FromFields("backlog", "", "draft_ready", 0, false),
			// Spec complete, awaiting human approval to move forward
			wantPrimary:  "approve_spec",
			wantEnabled:  []string{"approve_spec", "refine", "cancel"},
			wantDisabled: []string{"retry", "advance", "resume"},
		},
		{
			name:  "backlog stage, awaiting_user (run blocked)",
			state: taskcontrol.FromFields("backlog", "awaiting_user", "", 0, true),
			// Needs human to look at it; resume is the primary
			wantPrimary:  "resume",
			wantEnabled:  []string{"resume", "cancel"},
			wantDisabled: []string{"approve_spec", "advance", "retry"},
		},
		{
			name:  "implementation stage, running — no pending perms",
			state: taskcontrol.FromFields("implementation", "running", "", 0, false),
			// Agent is running, only thing to do is watch or cancel
			wantPrimary:  "",
			wantEnabled:  []string{"cancel"},
			wantDisabled: []string{"advance", "retry", "resume", "approve_all_pending"},
		},
		{
			name:         "implementation stage, failed",
			state:        taskcontrol.FromFields("implementation", "failed", "", 0, false),
			wantPrimary:  "retry",
			wantEnabled:  []string{"retry", "cancel"},
			wantDisabled: []string{"advance", "resume", "approve_all_pending"},
		},
		{
			name:  "implementation stage, awaiting_user with pending permissions",
			state: taskcontrol.FromFields("implementation", "awaiting_user", "", 3, true),
			// Blocked by permissions — approve_all_pending is primary; advance disabled with reason
			wantPrimary:  "approve_all_pending",
			wantEnabled:  []string{"approve_all_pending", "cancel"},
			wantDisabled: []string{"advance", "retry"},
			wantReasonIn: map[string]string{
				"advance": "3 pending",
			},
		},
		{
			name:  "self_review stage, running",
			state: taskcontrol.FromFields("self_review", "running", "", 0, false),
			// Agent reviewing — nothing to do but wait or cancel
			wantPrimary:  "",
			wantEnabled:  []string{"cancel"},
			wantDisabled: []string{"retry", "advance", "resume", "approve_all_pending"},
		},
		{
			name:         "finalization stage, running",
			state:        taskcontrol.FromFields("finalization", "running", "", 0, false),
			wantPrimary:  "",
			wantEnabled:  []string{"cancel"},
			wantDisabled: []string{"retry", "advance", "resume"},
		},
		{
			name:  "done — terminal, no enabled actions",
			state: taskcontrol.FromFields("done", "", "", 0, false),
			// Terminal — no enabled actions at all (open_pr may be present but disabled)
			wantPrimary:  "",
			wantEnabled:  []string{},
			wantDisabled: []string{"cancel", "retry", "advance", "resume"},
		},
		{
			name:         "cancelled — terminal, no enabled actions",
			state:        taskcontrol.FromFields("cancelled", "", "", 0, false),
			wantPrimary:  "",
			wantEnabled:  []string{},
			wantDisabled: []string{"retry", "advance"},
		},
		{
			name:         "implementation, awaiting_user, no pending perms — resume is primary",
			state:        taskcontrol.FromFields("implementation", "awaiting_user", "", 0, true),
			wantPrimary:  "resume",
			wantEnabled:  []string{"resume", "cancel"},
			wantDisabled: []string{"approve_all_pending", "advance"},
		},
		{
			name:  "on_hold stage",
			state: taskcontrol.FromFields("on_hold", "", "", 0, true),
			// Task on hold — resume is the primary
			wantPrimary:  "resume",
			wantEnabled:  []string{"resume", "cancel"},
			wantDisabled: []string{"advance", "retry"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actions := taskcontrol.ComputeActions(tc.state)
			require.NotNil(t, actions, "ComputeActions must not return nil")

			primary := primaryAction(t, actions)
			assert.Equal(t, tc.wantPrimary, primary.Action,
				"primary action mismatch; got primary %q", primary.Action)

			for _, name := range tc.wantEnabled {
				a := findAction(actions, name)
				assert.True(t, a.Enabled, "action %q should be enabled", name)
			}
			for _, name := range tc.wantDisabled {
				a := findAction(actions, name)
				assert.False(t, a.Enabled, "action %q should be disabled", name)
			}
			for name, wantSub := range tc.wantReasonIn {
				a := findAction(actions, name)
				assert.Contains(t, a.Reason, wantSub,
					"action %q Reason should contain %q", name, wantSub)
			}
		})
	}
}

// TestComputeActions_ExactlyOnePrimaryAmongEnabled verifies the invariant that
// at most one enabled action carries Primary:true.
func TestComputeActions_ExactlyOnePrimaryAmongEnabled(t *testing.T) {
	states := []taskcontrol.TaskState{
		taskcontrol.FromFields("backlog", "", "refining", 0, false),
		taskcontrol.FromFields("backlog", "", "draft_ready", 0, false),
		taskcontrol.FromFields("implementation", "running", "", 0, false),
		taskcontrol.FromFields("implementation", "failed", "", 0, false),
		taskcontrol.FromFields("implementation", "awaiting_user", "", 3, true),
		taskcontrol.FromFields("done", "", "", 0, false),
		taskcontrol.FromFields("cancelled", "", "", 0, false),
	}
	for _, s := range states {
		actions := taskcontrol.ComputeActions(s)
		var primaries int
		for _, a := range actions {
			if a.Primary && a.Enabled {
				primaries++
			}
		}
		assert.LessOrEqual(t, primaries, 1,
			"at most one enabled action may be Primary for stage=%q runStatus=%q",
			s.Stage, s.RunStatus)
	}
}

// TestComputeActions_NoPrimaryForTerminal verifies terminal stages return no primary.
func TestComputeActions_NoPrimaryForTerminal(t *testing.T) {
	for _, stage := range []string{"done", "cancelled"} {
		actions := taskcontrol.ComputeActions(taskcontrol.FromFields(stage, "", "", 0, false))
		p := primaryAction(t, actions)
		assert.Empty(t, p.Action, "terminal stage %q must not have a primary", stage)
	}
}
