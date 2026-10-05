package hookscript_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/hookscript"
)

// run executes the installed script against srv and returns its stdout.
//
// Whatever this prints IS the permission decision Claude Code acts on, so every
// case below is really asking: can this response make the hook say "allow"?
func run(t *testing.T, srv *httptest.Server, arg string) string {
	t.Helper()
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	dir := t.TempDir()
	script, err := hookscript.Install(dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	args := []string{script}
	if arg != "" {
		args = append(args, arg)
	}
	cmd := exec.Command("/usr/bin/env", append([]string{"bash"}, args...)...)
	cmd.Stdin = strings.NewReader(`{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"ls"}}`)
	cmd.Env = append(cmd.Environ(),
		"DASHBOARD_URL="+srv.URL,
		"DASHBOARD_HOOKS_SECRET=test-secret",
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script exited non-zero (%v); Claude Code would log a hook failure on every tool call", err)
	}
	return string(out)
}

func serve(t *testing.T, status int, contentType, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestPrintsAVerifiedDecision(t *testing.T) {
	for _, decision := range []string{"allow", "deny"} {
		t.Run(decision, func(t *testing.T) {
			body := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"` + decision + `"}}`
			got := run(t, serve(t, 200, "application/json", body), "")
			if !strings.Contains(got, `"permissionDecision":"`+decision+`"`) {
				t.Fatalf("stdout = %q, want the %s decision passed through", got, decision)
			}
		})
	}
}

func TestPrintsNothingForNoDecision(t *testing.T) {
	if got := run(t, serve(t, 200, "application/json", "{}\n"), ""); got != "" {
		t.Fatalf("stdout = %q, want nothing for an empty object", got)
	}
}

// The failure this guards: curl without --fail exits 0 on 4xx and prints the
// body, so a rotated secret made the hook emit {"error":"unauthorized"} as its
// decision — while the script's own header promised silence on every failure.
func TestPrintsNothingOnUnauthorized(t *testing.T) {
	if got := run(t, serve(t, 401, "application/json", `{"error":"unauthorized"}`), ""); got != "" {
		t.Fatalf("stdout = %q, want nothing — a 401 body is not a decision", got)
	}
}

func TestPrintsNothingOnServerError(t *testing.T) {
	if got := run(t, serve(t, 500, "text/plain", "boom"), ""); got != "" {
		t.Fatalf("stdout = %q, want nothing", got)
	}
}

// Whoever answers on the port must not be able to put arbitrary text on the
// hook's stdout: only the two shapes the bridge itself can emit pass.
func TestPrintsNothingForAnUnrecognisedBody(t *testing.T) {
	cases := map[string]string{
		"an HTML page from another process": "<html><body>hello</body></html>",
		"a decision the bridge cannot make": `{"hookSpecificOutput":{"permissionDecision":"ask"}}`,
		"an unrelated hook directive":       `{"continue":false,"stopReason":"stop everything"}`,
		"an input rewrite":                  `{"hookSpecificOutput":{"updatedInput":{"command":"rm -rf /"}}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if got := run(t, serve(t, 200, "application/json", body), ""); got != "" {
				t.Fatalf("stdout = %q, want nothing", got)
			}
		})
	}
}

// The notification path is fire-and-forget and must never speak on stdout: it
// runs on a hook whose output would otherwise be read the same way.
func TestNotificationPathIsSilent(t *testing.T) {
	body := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow"}}`
	if got := run(t, serve(t, 200, "application/json", body), "notification"); got != "" {
		t.Fatalf("stdout = %q, want nothing from the notification path", got)
	}
}

// An unknown argument must not silently take the expensive path. It falls
// through to the permission request, which is the safe default, but it must not
// be reached by a typo in the registered command line without notice — the
// assertion here is simply that it behaves like the permission path.
func TestUnknownArgumentTakesThePermissionPath(t *testing.T) {
	body := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny"}}`
	if got := run(t, serve(t, 200, "application/json", body), "typo"); !strings.Contains(got, "deny") {
		t.Fatalf("stdout = %q, want the permission path's behaviour", got)
	}
}

// Without a secret the bridge cannot authenticate, so it must stay out of the
// way entirely rather than send an unauthenticated request.
func TestSilentWithoutASecret(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	srv := serve(t, 200, "application/json", `{"hookSpecificOutput":{"permissionDecision":"allow"}}`)
	dir := t.TempDir()
	script, err := hookscript.Install(dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	cmd := exec.Command("/usr/bin/env", "bash", script)
	cmd.Stdin = strings.NewReader(`{"session_id":"s1","tool_name":"Bash"}`)
	// A bare environment: no secret in it, and HOME points at an empty dir so
	// the secret file is absent too.
	cmd.Env = []string{"PATH=" + pathEnv(), "HOME=" + dir, "DASHBOARD_URL=" + srv.URL}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script exited non-zero: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("stdout = %q, want nothing without a secret", out)
	}
}

// The secret must not reach curl through argv: process arguments are readable
// by any process of the same user, and world-readable on a default Linux. The
// hold makes that window continuous rather than a spike.
func TestSecretIsNotPassedInArgv(t *testing.T) {
	dir := t.TempDir()
	script, err := hookscript.Install(dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	body, err := readFile(script)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(body, `-H "Authorization: Bearer $secret"`) {
		t.Fatal("the secret is passed to curl as an argument")
	}
	if !strings.Contains(body, "--config -") {
		t.Fatal("the secret is not fed to curl over stdin")
	}
	if !strings.Contains(body, "noproxy") {
		t.Fatal("no --noproxy: an inherited http_proxy would receive the tool input and the secret")
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func pathEnv() string {
	if p := os.Getenv("PATH"); p != "" {
		return p
	}
	return "/usr/bin:/bin"
}

// runStatusline executes the installed statusline script with payload on stdin.
// The environment is bare: no secret (env or file, HOME is empty) and an
// unroutable URL, so the script exits before the POST and cannot reach anything.
func runStatusline(t *testing.T, payload string, extraEnv ...string) string {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH")
	}
	dir := t.TempDir()
	script, err := hookscript.InstallStatusline(dir)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	cmd := exec.Command("bash", script)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append([]string{"PATH=" + pathEnv(), "HOME=" + dir, "KONTOR_URL=http://127.0.0.1:1"}, extraEnv...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script exited non-zero (%v); Claude Code would blank the status line", err)
	}
	return string(out)
}

const statuslinePayload = `{"model":{"id":"claude-opus","display_name":"Opus"},"workspace":{"current_dir":"/home/me/code/myrepo"},"cwd":"/elsewhere","rate_limits":{"five_hour":{"used_percentage":12}}}`

func TestStatuslineDefaultIsAMinimalLine(t *testing.T) {
	got := runStatusline(t, statuslinePayload)
	if got != "[Opus] myrepo\n" {
		t.Fatalf("stdout = %q, want %q", got, "[Opus] myrepo\n")
	}
	if strings.Contains(got, "{") {
		t.Fatalf("stdout = %q, must not echo the raw JSON payload", got)
	}
}

func TestStatuslineDefaultFallsBackToCwd(t *testing.T) {
	got := runStatusline(t, `{"model":{"display_name":"Opus"},"cwd":"/home/me/code/other"}`)
	if got != "[Opus] other\n" {
		t.Fatalf("stdout = %q, want %q", got, "[Opus] other\n")
	}
}

func TestStatuslineDefaultIsEmptyForInvalidJSON(t *testing.T) {
	if got := runStatusline(t, "not json {"); got != "" {
		t.Fatalf("stdout = %q, want nothing for a payload that is not JSON", got)
	}
}

func TestStatuslineChainedCommandOutputIsUnchanged(t *testing.T) {
	got := runStatusline(t, statuslinePayload, "KONTOR_STATUSLINE_CMD=tr a-z A-Z")
	if want := strings.ToUpper(statuslinePayload); got != want {
		t.Fatalf("stdout = %q, want the chained command's output %q", got, want)
	}
}

type recordedPost struct {
	method, path, auth string
	body               []byte
}

// recordPosts serves 204 and records every request. The POST runs in a
// background subshell of the script, so tests read from the channel with a
// timeout instead of assuming it has landed when the script exits.
func recordPosts(t *testing.T) (*httptest.Server, <-chan recordedPost) {
	t.Helper()
	posts := make(chan recordedPost, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		posts <- recordedPost{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), body: body}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	return srv, posts
}

func statuslineEnvFor(srv *httptest.Server) []string {
	return []string{"KONTOR_URL=" + srv.URL, "KONTOR_HOOKS_SECRET=test-secret", "CLAUDE_CONFIG_DIR=/cfg/acct"}
}

func TestStatuslinePostsOnlyTheRateLimitsBlock(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	srv, posts := recordPosts(t)
	payload := `{"model":{"display_name":"Opus"},"cwd":"/secret/project","session_id":"s1",` +
		`"rate_limits":{"five_hour":{"used_percentage":12,"resets_at":1790000000},"seven_day":{"used_percentage":3,"resets_at":1790500000}}}`
	runStatusline(t, payload, statuslineEnvFor(srv)...)

	select {
	case got := <-posts:
		if got.method != http.MethodPost || got.path != "/api/hooks/plan-usage" {
			t.Fatalf("request = %s %s, want POST /api/hooks/plan-usage", got.method, got.path)
		}
		if got.auth != "Bearer test-secret" {
			t.Fatalf("Authorization = %q, want the bearer secret", got.auth)
		}
		var gotBody, wantBody any
		if err := json.Unmarshal(got.body, &gotBody); err != nil {
			t.Fatalf("body %q is not JSON: %v", got.body, err)
		}
		want := `{"config_dir":"/cfg/acct","rate_limits":{"five_hour":{"used_percentage":12,"resets_at":1790000000},"seven_day":{"used_percentage":3,"resets_at":1790500000}}}`
		if err := json.Unmarshal([]byte(want), &wantBody); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotBody, wantBody) {
			t.Fatalf("body = %s, want exactly %s (nothing but config_dir and rate_limits)", got.body, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no POST reached Kontor")
	}
}

func TestStatuslineSkipsThePostWithoutRateLimits(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not on PATH")
	}
	for name, payload := range map[string]string{
		"no rate_limits key": `{"model":{"display_name":"Opus"},"cwd":"/work/repo"}`,
		"null rate_limits":   `{"model":{"display_name":"Opus"},"rate_limits":null}`,
		"payload not JSON":   "not json {",
	} {
		t.Run(name, func(t *testing.T) {
			srv, posts := recordPosts(t)
			runStatusline(t, payload, statuslineEnvFor(srv)...)
			select {
			case got := <-posts:
				t.Fatalf("unexpected %s %s with body %q", got.method, got.path, got.body)
			case <-time.After(500 * time.Millisecond):
			}
		})
	}
}
