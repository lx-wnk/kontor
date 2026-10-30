// Package insights implements the /api/insights/* endpoints — per-session
// analytics over the agent_cost_trends table for the Insights page.
package insights

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	apicost "github.com/lx-wnk/kontor/server/internal/api/cost"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

const (
	dateLayout   = "2006-01-02"
	defaultLimit = 50
	maxLimit     = 200
)

// sortExprs maps the `sort` param to a fixed SQL expression; the param never reaches SQL text.
var sortExprs = map[string]string{
	"cost":        "cost_usd",
	"agent_ms":    "agent_ms",
	"tokens":      "(input_tokens + output_tokens + cache_read_tokens + cache_creation_tokens)",
	"turns":       "turns",
	"started_at":  "started_at",
	"recorded_at": "recorded_at",
	"lines":       "(lines_added + lines_removed)",
}

// Handler serves the /api/insights/* endpoints.
type Handler struct {
	db *sql.DB
}

// NewHandler returns a Handler backed by db. db may be nil; every endpoint
// then answers 200 with an empty payload.
func NewHandler(db *sql.DB) *Handler { return &Handler{db: db} }

// Mount registers the insights routes. Must be called inside the protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/insights/summary", h.getSummary)
	r.Get("/api/insights/sessions", h.getSessions)
	r.Get("/api/insights/projects", h.getProjects)
}

type totals struct {
	CostUSD   float64 `json:"costUsd"`
	AgentMs   int64   `json:"agentMs"`
	Tokens    int64   `json:"tokens"`
	Sessions  int     `json:"sessions"`
	Estimated int     `json:"estimated"`
}

type summaryResponse struct {
	From     string  `json:"from"`
	To       string  `json:"to"`
	Current  totals  `json:"current"`
	Previous *totals `json:"previous"`
}

type sessionRow struct {
	SessionID           string             `json:"sessionId"`
	Title               string             `json:"title"`
	ProjectPath         string             `json:"projectPath"`
	ProjectName         string             `json:"projectName"`
	Model               string             `json:"model"`
	StartedAt           *string            `json:"startedAt"`
	RecordedAt          string             `json:"recordedAt"`
	CostUSD             float64            `json:"costUsd"`
	CostSource          string             `json:"costSource"`
	CostUnknownModel    bool               `json:"costUnknownModel"`
	AgentMs             int64              `json:"agentMs"`
	AgentTimeSource     string             `json:"agentTimeSource"`
	InputTokens         int64              `json:"inputTokens"`
	OutputTokens        int64              `json:"outputTokens"`
	CacheReadTokens     int64              `json:"cacheReadTokens"`
	CacheCreationTokens int64              `json:"cacheCreationTokens"`
	Turns               int                `json:"turns"`
	LinesAdded          int                `json:"linesAdded"`
	LinesRemoved        int                `json:"linesRemoved"`
	ModelCosts          map[string]float64 `json:"modelCosts"`
}

