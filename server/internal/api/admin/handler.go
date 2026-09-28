// Package admin serves privileged server-control endpoints (currently restart).
package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/restart"
)

// buildOutputLines is how many trailing lines of build output the 500
// response carries — enough to see the actual compiler error, not the whole
// (possibly huge) log.
const buildOutputLines = 40

// Validator decides whether a restart is safe (e.g. won't lock out auth).
type Validator interface {
	Validate(ctx context.Context) error
}

// Handler serves POST /api/admin/restart. trigger signals the run-loop to
// restart; mode is reported in the 202 body.
type Handler struct {
	validator Validator
	mode      string
	restarter restart.Restarter
	trigger   func()
}

func New(v Validator, mode string, restarter restart.Restarter, trigger func()) *Handler {
	return &Handler{validator: v, mode: mode, restarter: restarter, trigger: trigger}
}

func (h *Handler) Mount(r chi.Router) {
	r.Post("/api/admin/restart", h.restart)
}

// restartRequest is the optional POST body. Absent, malformed, or
// rebuild:false all decode to the zero value and take the pre-rebuild code
// path unchanged — this field is the ONLY thing read from the request.
type restartRequest struct {
	Rebuild bool `json:"rebuild"`
}

func (h *Handler) restart(w http.ResponseWriter, r *http.Request) {
	if err := h.validator.Validate(r.Context()); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	var req restartRequest
	if r.Body != nil {
		// Error (missing/malformed body) is intentionally ignored — it just
		// leaves req.Rebuild at its zero value (false), same as an absent body.
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	if req.Rebuild {
		// req.Rebuild is a bool; nothing else from the request reaches Build,
		// which itself takes no argument derived from the request. That is
		// deliberate — this endpoint must never turn into a way to run an
		// arbitrary command via the HTTP body, query string, or headers.
		output, err := h.restarter.Build(r.Context())
		if err != nil {
			// Fail-safe: return here without calling h.trigger(). The run
			// loop never sees a restart signal, so it never re-execs — the
			// old binary keeps serving.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":  err.Error(),
				"output": lastLines(output, buildOutputLines),
			})
			return
		}
	}

	h.trigger()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "restarting", "mode": h.mode})
}

// lastLines returns the final n lines of build output, trimmed of a trailing
// newline first so a clean log doesn't count a phantom empty last line.
func lastLines(output []byte, n int) string {
	trimmed := strings.TrimRight(string(output), "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
