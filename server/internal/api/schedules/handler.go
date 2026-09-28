// Package schedules implements the REST surface for recurring task schedules:
// CRUD, a non-persisting /preview (cron + human echo + next 5 runs), and
// /{id}/run-now. Mounted in the JWT/same-origin group.
package schedules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/scheduler"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

const previewRunCount = 5

// Translator converts a natural-language phrase to a validated cron expression.
// Satisfied by *scheduler.NLCron.
type Translator interface {
	Translate(ctx context.Context, phrase string) (string, error)
}

// Runner fires a schedule immediately. Satisfied by *scheduler.Scheduler.
type Runner interface {
	RunNow(ctx context.Context, scheduleID string) (string, error)
}

// Handler serves /api/schedules.
type Handler struct {
	repo       repo.TaskScheduleRepo
	translator Translator
	runner     Runner
	apps       repo.MCPApplicationRepo
	tasks      repo.TaskRepo
	stageRuns  repo.StageRunRepo
	bypassAuth bool
}

// NewHandler builds the schedules handler. runner may be nil (run-now disabled).
// bypassAuth is the loopback single-user mode, in which the listing is not
// scoped to a user id because there is only one implicit user.
func NewHandler(r repo.TaskScheduleRepo, t Translator, runner Runner, apps repo.MCPApplicationRepo, tasks repo.TaskRepo, stageRuns repo.StageRunRepo, bypassAuth bool) *Handler {
	return &Handler{repo: r, translator: t, runner: runner, apps: apps, tasks: tasks, stageRuns: stageRuns, bypassAuth: bypassAuth}
}

// validateApplications checks that every id names an existing MCP application.
// nil ids leaves an existing schedule's applications untouched on update.
func (h *Handler) validateApplications(ctx context.Context, ids *[]string) error {
	if ids == nil {
		return nil
	}
	for _, id := range *ids {
		if h.apps == nil {
			return apierr.NewAppError(http.StatusBadRequest, "applications: MCP applications are not available")
		}
		if _, err := h.apps.GetByResourceID(ctx, id); err != nil {
			return apierr.NewAppError(http.StatusBadRequest, "applications: unknown application "+id)
		}
	}
	return nil
}

// Mount registers the schedule routes on r (already inside the JWT group).
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/schedules", apierr.ErrorMiddleware(h.list))
	r.Post("/api/schedules", apierr.ErrorMiddleware(h.create))
	r.Post("/api/schedules/preview", apierr.ErrorMiddleware(h.preview))
	r.Get("/api/schedules/{id}", apierr.ErrorMiddleware(h.get))
	r.Patch("/api/schedules/{id}", apierr.ErrorMiddleware(h.update))
	r.Delete("/api/schedules/{id}", apierr.ErrorMiddleware(h.delete))
	r.Post("/api/schedules/{id}/run-now", apierr.ErrorMiddleware(h.runNow))
	r.Get("/api/schedules/{id}/runs", apierr.ErrorMiddleware(h.listRuns))
}

