package insights

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/history"
)

const costStateLine = `{"type":"cost-state","totalCostUSD":3.2,"totalAPIDuration":1000,"totalToolDuration":500,"totalLinesAdded":7,"totalLinesRemoved":2,"modelUsage":{"claude-opus-4":{"costUSD":3.0},"claude-haiku-4-5":{"costUSD":0.2}}}`

func assistantUsage(ts, model, in, out string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","model":"` + model + `","usage":{"input_tokens":` + in + `,"output_tokens":` + out + `}}}`
}

func importOnce(t *testing.T, client *ent.Client, file string) {
	t.Helper()
	// The collect seam pins the scan to file so no real ~/.claude is read.
	imp := history.NewImporter(repo.NewAgentCostTrendRepo(client)).
		WithCollectFn(func(string) ([]string, error) { return []string{file}, nil })
	done := make(chan struct{})
	if err := imp.Run(t.Context(), func(p history.ImportProgress) {
		if p.Done {
			close(done)
		}
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("import did not finish")
	}
}

func onlySession(t *testing.T, h *chi.Mux) sessionRow {
	t.Helper()
	var got sessionsResponse
	if c := get(t, h, "/api/insights/sessions?to=2026-10-30", &got); c != 200 || len(got.Rows) != 1 {
		t.Fatalf("status %d, rows %+v", c, got.Rows)
	}
	return got.Rows[0]
}

func TestIntegration_ImportedSessionThroughSessionsAPI(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("DASHBOARD_CLAUDE_CONFIG_DIRS", home)
	projDir := filepath.Join(home, "projects", "-p")
	subDir := filepath.Join(projDir, "sess-int", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(projDir, "sess-int.jsonl")
	content := strings.Join([]string{
		`{"type":"user","timestamp":"2026-10-20T10:00:00Z","cwd":"/p","message":{"role":"user","content":"hi"}}`,
		assistantUsage("2026-10-20T10:00:05Z", "claude-opus-4", "100", "10"),
		costStateLine,
	}, "\n") + "\n"
	if err := os.WriteFile(parent, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "agent-a.jsonl"), []byte(assistantUsage("2026-10-20T10:00:02Z", "claude-haiku-4-5", "50", "5")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = bundle.Close() })
	h := chi.NewRouter()
	NewHandler(bundle.DB).Mount(h)
	importOnce(t, bundle.Client, parent)
	fresh := onlySession(t, h)
	if fresh.SessionID != "sess-int" || fresh.CostSource != repo.SourceCostState || fresh.CostUSD != 3.2 ||
		fresh.AgentMs != 1500 || fresh.AgentTimeSource != repo.SourceCostState {
		t.Fatalf("fresh row = %+v", fresh)
	}
	if fresh.InputTokens != 150 || fresh.OutputTokens != 15 {
		t.Fatalf("fresh tokens = %d/%d, want parent+subagent 150/15", fresh.InputTokens, fresh.OutputTokens)
	}

	f, err := os.OpenFile(parent, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(assistantUsage("2026-10-20T10:00:09Z", "claude-opus-4", "1", "1") + "\n")
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(parent, later, later); err != nil {
		t.Fatal(err)
	}

	importOnce(t, bundle.Client, parent)
	stale := onlySession(t, h)
	if stale.CostSource != repo.SourceEstimate || stale.AgentTimeSource != repo.SourceEstimate || stale.CostUSD == 3.2 || stale.CostUSD <= 0 {
		t.Fatalf("stale row = %+v", stale)
	}
	if stale.InputTokens != 151 || stale.OutputTokens != 16 {
		t.Fatalf("stale tokens = %d/%d, want 151/16", stale.InputTokens, stale.OutputTokens)
	}
	if stale.LinesAdded != 7 || stale.LinesRemoved != 2 || len(stale.ModelCosts) != 2 || stale.ModelCosts["claude-opus-4"] != 3.0 {
		t.Fatalf("stale row lost cost-state counters: %+v", stale)
	}
}
