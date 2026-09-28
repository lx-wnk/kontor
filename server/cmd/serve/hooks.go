package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"strings"

	"github.com/lx-wnk/kontor/server/internal/hookscript"
	"github.com/spf13/cobra"
)

// permissionHookTimeoutSeconds is the per-hook timeout written into settings.
// It must exceed the server's own hold plus the script's curl budget, so the
// side that gives up first is ours. When it does expire, Claude Code falls back
// to asking in the terminal -- it neither allows nor denies on its own.
const permissionHookTimeoutSeconds = 30

// notificationHookTimeoutSeconds bounds the fire-and-forget Notification hook.
// It must stay at or above the script's own curl budget for that path.
const notificationHookTimeoutSeconds = 5

// permissionGatedTools is the PreToolUse matcher. Only tools Claude Code can
// actually prompt about are worth intercepting: the hook fires before the
// permission system runs, so a matcher of "*" makes every Read, Grep and
// TodoWrite call pay a round trip to the dashboard for a decision that was
// never going to be asked for.
const permissionGatedTools = "Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch"

// notificationArg selects the script's fire-and-forget path.
const notificationArg = "notification"

// permissionPromptNotification narrows the Notification hook to the one type the
// bridge acts on. Claude Code filters by matcher, so an idle reminder or an auth
// success no longer spawns a process and a round trip to be discarded.
const permissionPromptNotification = "permission_prompt"

// newHooksCmd builds `agent-dashboard hooks`.
//
// The permission bridge only reaches sessions whose settings name the hook, and
// settings are read at session start. This command writes that entry; sessions
// already running are unaffected and have to be restarted to pick it up.
func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Install or remove the dashboard's Claude Code hooks",
	}
	cmd.AddCommand(newHooksInstallCmd(), newHooksUninstallCmd())
	return cmd
}

func newHooksInstallCmd() *cobra.Command {
	var settingsPath, scriptPath string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register the permission bridge as a PreToolUse and Notification hook",
		Long: "Adds two hook entries to the Claude Code settings file so a permission\n" +
			"prompt can be answered in the dashboard instead of in the session's\n" +
			"terminal. Existing hooks are preserved; running it twice changes nothing.\n\n" +
			"Sessions read settings at start, so restart any session that should use it.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveSettingsPath(settingsPath)
			if err != nil {
				return err
			}
			settings, err := readSettings(path)
			if err != nil {
				return err
			}
			script := scriptPath
			if !dryRun || script == "" {
				// A dry run still needs a path to print, but must not write the
				// script out; fall back to the path it would use.
				if dryRun {
					script = filepath.Join(filepath.Dir(path), hookscript.Dir, hookscript.Name)
				} else if script, err = materialiseHookScript(scriptPath, filepath.Dir(path)); err != nil {
					return err
				}
			}

			outcome, err := applyPermissionHooks(settings, script)
			if err != nil {
				return err
			}
			if outcome == hooksUnchanged {
				fmt.Fprintf(cmd.OutOrStdout(), "already installed: %s\n", path)
				return nil
			}
			if dryRun {
				out, _ := json.MarshalIndent(settings, "", "  ")
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", out)
				return nil
			}
			if err := writeSettings(path, settings); err != nil {
				return err
			}
			verb := "installed"
			if outcome == hooksRepaired {
				verb = "updated"
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s the permission bridge in %s\nscript: %s\nRestart any running session to pick it up.\n",
				verb, path, script)
			return nil
		},
	}
	cmd.Flags().StringVar(&settingsPath, "settings", "", "settings file to edit (default ~/.claude/settings.json)")
	cmd.Flags().StringVar(&scriptPath, "script", "", "use this script instead of the embedded one (development)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the resulting settings instead of writing them")
	return cmd
}

