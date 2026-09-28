// Package grants implements the HTTP surface for capability grants:
// GET/POST /api/grants, DELETE /api/grants/{id}, and GET /api/capabilities
// (the catalogue a grant's capabilityName must name). It mirrors the
// `agent-dashboard grants` CLI (internal/cli/cmd_grants.go) so the dashboard UI can do
// over HTTP what an operator can already do at the terminal — every refusal
// the CLI enforces before writing a grant is re-checked here, because this
// route is a second, independent door into the same write path.
package grants

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler handles /api/grants and /api/capabilities.
type Handler struct {
	grants repo.GrantRepo
	caps   repo.CapabilityRepo
}

// NewHandler creates a Handler.
func NewHandler(grants repo.GrantRepo, caps repo.CapabilityRepo) *Handler {
	return &Handler{grants: grants, caps: caps}
}

// Mount registers all grant and capability-catalogue routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/grants", apierr.ErrorMiddleware(h.list))
	r.Post("/api/grants", apierr.ErrorMiddleware(h.create))
	r.Delete("/api/grants/{id}", apierr.ErrorMiddleware(h.revoke))
	r.Get("/api/capabilities", apierr.ErrorMiddleware(h.listCapabilities))
}

// grantResponse is the API response shape for one grant. ent.Grant's tags
// carry omitempty and snake_case storage names, so encoding the entity
// directly dropped limitCount:0 (unlimited) and contextRef:"" (global scope)
// while every other field answered under its column name instead of camelCase.
type grantResponse struct {
	ID                 string     `json:"id"`
	CapabilityName     string     `json:"capabilityName"`
	ContextKind        string     `json:"contextKind"`
	ContextRef         string     `json:"contextRef"`
	Pattern            string     `json:"pattern"`
	Mode               string     `json:"mode"`
	LimitCount         int        `json:"limitCount"`
	LimitWindowSeconds int        `json:"limitWindowSeconds"`
	ExpiresAt          *time.Time `json:"expiresAt"`
	GrantedBy          string     `json:"grantedBy"`
	GrantedAt          time.Time  `json:"grantedAt"`
	RevokedAt          *time.Time `json:"revokedAt"`
	RevokedBy          string     `json:"revokedBy"`
	Reason             string     `json:"reason"`
	NodeID             string     `json:"nodeId"`
}

func toGrantResponse(g *ent.Grant) grantResponse {
	return grantResponse{
		ID:                 g.ID,
		CapabilityName:     g.CapabilityName,
		ContextKind:        g.ContextKind,
		ContextRef:         g.ContextRef,
		Pattern:            g.Pattern,
		Mode:               g.Mode,
		LimitCount:         g.LimitCount,
		LimitWindowSeconds: g.LimitWindowSeconds,
		ExpiresAt:          g.ExpiresAt,
		GrantedBy:          g.GrantedBy,
		GrantedAt:          g.GrantedAt,
		RevokedAt:          g.RevokedAt,
		RevokedBy:          g.RevokedBy,
		Reason:             g.Reason,
		NodeID:             g.NodeID,
	}
}

func toGrantResponses(rows []*ent.Grant) []grantResponse {
	resp := make([]grantResponse, len(rows))
	for i, g := range rows {
		resp[i] = toGrantResponse(g)
	}
	return resp
}

// capabilityResponse is the API response shape for one catalogue entry —
// camelCased and, unlike ent.Capability's tags, without omitempty so a
// false requiresPattern/reversible still round-trips.
type capabilityResponse struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Class           string   `json:"class"`
	EnforceableBy   []string `json:"enforceableBy"`
	RequiresPattern bool     `json:"requiresPattern"`
	Reversible      bool     `json:"reversible"`
	Description     string   `json:"description"`
}

func toCapabilityResponse(c *ent.Capability) capabilityResponse {
	enforceableBy := c.EnforceableBy
	if enforceableBy == nil {
		enforceableBy = []string{}
	}
	return capabilityResponse{
		ID:              c.ID,
		Name:            c.Name,
		Class:           c.Class,
		EnforceableBy:   enforceableBy,
		RequiresPattern: c.RequiresPattern,
		Reversible:      c.Reversible,
		Description:     c.Description,
	}
}

func toCapabilityResponses(rows []*ent.Capability) []capabilityResponse {
	resp := make([]capabilityResponse, len(rows))
	for i, c := range rows {
		resp[i] = toCapabilityResponse(c)
	}
	return resp
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	capFilter := r.URL.Query().Get("capability")
	var rows []*ent.Grant
	var err error
	if capFilter != "" {
		rows, err = h.grants.ListForCapability(r.Context(), capFilter)
	} else {
		rows, err = h.grants.List(r.Context())
	}
	if err != nil {
		return fmt.Errorf("grants.list: %w", err)
	}
	// ListForCapability orders oldest-first (repo.grant_repo.go); re-sort here
	// so both query paths answer newest-first, matching `agent-dashboard grants list`
	// (cmd_grants.go sorts the same way regardless of which repo method it called).
	sort.Slice(rows, func(i, j int) bool { return rows[i].GrantedAt.After(rows[j].GrantedAt) })
	apierr.WriteJSON(w, http.StatusOK, toGrantResponses(rows))
	return nil
}

