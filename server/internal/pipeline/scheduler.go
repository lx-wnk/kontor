package pipeline

import (
	"context"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/ranking"
)

const (
	maxParallelKey     = "maxParallelOrchestrators"
	defaultMaxParallel = 3
)

// scheduler decides runner-slot capacity and selects the next tasks to spawn.
// It does not dispatch — callers apply the returned tasks via ProgressTask.
type scheduler struct {
	cache        *configCache
	taskRepo     repo.TaskRepo
	stageRunRepo repo.StageRunRepo
	depRepo      repo.DependencyRepo
}

// newScheduler constructs a scheduler backed by the orchestrator's shared
// configCache and the given repos.
func newScheduler(cache *configCache, taskRepo repo.TaskRepo, stageRunRepo repo.StageRunRepo, depRepo repo.DependencyRepo) *scheduler {
	return &scheduler{cache: cache, taskRepo: taskRepo, stageRunRepo: stageRunRepo, depRepo: depRepo}
}

// HasFreeSlot checks whether a runner slot is available for the given task.
// Called from the route/MCP-triggered ProgressTask path where no prefetched
// running set is available — issues one ListByStatus("running") query.
// F-PERF-007: the tick()-driven path uses Pick which derives busyTaskIDs from
// the already-loaded allRunning slice, bypassing this function entirely for
// the normal picker flow.
func (s *scheduler) HasFreeSlot(ctx context.Context, exceptTaskID string) bool {
	max := s.cache.Number(ctx, maxParallelKey, defaultMaxParallel)
	running, _ := s.stageRunRepo.ListByStatus(ctx, "running")
	busyTaskIDs := make(map[string]bool)
	for _, r := range running {
		if r.TaskID != exceptTaskID {
			busyTaskIDs[r.TaskID] = true
		}
	}
	return len(busyTaskIDs) < max
}

// Pick selects tasks for available runner slots. allRunning is the prefetched
// set of non-terminal stage_runs (running + awaiting_user + on_hold) already
// loaded by tick(). F-PERF-007: busyTaskIDs is derived from the already-loaded
// allRunning slice — no second ListByStatus("running") call is issued here.
func (s *scheduler) Pick(ctx context.Context, allRunning []*ent.StageRun) []*ent.Task {
	max := s.cache.Number(ctx, maxParallelKey, defaultMaxParallel)
	busyTaskIDs := make(map[string]bool)
	for _, r := range allRunning {
		if r.Status == "running" {
			busyTaskIDs[r.TaskID] = true
		}
	}
	freeSlots := max - len(busyTaskIDs)
	if freeSlots <= 0 {
		return nil
	}
	candidates, _ := s.taskRepo.ListPickable(ctx)

	// Batch-fetch the latest run per candidate to avoid N+1 per task.
	candidateIDs := make([]string, len(candidates))
	for i, t := range candidates {
		candidateIDs[i] = t.ID
	}
	var latestByTask map[string]*ent.StageRun
	if len(candidateIDs) > 0 {
		latestByTask, _ = s.stageRunRepo.GetLatestForTasks(ctx, candidateIDs)
	}

	var ready []*ent.Task
	for _, t := range candidates {
		if busyTaskIDs[t.ID] {
			continue
		}
		// Only skip if the latest run is specifically on the current stage and blocking.
		// If the latest run is for a different stage, no run exists for currentStage yet.
		if latest := latestByTask[t.ID]; latest != nil && latest.Stage == t.CurrentStage &&
			(latest.Status == "awaiting_user" || latest.Status == "failed" || latest.Status == "requeued") {
			continue
		}
		// Dependency gate: skip unless all upstream deps are satisfied.
		// N+1 per candidate is intentional here (upstreams are rare and small).
		if s.depRepo != nil {
			resolveStage := func(ctx context.Context, id string) (string, error) {
				upstream, err := s.taskRepo.GetByID(ctx, id)
				if err != nil {
					return "", err
				}
				return upstream.CurrentStage, nil
			}
			allSat, _, _, err := EvaluateTaskDeps(ctx, t.ID, s.depRepo, resolveStage)
			if err != nil {
				slog.Warn("scheduler: dep eval failed", "taskID", t.ID, "err", err)
				continue
			}
			if !allSat {
				continue
			}
		}
		ready = append(ready, t)
	}
	// SQL already sorts by: silver_bullet DESC, priority DESC, created_at ASC
	// (see ListPickable in task_repo.go).
	// Remaining Go-only sort: stage index DESC (custom enum order).
	sortByStageIndex(ready)
	picks := ready
	if len(picks) > freeSlots {
		picks = picks[:freeSlots]
	}
	return picks
}

// sortByStageIndex performs an insertion sort by stage index descending (further
// along the pipeline = higher priority). silver_bullet, priority, and created_at
// are already ordered by SQL in ListPickable — this pass handles only the
// stage-dimension that SQL cannot express with a simple ORDER BY.
//
// F-PERF-010: SQL covers silver_bullet DESC + priority DESC + created_at ASC;
// Go covers stage_index DESC only.
func sortByStageIndex(tasks []*ent.Task) {
	stageIdx := func(s string) int {
		for i, st := range StageOrder {
			if st == s {
				return i
			}
		}
		return -1
	}
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0; j-- {
			a, b := tasks[j-1], tasks[j]
			if stageIdx(a.CurrentStage) < stageIdx(b.CurrentStage) {
				tasks[j-1], tasks[j] = tasks[j], tasks[j-1]
			} else {
				break
			}
		}
	}
}

// sortPickCandidates is the full multi-key sort used by tests (via export_test.go).
// Production code uses sortByStageIndex (stage-only sort, after SQL handles the rest).
func sortPickCandidates(tasks []*ent.Task) {
	priorityRank := map[string]int{"high": 3, "medium": 2, "low": 1}
	stageIdx := func(s string) int {
		for i, st := range StageOrder {
			if st == s {
				return i
			}
		}
		return -1
	}
	for i := 1; i < len(tasks); i++ {
		for j := i; j > 0; j-- {
			a, b := tasks[j-1], tasks[j]
			if shouldSwap(a, b, priorityRank, stageIdx) {
				tasks[j-1], tasks[j] = tasks[j], tasks[j-1]
			} else {
				break
			}
		}
	}
}

func shouldSwap(a, b *ent.Task, priorityRank map[string]int, stageIdx func(string) int) bool {
	if a.SilverBullet != b.SilverBullet {
		return !a.SilverBullet // b has silver bullet — b should come first
	}
	si, sj := stageIdx(a.CurrentStage), stageIdx(b.CurrentStage)
	if si != sj {
		return si < sj // b is further along — b should come first
	}
	pi, pj := priorityRank[a.Priority], priorityRank[b.Priority]
	if pi != pj {
		return pi < pj // b has higher priority — b should come first
	}
	ri, rj := ranking.EffectiveRank(a), ranking.EffectiveRank(b)
	if ri != rj {
		return ri > rj // b has the lower rank (manual drag order) — b should come first
	}
	return a.CreatedAt.After(b.CreatedAt) // b is older — b should come first
}
