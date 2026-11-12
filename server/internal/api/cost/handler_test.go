package cost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

func getSummaryResp(t *testing.T, query string) (int, summaryResponse) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	now := time.Now().UTC()
	rows := []repo.AgentCostRow{
		{SessionID: uuid.NewString(), ProjectPath: "/a", Model: "m1", CostUSD: 1, RecordedAt: now.AddDate(0, 0, -1)},
		{SessionID: uuid.NewString(), ProjectPath: "/b", Model: "m2", CostUSD: 2, RecordedAt: now.AddDate(0, 0, -2)},
		{SessionID: uuid.NewString(), ProjectPath: "", Model: "m1", CostUSD: 4, RecordedAt: now.AddDate(0, 0, -3)},
	}
	if err := repo.NewAgentCostTrendRepo(bundle.Client).Upsert(t.Context(), rows); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	rec := httptest.NewRecorder()
	newRouter(NewHandler(bundle.DB)).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/cost/summary"+query, nil))
	var resp summaryResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
	}
	return rec.Code, resp
}

func TestSummary_ProjectFilter(t *testing.T) {
	code, resp := getSummaryResp(t, "?project=/a")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if resp.TotalUSD != 1 || len(resp.ByProject) != 1 || resp.ByProject[0].ProjectPath != "/a" {
		t.Fatalf("totals/byProject not filtered: %+v", resp)
	}
	if len(resp.ByDay) != 1 || resp.ByDay[0].CostUSD != 1 {
		t.Fatalf("byDay not filtered: %+v", resp.ByDay)
	}
}

func TestSummary_NoProjectUnchanged(t *testing.T) {
	code, resp := getSummaryResp(t, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if resp.TotalUSD != 7 || len(resp.ByProject) != 3 || len(resp.ByDay) != 3 {
		t.Fatalf("unfiltered response changed: %+v", resp)
	}
}

func TestSummary_UnknownProjectBucket(t *testing.T) {
	_, resp := getSummaryResp(t, "?project=unknown")
	if resp.TotalUSD != 4 || len(resp.ByProject) != 1 || resp.ByProject[0].ProjectPath != "unknown" {
		t.Fatalf("unknown bucket not matched: %+v", resp)
	}
}

func TestSummary_ProjectTooLong(t *testing.T) {
	code, _ := getSummaryResp(t, "?project="+strings.Repeat("x", ProjectParamMaxLen+1))
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestRangeWhere(t *testing.T) {
	const dateTerm = "substr(recorded_at, 1, 10)"
	const projTerm = "COALESCE(NULLIF(project_path, ''), 'unknown') = ?"
	cases := []struct {
		name              string
		from, to, project string
		clause            string
		args              []any
	}{
		{"both bounds", "2026-01-01", "2026-01-31", "", dateTerm + " >= ? AND " + dateTerm + " <= ?", []any{"2026-01-01", "2026-01-31"}},
		{"empty from", "", "2026-01-31", "", dateTerm + " <= ?", []any{"2026-01-31"}},
		{"with project", "2026-01-01", "2026-01-31", "/a", dateTerm + " >= ? AND " + dateTerm + " <= ? AND " + projTerm, []any{"2026-01-01", "2026-01-31", "/a"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			clause, args := RangeWhere(c.from, c.to, c.project)
			if clause != c.clause || !reflect.DeepEqual(args, c.args) {
				t.Fatalf("got %q %v, want %q %v", clause, args, c.clause, c.args)
			}
		})
	}
}
