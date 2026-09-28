package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/agentcosttrend"
)

// AgentCostRow is the input type for Upsert.
type AgentCostRow struct {
	SessionID    string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      float64
	RecordedAt   time.Time
	// New v2 fields — project grouping, cwd, and skip-unchanged support.
	Cwd         string
	ProjectPath string
	ProjectName string
	SourceMtime int64
}

// AgentCostTrendRepo defines read/write operations for the agent_cost_trend table.
type AgentCostTrendRepo interface {
	Upsert(ctx context.Context, rows []AgentCostRow) error
	ListByTimeRange(ctx context.Context, from, to time.Time) ([]*ent.AgentCostTrend, error)
	// ListSourceMtimes returns a map of session_id → source_mtime for every row.
	// It is used by the importer to decide which files can be skipped (mtime unchanged).
	ListSourceMtimes(ctx context.Context) (map[string]int64, error)
}

type entAgentCostTrendRepo struct{ client *ent.Client }

// NewAgentCostTrendRepo creates an AgentCostTrendRepo backed by the given ent client.
func NewAgentCostTrendRepo(client *ent.Client) AgentCostTrendRepo {
	return &entAgentCostTrendRepo{client: client}
}

// Upsert writes all rows, updating model, input_tokens, output_tokens, cost_usd,
// and recorded_at on conflict with an existing session_id. Empty slice is a no-op.
//
// ent's sql/upsert feature only generates OnConflict on the single-entity Create
// builder (not CreateBulk), so each row is upserted individually inside one
// transaction for atomicity. UpdateNewValues() refreshes every mutable column
// from the incoming row and leaves the existing id untouched.
func (r *entAgentCostTrendRepo) Upsert(ctx context.Context, rows []AgentCostRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("agentcosttrend.Upsert: begin tx: %w", err)
	}
	for _, row := range rows {
		if err := tx.AgentCostTrend.Create().
			SetID(uuid.New().String()).
			SetSessionID(row.SessionID).
			SetModel(row.Model).
			SetInputTokens(row.InputTokens).
			SetOutputTokens(row.OutputTokens).
			SetCostUsd(row.CostUSD).
			SetRecordedAt(row.RecordedAt).
			SetCwd(row.Cwd).
			SetProjectPath(row.ProjectPath).
			SetProjectName(row.ProjectName).
			SetSourceMtime(row.SourceMtime).
			OnConflictColumns(agentcosttrend.FieldSessionID).
			UpdateNewValues().
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("agentcosttrend.Upsert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("agentcosttrend.Upsert: commit: %w", err)
	}
	return nil
}

// ListByTimeRange returns all rows whose recorded_at is in [from, to].
func (r *entAgentCostTrendRepo) ListByTimeRange(ctx context.Context, from, to time.Time) ([]*ent.AgentCostTrend, error) {
	rows, err := r.client.AgentCostTrend.Query().
		Where(
			agentcosttrend.RecordedAtGTE(from),
			agentcosttrend.RecordedAtLTE(to),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("agentcosttrend.ListByTimeRange: %w", err)
	}
	return rows, nil
}

// ListSourceMtimes returns a session_id → source_mtime map for every row.
// The importer uses this to determine which files are unchanged and can be skipped.
func (r *entAgentCostTrendRepo) ListSourceMtimes(ctx context.Context) (map[string]int64, error) {
	type sessionMtime struct {
		SessionID   string `json:"session_id"`
		SourceMtime int64  `json:"source_mtime"`
	}
	var results []sessionMtime
	if err := r.client.AgentCostTrend.Query().
		Select(agentcosttrend.FieldSessionID, agentcosttrend.FieldSourceMtime).
		Scan(ctx, &results); err != nil {
		return nil, fmt.Errorf("agentcosttrend.ListSourceMtimes: %w", err)
	}
	m := make(map[string]int64, len(results))
	for _, row := range results {
		m[row.SessionID] = row.SourceMtime
	}
	return m, nil
}