type sessionsResponse struct {
	Rows   []sessionRow `json:"rows"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

type projectRow struct {
	ProjectPath string `json:"projectPath"`
	ProjectName string `json:"projectName"`
	Sessions    int    `json:"sessions"`
}

type rangeParams struct {
	from, to, project string
}

func parseRange(w http.ResponseWriter, r *http.Request) (rangeParams, bool) {
	q := r.URL.Query()
	p := rangeParams{from: q.Get("from"), to: q.Get("to"), project: q.Get("project")}
	if p.to == "" {
		p.to = time.Now().UTC().Format(dateLayout)
	}
	for _, v := range []string{p.from, p.to} {
		if v == "" {
			continue
		}
		if _, err := time.Parse(dateLayout, v); err != nil {
			apierr.JSONError(w, http.StatusBadRequest, "dates must be YYYY-MM-DD")
			return p, false
		}
	}
	if p.from != "" && p.from > p.to {
		apierr.JSONError(w, http.StatusBadRequest, "from must not be after to")
		return p, false
	}
	if len(p.project) > apicost.ProjectParamMaxLen {
		apierr.JSONError(w, http.StatusBadRequest, "project too long")
		return p, false
	}
	return p, true
}

// previousWindow returns the equally long window right before [from, to].
func previousWindow(from, to string) (string, string) {
	f, _ := time.Parse(dateLayout, from)
	t, _ := time.Parse(dateLayout, to)
	days := int(t.Sub(f).Hours()/24) + 1
	return f.AddDate(0, 0, -days).Format(dateLayout), f.AddDate(0, 0, -1).Format(dateLayout)
}

func (h *Handler) getSummary(w http.ResponseWriter, r *http.Request) {
	p, ok := parseRange(w, r)
	if !ok {
		return
	}
	resp := summaryResponse{From: p.from, To: p.to}
	if h.db == nil {
		if p.from != "" {
			resp.Previous = &totals{}
		}
		apierr.WriteJSON(w, http.StatusOK, resp)
		return
	}
	var err error
	if resp.Current, err = h.queryTotals(r.Context(), p.from, p.to, p.project); err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("insights.summary: %v", err))
		return
	}
	if p.from != "" {
		pf, pt := previousWindow(p.from, p.to)
		prev, err := h.queryTotals(r.Context(), pf, pt, p.project)
		if err != nil {
			apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("insights.summary previous: %v", err))
			return
		}
		resp.Previous = &prev
	}
	apierr.WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) queryTotals(ctx context.Context, from, to, project string) (totals, error) {
	where, args := apicost.RangeWhere(from, to, project)
	//nolint:gosec // G202: where is a fixed clause from RangeWhere; values are bound
	q := `
SELECT COALESCE(SUM(cost_usd), 0),
       COALESCE(SUM(agent_ms), 0),
       COALESCE(SUM(input_tokens + output_tokens + cache_read_tokens + cache_creation_tokens), 0),
       COUNT(*),
       COALESCE(SUM(cost_source = 'estimate'), 0)
FROM agent_cost_trends
WHERE ` + where
	var t totals
	err := h.db.QueryRowContext(ctx, q, args...).Scan(&t.CostUSD, &t.AgentMs, &t.Tokens, &t.Sessions, &t.Estimated)
	return t, err
}

func (h *Handler) getSessions(w http.ResponseWriter, r *http.Request) {
	p, ok := parseRange(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	sortKey := valueOr(q.Get("sort"), "cost")
	sortExpr, known := sortExprs[sortKey]
	order := valueOr(q.Get("order"), "desc")
	limit, limitOK := intParam(q.Get("limit"), defaultLimit, 1, maxLimit)
	offset, offsetOK := intParam(q.Get("offset"), 0, 0, 1<<31-1)
	if !known || (order != "asc" && order != "desc") || !limitOK || !offsetOK {
		apierr.JSONError(w, http.StatusBadRequest, "invalid sort, order, limit or offset")
		return
	}

	resp := sessionsResponse{Rows: []sessionRow{}, Limit: limit, Offset: offset}
	if h.db == nil {
		apierr.WriteJSON(w, http.StatusOK, resp)
		return
	}
	rows, total, err := h.querySessions(r.Context(), p, sortExpr, order, limit, offset)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("insights.sessions: %v", err))
		return
	}
	resp.Rows, resp.Total = rows, total
	apierr.WriteJSON(w, http.StatusOK, resp)
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intParam(v string, def, lo, hi int) (int, bool) {
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= lo && n <= hi
}

func (h *Handler) querySessions(ctx context.Context, p rangeParams, sortExpr, order string, limit, offset int) ([]sessionRow, int, error) {
	where, args := apicost.RangeWhere(p.from, p.to, p.project)
	var total int
	//nolint:gosec // G202: where is a fixed clause from RangeWhere; values are bound
	if err := h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_cost_trends WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	//nolint:gosec // G202: where is a fixed clause; sortExpr and order come from allow-lists
	q := `
SELECT session_id, COALESCE(title, ''), COALESCE(NULLIF(project_path, ''), 'unknown'), COALESCE(project_name, ''),
       COALESCE(model, ''), started_at, recorded_at, COALESCE(cost_usd, 0), COALESCE(cost_source, ''),
       COALESCE(cost_unknown_model, 0), COALESCE(agent_ms, 0), COALESCE(agent_time_source, ''),
       COALESCE(input_tokens, 0), COALESCE(output_tokens, 0), COALESCE(cache_read_tokens, 0),
       COALESCE(cache_creation_tokens, 0), COALESCE(turns, 0), COALESCE(lines_added, 0),
       COALESCE(lines_removed, 0), model_costs
FROM agent_cost_trends
WHERE ` + where + `
ORDER BY ` + sortExpr + ` ` + order + `, session_id ASC
LIMIT ? OFFSET ?`
	rs, err := h.db.QueryContext(ctx, q, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rs.Close()

	out := make([]sessionRow, 0, limit)
	for rs.Next() {
		var s sessionRow
		var started, recorded, costs any
		if err := rs.Scan(&s.SessionID, &s.Title, &s.ProjectPath, &s.ProjectName, &s.Model, &started, &recorded,
			&s.CostUSD, &s.CostSource, &s.CostUnknownModel, &s.AgentMs, &s.AgentTimeSource, &s.InputTokens,
			&s.OutputTokens, &s.CacheReadTokens, &s.CacheCreationTokens, &s.Turns, &s.LinesAdded,
			&s.LinesRemoved, &costs); err != nil {
			return nil, 0, err
		}
		s.StartedAt = rfc3339Ptr(started)
		if rec := rfc3339Ptr(recorded); rec != nil {
			s.RecordedAt = *rec
		}
		s.ModelCosts = map[string]float64{}
		if raw := asString(costs); raw != "" {
			if err := json.Unmarshal([]byte(raw), &s.ModelCosts); err != nil || s.ModelCosts == nil {
				s.ModelCosts = map[string]float64{}
			}
		}
		out = append(out, s)
	}
	return out, total, rs.Err()
}

func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

// ent stores times as Go's time.String() form; legacy rows hold RFC3339.
var timeLayouts = []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST", "2006-01-02 15:04:05.999999999-07:00"}

func rfc3339Ptr(v any) *string {
	if t, ok := v.(time.Time); ok {
		s := t.UTC().Format(time.RFC3339)
		return &s
	}
	raw := asString(v)
	if raw == "" {
		return nil
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			s := t.UTC().Format(time.RFC3339)
			return &s
		}
	}
	return &raw
}

func (h *Handler) getProjects(w http.ResponseWriter, r *http.Request) {
	p, ok := parseRange(w, r)
	if !ok {
		return
	}
	out := []projectRow{}
	if h.db == nil {
		apierr.WriteJSON(w, http.StatusOK, out)
		return
	}
	where, args := apicost.RangeWhere(p.from, p.to, "")
	//nolint:gosec // G202: where is a fixed clause from RangeWhere; values are bound
	q := `
SELECT COALESCE(NULLIF(project_path, ''), 'unknown') AS pp, COALESCE(MAX(project_name), ''), COUNT(*) AS n
FROM agent_cost_trends
WHERE ` + where + `
GROUP BY pp
ORDER BY n DESC, pp ASC`
	rs, err := h.db.QueryContext(r.Context(), q, args...)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("insights.projects: %v", err))
		return
	}
	defer rs.Close()
	for rs.Next() {
		var pr projectRow
		if err := rs.Scan(&pr.ProjectPath, &pr.ProjectName, &pr.Sessions); err != nil {
			apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("insights.projects: %v", err))
			return
		}
		if pr.ProjectName == "" {
			pr.ProjectName = pr.ProjectPath
		}
		out = append(out, pr)
	}
	if err := rs.Err(); err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("insights.projects: %v", err))
		return
	}
	apierr.WriteJSON(w, http.StatusOK, out)
}
