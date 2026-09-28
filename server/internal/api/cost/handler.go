// Package cost implements the /api/cost/* analytics endpoints — aggregations
// over the agent_cost_trends table for the cost-analytics dashboard view.
package cost

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
)

// defaultRangeDays is the look-back window used when the request omits `from`.
const defaultRangeDays = 30

// Handler serves the /api/cost/* endpoints.
//
// All routes are read-only and execute SQL aggregations against the
// agent_cost_trends table.
type Handler struct {
	db *sql.DB
}

// NewHandler returns a Handler backed by db. db may be nil; in that case
// every endpoint responds with an empty summary instead of a 500 so the
// dashboard remains operable on fresh installs.
func NewHandler(db *sql.DB) *Handler {
	return &Handler{db: db}
}

// Mount registers the cost-analytics routes. Must be called inside the
// JWT/auth-bypass protected group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/cost/summary", h.getSummary)
}

// ─── response types ───────────────────────────────────────────────────────────

// modelBreakdown is the per-model spend + token totals over the requested range.
type modelBreakdown struct {
	Model        string  `json:"model"`
	CostUSD      float64 `json:"costUsd"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Sessions     int     `json:"sessions"`
}

// projectBreakdown is the per-project spend + token totals over the requested
// range. ProjectPath is the stable grouping key; ProjectName is the display label.
type projectBreakdown struct {
	ProjectPath  string  `json:"projectPath"`
	ProjectName  string  `json:"projectName"`
	CostUSD      float64 `json:"costUsd"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	Sessions     int     `json:"sessions"`
}

// dayPoint is a per-day, per-model cost slice. Day is ISO-8601 (YYYY-MM-DD, UTC).
type dayPoint struct {
	Day     string  `json:"day"`
	Model   string  `json:"model"`
	CostUSD float64 `json:"costUsd"`
}

// weekPoint is a per-ISO-week total cost (all models combined).
type weekPoint struct {
	Week    string  `json:"week"` // YYYY-WW (ISO week)
	CostUSD float64 `json:"costUsd"`
}

// summaryResponse is the full /api/cost/summary payload.
type summaryResponse struct {
	ByModel           []modelBreakdown   `json:"byModel"`
	ByProject         []projectBreakdown `json:"byProject"`
	ByDay             []dayPoint         `json:"byDay"`
	ByWeek            []weekPoint        `json:"byWeek"`
	TotalUSD          float64            `json:"totalUsd"`
	TotalInputTokens  int64              `json:"totalInputTokens"`
	TotalOutputTokens int64              `json:"totalOutputTokens"`
	From              string             `json:"from"` // YYYY-MM-DD echo of the applied range
	To                string             `json:"to"`   // YYYY-MM-DD echo of the applied range
	UpdatedAt         int64              `json:"updatedAt"`
}

// ─── GET /api/cost/summary?from=YYYY-MM-DD&to=YYYY-MM-DD ────────────────────────

func (h *Handler) getSummary(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UTC()
	from, to := parseRange(r, now)

	if h.db == nil {
		apierr.WriteJSON(w, http.StatusOK, emptySummary(from, to))
		return
	}

	ctx := r.Context()
	fromStr, toStr := dateStr(from), dateStr(to)

	byModel, err := h.queryByModel(ctx, fromStr, toStr)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("cost.byModel: %v", err))
		return
	}
	byProject, err := h.queryByProject(ctx, fromStr, toStr)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("cost.byProject: %v", err))
		return
	}
	byDay, err := h.queryByDay(ctx, fromStr, toStr)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("cost.byDay: %v", err))
		return
	}
	byWeek, err := h.queryByWeek(ctx, fromStr, toStr)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("cost.byWeek: %v", err))
		return
	}

	var totalUSD float64
	var totalIn, totalOut int64
	for _, m := range byModel {
		totalUSD += m.CostUSD
		totalIn += m.InputTokens
		totalOut += m.OutputTokens
	}

	apierr.WriteJSON(w, http.StatusOK, summaryResponse{
		ByModel:           byModel,
		ByProject:         byProject,
		ByDay:             byDay,
		ByWeek:            byWeek,
		TotalUSD:          totalUSD,
		TotalInputTokens:  totalIn,
		TotalOutputTokens: totalOut,
		From:              fromStr,
		To:                toStr,
		UpdatedAt:         now.UnixMilli(),
	})
}

// parseRange reads the from/to query params (YYYY-MM-DD). Missing `from` defaults
// to defaultRangeDays ago; missing `to` defaults to today. An unparseable value
// falls back to its default. `to` is inclusive of the whole day.
func parseRange(r *http.Request, now time.Time) (from, to time.Time) {
	from = now.AddDate(0, 0, -defaultRangeDays)
	to = now
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil {
			to = t
		}
	}
	return from, to
}

// ─── queries ──────────────────────────────────────────────────────────────────

