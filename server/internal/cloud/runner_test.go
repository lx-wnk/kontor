package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

const testSessionID = "session_abcdefgh"

var scrubbedKeys = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
	"ANTHROPIC_BASE_URL",
}

// fakeClaude installs a claude script on PATH that records argv, env and stdin
// into the returned directory, then runs body.
func fakeClaude(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nR=%q\nprintf '%%s\\n' \"$@\" > \"$R/argv\"\nenv > \"$R/env\"\ncat > \"$R/stdin\"\n%s\n", dir, body)
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	return dir
}

func readRecorded(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSend_ArgvEnvStdin(t *testing.T) {
	dir := fakeClaude(t, `echo '{"ok":true,"session_id":"session_abcdefgh","url":"https://claude.ai/code/session_abcdefgh"}'`)
	const text = "--help\nrm -rf"
	cfg := filepath.Join(dir, "account-cfg")
	spawnerEnv := map[string]string{"CLAUDE_CONFIG_DIR": cfg}
	for _, k := range scrubbedKeys {
		t.Setenv(k, "from-process")
		spawnerEnv[k] = "from-spawner"
	}

	res, err := Runner{}.Send(t.Context(), &ent.Spawner{Env: spawnerEnv}, testSessionID, text)
	if err != nil {
		t.Fatal(err)
	}

	want := SendResult{OK: true, SessionID: testSessionID, URL: "https://claude.ai/code/" + testSessionID}
	if res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	if got, want := readRecorded(t, dir, "argv"), "-p\n--cloud\n"+testSessionID+"\n--output-format\njson\n"; got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if got := readRecorded(t, dir, "stdin"); got != text {
		t.Fatalf("stdin = %q, want %q byte for byte", got, text)
	}
	if strings.Contains(readRecorded(t, dir, "argv"), "rm -rf") {
		t.Fatal("message text leaked into argv")
	}
	env := readRecorded(t, dir, "env")
	if !strings.Contains(env, "CLAUDE_CONFIG_DIR="+cfg+"\n") {
		t.Fatalf("env lacks the spawner's CLAUDE_CONFIG_DIR:\n%s", env)
	}
	for _, k := range scrubbedKeys {
		if strings.Contains(env, k+"=") {
			t.Errorf("scrubbed key %s reached the child", k)
		}
	}
}

func TestSend_OkFalse(t *testing.T) {
	fakeClaude(t, `echo '{"ok":false,"session_id":"session_x","error":"archived"}'`)

	res, err := Runner{}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")
	if err != nil {
		t.Fatal(err)
	}
	if want := (SendResult{OK: false, SessionID: "session_x", Error: "archived"}); res != want {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
}

func TestSend_NonJSONStderr(t *testing.T) {
	fakeClaude(t, `echo 'not logged in' >&2; exit 1`)

	_, err := Runner{}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Fatalf("err = %v, want one containing %q", err, "not logged in")
	}
}

func TestSend_UnreadableOutputDoesNotEchoStdout(t *testing.T) {
	fakeClaude(t, `echo 'SECRET-STDOUT'`)

	_, err := Runner{}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")
	if err == nil {
		t.Fatal("want an error for non-JSON stdout")
	}
	if strings.Contains(err.Error(), "SECRET-STDOUT") {
		t.Fatalf("error echoes CLI stdout: %v", err)
	}
}

func TestSend_Timeout(t *testing.T) {
	fakeClaude(t, `sleep 5; :`)

	start := time.Now()
	_, err := Runner{Timeout: 100 * time.Millisecond}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("took %v: the child was not killed at the deadline", elapsed)
	}
}

func TestSend_ClaudeMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Runner{}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")
	if !errors.Is(err, ErrClaudeNotFound) {
		t.Fatalf("err = %v, want ErrClaudeNotFound", err)
	}
}

func TestSend_RejectsFlagLikeSessionID(t *testing.T) {
	dir := fakeClaude(t, `echo '{"ok":true}'`)

	_, err := Runner{}.Send(t.Context(), &ent.Spawner{}, "--dangerously-skip-permissions", "hi")

	if !errors.Is(err, ErrInvalidSessionRef) {
		t.Fatalf("err = %v, want ErrInvalidSessionRef", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "argv")); statErr == nil {
		t.Fatal("claude ran despite an invalid session ID")
	}
}

func TestAuthStatus_DropsIdentityFields(t *testing.T) {
	dir := fakeClaude(t, `echo '{"loggedIn":true,"authMethod":"claude.ai","apiProvider":"firstParty","subscriptionType":"max","email":"a@b.c","orgName":"Acme","orgId":"o1"}'`)

	st, err := Runner{}.AuthStatus(t.Context(), &ent.Spawner{})
	if err != nil {
		t.Fatal(err)
	}

	want := AuthStatus{LoggedIn: true, AuthMethod: "claude.ai", APIProvider: "firstParty", SubscriptionType: "max"}
	if st != want {
		t.Fatalf("status = %+v, want %+v", st, want)
	}
	if n := reflect.TypeOf(st).NumField(); n != 4 {
		t.Fatalf("AuthStatus has %d fields, want exactly the 4 named ones", n)
	}
	if got, want := readRecorded(t, dir, "argv"), "auth\nstatus\n--json\n"; got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// A signed-out CLI prints its JSON and exits 1.
func TestAuthStatus_LoggedOutExitsNonZeroWithJSON(t *testing.T) {
	fakeClaude(t, `echo '{"loggedIn":false,"authMethod":"none","apiProvider":"firstParty"}'; exit 1`)

	st, err := Runner{}.AuthStatus(t.Context(), &ent.Spawner{})
	if err != nil {
		t.Fatal(err)
	}
	if st.LoggedIn || st.CloudReady() {
		t.Fatalf("status = %+v, want logged out", st)
	}
}

func TestAuthStatus_ClaudeMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Runner{}.AuthStatus(t.Context(), &ent.Spawner{})
	if !errors.Is(err, ErrClaudeNotFound) {
		t.Fatalf("err = %v, want ErrClaudeNotFound", err)
	}
}

