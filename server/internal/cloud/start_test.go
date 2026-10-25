package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

const (
	startSessionID   = "session_01P9rodyByipbcC2hCQz5X6A"
	startDescription = "Fix the flaky login test"
	startCreated     = `echo 'Created cloud session: Fix the flaky login test'
echo 'View: https://claude.ai/code/session_01P9rodyByipbcC2hCQz5X6A?from=cli&m=0'
echo 'Resume with: claude --teleport session_01P9rodyByipbcC2hCQz5X6A'`
)

// fakeStartClaude installs a claude script on PATH that records argv, env and
// its physical cwd into the returned directory, refuses to run without a
// terminal like the real `claude --cloud`, then runs body.
func fakeStartClaude(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nR=%q\nprintf '%%s\\n' \"$@\" > \"$R/argv\"\nenv > \"$R/env\"\npwd -P > \"$R/pwd\"\n"+
		"[ -t 0 ] || { echo 'Error: --cloud requires an interactive terminal.' >&2; exit 1; }\n%s\n", dir, body)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return dir
}

func wantStarted(t *testing.T, res StartResult, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	if want := (StartResult{SessionID: startSessionID, URL: SessionURL(startSessionID)}); res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
}

func recordedPid(t *testing.T, dir, name string) int {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(readRecorded(t, dir, name)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	return pid
}

func requireDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d survived", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func recordedEnvValue(env, key string) string {
	for _, line := range strings.Split(env, "\n") {
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestStart_CreatesSessionOnATerminal(t *testing.T) {
	rec := fakeStartClaude(t, startCreated)
	work := t.TempDir()

	res, err := Runner{}.Start(t.Context(), &ent.Spawner{}, work, startDescription)

	wantStarted(t, res, err)
	if got, want := readRecorded(t, rec, "argv"), "--cloud\n"+startDescription+"\n"; got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	wantDir, err := filepath.EvalSymlinks(work)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readRecorded(t, rec, "pwd")); got != wantDir {
		t.Fatalf("child cwd = %q, want %q", got, wantDir)
	}
}

func TestStart_ParsesURLAmidTerminalEscapes(t *testing.T) {
	fakeStartClaude(t, `printf 'Created cloud session: x\r\n\033[2K\033[1mView:\033[22m \033[36mhttps://claude.ai/code/session_01P9rodyByipbcC2hCQz5X6A?from=cli&m=0\033[39m\r\n'
printf '\033]0;claude\007Resume with: claude --teleport session_01P9rodyByipbcC2hCQz5X6A\r\n'`)

	res, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	wantStarted(t, res, err)
}

func TestStart_WaitsForTheWholeURLLine(t *testing.T) {
	fakeStartClaude(t, `printf 'View: https://claude.ai/code/session_01P9rody'; sleep 0.3; printf 'ByipbcC2hCQz5X6A?from=cli&m=0\n'`)

	res, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	wantStarted(t, res, err)
}

func TestStart_IgnoresTheTitleLine(t *testing.T) {
	for name, title := range map[string]string{
		"another session URL":       "see https://claude.ai/code/session_wrongwrongwrong",
		"the trust prompt fragment": "Quick safety check: is the login flaky",
	} {
		t.Run(name, func(t *testing.T) {
			fakeStartClaude(t, fmt.Sprintf("echo 'Created cloud session: %s'\necho 'View: %s?from=cli&m=0'", title, SessionURL(startSessionID)))

			res, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

			wantStarted(t, res, err)
		})
	}
}

func TestStart_UntrustedFolderKillsProcessGroup(t *testing.T) {
	rec := fakeStartClaude(t, `(trap '' HUP; exec sleep 30) &
echo $! > "$R/grandchild"
echo 'Quick safety check: Is this a project you created or one you trust?'
sleep 30`)

	start := time.Now()
	_, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	if !errors.Is(err, ErrFolderNotTrusted) {
		t.Fatalf("err = %v, want ErrFolderNotTrusted", err)
	}
	if errors.Is(err, ErrStartUnconfirmed) {
		t.Fatal("no session title was printed, so the start is not unconfirmed")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: the trust prompt was not answered by a kill", elapsed)
	}
	requireDead(t, recordedPid(t, rec, "grandchild"))
}

func TestStart_KillsAGrandchildAfterANormalExit(t *testing.T) {
	rec := fakeStartClaude(t, `(trap '' HUP; exec sleep 30) &
echo $! > "$R/grandchild"
`+startCreated)

	res, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	wantStarted(t, res, err)
	requireDead(t, recordedPid(t, rec, "grandchild"))
}

func TestStart_StopsAChildThatLingersPastTheExitGrace(t *testing.T) {
	rec := fakeStartClaude(t, "echo $$ > \"$R/pid\"\n"+startCreated+"\nsleep 30")

	start := time.Now()
	res, err := Runner{exitGrace: 200 * time.Millisecond}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	wantStarted(t, res, err)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %v: the lingering child was not stopped after the exit grace", elapsed)
	}
	requireDead(t, recordedPid(t, rec, "pid"))
}

func TestStart_Timeout(t *testing.T) {
	fakeStartClaude(t, `sleep 5`)

	start := time.Now()
	_, err := Runner{Timeout: 200 * time.Millisecond}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if errors.Is(err, ErrStartUnconfirmed) {
		t.Fatal("no session title was printed, so the start is not unconfirmed")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v: the child was not killed at the deadline", elapsed)
	}
}

func TestStart_ParentCancelIsContextCanceled(t *testing.T) {
	fakeStartClaude(t, `exec sleep 5`)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := Runner{}.Start(ctx, &ent.Spawner{}, t.TempDir(), startDescription)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatal("a cancelled parent must not read as the runner's own timeout")
	}
}

