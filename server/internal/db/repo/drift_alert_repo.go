package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/driftalert"
)

// Drift alert status vocabulary — single source of truth.
const (
	DriftStatusOpen         = "open"
	DriftStatusAcknowledged = "acknowledged"
)

// DriftAlertRow is the input type for UpsertOpen.
type DriftAlertRow struct {
	SpawnerID     string
	Model         string
	Stage         string
	MetricKey     string
	Direction     string
	BaselineValue float64
	RecentValue   float64
	Delta         float64
	Threshold     float64
	SampleCount   int
}

// DriftAlertRepo defines read/write operations for the drift_alert table.
type DriftAlertRepo interface {
	// UpsertOpen ensures at most one open alert per (spawner_id, model, stage, metric_key).
	// If an open alert already exists for that dimension, it is updated in place;
	// otherwise a new row is created with status="open".
	UpsertOpen(ctx context.Context, rows []DriftAlertRow) error
	ListByStatus(ctx context.Context, statuses ...string) ([]*ent.DriftAlert, error)
	Acknowledge(ctx context.Context, id string) error
}

type entDriftAlertRepo struct{ client *ent.Client }

// NewDriftAlertRepo creates a DriftAlertRepo backed by the given ent client.
func NewDriftAlertRepo(client *ent.Client) DriftAlertRepo {
	return &entDriftAlertRepo{client: client}
}

// UpsertOpen creates or updates an open alert for each row.
// SQLite does not support referencing a partial unique index as an ON CONFLICT target,
// so we use a query-then-update-or-create pattern inside a single transaction.
func (r *entDriftAlertRepo) UpsertOpen(ctx context.Context, rows []DriftAlertRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("driftalert.UpsertOpen: begin tx: %w", err)
	}
	now := time.Now()
	for _, row := range rows {
		existing, err := tx.DriftAlert.Query().
			Where(
				driftalert.SpawnerID(row.SpawnerID),
				driftalert.Model(row.Model),
				driftalert.Stage(row.Stage),
				driftalert.MetricKey(row.MetricKey),
				driftalert.Status(DriftStatusOpen),
			).
			Only(ctx)
		if err != nil && !ent.IsNotFound(err) {
			_ = tx.Rollback()
			return fmt.Errorf("driftalert.UpsertOpen: query: %w", err)
		}

		if existing != nil {
			if err := tx.DriftAlert.UpdateOneID(existing.ID).
				SetDirection(row.Direction).
				SetBaselineValue(row.BaselineValue).
				SetRecentValue(row.RecentValue).
				SetDelta(row.Delta).
				SetThreshold(row.Threshold).
				SetSampleCount(row.SampleCount).
				SetDetectedAt(now).
				Exec(ctx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("driftalert.UpsertOpen: update: %w", err)
			}
		} else {
			if err := tx.DriftAlert.Create().
				SetID(uuid.New().String()).
				SetSpawnerID(row.SpawnerID).
				SetModel(row.Model).
				SetStage(row.Stage).
				SetMetricKey(row.MetricKey).
				SetStatus(DriftStatusOpen).
				SetDirection(row.Direction).
				SetBaselineValue(row.BaselineValue).
				SetRecentValue(row.RecentValue).
				SetDelta(row.Delta).
				SetThreshold(row.Threshold).
				SetSampleCount(row.SampleCount).
				SetDetectedAt(now).
				Exec(ctx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("driftalert.UpsertOpen: create: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("driftalert.UpsertOpen: commit: %w", err)
	}
	return nil
}

// maxAlertRows caps drift-alert read queries to bound memory use.
const maxAlertRows = 10000

// ListByStatus returns all drift alerts matching any of the given statuses.
func (r *entDriftAlertRepo) ListByStatus(ctx context.Context, statuses ...string) ([]*ent.DriftAlert, error) {
	rows, err := r.client.DriftAlert.Query().
		Where(driftalert.StatusIn(statuses...)).
		Limit(maxAlertRows).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("driftalert.ListByStatus: %w", err)
	}
	return rows, nil
}

// Acknowledge sets status=acknowledged and acknowledged_at=now for the given alert ID.
// The returned error is ent.IsNotFound when no row matches id; callers map it to 404.
func (r *entDriftAlertRepo) Acknowledge(ctx context.Context, id string) error {
	if err := r.client.DriftAlert.UpdateOneID(id).
		SetStatus(DriftStatusAcknowledged).
		SetAcknowledgedAt(time.Now()).
		Exec(ctx); err != nil {
		return fmt.Errorf("driftalert.Acknowledge: %w", err)
	}
	return nil
}
