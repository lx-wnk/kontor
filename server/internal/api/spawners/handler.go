package spawners

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/services"
	"github.com/lx-wnk/kontor/server/internal/sse"
	"github.com/lx-wnk/kontor/server/internal/validation"
)

// Handler exposes admin-only CRUD endpoints for spawners.
type Handler struct {
	repo        repo.SpawnerRepo
	broadcaster *sse.SpawnerBroadcaster
}

// NewHandler returns a Handler backed by the given repo. broadcaster may be nil
// (e.g. in tests); emit becomes a no-op then.
func NewHandler(r repo.SpawnerRepo, broadcaster *sse.SpawnerBroadcaster) *Handler {
	return &Handler{repo: r, broadcaster: broadcaster}
}

// emit broadcasts a typed spawner event. No-op when broadcaster is nil.
func (h *Handler) emit(eventType, id string, payload any) {
	if h.broadcaster == nil {
		return
	}
	h.broadcaster.Broadcast(sse.SpawnerEvent{Type: eventType, SpawnerID: id, Payload: payload})
}

// Stream serves GET /api/spawners/stream — live spawner CRUD events.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	sse.WriteHeaders(w)
	flusher.Flush()
	sub := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)
	for {
		select {
		case data, ok := <-sub:
			if !ok {
				return
			}
			w.Write(data) //nolint:errcheck
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// Mount registers all spawner routes on r. Caller must wrap r with
// RequireAuth + RequireAdminOrBypass — spawner CRUD is RCE-equivalent.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/spawners", apierr.ErrorMiddleware(h.List))
	r.Post("/api/spawners", apierr.ErrorMiddleware(h.Create))
	r.Get("/api/spawners/{id}", apierr.ErrorMiddleware(h.Get))
	r.Patch("/api/spawners/{id}", apierr.ErrorMiddleware(h.Update))
	r.Post("/api/spawners/{id}/default", apierr.ErrorMiddleware(h.SetDefault))
	r.Delete("/api/spawners/{id}", apierr.ErrorMiddleware(h.Delete))
}

const isoFormat = "2006-01-02T15:04:05Z"

func tsFmt(t time.Time) string { return t.UTC().Format(isoFormat) }

type spawnerView struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	AdapterType   string            `json:"adapterType"`
	AdapterConfig map[string]string `json:"adapterConfig"`
	ModelOverride *string           `json:"modelOverride,omitempty"`
	Description   *string           `json:"description,omitempty"`
	BuiltIn       bool              `json:"builtIn"`
	IsDefault     bool              `json:"isDefault"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt"`
}

func toSpawnerView(s *ent.Spawner) spawnerView {
	args := s.Args
	if args == nil {
		args = []string{}
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	adapterCfg := s.AdapterConfig
	if adapterCfg == nil {
		adapterCfg = map[string]string{}
	}
	adapterType := s.AdapterType
	if adapterType == "" {
		adapterType = "claude"
	}
	return spawnerView{
		ID:            s.ID,
		Name:          s.Name,
		Slug:          s.Slug,
		Command:       s.Command,
		Args:          args,
		Env:           env,
		AdapterType:   adapterType,
		AdapterConfig: adapterCfg,
		ModelOverride: s.ModelOverride,
		Description:   s.Description,
		BuiltIn:       s.BuiltIn,
		IsDefault:     s.IsDefault,
		CreatedAt:     tsFmt(s.CreatedAt),
		UpdatedAt:     tsFmt(s.UpdatedAt),
	}
}

// List returns all spawners (built-in + custom).
// GET /api/spawners
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.repo.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]spawnerView, len(rows))
	for i, s := range rows {
		out[i] = toSpawnerView(s)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createBody struct {
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	AdapterType   string            `json:"adapterType"`
	AdapterConfig map[string]string `json:"adapterConfig"`
	ModelOverride *string           `json:"modelOverride"`
	Description   *string           `json:"description"`
}

// Create creates a custom spawner. builtIn is always forced to false from the API.
// POST /api/spawners
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) error {
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Name == "" {
		return apierr.NewAppError(http.StatusBadRequest, "name is required")
	}
	if !ValidateSlug(body.Slug) {
		return apierr.NewAppError(http.StatusBadRequest, validation.SlugPatternMessage)
	}
	if ok, reason := services.ValidateSpawnerCommand(body.Command); !ok {
		return apierr.NewAppError(http.StatusBadRequest, reason)
	}
	if msg, ok := ValidateEnv(body.Env); !ok {
		return apierr.NewAppError(http.StatusBadRequest, msg)
	}
	adapterType := body.AdapterType
	if adapterType == "" {
		adapterType = "claude"
	}
	if msg, ok := ValidateAdapterType(adapterType); !ok {
		return apierr.NewAppError(http.StatusBadRequest, msg)
	}
	adapterCfg := body.AdapterConfig
	if adapterCfg == nil {
		adapterCfg = map[string]string{}
	}
	if msg, ok := ValidateAdapterConfig(adapterType, adapterCfg); !ok {
		return apierr.NewAppError(http.StatusBadRequest, msg)
	}

	s, err := h.repo.Create(r.Context(), body.Name, body.Slug, body.Command, body.Args, body.Env, body.ModelOverride, body.Description, adapterType, adapterCfg, false)
	if err != nil {
		if ent.IsConstraintError(err) {
			return apierr.NewAppError(http.StatusConflict, "slug already exists")
		}
		return err
	}
	v := toSpawnerView(s)
	h.emit("spawner_created", s.ID, v)
	apierr.WriteJSON(w, http.StatusCreated, v)
	return nil
}

