package rawrepo

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/proc"
)

// StageRunBulkRepo provides bulk raw-SQL helpers for stage_runs that cannot
// be expressed efficiently through ent (e.g. window-function queries).
type StageRunBulkRepo interface {
	// LatestPerTask returns the most-recent stage_run per task_id for the given
	// task IDs using a ROW_NUMBER() window function. Correctness is exact
	// regardless of iteration count, unlike the Go-side heuristic limit.
	LatestPerTask(ctx context.Context, taskIDs []string) (map[string]*ent.StageRun, error)

	// AllForTaskIDs returns all stage_runs for the given task IDs grouped by
	// task_id. Used for bulk eager-loading in export routes.
	AllForTaskIDs(ctx context.Context, taskIDs []string) (map[string][]*ent.StageRun, error)

	// ChildSummariesByParent returns a compact summary of child tasks for each
	// parent ID in a single query joining tasks to their latest stage_run.
	// Parents with no children are absent from the returned map.
	ChildSummariesByParent(ctx context.Context, parentIDs []string) (map[string]*ChildSummary, error)
}

// ChildSummary is the pre-aggregated child-task summary attached to a parent EnrichedTask.
type ChildSummary struct {
	ChildCount       int
	ActiveChildCount int
	HasActive        bool
	TokensUsed       int
	CostCents        int
	DurationSeconds  int
	CurrentStage     string
	LatestOutput     string
}

type sqlStageRunBulkRepo struct {
	db         *sql.DB
	isPidAlive func(int) bool
}

// NewStageRunBulkRepo returns a StageRunBulkRepo backed by db.
// proc.IsPidAlive is used as the default liveness probe; tests can
// substitute a fake by calling newStageRunBulkRepoWithProbe directly.
func NewStageRunBulkRepo(db *sql.DB) StageRunBulkRepo {
	return &sqlStageRunBulkRepo{db: db, isPidAlive: proc.IsPidAlive}
}

// NewStageRunBulkRepoWithProbe is the test-seam constructor that substitutes
// the default proc.IsPidAlive with a custom probe.
func NewStageRunBulkRepoWithProbe(db *sql.DB, probe func(int) bool) StageRunBulkRepo {
	return &sqlStageRunBulkRepo{db: db, isPidAlive: probe}
}

