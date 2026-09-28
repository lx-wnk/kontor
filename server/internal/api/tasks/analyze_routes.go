package tasks

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/proc"
)

var claudeBin = resolvebin("claude")

// activeAnalysisTasks tracks in-flight analysis PIDs to prevent duplicate spawns.
var (
	analysisMu         sync.Mutex
	activeAnalysisPIDs = map[string]int{} // taskID → PID
)

func buildAnalysisPrompt(task *ent.Task, errorSummary string, sessionLogPaths []string) string {
	logsBlock := "(none found)"
	if len(sessionLogPaths) > 0 {
		lines := make([]string, len(sessionLogPaths))
		for i, p := range sessionLogPaths {
			lines[i] = "- " + p
		}
		logsBlock = strings.Join(lines, "\n")
	}
	worktree := "(none)"
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		worktree = *task.WorktreePath
	}
	desc := ""
	if task.Description != nil && *task.Description != "" {
		desc = "\n## Description\n" + *task.Description
	}
	host := "127.0.0.1"
	if v := os.Getenv("DASHBOARD_HOST"); v != "" {
		host = v
	}
	port := "13120"
	if v := os.Getenv("DASHBOARD_PORT"); v != "" {
		port = v
	}
	return strings.Join([]string{
		"# Failure Analysis Session",
		"",
		"You are attached to a failed pipeline task as an independent analysis",
		"session. You are NOT part of the pipeline state machine — your job is",
		"to help the human understand what went wrong and decide what to do next.",
		"",
		"## Task",
		"- id: " + task.ID,
		"- slug: " + task.Slug,
		"- title: " + task.Title,
		"- current stage: " + task.CurrentStage,
		"- worktree: " + worktree,
		"- cwd: " + task.Cwd,
		desc,
		"",
		"## Error Summary",
		errorSummary,
		"",
		"## Relevant Session Logs (JSONL files on disk)",
		logsBlock,
		"",
		"## What to do",
		"1. Read the session JSONL(s) above and any task-relevant files to",
		"   identify what actually went wrong.",
		"2. Report to the human in plain language: root cause, what is still",
		"   salvageable, and a recommendation (retry as-is, edit something",
		"   first, split the task, or abandon it).",
		"3. If the human asks you to adjust the task itself, you may:",
		fmt.Sprintf("   - curl the dashboard: POST http://%s:%s/api/tasks/%s", host, port, task.ID),
		"     with Content-Type: application/json to patch editable fields.",
		"   - Edit files under the worktree directly.",
		"",
		"Start by reading the newest session JSONL from the list above.",
	}, "\n")
}

func (h *Handler) analyzeTask(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	task, err := h.taskRepo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("analyze: get task: %w", err)
	}

	// Get latest stage run.
	runs, err := h.srRepo.ListForTask(r.Context(), id)
	if err != nil {
		return fmt.Errorf("analyze: list runs: %w", err)
	}
	if len(runs) == 0 {
		return apierr.NewAppError(http.StatusConflict, "task has no stage runs to analyze")
	}
	latest := runs[len(runs)-1]

	// Dedup: reject if a live analysis agent is already running for this task.
	analysisMu.Lock()
	if existingPID, ok := activeAnalysisPIDs[id]; ok {
		if proc.IsPidAlive(existingPID) {
			analysisMu.Unlock()
			return apierr.NewAppError(http.StatusConflict, fmt.Sprintf(
				"analysis session already running for this task (pid %d)", existingPID))
		}
		delete(activeAnalysisPIDs, id)
	}
	analysisMu.Unlock()

	// Build error summary.
	errorSummary := fmt.Sprintf("latest stage_run (%s iter %d) status: %s",
		latest.Stage, latest.Iteration, latest.Status)
	if latest.Output != nil {
		if errMsg, ok := latest.Output["error"].(string); ok && errMsg != "" {
			errorSummary = fmt.Sprintf("latest stage_run (%s iter %d) failed with: %s",
				latest.Stage, latest.Iteration, errMsg)
		}
	}

	// Collect session JSONL paths.
	cwd := task.Cwd
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		cwd = *task.WorktreePath
	}
	projectDir, _ := pipeline.ResolvedProjectDir(cwd)
	var sessionLogPaths []string
	for _, run := range runs {
		if run.SessionID != nil && *run.SessionID != "" {
			sessionLogPaths = append(sessionLogPaths, filepath.Join(projectDir, *run.SessionID+".jsonl"))
		}
	}

	prompt := buildAnalysisPrompt(task, errorSummary, sessionLogPaths)

	cmd := exec.Command(claudeBin, "-p", prompt, "--permission-mode", "default")
	cmd.Dir = cwd
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("analyze: spawn: %w", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Release() //nolint:errcheck // detach

	analysisMu.Lock()
	activeAnalysisPIDs[id] = pid
	analysisMu.Unlock()

	return jsonReply(w, http.StatusAccepted, map[string]any{"pid": pid, "cwd": cwd})
}
