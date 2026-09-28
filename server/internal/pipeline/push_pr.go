package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/worktree"
)

// decideFinalizationTransition decides what happens once the finalization
// stage completes. Tasks with no worktree, or whose push is disabled, reach
// done unchanged with the worktree kept. Otherwise it pushes the branch,
// refuses to finish with unpushed work, and opens (or reuses) a draft PR. A
// failed PR creation does not block done: the error lands in pr_error.
func (o *PipelineOrchestrator) decideFinalizationTransition(ctx context.Context, task *ent.Task, run *ent.StageRun, output map[string]any) StageTransition {
	if !o.finalizationPushes(task) {
		return DoneTransition{Output: output}
	}
	// The agent has exited; its spawn artefacts (.claude/settings.json) would otherwise read as uncommitted work below.
	o.spawnCleanups.release(run.ID)

	if o.opts.PushFn != nil {
		if err := o.opts.PushFn(ctx, task); err != nil {
			slog.Warn("finalization: git push failed", "taskID", task.ID, "err", err)
			return FailTransition{Reason: fmt.Sprintf("finalization: git push failed: %s", err), Output: output}
		}
	}

	if o.opts.HasUnpushedWorkFn != nil && o.opts.HasUnpushedWorkFn(ctx, task) {
		slog.Warn("finalization: worktree has unpushed work after push", "taskID", task.ID)
		return FailTransition{Reason: "finalization: worktree has unpushed work (push ran but work remains unpushed)", Output: output}
	}

	if o.taskTerminatedDuringPush(ctx, task.ID) {
		return FailTransition{Reason: "finalization: task cancelled during push", Output: output}
	}

	if o.opts.CreateDraftPRFn == nil {
		return DoneTransition{Output: output}
	}

	selfRun, err := o.stageRuns.GetLatestByTaskAndStage(ctx, task.ID, "self_review")
	if err != nil && !ent.IsNotFound(err) {
		slog.Warn("finalization: self_review lookup failed; PR omits its findings", "taskID", task.ID, "err", err)
	}
	title, prBody := buildFinalizationPRArgs(task, output, selfRun)
	base := resolveBase(ctx, *task.WorktreePath, task)
	branch := worktree.CreateBranch(task.SourceBranch, task.Slug)

	prNumber, prURL, err := o.opts.CreateDraftPRFn(ctx, *task.WorktreePath, branch, base, title, prBody)
	if err != nil {
		slog.Warn("finalization: create draft PR failed; task still reaches done", "taskID", task.ID, "err", err)
		return DoneTransition{Output: output, MetadataPatch: map[string]any{"pr_error": err.Error()}}
	}

	return DoneTransition{
		Output: output,
		MetadataPatch: map[string]any{
			"pr_number": prNumber,
			"pr_url":    prURL,
		},
	}
}

func (o *PipelineOrchestrator) finalizationPushes(task *ent.Task) bool {
	return task.WorktreePath != nil && *task.WorktreePath != "" && IsGitPushAllowed(task, o.opts.AllowGitPush)
}

func (o *PipelineOrchestrator) taskTerminatedDuringPush(ctx context.Context, taskID string) bool {
	if ctx.Err() != nil {
		return true
	}
	current, err := o.opts.TaskRepo.GetByID(ctx, taskID)
	return err == nil && IsTerminalStage(current.CurrentStage)
}

