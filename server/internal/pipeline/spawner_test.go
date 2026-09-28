package pipeline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestBuildAllowList_ExcludesGitPushByDefault(t *testing.T) {
	pattern := "git push origin HEAD"
	perms := []*ent.TaskPermission{
		{Tool: "Bash", Pattern: &pattern, Granted: true},
		{Tool: "Read", Granted: true},
	}
	allow := pipeline.BuildAllowList("manual", perms, false, false, nil)
	require.Contains(t, allow, "Read")
	for _, a := range allow {
		require.NotContains(t, a, "git push")
	}
}

func TestBuildAllowList_AllowsGitPushWhenEnabled(t *testing.T) {
	pattern := "git push origin HEAD"
	perms := []*ent.TaskPermission{
		{Tool: "Bash", Pattern: &pattern, Granted: true},
	}
	allow := pipeline.BuildAllowList("manual", perms, false, true, nil)
	require.Contains(t, allow, "Bash(git push origin HEAD)")
}

func TestBuildAllowList_FiltersDenied(t *testing.T) {
	perms := []*ent.TaskPermission{
		{Tool: "Bash", Granted: false},
		{Tool: "Read", Granted: true},
	}
	allow := pipeline.BuildAllowList("manual", perms, false, false, nil)
	require.Contains(t, allow, "Read")
	for _, a := range allow {
		require.NotEqual(t, "Bash", a)
	}
}

func TestBuildAllowList_IncludesChannelTools(t *testing.T) {
	allow := pipeline.BuildAllowList("manual", nil, true, false, nil)
	require.Contains(t, allow, "mcp__kontor-channel__request_permission")
	require.Contains(t, allow, "mcp__kontor-channel__dashboard_reply")
}

func TestBuildSpawnArgs_Basic(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{},
		StageRun: &ent.StageRun{},
		Prompt:   "do the thing",
		Model:    "claude-opus-4-7",
	}
	args := pipeline.BuildSpawnArgs(opts)
	require.Contains(t, args, "-p")
	require.Contains(t, args, "do the thing")
	require.Contains(t, args, "--model")
	require.Contains(t, args, "claude-opus-4-7")
}

func TestBuildSpawnArgs_WithResume(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:            &ent.Task{},
		StageRun:        &ent.StageRun{},
		Prompt:          "p",
		ResumeSessionID: "abc123",
	}
	args := pipeline.BuildSpawnArgs(opts)
	require.Contains(t, args, "--resume")
	require.Contains(t, args, "abc123")
}

func TestBuildSpawnArgs_WithEffort(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{},
		StageRun: &ent.StageRun{},
		Prompt:   "p",
		Effort:   "high",
	}
	args := pipeline.BuildSpawnArgs(opts)
	require.Contains(t, args, "--effort")
	require.Contains(t, args, "high")
}

func TestBuildSpawnArgs_NoEffortFlagWhenUnset(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{},
		StageRun: &ent.StageRun{},
		Prompt:   "p",
	}
	args := pipeline.BuildSpawnArgs(opts)
	require.NotContains(t, args, "--effort")
}

func TestBuildSpawnArgs_DefaultPermissionModeWithoutSkip(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{},
		StageRun: &ent.StageRun{},
		Prompt:   "p",
	}
	args := pipeline.BuildSpawnArgs(opts)
	require.Contains(t, args, "--permission-mode")
	require.Contains(t, args, "default")
}

func TestBuildSpawnArgs_OmitsPermissionModeForSkipSpawner(t *testing.T) {
	for _, flag := range []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions"} {
		opts := pipeline.SpawnAgentOptions{
			Task:     &ent.Task{},
			StageRun: &ent.StageRun{},
			Prompt:   "p",
			Spawner:  &ent.Spawner{Command: "claude", Args: []string{flag}},
		}
		args := pipeline.BuildSpawnArgs(opts)
		require.NotContains(t, args, "--permission-mode",
			"skip-permissions spawner (%s) must not get a forced --permission-mode, which overrides the skip", flag)
	}
}

func TestBuildSpawnArgs_OmitsDefaultWhenSpawnerSetsPermissionMode(t *testing.T) {
	// Spawner declares its own --permission-mode auto → dashboard must NOT append
	// a second --permission-mode default (claude errors on the duplicate flag).
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{},
		StageRun: &ent.StageRun{},
		Prompt:   "p",
		Spawner:  &ent.Spawner{Command: "claude", Args: []string{"--permission-mode", "auto"}},
	}
	args := pipeline.BuildSpawnArgs(opts)
	require.NotContains(t, args, "default",
		"dashboard must not append --permission-mode default when the spawner sets its own mode")
}

