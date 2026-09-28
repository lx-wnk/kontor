package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/envsec"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/pathutil"
	"github.com/lx-wnk/kontor/server/internal/permissions"
	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
)

var gitPushRE = regexp.MustCompile(`(?i)\bgit push\b`)

// syntheticSpawnPID is the PID returned by syntheticSpawn. No real process
// owns this value, so IsPidAlive is false and syscallKill is a no-op.
const syntheticSpawnPID = 2147483647

// syntheticSpawn is the default SpawnFunc used when no real spawner is wired
// (all tests, the package-level HandlersByStage registry). It returns a
// SpawnResult whose PID no real process owns, so IsPidAlive is false and
// syscallKill is a harmless no-op.
func syntheticSpawn(opts SpawnAgentOptions) (SpawnResult, error) {
	cwd := opts.Task.Cwd
	if opts.Task.WorktreePath != nil && *opts.Task.WorktreePath != "" {
		cwd = *opts.Task.WorktreePath
	}
	return SpawnResult{PID: syntheticSpawnPID, Cwd: cwd, Cleanup: func() {}}, nil
}

const systemPromptMaxChars = 10000

type SpawnAgentOptions struct {
	Task            *ent.Task
	StageRun        *ent.StageRun
	Prompt          string
	SystemPrompt    string
	Model           string
	Permissions     []*ent.TaskPermission
	EnableChannel   bool
	ResumeSessionID string
	MCPToken        string
	MCPUrl          string
	// TaskAPIToken is the stage-run credential the agent presents to the
	// dashboard's own MCP endpoint. Empty means the config gets no such entry
	// and the agent reaches no task API at all — the user-scope registration
	// is shut out of the spawn config either way.
	TaskAPIToken string
	// Spawner is the resolved DB spawner row that controls which executable
	// is launched and which extra env vars are seeded. When nil the spawner
	// behaves identically to the legacy `claude` CLI path. The built-in
	// claude-default spawner (Command="claude", empty Args/Env) is also
	// treated as the legacy path so existing tasks spawn byte-identically.
	Spawner *ent.Spawner

	// AdditionalDirs holds the paths of project folders (beyond the cwd) that
	// should be accessible to the spawned agent. Each non-empty entry is passed
	// to the `claude` CLI as `--add-dir <path>`. The default folder is already
	// the cwd and must NOT appear here.
	AdditionalDirs []string

	// AllowGitPush is the global git-push setting ("git.allowPush"). Combined with
	// the per-task metadata override inside IsGitPushAllowed.
	AllowGitPush bool

	// Effort is the resolved reasoning-effort level (e.g. "low"/"high") for
	// this spawn. Empty means "no --effort flag" — the caller (stage_handlers.go)
	// only populates it for an adapter type that supports it and only with a
	// value the claude CLI actually recognizes (services.IsValidEffortLevel);
	// an unresolved or unrecognized value must reach here as "", never a guess.
	Effort string

	// Applications is the resolved set of MCP applications this run may use:
	// their server configs, which of their tools are allowed or denied, and
	// which tool names exist in their catalogue at all. Zero value means the
	// run gets no MCP applications.
	Applications mcpapps.RunApplications
}

type SpawnResult struct {
	PID          int
	Cwd          string
	SettingsPath string
	Cleanup      func()
}

// channelAllow is prepended to every permission allow-list for a channel-enabled
// spawn. It is derived from the bridge's own tool list rather than written out:
// a tool the bridge registers but this list omits is not permitted, and nothing
// reports it — the agent sees it as unavailable and takes whatever fallback the
// prompt offers. set_stage_output was missing exactly that way.
//
// The list does NOT reach the agent as --allowedTools, despite what an earlier
// version of this comment claimed. writeSettingsFile renders it into
// .claude/settings.json under permissions.allow; the spawn command line carries
// only --mcp-config and --strict-mcp-config. Verified 2026-09-05 by reading the
// command line of a running stage agent.
var channelAllow = channelconfig.AllowListEntries()

