package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/sanitize"
	"github.com/lx-wnk/kontor/server/internal/services"
)

const (
	claudeCommand  = "claude"
	defaultTimeout = 60 * time.Second
	authMethodWeb  = "claude.ai"
	// killGrace bounds how long Wait lingers on a killed child's pipes.
	killGrace = time.Second
	// maxStderrBytes bounds the CLI stderr copied into an error.
	maxStderrBytes = 4096
)

// envNamePattern is a plain env var name. A spawner env key that holds "=" or
// whitespace would otherwise turn into a different variable than the one the
// scrub list checks.
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	// ErrClaudeNotFound reports that the claude CLI is not on PATH.
	ErrClaudeNotFound = errors.New("the claude CLI is not on PATH")
	// ErrTimeout reports that the claude CLI outlived the runner's timeout.
	ErrTimeout = errors.New("the claude CLI timed out")
)

// scrubbedEnvKeys would switch the CLI to API-key or third-party auth, send the
// account's OAuth token to another host (ANTHROPIC_BASE_URL), or
// (CLAUDE_CODE_OAUTH_TOKEN, which passes BuildSpawnEnv's CLAUDE_ prefix rule)
// override the account CLAUDE_CONFIG_DIR selects. Cloud sessions need the
// claude.ai login of that config dir.
var scrubbedEnvKeys = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
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
	if err := r.runJSON(ctx, sp, text, &res, "-p", "--cloud", sessionID, "--output-format", "json"); err != nil {
		return SendResult{}, err
	}
	return res, nil
}

// AuthStatus reads the sign-in status of the spawner's Claude account.
func (r Runner) AuthStatus(ctx context.Context, sp *ent.Spawner) (AuthStatus, error) {
	var st AuthStatus
	if err := r.runJSON(ctx, sp, "", &st, "auth", "status", "--json"); err != nil {
		return AuthStatus{}, err
	}
	return st, nil
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
	// The CLI can spawn helpers; killing only the leader would orphan them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGroup(cmd.Process.Pid) }
	cmd.WaitDelay = killGrace
	out, err := cmd.Output()

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("claude %s: %w", args[0], ctxErr)
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return ErrTimeout
		}
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
		detail := cleanStderr(exitErr.Stderr)
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
		if envNamePattern.MatchString(k) {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// cleanStderr makes CLI stderr safe to store: no control characters (a newline
// becomes a space) and at most maxStderrBytes.
func cleanStderr(stderr []byte) string {
	text, _ := sanitize.ForDisplayCapped(string(stderr), maxStderrBytes)
	if len(text) > maxStderrBytes {
		text = strings.ToValidUTF8(text[:maxStderrBytes], "")
	}
	return text
}

func killGroup(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