func TestBuildSpawnArgs_AdditionalDirs(t *testing.T) {
	t.Run("no additional dirs produces no --add-dir flags", func(t *testing.T) {
		opts := pipeline.SpawnAgentOptions{
			Task:     &ent.Task{},
			StageRun: &ent.StageRun{},
			Prompt:   "p",
		}
		args := pipeline.BuildSpawnArgs(opts)
		for _, a := range args {
			require.NotEqual(t, "--add-dir", a)
		}
	})

	t.Run("each additional dir gets its own --add-dir flag", func(t *testing.T) {
		opts := pipeline.SpawnAgentOptions{
			Task:           &ent.Task{},
			StageRun:       &ent.StageRun{},
			Prompt:         "p",
			AdditionalDirs: []string{"/extra1", "/extra2"},
		}
		args := pipeline.BuildSpawnArgs(opts)

		var addDirValues []string
		for i, a := range args {
			if a == "--add-dir" && i+1 < len(args) {
				addDirValues = append(addDirValues, args[i+1])
			}
		}
		require.Equal(t, []string{"/extra1", "/extra2"}, addDirValues)
	})

	t.Run("empty string entries in AdditionalDirs are skipped", func(t *testing.T) {
		opts := pipeline.SpawnAgentOptions{
			Task:           &ent.Task{},
			StageRun:       &ent.StageRun{},
			Prompt:         "p",
			AdditionalDirs: []string{"", "/extra", ""},
		}
		args := pipeline.BuildSpawnArgs(opts)

		var addDirValues []string
		for i, a := range args {
			if a == "--add-dir" && i+1 < len(args) {
				addDirValues = append(addDirValues, args[i+1])
			}
		}
		require.Equal(t, []string{"/extra"}, addDirValues)
	})

	t.Run("--add-dir appears once per dir with correct values", func(t *testing.T) {
		opts := pipeline.SpawnAgentOptions{
			Task:           &ent.Task{},
			StageRun:       &ent.StageRun{},
			Prompt:         "p",
			AdditionalDirs: []string{"/a", "/b", "/c"},
		}
		args := pipeline.BuildSpawnArgs(opts)

		count := 0
		for _, a := range args {
			if a == "--add-dir" {
				count++
			}
		}
		require.Equal(t, 3, count)
	})
}

func TestBuildSpawnEnv_ExpandsTildeInSpawnerEnv(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{},
		StageRun: &ent.StageRun{},
		Prompt:   "p",
		Spawner: &ent.Spawner{
			Command: "claude",
			Env:     map[string]string{"CLAUDE_CONFIG_DIR": "~/.claude-work"},
		},
	}
	env := pipeline.BuildSpawnEnv(opts)
	require.Contains(t, env, "CLAUDE_CONFIG_DIR=/tmp/fakehome/.claude-work")
	require.NotContains(t, env, "CLAUDE_CONFIG_DIR=~/.claude-work")
}

func TestBuildSpawnEnv_DenyListExcludesSecrets(t *testing.T) {
	t.Setenv("DASHBOARD_SECRET_KEY", "master-key-value")
	t.Setenv("DASHBOARD_JWT_SECRET", "super-secret-value")
	t.Setenv("DASHBOARD_AUTH_PLUGIN_SECRET", "auth-plugin-secret-value")
	t.Setenv("DASHBOARD_HOOKS_SECRET", "hook-secret-value")

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t1"},
		StageRun: &ent.StageRun{ID: "r1"},
	}
	env := pipeline.BuildSpawnEnv(opts)

	for _, e := range env {
		require.False(t, strings.HasPrefix(e, "DASHBOARD_SECRET_KEY="),
			"DASHBOARD_SECRET_KEY must not appear in spawn env, got: %s", e)
		require.False(t, strings.HasPrefix(e, "DASHBOARD_JWT_SECRET="),
			"DASHBOARD_JWT_SECRET must not appear in spawn env, got: %s", e)
		require.False(t, strings.HasPrefix(e, "DASHBOARD_AUTH_PLUGIN_SECRET="),
			"DASHBOARD_AUTH_PLUGIN_SECRET must not appear in spawn env, got: %s", e)
		require.False(t, strings.HasPrefix(e, "DASHBOARD_HOOKS_SECRET="),
			"DASHBOARD_HOOKS_SECRET must not appear in spawn env, got: %s", e)
	}
}