// BuildDenyList returns the settings deny entries that must be written
// alongside the allow list. On the allow-all path, a deny for "Bash(git push:*)"
// is injected when allowGitPush is false so Claude refuses git-push even though
// blanket Bash is allowed. Returns nil when no deny entries are needed.
func BuildDenyList(autonomy string, allowGitPush bool) []string {
	if taskcontrol.IsAllowAll(autonomy) && !allowGitPush {
		return []string{"Bash(git push:*)"}
	}
	return nil
}

// capabilityCatalogue holds the CapabilityView seeded for each tool name,
// keyed by name. Set once at boot (di.go) after the catalogue is seeded and
// read back from the database. nil until boot runs — every unit test in this
// package, and any code path that never calls SetCapabilityCatalogue — in
// which case capabilityViewFor falls back to the same defaults the seeder
// assigns (see repo.SeedCapabilities), so behaviour is unaffected until a
// human edits a class through the catalogue.
//
// A package-level catalogue rather than a BuildAllowList parameter is a
// deliberate choice: every caller of BuildAllowList was left untouched when
// this file was rewritten to resolve through capability.Decide, and giving
// this function a live repository handle would make a pure function depend
// on a database connection it does not otherwise need.
var capabilityCatalogue map[string]capability.CapabilityView

// SetCapabilityCatalogue installs the resolved capability catalogue that
// resolvePermissionDecisions consults when translating a TaskPermission into
// a capability.CapabilityView. Call once at boot after the catalogue has
// been seeded and loaded from the database.
func SetCapabilityCatalogue(views map[string]capability.CapabilityView) {
	capabilityCatalogue = views
}

// capabilityViewFor resolves the CapabilityView for tool, preferring the
// booted catalogue and falling back to repo.DefaultCapabilityView — the same
// function repo.SeedCapabilities calls to fill a missing row — when the
// catalogue has no entry yet (e.g. in tests, or before the first boot has
// seeded it). The fallback delegates rather than holding its own copy of the
// class/enforcement literals, so the two paths cannot silently drift apart.
func capabilityViewFor(tool string) capability.CapabilityView {
	if v, ok := capabilityCatalogue[tool]; ok {
		return v
	}
	return repo.DefaultCapabilityView(tool)
}

// permissionGrantContextRef is the synthetic context reference paired with
// every on-the-fly grant built from a TaskPermission row below. BuildAllowList
// has no task ID to thread through capability.Decide, and none is needed: a
// grant built for the duration of one call only has to agree with its own
// request on which context it applies to, not with any real entity.
const permissionGrantContextRef = "task-permission"

// BuildAllowList assembles the permission allow-list for a spawn. It is
// rendered into .claude/settings.json by writeSettingsFile, not passed as
// --allowedTools — the spawn command line carries neither.
// When autonomy is an allow-all level (spec_gated or full), the restrictive
// per-permission loop is skipped and the permissive list is returned instead.
// Otherwise each granted TaskPermission is translated into a capability grant
// and resolved through capability.Decide, then capability.SpawnEnforcer
// renders whatever decided allow.
func BuildAllowList(autonomy string, perms []*ent.TaskPermission, enableChannel, allowGitPush bool, catalogueTools map[string]bool) []string {
	var allow []string
	if enableChannel {
		allow = append(allow, channelAllow...)
	}
	if taskcontrol.IsAllowAll(autonomy) {
		return append(allow, taskcontrol.PermissiveAllowList(allowGitPush)...)
	}
	decisions, entries := resolvePermissionDecisions(perms, allowGitPush, catalogueTools)
	return append(allow, capability.SpawnEnforcer{}.AllowList(decisions, entries)...)
}

