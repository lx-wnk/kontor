// Package eval implements the /api/eval/* endpoints for metric snapshots and drift alerts.
package eval

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/eval"
)

const (
	defaultLookbackHours = 336  // 14 days
	maxLookbackHours     = 8760 // 365 days — caps the read window
)

// validStatuses is the allow-list for the drift ?status= query param.
var validStatuses = map[string]bool{
	repo.DriftStatusOpen:         true,
	repo.DriftStatusAcknowledged: true,
}

// Scanner is satisfied by *eval.Service without creating a direct dependency on it.
type Scanner interface {
	Scan(ctx context.Context) error
}

// Handler handles all /api/eval/* routes.
type Handler struct {
	metricRepo repo.EvalMetricRepo
	alertRepo  repo.DriftAlertRepo
	scanner    Scanner
}

// NewHandler creates a Handler backed by the given repos and scanner.
func NewHandler(metricRepo repo.EvalMetricRepo, alertRepo repo.DriftAlertRepo, scanner Scanner) *Handler {
	return &Handler{metricRepo: metricRepo, alertRepo: alertRepo, scanner: scanner}
}

// Mount registers eval routes on the given router.
// Must be called inside a protected (JWT / bypass) group.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/eval/metrics", h.getMetrics)
	r.Get("/api/eval/drift", h.getDrift)
	r.Post("/api/eval/drift/{id}/ack", h.ackDrift)
	r.Post("/api/eval/scan", h.triggerScan)
}

type metricSnapshotDTO struct {
	ID          string  `json:"id"`
	SpawnerID   string  `json:"spawnerId"`
	Model       string  `json:"model"`
	Stage       string  `json:"stage"`
	MetricKey   string  `json:"metricKey"`
	Value       float64 `json:"value"`
	SampleCount int     `json:"sampleCount"`
	WindowStart string  `json:"windowStart"`
	WindowEnd   string  `json:"windowEnd"`
	RecordedAt  string  `json:"recordedAt"`
}

type driftAlertDTO struct {
	ID             string  `json:"id"`
	SpawnerID      string  `json:"spawnerId"`
	Model          string  `json:"model"`
	Stage          string  `json:"stage"`
	MetricKey      string  `json:"metricKey"`
	Status         string  `json:"status"`
	Direction      string  `json:"direction"`
	BaselineValue  float64 `json:"baselineValue"`
	RecentValue    float64 `json:"recentValue"`
	Delta          float64 `json:"delta"`
	Threshold      float64 `json:"threshold"`
	SampleCount    int     `json:"sampleCount"`
	DetectedAt     string  `json:"detectedAt"`
	AcknowledgedAt *string `json:"acknowledgedAt"`
}

func (h *Handler) getMetrics(w http.ResponseWriter, r *http.Request) {
	hours := defaultLookbackHours
	if v := r.URL.Query().Get("hours"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			apierr.JSONError(w, http.StatusBadRequest, "hours must be a positive integer")
			return
		}
		if n > maxLookbackHours {
			n = maxLookbackHours
		}
		hours = n
	}

	now := time.Now().UTC()
	from := now.Add(-time.Duration(hours) * time.Hour)
	to := now

	metric := r.URL.Query().Get("metric")

	var (
		rows []*ent.EvalMetricSnapshot
		err  error
	)
	if metric != "" {
		rows, err = h.metricRepo.ListByMetric(r.Context(), metric, from, to)
	} else {
		rows, err = h.metricRepo.ListByTimeRange(r.Context(), from, to)
	}
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, "failed to query eval metrics")
		return
	}

	dtos := make([]metricSnapshotDTO, 0, len(rows))
	for _, s := range rows {
		dtos = append(dtos, toMetricDTO(s))
	}
	apierr.WriteJSON(w, http.StatusOK, dtos)
}

func (h *Handler) getDrift(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = repo.DriftStatusOpen
	}
	if !validStatuses[status] {
		apierr.JSONError(w, http.StatusBadRequest, "status must be 'open' or 'acknowledged'")
		return
	}

	rows, err := h.alertRepo.ListByStatus(r.Context(), status)
	if err != nil {
		apierr.JSONError(w, http.StatusInternalServerError, "failed to query drift alerts")
		return
	}

	dtos := make([]driftAlertDTO, 0, len(rows))
	for _, a := range rows {
		dtos = append(dtos, toDriftDTO(a))
	}
	apierr.WriteJSON(w, http.StatusOK, dtos)
}

func (h *Handler) ackDrift(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		apierr.JSONError(w, http.StatusBadRequest, "missing alert id")
		return
	}

	if err := h.alertRepo.Acknowledge(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			apierr.JSONError(w, http.StatusNotFound, "drift alert not found")
			return
		}
		apierr.JSONError(w, http.StatusInternalServerError, "failed to acknowledge drift alert")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) triggerScan(w http.ResponseWriter, r *http.Request) {
	if err := h.scanner.Scan(r.Context()); err != nil {
		if errors.Is(err, eval.ErrScanInProgress) {
			apierr.JSONError(w, http.StatusConflict, "a scan is already in progress")
			return
		}
		apierr.JSONError(w, http.StatusInternalServerError, "scan failed")
		return
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func toMetricDTO(s *ent.EvalMetricSnapshot) metricSnapshotDTO {
	return metricSnapshotDTO{
		ID:          s.ID,
		SpawnerID:   s.SpawnerID,
		Model:       s.Model,
		Stage:       s.Stage,
		MetricKey:   s.MetricKey,
		Value:       s.Value,
		SampleCount: s.SampleCount,
		WindowStart: s.WindowStart.UTC().Format(time.RFC3339),
		WindowEnd:   s.WindowEnd.UTC().Format(time.RFC3339),
		RecordedAt:  s.RecordedAt.UTC().Format(time.RFC3339),
	}
}

func toDriftDTO(a *ent.DriftAlert) driftAlertDTO {
	var ackAt *string
	if a.AcknowledgedAt != nil {
		s := a.AcknowledgedAt.UTC().Format(time.RFC3339)
		ackAt = &s
	}
	return driftAlertDTO{
		ID:             a.ID,
		SpawnerID:      a.SpawnerID,
		Model:          a.Model,
		Stage:          a.Stage,
		MetricKey:      a.MetricKey,
		Status:         a.Status,
		Direction:      a.Direction,
		BaselineValue:  a.BaselineValue,
		RecentValue:    a.RecentValue,
		Delta:          a.Delta,
		Threshold:      a.Threshold,
		SampleCount:    a.SampleCount,
		DetectedAt:     a.DetectedAt.UTC().Format(time.RFC3339),
		AcknowledgedAt: ackAt,
	}
}
