package cloud

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func unsetConfigDir(t *testing.T) {
	t.Helper()
	t.Setenv(claudeconfig.EnvVar, "")
	if err := os.Unsetenv(claudeconfig.EnvVar); err != nil {
		t.Fatal(err)
	}
}

func TestAccountDir_UnsetIsEmpty(t *testing.T) {
	unsetConfigDir(t)

	if got := AccountDir(&ent.Spawner{}); got != "" {
		t.Fatalf("AccountDir = %q, want \"\"", got)
	}
}

func TestAccountDir_ProcessEnvWhenSpawnerHasNone(t *testing.T) {
	t.Setenv(claudeconfig.EnvVar, "/srv/claude-server")

	if got := AccountDir(&ent.Spawner{}); got != "/srv/claude-server" {
		t.Fatalf("AccountDir = %q, want the server's dir", got)
	}
}

func TestAccountDir_ExpandsTildeButKeepsSymlink(t *testing.T) {
	unsetConfigDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, ".claude-personal")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".claude")); err != nil {
		t.Fatal(err)
	}

	got := AccountDir(&ent.Spawner{Env: map[string]string{claudeconfig.EnvVar: "~/.claude"}})

	if want := filepath.Join(home, ".claude"); got != want {
		t.Fatalf("AccountDir = %q, want the literal %q (a symlink and its target are separate logins)", got, want)
	}
}

func TestAccountDir_MatchesWhatTheChildReceives(t *testing.T) {
	unsetConfigDir(t)
	dir := fakeClaude(t, `echo '{"ok":true}'`)
	sp := &ent.Spawner{Env: map[string]string{claudeconfig.EnvVar: filepath.Join(dir, "acct")}}

	if _, err := (Runner{}).Send(t.Context(), sp, testSessionID, "hi"); err != nil {
		t.Fatal(err)
	}

	env := readRecorded(t, dir, "env")
	if want := claudeconfig.EnvVar + "=" + AccountDir(sp) + "\n"; !strings.Contains("\n"+env, "\n"+want) {
		t.Fatalf("child env lacks %q:\n%s", want, env)
	}
}

func TestLoginHint(t *testing.T) {
	tests := []struct {
		name string
		dir  string
		want string
	}{
		{"unset", "", "claude auth login"},
		{"plain", "/Users/a/.claude-work", "CLAUDE_CONFIG_DIR='/Users/a/.claude-work' claude auth login"},
		{"space and quote", "/Users/a/my dir/it's", `CLAUDE_CONFIG_DIR='/Users/a/my dir/it'\''s' claude auth login`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.dir == "" {
				unsetConfigDir(t)
			}
			sp := &ent.Spawner{Env: map[string]string{claudeconfig.EnvVar: tt.dir}}
			if got := LoginHint(sp); got != tt.want {
				t.Fatalf("LoginHint = %q, want %q", got, tt.want)
			}
		})
	}
}

// The quoted hint must hand the shell exactly the dir, however odd the path.
func TestLoginHint_ShellRoundTrip(t *testing.T) {
	const dir = "/tmp/a b/it's $HOME `x`"
	hint := LoginHint(&ent.Spawner{Env: map[string]string{claudeconfig.EnvVar: dir}})
	assignment := strings.TrimSuffix(hint, " claude auth login")

	out, err := exec.Command("/bin/sh", "-c", assignment+` printenv `+claudeconfig.EnvVar).Output() //nolint:gosec // test-built shell line
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(string(out), "\n"); got != dir {
		t.Fatalf("shell saw %q, want %q", got, dir)
	}
}

func TestTrustHint(t *testing.T) {
	tests := []struct {
		name    string
		account string
		dir     string
		want    string
	}{
		{"no account dir", "", "/work/app", "cd '/work/app' && claude"},
		{"own account dir", "/Users/a/.claude-work", "/work/app", "cd '/work/app' && CLAUDE_CONFIG_DIR='/Users/a/.claude-work' claude"},
		{"quotes in both", "/Users/a/it's", "/work/it's app", `cd '/work/it'\''s app' && CLAUDE_CONFIG_DIR='/Users/a/it'\''s' claude`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp := &ent.Spawner{}
			if tt.account == "" {
				unsetConfigDir(t)
			} else {
				sp.Env = map[string]string{claudeconfig.EnvVar: tt.account}
			}
			if got := TrustHint(sp, tt.dir); got != tt.want {
				t.Fatalf("TrustHint = %q, want %q", got, tt.want)
			}
		})
	}
}

// The quoted hint must land the shell in exactly the folder with exactly the account dir.
func TestTrustHint_ShellRoundTrip(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "a b", "it's $HOME `x`")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	const account = "/tmp/a b/it's $HOME `y`"
	hint := TrustHint(&ent.Spawner{Env: map[string]string{claudeconfig.EnvVar: account}}, folder)
	line := strings.TrimSuffix(hint, " claude") + ` sh -c 'pwd -P; printenv ` + claudeconfig.EnvVar + `'`

	out, err := exec.Command("/bin/sh", "-c", line).Output() //nolint:gosec // test-built shell line
	if err != nil {
		t.Fatal(err)
	}
	wantFolder, err := filepath.EvalSymlinks(folder)
	if err != nil {
		t.Fatal(err)
	}
	if want := wantFolder + "\n" + account + "\n"; string(out) != want {
		t.Fatalf("shell saw %q, want %q", out, want)
	}
}
