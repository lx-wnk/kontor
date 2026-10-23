package cloudroutines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/api/cloudsessions"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/cloud"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/cloudsession"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const (
	maxTokenBytes = 4096
	maxTextBytes  = 100000
	maxBodyBytes  = 1 << 20

	mayHaveStarted = "the routine may have started a run; check claude.ai before firing again"
)

// Firer starts a routine run; cloud.RoutineClient satisfies it.
type Firer interface {
	Fire(ctx context.Context, fireURL, token, text string) (cloud.FireResult, error)
}

// Handler serves the cloud routine routes.
type Handler struct {
	routines    repo.CloudRoutineRepo
	sessions    repo.CloudSessionRepo
	spawners    repo.SpawnerRepo
	firer       Firer
	broadcaster *sse.TypedBroadcaster[sse.CloudSessionEvent]
	previewOn   func() bool

	firingMu sync.Mutex
	firing   map[string]struct{}
}

// NewHandler returns a Handler. previewOn reports the preview features switch;
// every route answers 404 while it is off. It panics on any nil dependency.
func NewHandler(routines repo.CloudRoutineRepo, sessions repo.CloudSessionRepo, spawners repo.SpawnerRepo, firer Firer,
	broadcaster *sse.TypedBroadcaster[sse.CloudSessionEvent], previewOn func() bool) *Handler {
	if routines == nil || sessions == nil || spawners == nil || firer == nil || broadcaster == nil || previewOn == nil {
		panic("cloudroutines.Handler requires routines, sessions, spawners, firer, broadcaster and previewOn")
	}
	return &Handler{
		routines: routines, sessions: sessions, spawners: spawners, firer: firer,
		broadcaster: broadcaster, previewOn: previewOn, firing: map[string]struct{}{},
	}
}

// Mount registers the cloud routine routes on r. Caller must wrap r with
// RequireAuth — these routes hold and use claude.ai bearer tokens.
func (h *Handler) Mount(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(h.requirePreview)
		g.Get("/api/cloud-routines", apierr.ErrorMiddleware(h.List))
		g.Post("/api/cloud-routines", apierr.ErrorMiddleware(h.Create))
		g.Patch("/api/cloud-routines/{id}", apierr.ErrorMiddleware(h.Update))
		g.Delete("/api/cloud-routines/{id}", apierr.ErrorMiddleware(h.Delete))
		g.Post("/api/cloud-routines/{id}/fire", apierr.ErrorMiddleware(h.Fire))
	})
}

