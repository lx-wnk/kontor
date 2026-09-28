package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/claudemodel"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// validStageModelKeys lists the agent-driven stages that support per-stage model and spawner overrides.
var validStageModelKeys = [4]string{"implementation", "self_review", "finalization", "plan_review"}

const (
	stageModelKeyPrefix   = "stageModel."
	stageSpawnerKeyPrefix = "stageSpawner."
)

// pipelineConfigResponse is the canonical GET /api/pipeline/config payload.
type pipelineConfigResponse struct {
	MaxParallelOrchestrators int               `json:"maxParallelOrchestrators"`
	StageTimeoutSeconds      int               `json:"stageTimeoutSeconds"`
	MaxAutoRetries           int               `json:"maxAutoRetries"`
	RetryBackoffSeconds      int               `json:"retryBackoffSeconds"`
	ExtraSafeBashCommands    string            `json:"extraSafeBashCommands"`
	StageModels              map[string]string `json:"stageModels"`
	StageSpawners            map[string]string `json:"stageSpawners"`
	PlanMode                 bool              `json:"planMode"`
}

// projectPipelineConfigResponse is the per-project GET /api/projects/{id}/pipeline-config payload.
// Empty string for a stage means "inherit global".
type projectPipelineConfigResponse struct {
	StageModels   map[string]string `json:"stageModels"`
	StageSpawners map[string]string `json:"stageSpawners"`
	PlanMode      *bool             `json:"planMode"`
}

// readScopedStageSpawners returns a stage→spawnerID map for the given scope.
// Empty string value means no override for that stage.
func (h *Handler) readScopedStageSpawners(ctx context.Context, projectID *string) map[string]string {
	out := make(map[string]string, len(validStageModelKeys))
	for _, stage := range validStageModelKeys {
		out[stage] = h.cfgRepo.GetStringForScope(ctx, projectID, stageSpawnerKeyPrefix+stage, "")
	}
	return out
}

// readScopedStageModels returns a stage→modelID map for the given scope.
// Empty string value means no override for that stage.
func (h *Handler) readScopedStageModels(ctx context.Context, projectID *string) map[string]string {
	out := make(map[string]string, len(validStageModelKeys))
	for _, stage := range validStageModelKeys {
		out[stage] = h.cfgRepo.GetStringForScope(ctx, projectID, stageModelKeyPrefix+stage, "")
	}
	return out
}

// writeScopedStageSpawners applies stageSpawners map writes for the given scope.
// empty value → delete (inherit), non-empty → validate existence then set.
func (h *Handler) writeScopedStageSpawners(ctx context.Context, projectID *string, stageSpawners map[string]string) error {
	for _, stage := range validStageModelKeys {
		spawnerID, ok := stageSpawners[stage]
		if !ok {
			continue
		}
		if spawnerID == "" {
			if err := h.cfgRepo.DeleteScoped(ctx, projectID, stageSpawnerKeyPrefix+stage); err != nil {
				return fmt.Errorf("stageSpawner.%s: %w", stage, err)
			}
			continue
		}
		if _, err := h.spawnerRepo.GetByID(ctx, spawnerID); err != nil {
			if ent.IsNotFound(err) {
				return apierr.NewAppError(http.StatusBadRequest, fmt.Sprintf("stageSpawner.%s: unknown spawner %q", stage, spawnerID))
			}
			return fmt.Errorf("stageSpawner.%s: lookup: %w", stage, err)
		}
		if err := h.cfgRepo.SetScoped(ctx, projectID, stageSpawnerKeyPrefix+stage, spawnerID); err != nil {
			return fmt.Errorf("stageSpawner.%s: %w", stage, err)
		}
	}
	return nil
}

// writeScopedStageModels applies stageModels map writes for the given scope.
// empty value → delete (inherit), non-empty → validate then set.
func (h *Handler) writeScopedStageModels(ctx context.Context, projectID *string, stageModels map[string]string) error {
	for _, stage := range validStageModelKeys {
		model, ok := stageModels[stage]
		if !ok {
			continue
		}
		if model == "" {
			if err := h.cfgRepo.DeleteScoped(ctx, projectID, stageModelKeyPrefix+stage); err != nil {
				return fmt.Errorf("stageModel.%s: %w", stage, err)
			}
			continue
		}
		if !claudemodel.IsKnown(model) {
			return apierr.NewAppError(http.StatusBadRequest, fmt.Sprintf("stageModel.%s: unknown model %q", stage, model))
		}
		if err := h.cfgRepo.SetScoped(ctx, projectID, stageModelKeyPrefix+stage, model); err != nil {
			return fmt.Errorf("stageModel.%s: %w", stage, err)
		}
	}
	return nil
}

