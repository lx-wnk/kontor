package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/hookscript"
)

const testScript = "/opt/dash/kontor-hooks/kontor-permission.sh"

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestApplyPermissionHooksIsIdempotent(t *testing.T) {
	settings := map[string]any{}

	if got, err := applyPermissionHooks(settings, testScript); err != nil || got != hooksInstalled {
		t.Fatalf("first install = (%v, %v), want (installed, nil)", got, err)
	}
	if got, err := applyPermissionHooks(settings, testScript); err != nil || got != hooksUnchanged {
		t.Fatalf("second install = (%v, %v), want (unchanged, nil)", got, err)
	}

	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"PreToolUse", "Notification"} {
		entries, _ := hooks[event].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s has %d entries, want exactly 1", event, len(entries))
		}
	}
}

// The matcher must not be "*". PreToolUse fires before Claude Code evaluates
// whether to prompt, so every Read and Grep would otherwise pay a round trip to
// the dashboard for a decision that was never going to be asked for.
func TestPreToolUseMatcherIsNarrowedToPromptingTools(t *testing.T) {
	settings := map[string]any{}
	mustApply(t, settings, testScript)

	entry := settings["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)
	matcher, _ := entry["matcher"].(string)
	if matcher == "*" || matcher == "" {
		t.Fatalf("matcher = %q, want the prompting-tool set", matcher)
	}
	for _, tool := range []string{"Bash", "Write", "Edit", "WebFetch"} {
		if !strings.Contains(matcher, tool) {
			t.Errorf("matcher %q does not cover %s", matcher, tool)
		}
	}
	if strings.Contains(matcher, "Read") || strings.Contains(matcher, "Grep") {
		t.Errorf("matcher %q covers a tool that never prompts", matcher)
	}
}

// The Notification hook filters by type in the settings file, so an idle
// reminder or an auth success no longer spawns a process to be discarded.
func TestNotificationHookIsFilteredByType(t *testing.T) {
	settings := map[string]any{}
	mustApply(t, settings, testScript)

	entry := settings["hooks"].(map[string]any)["Notification"].([]any)[0].(map[string]any)
	if matcher, _ := entry["matcher"].(string); matcher != "permission_prompt" {
		t.Fatalf("Notification matcher = %q, want permission_prompt", matcher)
	}
}

// The settings file is the user's own; an install must never drop what is
// already in it.
func TestApplyPermissionHooksKeepsForeignHooks(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": "/usr/local/bin/my-own-guard"}},
			}},
		},
		"permissions": map[string]any{"defaultMode": "auto"},
	}

	mustApply(t, settings, testScript)

	hooks := settings["hooks"].(map[string]any)
	entries := hooks["PreToolUse"].([]any)
	if len(entries) != 2 {
		t.Fatalf("PreToolUse has %d entries, want the existing one plus ours", len(entries))
	}
	if settings["permissions"] == nil {
		t.Fatal("an unrelated settings key was dropped")
	}
}

// The command written into settings is an absolute path. After a binary upgrade
// or a moved checkout it points at nothing, and Claude Code then reports a
// non-blocking hook failure on every tool call — while a basename-matching
// install cheerfully said "already installed".
func TestApplyPermissionHooksRepairsAStalePath(t *testing.T) {
	settings := map[string]any{}
	mustApply(t, settings, "/old/location/dashboard-hooks/dashboard-permission.sh")

	got, err := applyPermissionHooks(settings, testScript)
	if err != nil || got != hooksRepaired {
		t.Fatalf("re-install after a move = (%v, %v), want (repaired, nil)", got, err)
	}

	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"PreToolUse", "Notification"} {
		entries := hooks[event].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s has %d entries — repair appended instead of replacing", event, len(entries))
		}
		cmd, ours, _ := entryCommand(entries[0], testScript)
		if !ours || !strings.HasPrefix(cmd, testScript) {
			t.Fatalf("%s still points at %q", event, cmd)
		}
	}
}

func TestRemovePermissionHooksLeavesForeignHooks(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": "/usr/local/bin/my-own-guard"}},
			}},
		},
	}
	mustApply(t, settings, testScript)

	if removed, _ := removePermissionHooks(settings); !removed {
		t.Fatal("uninstall reported no change")
	}
	hooks := settings["hooks"].(map[string]any)
	entries := hooks["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("PreToolUse has %d entries, want only the foreign one", len(entries))
	}
	if _, ok := hooks["Notification"]; ok {
		t.Fatal("an emptied event key was left behind")
	}
}