// resolvePermissionDecisions translates granted TaskPermission rows into
// capability.Decide requests and resolves each one. It applies the validation
// capability.Decide has no notion of — tool allow-list membership,
// blanket-Bash and bare-WebFetch rejection, git-push containment, Bash
// command safety — exactly as the pre-gate filter chain did, then lets
// Decide resolve context specificity, mode ranking, and grant expiry for
// whatever survives. The returned slices are parallel and preserve the order
// perms was walked in, as capability.SpawnEnforcer.AllowList requires.
func resolvePermissionDecisions(perms []*ent.TaskPermission, allowGitPush bool, catalogueTools map[string]bool) ([]capability.Decision, []capability.AllowEntry) {
	contexts := []capability.Context{{Kind: "task", Ref: permissionGrantContextRef}}

	type survivor struct{ tool, value string }
	grantsByTool := make(map[string][]capability.GrantView)
	var survivors []survivor

	for _, p := range perms {
		if !p.Granted {
			continue // rule 1: not granted
		}
		if !permissions.IsAllowedTool(p.Tool) && !catalogueTools[p.Tool] {
			continue // rule 3: tool not on the allow-list and not in any attached application's catalogue
		}

		var value string
		switch p.Tool {
		case "Bash":
			if p.Pattern == nil || *p.Pattern == "" {
				continue // rule 4: blanket Bash allow is forbidden
			}
			normalized := strings.Join(strings.Fields(*p.Pattern), " ")
			if !p.ManualOverride {
				// Human explicitly approved this exact pattern — skip the git-push and safety gates.
				if !allowGitPush && gitPushRE.MatchString(normalized) {
					continue // rule 5: git push forbidden
				}
				if ok, _ := permissions.IsSafeBashPattern(normalized); !ok {
					continue // rule 6: unsafe shell pattern
				}
			}
			value = normalized
		case "WebFetch":
			// Bare WebFetch grants (no pattern) are rejected — require a domain pattern.
			if p.Pattern == nil || strings.TrimSpace(*p.Pattern) == "" {
				continue // rule 7: bare WebFetch forbidden
			}
			value = strings.TrimSpace(*p.Pattern)
		default:
			if p.Pattern != nil {
				value = *p.Pattern
			}
		}

		grantsByTool[p.Tool] = append(grantsByTool[p.Tool], capability.GrantView{
			ID:          p.ID,
			Capability:  p.Tool,
			ContextKind: "task",
			ContextRef:  permissionGrantContextRef,
			Pattern:     value,
			Mode:        "allow",
			ExpiresAt:   p.ExpiresAt,
		})
		survivors = append(survivors, survivor{tool: p.Tool, value: value})
	}

	decisions := make([]capability.Decision, 0, len(survivors))
	entries := make([]capability.AllowEntry, 0, len(survivors))
	for _, s := range survivors {
		capView := capabilityViewFor(s.tool)
		req := capability.Request{Capability: s.tool, Value: s.value, Contexts: contexts}
		decisions = append(decisions, capability.Decide(req, grantsByTool[s.tool], capView))
		entries = append(entries, capability.AllowEntry{Tool: s.tool, Pattern: s.value})
	}
	return decisions, entries
}

// spawnToolLists renders one spawn's tool decisions in one place, so the
// settings file and the spawn's command-line flags always agree: permission
// grants and channel tools, plus whatever the run's attached MCP applications
// allow or deny on top.
func spawnToolLists(opts SpawnAgentOptions, allowGitPush bool) (allow, deny []string) {
	allow = append(BuildAllowList(opts.Task.Autonomy, opts.Permissions, opts.EnableChannel, allowGitPush, opts.Applications.CatalogueTools), taskAPIAllow(opts)...)
	allow = append(allow, opts.Applications.Allow...)
	deny = append(BuildDenyList(opts.Task.Autonomy, allowGitPush), opts.Applications.Deny...)
	return allow, deny
}

