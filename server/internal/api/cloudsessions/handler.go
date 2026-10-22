package cloudsessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/cloud"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/cloudsession"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pathutil"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const (
	maxTitleRunes  = 200
	maxTextBytes   = 100000
	maxBodyBytes   = 1 << 20
	authCacheTTL   = 60 * time.Second
	claudeAdapter  = "claude"
	sendFailedText = "the cloud session did not accept the message"
)

// CLI is the part of the claude CLI the handler needs; cloud.Runner satisfies it.
type CLI interface {
	Send(ctx context.Context, sp *ent.Spawner, sessionID, text string) (cloud.SendResult, error)
	AuthStatus(ctx context.Context, sp *ent.Spawner) (cloud.AuthStatus, error)
}

// ConfigDir is the Claude config dir a spawner's CLI runs on.
func ConfigDir(sp *ent.Spawner) string {
	if dir := pipeline.BaseSpawnEnv(sp)[claudeconfig.EnvVar]; dir != "" {
		return dir
	}
	return claudeconfig.DefaultDir()
}

type authEntry struct {
	status  cloud.AuthStatus
	fetched time.Time
}

// AuthCache memoises `claude auth status` per canonical config dir.
type AuthCache struct {
	cli     CLI
	now     func() time.Time
	mu      sync.Mutex
	entries map[string]authEntry
}

// NewAuthCache returns a cache with a 60 s TTL; now is the clock.
func NewAuthCache(cli CLI, now func() time.Time) *AuthCache {
	return &AuthCache{cli: cli, now: now, entries: map[string]authEntry{}}
}

// Status returns the cached status for the spawner's config dir, asking the CLI
// when the entry is older than the TTL or refresh is set. Errors are not cached.
func (c *AuthCache) Status(ctx context.Context, sp *ent.Spawner, refresh bool) (cloud.AuthStatus, error) {
	key := pathutil.Canonical(ConfigDir(sp))
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && !refresh && c.now().Sub(e.fetched) < authCacheTTL {
		return e.status, nil
	}
	st, err := c.cli.AuthStatus(ctx, sp)
	if err != nil {
		return cloud.AuthStatus{}, err
	}
	c.mu.Lock()
	c.entries[key] = authEntry{status: st, fetched: c.now()}
	c.mu.Unlock()
	return st, nil
}

// Handler serves the cloud session routes.
type Handler struct {
	sessions    repo.CloudSessionRepo
	spawners    repo.SpawnerRepo
	cli         CLI
	auth        *AuthCache
	broadcaster *sse.TypedBroadcaster[sse.CloudSessionEvent]
}

// NewHandler returns a Handler. broadcaster may be nil; emit is then a no-op.
func NewHandler(sessions repo.CloudSessionRepo, spawners repo.SpawnerRepo, cli CLI, broadcaster *sse.TypedBroadcaster[sse.CloudSessionEvent]) *Handler {
	return &Handler{
		sessions:    sessions,
		spawners:    spawners,
		cli:         cli,
		auth:        NewAuthCache(cli, time.Now),
		broadcaster: broadcaster,
	}
}

func (h *Handler) emit(eventType, id string, payload any) {
	if h.broadcaster == nil {
		return
	}
	h.broadcaster.Broadcast(sse.CloudSessionEvent{Type: eventType, CloudSessionID: id, Payload: payload})
}

// Mount registers the cloud session routes on r. Caller must wrap r with
// RequireAuth — these routes run the claude CLI under a spawner's account.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/cloud-sessions", apierr.ErrorMiddleware(h.List))
	r.Get("/api/cloud-sessions/stream", h.Stream)
	r.Post("/api/cloud-sessions/import", apierr.ErrorMiddleware(h.Import))
	r.Post("/api/cloud-sessions/{id}/messages", apierr.ErrorMiddleware(h.Send))
	r.Delete("/api/cloud-sessions/{id}", apierr.ErrorMiddleware(h.Delete))
}

// Stream serves GET /api/cloud-sessions/stream — live cloud session events.
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	sub := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(sub)
	sse.WriteHeaders(w)
	flusher.Flush()
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

type sessionView struct {
	ID            string  `json:"id"`
	SessionID     string  `json:"sessionId"`
	URL           string  `json:"url"`
	SpawnerID     string  `json:"spawnerId"`
	Title         string  `json:"title"`
	Origin        string  `json:"origin"`
	LastMessage   string  `json:"lastMessage"`
	LastMessageAt *string `json:"lastMessageAt"`
	LastError     string  `json:"lastError"`
	CreatedAt     string  `json:"createdAt"`
	UpdatedAt     string  `json:"updatedAt"`
}

