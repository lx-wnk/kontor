package cmdscope

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lx-wnk/kontor/server/internal/sanitize"
)

// maxArgumentHintRunes caps the argument template a command or skill file may
// contribute. Real hints are a handful of bracketed tokens; the cap exists
// because the file may come from an installed plugin, and the menu renders the
// hint in the same style as the dashboard's own usage templates.
const maxArgumentHintRunes = 120

// sanitizeArgumentHint normalises a frontmatter-supplied argument hint for
// display. Command and skill files are read from the plugin cache as well as
// from user and project directories, so this is where third-party content
// crosses into the API. sanitize.ForDisplayCapped does not check UTF-8
// validity — invalid bytes decode to U+FFFD and would otherwise be kept as
// ordinary runes — so an explicit ValidString guard drops such a hint
// entirely, matching the old behaviour. maxArgumentHintRunes bounds what the
// menu renders, ellipsis included, so a truncated hint gives back one rune
// to make room for it — kept outside the sanitized text at the call site,
// per ForDisplayCapped's contract: a marker inside the text is one the text
// can forge.
func sanitizeArgumentHint(v string) string {
	if !utf8.ValidString(v) {
		return ""
	}
	cleaned, dropped := sanitize.ForDisplayCapped(v, maxArgumentHintRunes)
	if dropped > 0 {
		runes := []rune(cleaned)
		// ForDisplayCapped can report dropped > 0 while keeping fewer than
		// maxArgumentHintRunes-1 runes (e.g. a mostly-bidi-override input),
		// so the slice bound is the shorter of the two.
		limit := min(len(runes), maxArgumentHintRunes-1)
		cleaned = string(runes[:limit]) + "…"
	}
	return cleaned
}

// SlashCommand is one slash command available within a Scope.
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "builtin" | "user" | "project" | "plugin:<plugin-id>"
	// ArgumentHint is the command's `argument-hint:` frontmatter value, e.g.
	// "[base-branch] [--apply-fixes]" — the argument template Claude's own slash
	// menu shows. Empty for built-ins, which have no file to read it from.
	ArgumentHint string `json:"argumentHint,omitempty"`
}

// CommandDetail is a slash command with its on-disk body, for the Config
// explorer. Built-in commands carry an empty Body and Path.
type CommandDetail struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	// ArgumentHint travels through this struct because SlashCommands() builds
	// from it, and is emitted on GET /api/config/commands for API consumers.
	// The Config explorer does not render it — nothing in the dashboard UI
	// reads it from here.
	ArgumentHint string `json:"argumentHint,omitempty"`
	Body         string `json:"body"`
	// Path is the absolute on-disk path of the command file. Empty for builtins.
	Path string `json:"path,omitempty"`
	// Editable is true only for user/project sources (see IsEditableSource).
	Editable bool `json:"editable"`
}

// SkillEntry is one installed skill available within a Scope.
type SkillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // "user" | "project" | "plugin:<plugin-id>"
	// ArgumentHint mirrors SlashCommand.ArgumentHint — every skill is typeable
	// as /<name>, so it carries the same argument template. Emitted on
	// GET /api/config/skills for API consumers; the Config explorer does not
	// render it.
	ArgumentHint string `json:"argumentHint,omitempty"`
	// Path is the absolute on-disk path of the SKILL.md file.
	Path string `json:"path,omitempty"`
	// Editable is true only for user/project sources (see IsEditableSource).
	Editable bool `json:"editable"`
}

// IsEditableSource reports whether a skill/command/memory source may be edited
// in-dashboard. Only user- and project-layer files are writable; plugin and
// builtin sources are read-only.
func IsEditableSource(source string) bool {
	return source == "user" || source == "project"
}

// maxCommandBodyBytes caps the on-disk command file body read into CommandDetail.
const maxCommandBodyBytes = 1 * 1024 * 1024 // 1 MB