func BuildSpawnArgs(opts SpawnAgentOptions) []string {
	var args []string
	if opts.ResumeSessionID != "" {
		args = append(args, "--resume", opts.ResumeSessionID)
	}
	args = append(args, "-p", opts.Prompt)
	// Only force --permission-mode default when the spawner has not declared its
	// own permission posture. A spawner that passes --dangerously-skip-permissions
	// or its own --permission-mode (e.g. "auto"/"acceptEdits") would otherwise get
	// a SECOND, conflicting --permission-mode default appended — claude then errors
	// on the duplicate flag (or default silently wins), putting the agent back into
	// a gated mode with an (often empty) allow-list where every Edit/Write/Bash fails.
	if !spawnerControlsPermissionMode(opts.Spawner) {
		args = append(args, "--permission-mode", "default")
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.SystemPrompt != "" {
		sp := opts.SystemPrompt
		if len(sp) > systemPromptMaxChars {
			sp = sp[:systemPromptMaxChars]
		}
		args = append(args, "--system-prompt", sp)
	}
	// Apply spawner ModelOverride only when no --model was already injected
	// from opts.Model. The override is meant for spawners that target a
	// fixed model regardless of the task's preferred model.
	if opts.Spawner != nil && opts.Spawner.ModelOverride != nil && *opts.Spawner.ModelOverride != "" {
		if !containsArg(args, "--model") {
			args = append(args, "--model", *opts.Spawner.ModelOverride)
		}
	}
	// Grant the spawned agent access to extra project folders. Each path is
	// passed as a separate --add-dir flag so the claude CLI adds the directory
	// to its allowed file-system scope. Empty entries are skipped defensively.
	for _, dir := range opts.AdditionalDirs {
		if dir == "" {
			continue
		}
		args = append(args, "--add-dir", dir)
	}
	return args
}

// skipPermissionFlags are the claude CLI flags that bypass all permission
// checks. Both spellings are accepted by the CLI.
var skipPermissionFlags = map[string]struct{}{
	"--dangerously-skip-permissions":       {},
	"--allow-dangerously-skip-permissions": {},
}

// spawnerControlsPermissionMode reports whether the resolved spawner already
// declares its own permission posture — either a --dangerously-skip-permissions
// flag (either spelling) or an explicit --permission-mode. When true, the
// dashboard must not append its default --permission-mode, to avoid a duplicate
// / conflicting flag.
func spawnerControlsPermissionMode(sp *ent.Spawner) bool {
	if sp == nil {
		return false
	}
	for _, a := range sp.Args {
		if _, ok := skipPermissionFlags[a]; ok {
			return true
		}
		if a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode=") {
			return true
		}
	}
	return false
}

func containsArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// buildExecCommand returns an *exec.Cmd configured for the resolved spawner.
// Spawner-declared args appear BEFORE dashboard-built args so interpreter
// flags (e.g. `npx` + package name) precede the per-task switches.
func buildExecCommand(sp *ent.Spawner, args []string) *exec.Cmd {
	if isLegacyClaudeSpawner(sp) {
		return exec.Command("claude", args...)
	}
	combined := make([]string, 0, len(sp.Args)+len(args))
	combined = append(combined, sp.Args...)
	combined = append(combined, args...)
	// #nosec G204 -- sp.Command/sp.Args are not re-validated here; writers are the /api/spawners CRUD (ValidateSpawnerCommand allow-list) and the DASHBOARD_SPAWN_COMMAND seed in serverapp/di_seed.go, which skips that check but is reachable only by whoever sets the server environment (already near-RCE), and the CRUD actor is any authenticated caller — under the default auth.mode=none, any local process reaching the loopback port with a matching Origin; task data only reaches argv, which exec.Command passes without a shell.
	return exec.Command(sp.Command, combined...)
}