func newHooksUninstallCmd() *cobra.Command {
	var settingsPath string
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the dashboard's permission-bridge hooks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveSettingsPath(settingsPath)
			if err != nil {
				return err
			}
			settings, err := readSettings(path)
			if err != nil {
				return err
			}
			removed, foreign := removePermissionHooks(settings)
			for _, cmdLine := range foreign {
				fmt.Fprintf(cmd.ErrOrStderr(),
					"left in place: %s — it runs the same script from a path this command did not install\n", cmdLine)
			}
			if !removed {
				fmt.Fprintf(cmd.OutOrStdout(), "nothing to remove in %s\n", path)
				return nil
			}
			if err := writeSettings(path, settings); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed the permission bridge from %s\n", path)
			return nil
		},
	}
	cmd.Flags().StringVar(&settingsPath, "settings", "", "settings file to edit (default ~/.claude/settings.json)")
	return cmd
}

func resolveSettingsPath(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	// CLAUDE_CONFIG_DIR moves the whole config tree, settings included, so it
	// wins over the default location.
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, "settings.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// materialiseHookScript writes the embedded script under the Claude config dir
// and returns its path. An explicit override is honoured for development, where
// pointing the hook at a working copy is the point.
func materialiseHookScript(override, configDir string) (string, error) {
	if override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("hook script not found at %s: %w", abs, err)
		}
		return abs, nil
	}
	return hookscript.Install(configDir)
}

func readSettings(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		// Refuse rather than overwrite: this file holds the user's own
		// configuration and a parse failure is not a reason to discard it.
		return nil, fmt.Errorf("%s is not valid JSON — fix or move it first: %w", path, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

// writeSettings replaces the file atomically. This is the user's own
// configuration, often tracked in dotfiles; os.WriteFile truncates in place, so
// a full disk or a signal between truncate and write left a stump behind — the
// exact unparseable file readSettings then refuses to touch.
//
// 0700 on the directory, not 0755: it is the same ~/.claude that holds session
// transcripts and the hooks secret, and the secret store already creates it 0700.
// Whichever command ran first would otherwise decide the mode.
func writeSettings(path string, settings map[string]any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".settings-*.json")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	// Explicit chmod: os.CreateTemp makes the file 0600, but an existing target
	// keeps its own mode across a rename, so tighten the replacement itself.
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// hookMarker is what makes an entry recognisably about the permission bridge.
// It is not by itself proof of ownership: a hand-written entry pointing at
// somebody's own fork of the script carries it too, and treating that as ours
// meant uninstall deleted it and install reported "already installed" for a
// script this command never wrote.
var hookMarkers = []string{hookscript.Name, hookscript.LegacyName}

// ownedDir is the path fragment this command does own. Everything it installs
// lives in the directory it creates next to the settings file, so a stale entry
// from an older install is still recognisable there and can be repaired, while
// a marker match anywhere else is somebody else's file.
var ownedDirs = []string{
	hookscript.Dir + string(os.PathSeparator) + hookscript.Name,
	hookscript.LegacyDir + string(os.PathSeparator) + hookscript.LegacyName,
}

type hooksOutcome int

const (
	hooksUnchanged hooksOutcome = iota
	hooksInstalled
	hooksRepaired
)

// applyPermissionHooks adds or repairs the two entries and reports what it did.
// Repair matters because the command written into settings is an absolute path:
// after a binary upgrade or a moved checkout it points at nothing, and Claude
// Code then reports a non-blocking hook failure on every tool call while the CLI
// happily said "already installed".
func applyPermissionHooks(settings map[string]any, script string) (hooksOutcome, error) {
	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, event := range []string{"PreToolUse", "Notification"} {
		if v, present := hooks[event]; present {
			if _, isArray := v.([]any); !isArray {
				// Refuse rather than overwrite: this is a shape the command did
				// not write, and readSettings already declines to discard what
				// it cannot parse for the same reason.
				return hooksUnchanged, fmt.Errorf("hooks.%s is not a list — fix or remove it first", event)
			}
		}
	}
	pre, err := upsertHookEntry(hooks, "PreToolUse", script, map[string]any{
		"matcher": permissionGatedTools,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": script,
			"timeout": permissionHookTimeoutSeconds,
		}},
	})
	if err != nil {
		return hooksUnchanged, err
	}
	// Not folded into one expression: both entries must be attempted, and || or
	// && would skip the second whenever the first already decided the outcome.
	notify, err := upsertHookEntry(hooks, "Notification", script+" "+notificationArg, map[string]any{
		"matcher": permissionPromptNotification,
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": script + " " + notificationArg,
			"timeout": notificationHookTimeoutSeconds,
		}},
	})
	if err != nil {
		return hooksUnchanged, err
	}

	outcome := hooksUnchanged
	for _, o := range []hooksOutcome{pre, notify} {
		if o == hooksRepaired {
			outcome = hooksRepaired
		} else if o == hooksInstalled && outcome != hooksRepaired {
			outcome = hooksInstalled
		}
	}
	if outcome != hooksUnchanged {
		settings["hooks"] = hooks
	}
	return outcome, nil
}