func (h *Handler) requirePreview(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.previewOn() {
			apierr.JSONError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type routineView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	FireURL   string `json:"fireUrl"`
	SpawnerID string `json:"spawnerId"`
	HasToken  bool   `json:"hasToken"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func newRoutineView(r *ent.CloudRoutine) routineView {
	return routineView{
		ID:        r.ID,
		Name:      r.Name,
		FireURL:   r.FireURL,
		SpawnerID: r.SpawnerID,
		HasToken:  repo.CloudRoutineHasToken(r),
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func badRequest(msg string) error { return apierr.NewAppError(http.StatusBadRequest, msg) }

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return badRequest("invalid JSON body")
	}
	return nil
}

func validateName(s string) (string, error) {
	name, err := cloudsessions.ValidateTitle(s)
	if err != nil {
		return "", badRequest(err.Error())
	}
	if name == "" {
		return "", badRequest("name is required")
	}
	return name, nil
}

func validateFireURL(s string) (string, error) {
	u := strings.TrimSpace(s)
	if err := cloud.ValidateFireURL(u); err != nil {
		return "", badRequest(err.Error())
	}
	return u, nil
}

func validateToken(s string) error {
	switch {
	case s == "":
		return badRequest("token is required")
	case len(s) > maxTokenBytes:
		return badRequest(fmt.Sprintf("token must be at most %d bytes", maxTokenBytes))
	case strings.ContainsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }):
		return badRequest("token: contains whitespace or control characters")
	}
	return nil
}

func (h *Handler) claudeSpawner(ctx context.Context, id string) (*ent.Spawner, error) {
	sp, err := h.spawners.GetByID(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, badRequest("spawner not found")
		}
		return nil, err
	}
	if !cloudsessions.RunsClaude(sp) {
		return nil, badRequest("cloud routines need a claude spawner")
	}
	return sp, nil
}

func (h *Handler) acquireFire(id string) bool {
	h.firingMu.Lock()
	defer h.firingMu.Unlock()
	if _, busy := h.firing[id]; busy {
		return false
	}
	h.firing[id] = struct{}{}
	return true
}

func (h *Handler) releaseFire(id string) {
	h.firingMu.Lock()
	defer h.firingMu.Unlock()
	delete(h.firing, id)
}

func warnUnrecorded(routineID, reason string) {
	slog.Warn("routine run may have started without a Kontor session record", "routineId", routineID, "reason", reason)
}

func mapRepoError(err error) error {
	switch {
	case errors.Is(err, repo.ErrSecretsUnavailable):
		return apierr.NewAppError(http.StatusServiceUnavailable, "routine tokens are unavailable: no secret key configured")
	case ent.IsNotFound(err):
		return apierr.NewAppError(http.StatusNotFound, "cloud routine not found")
	}
	return err
}

// List returns all routines ordered by name; the token is never included.
// GET /api/cloud-routines
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.routines.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]routineView, len(rows))
	for i, row := range rows {
		out[i] = newRoutineView(row)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createBody struct {
	Name      string `json:"name"`
	FireURL   string `json:"fireUrl"`
	SpawnerID string `json:"spawnerId"`
	Token     string `json:"token"`
}

// Create stores a routine with its token encrypted.
// POST /api/cloud-routines
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) error {
	var body createBody
	if err := decodeBody(w, r, &body); err != nil {
		return err
	}
	name, err := validateName(body.Name)
	if err != nil {
		return err
	}
	fireURL, err := validateFireURL(body.FireURL)
	if err != nil {
		return err
	}
	if err := validateToken(body.Token); err != nil {
		return err
	}
	sp, err := h.claudeSpawner(r.Context(), body.SpawnerID)
	if err != nil {
		return err
	}
	created, err := h.routines.Create(r.Context(), repo.CreateCloudRoutineInput{
		Name: name, FireURL: fireURL, SpawnerID: sp.ID, Token: body.Token,
	})
	if err != nil {
		return mapRepoError(err)
	}
	apierr.WriteJSON(w, http.StatusCreated, newRoutineView(created))
	return nil
}

type updateBody struct {
	Name    *string `json:"name"`
	FireURL *string `json:"fireUrl"`
	Token   *string `json:"token"`
}

// Update changes a routine; an empty token keeps the stored one.
// PATCH /api/cloud-routines/{id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) error {
	var body updateBody
	if err := decodeBody(w, r, &body); err != nil {
		return err
	}
	var in repo.UpdateCloudRoutineInput
	if body.Name != nil {
		name, err := validateName(*body.Name)
		if err != nil {
			return err
		}
		in.Name = &name
	}
	if body.FireURL != nil {
		fireURL, err := validateFireURL(*body.FireURL)
		if err != nil {
			return err
		}
		in.FireURL = &fireURL
	}
	if body.Token != nil && *body.Token != "" {
		if err := validateToken(*body.Token); err != nil {
			return err
		}
		in.Token = body.Token
	}
	updated, err := h.routines.Update(r.Context(), chi.URLParam(r, "id"), in)
	if err != nil {
		return mapRepoError(err)
	}
	apierr.WriteJSON(w, http.StatusOK, newRoutineView(updated))
	return nil
}

// Delete removes the routine; sessions it already started stay.
// DELETE /api/cloud-routines/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) error {
	if err := h.routines.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		return mapRepoError(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type fireBody struct {
	Text string `json:"text"`
}

// Fire starts the routine and records the cloud session it created. The
// session URL is rebuilt from the validated ID; the one the API returned is ignored.
// POST /api/cloud-routines/{id}/fire
func (h *Handler) Fire(w http.ResponseWriter, r *http.Request) error {
	var body fireBody
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		return badRequest("invalid JSON body")
	}
	if len(body.Text) > maxTextBytes {
		return badRequest(fmt.Sprintf("text must be at most %d bytes", maxTextBytes))
	}

	id := chi.URLParam(r, "id")
	routine, err := h.routines.Get(r.Context(), id)
	if err != nil {
		return mapRepoError(err)
	}
	if !h.acquireFire(id) {
		return apierr.NewAppError(http.StatusConflict, "routine is already firing")
	}
	defer h.releaseFire(id)
	token, err := h.routines.Token(r.Context(), id)
	if err != nil {
		return mapRepoError(err)
	}
	sp, err := h.spawners.GetByID(r.Context(), routine.SpawnerID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusConflict, "spawner of this routine no longer exists")
		}
		return err
	}
	if !cloudsessions.RunsClaude(sp) {
		return apierr.NewAppError(http.StatusConflict, "spawner "+sp.ID+" no longer runs Claude")
	}

	// A client that hangs up after the routine started must not lose the session record.
	ctx := context.WithoutCancel(r.Context())
	result, err := h.firer.Fire(ctx, routine.FireURL, token, body.Text)
	if err != nil {
		var fireErr *cloud.FireError
		if errors.As(err, &fireErr) {
			apierr.WriteJSON(w, http.StatusBadGateway, map[string]any{
				"error":  err.Error(),
				"status": fireErr.Status,
				"body":   fireErr.Body,
			})
			return nil
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			warnUnrecorded(id, "fire timed out or was canceled")
			return apierr.NewAppError(http.StatusBadGateway, "routine fire did not finish; "+mayHaveStarted)
		}
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	sessionID, err := cloud.ParseSessionRef(result.SessionID)
	if err != nil {
		warnUnrecorded(id, "fire result has an invalid session id")
		return apierr.NewAppError(http.StatusBadGateway, "routine fire returned an invalid session id")
	}

	created, err := h.sessions.Create(ctx, repo.CreateCloudSessionInput{
		SessionID: sessionID,
		URL:       cloud.SessionURL(sessionID),
		SpawnerID: sp.ID,
		Title:     routine.Name,
		Origin:    cloudsession.OriginRoutine,
	})
	if err != nil {
		warnUnrecorded(id, "session row was not created")
		if errors.Is(err, repo.ErrCloudSessionExists) {
			return apierr.NewAppError(http.StatusConflict, "cloud session already recorded")
		}
		slog.Error("record routine session", "routineId", id, "err", err)
		return apierr.NewAppError(http.StatusBadGateway, "routine session could not be recorded; "+mayHaveStarted)
	}
	view := cloudsessions.NewSessionView(created)
	h.broadcaster.Broadcast(sse.CloudSessionEvent{Type: sse.CloudSessionCreated, CloudSessionID: created.ID, Payload: view})
	apierr.WriteJSON(w, http.StatusCreated, view)
	return nil
}
