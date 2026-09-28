// Package history implements the /api/history import endpoints.
package history

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/auth"
	histsvc "github.com/lx-wnk/kontor/server/internal/history"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// client is a registered SSE consumer for a user's import progress.
type client struct {
	id   uint64
	send chan histsvc.ImportProgress
}

// Handler handles history import routes with per-user job tracking.
type Handler struct {
	importer *histsvc.Importer

	mu          sync.Mutex
	nextID      uint64
	currentJobs map[string]*histsvc.ImportProgress // userID -> latest progress
	jobClients  map[string][]*client               // userID -> connected SSE clients
}

// NewHandler creates a Handler backed by imp.
func NewHandler(imp *histsvc.Importer) *Handler {
	return &Handler{
		importer:    imp,
		currentJobs: make(map[string]*histsvc.ImportProgress),
		jobClients:  make(map[string][]*client),
	}
}

// Mount registers the history import routes on r.
// All routes require JWT auth — mount inside a protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Post("/api/history/import", h.startImport)
	r.Get("/api/history/import/status", h.streamStatus)
}

// POST /api/history/import — start an import job.
func (h *Handler) startImport(w http.ResponseWriter, r *http.Request) {
	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	userID := payload.Sub

	// Check if an import is already running for this user.
	h.mu.Lock()
	existing, has := h.currentJobs[userID]
	if has && !existing.Done {
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "import already in progress",
		})
		return
	}
	// Initialise a fresh progress record.
	initial := &histsvc.ImportProgress{}
	h.currentJobs[userID] = initial
	h.mu.Unlock()

	onProgress := func(p histsvc.ImportProgress) {
		h.mu.Lock()
		cp := p
		h.currentJobs[userID] = &cp

		// Copy current client list to notify outside the lock.
		cls := make([]*client, len(h.jobClients[userID]))
		copy(cls, h.jobClients[userID])
		if p.Done {
			// Remove all clients; each will drain its channel after we send.
			h.jobClients[userID] = nil
		}
		h.mu.Unlock()

		for _, c := range cls {
			select {
			case c.send <- p:
			default:
				// Slow client — drop the event to avoid blocking the importer.
			}
		}
	}

	// Use WithoutCancel so the import goroutine inherits request values but is
	// not aborted when the HTTP response returns. context.Background() was used
	// previously and would ignore server-shutdown signals.
	if err := h.importer.Run(context.WithoutCancel(r.Context()), onProgress); err != nil {
		// Run returns an error only when already running globally (single-instance
		// guard) — e.g. a scheduled background scan currently holds the lock. Drop
		// the per-user record we optimistically created above; otherwise it stays
		// {Done:false} forever and permanently blocks this user's later imports at
		// the per-user check above (the scheduled scan never touches this map).
		h.mu.Lock()
		delete(h.currentJobs, userID)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":      true,
		"message": "Import started — stream progress at GET /api/history/import/status",
	})
}

// GET /api/history/import/status — SSE stream of ImportProgress events.
func (h *Handler) streamStatus(w http.ResponseWriter, r *http.Request) {
	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}
	userID := payload.Sub

	sse.WriteHeaders(w)

	flusher, canFlush := w.(http.Flusher)

	sendEvent := func(p histsvc.ImportProgress) {
		data, err := json.Marshal(p)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if canFlush {
			flusher.Flush()
		}
	}

	// Register this SSE client before checking current state to avoid a race.
	h.mu.Lock()
	h.nextID++
	cl := &client{id: h.nextID, send: make(chan histsvc.ImportProgress, 16)}
	h.jobClients[userID] = append(h.jobClients[userID], cl)
	current := h.currentJobs[userID]
	h.mu.Unlock()

	// Deregister client on disconnect.
	defer func() {
		h.mu.Lock()
		cbs := h.jobClients[userID]
		out := cbs[:0]
		for _, c := range cbs {
			if c.id != cl.id {
				out = append(out, c)
			}
		}
		h.jobClients[userID] = out
		h.mu.Unlock()
	}()

	// Send current state immediately if a job exists.
	if current != nil {
		sendEvent(*current)
		if current.Done {
			return
		}
	}

	// Stream future updates until done or client disconnects.
	for {
		select {
		case <-r.Context().Done():
			return
		case p, ok := <-cl.send:
			if !ok {
				return
			}
			sendEvent(p)
			if p.Done {
				return
			}
		}
	}
}