// isLegacyClaudeSpawner returns true when the spawner is nil or describes the
// built-in `claude` CLI with no custom args. In that case SpawnStageAgent
// behaves byte-identically to the pre-pluggable-spawner path.
func isLegacyClaudeSpawner(sp *ent.Spawner) bool {
	if sp == nil {
		return true
	}
	if (sp.Command == "" || sp.Command == "claude") && len(sp.Args) == 0 {
		return true
	}
	return false
}

// buildSpawnArgsWithChannelConfig assembles the final argv, including the
// permission flags. allow and deny are passed as --allowedTools and
// --disallowedTools because the settings file is not a reliable carrier: Claude
// Code applies permissions.allow from .claude/settings.json only in a workspace
// that has been trusted, and a stage worktree is created fresh, so it never has
// been. The CLI is explicit about it — "Ignoring N permissions.allow entries
// from .claude/settings.json: this workspace has not been trusted" — and
// discards every entry. Command-line flags carry no such condition.
//
// Verified 2026-09-05 in an untrusted directory: a settings file allowing Write
// leaves Write blocked ("Permission not granted, session non-interactive"),
// while --allowedTools Write lets the same call through.
func buildSpawnArgsWithChannelConfig(opts SpawnAgentOptions, channelCfgPath string, allow, deny []string) []string {
	args := BuildSpawnArgs(opts)
	if len(allow) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, allow...)
	}
	if len(deny) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, deny...)
	}
	if channelCfgPath != "" {
		// --strict-mcp-config is what makes the written file the agent's entire
		// MCP surface. Without it the file is merged on top of the user- and
		// project-scope registrations, and the broad `claude mcp add --scope
		// user` dashboard-tasks credential onboarding writes stays reachable —
		// which would give every stage agent back the scopes its own key omits.
		// The user's other servers are carried into the file instead (see
		// SpawnStageAgent); a project-scope .mcp.json is not.
		args = append(args, "--mcp-config", channelCfgPath, "--strict-mcp-config")
	}
	return args
}

// allowedEnvPrefixes are env var prefixes always forwarded to spawned agents.
var allowedEnvPrefixes = []string{"CLAUDE_", "DASHBOARD_", "KONTOR_"}

// deniedEnvKeys are secrets that must never reach spawned agents even if they
// match an allowedEnvPrefixes entry. Canonical set — see envsec.DeniedSecretEnvKeys.
var deniedEnvKeys = envsec.DeniedSecretEnvKeys

// allowedEnvKeys are exact env var names always forwarded to spawned agents.
var allowedEnvKeys = map[string]struct{}{
	"PATH":            {},
	"HOME":            {},
	"USER":            {},
	"LOGNAME":         {},
	"LANG":            {},
	"LC_ALL":          {},
	"LC_CTYPE":        {},
	"TERM":            {},
	"SHELL":           {},
	"TMPDIR":          {},
	"TMP":             {},
	"TEMP":            {},
	"XDG_RUNTIME_DIR": {},
	"XDG_CONFIG_HOME": {},
	"GOPATH":          {},
	"GOROOT":          {},
	"NODE_PATH":       {},
}

// expandLeadingTilde is an alias for pathutil.ExpandLeadingTilde kept for
// package-internal use. Callers outside pipeline should import pathutil directly.
var expandLeadingTilde = pathutil.ExpandLeadingTilde

