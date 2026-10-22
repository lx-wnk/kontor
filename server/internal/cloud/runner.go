package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/services"
)

const (
	claudeCommand  = "claude"
	defaultTimeout = 60 * time.Second
	authMethodWeb  = "claude.ai"
	// killGrace bounds how long Wait lingers on a killed child's pipes.
	killGrace = time.Second
)

var (
	// ErrClaudeNotFound reports that the claude CLI is not on PATH.
	ErrClaudeNotFound = errors.New("the claude CLI is not on PATH")
	// ErrTimeout reports that the claude CLI outlived the runner's timeout.
	ErrTimeout = errors.New("the claude CLI timed out")
)

// scrubbedEnvKeys would switch the CLI to API-key or third-party auth, or
// (CLAUDE_CODE_OAUTH_TOKEN, which passes BuildSpawnEnv's CLAUDE_ prefix rule)
// override the account CLAUDE_CONFIG_DIR selects. Cloud sessions need the
// claude.ai login of that config dir.
var scrubbedEnvKeys = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
}

// Runner runs the claude CLI under a spawner's Claude account.
type Runner struct {
	// Timeout bounds one CLI call; zero means 60 seconds.
	Timeout time.Duration
}

// SendResult is the CLI's answer to a message sent to a cloud session.
type SendResult struct {
	OK        bool   `json:"ok"`
	SessionID string `json:"session_id"`
	URL       string `json:"url"`
	Error     string `json:"error"`
}

// AuthStatus is the part of `claude auth status --json` Kontor keeps. Email
// and organisation data are deliberately not decoded.
type AuthStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	APIProvider      string `json:"apiProvider"`
	SubscriptionType string `json:"subscriptionType"`
}

// CloudReady reports whether the account is signed in through claude.ai, the
// only login that can reach cloud sessions.
func (a AuthStatus) CloudReady() bool {
	return a.LoggedIn && a.AuthMethod == authMethodWeb
}

// Send delivers text to the cloud session. The text goes to claude on stdin,
// never into argv.
func (r Runner) Send(ctx context.Context, sp *ent.Spawner, sessionID, text string) (SendResult, error) {
	if !sessionIDPattern.MatchString(sessionID) {
		return SendResult{}, ErrInvalidSessionRef
	}
	var res SendResult
	err := r.runJSON(ctx, sp, text, &res, "-p", "--cloud", sessionID, "--output-format", "json")
	return res, err
}

// AuthStatus reads the sign-in status of the spawner's Claude account.
func (r Runner) AuthStatus(ctx context.Context, sp *ent.Spawner) (AuthStatus, error) {
	var st AuthStatus
	err := r.runJSON(ctx, sp, "", &st, "auth", "status", "--json")
	return st, err
}

// runJSON decodes the CLI's stdout into v. A signed-out `auth status` prints
// its JSON and exits 1, so JSON on stdout wins over a non-zero exit.
func (r Runner) runJSON(ctx context.Context, sp *ent.Spawner, stdin string, v any, args ...string) error {
	if ok, reason := services.ValidateSpawnerCommand(claudeCommand); !ok {
		return fmt.Errorf("claude command not permitted: %s", reason)
	}
	timeout := r.Timeout
	if timeout == 0 {
		timeout = defaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- the command is the constant "claude" (allow-list checked
	// above) and exec.CommandContext passes argv without a shell. The only
	// caller-supplied argv element, the session ID, is pattern-checked in Send.
	cmd := exec.CommandContext(runCtx, claudeCommand, args...)
	cmd.Env = childEnv(sp)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.WaitDelay = killGrace
	out, err := cmd.Output()

	if err != nil && ctx.Err() == nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return ErrTimeout
	}
	var exitErr *exec.ExitError
	exited := errors.As(err, &exitErr)
	if err == nil || exited {
		if json.Unmarshal(bytes.TrimSpace(out), v) == nil {
			return nil
		}
	}
	switch {
	case err == nil:
		return errors.New("claude printed output that is not the expected JSON")
	case exited:
		detail := strings.TrimSpace(string(exitErr.Stderr))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("claude %s failed: %s", args[0], detail)
	case errors.Is(err, exec.ErrNotFound):
		return ErrClaudeNotFound
	default:
		return fmt.Errorf("claude %s: %w", args[0], err)
	}
}

func childEnv(sp *ent.Spawner) []string {
	base := pipeline.BaseSpawnEnv(sp)
	for _, k := range scrubbedEnvKeys {
		delete(base, k)
	}
	env := make([]string, 0, len(base))
	for k, v := range base {
		env = append(env, k+"="+v)
	}
	return env
}
