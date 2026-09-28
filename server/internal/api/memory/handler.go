// Package memory implements the HTTP surface over the system memory store:
// spaces, entries, and the record of what got injected into a spawn.
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mem "github.com/lx-wnk/kontor/server/internal/memory"
)

// Handler serves the /api/memory/* endpoints. Row scoping (which spaces and
// entries a given scope may see) is carried by the repo and Retriever
// queries this handler calls, the same per-query-not-middleware pattern
// rawrepo.SearchRepo uses for user_id — there is no separate filter layer
// here to keep in sync with it.
type Handler struct {
	repo      repo.MemoryRepo
	retriever *mem.Retriever
	gate      mem.Gate
}

// NewHandler creates a Handler.
func NewHandler(r repo.MemoryRepo, retriever *mem.Retriever, gate mem.Gate) *Handler {
	return &Handler{repo: r, retriever: retriever, gate: gate}
}

// The four response shapes below exist because every payload on these routes is
// an ent entity or an untagged Go struct: ent tags the storage column names in
// snake_case with omitempty, and memory.Entry carries no json tags at all, so
// today the search route answers PascalCase Go field names.

// memorySpaceResponse is a memory space — a resource-registry row of kind
// memory_space.
type memorySpaceResponse struct {
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

func toMemorySpaceResponse(r *ent.Resource) memorySpaceResponse {
	return memorySpaceResponse{
		ID:        r.ID,
		Kind:      r.Kind,
		Slug:      r.Slug,
		Name:      r.Name,
		ScopeKind: r.ScopeKind,
		ScopeRef:  r.ScopeRef,
		NodeID:    r.NodeID,
		State:     r.State,
		Version:   r.Version,
		Origin:    r.Origin,
		OriginRef: r.OriginRef,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
	}
}

// memoryEntryResponse is a stored memory entry, the full row.
type memoryEntryResponse struct {
	ID           string     `json:"id"`
	SpaceID      string     `json:"spaceId"`
	Summary      string     `json:"summary"`
	Content      string     `json:"content"`
	Kind         string     `json:"kind"`
	SourceKind   string     `json:"sourceKind"`
	SourceRef    *string    `json:"sourceRef"`
	Confidence   float64    `json:"confidence"`
	ValidFrom    time.Time  `json:"validFrom"`
	ValidUntil   *time.Time `json:"validUntil"`
	SupersededBy *string    `json:"supersededBy"`
	UserID       *string    `json:"userId"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

func toMemoryEntryResponse(e *ent.MemoryEntry) memoryEntryResponse {
	return memoryEntryResponse{
		ID:           e.ID,
		SpaceID:      e.SpaceID,
		Summary:      e.Summary,
		Content:      e.Content,
		Kind:         e.Kind,
		SourceKind:   e.SourceKind,
		SourceRef:    e.SourceRef,
		Confidence:   e.Confidence,
		ValidFrom:    e.ValidFrom,
		ValidUntil:   e.ValidUntil,
		SupersededBy: e.SupersededBy,
		UserID:       e.UserID,
		CreatedAt:    e.CreatedAt,
		UpdatedAt:    e.UpdatedAt,
	}
}

// memorySearchHitResponse is a retrieval result. It is a narrower shape than
// memoryEntryResponse on purpose: mem.Entry is the projection the retriever
// resolves, and padding it out would invent values it never loaded.
type memorySearchHitResponse struct {
	ID         string    `json:"id"`
	SpaceID    string    `json:"spaceId"`
	Summary    string    `json:"summary"`
	Content    string    `json:"content"`
	Kind       string    `json:"kind"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"createdAt"`
}

func toMemorySearchHitResponses(entries []mem.Entry) []memorySearchHitResponse {
	resp := make([]memorySearchHitResponse, len(entries))
	for i, e := range entries {
		resp[i] = memorySearchHitResponse{
			ID:         e.ID,
			SpaceID:    e.SpaceID,
			Summary:    e.Summary,
			Content:    e.Content,
			Kind:       e.Kind,
			Confidence: e.Confidence,
			CreatedAt:  e.CreatedAt,
		}
	}
	return resp
}

// toMemorySearchHitResponsesFromEntries maps ent.MemoryEntry rows (a browse
// listing) into the same memorySearchHitResponse shape the search branch
// answers, so GET /api/memory/entries keeps exactly one response shape
// regardless of which path served the request.
func toMemorySearchHitResponsesFromEntries(entries []*ent.MemoryEntry) []memorySearchHitResponse {
	resp := make([]memorySearchHitResponse, len(entries))
	for i, e := range entries {
		resp[i] = memorySearchHitResponse{
			ID:         e.ID,
			SpaceID:    e.SpaceID,
			Summary:    e.Summary,
			Content:    e.Content,
			Kind:       e.Kind,
			Confidence: e.Confidence,
			CreatedAt:  e.CreatedAt,
		}
	}
	return resp
}

// memoryInjectionResponse is the record of what was pushed into one spawn.
type memoryInjectionResponse struct {
	ID             string    `json:"id"`
	StageRunID     string    `json:"stageRunId"`
	EntryIDs       []string  `json:"entryIds"`
	CharBudget     int       `json:"charBudget"`
	CharsUsed      int       `json:"charsUsed"`
	CandidateCount int       `json:"candidateCount"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

func toMemoryInjectionResponses(injections []*ent.MemoryInjection) []memoryInjectionResponse {
	resp := make([]memoryInjectionResponse, len(injections))
	for i, in := range injections {
		resp[i] = memoryInjectionResponse{
			ID:             in.ID,
			StageRunID:     in.StageRunID,
			EntryIDs:       in.EntryIds,
			CharBudget:     in.CharBudget,
			CharsUsed:      in.CharsUsed,
			CandidateCount: in.CandidateCount,
			CreatedAt:      in.CreatedAt,
			UpdatedAt:      in.UpdatedAt,
		}
	}
	return resp
}

// Mount registers all /api/memory/* routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/memory/spaces", apierr.ErrorMiddleware(h.listSpaces))
	r.Post("/api/memory/spaces", apierr.ErrorMiddleware(h.createSpace))
	r.Get("/api/memory/entries", apierr.ErrorMiddleware(h.searchEntries))
	r.Post("/api/memory/entries", apierr.ErrorMiddleware(h.createEntry))
	r.Patch("/api/memory/entries/{id}", apierr.ErrorMiddleware(h.supersedeEntry))
	r.Delete("/api/memory/entries/{id}", apierr.ErrorMiddleware(h.expireEntry))
	r.Get("/api/memory/injections", apierr.ErrorMiddleware(h.listInjections))
}

// scopeFromQuery parses the "scope"/"scopeRef" query params shared by every
// GET route, via mem.ParseScope — the same fail-closed core the MCP memory
// tools parse their own scope/scopeRef arguments with.
func scopeFromQuery(q url.Values) (repo.Scope, error) {
	scope, err := mem.ParseScope(q.Get("scope"), q.Get("scopeRef"))
	if err != nil {
		return repo.Scope{}, apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	return scope, nil
}

// authorize gates capName against value in scope via h.gate, the same
// capability check the MCP memory tools enforce for the same actions — an
// HTTP caller gets no wider access than an MCP one.
func (h *Handler) authorize(ctx context.Context, capName, value string, scope repo.Scope) error {
	if err := h.gate.Authorize(ctx, capName, value, scope); err != nil {
		return apierr.NewAppError(http.StatusForbidden, err.Error())
	}
	return nil
}

func (h *Handler) listSpaces(w http.ResponseWriter, r *http.Request) error {
	scope, err := scopeFromQuery(r.URL.Query())
	if err != nil {
		return err
	}
	// Read access is granted per scope, not per space — listing fans out
	// across every space visible in scope, so there is no single space
	// identity to match a grant pattern against. Mirrors memory_search.
	if err := h.authorize(r.Context(), repo.CapabilityMemoryRead, "", scope); err != nil {
		return err
	}
	spaces, err := h.repo.ListSpaces(r.Context(), scope)
	if err != nil {
		return err
	}
	resp := make([]memorySpaceResponse, len(spaces))
	for i, s := range spaces {
		resp[i] = toMemorySpaceResponse(s)
	}
	apierr.WriteJSON(w, http.StatusOK, resp)
	return nil
}

type createSpaceBody struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Scope    string `json:"scope"`
	ScopeRef string `json:"scopeRef"`
}

func (h *Handler) createSpace(w http.ResponseWriter, r *http.Request) error {
	var in createSpaceBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if in.Slug == "" {
		return apierr.NewAppError(http.StatusBadRequest, "slug is required")
	}
	scope, err := mem.ParseScope(in.Scope, in.ScopeRef)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	if err := h.authorize(r.Context(), repo.CapabilityMemoryWrite, in.Slug, scope); err != nil {
		return err
	}

	name := in.Name
	if name == "" {
		name = in.Slug
	}
	space, err := h.repo.CreateSpace(r.Context(), repo.CreateSpaceInput{Slug: in.Slug, Name: name, Scope: scope})
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	apierr.WriteJSON(w, http.StatusCreated, toMemorySpaceResponse(space))
	return nil
}

func (h *Handler) searchEntries(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	scope, err := scopeFromQuery(q)
	if err != nil {
		return err
	}
	if err := h.authorize(r.Context(), repo.CapabilityMemoryRead, "", scope); err != nil {
		return err
	}

	if spaceID := q.Get("spaceId"); spaceID != "" {
		return h.browseSpace(w, r, spaceID, scope)
	}

	// An unparseable or absent limit is ignored rather than rejected — same
	// as GET /api/search — and Retriever.Retrieve clamps whatever comes
	// through (<=0 to its default, above its ceiling down to it), so a zero,
	// negative or enormous limit can never reach an unbounded query.
	limit := 0
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			limit = n
		}
	}
	entries, err := h.retriever.Retrieve(r.Context(), mem.Query{Text: q.Get("q"), Scope: scope, Limit: limit})
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, toMemorySearchHitResponses(entries))
	return nil
}

