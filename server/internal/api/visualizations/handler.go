// Package visualizations implements the /api/visualizations/* endpoints.
// All handlers are read-only and share the analytics package's JSONL
// scanning helpers. Endpoints stay layer-clean: this package may import
// analytics, parser, and sdk, but never pipeline/, db/ent, or mcp/.
package visualizations

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/analytics"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// requestTimeout is the per-endpoint upper bound. Spec §E hard-caps each
// visualization scan at 5 seconds; analytics.scanSessionsForTools honors
// ctx cancellation so this is enforced via context.WithTimeout.
const requestTimeout = 5 * time.Second

// defaultWindow is the lookback used when neither `from` nor `to` is
// provided. Aligned with spec §A.
const defaultWindow = 7 * 24 * time.Hour

// Handler is a stateless bundle of /api/visualizations/* HTTP handlers.
type Handler struct{}

// NewHandler builds a Handler. Kept as a function so future deps (e.g.
// an injected scan-dir override) can be added without churning callers.
func NewHandler() *Handler { return &Handler{} }

// Mount registers every visualization route on the given router. Must be
// called from inside the JWT-protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/visualizations/sankey", apierr.ErrorMiddleware(withTimeout(h.Sankey)))
	r.Get("/api/visualizations/dag", apierr.ErrorMiddleware(withTimeout(h.DAG)))
	r.Get("/api/visualizations/spawn-tree", apierr.ErrorMiddleware(withTimeout(h.SpawnTree)))
	r.Get("/api/visualizations/co-occurrence", apierr.ErrorMiddleware(withTimeout(h.CoOccurrence)))
}

// withTimeout wraps an apierr.HandlerFunc so the request context is
// cancelled after requestTimeout and the response surfaces a 503 if the
// underlying scan honors the cancellation.
func withTimeout(next apierr.HandlerFunc) apierr.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		err := next(w, r.WithContext(ctx))
		if errors.Is(err, context.DeadlineExceeded) {
			return apierr.NewAppError(http.StatusServiceUnavailable, "visualization timed out")
		}
		return err
	}
}

// acceptedTimeLayouts lists every timestamp format the `from`/`to` query
// params accept, tried in order. RFC3339 covers programmatic/MCP callers;
// the naked layouts cover the browser's <input type="datetime-local">,
// which emits local wall-clock with no timezone and (usually) no seconds.
// Naked layouts are parsed as UTC — the SPA pre-converts to RFC3339 via
// toISOString() so the timezone stays correct for UI traffic; these are
// the defensive fallback for raw API clients.
var acceptedTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
}

// parseTimestamp parses a `from`/`to` query value against every accepted
// layout. Returns an error if none match (empty string is treated as a
// non-match — callers guard against empty before calling).
func parseTimestamp(raw string) (time.Time, error) {
	for _, layout := range acceptedTimeLayouts {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, errors.New("unrecognized timestamp format")
}

// parseOpts converts the shared `session`/`from`/`to` query string into a
// ScanOpts. Returns a 400 AppError if the time bounds are malformed.
func parseOpts(r *http.Request, allowMultiSession bool) (analytics.ScanOpts, error) {
	q := r.URL.Query()
	opts := analytics.ScanOpts{}

	if raw := strings.TrimSpace(q.Get("session")); raw != "" {
		ids := strings.Split(raw, ",")
		cleaned := make([]string, 0, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" {
				cleaned = append(cleaned, id)
			}
		}
		if !allowMultiSession && len(cleaned) != 1 {
			return opts, apierr.NewAppError(http.StatusBadRequest, "exactly one session id required")
		}
		opts.Sessions = cleaned
	}

	if raw := strings.TrimSpace(q.Get("from")); raw != "" {
		ts, err := parseTimestamp(raw)
		if err != nil {
			return opts, apierr.NewAppError(http.StatusBadRequest, "invalid `from` timestamp")
		}
		opts.From = ts
	}
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		ts, err := parseTimestamp(raw)
		if err != nil {
			return opts, apierr.NewAppError(http.StatusBadRequest, "invalid `to` timestamp")
		}
		opts.To = ts
	}

	if opts.From.IsZero() && opts.To.IsZero() {
		// Default window: last 7 days. Aggregate endpoints honor this;
		// DAG callers should override by passing both bounds explicitly,
		// but the default still keeps the response cheap.
		opts.To = time.Now().UTC()
		opts.From = opts.To.Add(-defaultWindow)
	}
	// Enforce the spec §E session cap. ScanOpts.MaxSessions=0 already
	// falls back to DefaultMaxSessions inside analytics; setting it
	// explicitly keeps the value observable in tests + future tracing.
	opts.MaxSessions = analytics.DefaultMaxSessions
	return opts, nil
}

// Sankey returns the tool-call sankey across the selected session window.
func (h *Handler) Sankey(w http.ResponseWriter, r *http.Request) error {
	opts, err := parseOpts(r, true /* multi-session ok */)
	if err != nil {
		return err
	}
	data, err := analytics.BuildSankey(r.Context(), opts, nil)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, data)
	return nil
}

// DAG returns the chronological session DAG for a single session ID.
func (h *Handler) DAG(w http.ResponseWriter, r *http.Request) error {
	if strings.TrimSpace(r.URL.Query().Get("session")) == "" {
		return apierr.NewAppError(http.StatusBadRequest, "session query parameter is required for dag")
	}
	opts, err := parseOpts(r, false /* single session */)
	if err != nil {
		return err
	}
	data, err := analytics.BuildDAG(r.Context(), opts, nil)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, data)
	return nil
}

// SpawnTree returns the parent → subagent tree for the scan window.
func (h *Handler) SpawnTree(w http.ResponseWriter, r *http.Request) error {
	opts, err := parseOpts(r, true)
	if err != nil {
		return err
	}
	data, err := analytics.BuildSpawnTree(r.Context(), opts, nil)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, data)
	return nil
}

// CoOccurrence returns the symmetric session-count matrix for the window.
func (h *Handler) CoOccurrence(w http.ResponseWriter, r *http.Request) error {
	opts, err := parseOpts(r, true)
	if err != nil {
		return err
	}
	data, err := analytics.BuildCoOccurrence(r.Context(), opts, nil)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, data)
	return nil
}