func BuildSpawnEnv(opts SpawnAgentOptions) []string {
	// Stage 1: spawner-declared env (lowest precedence). Each entry is
	// subject to the global deny list before reaching the merged map.
	merged := make(map[string]string)
	if opts.Spawner != nil {
		for k, v := range opts.Spawner.Env {
			if _, denied := deniedEnvKeys[k]; denied {
				continue
			}
			// Spawner env is user-authored config, not a shell expansion, so a
			// leading `~` is taken literally by exec — `CLAUDE_CONFIG_DIR=~/x`
			// would make claude create a bogus `./~/x` dir under the cwd. Expand
			// it the way a shell would before forwarding.
			merged[k] = expandLeadingTilde(v)
		}
	}

	// Stage 2: inherited process env, filtered by the allow-list/prefix
	// rules. Always wins over a spawner-declared key of the same name.
	for _, e := range os.Environ() {
		key, val, found := strings.Cut(e, "=")
		if !found {
			continue
		}
		if _, denied := deniedEnvKeys[key]; denied {
			continue
		}
		if _, inherited := envsec.InheritedSessionEnvKeys[key]; inherited {
			continue
		}
		if _, ok := allowedEnvKeys[key]; ok {
			merged[key] = val
			continue
		}
		for _, prefix := range allowedEnvPrefixes {
			if strings.HasPrefix(key, prefix) {
				merged[key] = val
				break
			}
		}
	}

	// Stage 3: dashboard-controlled identifiers (highest precedence).
	merged["DASHBOARD_STAGE_RUN_ID"] = opts.StageRun.ID
	merged["DASHBOARD_TASK_ID"] = opts.Task.ID
	if opts.MCPToken != "" {
		merged[channelconfig.EnvMCPToken] = opts.MCPToken
	}
	if opts.MCPUrl != "" {
		merged["DASHBOARD_MCP_URL"] = opts.MCPUrl
	}

	// Final defense-in-depth pass: secrets must never leak even if a future
	// code path puts them into `merged` above. The Stage-1 and Stage-2 loops
	// already filter them, but this guarantees the invariant at the exit.
	// DASHBOARD_MCP_TOKEN is deliberately NOT in deniedEnvKeys — it is
	// injected at Stage 3 above, and this loop must not delete it or the
	// spawned agent's channel bridge loses /api/mcp access. It is one value
	// from config (Options.MCPToken, wired once in serverapp/di_pipeline.go),
	// handed unchanged to every spawn: it identifies the dashboard, not this
	// task, so nothing server-side can attribute an MCP call to the task or
	// the routine that made it.
	for denied := range deniedEnvKeys {
		delete(merged, denied)
	}

	env := make([]string, 0, len(merged))
	for k, v := range merged {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

func writeSettingsFile(cwd string, allow, deny []string) (string, bool, bool, error) {
	if len(allow) == 0 && len(deny) == 0 {
		return "", false, false, nil
	}
	claudeDir := filepath.Join(cwd, ".claude")
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if _, err := os.Stat(settingsPath); os.IsNotExist(err) {
		if err := os.MkdirAll(claudeDir, 0o700); err != nil {
			return "", false, false, fmt.Errorf("writeSettingsFile: mkdir .claude: %w", err)
		}
		permsMap := map[string]any{"allow": allow}
		if len(deny) > 0 {
			permsMap["deny"] = deny
		}
		settings := map[string]any{
			"permissions":       permsMap,
			"_dashboardManaged": true,
		}
		data, _ := json.MarshalIndent(settings, "", "  ")
		if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
			return "", false, false, fmt.Errorf("writeSettingsFile: write: %w", err)
		}
		return settingsPath, true, false, nil
	}
	slog.Warn("settings.json is not dashboard-managed — merging into settings.local.json", "path", settingsPath)
	localPath := filepath.Join(claudeDir, "settings.local.json")
	var existing map[string]any
	if data, err := os.ReadFile(localPath); err == nil {
		_ = json.Unmarshal(data, &existing)
	}
	if existing == nil {
		existing = map[string]any{}
	}
	existingPerms, _ := existing["permissions"].(map[string]any)
	if existingPerms == nil {
		existingPerms = map[string]any{}
	}
	existingAllow, _ := existingPerms["allow"].([]any)
	existingSet := make(map[string]bool, len(existingAllow))
	for _, e := range existingAllow {
		if s, ok := e.(string); ok {
			existingSet[s] = true
		}
	}
	var newEntries []string
	for _, entry := range allow {
		if !existingSet[entry] {
			newEntries = append(newEntries, entry)
		}
	}
	// Union deny entries the same way allow entries are unioned.
	existingDeny, _ := existingPerms["deny"].([]any)
	existingDenySet := make(map[string]bool, len(existingDeny))
	for _, e := range existingDeny {
		if s, ok := e.(string); ok {
			existingDenySet[s] = true
		}
	}
	var newDenyEntries []string
	for _, entry := range deny {
		if !existingDenySet[entry] {
			newDenyEntries = append(newDenyEntries, entry)
		}
	}
	if len(newEntries) == 0 && len(newDenyEntries) == 0 {
		return localPath, false, true, nil
	}
	merged := make([]any, 0, len(existingAllow)+len(newEntries))
	merged = append(merged, existingAllow...)
	for _, e := range newEntries {
		merged = append(merged, e)
	}
	existingPerms["allow"] = merged
	if len(newDenyEntries) > 0 {
		mergedDeny := make([]any, 0, len(existingDeny)+len(newDenyEntries))
		mergedDeny = append(mergedDeny, existingDeny...)
		for _, e := range newDenyEntries {
			mergedDeny = append(mergedDeny, e)
		}
		existingPerms["deny"] = mergedDeny
	}
	existing["permissions"] = existingPerms
	existing["_dashboardManagedAllows"] = newEntries
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		return "", false, false, fmt.Errorf("writeSettingsFile: mkdir .claude (local): %w", err)
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(localPath, data, 0o600); err != nil {
		return "", false, false, fmt.Errorf("writeSettingsFile: write local: %w", err)
	}
	return localPath, true, true, nil
}

func ShouldCleanSettingsFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	managed, _ := parsed["_dashboardManaged"].(bool)
	return managed
}

