// Package systemprompts implements CRUD endpoints for custom system prompts
// at /api/settings/system-prompts.
package systemprompts

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler handles the /api/settings/system-prompts endpoints.
type Handler struct {
	repo repo.SystemPromptRepo
}

// NewHandler creates a Handler backed by the given repo.
func NewHandler(r repo.SystemPromptRepo) *Handler {
	return &Handler{repo: r}
}

// systemPromptResponse is the API response shape for one custom system prompt.
// ent.SystemPrompt's tags carry omitempty, which drops priority 0 — the value
// the create form submits by default — so the settings table rendered a blank
// cell and the edit form re-seeded itself from undefined.
type systemPromptResponse struct {
	ID        string    `json:"id"`
	Scope     string    `json:"scope"`
	Stage     *string   `json:"stage"`
	Content   string    `json:"content"`
	Priority  int       `json:"priority"`
	CreatedBy *string   `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func toSystemPromptResponse(p *ent.SystemPrompt) systemPromptResponse {
	return systemPromptResponse{
		ID:        p.ID,
		Scope:     p.Scope,
		Stage:     p.Stage,
		Content:   p.Content,
		Priority:  p.Priority,
		CreatedBy: p.CreatedBy,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}
}

func toSystemPromptResponses(prompts []*ent.SystemPrompt) []systemPromptResponse {
	resp := make([]systemPromptResponse, len(prompts))
	for i, p := range prompts {
		resp[i] = toSystemPromptResponse(p)
	}
	return resp
}

// Mount registers all system-prompt routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/settings/system-prompts", apierr.ErrorMiddleware(h.list))
	r.Post("/api/settings/system-prompts", apierr.ErrorMiddleware(h.create))
	r.Put("/api/settings/system-prompts/{id}", apierr.ErrorMiddleware(h.update))
	r.Delete("/api/settings/system-prompts/{id}", apierr.ErrorMiddleware(h.delete))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	prompts, err := h.repo.List(r.Context())
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, toSystemPromptResponses(prompts))
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in repo.CreateSystemPromptInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if in.Content == "" {
		return apierr.NewAppError(http.StatusBadRequest, "content is required")
	}
	// Reject non-global scopes: ListForStage hardcodes scope='global', so any
	// other scope would create a prompt that is never applied.
	if in.Scope != "" && in.Scope != "global" {
		return apierr.NewAppError(http.StatusBadRequest, "only scope 'global' is currently supported")
	}
	if payload, ok := auth.PayloadFromContext(r.Context()); ok {
		login := payload.Login
		in.CreatedBy = &login
	} else {
		in.CreatedBy = nil
	}
	prompt, err := h.repo.Create(r.Context(), in)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusCreated, toSystemPromptResponse(prompt))
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	var in repo.UpdateSystemPromptInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if in.Content != nil && *in.Content == "" {
		return apierr.NewAppError(http.StatusBadRequest, "content is required")
	}
	prompt, err := h.repo.Update(r.Context(), id, in)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, toSystemPromptResponse(prompt))
	return nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
