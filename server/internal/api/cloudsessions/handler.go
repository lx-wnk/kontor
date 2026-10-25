package cloudsessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/cloud"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/cloudsession"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const (
	maxTitleRunes       = 200
	maxStartTitleRunes  = 120
	maxDescriptionBytes = 10000
	maxTextBytes        = 100000
	maxBodyBytes        = 1 << 20
	authCacheTTL        = 60 * time.Second
	claudeAdapter       = "claude"
	sendFailedText      = "the cloud session did not accept the message"
)

// CLI is the part of the claude CLI the handler needs; cloud.Runner satisfies it.
type CLI interface {
	Send(ctx context.Context, sp *ent.Spawner, sessionID, text string) (cloud.SendResult, error)
	AuthStatus(ctx context.Context, sp *ent.Spawner) (cloud.AuthStatus, error)
	Start(ctx context.Context, sp *ent.Spawner, dir, description string) (cloud.StartResult, error)
}

// ValidateTitle returns the trimmed session title, or an error when it is too long or holds control characters.
func ValidateTitle(s string) (string, error) {
	title := strings.TrimSpace(s)
	if utf8.RuneCountInString(title) > maxTitleRunes {
		return "", fmt.Errorf("title must be at most %d characters", maxTitleRunes)
	}
	if strings.ContainsFunc(title, unicode.IsControl) {
		return "", errors.New("title: contains control characters")
	}
	return title, nil
}

// RunsClaude reports whether the spawner drives the claude CLI; an empty adapter
// type is the schema default, claude.
func RunsClaude(sp *ent.Spawner) bool {
	return sp.AdapterType == "" || sp.AdapterType == claudeAdapter
}

type authEntry struct {
	status  cloud.AuthStatus
	fetched time.Time
}

// authCache memoises `claude auth status` per account dir.
type authCache struct {
	cli     CLI
	now     func() time.Time
	flight  singleflight.Group
	mu      sync.Mutex
	entries map[string]authEntry
}

// newAuthCache returns a cache with a 60 s TTL; now is the clock.
func newAuthCache(cli CLI, now func() time.Time) *authCache {
	return &authCache{cli: cli, now: now, entries: map[string]authEntry{}}
}

// Status returns the cached status for the spawner's config dir, asking the CLI
// when the entry is older than the TTL or refresh is set. Concurrent lookups for
// one account share a single CLI call. Only CloudReady results are cached, so a
// retry right after `claude auth login` sees the new state.
func (c *authCache) Status(ctx context.Context, sp *ent.Spawner, refresh bool) (cloud.AuthStatus, error) {
	key := cloud.AccountDir(sp)
	c.mu.Lock()
	e, ok := c.entries[key]
	c.mu.Unlock()
	if ok && !refresh && c.now().Sub(e.fetched) < authCacheTTL {
		return e.status, nil
	}
	// The shared call must not die with the first caller's request.
	v, err, _ := c.flight.Do(key, func() (any, error) {
		st, err := c.cli.AuthStatus(context.WithoutCancel(ctx), sp)
		if err != nil {
			return nil, err
		}
		if st.CloudReady() {
			c.mu.Lock()
			c.entries[key] = authEntry{status: st, fetched: c.now()}
			c.mu.Unlock()
		}
		return st, nil
	})
	if err != nil {
		return cloud.AuthStatus{}, err
	}
	return v.(cloud.AuthStatus), nil
}

// Handler serves the cloud session routes.
type Handler struct {
	sessions    repo.CloudSessionRepo
	spawners    repo.SpawnerRepo
	folders     repo.ProjectFolderRepo
	cli         CLI
	auth        *authCache
	broadcaster *sse.TypedBroadcaster[sse.CloudSessionEvent]

	startingMu sync.Mutex
	starting   map[string]struct{}
}

// NewHandler returns a Handler. It panics on a nil broadcaster.
func NewHandler(sessions repo.CloudSessionRepo, spawners repo.SpawnerRepo, folders repo.ProjectFolderRepo, cli CLI,
	broadcaster *sse.TypedBroadcaster[sse.CloudSessionEvent]) *Handler {
	if broadcaster == nil {
		panic("cloudsessions.Handler requires a broadcaster")
	}
	return &Handler{
		sessions:    sessions,
		spawners:    spawners,
		folders:     folders,
		cli:         cli,
		auth:        newAuthCache(cli, time.Now),
		broadcaster: broadcaster,
		starting:    map[string]struct{}{},
	}
}