// stderrLogger forwards lines written to stderr of a spawned agent to slog.Warn.
type stderrLogger struct{ prefix string }

func (l *stderrLogger) Write(b []byte) (int, error) {
	line := strings.TrimRight(string(b), "\n")
	if line != "" {
		slog.Warn("agent stderr", "prefix", l.prefix, "line", line)
	}
	return len(b), nil
}

// buildTaskAPI returns the channelconfig.TaskAPI entry for opts, or nil when
// no credential was minted or no dashboard URL is configured — the written
// config then gets no dashboard-tasks server.
func buildTaskAPI(opts SpawnAgentOptions) *channelconfig.TaskAPI {
	if opts.TaskAPIToken == "" || opts.MCPUrl == "" {
		return nil
	}
	return &channelconfig.TaskAPI{URL: opts.MCPUrl + mcp.EndpointPath, Token: opts.TaskAPIToken}
}

// taskAPIAllow returns the settings allow entries for the dashboard-tasks MCP
// server, or nil when this spawn reaches no such server — the gate is
// buildTaskAPI, so the entries appear exactly when the config entry does.
func taskAPIAllow(opts SpawnAgentOptions) []string {
	if !opts.EnableChannel || buildTaskAPI(opts) == nil {
		return nil
	}
	return mcp.StageRunAllowedTools()
}

// IsGitPushAllowed reports whether the task may run `git push`: true when the
// task opts in via metadata["allowGitPush"], or when the global setting
// (git.allowPush) is enabled.
func IsGitPushAllowed(t *ent.Task, allowGitPushGlobal bool) bool {
	if t.Metadata != nil {
		if v, ok := t.Metadata["allowGitPush"].(bool); ok && v {
			return true
		}
	}
	return allowGitPushGlobal
}

