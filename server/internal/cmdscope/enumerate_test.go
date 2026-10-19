package cmdscope

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeFile writes content to path, creating parent dirs.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func names(cmds []SlashCommand) map[string]string {
	m := make(map[string]string, len(cmds))
	for _, c := range cmds {
		m[c.Name] = c.Source
	}
	return m
}

func TestCommands_AllLayers(t *testing.T) {
	cfg := t.TempDir()
	cwd := t.TempDir()

	// user command
	writeFile(t, filepath.Join(cfg, "commands", "deploy.md"), "---\ndescription: Deploy app\n---\nbody")
	// project command
	writeFile(t, filepath.Join(cwd, ".claude", "commands", "lint.md"), "no frontmatter")
	// plugin command: cache/<marketplace>/<plugin>/<version>/commands/<name>.md
	writeFile(t, filepath.Join(cfg, "plugins", "cache", "market", "acme-plugin", "v1", "commands", "scan.md"), "---\ndescription: Scan\n---\n")

	scope := Scope{ConfigDir: cfg, ProjectCwd: cwd, Command: "claude", Supported: true}
	got := scope.Commands()
	byName := names(got)

	// builtins present
	require.Equal(t, "builtin", byName["/clear"])
	require.Equal(t, "builtin", byName["/help"])
	// layered sources
	require.Equal(t, "user", byName["/deploy"])
	require.Equal(t, "project", byName["/lint"])
	// plugin command name stays bare; the plugin is recorded in Source
	require.Equal(t, "plugin:acme-plugin", byName["/scan"])

	// descriptions parsed from frontmatter
	for _, c := range got {
		if c.Name == "/deploy" {
			require.Equal(t, "Deploy app", c.Description)
		}
	}

	// builtins sort first
	require.Equal(t, "builtin", got[0].Source)
}

func TestCommands_DedupPrecedence(t *testing.T) {
	cfg := t.TempDir()
	cwd := t.TempDir()

	// same bare /build at user, project, and plugin layers — names are bare so
	// all three collide and dedup to the highest-precedence layer (project).
	writeFile(t, filepath.Join(cfg, "commands", "build.md"), "---\ndescription: user build\n---")
	writeFile(t, filepath.Join(cwd, ".claude", "commands", "build.md"), "---\ndescription: project build\n---")
	writeFile(t, filepath.Join(cfg, "plugins", "cache", "market", "p", "commands", "build.md"), "---\ndescription: plugin build\n---")
	// also collide with a builtin name — builtin must win
	writeFile(t, filepath.Join(cfg, "commands", "help.md"), "---\ndescription: hijacked\n---")

	scope := Scope{ConfigDir: cfg, ProjectCwd: cwd, Command: "claude", Supported: true}
	got := scope.Commands()

	var build, help SlashCommand
	count := map[string]int{}
	for _, c := range got {
		count[c.Name]++
		if c.Name == "/build" {
			build = c
		}
		if c.Name == "/help" {
			help = c
		}
	}

	require.Equal(t, 1, count["/build"], "bare /build must be deduped across all layers")
	require.Equal(t, "project", build.Source, "project layer shadows user and plugin")
	require.Equal(t, "project build", build.Description)

	require.Equal(t, 1, count["/help"], "help must be deduped")
	require.Equal(t, "builtin", help.Source, "builtin can never be overridden")
}

