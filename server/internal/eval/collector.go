package eval

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Dimension is the grouping key for all metrics.
type Dimension struct {
	SpawnerID string
	Model     string
	Stage     string
}

// MetricValue is one computed metric within a Dimension bucket.
type MetricValue struct {
	Key         string
	Value       float64
	SampleCount int
}

// Collector computes per-dimension eval metrics over a time window of stage runs.
type Collector struct {
	sr repo.StageRunRepo
	tr repo.TaskRepo
}

// NewCollector creates a Collector backed by the given repos.
func NewCollector(sr repo.StageRunRepo, tr repo.TaskRepo) *Collector {
	return &Collector{sr: sr, tr: tr}
}

// Collect queries all stage_runs in [from, to], buckets them by Dimension, and
// computes the metrics defined in metrics.go for each bucket.
func (c *Collector) Collect(ctx context.Context, from, to time.Time) (map[Dimension][]MetricValue, error) {
	runs, err := c.sr.ListInWindow(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return map[Dimension][]MetricValue{}, nil
	}

	taskMap, err := c.fetchTaskMap(ctx, runs)
	if err != nil {
		return nil, err
	}

	buckets := bucket(runs, taskMap)
	result := make(map[Dimension][]MetricValue, len(buckets))
	for dim, bucket := range buckets {
		result[dim] = computeMetrics(bucket)
	}
	return result, nil
}

// fetchTaskMap loads tasks for all distinct task IDs found in the runs slice.
func (c *Collector) fetchTaskMap(ctx context.Context, runs []*ent.StageRun) (map[string]*ent.Task, error) {
	idSet := make(map[string]struct{}, len(runs))
	for _, r := range runs {
		idSet[r.TaskID] = struct{}{}
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}

	tasks, err := c.tr.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	m := make(map[string]*ent.Task, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return m, nil
}

// bucket groups runs by Dimension, resolving spawner_id and model from the task.
func bucket(runs []*ent.StageRun, taskMap map[string]*ent.Task) map[Dimension][]*ent.StageRun {
	out := make(map[Dimension][]*ent.StageRun)
	for _, r := range runs {
		dim := dimensionFor(r, taskMap)
		out[dim] = append(out[dim], r)
	}
	return out
}

// dimensionFor resolves the Dimension for one stage run using its parent task.
func dimensionFor(r *ent.StageRun, taskMap map[string]*ent.Task) Dimension {
	dim := Dimension{Stage: r.Stage}
	t, ok := taskMap[r.TaskID]
	if !ok {
		return dim
	}
	if t.SpawnerID != nil {
		dim.SpawnerID = *t.SpawnerID
	}
	// model lives in task.Metadata["model"]; fallback to "default"
	dim.Model = "default"
	if t.Metadata != nil {
		if v, ok := t.Metadata["model"]; ok {
			if s, ok := v.(string); ok && s != "" {
				dim.Model = s
			}
		}
	}
	return dim
}

// computeMetrics derives all MetricValues for one bucket of stage runs.
func computeMetrics(runs []*ent.StageRun) []MetricValue {
	var (
		totalCount     = len(runs)
		doneCount      int
		failedCount    int
		awaitingCount  int
		escalatedCount int
		timeoutCount   int
		iterSum        int
		doneIter       int
		firstIterFail  int
		firstIterTotal int
		durationSum    float64
		durationCount  int
		costSum        int
		tokenSum       int
		terminalCount  int
	)

	for _, r := range runs {
		switch r.Status {
		case "done":
			doneCount++
			terminalCount++
			iterSum += r.Iteration
			doneIter++
		case "failed":
			failedCount++
			terminalCount++
		case "awaiting_user":
			awaitingCount++
		}

		// Escalation heuristic: output["escalated"]==true OR failed with requeue_reason present.
		if isEscalated(r) {
			escalatedCount++
		}

		// Timeout heuristic: output["error"] string containing "timeout".
		if isTimeout(r) {
			timeoutCount++
		}

		// First-iteration validation fail: iteration<=1 AND output["passed"]==false.
		if r.Iteration <= 1 {
			firstIterTotal++
			if isValidationFail(r) {
				firstIterFail++
			}
		}

		// Duration: only when both timestamps are set.
		if r.StartedAt != nil && r.EndedAt != nil {
			durationSum += r.EndedAt.Sub(*r.StartedAt).Seconds()
			durationCount++
		}

		if r.Status == "done" || r.Status == "failed" {
			costSum += r.CostCents
			tokenSum += r.TokensUsed
		}
	}

	var out []MetricValue

	if denom := doneCount + failedCount; denom > 0 {
		out = append(out, MetricValue{
			Key:         MetricSuccessRate,
			Value:       float64(doneCount) / float64(denom),
			SampleCount: denom,
		})
	}

	if doneIter > 0 {
		out = append(out, MetricValue{
			Key:         MetricMeanIterations,
			Value:       float64(iterSum) / float64(doneIter),
			SampleCount: doneIter,
		})
	}

	if firstIterTotal > 0 {
		out = append(out, MetricValue{
			Key:         MetricFirstIterValidationFail,
			Value:       float64(firstIterFail) / float64(firstIterTotal),
			SampleCount: firstIterTotal,
		})
	}

	if totalCount > 0 {
		out = append(out, MetricValue{
			Key:         MetricAwaitingUserRate,
			Value:       float64(awaitingCount) / float64(totalCount),
			SampleCount: totalCount,
		})
		out = append(out, MetricValue{
			Key:         MetricEscalationRate,
			Value:       float64(escalatedCount) / float64(totalCount),
			SampleCount: totalCount,
		})
		out = append(out, MetricValue{
			Key:         MetricTimeoutRate,
			Value:       float64(timeoutCount) / float64(totalCount),
			SampleCount: totalCount,
		})
	}

	if durationCount > 0 {
		out = append(out, MetricValue{
			Key:         MetricMeanDurationSeconds,
			Value:       durationSum / float64(durationCount),
			SampleCount: durationCount,
		})
	}

	if terminalCount > 0 {
		out = append(out, MetricValue{
			Key:         MetricMeanCostCents,
			Value:       math.Round(float64(costSum)/float64(terminalCount)*100) / 100,
			SampleCount: terminalCount,
		})
		out = append(out, MetricValue{
			Key:         MetricMeanTokens,
			Value:       float64(tokenSum) / float64(terminalCount),
			SampleCount: terminalCount,
		})
	}

	return out
}

// isEscalated returns true when a run shows an escalation signal.
// Heuristic: output["escalated"]==true OR failed status with requeue_reason in output.
func isEscalated(r *ent.StageRun) bool {
	if r.Output == nil {
		return false
	}
	if v, ok := r.Output["escalated"]; ok {
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	// Failed runs that carry a requeue_reason are treated as escalated.
	if r.Status == "failed" {
		if _, ok := r.Output["requeue_reason"]; ok {
			return true
		}
	}
	return false
}

// isTimeout returns true when the run's error output indicates a timeout.
// Heuristic: output["error"] string contains "timeout" (case-insensitive).
func isTimeout(r *ent.StageRun) bool {
	if r.Output == nil {
		return false
	}
	if v, ok := r.Output["error"]; ok {
		if s, ok := v.(string); ok {
			return strings.Contains(strings.ToLower(s), "timeout")
		}
	}
	return false
}

// isValidationFail returns true when output["passed"]==false (bool).
func isValidationFail(r *ent.StageRun) bool {
	if r.Output == nil {
		return false
	}
	v, ok := r.Output["passed"]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && !b
}
