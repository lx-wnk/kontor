package cost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func newRouter(h *Handler) http.Handler {
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func seedTrends(t *testing.T, costRepo repo.AgentCostTrendRepo) {
	t.Helper()
	now := time.Now().UTC()
	rows := []repo.AgentCostRow{
		{SessionID: uuid.NewString(), Model: "claude-opus-4", InputTokens: 1000, OutputTokens: 200, CostUSD: 1.25, RecordedAt: now.AddDate(0, 0, -1)},
		{SessionID: uuid.NewString(), Model: "claude-opus-4", InputTokens: 500, OutputTokens: 100, CostUSD: 0.50, RecordedAt: now.AddDate(0, 0, -2)},
		{SessionID: uuid.NewString(), Model: "claude-sonnet-4", InputTokens: 800, OutputTokens: 150, CostUSD: 0.30, RecordedAt: now.AddDate(0, 0, -1)},
		{SessionID: uuid.NewString(), Model: "", InputTokens: 100, OutputTokens: 50, CostUSD: 0.05, RecordedAt: now.AddDate(0, 0, -10)},
	}
	if err := costRepo.Upsert(t.Context(), rows); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
}

func TestSummary_EmptyDB(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	h := NewHandler(bundle.DB)
	r := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/api/cost/summary", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp summaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalUSD != 0 {
		t.Fatalf("total = %v, want 0", resp.TotalUSD)
	}
	if len(resp.ByModel) != 0 || len(resp.ByDay) != 0 || len(resp.ByWeek) != 0 {
		t.Fatalf("expected empty aggregations, got %+v", resp)
	}
}

func TestSummary_NilDB(t *testing.T) {
	h := NewHandler(nil)
	r := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/api/cost/summary", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("nil-db should return 200, got %d", rec.Code)
	}
	var resp summaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalUSD != 0 || len(resp.ByModel) != 0 {
		t.Fatalf("nil-db should yield empty summary, got %+v", resp)
	}
}

func TestSummary_WithSeededRows(t *testing.T) {
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })

	costRepo := repo.NewAgentCostTrendRepo(bundle.Client)
	seedTrends(t, costRepo)

	h := NewHandler(bundle.DB)
	r := newRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/api/cost/summary", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var resp summaryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// All four rows contribute to TotalUSD (1.25 + 0.50 + 0.30 + 0.05 = 2.10).
	if got := resp.TotalUSD; got < 2.09 || got > 2.11 {
		t.Fatalf("TotalUSD = %v, want ≈ 2.10", got)
	}
	// Three distinct models including the empty-string → "unknown" fallback.
	if got := len(resp.ByModel); got != 3 {
		t.Fatalf("ByModel rows = %d, want 3 (got %+v)", got, resp.ByModel)
	}
	// claude-opus-4 should sort first by total_cost.
	if resp.ByModel[0].Model != "claude-opus-4" {
		t.Fatalf("expected top model claude-opus-4, got %s", resp.ByModel[0].Model)
	}
	// At least two days of activity within the 30-day window.
	if len(resp.ByDay) < 2 {
		t.Fatalf("ByDay rows = %d, want ≥ 2", len(resp.ByDay))
	}
	// At least one week bucket.
	if len(resp.ByWeek) == 0 {
		t.Fatalf("ByWeek empty, want ≥ 1 bucket")
	}
}
