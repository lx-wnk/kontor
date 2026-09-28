// Package capabilities implements the browser-facing endpoint that lets a
// human answer a pending capability decision raised by serverask.Asker.
package capabilities

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/askgate"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/serverask"
)

// maxRespondBodyBytes mirrors hooks.maxPermissionBodyBytes, which answers the
// same shape of request but is unexported to that package.
const maxRespondBodyBytes = 64 * 1024

// Resolver is the one method Handler needs from *serverask.Asker.
type Resolver interface {
	Resolve(id, decision string) (serverask.Pending, error)
}

// Recorder is the one method Handler needs from repo.AuditEventRepo.
type Recorder interface {
	RecordAudit(ctx context.Context, userID *string, action, target string, metadata map[string]any) error
}

// Handler answers POST /api/capabilities/decisions/respond.
type Handler struct {
	resolver Resolver
	audit    Recorder
}

// New returns a Handler that delivers decisions through resolver and records
// them through audit. A nil audit disables recording, which is what a router
// built without an audit repo gets.
func New(resolver Resolver, audit Recorder) *Handler {
	return &Handler{resolver: resolver, audit: audit}
}

// Respond handles POST /api/capabilities/decisions/respond — the browser.
//
// Authenticated by the session-auth group in router.go, the same split
// hooks.PermissionRespond uses.
func (h *Handler) Respond(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string `json:"id"`
		Decision string `json:"decision"` // "allow" | "deny"
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRespondBodyBytes)).Decode(&body); err != nil {
		apierr.JSONError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Decision != "allow" && body.Decision != "deny" {
		apierr.JSONError(w, http.StatusBadRequest, `decision must be "allow" or "deny"`)
		return
	}
	pending, err := h.resolver.Resolve(body.ID, body.Decision)
	switch {
	case err == nil:
		h.record(r, body.Decision, pending)
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, askgate.ErrNotPending):
		apierr.JSONError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, serverask.ErrInvalidDecision):
		apierr.JSONError(w, http.StatusBadRequest, err.Error())
	default:
		apierr.JSONError(w, http.StatusInternalServerError, "internal error")
	}
}

// record writes the audit trail for a human turning a blocked call into a
// permitted one. The decision has already taken effect by the time this runs,
// so a failed write is logged rather than surfaced — refusing the response
// would tell the caller nothing happened when it did.
func (h *Handler) record(r *http.Request, decision string, p serverask.Pending) {
	if h.audit == nil {
		return
	}
	action := repo.AuditActionCapabilityAllow
	if decision == "deny" {
		action = repo.AuditActionCapabilityDeny
	}
	var userID *string
	if payload, ok := auth.PayloadFromContext(r.Context()); ok && payload.Sub != "" {
		sub := payload.Sub
		userID = &sub
	}
	// The browser may already be gone; the record is about what was decided,
	// not about whether anyone is still listening.
	ctx := context.WithoutCancel(r.Context())
	if err := h.audit.RecordAudit(ctx, userID, action, p.Capability, map[string]any{
		"value":   p.Value,
		"context": p.Context,
		"reason":  p.Reason,
	}); err != nil {
		slog.Warn("capabilities.Respond: audit write failed",
			"capability", p.Capability, "decision", decision, "err", err)
	}
}