func (h *Handler) getPipelineConfig(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	maxParallel := int(h.cfgRepo.GetNumber(ctx, "maxParallelOrchestrators", 3))
	stageTimeout := int(h.cfgRepo.GetNumber(ctx, "stageTimeoutSeconds", db.DefaultStageTimeoutSeconds))
	maxAutoRetries := int(h.cfgRepo.GetNumber(ctx, "maxAutoRetries", 3))
	retryBackoffSeconds := int(h.cfgRepo.GetNumber(ctx, "retryBackoffSeconds", 60))
	extraSafeBashCommands := h.cfgRepo.GetString(ctx, "extraSafeBashCommands", "")
	planMode := h.cfgRepo.GetString(ctx, "planMode", "") == "true"

	stageModels := make(map[string]string, len(validStageModelKeys))
	for _, stage := range validStageModelKeys {
		stageModels[stage] = h.orchestrator.EffectiveStageModel(ctx, stage)
	}

	return jsonReply(w, http.StatusOK, pipelineConfigResponse{
		MaxParallelOrchestrators: maxParallel,
		StageTimeoutSeconds:      stageTimeout,
		MaxAutoRetries:           maxAutoRetries,
		RetryBackoffSeconds:      retryBackoffSeconds,
		ExtraSafeBashCommands:    extraSafeBashCommands,
		StageModels:              stageModels,
		StageSpawners:            h.readScopedStageSpawners(ctx, nil),
		PlanMode:                 planMode,
	})
}