// startFinalizationPush runs the push and PR creation off the tick loop.
// finalizationInFlight holds the push's cancel func per task, so a later tick
// skips the task and NotifyTaskTerminated can abort the push. A restart
// mid-push drops the marker; recovery re-runs finalization, and both the push
// and ProductionCreateDraftPRFn are idempotent.
func (o *PipelineOrchestrator) startFinalizationPush(ctx context.Context, task *ent.Task, run *ent.StageRun, output map[string]any) {
	pushCtx, cancel := context.WithCancel(ctx)
	if _, loaded := o.finalizationInFlight.LoadOrStore(task.ID, cancel); loaded {
		cancel()
		return
	}
	go func() {
		defer o.finalizationInFlight.Delete(task.ID)
		defer cancel()
		transition := o.decideFinalizationTransition(pushCtx, task, run, output)
		fresh, err := o.stageRuns.GetByID(ctx, run.ID)
		if err != nil || fresh.Status != "running" {
			return
		}
		if _, err := o.applyTransition(ctx, task, fresh, transition); err != nil {
			slog.Error("finalization: applying push result failed", "taskID", task.ID, "err", err)
		}
	}()
}

func (o *PipelineOrchestrator) cancelFinalizationPush(taskID string) {
	if cancel, ok := o.finalizationInFlight.Load(taskID); ok {
		cancel.(context.CancelFunc)()
	}
}

// ghTimeout bounds every individual `gh` invocation.
const ghTimeout = 30 * time.Second

// ProductionPushFn is the production wiring for OrchestratorOptions.PushFn.
// It pushes the task's worktree branch to origin, setting the upstream if
// none is configured yet.
func ProductionPushFn(ctx context.Context, task *ent.Task) error {
	if task.WorktreePath == nil || *task.WorktreePath == "" {
		return fmt.Errorf("push: task has no worktree")
	}
	out, err := gitRunner.Combined(ctx, *task.WorktreePath, "push", "-u", "origin", "HEAD")
	if err != nil {
		return fmt.Errorf("git push: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

// ghPRListEntry decodes one row of `gh pr list --json number,url`.
type ghPRListEntry struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

// ProductionCreateDraftPRFn is the production wiring for
// OrchestratorOptions.CreateDraftPRFn. It is idempotent: an already-open PR
// for branch is returned as-is instead of creating a duplicate.
func ProductionCreateDraftPRFn(ctx context.Context, worktreePath, branch, base, title, prBody string) (int, string, error) {
	if number, url, ok := findExistingPR(ctx, worktreePath, branch); ok {
		return number, url, nil
	}

	args := []string{"pr", "create", "--draft", "--title", title, "--body", prBody}
	if base != "" {
		args = append(args, "--base", base)
	}
	out, err := runGH(ctx, worktreePath, args...)
	if err != nil {
		return 0, "", fmt.Errorf("gh pr create: %s: %w", strings.TrimSpace(out), err)
	}
	prURL := lastLine(out)
	if prURL == "" {
		return 0, "", fmt.Errorf("gh pr create: no URL in output: %s", strings.TrimSpace(out))
	}

	number, err := strconv.Atoi(path.Base(prURL))
	if err != nil {
		return 0, "", fmt.Errorf("gh pr create: no PR number in URL %q", prURL)
	}
	return number, prURL, nil
}

// findExistingPR looks up an already-open PR for branch. A lookup failure or
// empty result is not fatal — the caller falls back to creating a new one.
func findExistingPR(ctx context.Context, worktreePath, branch string) (number int, url string, ok bool) {
	out, err := runGH(ctx, worktreePath, "pr", "list", "--head", branch, "--state", "open", "--json", "number,url", "--limit", "1")
	if err != nil {
		slog.Warn("finalization: gh pr list failed; creating a new PR", "branch", branch, "err", err, "out", strings.TrimSpace(out))
		return 0, "", false
	}
	var entries []ghPRListEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		slog.Warn("finalization: gh pr list output unparseable; creating a new PR", "branch", branch, "err", err)
		return 0, "", false
	}
	if len(entries) == 0 {
		return 0, "", false
	}
	return entries[0].Number, entries[0].URL, true
}

func runGH(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()
	// #nosec G204 -- args are fixed gh subcommands plus caller-controlled
	// branch/title/body values that reach argv only; exec.CommandContext passes
	// them without a shell.
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out) + string(exitErr.Stderr), err
	}
	return string(out), err
}

