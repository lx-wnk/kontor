// Package analytics implements the /api/analytics/* endpoints.
package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/analytics"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler handles all /api/analytics/* routes.
type Handler struct {
	// analyticsRepo handles all read queries (patterns, heatmap, cost series).
	analyticsRepo rawrepo.AnalyticsRepo
	// rawDB is kept solely for analytics.DiscoverPatterns, which performs an
	// upsert-style write that does not fit the read-repo interface.
	rawDB   *sql.DB
	cfgRepo repo.PipelineConfigRepo
	mu      sync.Mutex // serializes concurrent pattern refresh calls
}

// NewHandler creates a Handler backed by the given repos.
func NewHandler(analyticsRepo rawrepo.AnalyticsRepo, rawDB *sql.DB, cfgRepo repo.PipelineConfigRepo) *Handler {
	return &Handler{analyticsRepo: analyticsRepo, rawDB: rawDB, cfgRepo: cfgRepo}
}

// Mount registers analytics routes on the given router.
// Must be called inside a protected (JWT / bypass) group.
// Note: uses absolute paths to match the project-wide convention in Mount methods.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/analytics/patterns", h.getPatterns)
	r.Post("/api/analytics/patterns/refresh", h.refreshPatterns)
	r.Get("/api/analytics/heatmap", h.getHeatmap)
	r.Get("/api/analytics/cost-forecast", h.getCostForecast)
}

// ─── GET /api/analytics/patterns ──────────────────────────────────────────────

type patternRow struct {
	ID         int    `json:"id"`
	Tools      string `json:"tools"`
	Frequency  int    `json:"frequency"`
	LastSeenAt string `json:"lastSeenAt"`
}

func (h *Handler) getPatterns(w http.ResponseWriter, r *http.Request) {
	rows, err := h.analyticsRepo.GetPatterns(r.Context())
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, "failed to query patterns")
		return
	}

	patterns := make([]patternRow, 0, len(rows))
	for _, p := range rows {
		patterns = append(patterns, patternRow{
			ID:         p.ID,
			Tools:      p.Tools,
			Frequency:  p.Frequency,
			LastSeenAt: p.LastSeenAt,
		})
	}

	apierr.WriteJSON(w, http.StatusOK, map[string]any{"patterns": patterns})
}

// ─── POST /api/analytics/patterns/refresh ─────────────────────────────────────

func (h *Handler) refreshPatterns(w http.ResponseWriter, r *http.Request) {
	if !h.mu.TryLock() {
		apierr.WriteJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "refresh already running"})
		return
	}
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("analytics: pattern refresh panicked", "panic", rec)
			}
			h.mu.Unlock()
		}()
		// Use context.Background() — the request context would be cancelled as
		// soon as the HTTP response is sent, which would abort a long scan.
		if err := analytics.DiscoverPatterns(context.Background(), h.rawDB); err != nil {
			slog.Error("analytics: discover patterns failed", "err", err)
		}
	}()
	apierr.WriteJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

// ─── GET /api/analytics/heatmap ───────────────────────────────────────────────

type heatmapResponse struct {
	// Grid is indexed [dow][hour], dow 0=Sun, hour 0-23.
	Grid [7][24]float64 `json:"grid"`
}

func (h *Handler) getHeatmap(w http.ResponseWriter, r *http.Request) {
	points, err := h.analyticsRepo.GetHeatmap(r.Context())
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, fmt.Sprintf("analytics.heatmap: %v", err))
		return
	}

	var resp heatmapResponse
	for _, p := range points {
		if p.DOW >= 0 && p.DOW < 7 && p.Hour >= 0 && p.Hour < 24 {
			resp.Grid[p.DOW][p.Hour] = p.Cost
		}
	}

	apierr.WriteJSON(w, http.StatusOK, resp)
}

// ─── GET /api/analytics/cost-forecast ─────────────────────────────────────────

type forecastPoint struct {
	T             int64   `json:"t"`             // unix ms
	ProjectedCost float64 `json:"projectedCost"` // USD
}

type forecastResponse struct {
	Forecast []forecastPoint `json:"forecast"`
	Alert    string          `json:"alert"` // "" | "warn" | "critical"
}

func (h *Handler) getCostForecast(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	since := time.Now().UTC().AddDate(0, 0, -30)
	samples, err := h.analyticsRepo.GetCostSince(ctx, since)
	if err != nil {
		slog.Warn("analytics: forecast query failed", "err", err)
		apierr.WriteJSON(w, http.StatusOK, forecastResponse{Forecast: []forecastPoint{}, Alert: ""})
		return
	}

	if len(samples) == 0 {
		apierr.WriteJSON(w, http.StatusOK, forecastResponse{Forecast: []forecastPoint{}, Alert: ""})
		return
	}

	// Build cumulative-sum series as {t (unix ms, normalized), y (cumulative USD)}.
	// Normalization avoids float64 cancellation with large Unix ms values.
	t0 := float64(samples[0].RecordedAt.UnixMilli())
	series := make([]dataPoint, 0, len(samples))
	var cumCost float64
	for _, s := range samples {
		cumCost += s.CostUSD
		series = append(series, dataPoint{
			t: float64(s.RecordedAt.UnixMilli()) - t0,
			y: cumCost,
		})
	}

	slope, intercept := linearRegression(series)

	// Project 7 days forward from now (re-apply t0 offset when evaluating).
	now := time.Now().UTC()
	forecast := make([]forecastPoint, 7)
	for i := 0; i < 7; i++ {
		ft := now.AddDate(0, 0, i+1)
		tNorm := float64(ft.UnixMilli()) - t0
		projected := math.Max(0, slope*tNorm+intercept)
		forecast[i] = forecastPoint{
			T:             ft.UnixMilli(),
			ProjectedCost: projected,
		}
	}

	// Determine alert level from pipeline config.
	warnCents := h.cfgRepo.GetNumber(ctx, "cost_forecast_warn_cents", 1000)
	critCents := h.cfgRepo.GetNumber(ctx, "cost_forecast_critical_cents", 5000)

	projectedCents := forecast[6].ProjectedCost * 100
	alert := ""
	switch {
	case projectedCents >= critCents:
		alert = "critical"
	case projectedCents >= warnCents:
		alert = "warn"
	}

	apierr.WriteJSON(w, http.StatusOK, forecastResponse{Forecast: forecast, Alert: alert})
}

// ─── helpers ──────────────────────────────────────────────────────────────────

type dataPoint struct{ t, y float64 }

// linearRegression performs OLS on a series of (t, y) points.
// Returns slope and intercept such that y ≈ slope*t + intercept.
func linearRegression(pts []dataPoint) (slope, intercept float64) {
	n := float64(len(pts))
	if n == 0 {
		return 0, 0
	}
	var sumT, sumY, sumTT, sumTY float64
	for i := range pts {
		sumT += pts[i].t
		sumY += pts[i].y
		sumTT += pts[i].t * pts[i].t
		sumTY += pts[i].t * pts[i].y
	}
	denom := n*sumTT - sumT*sumT
	if denom == 0 {
		return 0, sumY / n
	}
	slope = (n*sumTY - sumT*sumY) / denom
	intercept = (sumY - slope*sumT) / n
	return slope, intercept
}
