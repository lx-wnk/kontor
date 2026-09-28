package eval

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// ErrScanInProgress is returned by Scan when another scan already holds the
// single-instance guard. Callers (ticker, HTTP endpoint) treat it as "busy".
var ErrScanInProgress = errors.New("eval: scan already in progress")

// Service orchestrates periodic metric collection, snapshot persistence, and drift detection.
type Service struct {
	collector   *Collector
	metricRepo  repo.EvalMetricRepo
	alertRepo   repo.DriftAlertRepo
	th          Thresholds
	windowHours int
	onDrift     func([]DriftFinding)
	clock       func() time.Time
	mu          sync.Mutex
	running     bool
}

// NewService creates a Service with the given repos and thresholds.
// clock defaults to time.Now; onDrift defaults to nil (no-op).
func NewService(
	collector *Collector,
	metricRepo repo.EvalMetricRepo,
	alertRepo repo.DriftAlertRepo,
	th Thresholds,
	windowHours int,
) *Service {
	return &Service{
		collector:   collector,
		metricRepo:  metricRepo,
		alertRepo:   alertRepo,
		th:          th,
		windowHours: windowHours,
		clock:       time.Now,
	}
}

// WithOnDrift returns a new Service identical to s but with fn as the drift callback.
// fn is the ONLY outward hook — eval/ remains notifications-agnostic.
func (s *Service) WithOnDrift(fn func([]DriftFinding)) *Service {
	return &Service{
		collector:   s.collector,
		metricRepo:  s.metricRepo,
		alertRepo:   s.alertRepo,
		th:          s.th,
		windowHours: s.windowHours,
		onDrift:     fn,
		clock:       s.clock,
	}
}

// WithClock returns a new Service identical to s but with fn as the clock function.
func (s *Service) WithClock(fn func() time.Time) *Service {
	return &Service{
		collector:   s.collector,
		metricRepo:  s.metricRepo,
		alertRepo:   s.alertRepo,
		th:          s.th,
		windowHours: s.windowHours,
		onDrift:     s.onDrift,
		clock:       fn,
	}
}

// Scan acquires the single-instance guard, then collects recent metrics, persists
// them as snapshots, builds a baseline from prior snapshot history, detects drift,
// and fires the onDrift callback if set. It returns ErrScanInProgress if another
// scan is already running, so a manual POST /api/eval/scan can never race the ticker.
// Baseline is derived from prior snapshots, so no alerts fire until ~2*window of
// history exists — cold-start silence is expected behaviour, not a bug.
func (s *Service) Scan(ctx context.Context) error {
	if !s.tryLock() {
		return ErrScanInProgress
	}
	defer s.unlock()
	return s.scan(ctx)
}

func (s *Service) scan(ctx context.Context) error {
	now := s.clock()
	W := time.Duration(s.windowHours) * time.Hour
	recentFrom := now.Add(-W)
	baseFrom := now.Add(-2 * W)
	baseTo := recentFrom

	recent, err := s.collector.Collect(ctx, recentFrom, now)
	if err != nil {
		return fmt.Errorf("eval.Scan: collect: %w", err)
	}

	// Persist recent metrics as snapshots (chart history + future baseline source).
	var snapshots []repo.EvalMetricSnapshotRow
	for dim, metrics := range recent {
		for _, mv := range metrics {
			snapshots = append(snapshots, repo.EvalMetricSnapshotRow{
				SpawnerID:   dim.SpawnerID,
				Model:       dim.Model,
				Stage:       dim.Stage,
				MetricKey:   mv.Key,
				Value:       mv.Value,
				SampleCount: mv.SampleCount,
				WindowStart: recentFrom,
				WindowEnd:   now,
				RecordedAt:  now,
			})
		}
	}
	if err := s.metricRepo.Insert(ctx, snapshots); err != nil {
		return fmt.Errorf("eval.Scan: insert snapshots: %w", err)
	}

	historicalRows, err := s.metricRepo.ListByTimeRange(ctx, baseFrom, baseTo)
	if err != nil {
		return fmt.Errorf("eval.Scan: list baseline snapshots: %w", err)
	}

	baseline := buildBaseline(historicalRows)
	findings := DetectDrift(recent, baseline, s.th)

	if len(findings) > 0 {
		alertRows := make([]repo.DriftAlertRow, len(findings))
		for i, f := range findings {
			alertRows[i] = repo.DriftAlertRow{
				SpawnerID:     f.Dim.SpawnerID,
				Model:         f.Dim.Model,
				Stage:         f.Dim.Stage,
				MetricKey:     f.MetricKey,
				Direction:     f.Direction,
				BaselineValue: f.BaselineValue,
				RecentValue:   f.RecentValue,
				Delta:         f.Delta,
				Threshold:     f.Threshold,
				SampleCount:   f.SampleCount,
			}
		}
		if err := s.alertRepo.UpsertOpen(ctx, alertRows); err != nil {
			return fmt.Errorf("eval.Scan: upsert alerts: %w", err)
		}
		if s.onDrift != nil {
			s.onDrift(findings)
		}
	}

	return nil
}

