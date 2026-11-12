package insights

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t.Add(10 * time.Hour)
}

func setup(t *testing.T, rows ...repo.AgentCostRow) http.Handler {
	t.Helper()
	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	if len(rows) > 0 {
		if err := repo.NewAgentCostTrendRepo(bundle.Client).Upsert(t.Context(), rows); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
	}
	r := chi.NewRouter()
	NewHandler(bundle.DB).Mount(r)
	return r
}

func get(t *testing.T, h http.Handler, url string, into any) int {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if into != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatalf("unmarshal %s: %v", rec.Body.String(), err)
		}
	}
	return rec.Code
}

func row(id string, d string, cost float64) repo.AgentCostRow {
	return repo.AgentCostRow{SessionID: id, Model: "m", CostUSD: cost, RecordedAt: day(d), CostSource: repo.SourceCostState, ProjectPath: "/p", ProjectName: "p"}
}

func TestSummary_CurrentAndPrevious(t *testing.T) {
	a := row("a", "2026-10-20", 1)
	a.CostSource = repo.SourceEstimate
	h := setup(t, a, row("b", "2026-10-27", 2))
	var got summaryResponse
	if c := get(t, h, "/api/insights/summary?from=2026-10-24&to=2026-10-30", &got); c != 200 {
		t.Fatalf("status %d", c)
	}
	if got.Current.CostUSD != 2 || got.Current.Estimated != 0 || got.Current.Sessions != 1 {
		t.Fatalf("current = %+v", got.Current)
	}
	if got.Previous == nil || got.Previous.CostUSD != 1 || got.Previous.Estimated != 1 {
		t.Fatalf("previous = %+v", got.Previous)
	}
}

func TestSummary_PreviousWindowEdges(t *testing.T) {
	h := setup(t, row("out", "2026-10-16", 100), row("first", "2026-10-17", 1), row("last", "2026-10-23", 2), row("cur", "2026-10-24", 4))
	var got summaryResponse
	get(t, h, "/api/insights/summary?from=2026-10-24&to=2026-10-30", &got)
	if got.Current.CostUSD != 4 || got.Current.Sessions != 1 {
		t.Fatalf("current = %+v", got.Current)
	}
	if got.Previous == nil || got.Previous.CostUSD != 3 || got.Previous.Sessions != 2 {
		t.Fatalf("previous = %+v", got.Previous)
	}
}

func TestSummary_EstimatedCountsLegacyAndUnknownModel(t *testing.T) {
	est, leg, unk := row("est", "2026-10-25", 1), row("leg", "2026-10-25", 1), row("unk", "2026-10-25", 1)
	est.CostSource, leg.CostSource, unk.CostUnknownModel = repo.SourceEstimate, repo.SourceLegacy, true
	pleg := row("pleg", "2026-10-20", 1)
	pleg.CostSource = repo.SourceLegacy
	h := setup(t, est, leg, unk, row("ok", "2026-10-25", 1), pleg)
	var got summaryResponse
	get(t, h, "/api/insights/summary?from=2026-10-24&to=2026-10-30", &got)
	if got.Current.Estimated != 3 || got.Current.Legacy != 1 || got.Current.Sessions != 4 {
		t.Fatalf("current = %+v", got.Current)
	}
	if got.Previous == nil || got.Previous.Estimated != 1 || got.Previous.Legacy != 1 {
		t.Fatalf("previous = %+v", got.Previous)
	}
}

func TestSummary_ProjectScopesCurrentAndPrevious(t *testing.T) {
	q1, q2 := row("q1", "2026-10-25", 10), row("q2", "2026-10-20", 20)
	q1.ProjectPath, q2.ProjectPath = "/q", "/q"
	h := setup(t, row("p1", "2026-10-25", 1), row("p2", "2026-10-20", 2), q1, q2)
	var got summaryResponse
	get(t, h, "/api/insights/summary?from=2026-10-24&to=2026-10-30&project=/p", &got)
	if got.Current.CostUSD != 1 || got.Current.Sessions != 1 {
		t.Fatalf("current = %+v", got.Current)
	}
	if got.Previous == nil || got.Previous.CostUSD != 2 || got.Previous.Sessions != 1 {
		t.Fatalf("previous = %+v", got.Previous)
	}
}

