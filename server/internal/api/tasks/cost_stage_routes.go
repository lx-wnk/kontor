package tasks

import (
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

type costBreakdownRow struct {
	ID         string  `json:"id"`
	Stage      string  `json:"stage"`
	Iteration  int     `json:"iteration"`
	CostCents  int     `json:"costCents"`
	TokensUsed int     `json:"tokensUsed"`
	StartedAt  *string `json:"startedAt"`
	EndedAt    *string `json:"endedAt"`
}

func (h *Handler) getCostBreakdown(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	runs, err := h.srRepo.ListForTask(r.Context(), id)
	if err != nil {
		return fmt.Errorf("cost_breakdown: %w", err)
	}
	rows := make([]costBreakdownRow, 0, len(runs))
	for _, sr := range runs {
		if sr.Status != "done" && sr.Status != "failed" {
			continue
		}
		row := costBreakdownRow{
			ID:         sr.ID,
			Stage:      sr.Stage,
			Iteration:  sr.Iteration,
			CostCents:  sr.CostCents,
			TokensUsed: sr.TokensUsed,
		}
		if sr.StartedAt != nil {
			s := sr.StartedAt.Format("2006-01-02T15:04:05Z07:00")
			row.StartedAt = &s
		}
		if sr.EndedAt != nil {
			e := sr.EndedAt.Format("2006-01-02T15:04:05Z07:00")
			row.EndedAt = &e
		}
		rows = append(rows, row)
	}
	return jsonReply(w, http.StatusOK, rows)
}

func (h *Handler) getStageRunAgentOutput(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")
	runID := chi.URLParam(r, "runId")

	sr, err := h.srRepo.GetByID(r.Context(), runID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("stage_run_output: get run: %w", err)
	}
	if sr.TaskID != taskID {
		return apierr.ErrNotFound
	}

	// Resolve session ID: prefer the one stored on the run; fall back to newest-by-mtime.
	task, err := h.taskRepo.GetByID(r.Context(), taskID)
	if err != nil {
		return fmt.Errorf("stage_run_output: get task: %w", err)
	}

	sessionID := ""
	if sr.SessionID != nil {
		sessionID = *sr.SessionID
	} else {
		sessionID, _ = pipeline.FindNewestSessionID(task.Cwd, "")
	}

	if sessionID == "" {
		return jsonReply(w, http.StatusOK, map[string]any{"rawText": "", "output": nil})
	}

	result, err := pipeline.ReadLastStageJsonOutput(task.Cwd, sessionID)
	if err != nil {
		return fmt.Errorf("stage_run_output: read: %w", err)
	}
	return jsonReply(w, http.StatusOK, map[string]any{
		"rawText": result.RawText,
		"output":  result.Output,
	})
}
