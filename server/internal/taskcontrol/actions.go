package taskcontrol

import "fmt"

// Action describes a single control surface action that the UI or MCP can
// present or invoke for a task. Disabled actions with a non-empty Reason
// should be shown grayed-out with the reason as a tooltip.
type Action struct {
	Action  string `json:"action"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
	Primary bool   `json:"primary"`
}

// action name constants — single source of truth; never type these strings elsewhere.
const (
	ActionRefine            = "refine"
	ActionApproveSpec       = "approve_spec"
	ActionAdvance           = "advance"
	ActionRetry             = "retry"
	ActionBack              = "back"
	ActionCancel            = "cancel"
	ActionHold              = "hold"
	ActionResume            = "resume"
	ActionApproveAllPending = "approve_all_pending"
	ActionOpenPR            = "open_pr"
)

// enabled returns an enabled Action, optionally marked primary.
func enabled(name string, primary bool) Action {
	return Action{Action: name, Enabled: true, Primary: primary}
}

// disabled returns a disabled Action with an explanatory reason.
func disabled(name, reason string) Action {
	return Action{Action: name, Enabled: false, Reason: reason}
}

// ComputeActions returns the full set of contextually relevant actions for
// the given TaskState. Exactly one enabled action carries Primary:true (the
// happy-path next step), except for terminal stages where no action is primary.
//
// Rules encoded here are the single source of truth for task state-machine
// transitions visible to the UI and MCP. Do not duplicate this logic elsewhere.
func ComputeActions(s TaskState) []Action {
	// Terminal stages: nothing meaningful can happen.
	if s.Stage == "done" || s.Stage == "cancelled" {
		return []Action{
			disabled(ActionAdvance, "task is terminal"),
			disabled(ActionRetry, "task is terminal"),
			disabled(ActionResume, "task is terminal"),
			disabled(ActionCancel, "task is terminal"),
			disabled(ActionApproveAllPending, "task is terminal"),
			disabled(ActionOpenPR, "task is terminal"),
		}
	}

	// Backlog stage: spec authoring and approval flow.
	if s.Stage == "backlog" {
		return backlogActions(s)
	}

	// on_hold: a human explicitly parked the task.
	if s.Stage == "on_hold" {
		return []Action{
			enabled(ActionResume, true),
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "task is on hold — resume first"),
			disabled(ActionRetry, "task is on hold — resume first"),
			disabled(ActionApproveAllPending, "task is on hold"),
		}
	}

	// Agent-driven stages: implementation, self_review, finalization.
	return agentStageActions(s)
}

// backlogActions handles the backlog stage state machine.
// Backlog is the only stage where a refine runner may be active, and where
// "approve_spec" is the key gate to moving the task forward.
func backlogActions(s TaskState) []Action {
	// Refine runner in flight — wait for it; advance is the tentative next step.
	// RefineStatus values mirror refine.Status* ("refining"/"draft_ready"/...).
	if s.RunStatus == "running" || s.RefineStatus == "refining" {
		return []Action{
			enabled(ActionAdvance, true),
			enabled(ActionCancel, false),
			disabled(ActionApproveSpec, "spec refinement is still running"),
			disabled(ActionRetry, "refinement is in progress"),
			disabled(ActionResume, "task is running"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}
	}

	// Human is needed (awaiting_user or on_hold run).
	if s.NeedsUser || s.RunStatus == "awaiting_user" {
		return []Action{
			enabled(ActionResume, true),
			enabled(ActionCancel, false),
			disabled(ActionApproveSpec, "task needs user action before approval"),
			disabled(ActionAdvance, "task needs user action"),
			disabled(ActionRetry, "task needs user action"),
		}
	}

	// Spec draft is ready (refine done or task freshly created with no run).
	// This is the normal idle state after backlog authoring.
	if s.RefineStatus == "draft_ready" || s.RunStatus == "" || s.RunStatus == "done" {
		return []Action{
			enabled(ActionApproveSpec, true),
			enabled(ActionRefine, false),
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "use approve_spec to advance from backlog"),
			disabled(ActionRetry, "no failed run to retry"),
			disabled(ActionResume, "task is not awaiting user"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}
	}

	// Failed refine run — let the user retry or just approve-and-move-on.
	if s.RunStatus == "failed" {
		return []Action{
			enabled(ActionApproveSpec, true),
			enabled(ActionRefine, false),
			enabled(ActionCancel, false),
			disabled(ActionRetry, "use refine to re-run, or approve_spec to advance"),
			disabled(ActionAdvance, "use approve_spec to advance from backlog"),
			disabled(ActionResume, "task is not awaiting user"),
		}
	}

	// Requeued or any other transient run status — treat like idle spec-ready.
	return []Action{
		enabled(ActionApproveSpec, true),
		enabled(ActionRefine, false),
		enabled(ActionCancel, false),
		disabled(ActionAdvance, "use approve_spec to advance from backlog"),
		disabled(ActionRetry, "no failed run to retry"),
		disabled(ActionResume, "task is not awaiting user"),
	}
}

// agentStageActions handles implementation, self_review, and finalization.
func agentStageActions(s TaskState) []Action {
	switch s.RunStatus {
	case "running":
		// Agent process is live — the only valid user action is cancel.
		return []Action{
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "agent is running — wait for completion"),
			disabled(ActionRetry, "agent is still running"),
			disabled(ActionResume, "agent is still running"),
			disabled(ActionApproveAllPending, "no pending permissions while running"),
		}

	case "awaiting_user":
		// Permission requests take priority: approve them first.
		if s.PendingPerms > 0 {
			return []Action{
				enabled(ActionApproveAllPending, true),
				enabled(ActionCancel, false),
				disabled(ActionAdvance, fmt.Sprintf("blocked: %d pending permission(s)", s.PendingPerms)),
				disabled(ActionRetry, "resolve pending permissions first"),
				disabled(ActionResume, "resolve pending permissions first"),
			}
		}
		// No pending perms — task is waiting on the user for another reason.
		return []Action{
			enabled(ActionResume, true),
			enabled(ActionCancel, false),
			disabled(ActionApproveAllPending, "no pending permissions"),
			disabled(ActionAdvance, "task is awaiting user action"),
			disabled(ActionRetry, "task is awaiting user, not failed"),
		}

	case "failed":
		return []Action{
			enabled(ActionRetry, true),
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "stage run failed — retry first"),
			disabled(ActionResume, "task failed, not awaiting user"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}

	case "done":
		// This stage_run is done; pipeline will auto-advance. While it hasn't yet
		// (or the task reached a non-terminal stage whose run is done), let the
		// user manually advance.
		return []Action{
			enabled(ActionAdvance, true),
			enabled(ActionCancel, false),
			disabled(ActionRetry, "stage run completed successfully"),
			disabled(ActionResume, "task is not awaiting user"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}

	case "on_hold":
		return []Action{
			enabled(ActionResume, true),
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "task is on hold — resume first"),
			disabled(ActionRetry, "task is on hold — resume first"),
			disabled(ActionApproveAllPending, "task is on hold"),
		}

	case "rate_limited":
		// Waiting for API usage reset; will auto-requeue after cooldown.
		return []Action{
			enabled(ActionRetry, false),
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "paused: usage limit — auto-requeue pending"),
			disabled(ActionResume, "paused: usage limit — auto-requeue pending"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}

	case "requeued", "pending":
		// Auto-requeue or pending spawn — agent will pick it up; wait or cancel.
		return []Action{
			enabled(ActionCancel, false),
			disabled(ActionAdvance, "agent spawn is queued"),
			disabled(ActionRetry, "already queued for retry"),
			disabled(ActionResume, "agent is queued, not awaiting user"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}

	default:
		// No run yet or unknown status — allow manual advance as the primary action.
		return []Action{
			enabled(ActionAdvance, true),
			enabled(ActionCancel, false),
			disabled(ActionRetry, "no failed run to retry"),
			disabled(ActionResume, "task is not awaiting user"),
			disabled(ActionApproveAllPending, "no pending permissions"),
		}
	}
}
