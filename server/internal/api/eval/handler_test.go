package eval

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── fakes ────────────────────────────────────────────────────────────────────

type fakeMetricRepo struct {
	byMetric    []*ent.EvalMetricSnapshot
	byTimeRange []*ent.EvalMetricSnapshot
	err         error
}

var _ repo.EvalMetricRepo = (*fakeMetricRepo)(nil)

func (f *fakeMetricRepo) Insert(_ context.Context, _ []repo.EvalMetricSnapshotRow) error {
	return nil
}

func (f *fakeMetricRepo) ListByMetric(_ context.Context, _ string, _, _ time.Time) ([]*ent.EvalMetricSnapshot, error) {
	return f.byMetric, f.err
}

func (f *fakeMetricRepo) ListByTimeRange(_ context.Context, _, _ time.Time) ([]*ent.EvalMetricSnapshot, error) {
	return f.byTimeRange, f.err
}

type fakeDriftRepo struct {
	alerts  []*ent.DriftAlert
	ackErr  error
	listErr error
}

var _ repo.DriftAlertRepo = (*fakeDriftRepo)(nil)

func (f *fakeDriftRepo) UpsertOpen(_ context.Context, _ []repo.DriftAlertRow) error { return nil }

func (f *fakeDriftRepo) ListByStatus(_ context.Context, _ ...string) ([]*ent.DriftAlert, error) {
	return f.alerts, f.listErr
}

func (f *fakeDriftRepo) Acknowledge(_ context.Context, _ string) error { return f.ackErr }

type fakeScanner struct{ err error }

func (f *fakeScanner) Scan(_ context.Context) error { return f.err }

// ─── helpers ──────────────────────────────────────────────────────────────────

func newRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func do(r http.Handler, method, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	require.NoError(t, json.NewDecoder(w.Body).Decode(v))
}

// ─── GET /api/eval/metrics ────────────────────────────────────────────────────

func TestGetMetrics_NoMetricParam_CallsTimeRange(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	snap := &ent.EvalMetricSnapshot{
		ID:          "snap-1",
		SpawnerID:   "sp-1",
		Model:       "claude-3",
		Stage:       "eval",
		MetricKey:   "latency",
		Value:       1.5,
		SampleCount: 10,
		WindowStart: now.Add(-time.Hour),
		WindowEnd:   now,
		RecordedAt:  now,
	}
	h := NewHandler(
		&fakeMetricRepo{byTimeRange: []*ent.EvalMetricSnapshot{snap}},
		&fakeDriftRepo{},
		&fakeScanner{},
	)

	w := do(newRouter(h), http.MethodGet, "/api/eval/metrics")

	assert.Equal(t, http.StatusOK, w.Code)
	var result []metricSnapshotDTO
	decodeJSON(t, w, &result)
	require.Len(t, result, 1)
	assert.Equal(t, "snap-1", result[0].ID)
	assert.Equal(t, "latency", result[0].MetricKey)
}

func TestGetMetrics_WithMetricParam_CallsListByMetric(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	snap := &ent.EvalMetricSnapshot{
		ID:          "snap-2",
		MetricKey:   "accuracy",
		Value:       0.95,
		SampleCount: 5,
		WindowStart: now.Add(-time.Hour),
		WindowEnd:   now,
		RecordedAt:  now,
	}
	h := NewHandler(
		&fakeMetricRepo{byMetric: []*ent.EvalMetricSnapshot{snap}},
		&fakeDriftRepo{},
		&fakeScanner{},
	)

	w := do(newRouter(h), http.MethodGet, "/api/eval/metrics?metric=accuracy")

	assert.Equal(t, http.StatusOK, w.Code)
	var result []metricSnapshotDTO
	decodeJSON(t, w, &result)
	require.Len(t, result, 1)
	assert.Equal(t, "accuracy", result[0].MetricKey)
}

func TestGetMetrics_InvalidHours_Returns400(t *testing.T) {
	h := NewHandler(&fakeMetricRepo{}, &fakeDriftRepo{}, &fakeScanner{})

	for _, bad := range []string{"abc", "-1", "0"} {
		w := do(newRouter(h), http.MethodGet, "/api/eval/metrics?hours="+bad)
		assert.Equal(t, http.StatusBadRequest, w.Code, "expected 400 for hours=%s", bad)
	}
}

