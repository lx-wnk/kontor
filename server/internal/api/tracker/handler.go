// Package tracker provides the /api/tracker/* HTTP handler for issue fetching
// and encrypted token settings management.
package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/tracker"
)

const settingsPluginID = "tracker"

// trackerSchema is the canonical settings schema for tracker credentials.
// The two *.token entries are secret — encrypted at rest via plugin.Service.
var trackerSchema = []plugin.SettingField{
	{Key: "tracker.github.token", Type: "string", Label: "GitHub personal access token", Secret: true},
	{Key: "tracker.github.defaultRepo", Type: "string", Label: "GitHub default repo (owner/repo)", Secret: false},
	{Key: "tracker.jira.baseUrl", Type: "url", Label: "Jira base URL (https://yourorg.atlassian.net)", Secret: false},
	{Key: "tracker.jira.email", Type: "string", Label: "Jira account email", Secret: false},
	{Key: "tracker.jira.token", Type: "string", Label: "Jira API token", Secret: true},
}

// ResolverFn is the tracker.Resolve signature; injectable for tests.
type ResolverFn func(ref string, cfg tracker.Config, client *http.Client) (tracker.Tracker, error)

// Handler serves /api/tracker/* endpoints.
type Handler struct {
	settings *plugin.Service
	httpCli  *http.Client
	resolver ResolverFn
}

// NewHandler builds a Handler. resolver defaults to tracker.Resolve if nil;
// httpCli defaults to a 30-second client if nil.
func NewHandler(settings *plugin.Service, httpCli *http.Client, resolver ResolverFn) *Handler {
	if resolver == nil {
		resolver = tracker.Resolve
	}
	if httpCli == nil {
		httpCli = &http.Client{Timeout: 30 * time.Second}
	}
	return &Handler{settings: settings, httpCli: httpCli, resolver: resolver}
}

// Mount registers the tracker routes. Callers must apply JWT + same-origin middleware.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/tracker/settings", apierr.ErrorMiddleware(h.getSettings))
	r.Put("/api/tracker/settings", apierr.ErrorMiddleware(h.putSettings))
	r.Post("/api/tracker/fetch", apierr.ErrorMiddleware(h.fetch))
}

type settingView struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Type   string   `json:"type"`
	Secret bool     `json:"secret"`
	Enum   []string `json:"enum,omitempty"`
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) error {
	values, err := h.settings.Get(r.Context(), settingsPluginID, trackerSchema)
	if err != nil {
		return fmt.Errorf("tracker.settings.get: %w", err)
	}
	schema := make([]settingView, len(trackerSchema))
	for i, f := range trackerSchema {
		schema[i] = settingView{Key: f.Key, Label: f.Label, Type: f.Type, Secret: f.Secret}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{"schema": schema, "values": values})
}

func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Values map[string]string `json:"values"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: invalid JSON", apierr.ErrBadRequest)
	}
	if err := h.settings.Put(r.Context(), settingsPluginID, trackerSchema, body.Values); err != nil {
		if errors.Is(err, plugin.ErrUnknownKey) || errors.Is(err, plugin.ErrInvalidValue) {
			return apierr.NewAppError(http.StatusBadRequest, err.Error())
		}
		return fmt.Errorf("tracker.settings.put: %w", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) fetch(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Ref string `json:"ref"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Ref == "" {
		return apierr.NewAppError(http.StatusBadRequest, "ref is required")
	}
	cfg, err := h.loadConfig(r.Context())
	if err != nil {
		return fmt.Errorf("tracker.fetch.loadConfig: %w", err)
	}
	t, err := h.resolver(body.Ref, cfg, h.httpCli)
	if err != nil {
		// Bad ref shape and missing-config both surface as 400 to the caller.
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	iss, err := t.FetchIssue(r.Context(), body.Ref)
	if err != nil {
		switch {
		case errors.Is(err, tracker.ErrBadRef):
			return apierr.NewAppError(http.StatusBadRequest, err.Error())
		case errors.Is(err, tracker.ErrTrackerAuth):
			return apierr.NewAppError(http.StatusUnauthorized, "tracker rejected the token")
		case errors.Is(err, tracker.ErrIssueNotFound):
			return apierr.NewAppError(http.StatusNotFound, "issue not found")
		default:
			return apierr.NewAppError(http.StatusBadGateway, "upstream error fetching issue")
		}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(iss)
}

func (h *Handler) loadConfig(ctx context.Context) (tracker.Config, error) {
	vals, err := h.settings.Decrypted(ctx, settingsPluginID, trackerSchema)
	if err != nil {
		return tracker.Config{}, err
	}
	return tracker.Config{
		GitHubToken:   vals["tracker.github.token"],
		GitHubDefRepo: vals["tracker.github.defaultRepo"],
		JiraBaseURL:   vals["tracker.jira.baseUrl"],
		JiraEmail:     vals["tracker.jira.email"],
		JiraToken:     vals["tracker.jira.token"],
	}, nil
}