// TestBuildSpawnEnv_MCPTokenSurvivesSecretStrip is the CQ-02 required
// assertion: the 4-key canonical deny-set (envsec.DeniedSecretEnvKeys) must
// be stripped from a pipeline agent's env while the per-task
// DASHBOARD_MCP_TOKEN injected at Stage 3 survives the final defense-in-depth
// delete loop — i.e. a live agent can still reach /api/mcp after the strip.
func TestBuildSpawnEnv_MCPTokenSurvivesSecretStrip(t *testing.T) {
	t.Setenv("DASHBOARD_SECRET_KEY", "master-key-value")
	t.Setenv("DASHBOARD_JWT_SECRET", "super-secret-value")
	t.Setenv("DASHBOARD_AUTH_PLUGIN_SECRET", "auth-plugin-secret-value")
	t.Setenv("DASHBOARD_HOOKS_SECRET", "hook-secret-value")
	t.Setenv("DASHBOARD_MCP_TOKEN", "leaked-ambient-token")

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t1"},
		StageRun: &ent.StageRun{ID: "r1"},
		MCPToken: "per-task-mcp-token",
		MCPUrl:   "http://127.0.0.1:13120/api/mcp",
	}
	env := pipeline.BuildSpawnEnv(opts)

	for _, denied := range []string{
		"DASHBOARD_SECRET_KEY",
		"DASHBOARD_JWT_SECRET",
		"DASHBOARD_AUTH_PLUGIN_SECRET",
		"DASHBOARD_HOOKS_SECRET",
	} {
		for _, e := range env {
			require.False(t, strings.HasPrefix(e, denied+"="),
				"%s must not appear in spawn env, got: %s", denied, e)
		}
	}

	// The Stage-3 per-task token must win over — and survive stripping
	// alongside — any ambient DASHBOARD_MCP_TOKEN inherited from os.Environ().
	require.Contains(t, env, "DASHBOARD_MCP_TOKEN=per-task-mcp-token")
	require.Contains(t, env, "DASHBOARD_MCP_URL=http://127.0.0.1:13120/api/mcp")
}

func TestBuildSpawnEnv_ForwardsPath(t *testing.T) {
	t.Setenv("PATH", "/test/path:/usr/bin")

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t2"},
		StageRun: &ent.StageRun{ID: "r2"},
	}
	env := pipeline.BuildSpawnEnv(opts)

	require.Contains(t, env, "PATH=/test/path:/usr/bin")
}

func TestBuildSpawnEnv_InjectsDashboardIdentifiers(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "task-xyz"},
		StageRun: &ent.StageRun{ID: "run-abc"},
		MCPToken: "mcp-tok",
		MCPUrl:   "http://127.0.0.1:13120/api/mcp",
	}
	env := pipeline.BuildSpawnEnv(opts)

	require.Contains(t, env, "DASHBOARD_TASK_ID=task-xyz")
	require.Contains(t, env, "DASHBOARD_STAGE_RUN_ID=run-abc")
	require.Contains(t, env, "DASHBOARD_MCP_TOKEN=mcp-tok")
	require.Contains(t, env, "DASHBOARD_MCP_URL=http://127.0.0.1:13120/api/mcp")
}

func TestBuildSpawnEnv_ArbitraryVarsNotForwarded(t *testing.T) {
	t.Setenv("MY_SECRET_VAR", "leaked")

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t3"},
		StageRun: &ent.StageRun{ID: "r3"},
	}
	env := pipeline.BuildSpawnEnv(opts)

	for _, e := range env {
		require.False(t, strings.HasPrefix(e, "MY_SECRET_VAR="),
			"arbitrary env var must not be forwarded, got: %s", e)
	}
}

// ---------------------------------------------------------------------------
// BuildAllowList — ManualOverride bypass
// ---------------------------------------------------------------------------