func SpawnStageAgent(opts SpawnAgentOptions) (SpawnResult, error) {
	cwd := opts.Task.Cwd
	if opts.Task.WorktreePath != nil && *opts.Task.WorktreePath != "" {
		cwd = *opts.Task.WorktreePath
	}
	allowGitPush := IsGitPushAllowed(opts.Task, opts.AllowGitPush)
	// Resolved once and used twice: as the spawn's permission flags, which are
	// what actually binds, and as the settings file, which still helps in a
	// workspace the user has trusted and is harmless where it is ignored.
	spawnAllow, spawnDeny := spawnToolLists(opts, allowGitPush)
	settingsPath, wrote, isLocal, err := writeSettingsFile(cwd, spawnAllow, spawnDeny)
	if err != nil {
		if !taskcontrol.IsAllowAll(opts.Task.Autonomy) {
			return SpawnResult{}, fmt.Errorf("writeSettingsFile: %w", err)
		}
		// Allow-all autonomy needs no allow-list file — a write failure here is harmless.
		slog.Warn("writeSettingsFile failed — continuing without pre-approved allow-list", "err", err)
	}

	// Write a temp MCP config file so the spawned agent gets the dashboard-channel MCP server.
	// Failures are non-fatal: the agent runs without the channel bridge rather than refusing to spawn.
	var channelCfgPath string
	if opts.EnableChannel {
		// The spawn runs --strict-mcp-config, so whatever is not in this file is
		// gone for the agent. Carry the operator's own servers over; the
		// resolver already read and merged them.
		userServers := opts.Applications.Servers
		if selfBin, binErr := channelconfig.SelfBinaryPath(); binErr == nil {
			if cfgPath, cfgErr := channelconfig.WriteTempConfig(selfBin, buildTaskAPI(opts), userServers); cfgErr == nil {
				channelCfgPath = cfgPath
			} else {
				slog.Warn("channelconfig: failed to write temp config — agent runs without channel bridge", "err", cfgErr)
			}
		} else {
			slog.Warn("channelconfig: failed to resolve self binary path — agent runs without channel bridge", "err", binErr)
		}
	}

	args := buildSpawnArgsWithChannelConfig(opts, channelCfgPath, spawnAllow, spawnDeny)
	cmd := buildExecCommand(opts.Spawner, args)
	cmd.Dir = cwd
	cmd.Env = BuildSpawnEnv(opts)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = &stderrLogger{prefix: fmt.Sprintf("agent[%s]", opts.Task.Slug)}
	if err := cmd.Start(); err != nil {
		if channelCfgPath != "" {
			_ = os.Remove(channelCfgPath)
		}
		return SpawnResult{}, fmt.Errorf("SpawnStageAgent.Start: %w", err)
	}
	cleanup := func() {
		if channelCfgPath != "" {
			_ = os.Remove(channelCfgPath)
		}
		if !wrote || settingsPath == "" {
			return
		}
		if isLocal {
			cleanupLocalSettingsEntries(settingsPath)
		} else if ShouldCleanSettingsFile(settingsPath) {
			_ = os.Remove(settingsPath)
		}
	}
	return SpawnResult{PID: cmd.Process.Pid, Cwd: cwd, SettingsPath: settingsPath, Cleanup: cleanup}, nil
}

func cleanupLocalSettingsEntries(localPath string) {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return
	}
	managed, _ := parsed["_dashboardManagedAllows"].([]any)
	if len(managed) == 0 {
		return
	}
	managedSet := make(map[string]bool, len(managed))
	for _, e := range managed {
		if s, ok := e.(string); ok {
			managedSet[s] = true
		}
	}
	delete(parsed, "_dashboardManagedAllows")
	if perms, ok := parsed["permissions"].(map[string]any); ok {
		if allow, ok := perms["allow"].([]any); ok {
			var filtered []any
			for _, e := range allow {
				if s, ok := e.(string); !ok || !managedSet[s] {
					filtered = append(filtered, e)
				}
			}
			if len(filtered) == 0 {
				delete(perms, "allow")
			} else {
				perms["allow"] = filtered
			}
			if len(perms) == 0 {
				delete(parsed, "permissions")
			}
		}
	}
	if len(parsed) == 0 {
		_ = os.Remove(localPath)
		return
	}
	out, _ := json.MarshalIndent(parsed, "", "  ")
	_ = os.WriteFile(localPath, out, 0o600)
}
