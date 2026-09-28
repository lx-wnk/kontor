package usage

import (
	"encoding/json"
	"net/http"

	"github.com/lx-wnk/kontor/server/internal/settings"
	"github.com/lx-wnk/kontor/server/internal/usage"
)

// Handler serves GET /api/usage.
type Handler struct {
	agg *usage.Aggregator
	svc *settings.Service
}

// NewHandler constructs the handler. agg may be nil; NewAggregator with default
// Options is used in that case (production path).
func NewHandler(svc *settings.Service, agg *usage.Aggregator) *Handler {
	if agg == nil {
		agg = usage.NewAggregator(usage.Options{})
	}
	return &Handler{agg: agg, svc: svc}
}

type windowDTO struct {
	Key          string   `json:"key"`
	Tokens       int64    `json:"tokens"`
	CostCents    int64    `json:"costCents"`
	BudgetTokens *int64   `json:"budgetTokens"`
	Pct          *float64 `json:"pct"`
}

type windowDetailDTO struct {
	Tokens    int64 `json:"tokens"`
	CostCents int64 `json:"costCents"`
}

type accountDTO struct {
	Label string          `json:"label"`
	W5h   windowDetailDTO `json:"w5h"`
	W7d   windowDetailDTO `json:"w7d"`
}

type responseDTO struct {
	Windows  []windowDTO  `json:"windows"`
	Accounts []accountDTO `json:"accounts,omitempty"`
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	res, err := h.agg.Aggregate()
	if err != nil {
		http.Error(w, "usage scan failed", http.StatusInternalServerError)
		return
	}

	sessionBudget := h.svc.Int("usage.budget.session")
	weeklyBudget := h.svc.Int("usage.budget.weekly")

	resp := responseDTO{
		Windows: []windowDTO{
			makeWindowDTO("5h", res.W5h, sessionBudget),
			makeWindowDTO("7d", res.W7d, weeklyBudget),
		},
	}
	if len(res.Accounts) > 1 {
		for _, acc := range res.Accounts {
			resp.Accounts = append(resp.Accounts, accountDTO{
				Label: acc.Label,
				W5h:   windowDetailDTO{Tokens: acc.W5h.Tokens, CostCents: acc.W5h.CostCents},
				W7d:   windowDetailDTO{Tokens: acc.W7d.Tokens, CostCents: acc.W7d.CostCents},
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func makeWindowDTO(key string, wu usage.WindowUsage, budget int) windowDTO {
	d := windowDTO{Key: key, Tokens: wu.Tokens, CostCents: wu.CostCents}
	if budget > 0 {
		b := int64(budget)
		pct := float64(wu.Tokens) / float64(b)
		if pct > 1 {
			pct = 1
		}
		d.BudgetTokens = &b
		d.Pct = &pct
	}
	return d
}