func TestBuildAllowList_ManualOverride_BypassesAllowList(t *testing.T) {
	pattern := "chmod +x ./x.sh"
	perms := []*ent.TaskPermission{
		{Tool: "Bash", Pattern: &pattern, Granted: true, ManualOverride: true},
	}
	allow := pipeline.BuildAllowList("manual", perms, false, false, nil)
	require.Contains(t, allow, "Bash(chmod +x ./x.sh)")
}

func TestBuildAllowList_NoManualOverride_StripsBlockedPattern(t *testing.T) {
	pattern := "chmod +x ./x.sh"
	perms := []*ent.TaskPermission{
		{Tool: "Bash", Pattern: &pattern, Granted: true, ManualOverride: false},
	}
	allow := pipeline.BuildAllowList("manual", perms, false, false, nil)
	for _, a := range allow {
		if strings.Contains(a, "chmod") {
			t.Errorf("BuildAllowList without override must strip 'chmod' pattern, got: %s", a)
		}
	}
}

func TestBuildAllowList_ManualOverride_BypassesGitPushGate(t *testing.T) {
	pattern := "git push origin HEAD"
	perms := []*ent.TaskPermission{
		{Tool: "Bash", Pattern: &pattern, Granted: true, ManualOverride: true},
	}
	// allowGitPush=false — override must still pass.
	allow := pipeline.BuildAllowList("manual", perms, false, false, nil)
	require.Contains(t, allow, "Bash(git push origin HEAD)")
}

func TestBuildAllowList_AllowAllAutonomy_ReturnsBlanketBash(t *testing.T) {
	for _, autonomy := range []string{"spec_gated", "full"} {
		allow := pipeline.BuildAllowList(autonomy, nil, false, false, nil)
		require.Contains(t, allow, "Bash", "autonomy=%s must include blanket Bash", autonomy)
		require.Contains(t, allow, "Read", "autonomy=%s must include Read", autonomy)
	}
}

func TestBuildAllowList_ManualAutonomy_PreservesGatedBehaviour(t *testing.T) {
	// manual with no granted perms → only channel tools (if any), no Bash
	allow := pipeline.BuildAllowList("manual", nil, false, false, nil)
	for _, a := range allow {
		require.NotEqual(t, "Bash", a, "manual autonomy with no perms must not include blanket Bash")
	}
}

func TestBuildAllowList_EmptyAutonomy_PreservesGatedBehaviour(t *testing.T) {
	allow := pipeline.BuildAllowList("", nil, false, false, nil)
	for _, a := range allow {
		require.NotEqual(t, "Bash", a, "empty autonomy must not include blanket Bash")
	}
}

// ---------------------------------------------------------------------------
// BuildDenyList — git-push containment on the allow-all path
// ---------------------------------------------------------------------------

func TestBuildDenyList_AllowAll_GitPushDisabled_ReturnsDeny(t *testing.T) {
	for _, autonomy := range []string{"spec_gated", "full"} {
		deny := pipeline.BuildDenyList(autonomy, false)
		require.Contains(t, deny, "Bash(git push:*)",
			"autonomy=%s, allowGitPush=false must include deny entry", autonomy)
	}
}

func TestBuildDenyList_AllowAll_GitPushEnabled_ReturnsNil(t *testing.T) {
	for _, autonomy := range []string{"spec_gated", "full"} {
		deny := pipeline.BuildDenyList(autonomy, true)
		require.Empty(t, deny,
			"autonomy=%s, allowGitPush=true must return no deny entries", autonomy)
	}
}

func TestBuildDenyList_ManualAutonomy_AlwaysNil(t *testing.T) {
	// manual autonomy never triggers the deny-list path (git push is already
	// blocked at the allow-list level via gitPushRE gate).
	deny := pipeline.BuildDenyList("manual", false)
	require.Empty(t, deny, "manual autonomy must return no deny entries")
}

func TestWriteSettingsFile_AllowAll_GitPushDisabled_IncludesDeny(t *testing.T) {
	cwd := t.TempDir()
	path, wrote, isLocal, err := pipeline.ExportedWriteSettingsFile("spec_gated", cwd, nil, false, false)
	require.NoError(t, err)
	require.True(t, wrote)
	require.False(t, isLocal)
	require.NotEmpty(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	perms, _ := parsed["permissions"].(map[string]any)
	deny, _ := perms["deny"].([]any)
	require.Contains(t, deny, "Bash(git push:*)", "settings.json must contain deny entry for git push")
}

func TestWriteSettingsFile_AllowAll_GitPushEnabled_NoDeny(t *testing.T) {
	cwd := t.TempDir()
	path, wrote, _, err := pipeline.ExportedWriteSettingsFile("spec_gated", cwd, nil, false, true)
	require.NoError(t, err)
	require.True(t, wrote)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(data, &parsed))
	perms, _ := parsed["permissions"].(map[string]any)
	deny, _ := perms["deny"].([]any)
	require.Empty(t, deny, "settings.json must NOT contain deny when allowGitPush=true")
}