func TestStart_ExitWithoutURLReportsOutput(t *testing.T) {
	fakeStartClaude(t, `echo boom; exit 1`)

	_, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want one containing %q", err, "boom")
	}
	if errors.Is(err, ErrStartUnconfirmed) {
		t.Fatal("no session title was printed, so the start is not unconfirmed")
	}
}

func TestStart_ChattyChildIsCutOff(t *testing.T) {
	for name, body := range map[string]string{
		"one long line":    `head -c 102400 /dev/zero | tr '\0' x; sleep 30`,
		"many short lines": `yes x | head -c 102400; sleep 30`,
	} {
		t.Run(name, func(t *testing.T) {
			fakeStartClaude(t, body)

			start := time.Now()
			_, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

			if !errors.Is(err, errStartOutputTooLong) {
				t.Fatalf("err = %v, want errStartOutputTooLong", err)
			}
			if errors.Is(err, ErrStartUnconfirmed) {
				t.Fatal("no session title was printed, so the start is not unconfirmed")
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("took %v: the output cap did not stop the child", elapsed)
			}
		})
	}
}

func TestStart_FailureAfterTheTitleLineIsUnconfirmed(t *testing.T) {
	const title = "echo 'Created cloud session: Fix the flaky login test'\n"
	for name, tc := range map[string]struct {
		body    string
		timeout time.Duration
		cause   error
	}{
		"timeout":          {body: `sleep 30`, timeout: 300 * time.Millisecond, cause: ErrTimeout},
		"exit without URL": {body: `exit 1`},
		"trust prompt":     {body: "echo 'Quick safety check: Is this a project you created or one you trust?'\nsleep 30", cause: ErrFolderNotTrusted},
		"output cap":       {body: `yes x | head -c 102400; sleep 30`, cause: errStartOutputTooLong},
	} {
		t.Run(name, func(t *testing.T) {
			fakeStartClaude(t, title+tc.body)

			start := time.Now()
			_, err := Runner{Timeout: tc.timeout}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

			if !errors.Is(err, ErrStartUnconfirmed) {
				t.Fatalf("err = %v, want ErrStartUnconfirmed", err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("err = %v, want it to wrap %v too", err, tc.cause)
			}
			if elapsed := time.Since(start); elapsed > 5*time.Second {
				t.Fatalf("took %v: the child was not stopped", elapsed)
			}
		})
	}
}

func TestStart_TimeoutKeepsTheOutputTail(t *testing.T) {
	fakeStartClaude(t, `echo 'still provisioning'; sleep 30`)

	_, err := Runner{Timeout: 300 * time.Millisecond}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if !strings.Contains(err.Error(), "still provisioning") {
		t.Fatalf("err = %q, want the output tail in it", err)
	}
}

func TestStart_TermFallback(t *testing.T) {
	for name, tc := range map[string]struct {
		term  string
		unset bool
		want  string
	}{
		"unset":   {unset: true, want: "xterm-256color"},
		"empty":   {want: "xterm-256color"},
		"present": {term: "dumb", want: "dumb"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := fakeStartClaude(t, startCreated)
			t.Setenv("TERM", tc.term)
			if tc.unset {
				if err := os.Unsetenv("TERM"); err != nil {
					t.Fatal(err)
				}
			}

			res, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

			wantStarted(t, res, err)
			if got := recordedEnvValue(readRecorded(t, rec, "env"), "TERM"); got != tc.want {
				t.Fatalf("TERM = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStart_RejectsInvalidDescription(t *testing.T) {
	for _, desc := range []string{"", "   ", "-x", "--dangerously-skip-permissions"} {
		t.Run(strconv.Quote(desc), func(t *testing.T) {
			rec := fakeStartClaude(t, startCreated)

			_, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), desc)

			if !errors.Is(err, ErrInvalidDescription) {
				t.Fatalf("err = %v, want ErrInvalidDescription", err)
			}
			if _, statErr := os.Stat(filepath.Join(rec, "argv")); statErr == nil {
				t.Fatal("claude ran despite an invalid description")
			}
		})
	}
}

func TestStart_BlockedEnvNeverReachesChild(t *testing.T) {
	rec := fakeStartClaude(t, startCreated)
	cfg := filepath.Join(rec, "account-cfg")
	spawnerEnv := map[string]string{"CLAUDE_CONFIG_DIR": cfg}
	for _, k := range blockedEnvKeys {
		t.Setenv(k, "from-process")
		spawnerEnv[k] = "from-spawner"
	}

	res, err := Runner{}.Start(t.Context(), &ent.Spawner{Env: spawnerEnv}, t.TempDir(), startDescription)

	wantStarted(t, res, err)
	env := readRecorded(t, rec, "env")
	if !strings.Contains(env, "CLAUDE_CONFIG_DIR="+cfg+"\n") {
		t.Fatalf("env lacks the spawner's CLAUDE_CONFIG_DIR:\n%s", env)
	}
	for _, k := range blockedEnvKeys {
		if envHas(env, k) {
			t.Errorf("blocked key %s reached the child", k)
		}
	}
}

func TestStart_ClaudeMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Runner{}.Start(t.Context(), &ent.Spawner{}, t.TempDir(), startDescription)

	if !errors.Is(err, ErrClaudeNotFound) {
		t.Fatalf("err = %v, want ErrClaudeNotFound", err)
	}
}

func TestStart_DefaultTimeout(t *testing.T) {
	if DefaultStartTimeout != 90*time.Second {
		t.Fatalf("DefaultStartTimeout = %v, want 90s", DefaultStartTimeout)
	}
}