func jsonReply(w http.ResponseWriter, status int, v any) error {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	return json.NewEncoder(w).Encode(v)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	payload, _ := auth.PayloadFromContext(r.Context())
	rows, err := h.repo.ListForUser(r.Context(), payload.Sub, h.bypassAuth)
	if err != nil {
		return fmt.Errorf("schedules.list: %w", err)
	}
	views := make([]scheduleView, len(rows))
	for i, s := range rows {
		views[i] = toView(s)
	}
	return jsonReply(w, http.StatusOK, views)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	s, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("schedules.get: %w", err)
	}
	return jsonReply(w, http.StatusOK, toView(s))
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var body scheduleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Name == "" {
		return apierr.NewAppError(http.StatusBadRequest, "name is required")
	}
	if body.Title == "" {
		return apierr.NewAppError(http.StatusBadRequest, "title is required")
	}
	if body.Cwd == "" {
		return apierr.NewAppError(http.StatusBadRequest, "cwd is required")
	}
	if !validation.IsValidSlug(body.SlugPrefix) {
		return apierr.NewAppError(http.StatusBadRequest, "slugPrefix: "+validation.SlugPatternMessage)
	}
	if err := h.validateApplications(r.Context(), body.Applications); err != nil {
		return err
	}
	runMode := body.RunMode
	if runMode == "" {
		runMode = repo.RunModeJob
	}
	if err := scheduler.CheckRunMode(r.Context(), runMode, body.Cwd); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	cronExpr, err := h.resolveCron(r.Context(), body.NLText, body.CronExpr)
	if err != nil {
		return err
	}

	payload, _ := auth.PayloadFromContext(r.Context())
	userID := payload.Sub
	tz := body.Timezone
	if tz == "" {
		tz = "UTC"
	}
	next := nextRunOrNil(cronExpr, tz)

	in := repo.CreateTaskScheduleInput{
		Name:                body.Name,
		Enabled:             body.Enabled,
		CronExpr:            cronExpr,
		Timezone:            tz,
		Catchup:             body.Catchup,
		SlugPrefix:          body.SlugPrefix,
		Title:               body.Title,
		Description:         body.Description,
		Cwd:                 body.Cwd,
		SourceBranch:        body.SourceBranch,
		TargetBranch:        body.TargetBranch,
		Priority:            body.Priority,
		MaxIterations:       body.MaxIterations,
		TokenBudget:         body.TokenBudget,
		CostBudgetCents:     body.CostBudgetCents,
		StageTimeoutSeconds: body.StageTimeoutSeconds,
		SilverBullet:        body.SilverBullet,
		ProjectID:           strPtrOrNil(body.ProjectID),
		SpawnerID:           strPtrOrNil(body.SpawnerID),
		PermissionTemplate:  body.PermissionTemplate,
		UserID:              &userID,
		NextRunAt:           next,
		RunMode:             runMode,
	}
	if body.Applications != nil {
		in.Applications = *body.Applications
	}
	if body.NLText != "" {
		nl := body.NLText
		in.NLText = &nl
	}
	s, err := h.repo.Create(r.Context(), in)
	if err != nil {
		return fmt.Errorf("schedules.create: %w", err)
	}
	return jsonReply(w, http.StatusCreated, toView(s))
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		return apierr.ErrNotFound
	}
	var body scheduleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	in := repo.UpdateTaskScheduleInput{}
	if body.RunMode != "" || body.Cwd != "" {
		effectiveMode := body.RunMode
		if effectiveMode == "" {
			effectiveMode = existing.RunMode
		}
		effectiveCwd := body.Cwd
		if effectiveCwd == "" {
			effectiveCwd = existing.Cwd
		}
		if err := scheduler.CheckRunMode(r.Context(), effectiveMode, effectiveCwd); err != nil {
			return apierr.NewAppError(http.StatusBadRequest, err.Error())
		}
		if body.RunMode != "" {
			in.RunMode = &body.RunMode
		}
	}
	if body.Name != "" {
		in.Name = &body.Name
	}
	if body.Enabled != nil {
		in.Enabled = body.Enabled
	}
	// Re-translate when the phrase or a raw cron is provided; recompute next.
	if body.NLText != "" || body.CronExpr != "" {
		cronExpr, err := h.resolveCron(r.Context(), body.NLText, body.CronExpr)
		if err != nil {
			return err
		}
		in.CronExpr = &cronExpr
		tz := body.Timezone
		if tz == "" {
			tz = "UTC"
		}
		if next := nextRunOrNil(cronExpr, tz); next != nil {
			in.NextRunAt = next
		}
		if body.NLText != "" {
			in.NLText = &body.NLText
		}
	}
	if body.Timezone != "" {
		in.Timezone = &body.Timezone
	}
	if body.Catchup != "" {
		in.Catchup = &body.Catchup
	}
	if body.Title != "" {
		in.Title = &body.Title
	}
	if body.Cwd != "" {
		in.Cwd = &body.Cwd
	}
	if body.SlugPrefix != "" {
		if !validation.IsValidSlug(body.SlugPrefix) {
			return apierr.NewAppError(http.StatusBadRequest, "slugPrefix: "+validation.SlugPatternMessage)
		}
		in.SlugPrefix = &body.SlugPrefix
	}
	if err := h.validateApplications(r.Context(), body.Applications); err != nil {
		return err
	}
	in.Applications = body.Applications
	if body.Priority != "" {
		in.Priority = &body.Priority
	}
	in.Description = body.Description
	in.SourceBranch = body.SourceBranch
	in.TargetBranch = body.TargetBranch
	in.PermissionTemplate = body.PermissionTemplate
	if body.MaxIterations > 0 {
		in.MaxIterations = &body.MaxIterations
	}
	s, err := h.repo.Update(r.Context(), id, in)
	if err != nil {
		return fmt.Errorf("schedules.update: %w", err)
	}
	return jsonReply(w, http.StatusOK, toView(s))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("schedules.delete: %w", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// preview translates a phrase (or validates a raw cron) and returns the cron
// expression, a human echo, and the next 5 fire times — without persisting.
func (h *Handler) preview(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		NLText   string `json:"nlText"`
		CronExpr string `json:"cronExpr"`
		Timezone string `json:"timezone"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	cronExpr, err := h.resolveCron(r.Context(), body.NLText, body.CronExpr)
	if err != nil {
		return err
	}
	tz := body.Timezone
	if tz == "" {
		tz = "UTC"
	}
	loc, lerr := time.LoadLocation(tz)
	if lerr != nil {
		loc = time.UTC
		tz = "UTC"
	}
	runs, err := scheduler.NextRuns(cronExpr, time.Now().In(loc), previewRunCount)
	if err != nil {
		return apierr.NewAppError(http.StatusUnprocessableEntity, err.Error())
	}
	out := make([]string, len(runs))
	for i, t := range runs {
		out[i] = t.Format(time.RFC3339)
	}
	return jsonReply(w, http.StatusOK, map[string]any{
		"cronExpr": cronExpr,
		"human":    describeCron(cronExpr),
		"timezone": tz,
		"nextRuns": out,
	})
}

func (h *Handler) runNow(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if h.runner == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "scheduler not available")
	}
	if _, err := h.repo.GetByID(r.Context(), id); err != nil {
		return apierr.ErrNotFound
	}
	taskID, err := h.runner.RunNow(r.Context(), id)
	if err != nil {
		return fmt.Errorf("schedules.runNow: %w", err)
	}
	return jsonReply(w, http.StatusOK, map[string]any{"taskId": taskID})
}

// resolveCron returns a validated cron expression from either a raw cron string
// or a natural-language phrase. A raw cronExpr takes precedence. An unparseable
// phrase yields 422.
func (h *Handler) resolveCron(ctx context.Context, nlText, cronExpr string) (string, error) {
	if cronExpr != "" {
		if err := scheduler.Validate(cronExpr); err != nil {
			return "", apierr.NewAppError(http.StatusUnprocessableEntity, err.Error())
		}
		return cronExpr, nil
	}
	if nlText == "" {
		return "", apierr.NewAppError(http.StatusBadRequest, "nlText or cronExpr is required")
	}
	if h.translator == nil {
		return "", apierr.NewAppError(http.StatusServiceUnavailable, "translator not available")
	}
	expr, err := h.translator.Translate(ctx, nlText)
	if err != nil {
		if errors.Is(err, scheduler.ErrUnparseable) {
			return "", apierr.NewAppError(http.StatusUnprocessableEntity, "could not translate phrase to a schedule: "+nlText)
		}
		return "", fmt.Errorf("schedules.translate: %w", err)
	}
	return expr, nil
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nextRunOrNil(cronExpr, tz string) *time.Time {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	runs, err := scheduler.NextRuns(cronExpr, time.Now().In(loc), 1)
	if err != nil || len(runs) == 0 {
		return nil
	}
	return &runs[0]
}
