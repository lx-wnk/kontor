// Package refine implements the /api/refine routes for the refinement chat feature.
package refine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/api/tasks"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// Deps holds the dependencies for Handler.
type Deps struct {
	Turns     repo.RefinementTurnRepo
	Tasks     repo.TaskRepo
	StageRuns repo.StageRunRepo
	// Advance is called after refinement is confirmed to progress the task past
	// the concept stage. Injected from the composition root so this package has
	// no runtime dependency on the pipeline orchestrator.
	Advance func(ctx context.Context, taskID string) error
	// Revoke invalidates the concept stage run's MCP credentials once Confirm
	// marks it done. Nil disables revocation.
	Revoke  func(ctx context.Context, stageRunID string) error
	Spawner func(ctx context.Context, cfg refine.SpawnConfig, sp *ent.Spawner) (<-chan string, error)
	// ResolveSpawner returns the effective spawner row for the given task. If nil,
	// the handler falls back to passing nil to the Spawner function (which then
	// uses the legacy `claude -p` exec path).
	ResolveSpawner func(ctx context.Context, taskID string) (*ent.Spawner, services.SpawnerSource, error)
	// Runner owns detached refinement runs + status. When nil, NewHandler
	// constructs one from Turns + the default spawner.
	Runner *refine.Runner
}

// Handler handles /api/refine routes.
type Handler struct {
	deps Deps
}

// NewHandler creates a Handler with the given dependencies.
// If deps.Spawner is nil, refine.RunRefinementTurn is used.
// If deps.Runner is nil, a Runner is constructed from Turns + Spawner.
func NewHandler(deps Deps) *Handler {
	if deps.Spawner == nil {
		deps.Spawner = refine.RunRefinementTurn
	}
	if deps.Runner == nil {
		deps.Runner = refine.NewRunner(deps.Turns, deps.Spawner)
	}
	return &Handler{deps: deps}
}

// Mount registers the refinement routes on r.
// All routes require JWT auth — mount inside a protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/refine/{taskId}/turns", h.listTurns)
	r.Post("/api/refine/{taskId}/turn", h.submitTurn)
	r.Post("/api/refine/{taskId}/confirm", h.confirm)
	r.Post("/api/refine/{taskId}/concept", h.injectConcept)
	r.Get("/api/refine/{taskId}/status", h.status)
}

// TurnResponse is the JSON shape returned by GET /api/refine/{taskId}/turns.
type TurnResponse struct {
	ID        string   `json:"id"`
	Role      string   `json:"role"`
	Content   string   `json:"content"`
	Phase     *string  `json:"phase,omitempty"`
	Options   []string `json:"options,omitempty"`
	CreatedAt string   `json:"created_at"` // RFC3339
}