func TestSkills_AllLayers(t *testing.T) {
	cfg := t.TempDir()
	cwd := t.TempDir()

	writeFile(t, filepath.Join(cfg, "skills", "alpha", "SKILL.md"), "---\nname: alpha\ndescription: Alpha skill\n---")
	writeFile(t, filepath.Join(cwd, ".claude", "skills", "beta", "SKILL.md"), "---\nname: beta\ndescription: Beta\n---")
	// plugin skill: cache/<marketplace>/<plugin>/<version>/skills/<name>/SKILL.md
	writeFile(t, filepath.Join(cfg, "plugins", "cache", "market", "myplug", "1.0", "skills", "gamma", "SKILL.md"), "---\nname: gamma\ndescription: Gamma\n---")

	scope := Scope{ConfigDir: cfg, ProjectCwd: cwd, Command: "claude", Supported: true}
	got := scope.Skills()

	src := map[string]string{}
	for _, s := range got {
		src[s.Name] = s.Source
	}
	require.Equal(t, "user", src["alpha"])
	require.Equal(t, "project", src["beta"])
	// plugin skill name stays bare; the plugin is recorded in Source
	require.Equal(t, "plugin:myplug", src["gamma"])
}

func TestSlashCommands_IncludesSkillsAsCommands(t *testing.T) {
	cfg := t.TempDir()
	cwd := t.TempDir()
	// a user skill, a plugin skill, and a real command file
	writeFile(t, filepath.Join(cfg, "skills", "vue", "SKILL.md"), "---\nname: vue\ndescription: Vue helper\n---")
	writeFile(t, filepath.Join(cfg, "plugins", "cache", "market", "superpowers", "5.1", "skills", "brainstorming", "SKILL.md"), "---\nname: brainstorming\ndescription: BS\n---")
	writeFile(t, filepath.Join(cwd, ".claude", "commands", "ship.md"), "---\ndescription: Ship\n---")

	scope := Scope{ConfigDir: cfg, ProjectCwd: cwd, Command: "claude", Supported: true}
	byName := names(scope.SlashCommands())

	require.Equal(t, "builtin", byName["/help"], "builtins still present")
	require.Equal(t, "project", byName["/ship"], "command files present")
	require.Equal(t, "user", byName["/vue"], "user skill exposed as /vue")
	require.Equal(t, "plugin:superpowers", byName["/brainstorming"], "plugin skill exposed bare with plugin source")

	// Commands() (config tab) must NOT include skills
	require.NotContains(t, names(scope.Commands()), "/vue")
}

func TestSkills_FallbackNameFromDir(t *testing.T) {
	cfg := t.TempDir()
	// no name: in frontmatter → fall back to dir name
	writeFile(t, filepath.Join(cfg, "skills", "noname", "SKILL.md"), "---\ndescription: x\n---")
	scope := Scope{ConfigDir: cfg, Command: "claude", Supported: true}
	got := scope.Skills()
	require.Len(t, got, 1)
	require.Equal(t, "noname", got[0].Name)
}

func TestCommands_EmptyConfigDirNoPanic(t *testing.T) {
	scope := Scope{ConfigDir: filepath.Join(t.TempDir(), "missing"), Command: "claude", Supported: true}
	got := scope.Commands()
	// only builtins
	require.Len(t, got, len(builtinCommands))
}

func TestCommands_SymlinkRejected(t *testing.T) {
	cfg := t.TempDir()
	target := filepath.Join(t.TempDir(), "evil.md")
	writeFile(t, target, "---\ndescription: evil\n---")
	cmdsDir := filepath.Join(cfg, "commands")
	require.NoError(t, os.MkdirAll(cmdsDir, 0o755))
	if err := os.Symlink(target, filepath.Join(cmdsDir, "evil.md")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	scope := Scope{ConfigDir: cfg, Command: "claude", Supported: true}
	for _, c := range scope.Commands() {
		require.NotEqual(t, "/evil", c.Name, "symlinked command file must be rejected")
	}
}

func TestCommands_InteractiveBuiltinsReachTheClient(t *testing.T) {
	scope := Scope{ConfigDir: filepath.Join(t.TempDir(), "missing"), Command: "claude", Supported: true}
	interactive := map[string]bool{}
	for _, c := range scope.Commands() {
		interactive[c.Name] = c.Interactive
	}
	require.True(t, interactive["/login"], "/login opens a picker")
	require.True(t, interactive["/model"], "/model opens a picker")
	require.False(t, interactive["/compact"], "/compact writes to the transcript")
}