func toView(s *ent.CloudSession) sessionView {
	var at *string
	if s.LastMessageAt != nil {
		f := s.LastMessageAt.UTC().Format(time.RFC3339)
		at = &f
	}
	return sessionView{
		ID:            s.ID,
		SessionID:     s.SessionID,
		URL:           s.URL,
		SpawnerID:     s.SpawnerID,
		Title:         s.Title,
		Origin:        string(s.Origin),
		LastMessage:   s.LastMessage,
		LastMessageAt: at,
		LastError:     s.LastError,
		CreatedAt:     s.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     s.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	return nil
}

// List returns all cloud sessions, newest first.
// GET /api/cloud-sessions
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.sessions.List(r.Context())
	if err != nil {
		return err
	}
	out := make([]sessionView, len(rows))
	for i, s := range rows {
		out[i] = toView(s)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type importBody struct {
	SpawnerID string `json:"spawnerId"`
	URL       string `json:"url"`
	Title     string `json:"title"`
}

// Import records an existing claude.ai cloud session under a spawner.
// POST /api/cloud-sessions/import
func (h *Handler) Import(w http.ResponseWriter, r *http.Request) error {
	var body importBody
	if err := decodeBody(w, r, &body); err != nil {
		return err
	}
	sessionID, err := cloud.ParseSessionRef(strings.TrimSpace(body.URL))
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	title := strings.TrimSpace(body.Title)
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return apierr.NewAppError(http.StatusBadRequest, fmt.Sprintf("title must be at most %d characters", maxTitleRunes))
	}
	if title == "" {
		title = sessionID
	}
	sp, err := h.spawners.GetByID(r.Context(), body.SpawnerID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusBadRequest, "spawner not found")
		}
		return err
	}
	if sp.AdapterType != "" && sp.AdapterType != claudeAdapter {
		return apierr.NewAppError(http.StatusBadRequest, "cloud sessions need a claude spawner")
	}

	created, err := h.sessions.Create(r.Context(), repo.CreateCloudSessionInput{
		SessionID: sessionID,
		URL:       "https://claude.ai/code/" + sessionID,
		SpawnerID: sp.ID,
		Title:     title,
		Origin:    cloudsession.OriginImported,
	})
	if err != nil {
		if errors.Is(err, repo.ErrCloudSessionExists) {
			return apierr.NewAppError(http.StatusConflict, "cloud session already imported")
		}
		return err
	}
	v := toView(created)
	h.emit(sse.CloudSessionCreated, created.ID, v)
	apierr.WriteJSON(w, http.StatusCreated, v)
	return nil
}

type sendBody struct {
	Text string `json:"text"`
}

// Send delivers a message to the cloud session through the spawner's account.
// It never retries: a timeout can fire after the message was already delivered.
// POST /api/cloud-sessions/{id}/messages
func (h *Handler) Send(w http.ResponseWriter, r *http.Request) error {
	var body sendBody
	if err := decodeBody(w, r, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Text) == "" {
		return apierr.NewAppError(http.StatusBadRequest, "text is required")
	}
	if len(body.Text) > maxTextBytes {
		return apierr.NewAppError(http.StatusBadRequest, fmt.Sprintf("text must be at most %d bytes", maxTextBytes))
	}

	id := chi.URLParam(r, "id")
	session, err := h.sessions.Get(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "cloud session not found")
		}
		return err
	}
	sp, err := h.spawners.GetByID(r.Context(), session.SpawnerID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "spawner not found")
		}
		return err
	}

	st, err := h.auth.Status(r.Context(), sp, false)
	if err != nil {
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	if !st.CloudReady() {
		return apierr.NewAppError(http.StatusConflict,
			"account is not signed in with claude.ai; run: CLAUDE_CONFIG_DIR="+ConfigDir(sp)+" claude auth login")
	}

	// A client that hangs up mid-send must not skip recording the outcome.
	ctx := context.WithoutCancel(r.Context())
	result, sendErr := h.cli.Send(ctx, sp, session.SessionID, body.Text)
	errText := ""
	switch {
	case sendErr != nil:
		errText = sendErr.Error()
	case !result.OK:
		errText = result.Error
		if errText == "" {
			errText = sendFailedText
		}
	}
	updated, err := h.sessions.RecordSend(ctx, session.ID, body.Text, time.Now().UTC(), errText)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "cloud session not found")
		}
		return err
	}
	v := toView(updated)
	h.emit(sse.CloudSessionUpdated, updated.ID, v)
	if sendErr != nil {
		return apierr.NewAppError(http.StatusBadGateway, errText)
	}
	apierr.WriteJSON(w, http.StatusOK, v)
	return nil
}

// Delete removes Kontor's row only; the claude.ai session is untouched.
// DELETE /api/cloud-sessions/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if err := h.sessions.Delete(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusNotFound, "cloud session not found")
		}
		return err
	}
	h.emit(sse.CloudSessionDeleted, id, nil)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
