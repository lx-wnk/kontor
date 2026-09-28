package plan

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// HandlerDeps holds the dependencies for the plan HTTP handler.
type HandlerDeps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	Advance   func(ctx context.Context, taskID string) error
	Requeue   func(ctx context.Context, taskID, prompt string) error
	Revoke    func(ctx context.Context, stageRunID string) error
}

// Handler handles /api/plan routes.
type Handler struct {
	deps HandlerDeps
}

// NewHandler creates a Handler with the given dependencies.
func NewHandler(deps HandlerDeps) *Handler {
	return &Handler{deps: deps}
}

// Mount registers the plan routes on r.
// All routes require JWT auth — mount inside a protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/api/plan/{taskId}/approve", h.approve)
	r.Post("/api/plan/{taskId}/reject", h.reject)
	r.Get("/api/plan/{taskId}/status", h.status)
}

// POST /api/plan/{taskId}/approve
func (h *Handler) approve(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	task, err := ApprovePlan(r.Context(), ApproveDeps{
		Turns:     h.deps.Turns,
		Tasks:     h.deps.Tasks,
		StageRuns: h.deps.StageRuns,
		Advance:   h.deps.Advance,
		Revoke:    h.deps.Revoke,
	}, taskID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tasks.ToTaskResponse(task))
}

// POST /api/plan/{taskId}/reject
func (h *Handler) reject(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	var body struct {
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Feedback) == "" {
		jsonError(w, "feedback is required", http.StatusBadRequest)
		return
	}

	if err := RejectPlan(r.Context(), RejectDeps{
		Turns:     h.deps.Turns,
		Tasks:     h.deps.Tasks,
		StageRuns: h.deps.StageRuns,
		Requeue:   h.deps.Requeue,
	}, taskID, body.Feedback); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// GET /api/plan/{taskId}/status
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	result, err := PlanStatus(r.Context(), StatusDeps{
		Turns:     h.deps.Turns,
		Tasks:     h.deps.Tasks,
		StageRuns: h.deps.StageRuns,
	}, taskID)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