func TestBuildSpawnEnv_ForwardsDashboardPrefix(t *testing.T) {
	t.Setenv("DASHBOARD_MCP_URL", "http://example.com")
	t.Setenv("DASHBOARD_JWT_SECRET", "x") // deny-list must override prefix match

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t4"},
		StageRun: &ent.StageRun{ID: "r4"},
	}
	env := pipeline.BuildSpawnEnv(opts)

	// DASHBOARD_MCP_URL should be present (set via Setenv, then overridden by injection
	// since opts.MCPUrl is empty — but the env key itself must appear from prefix forwarding
	// or injection; what matters is the deny-listed secret is absent)
	for _, e := range env {
		require.False(t, strings.HasPrefix(e, "DASHBOARD_JWT_SECRET="),
			"DASHBOARD_JWT_SECRET must be blocked even with DASHBOARD_ prefix, got: %s", e)
	}

	// DASHBOARD_MCP_URL from the environment must be forwarded via prefix rule
	require.Contains(t, env, "DASHBOARD_MCP_URL=http://example.com")
}

func TestBuildSpawnEnv_StripsInheritedSessionIdentity(t *testing.T) {
	t.Setenv("CLAUDE_CODE_CHILD_SESSION", "abc")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "def")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_CONFIG_DIR", "/config")

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t5"},
		StageRun: &ent.StageRun{ID: "r5"},
	}
	env := pipeline.BuildSpawnEnv(opts)

	for _, inherited := range []string{"CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ID", "CLAUDECODE"} {
		for _, e := range env {
			require.False(t, strings.HasPrefix(e, inherited+"="),
				"%s must not be inherited from the server's own session, got: %s", inherited, e)
		}
	}
	require.Contains(t, env, "CLAUDE_CONFIG_DIR=/config")
}

func TestBuildSpawnEnv_SpawnerExplicitEnv_KeepsInheritedKeyByOperatorIntent(t *testing.T) {
	t.Setenv("CLAUDE_EFFORT", "ambient-value")

	opts := pipeline.SpawnAgentOptions{
		Task:     &ent.Task{ID: "t6"},
		StageRun: &ent.StageRun{ID: "r6"},
		Spawner:  &ent.Spawner{Env: map[string]string{"CLAUDE_EFFORT": "high"}},
	}
	env := pipeline.BuildSpawnEnv(opts)

	require.Contains(t, env, "CLAUDE_EFFORT=high")
}

// ---------------------------------------------------------------------------
// CQ-06 — writeSettingsFile failure must fail loud under restrictive autonomy
// ---------------------------------------------------------------------------

// brokenSpawnCwd returns a path to a regular file (not a directory), which
// makes writeSettingsFile's os.MkdirAll(".claude") fail deterministically —
// independent of filesystem permissions or CI user privileges.
func brokenSpawnCwd(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
	return path
}

func TestSpawnStageAgent_CQ06_FailsLoudUnderRestrictiveAutonomy(t *testing.T) {
	opts := pipeline.SpawnAgentOptions{
		Task:          &ent.Task{ID: "t-restrictive", Cwd: brokenSpawnCwd(t), Autonomy: "manual"},
		StageRun:      &ent.StageRun{ID: "r-restrictive"},
		EnableChannel: true, // forces a non-empty allow list so writeSettingsFile attempts the write
		Spawner:       &ent.Spawner{Command: "/nonexistent/does-not-exist-xyz"},
	}
	_, err := pipeline.SpawnStageAgent(opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "writeSettingsFile:",
		"restrictive autonomy must fail loud on a writeSettingsFile error instead of spawning with no allow-list")
}

