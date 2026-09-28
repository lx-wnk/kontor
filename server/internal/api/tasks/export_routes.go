package tasks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func (h *Handler) exportTasks(w http.ResponseWriter, r *http.Request) error {
	payload, _ := auth.PayloadFromContext(r.Context())
	tasks, err := h.taskRepo.ListForUser(r.Context(), payload.Sub, h.bypassAuth)
	if err != nil {
		return fmt.Errorf("export: list tasks: %w", err)
	}

	// Collect task IDs and bulk-load all stage_runs in a single query,
	// eliminating the N+1 pattern that called ListForTask once per task.
	taskIDs := make([]string, len(tasks))
	for i, t := range tasks {
		taskIDs[i] = t.ID
	}
	runsByTask, err := h.srRepo.ListStageRunsByTaskIDs(r.Context(), taskIDs)
	if err != nil {
		return fmt.Errorf("export: bulk list stage runs: %w", err)
	}

	format := r.URL.Query().Get("format")
	if format == "csv" {
		header := "id,slug,title,currentStage,priority,createdAt,totalCostCents,totalTokens"
		rows := make([]string, 0, len(tasks))
		for _, t := range tasks {
			totalCost, totalTokens := 0, 0
			for _, sr := range runsByTask[t.ID] {
				totalCost += sr.CostCents
				totalTokens += sr.TokensUsed
			}
			title := strings.ReplaceAll(t.Title, `"`, `""`)
			createdAt := t.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
			rows = append(rows, fmt.Sprintf(`%s,%s,"%s",%s,%s,%s,%d,%d`,
				t.ID, t.Slug, title, t.CurrentStage, t.Priority, createdAt, totalCost, totalTokens))
		}
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", `attachment; filename="tasks.csv"`)
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, header)
		for _, row := range rows {
			fmt.Fprintln(w, row)
		}
		return nil
	}

	// JSON export with stage runs embedded. TaskResponse is embedded anonymously
	// so its fields flatten into the row — the same shape the API answers with,
	// rather than the entity's storage column names, because this file outlives
	// the session that downloaded it.
	type taskExportRow struct {
		TaskResponse
		StageRuns []stageRunResponse `json:"stageRuns"`
	}
	result := make([]taskExportRow, 0, len(tasks))
	for _, t := range tasks {
		result = append(result, taskExportRow{
			TaskResponse: ToTaskResponse(t),
			StageRuns:    toStageRunResponses(runsByTask[t.ID]),
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="tasks.json"`)
	w.WriteHeader(http.StatusOK)
	return json.NewEncoder(w).Encode(result)
}

// listFeedback returns an empty array until the feedback ent schema is added.
func (h *Handler) listFeedback(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := h.taskRepo.GetByID(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("feedback.list: get task: %w", err)
	}
	return jsonReply(w, http.StatusOK, []any{})
}
