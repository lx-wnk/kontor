package agents

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// ChannelStageOutputHandler handles POST /api/channel-stage-output.
// The channel bridge posts the structured stage output here; it is validated
// against the per-stage schema before being persisted to stage_runs.output.
type ChannelStageOutputHandler struct {
	stageRuns repo.StageRunRepo
	apiKeys   repo.ApiKeyRepo
	audit     repo.AuditEventRepo
}

// NewChannelStageOutputHandler creates a handler backed by the given repos.
// audit may be nil, which disables the stage_output_submitted record without
// changing any behaviour the agent can observe.
func NewChannelStageOutputHandler(stageRuns repo.StageRunRepo, apiKeys repo.ApiKeyRepo, audit repo.AuditEventRepo) *ChannelStageOutputHandler {
	return &ChannelStageOutputHandler{stageRuns: stageRuns, apiKeys: apiKeys, audit: audit}
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// Post handles POST /api/channel-stage-output.
func (h *ChannelStageOutputHandler) Post(w http.ResponseWriter, r *http.Request) {
	// Use a raw decode first so we can distinguish a missing "output" key from
	// an explicitly provided (but possibly empty) map.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	stageRunIDRaw, hasID := raw["stageRunId"]
	outputRaw, hasOutput := raw["output"]
	var stageRunID string
	var output map[string]any
	if hasID {
		if err := json.Unmarshal(stageRunIDRaw, &stageRunID); err != nil {
			writeJSONError(w, http.StatusBadRequest, "stageRunId must be a string")
			return
		}
	}
	if hasOutput {
		if err := json.Unmarshal(outputRaw, &output); err != nil {
			writeJSONError(w, http.StatusBadRequest, "output must be an object")
			return
		}
	}

	if stageRunID == "" || !hasOutput || output == nil {
		writeJSONError(w, http.StatusBadRequest, "missing stageRunId or output")
		return
	}

	hash := mcp.HashToken(bearerToken(r))
	key, err := h.apiKeys.GetByHash(r.Context(), hash)
	if err != nil {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch key.Kind {
	case repo.ApiKeyKindStageRun:
		if key.StageRunID != stageRunID {
			writeJSONError(w, http.StatusForbidden, "key not issued for this stage run")
			return
		}
	case repo.ApiKeyKindUser, "":
		// operator's own credential; Create normalises "" to user, tolerated
		// here for rows written outside the repo.
	default:
		writeJSONError(w, http.StatusForbidden, "key not issued for this stage run")
		return
	}

	sr, err := h.stageRuns.GetByID(r.Context(), stageRunID)
	if err != nil || sr == nil {
		writeJSONError(w, http.StatusNotFound, "stage_run not found")
		return
	}

	if sr.Status == "done" || sr.Status == "failed" {
		writeJSONError(w, http.StatusConflict, "stage run already finalized")
		return
	}

	if v := pipeline.ValidateStageOutput(sr.Stage, output); !v.OK {
		writeJSONError(w, http.StatusUnprocessableEntity, v.Error)
		return
	}

	if _, err := h.stageRuns.Update(r.Context(), sr.ID, repo.UpdateStageRunInput{Output: output}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to persist output")
		return
	}

	// Records that this stage result arrived through the set_stage_output tool
	// rather than being scraped from the session transcript. The two are
	// indistinguishable afterwards — both end as stage_runs.output — so without
	// this event there is no way to measure how often the tool channel works.
	// Best-effort: a failed audit write must not fail an accepted stage output.
	if h.audit != nil {
		if err := h.audit.RecordTaskAudit(r.Context(), sr.TaskID, nil, "stage_output_submitted", "stage_run:"+sr.ID,
			map[string]any{"stage": sr.Stage, "iteration": sr.Iteration, "channel": "set_stage_output"}); err != nil {
			slog.Warn("channel-stage-output: recording the submission audit failed", "stageRun", sr.ID, "err", err)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
