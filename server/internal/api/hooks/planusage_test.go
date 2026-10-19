package hooks

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/planusage"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

const planUsageTestSecret = "test-secret-value"

func newTestPlanUsageHandler() *PlanUsageHandler {
	store := planusage.NewStore()
	broadcaster := sse.NewPlanUsageBroadcaster(sse.NewBroadcaster())
	return NewPlanUsageHandler(planUsageTestSecret, store, broadcaster, func() int { return 15 })
}

// futureEpoch returns an epoch seconds value 3 hours from now (within the
// ValidWindow [now-24h, now+8d] range).
func futureEpoch() int64 { return time.Now().Add(3 * time.Hour).Unix() }

func TestPlanUsage_401_NoBearer(t *testing.T) {
	h := newTestPlanUsageHandler()
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestPlanUsage_401_WrongBearer(t *testing.T) {
	h := newTestPlanUsageHandler()
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer wrong-secret")
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestPlanUsage_400_EmptyConfigDir(t *testing.T) {
	h := newTestPlanUsageHandler()
	body := `{"config_dir":"","rate_limits":{}}`
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPlanUsage_400_RelativeConfigDir(t *testing.T) {
	h := newTestPlanUsageHandler()
	body := `{"config_dir":"relative/path","rate_limits":{}}`
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPlanUsage_400_InvalidPct(t *testing.T) {
	h := newTestPlanUsageHandler()
	body := fmt.Sprintf(`{"config_dir":"/home/.claude","rate_limits":{"five_hour":{"used_percentage":150,"resets_at":%d}}}`, futureEpoch())
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for pct>100, got %d", w.Code)
	}
}

func TestPlanUsage_400_InvalidJSON(t *testing.T) {
	h := newTestPlanUsageHandler()
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(`not json`))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestPlanUsage_204_ValidSample(t *testing.T) {
	h := newTestPlanUsageHandler()
	epoch := futureEpoch()
	body := fmt.Sprintf(`{"config_dir":"/home/user/.claude","rate_limits":{"five_hour":{"used_percentage":42.5,"resets_at":%d},"seven_day":{"used_percentage":10,"resets_at":%d}}}`, epoch, epoch)
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d; body: %s", w.Code, w.Body.String())
	}

	sample, ok := h.store.Get("/home/user/.claude")
	if !ok {
		t.Fatal("sample not stored")
	}
	if sample.FiveHour == nil || sample.FiveHour.UsedPct != 42.5 {
		t.Errorf("FiveHour.UsedPct = %v, want 42.5", sample.FiveHour)
	}
}

func TestPlanUsage_204_NilRateLimits(t *testing.T) {
	h := newTestPlanUsageHandler()
	body := `{"config_dir":"/home/user/.claude"}`
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestPlanUsage_204_TildeConfigDir(t *testing.T) {
	h := newTestPlanUsageHandler()
	body := fmt.Sprintf(`{"config_dir":"~/.claude-personal","rate_limits":{"five_hour":{"used_percentage":50,"resets_at":%d}}}`, futureEpoch())
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}

func TestPlanUsageSummary_Shape(t *testing.T) {
	h := newTestPlanUsageHandler()
	epoch := futureEpoch()
	body := fmt.Sprintf(`{"config_dir":"/home/user/.claude","rate_limits":{"five_hour":{"used_percentage":42.5,"resets_at":%d}}}`, epoch)
	req := httptest.NewRequest("POST", "/api/hooks/plan-usage", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+planUsageTestSecret)
	w := httptest.NewRecorder()
	h.PlanUsage(w, req)

	getReq := httptest.NewRequest("GET", "/api/plan-usage", nil)
	getW := httptest.NewRecorder()
	h.Summary(getW, getReq)

	if getW.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", getW.Code)
	}
	var summary struct {
		Accounts     map[string]json.RawMessage `json:"accounts"`
		StaleMinutes int                        `json:"staleMinutes"`
	}
	if err := json.Unmarshal(getW.Body.Bytes(), &summary); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if summary.StaleMinutes != 15 {
		t.Errorf("staleMinutes = %d, want 15", summary.StaleMinutes)
	}
	if len(summary.Accounts) != 1 {
		t.Errorf("accounts count = %d, want 1", len(summary.Accounts))
	}
}