// A hooks.<event> value that is not an array is something this command did not
// write and may not discard — readSettings refuses to overwrite an unparseable
// file for the same reason.
func TestApplyPermissionHooksRefusesANonArrayValue(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"Notification": map[string]any{"handwritten": true},
		},
	}

	if _, err := applyPermissionHooks(settings, testScript); err == nil {
		t.Fatal("install overwrote a shape it did not write")
	}
	hooks := settings["hooks"].(map[string]any)
	if _, ok := hooks["Notification"].(map[string]any); !ok {
		t.Fatalf("Notification = %#v, want the hand-written object untouched", hooks["Notification"])
	}
}

func TestRemovePermissionHooksLeavesANonArrayValueAlone(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"Notification": map[string]any{"handwritten": true},
			"PreToolUse": []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": testScript}},
			}},
		},
	}

	removePermissionHooks(settings)

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		t.Fatal("the whole hooks key was deleted")
	}
	if _, ok := hooks["Notification"].(map[string]any); !ok {
		t.Fatalf("Notification = %#v, want the hand-written object untouched", hooks["Notification"])
	}
}

func mustApply(t *testing.T, settings map[string]any, script string) {
	t.Helper()
	if _, err := applyPermissionHooks(settings, script); err != nil {
		t.Fatalf("applyPermissionHooks: %v", err)
	}
}

func TestRemovePermissionHooksOnCleanSettings(t *testing.T) {
	settings := map[string]any{}
	if removed, _ := removePermissionHooks(settings); removed {
		t.Fatal("uninstall reported a change with nothing installed")
	}
}

// A settings file that does not parse must abort the command: overwriting it
// would discard configuration the user wrote by hand.
func TestReadSettingsRefusesInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := readSettings(path); err == nil {
		t.Fatal("readSettings accepted a malformed file")
	}
}

func TestReadSettingsTreatsAMissingFileAsEmpty(t *testing.T) {
	settings, err := readSettings(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("readSettings: %v", err)
	}
	if len(settings) != 0 {
		t.Fatalf("settings = %v, want empty", settings)
	}
}

// os.WriteFile's perm applies only on create, so the previous version of this
// test proved nothing about the real target: an existing settings.json, often
// 0644. Pre-create it loose and assert the write tightens it.
func TestWriteSettingsTightensAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("pre-create: %v", err)
	}

	if err := writeSettings(path, map[string]any{"a": 1}); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600 — the file sits next to the hooks secret", perm)
	}
}

// A failed write must leave the original intact rather than a stump.
func TestWriteSettingsLeavesTheOriginalOnFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := []byte(`{"permissions":{"defaultMode":"auto"}}` + "\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("pre-create: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := writeSettings(path, map[string]any{"a": 1}); err == nil {
		t.Fatal("writeSettings succeeded into a read-only directory")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(original) {
		t.Fatalf("the original was damaged: %q", got)
	}
}

func TestWriteSettingsCreatesTheDirectoryOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "claude", "settings.json")
	if err := writeSettings(path, map[string]any{"a": 1}); err != nil {
		t.Fatalf("writeSettings: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("dir mode = %o, want 700 — it holds transcripts and the hooks secret", perm)
	}
}

// CLAUDE_CONFIG_DIR relocates the whole config tree, so it has to win over the
// default path or the hook lands in a settings file nothing reads.
func TestResolveSettingsPathHonoursConfigDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	got, err := resolveSettingsPath("")
	if err != nil {
		t.Fatalf("resolveSettingsPath: %v", err)
	}
	if want := filepath.Join(dir, "settings.json"); got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestResolveSettingsPathPrefersAnExplicitOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	if got, _ := resolveSettingsPath("/tmp/explicit.json"); got != "/tmp/explicit.json" {
		t.Fatalf("path = %q, want the override", got)
	}
}

// The script must land on the user's machine from the binary alone: the release
// archive never carried it, so every install path except a git checkout failed.
func TestMaterialiseHookScriptWritesTheEmbeddedScript(t *testing.T) {
	dir := t.TempDir()

	path, err := materialiseHookScript("", dir)
	if err != nil {
		t.Fatalf("materialiseHookScript: %v", err)
	}
	if want := filepath.Join(dir, hookscript.Dir, hookscript.Name); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("mode = %o, want 700 — it runs as the user on every gated tool call", perm)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(body), "api/hooks/permission") {
		t.Fatalf("the written file is not the bridge script:\n%s", body)
	}
}

