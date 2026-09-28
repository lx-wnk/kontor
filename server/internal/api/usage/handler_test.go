package usage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	apiusage "github.com/lx-wnk/kontor/server/internal/api/usage"
	"github.com/lx-wnk/kontor/server/internal/settings"
	"github.com/lx-wnk/kontor/server/internal/usage"
)

// fakeSettingsRepo satisfies settings.Repo with an in-memory map.
type fakeSettingsRepo struct{ data map[string]string }

func (f *fakeSettingsRepo) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := f.data[key]
	return v, ok, nil
}
func (f *fakeSettingsRepo) Set(_ context.Context, _, _ string) error          { return nil }
func (f *fakeSettingsRepo) SetSecret(_ context.Context, _, _, _ string) error { return nil }
func (f *fakeSettingsRepo) GetSecret(_ context.Context, key string) (string, string, bool, error) {
	v, ok := f.data[key]
	return v, "", ok, nil
}
func (f *fakeSettingsRepo) ListAll(_ context.Context) (map[string]string, error) {
	return f.data, nil
}

func buildHandler(t *testing.T, settingsData map[string]string, configDir string) http.Handler {
	t.Helper()
	svc := settings.New(&fakeSettingsRepo{data: settingsData}, nil)
	if err := svc.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	agg := usage.NewAggregator(usage.Options{
		ConfigDirs: func() []string { return []string{configDir} },
		Now:        func() time.Time { return now },
	})
	return apiusage.NewHandler(svc, agg)
}

func writeAssistantLine(t *testing.T, path string, ts time.Time, input, output int) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755) //nolint:errcheck
	line := `{"timestamp":"` + ts.UTC().Format(time.RFC3339Nano) + `","message":{"role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":` +
		itoa(input) + `,"output_tokens":` + itoa(output) + `,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	os.WriteFile(path, []byte(line+"\n"), 0o644) //nolint:errcheck
}

func itoa(n int) string {
	return fmt.Sprintf("%d", n)
}

func TestHandler_NoBudget(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	writeAssistantLine(t,
		filepath.Join(dir, "projects", "p", "aaaaaaaa-0000-0000-0000-000000000010.jsonl"),
		now.Add(-30*time.Minute), 500, 200)

	h := buildHandler(t, map[string]string{}, dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d", rec.Code)
	}
	var resp struct {
		Windows []struct {
			Key          string   `json:"key"`
			Tokens       int64    `json:"tokens"`
			BudgetTokens *int64   `json:"budgetTokens"`
			Pct          *float64 `json:"pct"`
		} `json:"windows"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Windows) != 2 {
		t.Fatalf("expected 2 windows, got %d", len(resp.Windows))
	}
	w5h := resp.Windows[0]
	if w5h.Key != "5h" {
		t.Errorf("first window key: got %q, want %q", w5h.Key, "5h")
	}
	if w5h.Tokens != 700 { // 500 + 200
		t.Errorf("5h tokens: got %d, want 700", w5h.Tokens)
	}
	if w5h.BudgetTokens != nil || w5h.Pct != nil {
		t.Error("expected nil budgetTokens and pct when no budget set")
	}
}

func TestHandler_WithBudget(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	writeAssistantLine(t,
		filepath.Join(dir, "projects", "p", "aaaaaaaa-0000-0000-0000-000000000011.jsonl"),
		now.Add(-30*time.Minute), 1000, 0)

	// session budget = 10000 tokens; pct = 1000/10000 = 0.1
	h := buildHandler(t, map[string]string{"usage.budget.session": "10000"}, dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/usage", nil))

	var resp struct {
		Windows []struct {
			Key          string   `json:"key"`
			BudgetTokens *int64   `json:"budgetTokens"`
			Pct          *float64 `json:"pct"`
		} `json:"windows"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	w5h := resp.Windows[0]
	if w5h.BudgetTokens == nil {
		t.Fatal("expected non-nil budgetTokens")
	}
	if *w5h.BudgetTokens != 10000 {
		t.Errorf("budgetTokens: got %d, want 10000", *w5h.BudgetTokens)
	}
	if w5h.Pct == nil {
		t.Fatal("expected non-nil pct")
	}
	if got := *w5h.Pct; got < 0.09 || got > 0.11 {
		t.Errorf("pct: got %f, want ~0.1", got)
	}
}
