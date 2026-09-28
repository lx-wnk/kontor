// Package adapters serves GET /api/adapters (the static adapter catalog). The
// legacy write endpoints (GET/POST /api/adapters/current,
// GET/PUT /api/settings/adapters) have been retired in favor of per-row
// Spawner rows and now respond with HTTP 410 Gone.
package adapters

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/llmadapter"
)

// availableAdapters is a thin shim re-exporting the canonical catalog from
// llmadapter.AvailableAdapters. Both the pipeline dispatch (adapter_factory.go)
// and this HTTP handler must agree on adapter names and required config keys.
var availableAdapters = llmadapter.AvailableAdapters

// retiredEndpointBody is the JSON body returned by all retired adapter routes.
// Stable contract so UI clients can detect the migration uniformly.
var retiredEndpointBody = map[string]string{
	"error":   "endpoint retired",
	"message": "LLM adapters are now configured per Spawner row at /api/spawners",
	"docs":    "docs/architecture/adr/0003-pluggable-spawners.md",
}

// Handler serves /api/adapters. The retired routes are also registered here so
// they return 410 Gone with a stable JSON body instead of 404 — clients that
// still hit them get an explicit migration pointer.
type Handler struct{}

// NewHandler creates a stateless Handler. The catalog is a package-level
// constant; no config is required.
func NewHandler() *Handler {
	return &Handler{}
}

// Mount registers all adapter routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/adapters", apierr.ErrorMiddleware(h.list))

	// Retired endpoints — return 410 Gone with the migration body.
	r.Get("/api/adapters/current", retiredHandler)
	r.Post("/api/adapters/current", retiredHandler)
	r.Get("/api/settings/adapters", retiredHandler)
	r.Put("/api/settings/adapters", retiredHandler)
}

// list returns the static catalogue of available adapters with their config requirements.
func (h *Handler) list(w http.ResponseWriter, _ *http.Request) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(availableAdapters)
}

// retiredHandler responds to every retired adapter endpoint with HTTP 410 Gone
// and the migration body. Intentionally not wrapped in ErrorMiddleware so the
// status code stays explicit.
func retiredHandler(w http.ResponseWriter, _ *http.Request) {
	apierr.WriteJSON(w, http.StatusGone, retiredEndpointBody)
}