func (h *Handler) emit(eventType, id string, payload any) {
	h.broadcaster.Broadcast(sse.CloudSessionEvent{Type: eventType, CloudSessionID: id, Payload: payload})
}

// Mount registers the cloud session routes on r. Caller must wrap r with
// RequireAuth — these routes run the claude CLI under a spawner's account.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/cloud-sessions", apierr.ErrorMiddleware(h.List))
	r.Get("/api/cloud-sessions/stream", h.Stream)
	r.Post("/api/cloud-sessions", apierr.ErrorMiddleware(h.Create))
	r.Post("/api/cloud-sessions/import", apierr.ErrorMiddleware(h.Import))
	r.Post("/api/cloud-sessions/{id}/messages", apierr.ErrorMiddleware(h.Send))
	r.Delete("/api/cloud-sessions/{id}", apierr.ErrorMiddleware(h.Delete))
	r.Get("/api/cloud-accounts", apierr.ErrorMiddleware(h.Accounts))
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

// SessionView is the JSON shape of a cloud session in responses and events.
type SessionView struct {
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

// NewSessionView maps a stored cloud session to its JSON view.
func NewSessionView(s *ent.CloudSession) SessionView {
	var at *string
	if s.LastMessageAt != nil {
		f := s.LastMessageAt.UTC().Format(time.RFC3339)
		at = &f
	}
	return SessionView{
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
	out := make([]SessionView, len(rows))
	for i, s := range rows {
		out[i] = NewSessionView(s)
	}
	apierr.WriteJSON(w, http.StatusOK, out)
	return nil
}

type createBody struct {
	SpawnerID   string `json:"spawnerId"`
	FolderID    string `json:"folderId"`
	Description string `json:"description"`
}

// Create starts a claude.ai cloud session for the description from a stored project folder and records it.
// POST /api/cloud-sessions
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) error {
	var body createBody
	if err := decodeBody(w, r, &body); err != nil {
		return err
	}
	description, err := validateDescription(body.Description)
	if err != nil {
		return err
	}
	sp, err := h.spawners.GetByID(r.Context(), body.SpawnerID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusBadRequest, "spawner not found")
		}
		return err
	}
	if !RunsClaude(sp) {
		return apierr.NewAppError(http.StatusBadRequest, "cloud sessions need a claude spawner")
	}
	folder, err := h.folders.GetByID(r.Context(), body.FolderID)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.NewAppError(http.StatusBadRequest, "project folder not found")
		}
		return err
	}
	if info, statErr := os.Stat(folder.Path); statErr != nil || !info.IsDir() {
		return apierr.NewAppError(http.StatusBadRequest, "project folder does not exist on disk")
	}

	account := cloud.AccountDir(sp)
	if !h.acquireStart(account) {
		return apierr.NewAppError(http.StatusConflict, "a cloud session is already being started for this account")
	}
	defer h.releaseStart(account)

	// A client that hangs up mid-start must not skip recording the session it started.
	ctx := context.WithoutCancel(r.Context())
	st, err := h.auth.Status(ctx, sp, false)
	if err != nil {
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	if !st.CloudReady() {
		return apierr.NewAppError(http.StatusConflict,
			"account is not signed in with claude.ai; run: "+cloud.LoginHint(sp))
	}

	res, err := h.cli.Start(ctx, sp, folder.Path, description)
	if err != nil {
		return startFailure(w, sp, folder.Path, err)
	}

	created, err := h.sessions.Create(ctx, repo.CreateCloudSessionInput{
		SessionID: res.SessionID,
		URL:       res.URL,
		SpawnerID: sp.ID,
		Title:     startTitle(description),
		Origin:    cloudsession.OriginStarted,
	})
	if err != nil {
		if errors.Is(err, repo.ErrCloudSessionExists) {
			return apierr.NewAppError(http.StatusConflict, "cloud session already recorded")
		}
		slog.Warn("cloud session started without a Kontor record", "sessionId", res.SessionID, "url", res.URL, "error", err)
		return writeMayHaveStarted(w, "the cloud session started but Kontor could not record it; import it by URL: "+res.URL, res.URL)
	}
	v := NewSessionView(created)
	h.emit(sse.CloudSessionCreated, created.ID, v)
	apierr.WriteJSON(w, http.StatusCreated, v)
	return nil
}

