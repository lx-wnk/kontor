package repo_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func alertRow(spawnerID, model, stage, metricKey, direction string, baseline, recent, delta, threshold float64, samples int) repo.DriftAlertRow {
	return repo.DriftAlertRow{
		SpawnerID:     spawnerID,
		Model:         model,
		Stage:         stage,
		MetricKey:     metricKey,
		Direction:     direction,
		BaselineValue: baseline,
		RecentValue:   recent,
		Delta:         delta,
		Threshold:     threshold,
		SampleCount:   samples,
	}
}

func TestDriftAlertRepo_UpsertOpen_Creates(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewDriftAlertRepo(client)

	row := alertRow("sp1", "claude-sonnet", "backlog", "success_rate", "down", 0.95, 0.70, -0.25, 0.10, 20)
	require.NoError(t, r.UpsertOpen(t.Context(), []repo.DriftAlertRow{row}))

	alerts, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.Len(t, alerts, 1)
	require.Equal(t, "open", alerts[0].Status)
	require.Equal(t, "down", alerts[0].Direction)
	require.InDelta(t, 0.70, alerts[0].RecentValue, 1e-9)
}

func TestDriftAlertRepo_UpsertOpen_NoDuplicate(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewDriftAlertRepo(client)

	row := alertRow("sp1", "claude-sonnet", "backlog", "success_rate", "down", 0.95, 0.70, -0.25, 0.10, 20)
	require.NoError(t, r.UpsertOpen(t.Context(), []repo.DriftAlertRow{row}))

	// Second UpsertOpen for same dimension + metric_key should update, not create a second row.
	updated := alertRow("sp1", "claude-sonnet", "backlog", "success_rate", "down", 0.95, 0.60, -0.35, 0.10, 30)
	require.NoError(t, r.UpsertOpen(t.Context(), []repo.DriftAlertRow{updated}))

	alerts, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.Len(t, alerts, 1, "expected exactly one open alert after second UpsertOpen on same dimension")
	require.InDelta(t, 0.60, alerts[0].RecentValue, 1e-9)
	require.Equal(t, 30, alerts[0].SampleCount)
}

func TestDriftAlertRepo_UpsertOpen_Empty(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewDriftAlertRepo(client)

	require.NoError(t, r.UpsertOpen(t.Context(), nil))
}

func TestDriftAlertRepo_ListByStatus(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewDriftAlertRepo(client)

	rows := []repo.DriftAlertRow{
		alertRow("sp1", "m1", "backlog", "metric_a", "down", 1, 0.5, -0.5, 0.1, 10),
		alertRow("sp2", "m2", "implement", "metric_b", "up", 1, 1.5, 0.5, 0.1, 10),
	}
	require.NoError(t, r.UpsertOpen(t.Context(), rows))

	open, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.Len(t, open, 2)

	acked, err := r.ListByStatus(t.Context(), "acknowledged")
	require.NoError(t, err)
	require.Len(t, acked, 0)
}

func TestDriftAlertRepo_Acknowledge(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewDriftAlertRepo(client)

	row := alertRow("sp1", "claude-sonnet", "backlog", "success_rate", "down", 0.95, 0.70, -0.25, 0.10, 20)
	require.NoError(t, r.UpsertOpen(t.Context(), []repo.DriftAlertRow{row}))

	alerts, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.Len(t, alerts, 1)

	require.NoError(t, r.Acknowledge(t.Context(), alerts[0].ID))

	open, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.Len(t, open, 0)

	acked, err := r.ListByStatus(t.Context(), "acknowledged")
	require.NoError(t, err)
	require.Len(t, acked, 1)
	require.Equal(t, "acknowledged", acked[0].Status)
	require.NotNil(t, acked[0].AcknowledgedAt)
}

func TestDriftAlertRepo_NewOpenAfterAck(t *testing.T) {
	client := openTestDB(t)
	r := repo.NewDriftAlertRepo(client)

	row := alertRow("sp1", "claude-sonnet", "backlog", "success_rate", "down", 0.95, 0.70, -0.25, 0.10, 20)
	require.NoError(t, r.UpsertOpen(t.Context(), []repo.DriftAlertRow{row}))

	alerts, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.NoError(t, r.Acknowledge(t.Context(), alerts[0].ID))

	// After acknowledgement, a new UpsertOpen for the same dimension should create a fresh open row.
	require.NoError(t, r.UpsertOpen(t.Context(), []repo.DriftAlertRow{row}))

	open, err := r.ListByStatus(t.Context(), "open")
	require.NoError(t, err)
	require.Len(t, open, 1, "expected a new open alert after ack + re-upsert")

	all, err := r.ListByStatus(t.Context(), "open", "acknowledged")
	require.NoError(t, err)
	require.Len(t, all, 2, "expected one acknowledged + one open")
}
