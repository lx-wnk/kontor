package repo_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestEvalMetricRepo_Insert_And_ListByMetric(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewEvalMetricRepo(client)

	base := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	rows := []repo.EvalMetricSnapshotRow{
		{SpawnerID: "sp1", Model: "claude-sonnet", Stage: "backlog", MetricKey: "success_rate", Value: 0.95, SampleCount: 20, WindowStart: base.Add(-1 * time.Hour), WindowEnd: base, RecordedAt: base},
		{SpawnerID: "sp1", Model: "claude-sonnet", Stage: "backlog", MetricKey: "cost_cents", Value: 42.0, SampleCount: 20, WindowStart: base.Add(-1 * time.Hour), WindowEnd: base, RecordedAt: base},
		{SpawnerID: "sp2", Model: "default", Stage: "implement", MetricKey: "success_rate", Value: 0.80, SampleCount: 10, WindowStart: base.Add(-2 * time.Hour), WindowEnd: base.Add(-1 * time.Hour), RecordedAt: base.Add(-30 * time.Minute)},
	}
	require.NoError(t, r.Insert(t.Context(), rows))

	// ListByMetric uses a half-open [from, to) window; bound just past base to include it.
	results, err := r.ListByMetric(t.Context(), "success_rate", base, base.Add(time.Nanosecond))
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "success_rate", results[0].MetricKey)
	require.Equal(t, "sp1", results[0].SpawnerID)

	// ListByMetric with wider window catches both success_rate rows (recorded_at base-30m and base).
	all, err := r.ListByMetric(t.Context(), "success_rate", base.Add(-1*time.Hour), base.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestEvalMetricRepo_Insert_Empty(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewEvalMetricRepo(client)

	require.NoError(t, r.Insert(t.Context(), nil))
}

func TestEvalMetricRepo_ListByTimeRange(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewEvalMetricRepo(client)

	base := time.Date(2025, 4, 1, 10, 0, 0, 0, time.UTC)
	rows := []repo.EvalMetricSnapshotRow{
		{SpawnerID: "sp1", Model: "m1", Stage: "backlog", MetricKey: "k1", Value: 1.0, SampleCount: 5, WindowStart: base.Add(-1 * time.Hour), WindowEnd: base, RecordedAt: base.Add(-2 * time.Hour)},
		{SpawnerID: "sp1", Model: "m1", Stage: "backlog", MetricKey: "k1", Value: 2.0, SampleCount: 5, WindowStart: base, WindowEnd: base.Add(time.Hour), RecordedAt: base},
		{SpawnerID: "sp1", Model: "m1", Stage: "backlog", MetricKey: "k1", Value: 3.0, SampleCount: 5, WindowStart: base.Add(time.Hour), WindowEnd: base.Add(2 * time.Hour), RecordedAt: base.Add(2 * time.Hour)},
	}
	require.NoError(t, r.Insert(t.Context(), rows))

	// Only the middle row (recorded_at=base) falls in [base-1h, base+1h].
	// Row 0 has recorded_at=base-2h (outside), row 2 has recorded_at=base+2h (outside).
	results, err := r.ListByTimeRange(t.Context(), base.Add(-1*time.Hour), base.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, results, 1)

	// Narrow to just base (half-open [base, base+1ns)).
	exact, err := r.ListByTimeRange(t.Context(), base, base.Add(time.Nanosecond))
	require.NoError(t, err)
	require.Len(t, exact, 1)
	require.InDelta(t, 2.0, exact[0].Value, 1e-9)
}