// builtinCommands are the Claude Code commands baked into the CLI binary. The
// CLI exposes no machine-readable listing, so they are curated here against
// CuratedBuiltinsVersion; the version probe surfaces drift (see version.go).
//
// The list goes stale in both directions, so re-curating means checking both.
// The CLI binary ships a "Recently changed surfaces" document naming commands
// that were removed or renamed — `strings <claude-binary> | grep -A20 "Removed
// slash commands"` reads it. Additions have no such record and have to come
// from the release notes or from using the CLI. Drift found at 2.1.224:
// /fork had been added, and /pr-comments and /vim had been removed since the
// list was last curated at 2.1.161.
var builtinCommands = []SlashCommand{
	{Name: "/add-dir", Description: "Add a working directory", Source: "builtin"},
	{Name: "/agents", Description: "Manage agents", Source: "builtin"},
	{Name: "/bug", Description: "Report a bug", Source: "builtin"},
	{Name: "/clear", Description: "Clear conversation history", Source: "builtin"},
	{Name: "/code-review", Description: "Review the current diff", Source: "builtin"},
	{Name: "/compact", Description: "Compact context window", Source: "builtin"},
	{Name: "/config", Description: "Open settings", Source: "builtin"},
	{Name: "/cost", Description: "Show token cost of the session", Source: "builtin"},
	{Name: "/doctor", Description: "Diagnose Claude Code health", Source: "builtin"},
	{Name: "/export", Description: "Export the conversation", Source: "builtin"},
	{Name: "/fork", Description: "Fork the session into a parallel conversation", Source: "builtin"},
	{Name: "/help", Description: "Show available commands", Source: "builtin"},
	{Name: "/init", Description: "Initialize a CLAUDE.md", Source: "builtin"},
	{Name: "/login", Description: "Log in to an account", Source: "builtin"},
	{Name: "/logout", Description: "Log out", Source: "builtin"},
	{Name: "/mcp", Description: "Manage MCP servers", Source: "builtin"},
	{Name: "/memory", Description: "Edit memory files", Source: "builtin"},
	{Name: "/model", Description: "Switch model", Source: "builtin"},
	{Name: "/release-notes", Description: "Show release notes", Source: "builtin"},
	{Name: "/resume", Description: "Resume a previous conversation", Source: "builtin"},
	{Name: "/review", Description: "Review a pull request", Source: "builtin"},
	{Name: "/run", Description: "Launch and drive the project's app", Source: "builtin"},
	{Name: "/security-review", Description: "Security review of pending changes on the current branch", Source: "builtin"},
	{Name: "/simplify", Description: "Simplify the changed code", Source: "builtin"},
	{Name: "/status", Description: "Show session status", Source: "builtin"},
	{Name: "/terminal-setup", Description: "Configure terminal key bindings", Source: "builtin"},
	{Name: "/verify", Description: "Verify a change does what it should", Source: "builtin"},
}

// sourceRank orders sources for dedup precedence (lower wins) and for the final
// sort. builtin > project > user > plugin: a more specific layer shadows a less
// specific one of the same name, and builtins can never be overridden.
func sourceRank(source string) int {
	switch source {
	case "builtin":
		return 0
	case "project":
		return 1
	case "user":
		return 2
	default: // plugin:<id>
		return 3
	}
}

// Commands returns the slash commands visible in the scope (without bodies):
// built-ins plus user, project, and plugin commands, deduped by name
// (builtin > project > user > plugin) and sorted builtins-first then by source
// then name. A non-Claude scope returns an empty slice.
func (s Scope) Commands() []SlashCommand {
	details := s.commandDetails(false)
	out := make([]SlashCommand, len(details))
	for i, d := range details {
		out[i] = d.slashCommand()
	}
	return out
}

func (d CommandDetail) slashCommand() SlashCommand {
	return SlashCommand{Name: d.Name, Description: d.Description, Source: d.Source, ArgumentHint: d.ArgumentHint}
}

// CommandDetails returns the same commands as Commands, each with its on-disk
// body (capped at maxCommandBodyBytes). Built-in commands carry an empty Body.
func (s Scope) CommandDetails() []CommandDetail {
	return s.commandDetails(true)
}

