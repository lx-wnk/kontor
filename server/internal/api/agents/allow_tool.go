package agents

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// askUserQuestionTool is Claude Code's interactive multiple-choice tool. It
// reaches the client as a pending tool use (so a session whose screen cannot be
// probed still shows that a question is open), but it is answered by the human
// rather than granted, so it is never a valid allow-rule target.
const askUserQuestionTool = "AskUserQuestion"

// AllowToolHandler handles POST /api/agents/{pid}/allow-tool.
// It creates a stable allow preset for the given tool+pattern keyed by the
// running agent's cwd. This affects FUTURE permission checks only — it does
// NOT answer or resume the currently paused agent prompt.
type AllowToolHandler struct {
	getAgents  GetAgentsFn
	presetRepo repo.PermissionPresetRepo
}

// NewAllowToolHandler creates an AllowToolHandler.
func NewAllowToolHandler(getAgents GetAgentsFn, presetRepo repo.PermissionPresetRepo) *AllowToolHandler {
	return &AllowToolHandler{getAgents: getAgents, presetRepo: presetRepo}
}

type allowToolRequest struct {
	Tool    string  `json:"tool"`
	Pattern *string `json:"pattern"`
}

type allowToolResponse struct {
	ProjectCwd string  `json:"projectCwd"`
	Tool       string  `json:"tool"`
	Pattern    *string `json:"pattern"`
}

// AllowTool handles POST /api/agents/{pid}/allow-tool.
func (h *AllowToolHandler) AllowTool(w http.ResponseWriter, r *http.Request) error {
	pidStr := r.PathValue("pid")
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return apierr.NewAppError(http.StatusBadRequest, "invalid pid")
	}

	var body allowToolRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Tool == "" {
		return apierr.NewAppError(http.StatusBadRequest, "tool is required")
	}
	// AskUserQuestion is not permission-gated: an unresolved one is waiting for
	// the user's ANSWER, not for a grant, so a standing allow-rule for it would
	// be meaningless. The dashboard already hides the button; reject it here too
	// so the endpoint cannot be used to write that rule directly.
	if body.Tool == askUserQuestionTool {
		return apierr.NewAppError(http.StatusBadRequest, askUserQuestionTool+" is answered, not granted — it cannot be allowed")
	}

	agents, err := h.getAgents(r.Context())
	if err != nil {
		return fmt.Errorf("allow-tool: get agents: %w", err)
	}
	var cwd string
	for _, a := range agents {
		if a.PID == pid {
			cwd = a.CWD
			break
		}
	}
	if cwd == "" {
		return apierr.NewAppError(http.StatusNotFound, "agent not found or no CWD")
	}

	var userID *string
	if payload, ok := auth.PayloadFromContext(r.Context()); ok {
		userID = &payload.Sub
	}

	input := repo.UpsertPresetInput{
		UserID:     userID,
		ProjectCwd: cwd,
		Tool:       body.Tool,
		Pattern:    body.Pattern,
	}
	if err := h.presetRepo.Upsert(r.Context(), input); err != nil {
		return fmt.Errorf("allow-tool: upsert preset: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(allowToolResponse{
		ProjectCwd: cwd,
		Tool:       body.Tool,
		Pattern:    body.Pattern,
	})
}