func (h *Handler) listCapabilities(w http.ResponseWriter, r *http.Request) error {
	rows, err := h.caps.List(r.Context())
	if err != nil {
		return fmt.Errorf("grants.listCapabilities: %w", err)
	}
	apierr.WriteJSON(w, http.StatusOK, toCapabilityResponses(rows))
	return nil
}

// createGrantRequest is the POST /api/grants body. Pattern is a pointer so a
// caller must send the key explicitly — same reason cmd_grants.go's --pattern
// flag has no usable default: a silently-defaulted pattern would compound
// with a silently-defaulted scope and mode into the widest grant the system
// can express.
type createGrantRequest struct {
	CapabilityName     string  `json:"capabilityName"`
	ContextKind        string  `json:"contextKind"`
	ContextRef         string  `json:"contextRef"`
	Pattern            *string `json:"pattern"`
	Mode               string  `json:"mode"`
	LimitCount         int     `json:"limitCount"`
	LimitWindowSeconds int     `json:"limitWindowSeconds"`
	ExpiresInSeconds   *int    `json:"expiresInSeconds"`
	Reason             string  `json:"reason"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var req createGrantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}

	if req.Pattern == nil {
		return apierr.NewAppError(http.StatusBadRequest,
			`pattern is required; pass "" or "*" to grant every value, or a specific pattern (prefix patterns end in *, e.g. "git status*")`)
	}
	if _, err := capability.ParsePattern(*req.Pattern); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid pattern: "+err.Error())
	}

	// An unknown capability name has no catalogue row, so capability.Decide
	// would resolve it to a zero-value view (empty class) and deny forever —
	// the grant would write successfully and then never take effect.
	if _, err := h.caps.Get(r.Context(), req.CapabilityName); err != nil {
		if repo.IsNotFound(err) {
			return apierr.NewAppError(http.StatusBadRequest,
				fmt.Sprintf("unknown capability %q — see GET /api/capabilities for valid names", req.CapabilityName))
		}
		return fmt.Errorf("grants.create: %w", err)
	}

	if !capability.IsValidContextKind(req.ContextKind) {
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("invalid context kind %q (valid: %s)", req.ContextKind, strings.Join(capability.ContextKinds(), ", ")))
	}
	if req.ContextKind == repo.GrantContextGlobal && req.ContextRef != "" {
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("contextRef must be empty for the global context, got %q", req.ContextRef))
	}
	if req.ContextKind != repo.GrantContextGlobal && req.ContextRef == "" {
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("contextRef is required for context kind %q", req.ContextKind))
	}

	if !capability.IsValidMode(req.Mode) {
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("invalid mode %q (valid: %s)", req.Mode, strings.Join(capability.Modes(), ", ")))
	}

	// A zero window is counted as "usages since now", which is always none, so
	// a limit paired with it never triggers and reads as unlimited.
	if req.LimitCount > 0 && req.LimitWindowSeconds <= 0 {
		return apierr.NewAppError(http.StatusBadRequest,
			"limitWindowSeconds must be greater than 0 when limitCount is set, or the limit never triggers")
	}

	var expiresAt *time.Time
	if req.ExpiresInSeconds != nil {
		if *req.ExpiresInSeconds <= 0 {
			return apierr.NewAppError(http.StatusBadRequest,
				"expiresInSeconds must be a positive number of seconds, or omit it for a grant that never expires")
		}
		t := time.Now().Add(time.Duration(*req.ExpiresInSeconds) * time.Second)
		expiresAt = &t
	}

	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}

	row, err := h.grants.Create(r.Context(), repo.CreateGrantInput{
		CapabilityName:     req.CapabilityName,
		Context:            repo.GrantContextFor(req.ContextKind, req.ContextRef),
		Pattern:            *req.Pattern,
		Mode:               req.Mode,
		LimitCount:         req.LimitCount,
		LimitWindowSeconds: req.LimitWindowSeconds,
		ExpiresAt:          expiresAt,
		GrantedBy:          payload.Sub,
		Reason:             req.Reason,
	})
	if err != nil {
		return fmt.Errorf("grants.create: %w", err)
	}
	apierr.WriteJSON(w, http.StatusCreated, toGrantResponse(row))
	return nil
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")

	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}

	if err := h.grants.Revoke(r.Context(), id, payload.Sub); err != nil {
		if repo.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("grants.revoke: %w", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}