func TestAuthStatus_CloudReady(t *testing.T) {
	tests := []struct {
		name string
		in   AuthStatus
		want bool
	}{
		{"claude.ai login", AuthStatus{LoggedIn: true, AuthMethod: "claude.ai"}, true},
		{"api key login", AuthStatus{LoggedIn: true, AuthMethod: "api_key"}, false},
		{"logged out", AuthStatus{LoggedIn: false, AuthMethod: "claude.ai"}, false},
		{"zero value", AuthStatus{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.CloudReady(); got != tt.want {
				t.Fatalf("CloudReady() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSend_EnvKeysCarryingAssignmentsNeverReachChild(t *testing.T) {
	dir := fakeClaude(t, `echo '{"ok":true}'`)
	spawnerEnv := map[string]string{
		"ANTHROPIC_API_KEY=sk-x":      "y",
		"CLAUDE_CODE_OAUTH_TOKEN=t":   "y",
		"CLAUDE_CONFIG_DIR":           filepath.Join(dir, "cfg"),
		"KEY WITH SPACE":              "y",
		"1STARTS_WITH_DIGIT":          "y",
		"VALID_NAME_9":                "kept",
		"ANTHROPIC_BASE_URL":          "https://evil.example",
		"ANTHROPIC_BASE_URL=http://x": "y",
	}

	if _, err := (Runner{}).Send(t.Context(), &ent.Spawner{Env: spawnerEnv}, testSessionID, "hi"); err != nil {
		t.Fatal(err)
	}

	env := readRecorded(t, dir, "env")
	for _, leaked := range []string{"sk-x", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "evil.example", "KEY WITH SPACE", "1STARTS_WITH_DIGIT"} {
		if strings.Contains(env, leaked) {
			t.Errorf("env contains %q:\n%s", leaked, env)
		}
	}
	if !strings.Contains(env, "VALID_NAME_9=kept\n") {
		t.Errorf("a plain env name was dropped:\n%s", env)
	}
}

func TestSend_StderrIsCappedAndStripped(t *testing.T) {
	fakeClaude(t, `printf 'line1\nline2\033[31m' >&2; head -c 10240 /dev/zero | tr '\0' x >&2; exit 1`)

	_, err := Runner{}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")

	if err == nil {
		t.Fatal("want an error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "line1 line2") {
		t.Errorf("newline was not turned into a space: %.80q", msg)
	}
	for _, r := range msg {
		if r != ' ' && r < 0x20 || r == 0x7f {
			t.Fatalf("error contains control character %q", r)
		}
	}
	if n := strings.Count(msg, "x"); n > 4096 || n == 0 {
		t.Errorf("x count = %d, want 1..4096", n)
	}
	if len(msg) > 4096+128 {
		t.Errorf("error is %d bytes, want stderr capped at 4096 plus a short prefix", len(msg))
	}
}

func TestSend_ParentCancelIsContextCanceled(t *testing.T) {
	fakeClaude(t, `exec sleep 5`)
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	_, err := Runner{}.Send(ctx, &ent.Spawner{}, testSessionID, "hi")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrTimeout) {
		t.Fatal("a cancelled parent must not read as the runner's own timeout")
	}
}

func TestSend_UndecodableJSONReturnsZeroResult(t *testing.T) {
	fakeClaude(t, `echo '{"session_id":"session_x","ok":"not-a-bool"}'`)

	res, err := Runner{}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")

	if err == nil {
		t.Fatal("want an error")
	}
	if res != (SendResult{}) {
		t.Fatalf("result = %+v, want the zero value", res)
	}
}

func TestAuthStatus_UndecodableJSONReturnsZeroStatus(t *testing.T) {
	fakeClaude(t, `echo '{"loggedIn":true,"authMethod":5}'`)

	st, err := Runner{}.AuthStatus(t.Context(), &ent.Spawner{})

	if err == nil {
		t.Fatal("want an error")
	}
	if st != (AuthStatus{}) {
		t.Fatalf("status = %+v, want the zero value", st)
	}
}

// The fake's own child outlives it unless the whole process group is killed.
func TestSend_TimeoutKillsProcessGroup(t *testing.T) {
	dir := fakeClaude(t, `sleep 30 & echo $! > "$R/grandchild"; wait`)

	_, err := Runner{Timeout: 300 * time.Millisecond}.Send(t.Context(), &ent.Spawner{}, testSessionID, "hi")
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}

	pid, convErr := strconv.Atoi(strings.TrimSpace(readRecorded(t, dir, "grandchild")))
	if convErr != nil {
		t.Fatal(convErr)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("grandchild %d survived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