func (s Scope) commandDetails(withBody bool) []CommandDetail {
	if !s.Supported {
		return []CommandDetail{}
	}

	out := make([]CommandDetail, 0, len(builtinCommands))
	for _, b := range builtinCommands {
		out = append(out, CommandDetail{Name: b.Name, Description: b.Description, Source: b.Source})
	}

	collect := func(dir, source string) {
		for _, file := range listCommandFiles(dir) {
			if d, ok := readCommand(file, source, withBody); ok {
				out = append(out, d)
			}
		}
	}

	// User commands: <ConfigDir>/commands/*.md
	collect(filepath.Join(s.ConfigDir, "commands"), "user")

	// Project commands: <ProjectCwd>/.claude/commands/*.md
	if s.ProjectCwd != "" {
		collect(filepath.Join(s.ProjectCwd, ".claude", "commands"), "project")
	}

	// Plugin commands: <ConfigDir>/plugins/cache/<marketplace>/<plugin>/**/commands/*.md.
	// Claude's slash menu shows these BARE (/<name>, not /<plugin>:<name>), so we
	// keep the name bare and record the plugin only in Source. The plugin level is
	// two deep under the cache (see pluginDirs).
	for _, pluginDir := range pluginDirs(filepath.Join(s.ConfigDir, "plugins", "cache")) {
		source := "plugin:" + filepath.Base(pluginDir)
		for _, file := range findCommandFiles(pluginDir) {
			if d, ok := readCommand(file, source, withBody); ok {
				out = append(out, d)
			}
		}
	}

	return dedupAndSortCommands(out)
}

// SlashCommands returns everything typeable as a leading-slash command in the
// scope: built-in + file commands AND skills (Claude exposes every skill as a
// /<name> — or /<plugin>:<name> — command). Deduped by name; a real command
// shadows a skill of the same name. A non-Claude scope returns an empty slice.
func (s Scope) SlashCommands() []SlashCommand {
	if !s.Supported {
		return []SlashCommand{}
	}
	details := s.commandDetails(false)
	for _, sk := range s.Skills() {
		details = append(details, CommandDetail{Name: "/" + sk.Name, Description: sk.Description, ArgumentHint: sk.ArgumentHint, Source: sk.Source})
	}
	details = dedupAndSortCommands(details)
	out := make([]SlashCommand, len(details))
	for i, d := range details {
		out[i] = d.slashCommand()
	}
	return out
}

// Skills returns the skills visible in the scope: user, project, and plugin
// skills, deduped by name (project > user > plugin). A non-Claude scope returns
// an empty slice.
func (s Scope) Skills() []SkillEntry {
	if !s.Supported {
		return []SkillEntry{}
	}

	out := []SkillEntry{}

	// User skills: <ConfigDir>/skills/<name>/SKILL.md
	out = append(out, skillsInDir(filepath.Join(s.ConfigDir, "skills"), "user")...)

	// Project skills: <ProjectCwd>/.claude/skills/<name>/SKILL.md
	if s.ProjectCwd != "" {
		out = append(out, skillsInDir(filepath.Join(s.ProjectCwd, ".claude", "skills"), "project")...)
	}

	// Plugin skills: <ConfigDir>/plugins/cache/<marketplace>/<plugin>/**/SKILL.md.
	// Claude's slash menu shows skills BARE (/<name>), so the name stays bare and
	// the plugin is recorded only in Source. Plugin level is two deep (pluginDirs).
	for _, pluginDir := range pluginDirs(filepath.Join(s.ConfigDir, "plugins", "cache")) {
		source := "plugin:" + filepath.Base(pluginDir)
		for _, file := range findSkillFiles(pluginDir) {
			fm := parseFrontmatterFile(file)
			if fm.Name == "" {
				continue
			}
			out = append(out, SkillEntry{Name: fm.Name, Description: fm.Description, ArgumentHint: fm.ArgumentHint, Source: source, Path: file, Editable: IsEditableSource(source)})
		}
	}

	return dedupAndSortSkills(out)
}

// skillsInDir enumerates <dir>/<name>/SKILL.md immediate-child skills.
func skillsInDir(dir, source string) []SkillEntry {
	var out []SkillEntry
	for _, entry := range listSkillDirs(dir) {
		skillPath := filepath.Join(dir, entry, "SKILL.md")
		fm := parseFrontmatterFile(skillPath)
		name := fm.Name
		if name == "" {
			name = entry
		}
		out = append(out, SkillEntry{Name: name, Description: fm.Description, ArgumentHint: fm.ArgumentHint, Source: source, Path: skillPath, Editable: IsEditableSource(source)})
	}
	return out
}