// GET /api/refine/{taskId}/turns
func (h *Handler) listTurns(w http.ResponseWriter, r *http.Request) {
	// Auth is enforced by RequireAuth middleware on the protected group; in
	// DASHBOARD_AUTH=none (bypass) mode no JWT payload is set, so the handler
	// must not re-gate on PayloadFromContext.
	taskID := chi.URLParam(r, "taskId")
	turns, err := h.deps.Turns.ListForTask(r.Context(), taskID, 0)
	if err != nil {
		jsonError(w, "failed to list turns", http.StatusInternalServerError)
		return
	}

	resp := make([]TurnResponse, 0, len(turns))
	for _, t := range turns {
		resp = append(resp, TurnResponse{
			ID:        t.ID,
			Role:      string(t.Role),
			Content:   t.Content,
			Phase:     t.Phase,
			Options:   t.Options,
			CreatedAt: t.CreatedAt.Format(time.RFC3339),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// GET /api/refine/{taskId}/status — current refine run status for the task.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")
	status, errMsg := h.deps.Runner.State(taskID)
	resp := map[string]string{"status": status}
	if errMsg != "" {
		resp["error"] = errMsg
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// POST /api/refine/{taskId}/turn — submit a user message and stream the assistant response via SSE.
func (h *Handler) submitTurn(w http.ResponseWriter, r *http.Request) {
	// Auth enforced by RequireAuth middleware (skipped in bypass mode) — see listTurns.
	taskID := chi.URLParam(r, "taskId")

	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		jsonError(w, "message is required", http.StatusBadRequest)
		return
	}

	// Fetch task for title + description + working directory.
	task, err := h.deps.Tasks.GetByID(r.Context(), taskID)
	if err != nil {
		jsonError(w, "task not found", http.StatusNotFound)
		return
	}

	// 1. Fetch prior history BEFORE storing the current user turn so it does not
	//    appear twice in the prompt (once in history, once as UserMessage).
	history, err := h.deps.Turns.ListForTaskNewest(r.Context(), taskID, 20)
	if err != nil {
		jsonError(w, "failed to fetch history", http.StatusInternalServerError)
		return
	}

	// 2. Build history, filtering out sentinel "confirmed" turns so they never
	//    appear in the model context.
	turns := make([]refine.Turn, 0, len(history))
	for _, t := range history {
		if t.Phase != nil && *t.Phase == "confirmed" {
			continue
		}
		turns = append(turns, refine.Turn{
			Role:    string(t.Role),
			Content: t.Content,
		})
	}

	workDir := task.Cwd
	if workDir == "" {
		workDir, _ = os.Getwd()
	}

	var desc string
	if task.Description != nil {
		desc = *task.Description
	}

	cfg := refine.SpawnConfig{
		TaskTitle:       task.Title,
		TaskDescription: desc,
		History:         turns,
		UserMessage:     body.Message,
		WorkDir:         workDir,
	}

	// Reject a second submit while a run is in flight (prevents duplicate turns).
	if h.deps.Runner.IsRunning(taskID) {
		jsonError(w, "a refinement run is already in progress", http.StatusConflict)
		return
	}

	// 3. Store the user turn only once we know we are starting a new run.
	if _, err := h.deps.Turns.Create(r.Context(), repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "user",
		Content: body.Message,
	}); err != nil {
		jsonError(w, "failed to store user turn", http.StatusInternalServerError)
		return
	}

	// 4. Resolve spawner BEFORE writing SSE headers so we can still send a JSON error.
	var resolvedSpawner *ent.Spawner
	if h.deps.ResolveSpawner != nil {
		sp, _, err := h.deps.ResolveSpawner(r.Context(), taskID)
		if err != nil {
			jsonError(w, "spawner resolution failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		resolvedSpawner = sp
	}

	// 5. Delegate to the runner — the goroutine persists the assistant turn even
	//    after the HTTP request context is cancelled (client disconnect).
	out, err := h.deps.Runner.Start(taskID, cfg, resolvedSpawner)
	if err != nil {
		jsonError(w, err.Error(), http.StatusConflict)
		return
	}

	// 6. Stream the runner's tee channel to the client via SSE.
	sse.WriteHeaders(w)
	flusher, canFlush := w.(http.Flusher)
	for {
		select {
		case line, open := <-out:
			if !open {
				return
			}
			// Split on embedded newlines to maintain valid SSE framing.
			for _, l := range strings.Split(line, "\n") {
				fmt.Fprintf(w, "data: %s\n", l)
			}
			fmt.Fprint(w, "\n")
			if canFlush {
				flusher.Flush()
			}
		case <-r.Context().Done():
			// Client disconnected — the runner keeps going and persists the turn.
			return
		}
	}
}

// POST /api/refine/{taskId}/confirm — mark refinement as confirmed and advance to backlog.
func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	// Auth enforced by RequireAuth middleware (skipped in bypass mode) — see listTurns.
	taskID := chi.URLParam(r, "taskId")

	task, err := Confirm(r.Context(), ConfirmDeps{
		Turns:     h.deps.Turns,
		Tasks:     h.deps.Tasks,
		StageRuns: h.deps.StageRuns,
		Advance:   h.deps.Advance,
		Revoke:    h.deps.Revoke,
	}, taskID)
	if err != nil {
		jsonError(w, "confirmed but could not fetch updated task", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(tasks.ToTaskResponse(task))
}

// ConceptBody is the JSON payload accepted by POST /api/refine/{taskId}/concept.
type ConceptBody struct {
	Spec         string   `json:"spec"`
	Plan         []string `json:"plan"`
	ToolRequests []string `json:"toolRequests"`
	RefinedTitle string   `json:"refinedTitle"`
	SourceBranch string   `json:"sourceBranch"`
	TargetBranch string   `json:"targetBranch"`
}

// POST /api/refine/{taskId}/concept — inject a finished concept without an agent round-trip.
func (h *Handler) injectConcept(w http.ResponseWriter, r *http.Request) {
	taskID := chi.URLParam(r, "taskId")

	var body ConceptBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Spec) == "" {
		jsonError(w, "spec is required", http.StatusBadRequest)
		return
	}

	raw := map[string]any{"spec": body.Spec}
	if len(body.Plan) > 0 {
		plans := make([]any, len(body.Plan))
		for i, p := range body.Plan {
			plans[i] = p
		}
		raw["plan"] = plans
	}
	if len(body.ToolRequests) > 0 {
		reqs := make([]any, len(body.ToolRequests))
		for i, p := range body.ToolRequests {
			reqs[i] = p
		}
		raw["toolRequests"] = reqs
	}

	c := refine.Concept{
		Raw:          raw,
		RefinedTitle: body.RefinedTitle,
		SourceBranch: body.SourceBranch,
		TargetBranch: body.TargetBranch,
	}

	if err := InjectConcept(r.Context(), InjectDeps{
		Turns:  h.deps.Turns,
		Runner: h.deps.Runner,
	}, taskID, c); err != nil {
		jsonError(w, "inject failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	status, _ := h.deps.Runner.State(taskID)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}

func jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