// An upgraded binary must replace a script an older one wrote.
func TestMaterialiseHookScriptOverwrites(t *testing.T) {
	dir := t.TempDir()
	path, err := materialiseHookScript("", dir)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# stale\n"), 0o700); err != nil {
		t.Fatalf("stale write: %v", err)
	}

	if _, err := materialiseHookScript("", dir); err != nil {
		t.Fatalf("second install: %v", err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "# stale") {
		t.Fatal("a stale script survived a re-install")
	}
}

// Round-trip through the real file so the two commands agree on the format.
func TestInstallThenUninstallRestoresTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := map[string]any{"permissions": map[string]any{"defaultMode": "auto"}}
	writeJSON(t, path, original)

	settings, err := readSettings(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	mustApply(t, settings, testScript)
	if err := writeSettings(path, settings); err != nil {
		t.Fatalf("write: %v", err)
	}

	settings, err = readSettings(path)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	removePermissionHooks(settings)

	if _, ok := settings["hooks"]; ok {
		t.Fatalf("hooks key survived the uninstall: %v", settings["hooks"])
	}
	perms, _ := settings["permissions"].(map[string]any)
	if perms["defaultMode"] != "auto" {
		t.Fatalf("settings = %v, want the original content back", settings)
	}
}

// A hand-written entry running somebody's own copy of the script carries the
// same filename. Deleting it was the old behaviour, and it is routine in a repo
// that uses worktrees: the user's fork simply vanished on uninstall.
func TestUninstallLeavesAForeignCopyOfTheScript(t *testing.T) {
	foreignCmd := "/home/me/forks/dashboard-permission.sh"
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": foreignCmd}},
			}},
		},
	}

	removed, foreign := removePermissionHooks(settings)

	if removed {
		t.Fatal("uninstall claimed to remove an entry this command never installed")
	}
	if len(foreign) != 1 || foreign[0] != foreignCmd {
		t.Fatalf("foreign = %v, want the entry named so the user can act on it", foreign)
	}
	entries := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("PreToolUse has %d entries, want the foreign one still there", len(entries))
	}
}

// Two registrations would both fire on every tool call, and silently replacing
// the user's is not this command's decision.
func TestInstallRefusesWhenAForeignCopyIsRegistered(t *testing.T) {
	settings := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{map[string]any{
				"matcher": "Bash",
				"hooks":   []any{map[string]any{"type": "command", "command": "/home/me/forks/dashboard-permission.sh"}},
			}},
		},
	}

	if _, err := applyPermissionHooks(settings, testScript); err == nil {
		t.Fatal("install silently replaced an entry pointing at somebody else's script")
	}
	entries := settings["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("PreToolUse has %d entries, want the foreign one untouched", len(entries))
	}
}

// An entry left by an older install still lives in the directory this command
// owns, so it stays repairable rather than being reported as somebody else's.
func TestAStaleEntryInTheOwnedDirectoryIsStillOurs(t *testing.T) {
	settings := map[string]any{}
	mustApply(t, settings, "/old/location/dashboard-hooks/dashboard-permission.sh")

	if _, err := applyPermissionHooks(settings, testScript); err != nil {
		t.Fatalf("re-install after a move: %v", err)
	}
	removed, foreign := removePermissionHooks(settings)
	if !removed || len(foreign) != 0 {
		t.Fatalf("uninstall = (%v, %v), want the repaired entry removed and nothing reported", removed, foreign)
	}
}

// An installation that ran a pre-rename binary has an entry pointing at the old
// directory and script name. The installer has to recognise it as its own and
// replace it; a matcher that only knows the current name appends a second entry
// and the permission hook then fires twice on every gated tool call.
func TestApplyPermissionHooksReplacesAPreRenameEntry(t *testing.T) {
	settings := map[string]any{}
	mustApply(t, settings, "/opt/dash/dashboard-hooks/dashboard-permission.sh")

	got, err := applyPermissionHooks(settings, testScript)
	if err != nil || got != hooksRepaired {
		t.Fatalf("install over a pre-rename entry = (%v, %v), want (repaired, nil)", got, err)
	}

	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"PreToolUse", "Notification"} {
		entries := hooks[event].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s has %d entries — the pre-rename entry was not replaced", event, len(entries))
		}
		cmd, ours, _ := entryCommand(entries[0], testScript)
		if !ours || !strings.HasPrefix(cmd, testScript) {
			t.Fatalf("%s points at %q, want the renamed script", event, cmd)
		}
	}
}