// dedupAndSort stable-sorts in by source rank, drops later duplicates sharing
// a name (keeping the highest-ranked first occurrence), then re-sorts the
// deduped result by source string and name — with an additional leading
// source-rank tiebreak when rankTiebreak is true (commands need it since
// "plugin:<id>" does not sort alphabetically after "user"/"project"; skills'
// original sort never had this tiebreak, so it is preserved as false there).
func dedupAndSort[T any](in []T, name func(T) string, source func(T) string, rankTiebreak bool) []T {
	sort.SliceStable(in, func(i, j int) bool {
		return sourceRank(source(in[i])) < sourceRank(source(in[j]))
	})
	seen := make(map[string]bool, len(in))
	deduped := make([]T, 0, len(in))
	for _, item := range in {
		n := name(item)
		if seen[n] {
			continue
		}
		seen[n] = true
		deduped = append(deduped, item)
	}
	sort.SliceStable(deduped, func(i, j int) bool {
		si, sj := source(deduped[i]), source(deduped[j])
		if rankTiebreak {
			if ri, rj := sourceRank(si), sourceRank(sj); ri != rj {
				return ri < rj
			}
		}
		if si != sj {
			return si < sj
		}
		return name(deduped[i]) < name(deduped[j])
	})
	return deduped
}

func dedupAndSortCommands(in []CommandDetail) []CommandDetail {
	return dedupAndSort(in, func(c CommandDetail) string { return c.Name }, func(c CommandDetail) string { return c.Source }, true)
}

func dedupAndSortSkills(in []SkillEntry) []SkillEntry {
	return dedupAndSort(in, func(s SkillEntry) string { return s.Name }, func(s SkillEntry) string { return s.Source }, false)
}

// ---- filesystem helpers (single canonical copy) ----

// listCommandFiles returns absolute paths of *.md files directly inside dir.
// Symlinks and hidden entries are rejected so a malicious entry cannot redirect
// a read outside the configured root.
func listCommandFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// listSkillDirs returns immediate child directory names of dir that contain a
// regular SKILL.md. Symlinked and hidden entries are skipped.
func listSkillDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		skillFile := filepath.Join(dir, e.Name(), "SKILL.md")
		info, err := os.Lstat(skillFile)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// immediateDirs returns the non-hidden immediate child directories of dir.
func immediateDirs(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out
}

// pluginDirs returns the per-plugin directories under the plugin cache. The
// cache is laid out as <cache>/<marketplace>/<plugin>/<version>/..., so the
// plugin level is two deep; filepath.Base of each result is the plugin name
// used as the /<plugin>:<name> command/skill namespace.
func pluginDirs(cacheDir string) []string {
	var out []string
	for _, marketplace := range immediateDirs(cacheDir) {
		out = append(out, immediateDirs(marketplace)...)
	}
	sort.Strings(out)
	return out
}