// Get returns a single spawner.
// GET /api/spawners/{id}
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	s, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "spawner not found")
		}
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, toSpawnerView(s))
	return nil
}

type updateBody struct {
	Name          *string           `json:"name"`
	Slug          *string           `json:"slug"`
	Command       *string           `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	AdapterType   *string           `json:"adapterType"`
	AdapterConfig map[string]string `json:"adapterConfig"`
	ModelOverride json.RawMessage   `json:"modelOverride"`
	Description   json.RawMessage   `json:"description"`
}

// Update partially updates a spawner. Built-in spawners are editable EXCEPT for
// their slug (the resolution backstop GetBySlug("claude-default") depends on it)
// — a slug change on a built-in returns 403.
// PATCH /api/spawners/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	existing, err := h.repo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "spawner not found")
		}
		return err
	}

	var body updateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if existing.BuiltIn && body.Slug != nil && *body.Slug != existing.Slug {
		return apierr.NewAppError(http.StatusForbidden, "cannot change the slug of a built-in spawner")
	}
	if body.Slug != nil && !ValidateSlug(*body.Slug) {
		return apierr.NewAppError(http.StatusBadRequest, validation.SlugPatternMessage)
	}
	if body.Command != nil {
		if ok, reason := services.ValidateSpawnerCommand(*body.Command); !ok {
			return apierr.NewAppError(http.StatusBadRequest, reason)
		}
	}
	if body.Env != nil {
		if msg, ok := ValidateEnv(body.Env); !ok {
			return apierr.NewAppError(http.StatusBadRequest, msg)
		}
	}
	if body.AdapterType != nil {
		if msg, ok := ValidateAdapterType(*body.AdapterType); !ok {
			return apierr.NewAppError(http.StatusBadRequest, msg)
		}
	}
	// Effective adapter_type for adapter_config validation: the patched value
	// if provided, otherwise the existing row's value.
	effectiveAdapterType := existing.AdapterType
	if effectiveAdapterType == "" {
		effectiveAdapterType = "claude"
	}
	if body.AdapterType != nil {
		effectiveAdapterType = *body.AdapterType
	}
	if body.AdapterConfig != nil {
		if msg, ok := ValidateAdapterConfig(effectiveAdapterType, body.AdapterConfig); !ok {
			return apierr.NewAppError(http.StatusBadRequest, msg)
		}
	}

	modelOverride, clearModelOverride, err := parseNullableString(body.ModelOverride)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "modelOverride must be a string or null")
	}
	description, clearDescription, err := parseNullableString(body.Description)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "description must be a string or null")
	}

	s, err := h.repo.Update(r.Context(), id,
		body.Name, body.Slug, body.Command,
		body.Args, body.Env,
		modelOverride, description,
		body.AdapterType, body.AdapterConfig,
		clearModelOverride, clearDescription,
	)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "spawner not found")
		}
		if ent.IsConstraintError(err) {
			return apierr.NewAppError(http.StatusConflict, "slug already exists")
		}
		return err
	}
	v := toSpawnerView(s)
	h.emit("spawner_updated", s.ID, v)
	apierr.WriteJSON(w, http.StatusOK, v)
	return nil
}

// SetDefault makes the given spawner the deployment-wide default, clearing the
// previous one atomically. Broadcasts an update for both the new and the former
// default so every connected UI re-renders its badge.
// POST /api/spawners/{id}/default
func (h *Handler) SetDefault(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	s, prevID, err := h.repo.SetDefault(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "spawner not found")
		}
		return err
	}
	v := toSpawnerView(s)
	h.emit("spawner_updated", s.ID, v)
	if prevID != "" {
		if prev, err := h.repo.GetByID(r.Context(), prevID); err == nil {
			h.emit("spawner_updated", prev.ID, toSpawnerView(prev))
		}
	}
	apierr.WriteJSON(w, http.StatusOK, v)
	return nil
}

// Delete removes a spawner. 403 if built-in, 409 if it is the current default or
// still referenced by tasks/projects.
// DELETE /api/spawners/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := h.repo.Delete(r.Context(), id); err != nil {
		switch {
		case errors.Is(err, repo.ErrSpawnerBuiltIn):
			return apierr.NewAppError(http.StatusForbidden, "cannot delete a built-in spawner")
		case errors.Is(err, repo.ErrSpawnerIsDefault):
			return apierr.NewAppError(http.StatusConflict, "cannot delete the default spawner; set another default first")
		case errors.Is(err, repo.ErrSpawnerInUse):
			return apierr.NewAppError(http.StatusConflict, "spawner is still referenced by tasks or projects")
		case ent.IsNotFound(err):
			return apierr.NewAppError(http.StatusNotFound, "spawner not found")
		default:
			return err
		}
	}
	h.emit("spawner_deleted", id, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// parseNullableString decodes a PATCH field that may be absent, JSON null, or
// a string. Returns (value, clear, err): clear=true when JSON null was sent.
func parseNullableString(raw json.RawMessage) (*string, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	s := string(raw)
	if s == "null" {
		return nil, true, nil
	}
	var v string
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, false, err
	}
	return &v, false, nil
}