func (h *Handler) putPipelineConfig(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		MaxParallelOrchestrators *int              `json:"maxParallelOrchestrators"`
		StageTimeoutSeconds      *int              `json:"stageTimeoutSeconds"`
		MaxAutoRetries           *int              `json:"maxAutoRetries"`
		RetryBackoffSeconds      *int              `json:"retryBackoffSeconds"`
		ExtraSafeBashCommands    *string           `json:"extraSafeBashCommands"`
		StageModels              map[string]string `json:"stageModels"`
		StageSpawners            map[string]string `json:"stageSpawners"`
		PlanMode                 *bool             `json:"planMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("pipeline_config.put: decode: %w", err)
	}
	ctx := r.Context()
	if body.MaxParallelOrchestrators != nil {
		if *body.MaxParallelOrchestrators < 1 {
			return fmt.Errorf("pipeline_config.put: maxParallelOrchestrators must be >= 1")
		}
		if err := h.cfgRepo.Set(ctx, "maxParallelOrchestrators", strconv.Itoa(*body.MaxParallelOrchestrators)); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	if body.StageTimeoutSeconds != nil {
		if *body.StageTimeoutSeconds < 0 {
			return fmt.Errorf("pipeline_config.put: stageTimeoutSeconds must be >= 0")
		}
		if err := h.cfgRepo.Set(ctx, "stageTimeoutSeconds", strconv.Itoa(*body.StageTimeoutSeconds)); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	if body.MaxAutoRetries != nil {
		if *body.MaxAutoRetries < 0 {
			return fmt.Errorf("pipeline_config.put: maxAutoRetries must be >= 0")
		}
		if err := h.cfgRepo.Set(ctx, "maxAutoRetries", strconv.Itoa(*body.MaxAutoRetries)); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	if body.RetryBackoffSeconds != nil {
		if *body.RetryBackoffSeconds < 0 {
			return fmt.Errorf("pipeline_config.put: retryBackoffSeconds must be >= 0")
		}
		if err := h.cfgRepo.Set(ctx, "retryBackoffSeconds", strconv.Itoa(*body.RetryBackoffSeconds)); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	if body.ExtraSafeBashCommands != nil {
		if err := h.cfgRepo.Set(ctx, "extraSafeBashCommands", *body.ExtraSafeBashCommands); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	// ok && model == "" → delete row (revert to coded default)
	// ok && model != "" → validate then set
	// !ok → leave untouched
	if body.StageModels != nil {
		if err := h.writeScopedStageModels(ctx, nil, body.StageModels); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	if body.StageSpawners != nil {
		if err := h.writeScopedStageSpawners(ctx, nil, body.StageSpawners); err != nil {
			return fmt.Errorf("pipeline_config.put: %w", err)
		}
	}
	if body.PlanMode != nil {
		v := "false"
		if *body.PlanMode {
			v = "true"
		}
		if err := h.cfgRepo.Set(ctx, "planMode", v); err != nil {
			return fmt.Errorf("pipeline_config.put: planMode: %w", err)
		}
	}
	h.orchestrator.InvalidateConfigCache()
	raw := h.cfgRepo.GetString(ctx, "extraSafeBashCommands", "")
	permissions.SetExtraSafeBashCommands(permissions.ParseExtraSafeBashCommands(raw))
	return h.getPipelineConfig(w, r)
}

// readProjectPlanMode returns the project-scoped planMode as *bool (nil when no override is set).
func readProjectPlanMode(h *Handler, ctx context.Context, projectID *string) *bool {
	raw := h.cfgRepo.GetStringForScope(ctx, projectID, "planMode", "")
	if raw == "" {
		return nil
	}
	v := raw == "true"
	return &v
}

// resolveCreatePlanMode resolves the effective planMode for a task being created:
// explicit request value > project-scoped default > global default > nil (ent default false).
func resolveCreatePlanMode(h *Handler, ctx context.Context, projectID *string, explicit *bool) *bool {
	if explicit != nil {
		return explicit
	}
	if pm := readProjectPlanMode(h, ctx, projectID); pm != nil {
		return pm
	}
	if h.cfgRepo.GetString(ctx, "planMode", "") == "true" {
		v := true
		return &v
	}
	return nil
}

func (h *Handler) getProjectPipelineConfig(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	if _, err := h.projectRepo.GetByID(ctx, id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("project_pipeline_config.get: %w", err)
	}
	planMode := readProjectPlanMode(h, ctx, &id)
	return jsonReply(w, http.StatusOK, projectPipelineConfigResponse{
		StageModels:   h.readScopedStageModels(ctx, &id),
		StageSpawners: h.readScopedStageSpawners(ctx, &id),
		PlanMode:      planMode,
	})
}

func (h *Handler) putProjectPipelineConfig(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	ctx := r.Context()
	if _, err := h.projectRepo.GetByID(ctx, id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("project_pipeline_config.put: %w", err)
	}
	var body struct {
		StageModels   map[string]string `json:"stageModels"`
		StageSpawners map[string]string `json:"stageSpawners"`
		PlanMode      *bool             `json:"planMode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("project_pipeline_config.put: decode: %w", err)
	}
	if body.StageModels != nil {
		if err := h.writeScopedStageModels(ctx, &id, body.StageModels); err != nil {
			return fmt.Errorf("project_pipeline_config.put: %w", err)
		}
	}
	if body.StageSpawners != nil {
		if err := h.writeScopedStageSpawners(ctx, &id, body.StageSpawners); err != nil {
			return fmt.Errorf("project_pipeline_config.put: %w", err)
		}
	}
	if body.PlanMode != nil {
		v := "false"
		if *body.PlanMode {
			v = "true"
		}
		if err := h.cfgRepo.SetScoped(ctx, &id, "planMode", v); err != nil {
			return fmt.Errorf("project_pipeline_config.put: planMode: %w", err)
		}
	}
	planMode := readProjectPlanMode(h, ctx, &id)
	return jsonReply(w, http.StatusOK, projectPipelineConfigResponse{
		StageModels:   h.readScopedStageModels(ctx, &id),
		StageSpawners: h.readScopedStageSpawners(ctx, &id),
		PlanMode:      planMode,
	})
}

func (h *Handler) getPipelineRecommendation(w http.ResponseWriter, r *http.Request) error {
	// Lightweight heuristic: cap parallel tasks at max(1, numCPU/2).
	cores := runtime.NumCPU()
	recommended := cores / 2
	if recommended < 1 {
		recommended = 1
	}
	return jsonReply(w, http.StatusOK, map[string]int{"maxParallelOrchestrators": recommended})
}
