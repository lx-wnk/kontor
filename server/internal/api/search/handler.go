// Package search provides the GET /api/search spotlight-search endpoint.
// It performs FTS5 full-text search over tasks and in-memory substring search
// over running agents.
package search

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/merger"
)

const (
	maxQueryLen  = 200
	defaultLimit = 20
	maxLimit     = 50
	minLimit     = 1
)

// taskSearchResult is the lightweight task shape returned by the search endpoint.
type taskSearchResult struct {
	ID           string  `json:"id"`
	Slug         string  `json:"slug"`
	Title        string  `json:"title"`
	Description  *string `json:"description"`
	Cwd          string  `json:"cwd"`
	CurrentStage string  `json:"currentStage"`
	Priority     string  `json:"priority"`
	CreatedAt    string  `json:"createdAt"`
}

// searchResponse is the top-level response shape.
type searchResponse struct {
	Tasks  []taskSearchResult `json:"tasks"`
	Agents []sdk.Agent        `json:"agents"`
}

// Handler handles GET /api/search.
type Handler struct {
	searchRepo rawrepo.SearchRepo
	merger     *merger.Merger
	enricher   merger.Enricher
	bypassAuth bool
}

// NewHandler creates a new Handler backed by the given SearchRepo. The merger is
// the shared roster builder used for the agent-search branch. The enricher
// (may be nil) is the same pipeline-task crossing applied by the router's
// GetAgents accessor — passing it here keeps agent search results enriched
// consistently with /api/agents and the SSE stream.
//
// bypassAuth is the loopback single-user mode: there is one implicit user, so
// results are not scoped to a user id and the agent roster is searchable.
func NewHandler(searchRepo rawrepo.SearchRepo, m *merger.Merger, enricher merger.Enricher, bypassAuth bool) *Handler {
	return &Handler{searchRepo: searchRepo, merger: m, enricher: enricher, bypassAuth: bypassAuth}
}

// Search handles GET /api/search?q=...&type=...&limit=...
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query().Get("q")
	searchType := r.URL.Query().Get("type")
	limitStr := r.URL.Query().Get("limit")

	if searchType == "" {
		searchType = "all"
	}

	limit := defaultLimit
	if limitStr != "" {
		if n, err := strconv.Atoi(limitStr); err == nil {
			limit = n
		}
	}
	if limit < minLimit {
		limit = minLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}

	payload, ok := auth.PayloadFromContext(r.Context())
	if !ok {
		// Missing payload ⟹ bypass mode (DASHBOARD_AUTH=none); act as local admin.
		payload = auth.BypassPayload()
	}

	// Trim and validate query.
	q = strings.TrimSpace(q)
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}

	resp := searchResponse{
		Tasks:  []taskSearchResult{},
		Agents: []sdk.Agent{},
	}

	if q == "" {
		writeJSON(w, http.StatusOK, resp)
		return nil
	}

	if searchType == "tasks" || searchType == "all" {
		tasks, err := h.searchTasks(r, q, payload.Sub, h.bypassAuth, limit)
		if err == nil {
			resp.Tasks = tasks
		}
	}

	if (searchType == "agents" || searchType == "all") && h.bypassAuth {
		agents, err := h.merger.GetAgents(r.Context(), merger.GetAgentsOpts{Enricher: h.enricher})
		if err == nil {
			resp.Agents = filterAgents(agents, q, limit)
		}
	}

	writeJSON(w, http.StatusOK, resp)
	return nil
}

// searchTasks executes an FTS5 query via the SearchRepo and returns matching tasks.
func (h *Handler) searchTasks(r *http.Request, q, userID string, isAdmin bool, limit int) ([]taskSearchResult, error) {
	ftsQuery := rawrepo.SanitizeFTSQuery(q)

	rows, err := h.searchRepo.SearchTasks(r.Context(), ftsQuery, userID, isAdmin, limit)
	if err != nil {
		// Return empty results gracefully — FTS errors should not be surfaced as 500.
		slog.Warn("search: FTS query failed", "err", err, "q", ftsQuery)
		return []taskSearchResult{}, nil
	}

	results := make([]taskSearchResult, 0, len(rows))
	for _, row := range rows {
		res := taskSearchResult{
			ID:           row.ID,
			Slug:         row.Slug,
			Title:        row.Title,
			Cwd:          row.Cwd,
			CurrentStage: row.CurrentStage,
			Priority:     row.Priority,
			CreatedAt:    row.CreatedAt.UTC().Format(time.RFC3339),
		}
		if row.Description.Valid {
			s := row.Description.String
			res.Description = &s
		}
		results = append(results, res)
	}
	return results, nil
}

// filterAgents returns agents matching the query by case-insensitive substring on
// projectName, currentAction, or cwd. Results are capped at limit.
func filterAgents(agents []sdk.Agent, q string, limit int) []sdk.Agent {
	lower := strings.ToLower(q)
	var matched []sdk.Agent
	for _, a := range agents {
		if strings.Contains(strings.ToLower(a.ProjectName), lower) ||
			(a.CurrentAction != nil && strings.Contains(strings.ToLower(*a.CurrentAction), lower)) ||
			strings.Contains(strings.ToLower(a.CWD), lower) {
			matched = append(matched, a)
			if len(matched) >= limit {
				break
			}
		}
	}
	if matched == nil {
		matched = []sdk.Agent{}
	}
	return matched
}

// writeJSON writes v as JSON with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