func TestGetMetrics_ValidHours_ReturnsOK(t *testing.T) {
	h := NewHandler(&fakeMetricRepo{byTimeRange: []*ent.EvalMetricSnapshot{}}, &fakeDriftRepo{}, &fakeScanner{})

	w := do(newRouter(h), http.MethodGet, "/api/eval/metrics?hours=24")

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestGetMetrics_RepoError_Returns500(t *testing.T) {
	h := NewHandler(
		&fakeMetricRepo{err: errors.New("db down")},
		&fakeDriftRepo{},
		&fakeScanner{},
	)

	w := do(newRouter(h), http.MethodGet, "/api/eval/metrics")

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── GET /api/eval/drift ──────────────────────────────────────────────────────

func TestGetDrift_DefaultStatusOpen(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	alert := &ent.DriftAlert{
		ID:            "alert-1",
		SpawnerID:     "sp-1",
		MetricKey:     "latency",
		Status:        "open",
		Direction:     "up",
		BaselineValue: 1.0,
		RecentValue:   2.0,
		Delta:         1.0,
		Threshold:     0.5,
		SampleCount:   20,
		DetectedAt:    now,
	}
	h := NewHandler(
		&fakeMetricRepo{},
		&fakeDriftRepo{alerts: []*ent.DriftAlert{alert}},
		&fakeScanner{},
	)

	w := do(newRouter(h), http.MethodGet, "/api/eval/drift")

	assert.Equal(t, http.StatusOK, w.Code)
	var result []driftAlertDTO
	decodeJSON(t, w, &result)
	require.Len(t, result, 1)
	assert.Equal(t, "alert-1", result[0].ID)
	assert.Nil(t, result[0].AcknowledgedAt)
}

func TestGetDrift_AcknowledgedAlert_IncludesAcknowledgedAt(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	ackAt := now
	alert := &ent.DriftAlert{
		ID:             "alert-2",
		Status:         "acknowledged",
		DetectedAt:     now,
		AcknowledgedAt: &ackAt,
	}
	h := NewHandler(
		&fakeMetricRepo{},
		&fakeDriftRepo{alerts: []*ent.DriftAlert{alert}},
		&fakeScanner{},
	)

	w := do(newRouter(h), http.MethodGet, "/api/eval/drift?status=acknowledged")

	assert.Equal(t, http.StatusOK, w.Code)
	var result []driftAlertDTO
	decodeJSON(t, w, &result)
	require.Len(t, result, 1)
	assert.NotNil(t, result[0].AcknowledgedAt)
}

func TestGetDrift_RepoError_Returns500(t *testing.T) {
	h := NewHandler(
		&fakeMetricRepo{},
		&fakeDriftRepo{listErr: errors.New("db error")},
		&fakeScanner{},
	)

	w := do(newRouter(h), http.MethodGet, "/api/eval/drift")

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── POST /api/eval/drift/{id}/ack ────────────────────────────────────────────

func TestAckDrift_ValidID_Returns204(t *testing.T) {
	h := NewHandler(&fakeMetricRepo{}, &fakeDriftRepo{}, &fakeScanner{})

	req := httptest.NewRequest(http.MethodPost, "/api/eval/drift/alert-99/ack", nil)
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestAckDrift_RepoError_Returns500(t *testing.T) {
	h := NewHandler(
		&fakeMetricRepo{},
		&fakeDriftRepo{ackErr: errors.New("update failed")},
		&fakeScanner{},
	)

	req := httptest.NewRequest(http.MethodPost, "/api/eval/drift/some-id/ack", nil)
	w := httptest.NewRecorder()
	newRouter(h).ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

// ─── POST /api/eval/scan ──────────────────────────────────────────────────────

func TestTriggerScan_Success_Returns200(t *testing.T) {
	h := NewHandler(&fakeMetricRepo{}, &fakeDriftRepo{}, &fakeScanner{})

	w := do(newRouter(h), http.MethodPost, "/api/eval/scan")

	assert.Equal(t, http.StatusOK, w.Code)
	var body map[string]bool
	decodeJSON(t, w, &body)
	assert.True(t, body["ok"])
}

func TestTriggerScan_ScannerError_Returns500(t *testing.T) {
	h := NewHandler(
		&fakeMetricRepo{},
		&fakeDriftRepo{},
		&fakeScanner{err: errors.New("scanner failed")},
	)

	w := do(newRouter(h), http.MethodPost, "/api/eval/scan")

	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
