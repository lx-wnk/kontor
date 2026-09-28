package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// taskDependencyResponse is the API response shape for one dependency edge.
// ent.TaskDependency stores only the two task ids and the two settings, so the
// titles and the upstream task's current stage are resolved from the referenced
// tasks — TaskDependenciesTab.vue and DependencyGraph.vue render all three, and
// dependsOnStage is what the required-stage badge is compared against.
type taskDependencyResponse struct {
	ID             string `json:"id"`
	TaskID         string `json:"taskId"`
	TaskTitle      string `json:"taskTitle"`
	DependsOnID    string `json:"dependsOnId"`
	DependsOnTitle string `json:"dependsOnTitle"`
	DependsOnStage string `json:"dependsOnStage"`
	RequiredStage  string `json:"requiredStage"`
	OnCancelAction string `json:"onCancelAction"`
}

// toDependencyResponses resolves both endpoints of every edge in one query via
// TaskRepo.ListByIDs. Eager-loading the edges in DependencyRepo instead would
// add the joins to pipeline.EvaluateTaskDeps and the orchestrator's downstream
// sweep, neither of which reads a title.
//
// A row whose referenced task no longer resolves answers with empty strings
// rather than failing the whole listing: the tab already falls back to the id.
func (h *Handler) toDependencyResponses(ctx context.Context, deps []*ent.TaskDependency) ([]taskDependencyResponse, error) {
	resp := make([]taskDependencyResponse, 0, len(deps))
	if len(deps) == 0 {
		return resp, nil
	}
	ids := make([]string, 0, len(deps)*2)
	for _, d := range deps {
		ids = append(ids, d.TaskID, d.DependsOnID)
	}
	tasks, err := h.taskRepo.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*ent.Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	for _, d := range deps {
		row := taskDependencyResponse{
			ID:             d.ID,
			TaskID:         d.TaskID,
			DependsOnID:    d.DependsOnID,
			RequiredStage:  d.RequiredStage,
			OnCancelAction: d.OnCancelAction,
		}
		if t, ok := byID[d.TaskID]; ok {
			row.TaskTitle = t.Title
		}
		if t, ok := byID[d.DependsOnID]; ok {
			row.DependsOnTitle = t.Title
			row.DependsOnStage = t.CurrentStage
		}
		resp = append(resp, row)
	}
	return resp, nil
}

func (h *Handler) listDependencies(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")
	deps, err := h.depRepo.ListUpstream(r.Context(), taskID)
	if err != nil {
		return fmt.Errorf("dependencies.list: %w", err)
	}
	resp, err := h.toDependencyResponses(r.Context(), deps)
	if err != nil {
		return fmt.Errorf("dependencies.list.resolve: %w", err)
	}
	return jsonReply(w, http.StatusOK, resp)
}

func (h *Handler) listDependents(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")
	deps, err := h.depRepo.ListDownstream(r.Context(), taskID)
	if err != nil {
		return fmt.Errorf("dependents.list: %w", err)
	}
	resp, err := h.toDependencyResponses(r.Context(), deps)
	if err != nil {
		return fmt.Errorf("dependents.list.resolve: %w", err)
	}
	return jsonReply(w, http.StatusOK, resp)
}

func (h *Handler) addDependency(w http.ResponseWriter, r *http.Request) error {
	taskID := chi.URLParam(r, "id")
	var body struct {
		DependsOnID    string `json:"dependsOnId"`
		RequiredStage  string `json:"requiredStage"`
		OnCancelAction string `json:"onCancelAction"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.DependsOnID == "" {
		return apierr.NewAppError(http.StatusBadRequest, "dependsOnId is required")
	}
	requiredStage := body.RequiredStage
	if requiredStage == "" {
		requiredStage = "done"
	}
	if requiredStage != "done" && requiredStage != "cancelled" {
		return apierr.NewAppError(http.StatusBadRequest, "requiredStage must be 'done' or 'cancelled'")
	}
	onCancelAction := body.OnCancelAction
	if onCancelAction == "" {
		onCancelAction = "on_hold"
	}
	if onCancelAction != "cancel" && onCancelAction != "start" && onCancelAction != "on_hold" {
		return apierr.NewAppError(http.StatusBadRequest, "onCancelAction must be 'cancel', 'start', or 'on_hold'")
	}
	dep, err := h.depRepo.Add(r.Context(), taskID, body.DependsOnID, requiredStage, onCancelAction)
	if err != nil {
		return fmt.Errorf("dependencies.add: %w", err)
	}
	resp, err := h.toDependencyResponses(r.Context(), []*ent.TaskDependency{dep})
	if err != nil {
		return fmt.Errorf("dependencies.add.resolve: %w", err)
	}
	return jsonReply(w, http.StatusCreated, resp[0])
}

func (h *Handler) removeDependency(w http.ResponseWriter, r *http.Request) error {
	depID := chi.URLParam(r, "depId")
	if err := h.depRepo.RemoveByID(r.Context(), depID); err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("dependencies.remove: %w", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Ensure depRepo is referenced — keeps the compiler happy if it would otherwise be unused.
var _ repo.DependencyRepo = (*struct{ repo.DependencyRepo })(nil)
