package hooks

import (
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/lx-wnk/kontor/server/internal/pathutil"
	"github.com/lx-wnk/kontor/server/internal/planusage"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// PlanUsageHandler serves the plan-usage ingress (POST, bearer-secret) and the
// summary read endpoint (GET, JWT/bypass).
type PlanUsageHandler struct {
	secret       string
	store        *planusage.Store
	broadcaster  *sse.PlanUsageBroadcaster
	staleMinutes func() int
}

// NewPlanUsageHandler creates a handler for plan-usage endpoints.
func NewPlanUsageHandler(secret string, store *planusage.Store, broadcaster *sse.PlanUsageBroadcaster, staleMinutes func() int) *PlanUsageHandler {
	return &PlanUsageHandler{
		secret:       secret,
		store:        store,
		broadcaster:  broadcaster,
		staleMinutes: staleMinutes,
	}
}

// planUsageBody is the JSON body the statusline hook script POSTs.
type planUsageBody struct {
	ConfigDir  string           `json:"config_dir"`
	RateLimits *rateLimitsBlock `json:"rate_limits"`
}

type rateLimitsBlock struct {
	FiveHour *rateLimitWindow `json:"five_hour"`
	SevenDay *rateLimitWindow `json:"seven_day"`
}

type rateLimitWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at"` // epoch seconds
}

// PlanUsage handles POST /api/hooks/plan-usage.
func (h *PlanUsageHandler) PlanUsage(w http.ResponseWriter, r *http.Request) {
	if !h.requireSecret(w, r) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEventBodyBytes))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body failed")
		return
	}
	var p planUsageBody
	if err := json.Unmarshal(body, &p); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// config_dir must be non-empty and absolute-or-tilde-prefixed.
	if p.ConfigDir == "" {
		jsonError(w, http.StatusBadRequest, "config_dir required")
		return
	}
	expanded := pathutil.ExpandLeadingTilde(p.ConfigDir)
	if !filepath.IsAbs(expanded) {
		jsonError(w, http.StatusBadRequest, "config_dir must be an absolute path or start with ~/")
		return
	}

	now := time.Now()
	var sample planusage.Sample
	sample.SampledAt = now

	if p.RateLimits != nil {
		if w5 := p.RateLimits.FiveHour; w5 != nil {
			win, ok := parseWindow(w5, now)
			if !ok {
				jsonError(w, http.StatusBadRequest, "five_hour: invalid used_percentage or resets_at")
				return
			}
			sample.FiveHour = win
		}
		if w7 := p.RateLimits.SevenDay; w7 != nil {
			win, ok := parseWindow(w7, now)
			if !ok {
				jsonError(w, http.StatusBadRequest, "seven_day: invalid used_percentage or resets_at")
				return
			}
			sample.SevenDay = win
		}
	}

	h.store.Set(p.ConfigDir, sample)
	if h.broadcaster != nil {
		h.broadcaster.Broadcast(sse.PlanUsageEvent{
			Type:    "plan_usage_updated",
			Payload: h.buildSummary(),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// Summary handles GET /api/plan-usage.
func (h *PlanUsageHandler) Summary(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(h.buildSummary())
}

type planUsageSummary struct {
	Accounts     map[string]planusage.Sample `json:"accounts"`
	StaleMinutes int                         `json:"staleMinutes"`
}

func (h *PlanUsageHandler) buildSummary() planUsageSummary {
	stale := 15
	if h.staleMinutes != nil {
		stale = h.staleMinutes()
	}
	return planUsageSummary{
		Accounts:     h.store.All(),
		StaleMinutes: stale,
	}
}

// Stream handles GET /api/plan-usage/stream (SSE).
func (h *PlanUsageHandler) Stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := h.broadcaster.Subscribe()
	defer h.broadcaster.Unsubscribe(ch)

	// Send initial state immediately.
	initial, _ := json.Marshal(sse.PlanUsageEvent{
		Type:    "plan_usage_updated",
		Payload: h.buildSummary(),
	})
	_, _ = w.Write([]byte("data: " + string(initial) + "\n\n"))
	flusher.Flush()

	for {
		select {
		case frame, ok := <-ch:
			if !ok {
				return
			}
			_, _ = w.Write(frame)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func parseWindow(raw *rateLimitWindow, now time.Time) (*planusage.Window, bool) {
	if raw.UsedPercentage == nil || raw.ResetsAt == nil {
		return nil, false
	}
	win := &planusage.Window{
		UsedPct:  *raw.UsedPercentage,
		ResetsAt: time.Unix(int64(*raw.ResetsAt), 0),
	}
	if !planusage.ValidWindow(win, now) {
		return nil, false
	}
	return win, true
}

func (h *PlanUsageHandler) requireSecret(w http.ResponseWriter, r *http.Request) bool {
	got := bearerToken(r)
	if len(got) == 0 || got != h.secret {
		jsonError(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	return true
}

func jsonError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