// upsertHookEntry adds the entry, or replaces an existing one of ours whose
// command has drifted. wantCommand is the exact command line this install would
// write, so an entry that already matches it is left untouched.
//
// A foreign entry running the same script from elsewhere stops the install: two
// registrations would both fire on every tool call, and silently replacing one
// the user wrote by hand is not this command's decision to make.
func upsertHookEntry(hooks map[string]any, event, wantCommand string, entry map[string]any) (hooksOutcome, error) {
	existing, _ := hooks[event].([]any)
	for i, e := range existing {
		cmd, ours, foreign := entryCommand(e, wantCommand)
		if foreign {
			return hooksUnchanged, fmt.Errorf(
				"hooks.%s already runs %s, which this command did not install — remove it first, or re-run with --script %s",
				event, cmd, cmd)
		}
		if !ours {
			continue
		}
		if cmd == wantCommand {
			return hooksUnchanged, nil
		}
		existing[i] = entry
		hooks[event] = existing
		return hooksRepaired, nil
	}
	hooks[event] = append(existing, entry)
	return hooksInstalled, nil
}

// removePermissionHooks deletes the entries this command installed and returns
// the commands of any it recognised but does not own, which it leaves in place.
func removePermissionHooks(settings map[string]any) (changed bool, foreign []string) {

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		return false, nil
	}
	for _, event := range []string{"PreToolUse", "Notification"} {
		raw, isArray := hooks[event].([]any)
		if !isArray {
			// Some other shape entirely: not something this command wrote, and
			// not something it may discard. readSettings refuses to overwrite a
			// file it cannot parse for the same reason.
			continue
		}
		kept := make([]any, 0, len(raw))
		for _, e := range raw {
			// No wantCommand here: uninstall has no install to compare against,
			// so only the directory this command owns identifies an entry.
			cmd, ours, isForeign := entryCommand(e, "")
			if ours {
				changed = true
				continue
			}
			if isForeign {
				foreign = append(foreign, cmd)
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(settings, "hooks")
	}
	return changed, foreign
}

// entryCommand reports an entry's command line when it is about the permission
// bridge at all, and whether this command owns it.
//
// Ownership is the whole point of the split: only what this command installed
// may be rewritten or deleted. A marker match that is not ours is a foreign
// script the user registered by hand, and both callers surface it instead of
// touching it.
func entryCommand(entry any, want string) (cmd string, ours, foreign bool) {
	m, ok := entry.(map[string]any)
	if !ok {
		return "", false, false
	}
	inner, _ := m["hooks"].([]any)
	for _, h := range inner {
		hm, ok := h.(map[string]any)
		if !ok {
			continue
		}
		c, _ := hm["command"].(string)
		if c == "" || !containsAny(c, hookMarkers) {
			continue
		}
		// want is the exact command being installed, which for a --script
		// override is the only thing identifying it.
		if c == want || containsAny(c, ownedDirs) {
			return c, true, false
		}
		return c, false, true
	}
	return "", false, false
}

// containsAny reports whether s contains any of the fragments. Both the marker
// and the owned directory exist in a current and a pre-rename spelling, and an
// entry written by either is this command's to repair.
func containsAny(s string, fragments []string) bool {
	for _, f := range fragments {
		if strings.Contains(s, f) {
			return true
		}
	}
	return false
}
