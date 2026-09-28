// Package resources implements the read-only HTTP surface over the ARMS
// resource registry: GET /api/resources. The registry is the identity table
// behind applications, routines, skills and memory spaces; until this route,
// GET /api/memory/spaces was the only window onto it and it is hard-filtered
// to kind = memory_space.
package resources

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mem "github.com/lx-wnk/kontor/server/internal/memory"
)

// Handler serves the resource registry read view. Read-only by construction:
// state transitions and deletion stay with the subsystem that owns the
// resource (the plugin lifecycle handler, the memory handler), so this route
// cannot become a second, unvalidated write path into the same table.
type Handler struct {
	resources  repo.ResourceRepo
	schedules  repo.TaskScheduleRepo
	gate       mem.Gate
	bypassAuth bool
}

// NewHandler creates a Handler. schedules may be nil, in which case
// kind=routine answers an empty list. bypassAuth is the loopback single-user
// mode, in which the routine listing is not narrowed to the caller — the same
// flag and the same meaning as api/schedules.NewHandler's.
func NewHandler(r repo.ResourceRepo, schedules repo.TaskScheduleRepo, gate mem.Gate, bypassAuth bool) *Handler {
	return &Handler{resources: r, schedules: schedules, gate: gate, bypassAuth: bypassAuth}
}

// Mount registers the resource registry routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/resources", apierr.ErrorMiddleware(h.list))
}

// resourceKinds is the set a ?kind= may name, ordered as the UI lists them.
// Enumerated here rather than derived from the table's distinct values so a
// typo is rejected as unknown even when the registry is empty.
var resourceKinds = []string{
	repo.ResourceKindApplication,
	repo.ResourceKindRoutine,
	repo.ResourceKindSkill,
	repo.ResourceKindMemorySpace,
}

func isValidKind(kind string) bool {
	for _, k := range resourceKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// resourceView is the camelCase JSON shape of a registry row. Hand-written
// rather than encoding *ent.Resource directly: ent's generated struct tags are
// snake_case, and a raw entity on the wire also republishes every column added
// to the schema later, whether or not the client should see it.
type resourceView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	ScopeKind string    `json:"scopeKind"`
	ScopeRef  string    `json:"scopeRef"`
	NodeID    string    `json:"nodeId"`
	State     string    `json:"state"`
	Version   string    `json:"version"`
	Origin    string    `json:"origin"`
	OriginRef string    `json:"originRef"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func viewOf(row *ent.Resource) resourceView {
	return resourceView{
		ID:        row.ID,
		Kind:      row.Kind,
		Slug:      row.Slug,
		Name:      row.Name,
		ScopeKind: row.ScopeKind,
		ScopeRef:  row.ScopeRef,
		NodeID:    row.NodeID,
		State:     row.State,
		Version:   row.Version,
		Origin:    row.Origin,
		OriginRef: row.OriginRef,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

// routineResources returns persisted resource rows for routines owned by the
// calling user. Per-user narrowing is preserved: the resource table has no
// user column, so identity comes from the schedule table. The caller's
// schedules are resolved first, then only resource rows whose OriginRef
// matches one of those schedule IDs are returned.
func (h *Handler) routineResources(r *http.Request) ([]resourceView, error) {
	if h.schedules == nil {
		return []resourceView{}, nil
	}
	payload, _ := auth.PayloadFromContext(r.Context())
	schedules, err := h.schedules.ListForUser(r.Context(), payload.Sub, h.bypassAuth)
	if err != nil {
		return nil, fmt.Errorf("resources.list routines: %w", err)
	}

	scheduleIDs := make(map[string]struct{}, len(schedules))
	for _, s := range schedules {
		scheduleIDs[s.ID] = struct{}{}
	}

	rows, err := h.resources.ListForKind(r.Context(), repo.ResourceKindRoutine)
	if err != nil {
		return nil, fmt.Errorf("resources.list routines: %w", err)
	}

	out := make([]resourceView, 0, len(scheduleIDs))
	for _, row := range rows {
		if _, ok := scheduleIDs[row.OriginRef]; ok {
			out = append(out, viewOf(row))
		}
	}
	return out, nil
}

// scopeFromQuery parses the shared "scope"/"scopeRef" query params through
// mem.ParseScope — the one parser every transport that accepts a caller-
// supplied scope uses (MCP tool args, the memory HTTP routes), so the accepted
// set cannot drift between them.
func scopeFromQuery(q url.Values) (repo.Scope, error) {
	scope, err := mem.ParseScope(q.Get("scope"), q.Get("scopeRef"))
	if err != nil {
		return repo.Scope{}, apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	return scope, nil
}

// list answers GET /api/resources?kind=&scope=&scopeRef=.
func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()

	kind := q.Get("kind")
	if kind == "" {
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("kind is required (valid: %s)", strings.Join(resourceKinds, ", ")))
	}
	if !isValidKind(kind) {
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("unknown kind %q (valid: %s)", kind, strings.Join(resourceKinds, ", ")))
	}

	scope, err := scopeFromQuery(q)
	if err != nil {
		return err
	}

	// Only memory_space is gated, and deliberately so. Its rows are the
	// field-for-field same registry rows GET /api/memory/spaces answers, and
	// that route requires memory.read (memory/handler.go, listSpaces) — so
	// ungated here this route is simply a way around that gate, and
	// ?scope=project&scopeRef=<path> additionally confirms which filesystem
	// paths hold memory spaces, since ListMerged always returns the global
	// rows and any extra row names the path's own. Same capability, same
	// empty value, same requested scope as listSpaces, so the two cannot
	// drift apart again. The other kinds are not gated: application rows are
	// already public via /api/plugins, routine rows are narrowed to the
	// calling user's schedules (routineResources filters by OriginRef),
	// and nothing writes skill yet. Do not "simplify" this into gating
	// everything or nothing.
	if kind == repo.ResourceKindMemorySpace {
		if err := h.gate.Authorize(r.Context(), repo.CapabilityMemoryRead, "", scope); err != nil {
			return apierr.NewAppError(http.StatusForbidden, err.Error())
		}
	}

	if kind == repo.ResourceKindRoutine {
		out, err := h.routineResources(r)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(out)
	}

	// ListMerged, not ListForScope: the UI wants the effective set a caller in
	// this scope would resolve — the global rows plus the scope's own, the
	// scope winning a slug collision — which is the same rule
	// ResourceRepo.Resolve applies to a single slug. This deliberately
	// disagrees with GET /api/memory/spaces (MemoryRepo.ListSpaces calls
	// ListForScope, the scope's own rows only): that route answers "what is
	// defined here", this one answers "what resolves here". Do not "fix" one
	// to match the other.
	rows, err := h.resources.ListMerged(r.Context(), kind, scope)
	if err != nil {
		return fmt.Errorf("resources.list: %w", err)
	}

	// make(..., 0, n) rather than a nil slice: a nil encodes as null, and a
	// kind with no writer yet (routine, skill) would then reach the client as
	// null on every request.
	out := make([]resourceView, 0, len(rows))
	for _, row := range rows {
		out = append(out, viewOf(row))
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}
