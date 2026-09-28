// Package prompttemplates implements CRUD endpoints for reusable prompt templates
// at /api/prompt-templates.
package prompttemplates

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler handles /api/prompt-templates endpoints.
type Handler struct{ repo repo.PromptTemplateRepo }

// NewHandler creates a Handler backed by the given repo.
func NewHandler(r repo.PromptTemplateRepo) *Handler { return &Handler{repo: r} }

// Mount registers all prompt-template routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/prompt-templates", apierr.ErrorMiddleware(h.list))
	r.Post("/api/prompt-templates", apierr.ErrorMiddleware(h.create))
	r.Delete("/api/prompt-templates/{id}", apierr.ErrorMiddleware(h.delete))
}

type templateView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	CreatedAt string `json:"createdAt"`
}

func toView(t *ent.PromptTemplate) templateView {
	return templateView{ID: t.ID, Name: t.Name, Body: t.Body, CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	tpls, err := h.repo.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]templateView, len(tpls))
	for i, t := range tpls {
		out[i] = toView(t)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createBody struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in createBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if in.Name == "" {
		return apierr.NewAppError(http.StatusBadRequest, "name is required")
	}
	if in.Body == "" {
		return apierr.NewAppError(http.StatusBadRequest, "body is required")
	}
	tpl, err := h.repo.Create(r.Context(), in.Name, in.Body)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusCreated, toView(tpl))
	return nil
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "template not found")
		}
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
