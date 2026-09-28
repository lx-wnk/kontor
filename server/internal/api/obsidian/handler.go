// Package obsidian implements the HTTP surface of the Obsidian vault.
package obsidian

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	obsidianapp "github.com/lx-wnk/kontor/server/internal/apps/obsidian"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// statusPingTimeout bounds the reachability probe status makes on every
// call — short because it blocks the settings panel's render, not because
// the vault is normally slow.
const statusPingTimeout = 3 * time.Second

// Handler serves the Obsidian HTTP routes registered by Mount.
type Handler struct {
	clients *obsidianapp.ClientHolder
	mem     repo.MemoryRepo
	gate    memory.Gate
	spaceID string
	// running serializes index runs: IndexNotes reads every existing
	// pointer before it writes any (index.go's priorPointers map), with no
	// transaction spanning the two and no unique index on
	// (space_id, source_ref), so two overlapping runs both see "not
	// indexed yet" and both write a pointer for the same note — a
	// permanent duplicate the reconciliation loop can never undo (it only
	// ever tracks one id per path). Rejecting a second run with 409 while
	// one is in flight is the fix at this trigger's scope; a DB-level fix
	// (unique index + transaction) belongs to the apps/obsidian package
	// this task does not own.
	// ponytail: a single in-process flag serializes one server's runs;
	// revisit only if this trigger is ever driven from more than one
	// server process against the same vault.
	running atomic.Bool
	graphs  graphCache

	// graphMu guards graphClient, the client the cached graph was last built
	// from. ClientHolder.Set can swap in a different vault at any time (a live
	// settings save); without this check a request right after a swap would
	// still serve the previous vault's graph until graphTTL passed.
	graphMu     sync.Mutex
	graphClient *obsidianapp.Client
}

// NewHandler creates a Handler. clients holds nil while the vault is
// unconfigured (see serverapp.buildObsidianClient's own doc comment) — index
// then answers 503 rather than reaching a nil client, the same "optional
// integration, never a boot failure" rule that function follows.
func NewHandler(clients *obsidianapp.ClientHolder, mem repo.MemoryRepo, gate memory.Gate, spaceID string) *Handler {
	return &Handler{clients: clients, mem: mem, gate: gate, spaceID: spaceID, graphs: graphCache{now: time.Now}}
}

// Mount registers the /api/obsidian/* routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/api/obsidian/index", apierr.ErrorMiddleware(h.index))
	r.Get("/api/obsidian/graph", apierr.ErrorMiddleware(h.graph))
	r.Post("/api/obsidian/open", apierr.ErrorMiddleware(h.open))
	r.Get("/api/obsidian/status", apierr.ErrorMiddleware(h.status))
}

// status reports whether a vault client is configured and, if so, whether it
// is actually reachable — no vault content crosses this handler, so unlike
// index/graph/open it needs no capability check. The reachability probe is a
// plain Client.Ping, never routed through h.gate: a gate failure here would
// misreport a working vault as unreachable for a reason that has nothing to
// do with the network.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) error {
	client := h.clients.Get()
	if client == nil {
		apierr.WriteJSON(w, http.StatusOK, map[string]bool{"configured": false})
		return nil
	}

	ctx, cancel := context.WithTimeout(r.Context(), statusPingTimeout)
	defer cancel()

	resp := map[string]any{"configured": true, "reachable": true}
	if err := client.Ping(ctx); err != nil {
		slog.Warn("obsidian status ping failed", "err", err)
		cause, hint := classifyPingError(err)
		resp["reachable"] = false
		resp["error"] = cause
		resp["hint"] = hint
	}
	apierr.WriteJSON(w, http.StatusOK, resp)
	return nil
}

// classifyPingError maps a Client.Ping failure to a short cause and an
// actionable hint. It never returns err.Error() verbatim: that text can
// carry the vault's URL, the same leak upstreamGraph guards against.
func classifyPingError(err error) (cause, hint string) {
	var uaErr x509.UnknownAuthorityError
	switch {
	case errors.As(err, &uaErr):
		return "certificate not trusted", `The vault uses a self-signed certificate: set TLS mode to "insecure-loopback" (127.0.0.1 only) or "pinned".`
	case errors.Is(err, obsidianapp.ErrUnauthorized):
		return "unauthorized", "Check the API key."
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection refused", "Is Obsidian running with the Local REST API plugin enabled?"
	default:
		return "vault unreachable", "Check that Obsidian is running and the settings above are correct."
	}
}

// invalidateGraphCacheOnSwap drops the cached graph when client differs from
// the one it was last built from.
func (h *Handler) invalidateGraphCacheOnSwap(client *obsidianapp.Client) {
	h.graphMu.Lock()
	defer h.graphMu.Unlock()
	if h.graphClient != client {
		h.graphClient = client
		h.graphs.reset()
	}
}

