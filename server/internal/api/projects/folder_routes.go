package projects

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// ListFolders returns all folders for a project, ordered (default first, then by label).
// GET /api/projects/{id}/folders
func (h *Handler) ListFolders(w http.ResponseWriter, r *http.Request) error {
	projectID := chi.URLParam(r, "id")
	if _, err := h.projects.GetByID(r.Context(), projectID); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		return err
	}
	folders, err := h.folders.ListByProject(r.Context(), projectID)
	if err != nil {
		return err
	}
	out := make([]folderView, len(folders))
	for i, f := range folders {
		out[i] = toFolderView(f, projectID)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

// SuggestFolders is a placeholder that currently returns the same order as ListFolders.
// In the future this will rank by recency of task.cwd matches.
// GET /api/projects/{id}/folders/suggest
func (h *Handler) SuggestFolders(w http.ResponseWriter, r *http.Request) error {
	projectID := chi.URLParam(r, "id")
	if _, err := h.projects.GetByID(r.Context(), projectID); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		return err
	}
	folders, err := h.folders.Suggest(r.Context(), projectID)
	if err != nil {
		return err
	}
	out := make([]folderView, len(folders))
	for i, f := range folders {
		out[i] = toFolderView(f, projectID)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createFolderBody struct {
	Path      string  `json:"path"`
	Label     *string `json:"label"`
	IsDefault bool    `json:"isDefault"`
}

// CreateFolder creates a new folder under a project.
// POST /api/projects/{id}/folders
func (h *Handler) CreateFolder(w http.ResponseWriter, r *http.Request) error {
	projectID := chi.URLParam(r, "id")
	if _, err := h.projects.GetByID(r.Context(), projectID); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "project not found")
		}
		return err
	}
	var body createFolderBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if !ValidateAbsolutePath(body.Path) {
		return apierr.NewAppError(http.StatusBadRequest, "path must be absolute and contain no '..' segment")
	}
	f, err := h.folders.Create(r.Context(), projectID, body.Path, body.Label, body.IsDefault)
	if err != nil {
		return err
	}
	h.emitProjectUpdated(r, projectID)
	apierr.WriteJSON(w, http.StatusCreated, toFolderView(f, projectID))
	return nil
}

type updateFolderBody struct {
	Path      *string         `json:"path"`
	Label     json.RawMessage `json:"label"`
	IsDefault *bool           `json:"isDefault"`
}

// UpdateFolder updates a folder. JSON `null` on label clears it.
// PATCH /api/projects/{id}/folders/{folderId}
func (h *Handler) UpdateFolder(w http.ResponseWriter, r *http.Request) error {
	projectID := chi.URLParam(r, "id")
	folderID := chi.URLParam(r, "folderId")

	// Verify folder belongs to project.
	existing, err := h.folders.GetByID(r.Context(), folderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "folder not found")
		}
		return err
	}
	owner, err := existing.QueryProject().Only(r.Context())
	if err != nil {
		return err
	}
	if owner.ID != projectID {
		return apierr.NewAppError(http.StatusNotFound, "folder not found")
	}

	var body updateFolderBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Path != nil && !ValidateAbsolutePath(*body.Path) {
		return apierr.NewAppError(http.StatusBadRequest, "path must be absolute and contain no '..' segment")
	}
	label, clearLabel, err := parseNullableString(body.Label)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "label must be a string or null")
	}

	f, err := h.folders.Update(r.Context(), folderID, body.Path, label, clearLabel, body.IsDefault)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "folder not found")
		}
		return err
	}
	h.emitProjectUpdated(r, projectID)
	apierr.WriteJSON(w, http.StatusOK, toFolderView(f, projectID))
	return nil
}

// DeleteFolder removes a folder. Historical task.cwd values are untouched.
// DELETE /api/projects/{id}/folders/{folderId}
func (h *Handler) DeleteFolder(w http.ResponseWriter, r *http.Request) error {
	projectID := chi.URLParam(r, "id")
	folderID := chi.URLParam(r, "folderId")

	existing, err := h.folders.GetByID(r.Context(), folderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "folder not found")
		}
		return err
	}
	owner, err := existing.QueryProject().Only(r.Context())
	if err != nil {
		return err
	}
	if owner.ID != projectID {
		return apierr.NewAppError(http.StatusNotFound, "folder not found")
	}

	if err := h.folders.Delete(r.Context(), folderID); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "folder not found")
		}
		return err
	}
	h.emitProjectUpdated(r, projectID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