// browseSpace answers GET /api/memory/entries?spaceId= — a plain listing of
// spaceID's valid entries, for the case an empty query text must not search:
// retrieve.go returns nothing for one by design, since a blank query must
// never dump the whole store into a prompt. Authorize has already run in
// searchEntries before this is reached, on the same empty value and scope
// the search path checks — resolving the space first would let scope answer
// as a 404-vs-403 oracle for an ungranted caller (see createEntry's own note
// on this).
//
// scope is the caller's requested scope, already authorized against; it is
// compared against the resolved space's own scope, and a mismatch answers
// 404 rather than the entries — a grant in one scope must not read a
// different scope's space by id. A missing space id answers the identical
// 404, so the two cases stay indistinguishable to the caller.
func (h *Handler) browseSpace(w http.ResponseWriter, r *http.Request, spaceID string, scope repo.Scope) error {
	space, err := h.repo.GetSpaceByID(r.Context(), spaceID)
	if err != nil {
		return apierr.NewAppError(http.StatusNotFound, "unknown space "+spaceID)
	}
	spaceScope := repo.Scope{Kind: repo.ScopeKind(space.ScopeKind), Ref: space.ScopeRef}.Normalize()
	if spaceScope != scope {
		return apierr.NewAppError(http.StatusNotFound, "unknown space "+spaceID)
	}

	entries, err := h.repo.ListValid(r.Context(), spaceID, time.Now())
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, toMemorySearchHitResponsesFromEntries(entries))
	return nil
}