// LatestPerTask uses ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY created_at DESC)
// to select exactly one row per task_id — the most recently created stage_run.
// This replaces the Go-side heuristic `Limit(len(ids)*20+20)` which silently
// misses the latest run when a task has more than 20 iterations.
func (r *sqlStageRunBulkRepo) LatestPerTask(ctx context.Context, taskIDs []string) (map[string]*ent.StageRun, error) {
	if len(taskIDs) == 0 {
		return map[string]*ent.StageRun{}, nil
	}

	placeholders, args := buildInArgs(taskIDs)
	// #nosec G201 -- the query text is a compile-time constant; the only interpolation is buildInArgs' "?" placeholder list, and every id is bound as a query parameter.
	q := fmt.Sprintf(`
SELECT id, task_id, stage, session_id, session_name, pid, status, iteration,
       output, tokens_used, cost_cents, started_at, ended_at, last_grant_at, created_at,
       retry_count, next_retry_at
FROM (
    SELECT id, task_id, stage, session_id, session_name, pid, status, iteration,
           output, tokens_used, cost_cents, started_at, ended_at, last_grant_at, created_at,
           retry_count, next_retry_at,
           ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY created_at DESC) AS rn
    FROM stage_runs
    WHERE task_id IN (%s)
)
WHERE rn = 1`, placeholders)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("stagerun.LatestPerTask: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*ent.StageRun, len(taskIDs))
	for rows.Next() {
		sr, err := scanStageRun(rows)
		if err != nil {
			return nil, fmt.Errorf("stagerun.LatestPerTask scan: %w", err)
		}
		result[sr.TaskID] = sr
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stagerun.LatestPerTask rows: %w", err)
	}
	return result, nil
}

// AllForTaskIDs returns all stage_runs for the given task IDs in a single
// query, grouped into a map[taskID][]*ent.StageRun ordered by iteration ASC.
func (r *sqlStageRunBulkRepo) AllForTaskIDs(ctx context.Context, taskIDs []string) (map[string][]*ent.StageRun, error) {
	if len(taskIDs) == 0 {
		return map[string][]*ent.StageRun{}, nil
	}

	placeholders, args := buildInArgs(taskIDs)
	// #nosec G201 -- the query text is a compile-time constant; the only interpolation is buildInArgs' "?" placeholder list, and every id is bound as a query parameter.
	q := fmt.Sprintf(`
SELECT id, task_id, stage, session_id, session_name, pid, status, iteration,
       output, tokens_used, cost_cents, started_at, ended_at, last_grant_at, created_at,
       retry_count, next_retry_at
FROM stage_runs
WHERE task_id IN (%s)
ORDER BY task_id, iteration ASC`, placeholders)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("stagerun.AllForTaskIDs: %w", err)
	}
	defer rows.Close()

	result := make(map[string][]*ent.StageRun, len(taskIDs))
	for rows.Next() {
		sr, err := scanStageRun(rows)
		if err != nil {
			return nil, fmt.Errorf("stagerun.AllForTaskIDs scan: %w", err)
		}
		result[sr.TaskID] = append(result[sr.TaskID], sr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stagerun.AllForTaskIDs rows: %w", err)
	}
	return result, nil
}

// terminalStatuses is the set of stage_run statuses that mark a child as no longer active.
var terminalStatuses = map[string]bool{"done": true, "failed": true}

// ChildSummariesByParent fetches all children of the given parent task IDs with
// their latest stage_run in a single window-function query, then aggregates
// per parent in Go. Parents with no children are absent from the returned map.
func (r *sqlStageRunBulkRepo) ChildSummariesByParent(ctx context.Context, parentIDs []string) (map[string]*ChildSummary, error) {
	if len(parentIDs) == 0 {
		return map[string]*ChildSummary{}, nil
	}

	placeholders, args := buildInArgs(parentIDs)
	// #nosec G201 -- the query text is a compile-time constant; the only interpolation is buildInArgs' "?" placeholder list, and every id is bound as a query parameter.
	q := fmt.Sprintf(`
SELECT t.parent_task_id, t.id, t.current_stage,
       lr.status, lr.pid, lr.tokens_used, lr.cost_cents, lr.output, lr.started_at, lr.ended_at, lr.created_at
FROM tasks t
LEFT JOIN (
    SELECT task_id, status, pid, tokens_used, cost_cents, output, started_at, ended_at, created_at,
           ROW_NUMBER() OVER (PARTITION BY task_id ORDER BY created_at DESC) AS rn
    FROM stage_runs
) lr ON lr.task_id = t.id AND lr.rn = 1
WHERE t.parent_task_id IN (%s)`, placeholders)

	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("stagerun.ChildSummariesByParent: %w", err)
	}
	defer rows.Close()

	// representative tracks the best active candidate seen so far per parent.
	type candidate struct {
		tokensUsed      int
		costCents       int
		durationSeconds int
		currentStage    string
		latestOutput    string
		bestTime        time.Time
	}
	result := map[string]*ChildSummary{}
	best := map[string]*candidate{}

	for rows.Next() {
		var (
			parentID     string
			childID      string
			currentStage string
			status       sql.NullString
			pid          sql.NullInt64
			tokensUsed   sql.NullInt64
			costCents    sql.NullInt64
			outputRaw    sql.NullString
			startedAt    sql.NullTime
			endedAt      sql.NullTime
			createdAt    sql.NullTime
		)
		if err := rows.Scan(
			&parentID, &childID, &currentStage,
			&status, &pid, &tokensUsed, &costCents, &outputRaw,
			&startedAt, &endedAt, &createdAt,
		); err != nil {
			return nil, fmt.Errorf("stagerun.ChildSummariesByParent scan: %w", err)
		}

		s, ok := result[parentID]
		if !ok {
			s = &ChildSummary{}
			result[parentID] = s
		}
		s.ChildCount++

		hasRun := status.Valid
		// A child is active when DB status is non-terminal AND, if a PID is
		// recorded, that process is still alive. A NULL pid means the run has
		// not yet recorded its process; treat it as alive to match enrichOne.
		pidLive := !pid.Valid || r.isPidAlive(int(pid.Int64))
		isActive := hasRun && !terminalStatuses[status.String] && pidLive
		if isActive {
			s.ActiveChildCount++

			// Compute a representative time for picking the most-recent active child.
			repTime := time.Time{}
			if startedAt.Valid {
				repTime = startedAt.Time
			} else if createdAt.Valid {
				repTime = createdAt.Time
			}

			if prev, seen := best[parentID]; !seen || repTime.After(prev.bestTime) {
				dur := 0
				if startedAt.Valid {
					// Cap duration at ended_at when present; otherwise use wall time.
					if endedAt.Valid {
						dur = int(endedAt.Time.Sub(startedAt.Time).Seconds())
					} else {
						dur = int(time.Since(startedAt.Time).Seconds())
					}
				}
				summary := extractSummary(outputRaw)
				best[parentID] = &candidate{
					tokensUsed:      int(tokensUsed.Int64),
					costCents:       int(costCents.Int64),
					durationSeconds: dur,
					currentStage:    currentStage,
					latestOutput:    summary,
					bestTime:        repTime,
				}
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("stagerun.ChildSummariesByParent rows: %w", err)
	}

	for parentID, s := range result {
		if c, ok := best[parentID]; ok {
			s.HasActive = true
			s.TokensUsed = c.tokensUsed
			s.CostCents = c.costCents
			s.DurationSeconds = c.durationSeconds
			s.CurrentStage = c.currentStage
			s.LatestOutput = c.latestOutput
		}
	}
	return result, nil
}

// extractSummary parses a stage_run output JSON blob and returns the "summary" string field.
func extractSummary(outputRaw sql.NullString) string {
	if !outputRaw.Valid || outputRaw.String == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(outputRaw.String), &m); err != nil {
		return ""
	}
	s, _ := m["summary"].(string)
	return s
}

// buildInArgs builds a comma-separated placeholder string and a matching args
// slice for use in SQL IN (...) clauses. Each element in ids becomes a single
// "?" placeholder.
func buildInArgs(ids []string) (string, []any) {
	placeholders := ""
	args := make([]any, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = id
	}
	return placeholders, args
}

// scanStageRun scans a single stage_run row into an *ent.StageRun. The column
// order must match the SELECT list in LatestPerTask and AllForTaskIDs.
func scanStageRun(rows *sql.Rows) (*ent.StageRun, error) {
	var sr ent.StageRun
	var (
		sessionID   sql.NullString
		sessionName sql.NullString
		pid         sql.NullInt64
		outputRaw   sql.NullString
		startedAt   sql.NullTime
		endedAt     sql.NullTime
		lastGrantAt sql.NullTime
		nextRetryAt sql.NullTime
	)
	err := rows.Scan(
		&sr.ID, &sr.TaskID, &sr.Stage,
		&sessionID, &sessionName, &pid,
		&sr.Status, &sr.Iteration,
		&outputRaw, &sr.TokensUsed, &sr.CostCents,
		&startedAt, &endedAt, &lastGrantAt,
		&sr.CreatedAt,
		&sr.RetryCount, &nextRetryAt,
	)
	if err != nil {
		return nil, err
	}
	if sessionID.Valid {
		sr.SessionID = &sessionID.String
	}
	if sessionName.Valid {
		sr.SessionName = &sessionName.String
	}
	if pid.Valid {
		p := int(pid.Int64)
		sr.Pid = &p
	}
	if outputRaw.Valid && outputRaw.String != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(outputRaw.String), &m); err == nil {
			sr.Output = m
		}
	}
	if startedAt.Valid {
		t := startedAt.Time
		sr.StartedAt = &t
	}
	if endedAt.Valid {
		t := endedAt.Time
		sr.EndedAt = &t
	}
	if lastGrantAt.Valid {
		t := lastGrantAt.Time
		sr.LastGrantAt = &t
	}
	if nextRetryAt.Valid {
		t := nextRetryAt.Time
		sr.NextRetryAt = &t
	}
	return &sr, nil
}