// TestSpawnStageAgent_TaskAPITokenReachesWrittenConfig closes the gap
// buildTaskAPI's own unit tests leave open: they cover the pure decision of
// whether a dashboard-tasks entry gets built, but SpawnStageAgent could still
// silently drop the result on the floor (e.g. reverting to
// WriteTempConfig(selfBin, nil)) without failing any of them. This drives the
// real function end to end with a real (near-instant) child process and reads
// the config file it actually wrote.
func TestSpawnStageAgent_TaskAPITokenReachesWrittenConfig(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "dashboard-"+strconv.Itoa(os.Getuid()))
	before, _ := os.ReadDir(dir) // ignore error — dir may not exist yet
	beforeSet := make(map[string]bool, len(before))
	for _, e := range before {
		beforeSet[e.Name()] = true
	}

	opts := pipeline.SpawnAgentOptions{
		Task:          &ent.Task{ID: "t-reach", Cwd: t.TempDir(), Autonomy: "full"},
		StageRun:      &ent.StageRun{ID: "r-reach"},
		EnableChannel: true,
		TaskAPIToken:  "reachtest-tok",
		MCPUrl:        "http://127.0.0.1:13120",
		Spawner:       &ent.Spawner{Command: "/usr/bin/true"},
	}
	result, err := pipeline.SpawnStageAgent(opts)
	require.NoError(t, err)
	t.Cleanup(result.Cleanup)
	if p, perr := os.FindProcess(result.PID); perr == nil {
		_, _ = p.Wait() // reap the child so it doesn't linger as a zombie
	}

	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	var newFile string
	for _, e := range after {
		if !beforeSet[e.Name()] {
			newFile = filepath.Join(dir, e.Name())
			break
		}
	}
	require.NotEmpty(t, newFile, "SpawnStageAgent must have written a new channel config file")

	data, err := os.ReadFile(newFile)
	require.NoError(t, err)

	var parsed struct {
		MCPServers map[string]struct {
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	tasksEntry, ok := parsed.MCPServers["kontor-tasks"]
	require.True(t, ok, "config must carry a dashboard-tasks entry when TaskAPIToken is set")
	require.Equal(t, "Bearer reachtest-tok", tasksEntry.Headers["Authorization"],
		"the minted token must reach the written config, not just buildTaskAPI's return value")
}

func TestSpawnStageAgent_CQ06_WarnsAndContinuesUnderAllowAllAutonomy(t *testing.T) {
	for _, autonomy := range []string{"spec_gated", "full"} {
		t.Run(autonomy, func(t *testing.T) {
			opts := pipeline.SpawnAgentOptions{
				Task:     &ent.Task{ID: "t-allow-all", Cwd: brokenSpawnCwd(t), Autonomy: autonomy},
				StageRun: &ent.StageRun{ID: "r-allow-all"},
				Spawner:  &ent.Spawner{Command: "/nonexistent/does-not-exist-xyz"},
			}
			_, err := pipeline.SpawnStageAgent(opts)
			// The spawn still fails overall (the spawner binary does not exist), but
			// it must fail at the process-start stage, not at writeSettingsFile — proving
			// the writeSettingsFile error was logged and swallowed, not returned.
			require.Error(t, err)
			require.NotContains(t, err.Error(), "writeSettingsFile:",
				"allow-all autonomy must warn and continue past a writeSettingsFile error")
		})
	}
}

// ---------------------------------------------------------------------------
// --strict-mcp-config and the user-scope server merge
// ---------------------------------------------------------------------------

// spawnRecording runs SpawnStageAgent against a shell script that records its
// own argv, then returns that argv together with the MCP config file the
// spawner wrote for it. Both come from the real spawn, so a change that keeps
// buildSpawnArgsWithChannelConfig correct but stops routing through it still
// shows up here.
func spawnRecording(t *testing.T, userConfigJSON string, opts pipeline.SpawnAgentOptions) ([]string, map[string]json.RawMessage) {
	t.Helper()
	// SpawnStageAgent no longer reads the server definitions itself — the
	// resolver does that upstream and hands the result
	// through opts.Applications.Servers, so the test simulates that hand-off
	// directly instead of writing a real user-scope config file.
	if userConfigJSON != "" {
		var cfg struct {
			MCPServers map[string]json.RawMessage `json:"mcpServers"`
		}
		if err := json.Unmarshal([]byte(userConfigJSON), &cfg); err == nil {
			opts.Applications.Servers = cfg.MCPServers
		}
	}

	scriptDir := t.TempDir()
	argvPath := filepath.Join(scriptDir, "argv.txt")
	script := filepath.Join(scriptDir, "record.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+argvPath+"\n"), 0o700))

	opts.Task = &ent.Task{ID: "t-strict", Cwd: t.TempDir(), Autonomy: "full"}
	opts.StageRun = &ent.StageRun{ID: "r-strict"}
	opts.EnableChannel = true
	opts.Spawner = &ent.Spawner{Command: script}

	result, err := pipeline.SpawnStageAgent(opts)
	require.NoError(t, err)
	t.Cleanup(result.Cleanup)
	if p, perr := os.FindProcess(result.PID); perr == nil {
		_, _ = p.Wait()
	}

	raw, err := os.ReadFile(argvPath)
	require.NoError(t, err, "the spawned process must have recorded its argv")
	argv := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	cfgPath := ""
	for i, a := range argv {
		if a == "--mcp-config" && i+1 < len(argv) {
			cfgPath = argv[i+1]
		}
	}
	require.NotEmpty(t, cfgPath, "spawn argv must carry --mcp-config <path>")

	data, err := os.ReadFile(cfgPath)
	require.NoError(t, err)
	var parsed struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	return argv, parsed.MCPServers
}

func TestSpawnStageAgent_PassesStrictMCPConfig(t *testing.T) {
	argv, _ := spawnRecording(t, "", pipeline.SpawnAgentOptions{})
	require.Contains(t, argv, "--strict-mcp-config",
		"without --strict-mcp-config the written file only adds to the user-scope servers, "+
			"leaving the broad onboarding dashboard-tasks credential reachable")
}

func TestSpawnStageAgent_CarriesUserScopeServersIntoTheSpawnConfig(t *testing.T) {
	_, servers := spawnRecording(t,
		`{"mcpServers":{"context7":{"type":"http","url":"https://ctx7.example/mcp"}}}`,
		pipeline.SpawnAgentOptions{})

	require.Contains(t, servers, "context7",
		"--strict-mcp-config makes this file the agent's whole MCP surface, so the "+
			"operator's own servers must be carried into it")
	require.Contains(t, servers, "kontor-channel")
}

// TestSpawnStageAgent_UserScopeDashboardTasksCannotOverrideTheSpawnCredential
// is the security property of this change: onboarding registers dashboard-tasks
// at user scope with a broad, long-lived key (tasks:write, pipeline:control).
// The spawn's own per-stage-run entry must win under that name, or the agent
// gets back exactly the scopes StageRunScopes leaves out.
func TestSpawnStageAgent_UserScopeDashboardTasksCannotOverrideTheSpawnCredential(t *testing.T) {
	// Both spellings are seeded: the rename must not have opened a door under
	// the old name, which an installation that predates it still carries.
	_, servers := spawnRecording(t,
		`{"mcpServers":{"kontor-tasks":{"type":"http","url":"http://127.0.0.1:13120/api/mcp",`+
			`"headers":{"Authorization":"Bearer onboarding-broad-key"}},`+
			`"dashboard-tasks":{"type":"http","url":"http://127.0.0.1:13120/api/mcp",`+
			`"headers":{"Authorization":"Bearer legacy-broad-key"}},`+
			`"dashboard-channel":{"command":"/somewhere/else","args":["channel"]}}}`,
		pipeline.SpawnAgentOptions{TaskAPIToken: "stagerun-tok", MCPUrl: "http://127.0.0.1:13120"})

	require.Contains(t, string(servers["kontor-tasks"]), "Bearer stagerun-tok",
		"the per-stage-run credential must win")
	require.NotContains(t, string(servers["kontor-tasks"]), "onboarding-broad-key",
		"the broad user-scope credential must never reach a spawned stage agent")
	require.NotContains(t, string(servers["dashboard-tasks"]), "legacy-broad-key",
		"a user-scope entry under the pre-rename name must not reach it either")
	require.NotContains(t, string(servers["kontor-channel"]), "/somewhere/else",
		"the dashboard's own channel bridge entry must win too")
}

func TestSpawnStageAgent_MalformedUserConfigStillSpawns(t *testing.T) {
	_, servers := spawnRecording(t, "{ not json at all", pipeline.SpawnAgentOptions{})
	require.Contains(t, servers, "kontor-channel",
		"a ~/.claude.json the dashboard does not own must never break a spawn")
}
