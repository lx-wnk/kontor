package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/evalmetricsnapshot"
)

// maxSnapshotRows caps read queries so an unbounded lookback window cannot pull
// the entire (growing) snapshot table into memory.
const maxSnapshotRows = 50000

// EvalMetricSnapshotRow is the input type for Insert.
type EvalMetricSnapshotRow struct {
	SpawnerID   string
	Model       string
	Stage       string
	MetricKey   string
	Value       float64
	SampleCount int
	WindowStart time.Time
	WindowEnd   time.Time
	RecordedAt  time.Time
}

// EvalMetricRepo defines read/write operations for the eval_metric_snapshot table.
type EvalMetricRepo interface {
	Insert(ctx context.Context, rows []EvalMetricSnapshotRow) error
	ListByMetric(ctx context.Context, metricKey string, from, to time.Time) ([]*ent.EvalMetricSnapshot, error)
	ListByTimeRange(ctx context.Context, from, to time.Time) ([]*ent.EvalMetricSnapshot, error)
}

type entEvalMetricRepo struct{ client *ent.Client }

// NewEvalMetricRepo creates an EvalMetricRepo backed by the given ent client.
func NewEvalMetricRepo(client *ent.Client) EvalMetricRepo {
	return &entEvalMetricRepo{client: client}
}

// Insert appends all rows in a single transaction. Snapshots are append-only — no conflict handling.
func (r *entEvalMetricRepo) Insert(ctx context.Context, rows []EvalMetricSnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("evalmetric.Insert: begin tx: %w", err)
	}
	for _, row := range rows {
		if err := tx.EvalMetricSnapshot.Create().
			SetID(uuid.New().String()).
			SetSpawnerID(row.SpawnerID).
			SetModel(row.Model).
			SetStage(row.Stage).
			SetMetricKey(row.MetricKey).
			SetValue(row.Value).
			SetSampleCount(row.SampleCount).
			SetWindowStart(row.WindowStart).
			SetWindowEnd(row.WindowEnd).
			SetRecordedAt(row.RecordedAt).
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("evalmetric.Insert: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("evalmetric.Insert: commit: %w", err)
	}
	return nil
}

// ListByMetric returns snapshots for a specific metric_key whose recorded_at is
// in the half-open range [from, to).
func (r *entEvalMetricRepo) ListByMetric(ctx context.Context, metricKey string, from, to time.Time) ([]*ent.EvalMetricSnapshot, error) {
	rows, err := r.client.EvalMetricSnapshot.Query().
		Where(
			evalmetricsnapshot.MetricKey(metricKey),
			evalmetricsnapshot.RecordedAtGTE(from),
			evalmetricsnapshot.RecordedAtLT(to),
		).
		Limit(maxSnapshotRows).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("evalmetric.ListByMetric: %w", err)
	}
	return rows, nil
}

// ListByTimeRange returns all snapshots whose recorded_at is in the half-open
// range [from, to). The exclusive upper bound keeps the baseline and recent
// windows disjoint so a boundary snapshot is never counted twice.
func (r *entEvalMetricRepo) ListByTimeRange(ctx context.Context, from, to time.Time) ([]*ent.EvalMetricSnapshot, error) {
	rows, err := r.client.EvalMetricSnapshot.Query().
		Where(
			evalmetricsnapshot.RecordedAtGTE(from),
			evalmetricsnapshot.RecordedAtLT(to),
		).
		Limit(maxSnapshotRows).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("evalmetric.ListByTimeRange: %w", err)
	}
	return rows, nil
}