// RunLoop runs one Scan immediately (boot scan), then rescans on every ticker tick
// until ctx is cancelled. If interval <= 0 the boot scan runs synchronously and
// RunLoop returns immediately after it completes (boot-only mode).
// An overlapping tick is skipped, not stacked.
func (s *Service) RunLoop(ctx context.Context, interval time.Duration) {
	slog.Info("eval.scheduler: starting drift-detection scanner", "interval", interval)

	if err := s.Scan(ctx); err != nil {
		slog.Warn("eval.scheduler: boot scan error", "err", err)
	}

	if interval <= 0 {
		return
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			go func() {
				switch err := s.Scan(ctx); {
				case errors.Is(err, ErrScanInProgress):
					slog.Debug("eval.scheduler: tick skipped, scan still in progress")
				case err != nil:
					slog.Warn("eval.scheduler: scan error", "err", err)
				}
			}()
		}
	}
}

func (s *Service) tryLock() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *Service) unlock() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

// baselineKey groups snapshot rows for baseline computation.
type baselineKey struct {
	Dimension
	MetricKey string
}

// buildBaseline aggregates snapshot rows into per-(Dimension, MetricKey) Baselines.
// Mean is the arithmetic mean of snapshot values; Stddev is population stddev;
// SampleCount is the SUM of each snapshot's SampleCount (the underlying stage_run count).
func buildBaseline(rows []*ent.EvalMetricSnapshot) map[Dimension]map[string]Baseline {
	// Group values and sample counts by (dimension, metric).
	type accumulator struct {
		values      []float64
		sampleCount int
	}
	groups := make(map[baselineKey]*accumulator)
	for _, row := range rows {
		k := baselineKey{
			Dimension: Dimension{
				SpawnerID: row.SpawnerID,
				Model:     row.Model,
				Stage:     row.Stage,
			},
			MetricKey: row.MetricKey,
		}
		a := groups[k]
		if a == nil {
			a = &accumulator{}
			groups[k] = a
		}
		a.values = append(a.values, row.Value)
		a.sampleCount += row.SampleCount
	}

	result := make(map[Dimension]map[string]Baseline, len(groups))
	for k, a := range groups {
		mean := arithmeticMean(a.values)
		stddev := populationStddev(a.values, mean)
		if result[k.Dimension] == nil {
			result[k.Dimension] = make(map[string]Baseline)
		}
		result[k.Dimension][k.MetricKey] = Baseline{
			Mean:        mean,
			Stddev:      stddev,
			SampleCount: a.sampleCount,
		}
	}
	return result
}

func arithmeticMean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

func populationStddev(vals []float64, mean float64) float64 {
	if len(vals) < 2 {
		return 0
	}
	sumSq := 0.0
	for _, v := range vals {
		d := v - mean
		sumSq += d * d
	}
	return math.Sqrt(sumSq / float64(len(vals)))
}
