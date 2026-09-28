package tasks

import (
	"net/http"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

// Decisions a human can make on a pending permission request.
const (
	DecisionAllowOnce    = "allow_once"
	DecisionAllowRoutine = "allow_routine"
	DecisionDenyRoutine  = "deny_routine"
	DecisionDenyOnce     = "deny_once"
)

const decisionUsage = "decision must be allow_once, allow_routine, deny_routine or deny_once"

// parseDecision accepts decision, or the legacy outcome ("granted"/"denied") as an alias.
func parseDecision(decision, outcome string) (string, error) {
	switch decision {
	case DecisionAllowOnce, DecisionAllowRoutine, DecisionDenyRoutine, DecisionDenyOnce:
		if outcome == "" {
			return decision, nil
		}
		alias, err := outcomeAlias(outcome)
		if err != nil {
			return "", err
		}
		if alias != decision {
			return "", apierr.NewAppError(http.StatusBadRequest, "decision and outcome disagree")
		}
		return decision, nil
	case "":
		if outcome == "" {
			return "", apierr.NewAppError(http.StatusBadRequest, decisionUsage)
		}
		return outcomeAlias(outcome)
	default:
		return "", apierr.NewAppError(http.StatusBadRequest, decisionUsage)
	}
}

func outcomeAlias(outcome string) (string, error) {
	switch outcome {
	case "granted":
		return DecisionAllowOnce, nil
	case "denied":
		return DecisionDenyOnce, nil
	default:
		return "", apierr.NewAppError(http.StatusBadRequest, "outcome must be granted or denied")
	}
}

// validateDecision refuses a routine decision the task or tools cannot take.
func validateDecision(decision string, task *ent.Task, tools []string) error {
	if decision != DecisionAllowRoutine && decision != DecisionDenyRoutine {
		return nil
	}
	if task.RoutineID == nil || *task.RoutineID == "" {
		return apierr.NewAppError(http.StatusBadRequest, "routine decisions need a task started by a routine")
	}
	for _, tool := range tools {
		if !mcpapps.IsApplicationTool(tool) {
			return apierr.NewAppError(http.StatusBadRequest, "routine decisions apply to application tools only")
		}
	}
	return nil
}

// decisionGrants returns the grant rows a decision writes for one tool.
func decisionGrants(decision string, task *ent.Task, tool, grantedBy string) []repo.CreateGrantInput {
	switch decision {
	case DecisionAllowOnce:
		if !mcpapps.IsApplicationTool(tool) {
			return nil
		}
		return []repo.CreateGrantInput{{
			CapabilityName: tool,
			Context:        repo.GrantContextFor(repo.GrantContextTask, task.ID),
			Mode:           repo.GrantModeAllow,
			GrantedBy:      grantedBy,
			Reason:         "permission decision allow_once",
		}}
	case DecisionAllowRoutine:
		return []repo.CreateGrantInput{{
			CapabilityName: tool,
			Context:        repo.GrantContextFor(repo.GrantContextRoutine, *task.RoutineID),
			Mode:           repo.GrantModeAllow,
			GrantedBy:      grantedBy,
			Reason:         "permission decision allow_routine",
		}}
	case DecisionDenyRoutine:
		return []repo.CreateGrantInput{{
			CapabilityName: tool,
			Context:        repo.GrantContextFor(repo.GrantContextRoutine, *task.RoutineID),
			Mode:           repo.GrantModeDeny,
			GrantedBy:      grantedBy,
			Reason:         "permission decision deny_routine",
		}}
	default:
		return nil
	}
}

// decisionResumePrompt is the prompt the run resumes with after the decision.
func decisionResumePrompt(decision string, tools []string) string {
	switch decision {
	case DecisionDenyOnce:
		return deniedResumePrompt(tools)
	case DecisionDenyRoutine:
		return "A human denied these tools for this routine, permanently: " + strings.Join(tools, ", ") +
			". Continue without them and state in your output what you could not do because of that."
	default:
		return ""
	}
}