type createEntryBody struct {
	SpaceSlug  string   `json:"spaceSlug"`
	Scope      string   `json:"scope"`
	ScopeRef   string   `json:"scopeRef"`
	Summary    string   `json:"summary"`
	Content    string   `json:"content"`
	Kind       string   `json:"kind"`
	SourceKind string   `json:"sourceKind"`
	SourceRef  string   `json:"sourceRef"`
	Confidence *float64 `json:"confidence"`
}

func (h *Handler) createEntry(w http.ResponseWriter, r *http.Request) error {
	var in createEntryBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if in.SpaceSlug == "" || in.Summary == "" || in.Content == "" || in.Kind == "" || in.SourceKind == "" {
		return apierr.NewAppError(http.StatusBadRequest, "spaceSlug, summary, content, kind and sourceKind are required")
	}
	scope, err := mem.ParseScope(in.Scope, in.ScopeRef)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}

	// Authorize before resolving the space: resolving first would let an
	// ungranted caller distinguish "unknown space" (404) from "denied"
	// (403) without ever holding a grant — a space-existence oracle ahead
	// of the gate. in.SpaceSlug is caller-supplied, so unlike
	// supersedeEntry/expireEntry (which must look an entry's space up
	// before they even know which slug to check), the value needed to
	// authorize is available before the space is resolved at all.
	if err := h.authorize(r.Context(), repo.CapabilityMemoryWrite, in.SpaceSlug, scope); err != nil {
		return err
	}

	// The space must already exist — this never creates one on the fly, the
	// same rule memory_write enforces: auto-creating here would let any
	// caller invent an arbitrary space identity no grant was ever written
	// against.
	space, err := h.repo.GetSpace(r.Context(), scope, in.SpaceSlug)
	if err != nil {
		return apierr.NewAppError(http.StatusNotFound, "unknown space "+in.SpaceSlug)
	}

	cleanSummary, cleanContent, err := mem.SanitizeForStore(in.Summary, in.Content)
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}

	confidence := 1.0
	if in.Confidence != nil {
		confidence = *in.Confidence
	}
	var sourceRef *string
	if in.SourceRef != "" {
		sourceRef = &in.SourceRef
	}

	entry, err := h.repo.CreateEntry(r.Context(), repo.CreateEntryInput{
		SpaceID:    space.ID,
		Summary:    cleanSummary,
		Content:    cleanContent,
		Kind:       in.Kind,
		SourceKind: in.SourceKind,
		SourceRef:  sourceRef,
		Confidence: confidence,
	})
	if err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	apierr.WriteJSON(w, http.StatusCreated, toMemoryEntryResponse(entry))
	return nil
}