type mayHaveStartedBody struct {
	Error          string `json:"error"`
	MayHaveStarted bool   `json:"mayHaveStarted"`
	URL            string `json:"url,omitempty"`
}

func writeMayHaveStarted(w http.ResponseWriter, message, url string) error {
	apierr.WriteJSON(w, http.StatusBadGateway, mayHaveStartedBody{Error: message, MayHaveStarted: true, URL: url})
	return nil
}

// startFailure maps a failed Runner.Start. ErrStartUnconfirmed goes first: a
// trust prompt after the title line wraps it together with ErrFolderNotTrusted.
func startFailure(w http.ResponseWriter, sp *ent.Spawner, folderPath string, err error) error {
	switch {
	case errors.Is(err, cloud.ErrStartUnconfirmed):
		slog.Warn("cloud session start unconfirmed", "spawnerId", sp.ID, "error", err)
		return writeMayHaveStarted(w, err.Error(), "")
	case errors.Is(err, cloud.ErrInvalidDescription):
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	case errors.Is(err, cloud.ErrFolderNotTrusted):
		return apierr.NewAppError(http.StatusConflict,
			"claude does not trust this folder for this account yet; run once and accept the trust prompt: "+cloud.TrustHint(sp, folderPath))
	}
	return apierr.NewAppError(http.StatusBadGateway, err.Error())
}

func isForbiddenControl(r rune) bool {
	return unicode.IsControl(r) && r != '\n' && r != '\t'
}

func validateDescription(s string) (string, error) {
	description := strings.TrimSpace(s)
	switch {
	case description == "":
		return "", apierr.NewAppError(http.StatusBadRequest, "description is required")
	case strings.HasPrefix(description, "-"):
		return "", apierr.NewAppError(http.StatusBadRequest, `description must not start with "-"`)
	case len(description) > maxDescriptionBytes:
		return "", apierr.NewAppError(http.StatusBadRequest, fmt.Sprintf("description must be at most %d bytes", maxDescriptionBytes))
	case strings.ContainsFunc(description, isForbiddenControl):
		return "", apierr.NewAppError(http.StatusBadRequest, "description must not contain control characters")
	}
	return description, nil
}

// startTitle is the description's first line, free of control characters and cut to maxStartTitleRunes.
func startTitle(description string) string {
	line, _, _ := strings.Cut(description, "\n")
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, line)
	if runes := []rune(line); len(runes) > maxStartTitleRunes {
		line = string(runes[:maxStartTitleRunes])
	}
	return strings.TrimSpace(line)
}

func (h *Handler) acquireStart(account string) bool {
	h.startingMu.Lock()
	defer h.startingMu.Unlock()
	if _, busy := h.starting[account]; busy {
		return false
	}
	h.starting[account] = struct{}{}
	return true
}

func (h *Handler) releaseStart(account string) {
	h.startingMu.Lock()
	defer h.startingMu.Unlock()
	delete(h.starting, account)
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
	title, err := ValidateTitle(body.Title)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
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
	if !RunsClaude(sp) {
		return apierr.NewAppError(http.StatusBadRequest, "cloud sessions need a claude spawner")
	}

	created, err := h.sessions.Create(r.Context(), repo.CreateCloudSessionInput{
		SessionID: sessionID,
		URL:       cloud.SessionURL(sessionID),
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
	v := NewSessionView(created)
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

	if !RunsClaude(sp) {
		return apierr.NewAppError(http.StatusConflict, "spawner "+sp.ID+" no longer runs Claude")
	}

	// A client that hangs up mid-send must not skip recording the outcome.
	ctx := context.WithoutCancel(r.Context())
	st, err := h.auth.Status(ctx, sp, false)
	if err != nil {
		updated, recErr := h.sessions.RecordError(ctx, session.ID, err.Error())
		if recErr != nil {
			if ent.IsNotFound(recErr) {
				return apierr.NewAppError(http.StatusNotFound, "cloud session not found")
			}
			return recErr
		}
		h.emit(sse.CloudSessionUpdated, updated.ID, NewSessionView(updated))
		return apierr.NewAppError(http.StatusBadGateway, err.Error())
	}
	if !st.CloudReady() {
		return apierr.NewAppError(http.StatusConflict,
			"account is not signed in with claude.ai; run: "+cloud.LoginHint(sp))
	}

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
	v := NewSessionView(updated)
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