func TestSummary_AllTimeHasNoPrevious(t *testing.T) {
	h := setup(t, row("a", "2026-10-20", 1))
	var got summaryResponse
	get(t, h, "/api/insights/summary?to=2026-10-30", &got)
	if got.Previous != nil || got.Current.CostUSD != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestSummary_TokensIncludeCache(t *testing.T) {
	r := row("a", "2026-10-20", 1)
	r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheCreationTokens = 1, 2, 4, 8
	r.AgentMs = 500
	h := setup(t, r)
	var got summaryResponse
	get(t, h, "/api/insights/summary?from=2026-10-01&to=2026-10-30", &got)
	if got.Current.Tokens != 15 || got.Current.AgentMs != 500 {
		t.Fatalf("current = %+v", got.Current)
	}
}

func TestSessions_DefaultSortCostDesc(t *testing.T) {
	h := setup(t, row("a", "2026-10-20", 1), row("b", "2026-10-21", 3), row("c", "2026-10-22", 2))
	var got sessionsResponse
	get(t, h, "/api/insights/sessions?to=2026-10-30", &got)
	if len(got.Rows) != 3 || got.Rows[0].SessionID != "b" || got.Rows[2].SessionID != "a" || got.Limit != 50 {
		t.Fatalf("got %+v", got)
	}
	if got.Rows[0].ModelCosts == nil || got.Rows[0].StartedAt != nil {
		t.Fatalf("modelCosts/startedAt wrong: %+v", got.Rows[0])
	}
	if got.Rows[0].RecordedAt != "2026-10-21T10:00:00Z" {
		t.Fatalf("recordedAt = %s", got.Rows[0].RecordedAt)
	}
}

func TestSessions_SortAgentMsAsc(t *testing.T) {
	a, b := row("a", "2026-10-20", 1), row("b", "2026-10-20", 1)
	a.AgentMs, b.AgentMs = 900, 100
	h := setup(t, a, b)
	var got sessionsResponse
	get(t, h, "/api/insights/sessions?to=2026-10-30&sort=agent_ms&order=asc", &got)
	if got.Rows[0].SessionID != "b" {
		t.Fatalf("got %+v", got.Rows)
	}
}

func TestSessions_Pagination(t *testing.T) {
	h := setup(t, row("a", "2026-10-20", 1), row("b", "2026-10-21", 3), row("c", "2026-10-22", 2))
	var got sessionsResponse
	get(t, h, "/api/insights/sessions?to=2026-10-30&limit=1&offset=1", &got)
	if got.Total != 3 || len(got.Rows) != 1 || got.Rows[0].SessionID != "c" || got.Offset != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestSessions_Rejects(t *testing.T) {
	h := setup(t)
	for _, q := range []string{
		"sort=cost%3Bdrop", "order=sideways", "from=2026-13-01&to=2026-10-30", "from=2026-10-30&to=2026-10-01",
		"to=2026-10-30&limit=201", "to=2026-10-30&limit=0", "to=2026-10-30&offset=-1", "to=bad",
	} {
		if c := get(t, h, "/api/insights/sessions?"+q, nil); c != 400 {
			t.Errorf("%q: status %d, want 400", q, c)
		}
	}
}

func TestSessions_ProjectWithQuote(t *testing.T) {
	a, b := row("a", "2026-10-20", 1), row("b", "2026-10-20", 1)
	a.ProjectPath = "/a'b"
	h := setup(t, a, b)
	var got sessionsResponse
	if c := get(t, h, "/api/insights/sessions?to=2026-10-30&project=/a'b", &got); c != 200 {
		t.Fatalf("status %d", c)
	}
	if got.Total != 1 || got.Rows[0].SessionID != "a" {
		t.Fatalf("got %+v", got)
	}
}

func TestProjects_DistinctInPeriod(t *testing.T) {
	a, b, c := row("a", "2026-10-20", 1), row("b", "2026-10-21", 1), row("c", "2026-10-22", 1)
	b.ProjectPath, b.ProjectName = "/q", "q"
	c.ProjectPath, c.ProjectName = "/q", "q"
	old := row("d", "2026-01-01", 1)
	old.ProjectPath = "/old"
	h := setup(t, a, b, c, old)
	var got []projectRow
	get(t, h, "/api/insights/projects?from=2026-10-01&to=2026-10-30", &got)
	if len(got) != 2 || got[0].ProjectPath != "/q" || got[0].Sessions != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestNilDB(t *testing.T) {
	r := chi.NewRouter()
	NewHandler(nil).Mount(r)
	var s sessionsResponse
	var p []projectRow
	var sum summaryResponse
	if get(t, r, "/api/insights/sessions", &s) != 200 || s.Rows == nil || s.Total != 0 {
		t.Fatalf("sessions %+v", s)
	}
	if get(t, r, "/api/insights/projects", &p) != 200 || p == nil {
		t.Fatalf("projects %+v", p)
	}
	if get(t, r, "/api/insights/summary", &sum) != 200 {
		t.Fatal("summary")
	}
}