// recordedAtRangeClause bounds recorded_at to [from, to] as a date-only window.
//
// The ent client persists time.Time via Go's default String() format
// ("2026-05-23 10:28:54.704002 +0000 UTC"); legacy rows may use RFC3339. Both
// share a lexicographically sortable "YYYY-MM-DD" prefix, so we compare the
// first 10 characters and avoid strftime() (which returns NULL for the Go form).
const recordedAtRangeClause = `substr(recorded_at, 1, 10) >= ? AND substr(recorded_at, 1, 10) <= ?`

func dateStr(t time.Time) string { return t.UTC().Format("2006-01-02") }

func (h *Handler) queryByModel(ctx context.Context, from, to string) ([]modelBreakdown, error) {
	q := `
SELECT
    COALESCE(NULLIF(model, ''), 'unknown') AS model,
    SUM(cost_usd)                          AS total_cost,
    SUM(input_tokens)                      AS in_tokens,
    SUM(output_tokens)                     AS out_tokens,
    COUNT(DISTINCT session_id)             AS sessions
FROM agent_cost_trends
WHERE ` + recordedAtRangeClause + `
GROUP BY model
ORDER BY total_cost DESC
`
	rows, err := h.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]modelBreakdown, 0)
	for rows.Next() {
		var m modelBreakdown
		if err := rows.Scan(&m.Model, &m.CostUSD, &m.InputTokens, &m.OutputTokens, &m.Sessions); err != nil {
			return nil, err
		}
		result = append(result, m)
	}
	return result, rows.Err()
}

func (h *Handler) queryByProject(ctx context.Context, from, to string) ([]projectBreakdown, error) {
	// Group by the stable project_path key; pick the most recent non-empty
	// project_name for display (MAX is a cheap deterministic choice).
	q := `
SELECT
    COALESCE(NULLIF(project_path, ''), 'unknown') AS project_path,
    COALESCE(NULLIF(MAX(project_name), ''), '')   AS project_name,
    SUM(cost_usd)                                 AS total_cost,
    SUM(input_tokens)                             AS in_tokens,
    SUM(output_tokens)                            AS out_tokens,
    COUNT(DISTINCT session_id)                    AS sessions
FROM agent_cost_trends
WHERE ` + recordedAtRangeClause + `
GROUP BY project_path
ORDER BY total_cost DESC
`
	rows, err := h.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]projectBreakdown, 0)
	for rows.Next() {
		var p projectBreakdown
		if err := rows.Scan(&p.ProjectPath, &p.ProjectName, &p.CostUSD, &p.InputTokens, &p.OutputTokens, &p.Sessions); err != nil {
			return nil, err
		}
		if p.ProjectName == "" {
			p.ProjectName = p.ProjectPath
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func (h *Handler) queryByDay(ctx context.Context, from, to string) ([]dayPoint, error) {
	q := `
SELECT
    substr(recorded_at, 1, 10)             AS day,
    COALESCE(NULLIF(model, ''), 'unknown') AS model,
    SUM(cost_usd)                          AS total_cost
FROM agent_cost_trends
WHERE ` + recordedAtRangeClause + `
GROUP BY day, model
ORDER BY day ASC, total_cost DESC
`
	rows, err := h.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]dayPoint, 0)
	for rows.Next() {
		var p dayPoint
		if err := rows.Scan(&p.Day, &p.Model, &p.CostUSD); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// queryByWeek aggregates per-ISO-week totals. SQLite can't reliably parse the
// ent-stored time format with strftime, so we read per-day totals and bucket
// them in Go using time.Time.ISOWeek().
func (h *Handler) queryByWeek(ctx context.Context, from, to string) ([]weekPoint, error) {
	q := `
SELECT
    substr(recorded_at, 1, 10) AS day,
    SUM(cost_usd)              AS total_cost
FROM agent_cost_trends
WHERE ` + recordedAtRangeClause + `
GROUP BY day
ORDER BY day ASC
`
	rows, err := h.db.QueryContext(ctx, q, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	weekTotals := make(map[string]float64)
	weekOrder := make([]string, 0)
	for rows.Next() {
		var day string
		var cost float64
		if err := rows.Scan(&day, &cost); err != nil {
			return nil, err
		}
		t, parseErr := time.Parse("2006-01-02", day)
		if parseErr != nil {
			continue
		}
		year, week := t.ISOWeek()
		key := fmt.Sprintf("%04d-W%02d", year, week)
		if _, seen := weekTotals[key]; !seen {
			weekOrder = append(weekOrder, key)
		}
		weekTotals[key] += cost
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]weekPoint, 0, len(weekOrder))
	for _, key := range weekOrder {
		result = append(result, weekPoint{Week: key, CostUSD: weekTotals[key]})
	}
	return result, nil
}

// emptySummary returns a zero-value summary used when the DB is unavailable
// on a fresh install. The frontend handles empty slices gracefully.
func emptySummary(from, to time.Time) summaryResponse {
	return summaryResponse{
		ByModel:   []modelBreakdown{},
		ByProject: []projectBreakdown{},
		ByDay:     []dayPoint{},
		ByWeek:    []weekPoint{},
		From:      dateStr(from),
		To:        dateStr(to),
		UpdatedAt: time.Now().UnixMilli(),
	}
}