func lastLine(s string) string {
	trimmed := strings.TrimSpace(s)
	lines := strings.Split(trimmed, "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// conventionalCommitRE matches a leading Conventional Commits type, optional
// scope, optional "!", and the mandatory ": " separator.
var conventionalCommitRE = regexp.MustCompile(`(?i)^(feat|fix|refactor|chore|docs|test|perf|build|ci|style|revert)(\([^)]*\))?!?:\s`)

// deriveConventionalTitle returns task.Title unchanged when it already carries
// a Conventional Commits prefix, else prefixes it with "feat: ".
func deriveConventionalTitle(task *ent.Task) string {
	if conventionalCommitRE.MatchString(task.Title) {
		return task.Title
	}
	return "feat: " + task.Title
}

// resolveBase returns the PR base branch: the repo's default branch as origin
// reports it, else the task's source branch. Empty lets gh pick the default.
func resolveBase(ctx context.Context, worktreePath string, task *ent.Task) string {
	if ref, err := gitRunner.Output(ctx, worktreePath, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimPrefix(strings.TrimSpace(ref), "origin/"); b != "" {
			return b
		}
	}
	if task.SourceBranch != nil {
		return *task.SourceBranch
	}
	return ""
}

// buildFinalizationPRArgs derives the title and body for the draft PR opened
// when a worktree task's finalization stage completes.
func buildFinalizationPRArgs(task *ent.Task, finOutput map[string]any, selfRun *ent.StageRun) (title, prBody string) {
	title = deriveConventionalTitle(task)
	var selfRunOutput map[string]any
	if selfRun != nil {
		selfRunOutput = selfRun.Output
	}
	prBody = buildPRBody(task, finOutput, selfRunOutput)
	return title, prBody
}

// buildPRBody renders the draft PR description from the finalization stage's
// output (summary, testPlan, openTodos) and the self_review stage's findings.
func buildPRBody(task *ent.Task, finOutput map[string]any, selfRunOutput map[string]any) string {
	var b strings.Builder

	b.WriteString("## Summary\n")
	switch {
	case notEmptyString(finOutput["summary"]):
		b.WriteString(finOutput["summary"].(string))
	case task.Description != nil && *task.Description != "":
		b.WriteString(*task.Description)
	default:
		b.WriteString(task.Title)
	}
	b.WriteString("\n")

	b.WriteString("\n## Test Plan\n")
	testPlan := stringSlice(finOutput["testPlan"])
	if len(testPlan) == 0 {
		b.WriteString("- [ ] Manual verification\n")
	} else {
		for _, item := range testPlan {
			fmt.Fprintf(&b, "- [ ] %s\n", item)
		}
	}

	if known := buildKnownIssues(finOutput, selfRunOutput); known != "" {
		b.WriteString("\n## Known Issues\n")
		b.WriteString(known)
	}

	fmt.Fprintf(&b, "\nKontor task: `%s` (%s)\n", task.Slug, task.ID)
	b.WriteString("\n🤖 Generated with [Kontor](https://github.com/lx-wnk/kontor)\n")

	return b.String()
}

// buildKnownIssues lists finalization's openTodos followed by every
// high/medium self_review finding — low-severity findings are noise the PR
// description does not need to carry.
func buildKnownIssues(finOutput, selfRunOutput map[string]any) string {
	var b strings.Builder
	for _, todo := range stringSlice(finOutput["openTodos"]) {
		fmt.Fprintf(&b, "- %s\n", todo)
	}
	for _, f := range findingsSlice(selfRunOutput["findings"]) {
		severity, _ := f["severity"].(string)
		if severity != "high" && severity != "medium" {
			continue
		}
		description, _ := f["description"].(string)
		file, _ := f["file"].(string)
		line := fmt.Sprintf("- [%s] %s", severity, description)
		if file != "" {
			line += fmt.Sprintf(" (%s)", file)
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

func notEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func findingsSlice(v any) []map[string]any {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(arr))
	for _, item := range arr {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}