// index runs one obsidianapp.IndexNotes pass and reports how many new
// pointer entries it created. Only one run is allowed in flight at a time
// (h.running) — a second POST while one is running gets 409, not a race
// against the first (see h.running's own doc comment for why that race is
// dangerous: permanent duplicate pointers, not just a wasted request).
//
// A human presses the button that reaches this handler, but the run behind
// it is unattended with respect to capabilities: h.gate is built with no
// Asker (see its construction in serverapp/di.go), so a capability that
// would otherwise pause for a human's decision denies instead — the click
// only starts the run, it is not present to answer for it. IndexNotes'
// three internal capability checks can therefore fail in two different ways
// depending on whether the capability class defaults to deny or ask
// (capability.Decide's defaultEffect); both capability.ErrDenied and
// capability.ErrAskRequired mean the same thing to this handler's caller —
// forbidden — so both map to 403, never the default 500 ErrorMiddleware
// would otherwise give an unrecognised error.
func (h *Handler) index(w http.ResponseWriter, r *http.Request) error {
	client := h.clients.Get()
	if client == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "obsidian vault not configured")
	}
	if !h.running.CompareAndSwap(false, true) {
		return apierr.NewAppError(http.StatusConflict, "an obsidian index run is already in progress")
	}
	defer h.running.Store(false)

	count, err := obsidianapp.IndexNotes(r.Context(), client, h.mem, h.gate, h.spaceID)
	if err != nil {
		if errors.Is(err, capability.ErrDenied) || errors.Is(err, capability.ErrAskRequired) {
			return apierr.NewAppError(http.StatusForbidden, err.Error())
		}
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]int{"indexed": count})
	return nil
}

func (h *Handler) authorizeRead(r *http.Request) error {
	err := h.gate.Authorize(r.Context(), repo.CapabilityMemoryRead, "", repo.GlobalScope())
	if errors.Is(err, capability.ErrDenied) || errors.Is(err, capability.ErrAskRequired) {
		return apierr.NewAppError(http.StatusForbidden, err.Error())
	}
	return err
}

// upstreamGraph maps an upstream failure to 502 without its text, which can carry the vault URL.
func upstreamGraph(g obsidianapp.Graph, err error) (obsidianapp.Graph, error) {
	if err != nil {
		slog.Warn("obsidian graph rebuild failed", "err", err)
		return obsidianapp.Graph{}, apierr.NewAppError(http.StatusBadGateway, "obsidian graph unavailable")
	}
	return g, nil
}

func (h *Handler) graph(w http.ResponseWriter, r *http.Request) error {
	client := h.clients.Get()
	if client == nil {
		apierr.WriteJSON(w, http.StatusOK, map[string]bool{"configured": false})
		return nil
	}
	if err := h.authorizeRead(r); err != nil {
		return err
	}
	h.invalidateGraphCacheOnSwap(client)
	g, err := upstreamGraph(h.graphs.get(r.Context(), client.Graph))
	if err != nil {
		return err
	}
	notes := make([][2]any, len(g.Notes))
	for i, n := range g.Notes {
		notes[i] = [2]any{n.Path, n.MtimeMs}
	}
	links := g.Links
	if links == nil {
		links = [][2]int{}
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]any{"configured": true, "notes": notes, "links": links})
	return nil
}

const maxOpenBodyBytes = 4 << 10

// open passes on only a note a freshly built graph lists, because Obsidian's /open creates a missing note.
func (h *Handler) open(w http.ResponseWriter, r *http.Request) error {
	client := h.clients.Get()
	if client == nil {
		return apierr.NewAppError(http.StatusServiceUnavailable, "obsidian vault not configured")
	}
	var body struct {
		Path string `json:"path"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxOpenBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid request body")
	}
	// Obsidian reads # as a heading subpath and would open, or create, a different note.
	if strings.Contains(body.Path, "#") {
		return apierr.NewAppError(http.StatusBadRequest, "note path must not contain #")
	}
	if err := h.authorizeRead(r); err != nil {
		return err
	}
	h.invalidateGraphCacheOnSwap(client)
	g, err := upstreamGraph(h.graphs.refresh(r.Context(), client.Graph))
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(g.Notes, func(n obsidianapp.GraphNote) bool { return n.Path == body.Path }) {
		return apierr.NewAppError(http.StatusNotFound, "note is not in the vault graph")
	}
	if err := client.OpenNote(r.Context(), body.Path); err != nil {
		slog.Warn("obsidian open failed", "err", err)
		return apierr.NewAppError(http.StatusBadGateway, "obsidian open failed")
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