// findSkillFiles walks root for SKILL.md files, pruning node_modules + hidden dirs.
func findSkillFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "SKILL.md" {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// findCommandFiles walks root for *.md files whose immediate parent dir is
// "commands", pruning node_modules + hidden dirs.
func findCommandFiles(root string) []string {
	var files []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "node_modules" || (strings.HasPrefix(name, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == "commands" {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// readCommand builds a CommandDetail for a slash-command markdown file. Name is
// the filename without .md, prefixed "/" (bare, matching Claude's slash menu;
// the originating plugin is recorded in Source, not the name). Description comes
// from optional YAML frontmatter, and (when withBody) body is the file content
// capped at maxCommandBodyBytes. ok is false only when the path has no usable name.
func readCommand(path, source string, withBody bool) (CommandDetail, bool) {
	base := strings.TrimSuffix(filepath.Base(path), ".md")
	if base == "" {
		return CommandDetail{}, false
	}
	d := CommandDetail{Name: "/" + base, Source: source, Path: path, Editable: IsEditableSource(source)}

	if withBody {
		body, fm := readCommandBody(path)
		d.Body = body
		d.Description = fm.Description
		d.ArgumentHint = fm.ArgumentHint
		return d, true
	}

	fm := parseFrontmatterFile(path)
	d.Description = fm.Description
	d.ArgumentHint = fm.ArgumentHint
	return d, true
}

// readCommandBody reads the (capped) file content and its frontmatter
// description in one pass.
func readCommandBody(path string) (body string, fm frontmatter) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", frontmatter{}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", frontmatter{}
	}
	defer f.Close()

	limit := int64(maxCommandBodyBytes)
	truncated := info.Size() > limit
	readSize := info.Size()
	if truncated {
		readSize = limit
	}
	buf := make([]byte, readSize)
	n, _ := io.ReadFull(f, buf)
	body = string(buf[:n])
	if truncated {
		body += "\n\n<!-- truncated: file exceeds 1 MB limit -->\n"
	}
	return body, parseFrontmatter(strings.NewReader(body))
}

// frontmatter holds the leading YAML keys a command or skill file contributes
// to the dashboard's command list.
type frontmatter struct {
	Name         string
	Description  string
	ArgumentHint string
}

// parseFrontmatterFile opens path and parses its frontmatter. A missing or
// unreadable file yields the zero value, never an error — a single bad file
// must not drop the whole command list.
func parseFrontmatterFile(path string) frontmatter {
	f, err := os.Open(path)
	if err != nil {
		return frontmatter{}
	}
	defer f.Close()
	return parseFrontmatter(f)
}

// parseFrontmatter scans the leading YAML frontmatter (capped at 80 lines) of r
// for the `name:`, `description:`, and `argument-hint:` keys, resolving
// block-scalar markers (>-, >, |, |-) against the following indented line.
// `argument-hint:` passes through sanitizeArgumentHint — the file may come from
// the plugin cache, and this is the only place that trust boundary is crossed.
// It always scans to the end of the frontmatter: the keys may appear in any
// order, so returning early on one of them would hide the others.
func parseFrontmatter(r io.Reader) frontmatter {
	var fm frontmatter
	sc := bufio.NewScanner(r)
	lineNum := 0
	inFrontmatter := false
	readingDesc := false

	for sc.Scan() {
		lineNum++
		if lineNum > 80 {
			break
		}
		line := sc.Text()

		if lineNum == 1 {
			if line == "---" {
				inFrontmatter = true
				continue
			}
			return frontmatter{}
		}
		if !inFrontmatter || line == "---" {
			break
		}

		if readingDesc && strings.HasPrefix(line, " ") {
			if fm.Description == "" {
				fm.Description = strings.TrimSpace(line)
			}
			readingDesc = false
			continue
		}
		readingDesc = false

		if strings.HasPrefix(line, "name:") {
			fm.Name = scalarValue(strings.TrimPrefix(line, "name:"))
		}
		if strings.HasPrefix(line, "argument-hint:") {
			fm.ArgumentHint = sanitizeArgumentHint(scalarValue(strings.TrimPrefix(line, "argument-hint:")))
		}
		if strings.HasPrefix(line, "description:") {
			desc := strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			switch desc {
			case ">-", ">", "|", "|-":
				readingDesc = true
			default:
				fm.Description = scalarValue(desc)
			}
		}
	}
	return fm
}

// scalarValue unquotes a single-line YAML scalar.
//
// A quoted value ends at its closing quote, so a trailing `# comment` is
// dropped rather than swallowed — real argument hints carry both, e.g.
// `"[arg]" # required when user-invocable`. Inside single quotes YAML escapes a
// literal apostrophe by doubling it, and inside double quotes with a backslash;
// both appear in real hints, so a naive search for the next quote character
// would truncate the value at the escape.
func scalarValue(raw string) string {
	v := strings.TrimSpace(raw)
	if len(v) > 0 {
		switch v[0] {
		case '\'':
			return unquoteSingle(v)
		case '"':
			return unquoteDouble(v)
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = strings.TrimSpace(v[:i])
	}
	return v
}

func unquoteSingle(v string) string {
	var b strings.Builder
	for i := 1; i < len(v); i++ {
		if v[i] != '\'' {
			b.WriteByte(v[i])
			continue
		}
		if i+1 < len(v) && v[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		return b.String()
	}
	return b.String()
}

func unquoteDouble(v string) string {
	var b strings.Builder
	for i := 1; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) {
			b.WriteByte(v[i+1])
			i++
			continue
		}
		if v[i] == '"' {
			return b.String()
		}
		b.WriteByte(v[i])
	}
	return b.String()
}