// spaceOfEntry resolves the space owning entry id, for authorizing a
// mutation addressed by entry id alone (the path carries no scope or slug).
// Fails closed (404, not 500) on an id that does not resolve to a live entry
// or whose space has since gone missing — indistinguishable from "not found"
// to the caller either way.
func (h *Handler) spaceOfEntry(ctx context.Context, id string) (*ent.Resource, error) {
	entry, err := h.repo.GetEntry(ctx, id)
	if err != nil {
		return nil, apierr.NewAppError(http.StatusNotFound, "entry not found")
	}
	space, err := h.repo.GetSpaceByID(ctx, entry.SpaceID)
	if err != nil {
		return nil, apierr.NewAppError(http.StatusNotFound, "entry not found")
	}
	return space, nil
}

type supersedeBody struct {
	SupersededBy string `json:"supersededBy"`
}

func (h *Handler) supersedeEntry(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	var in supersedeBody
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if in.SupersededBy == "" {
		return apierr.NewAppError(http.StatusBadRequest, "supersededBy is required")
	}

	space, err := h.spaceOfEntry(r.Context(), id)
	if err != nil {
		return err
	}
	scope := repo.Scope{Kind: repo.ScopeKind(space.ScopeKind), Ref: space.ScopeRef}.Normalize()
	if err := h.authorize(r.Context(), repo.CapabilityMemoryWrite, space.Slug, scope); err != nil {
		return err
	}

	if err := h.repo.SupersedeEntry(r.Context(), id, in.SupersededBy); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	updated, err := h.repo.GetEntry(r.Context(), id)
	if err != nil {
		return err
	}
	apierr.WriteJSON(w, http.StatusOK, toMemoryEntryResponse(updated))
	return nil
}

func (h *Handler) expireEntry(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")

	space, err := h.spaceOfEntry(r.Context(), id)
	if err != nil {
		return err
	}
	scope := repo.Scope{Kind: repo.ScopeKind(space.ScopeKind), Ref: space.ScopeRef}.Normalize()
	if err := h.authorize(r.Context(), repo.CapabilityMemoryWrite, space.Slug, scope); err != nil {
		return err
	}

	if err := h.repo.ExpireEntry(r.Context(), id, time.Now()); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, err.Error())
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// maxStageRunIDs bounds the repeated stageRun parameter. A task runs at most
// max_iterations (default 20) passes over the pipeline's handful of stages, so
// this clears any real task's stage-run count several times over.
const maxStageRunIDs = 200

// listInjections answers GET /api/memory/injections?stageRun=. The parameter
// repeats: ?stageRun=a&stageRun=b answers both runs' records in one array, in
// the order asked for. One id behaves exactly as it always did.
//
// The bulk form exists because the gate below records a rate-limit use on
// every call (memory.Gate.Authorize). One request per stage run meant opening
// a task modal with ten runs spent ten uses of the very same memory.read
// grant the pipeline's own memory push authorizes — so reading the memory
// view could exhaust the window and silently stop agent memory retrieval.
// N ids now cost one Authorize.
func (h *Handler) listInjections(w http.ResponseWriter, r *http.Request) error {
	stageRuns := dedupe(r.URL.Query()["stageRun"])
	if len(stageRuns) == 0 {
		return apierr.NewAppError(http.StatusBadRequest, "stageRun is required")
	}
	// One Authorize covers the whole list, so without a cap a single
	// capability check buys an unbounded number of queries.
	if len(stageRuns) > maxStageRunIDs {
		return apierr.NewAppError(http.StatusBadRequest,
			"at most "+strconv.Itoa(maxStageRunIDs)+" stageRun ids per request")
	}
	// An injection record has no single owning space to match a grant
	// pattern against (it can span several), so this is gated the same way
	// listSpaces and searchEntries are: a wildcard read at global — a project-
	// or application-scoped grant only backs a request in that same context.
	if err := h.authorize(r.Context(), repo.CapabilityMemoryRead, "", repo.GlobalScope()); err != nil {
		return err
	}
	var injections []*ent.MemoryInjection
	for _, stageRun := range stageRuns {
		// One local query per id rather than a single IN: the cost this
		// route had was the gate, not the reads, and the repo already owns
		// the per-run ordering.
		rows, err := h.repo.ListInjectionsByStageRun(r.Context(), stageRun)
		if err != nil {
			return err
		}
		injections = append(injections, rows...)
	}
	apierr.WriteJSON(w, http.StatusOK, toMemoryInjectionResponses(injections))
	return nil
}

// dedupe drops empty and repeated ids, preserving first-seen order: a caller
// repeating one id must not be answered that run's records twice, and
// ?stageRun= with an empty value is no id at all.
func dedupe(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" {
			continue
		}
		if _, dup := seen[v]; dup {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
