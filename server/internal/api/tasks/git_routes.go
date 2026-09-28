package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

var (
	aheadRE    = regexp.MustCompile(`\[ahead (\d+)`)
	behindRE   = regexp.MustCompile(`behind (\d+)`)
	branchRE   = regexp.MustCompile(`^## ([^.]+)`)
	noBranchRE = regexp.MustCompile(`^## HEAD \(no branch\)`)

	gitBin  = resolvebin("git")
	pnpmBin = resolvebin("pnpm")

	allowedCommands map[string][]string
)

func resolvebin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return name
}

func init() {
	allowedCommands = map[string][]string{
		"pnpm test":      {pnpmBin, "test", "--run"},
		"pnpm lint":      {pnpmBin, "lint"},
		"pnpm typecheck": {pnpmBin, "typecheck"},
		"pnpm build":     {pnpmBin, "build"},
		"git log":        {gitBin, "log", "--oneline", "-20"},
		"git diff":       {gitBin, "diff", "--stat"},
		"git status":     {gitBin, "status", "--short"},
	}
}

type gitLastCommit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
	Message   string `json:"message"`
	Author    string `json:"author"`
	Date      string `json:"date"`
}

type gitStatus struct {
	Branch      string         `json:"branch"`
	AheadCount  int            `json:"aheadCount"`
	BehindCount int            `json:"behindCount"`
	Staged      []string       `json:"staged"`
	Unstaged    []string       `json:"unstaged"`
	Untracked   []string       `json:"untracked"`
	LastCommit  *gitLastCommit `json:"lastCommit"`
	RemoteURL   *string        `json:"remoteUrl"`
}

// confineToHome rejects a working directory that resolves outside the user's
// home directory. Best-effort: when the home directory cannot be determined it
// does not block. Applied to every handler that runs git in a task-supplied cwd.
func confineToHome(cwd string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	cwdAbs, _ := filepath.Abs(cwd)
	homeAbs, _ := filepath.Abs(home)
	if !strings.HasPrefix(cwdAbs+string(filepath.Separator), homeAbs+string(filepath.Separator)) {
		return apierr.NewAppError(http.StatusForbidden, "task directory is outside the user home directory")
	}
	return nil
}

func runGit(ctx context.Context, cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, gitBin, args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	return string(out), err
}

func getGitStatus(ctx context.Context, cwd string) gitStatus {
	def := gitStatus{Branch: "unknown", Staged: []string{}, Unstaged: []string{}, Untracked: []string{}}

	out, err := runGit(ctx, cwd, "status", "--porcelain=v1", "-b")
	if err != nil {
		return def
	}

	lines := strings.Split(out, "\n")
	header := ""
	if len(lines) > 0 {
		header = lines[0]
	}

	branch := "unknown"
	if noBranchRE.MatchString(header) {
		branch = "HEAD"
	} else if m := branchRE.FindStringSubmatch(header); m != nil {
		branch = m[1]
	}

	ahead, behind := 0, 0
	if m := aheadRE.FindStringSubmatch(header); m != nil {
		fmt.Sscanf(m[1], "%d", &ahead) //nolint:errcheck
	}
	if m := behindRE.FindStringSubmatch(header); m != nil {
		fmt.Sscanf(m[1], "%d", &behind) //nolint:errcheck
	}

	staged := []string{}
	unstaged := []string{}
	untracked := []string{}
	for _, line := range lines[1:] {
		if len(line) < 2 {
			continue
		}
		xy := line[:2]
		file := line[3:]
		if xy == "??" {
			untracked = append(untracked, file)
			continue
		}
		x, y := rune(xy[0]), rune(xy[1])
		if x != ' ' && x != '?' {
			staged = append(staged, file)
		}
		if y != ' ' && y != '?' {
			unstaged = append(unstaged, file)
		}
	}

	var lastCommit *gitLastCommit
	if logOut, err := runGit(ctx, cwd, "log", "-1", "--format=%H%n%h%n%s%n%an%n%ai"); err == nil {
		parts := strings.Split(strings.TrimSpace(logOut), "\n")
		if len(parts) >= 5 {
			lastCommit = &gitLastCommit{
				Hash: parts[0], ShortHash: parts[1], Message: parts[2],
				Author: parts[3], Date: parts[4],
			}
		}
	}

	var remoteURL *string
	if remOut, err := runGit(ctx, cwd, "remote", "get-url", "origin"); err == nil {
		s := strings.TrimSpace(remOut)
		if s != "" {
			remoteURL = &s
		}
	}

	return gitStatus{
		Branch: branch, AheadCount: ahead, BehindCount: behind,
		Staged: staged, Unstaged: unstaged, Untracked: untracked,
		LastCommit: lastCommit, RemoteURL: remoteURL,
	}
}

func (h *Handler) getGitStatusHandler(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	task, err := h.taskRepo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("git_status: get task: %w", err)
	}
	cwd := task.Cwd
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		cwd = *task.WorktreePath
	}
	if err := confineToHome(cwd); err != nil {
		return err
	}
	status := getGitStatus(r.Context(), cwd)
	return jsonReply(w, http.StatusOK, status)
}

func (h *Handler) gitActionHandler(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	task, err := h.taskRepo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("git_action: get task: %w", err)
	}
	var body struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	if body.Action != "fetch" && body.Action != "pull" {
		return apierr.NewAppError(http.StatusBadRequest, "invalid action")
	}
	if body.Action == "pull" && !h.allowGitPull {
		return apierr.NewAppError(http.StatusForbidden, "pull is disabled. Enable the git.allowPull setting to allow it.")
	}
	cwd := task.Cwd
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		cwd = *task.WorktreePath
	}
	if err := confineToHome(cwd); err != nil {
		return err
	}
	var output string
	if body.Action == "fetch" {
		output, err = runGit(r.Context(), cwd, "fetch", "--prune")
	} else {
		output, err = runGit(r.Context(), cwd, "pull", "--ff-only")
	}
	if err != nil {
		return fmt.Errorf("git_action: %s: %w", body.Action, err)
	}
	return jsonReply(w, http.StatusOK, map[string]string{"output": output})
}

func (h *Handler) taskRunHandler(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	task, err := h.taskRepo.GetByID(r.Context(), id)
	if err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return fmt.Errorf("task_run: get task: %w", err)
	}
	var body struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return apierr.NewAppError(http.StatusBadRequest, "invalid JSON body")
	}
	args, ok := allowedCommands[body.Command]
	if !ok {
		allowed := make([]string, 0, len(allowedCommands))
		for k := range allowedCommands {
			allowed = append(allowed, k)
		}
		return apierr.NewAppError(http.StatusBadRequest,
			fmt.Sprintf("command not allowed. Allowed: %s", strings.Join(allowed, ", ")))
	}
	cwd := task.Cwd
	if task.WorktreePath != nil && *task.WorktreePath != "" {
		cwd = *task.WorktreePath
	}
	if err := confineToHome(cwd); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	// #nosec G204 -- args is a value of the compile-time allowedCommands table (LookPath-resolved git/pnpm plus fixed flags); the request body only selects a key and an unknown key is rejected with 400.
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	exitCode := 0
	if err := cmd.Run(); err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			exitCode = exit.ExitCode()
		} else {
			exitCode = 1
		}
	}
	combined := stdout.String() + stderr.String()
	return jsonReply(w, http.StatusOK, map[string]any{"output": combined, "exitCode": exitCode})
}
