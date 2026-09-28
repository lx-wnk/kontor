package repo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestAgentCostTrendRepo_Upsert(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAgentCostTrendRepo(client)

	now := time.Now().UTC().Truncate(time.Second)
	rows := []repo.AgentCostRow{
		{SessionID: "sess-1", Model: "claude-sonnet-4-6", InputTokens: 100, OutputTokens: 50, CostUSD: 0.001, RecordedAt: now.Add(-2 * time.Hour)},
		{SessionID: "sess-2", Model: "claude-haiku-4-5", InputTokens: 200, OutputTokens: 80, CostUSD: 0.002, RecordedAt: now.Add(-1 * time.Hour)},
		{SessionID: "sess-3", Model: "claude-opus-4-0", InputTokens: 300, OutputTokens: 120, CostUSD: 0.01, RecordedAt: now},
	}

	err := r.Upsert(t.Context(), rows)
	require.NoError(t, err)

	from := now.Add(-3 * time.Hour)
	to := now.Add(1 * time.Minute)
	results, err := r.ListByTimeRange(t.Context(), from, to)
	require.NoError(t, err)
	require.Len(t, results, 3)

	// Verify session IDs are all present.
	gotIDs := make(map[string]struct{}, 3)
	for _, res := range results {
		gotIDs[res.SessionID] = struct{}{}
	}
	for _, row := range rows {
		require.Contains(t, gotIDs, row.SessionID)
	}
}

func TestAgentCostTrendRepo_Upsert_Empty(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAgentCostTrendRepo(client)

	// Empty slice must be a no-op.
	err := r.Upsert(t.Context(), nil)
	require.NoError(t, err)
}

func TestAgentCostTrendRepo_Upsert_Idempotent(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAgentCostTrendRepo(client)

	now := time.Now().UTC().Truncate(time.Second)

	// First upsert — insert a row for session "S" with cost 1.0.
	first := []repo.AgentCostRow{
		{SessionID: "S", Model: "claude-sonnet-4-6", InputTokens: 100, OutputTokens: 50, CostUSD: 1.0, RecordedAt: now},
	}
	require.NoError(t, r.Upsert(t.Context(), first))

	// Second upsert — same session_id "S", different values including cost 2.0.
	second := []repo.AgentCostRow{
		{SessionID: "S", Model: "claude-opus-4-0", InputTokens: 200, OutputTokens: 80, CostUSD: 2.0, RecordedAt: now.Add(time.Minute)},
	}
	require.NoError(t, r.Upsert(t.Context(), second))

	// Expect exactly ONE row for "S" carrying the SECOND (updated) values.
	from := now.Add(-time.Hour)
	to := now.Add(time.Hour)
	results, err := r.ListByTimeRange(t.Context(), from, to)
	require.NoError(t, err)
	require.Len(t, results, 1, "expected exactly one row after upsert on same session_id")

	row := results[0]
	require.Equal(t, "S", row.SessionID)
	require.Equal(t, "claude-opus-4-0", row.Model)
	require.Equal(t, 200, row.InputTokens)
	require.Equal(t, 80, row.OutputTokens)
	require.InDelta(t, 2.0, row.CostUsd, 1e-9)
}

func TestAgentCostTrendRepo_ListSourceMtimes(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAgentCostTrendRepo(client)

	now := time.Now().UTC().Truncate(time.Second)
	rows := []repo.AgentCostRow{
		{SessionID: "sess-a", Model: "claude-sonnet-4-6", InputTokens: 10, OutputTokens: 5, CostUSD: 0.001, RecordedAt: now, SourceMtime: 111},
		{SessionID: "sess-b", Model: "claude-haiku-4-5", InputTokens: 20, OutputTokens: 8, CostUSD: 0.002, RecordedAt: now, SourceMtime: 222},
	}
	require.NoError(t, r.Upsert(t.Context(), rows))

	// Real ent Scan into the struct must map session_id/source_mtime via json tags.
	mtimes, err := r.ListSourceMtimes(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]int64{"sess-a": 111, "sess-b": 222}, mtimes)
}

func TestAgentCostTrendRepo_ListByTimeRange_Filtered(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewAgentCostTrendRepo(client)

	base := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	rows := []repo.AgentCostRow{
		{SessionID: "old-sess", Model: "claude-sonnet-4-6", InputTokens: 10, OutputTokens: 5, CostUSD: 0.0001, RecordedAt: base.Add(-24 * time.Hour)},
		{SessionID: "in-range", Model: "claude-sonnet-4-6", InputTokens: 20, OutputTokens: 10, CostUSD: 0.0002, RecordedAt: base},
		{SessionID: "future-sess", Model: "claude-sonnet-4-6", InputTokens: 30, OutputTokens: 15, CostUSD: 0.0003, RecordedAt: base.Add(24 * time.Hour)},
	}
	require.NoError(t, r.Upsert(t.Context(), rows))

	results, err := r.ListByTimeRange(t.Context(), base.Add(-1*time.Hour), base.Add(1*time.Hour))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "in-range", results[0].SessionID)
}
