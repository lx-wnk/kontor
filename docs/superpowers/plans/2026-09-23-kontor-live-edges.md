# Kontor Zentrale Live Edges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Draw a static, fading edge in the Zentrale hub from each agent to every vault note it read (dashed) or wrote (dotted) in the last ten minutes, and list the same notes in the hub's list view.

**Architecture:** The Go parser extracts note touches from Obsidian MCP calls and `curl …/vault/…` commands during the incremental JSONL scan it already runs for token totals. The merger maps the vault-relative paths onto the graph's root-relative form and re-applies the ten-minute window, and ships them as `Agent.recentNotes` over the existing SSE stream. The hub turns them into edges on its Canvas 2D brain and into rows in its list view.

**Tech Stack:** Go 1.26 (parser, merger, obsidian app), tygo codegen, Vue 3 + TypeScript, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-09-23-kontor-live-edges-design.md`

## Global Constraints

- Work on `develop`, commit per task, push at the end; never push to `main`.
- Everything that ships is English; Conventional Commits (`feat:`, `fix:`, `test:`, `docs:`, `refactor:`); no phase or task labels in commit messages.
- Every commit message ends with:
  `Co-Authored-By: Claude Opus 5.5 (1M context) <noreply@anthropic.com>` and `Claude-Session: https://claude.ai/code/session_01Y6e4eMyATMkbj8cWCDsJ1P`
- Comments only for timing, external contracts, non-obvious edge cases, or case-to-effect mappings; one short line.
- Window: ten minutes (`parser.NoteWindow`, `EDGE_WINDOW_MS`). Cap: 50 touches per session. Alpha: 0.9 fresh to 0.2 at ten minutes. Read dash `[4, 3]`, write dash `[1, 3]` with round caps.
- No animation frame loop; no new dependency.
- `go test ./...` / `task test` regenerate `server/internal/db/ent/`: run `git checkout -- server/internal/db/ent/` before every commit. Never run Go tests and `pnpm test:e2e` at the same time.
- `pnpm build` wipes `server/frontend/dist/.gitkeep`: restore with `git checkout HEAD -- server/frontend/dist/.gitkeep` before committing.
- Gate output is pasted raw into the task report; never piped through `tail`/`grep` in a way that hides the exit code.

## Review Focus

- A message timestamp slightly in the future (clock skew between machines) must render a fresh edge, not a negative age or a hidden one — pinned in Task 6.
- A note renamed or deleted after the agent touched it (path no longer in the graph) must simply lose its edge — pinned in Task 6.
- The graph still loading while agents already carry notes must draw nothing and not throw — pinned in Task 6.
- A session file that is truncated and rewritten must not keep notes from its old content — pinned in Task 2.
- A note name with spaces or umlauts reaches the URL percent-encoded and must still match the graph's decoded path — pinned in Task 1.

---

### Task 1: Extract note touches from a message

**Files:**
- Modify: `sdk/types.go` (add `NoteTouchKind`, its constants, `NoteTouch`; next to `RecentTool`)
- Create: `server/internal/parser/notes.go`
- Test: `server/internal/parser/notes_test.go`

**Interfaces:**
- Produces:
  - `sdk.NoteTouchKind` (`string`), `sdk.NoteTouchKindRead = "read"`, `sdk.NoteTouchKindWrite = "write"`
  - `sdk.NoteTouch{Path string; Kind NoteTouchKind; At string}` with JSON tags `path`, `kind`, `at`
  - `parser.NoteWindow time.Duration` (10 min)
  - `parser.NoteTouch{Path string; Kind sdk.NoteTouchKind; At time.Time}`
  - `func noteTouchesOf(m Message) []NoteTouch`
  - `func mergeNoteTouches(touches, add []NoteTouch, now time.Time) []NoteTouch`
  - `const maxNoteTouches = 50`

- [ ] **Step 1: Add the SDK types**

In `sdk/types.go`, directly after the `RecentTool` struct:

```go
// NoteTouchKind says whether an agent read or wrote a vault note.
type NoteTouchKind string

const (
	NoteTouchKindRead  NoteTouchKind = "read"
	NoteTouchKindWrite NoteTouchKind = "write"
)

// NoteTouch is one vault note an agent read or wrote recently.
type NoteTouch struct {
	// Path is relative to obsidian.vaultRoot, the form the vault graph lists.
	Path string        `json:"path"`
	Kind NoteTouchKind `json:"kind"`
	// At is RFC 3339, like Agent.LastActivity.
	At string `json:"at"`
}
```

- [ ] **Step 2: Write the failing tests**

Create `server/internal/parser/notes_test.go`:

```go
package parser

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var noteClock = time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)

func touch(path string, kind sdk.NoteTouchKind, at time.Time) NoteTouch {
	return NoteTouch{Path: path, Kind: kind, At: at}
}

func toolMessage(t *testing.T, at time.Time, name string, input map[string]string) Message {
	t.Helper()
	in, err := json.Marshal(input)
	require.NoError(t, err)
	content, err := json.Marshal([]toolUseBlock{{Type: "tool_use", ID: "t1", Name: name, Input: in}})
	require.NoError(t, err)
	return Message{Role: "assistant", Timestamp: at, Content: content}
}

func TestCurlNoteTouches(t *testing.T) {
	read, write := sdk.NoteTouchKindRead, sdk.NoteTouchKindWrite
	var zero time.Time
	tests := []struct {
		name    string
		command string
		want    []NoteTouch
	}{
		{"default root expands", `curl -sk -H "Authorization: Bearer $OBSIDIAN_API_KEY" "$OBSIDIAN_BASE_URL/vault/${OBSIDIAN_ROOT:-claude-memory}/private/kontor/sessions/hub.md"`,
			[]NoteTouch{touch("claude-memory/private/kontor/sessions/hub.md", read, zero)}},
		{"literal root", `curl -sk "$OBSIDIAN_BASE_URL/vault/claude-memory/misc/tools.md"`,
			[]NoteTouch{touch("claude-memory/misc/tools.md", read, zero)}},
		{"PUT is a write", `curl -sk -X PUT -H "Content-Type: text/markdown" --data-binary @note.md "$OBSIDIAN_BASE_URL/vault/claude-memory/work/x.md"`,
			[]NoteTouch{touch("claude-memory/work/x.md", write, zero)}},
		{"--request PATCH is a write", `curl --request PATCH "$B/vault/claude-memory/work/x.md"`,
			[]NoteTouch{touch("claude-memory/work/x.md", write, zero)}},
		{"-XPOST without a space is a write", `curl -XPOST "$B/vault/claude-memory/work/x.md"`,
			[]NoteTouch{touch("claude-memory/work/x.md", write, zero)}},
		{"a method stays in its own segment", `curl -X PUT "$B/vault/claude-memory/a.md" && curl "$B/vault/claude-memory/b.md"`,
			[]NoteTouch{touch("claude-memory/a.md", write, zero), touch("claude-memory/b.md", read, zero)}},
		{"a bare variable is dropped", `curl "$OBSIDIAN_BASE_URL/vault/$R/x.md"`, nil},
		{"a braced variable without default is dropped", `curl "$B/vault/${ROOT}/x.md"`, nil},
		{"a command substitution is dropped", `for f in a b; do curl "$B/vault/claude-memory/$(echo $f).md"; done`, nil},
		{"percent-encoding is decoded", `curl "$B/vault/claude-memory/Meine%20Notiz%20%C3%BCber.md"`,
			[]NoteTouch{touch("claude-memory/Meine Notiz über.md", read, zero)}},
		{"a directory listing is dropped", `curl "$B/vault/claude-memory/private/"`, nil},
		{"a query string is cut", `curl "$B/vault/claude-memory/x.md?raw=1"`,
			[]NoteTouch{touch("claude-memory/x.md", read, zero)}},
		{"no vault URL", `ls -la`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, curlNoteTouches(tt.command))
		})
	}
}

func TestNoteTouchesOf(t *testing.T) {
	read, write := sdk.NoteTouchKindRead, sdk.NoteTouchKindWrite
	at := noteClock
	tests := []struct {
		name string
		msg  Message
		want []NoteTouch
	}{
		{"MCP read_note", toolMessage(t, at, "mcp__obsidian__obsidian_read_note", map[string]string{"vault": "mainvault", "path": "claude-memory/a.md"}),
			[]NoteTouch{touch("claude-memory/a.md", read, at)}},
		{"MCP edit_note", toolMessage(t, at, "mcp__obsidian__obsidian_edit_note", map[string]string{"path": "claude-memory/a.md"}),
			[]NoteTouch{touch("claude-memory/a.md", write, at)}},
		{"MCP create_note", toolMessage(t, at, "mcp__obsidian__obsidian_create_note", map[string]string{"path": "claude-memory/a.md"}),
			[]NoteTouch{touch("claude-memory/a.md", write, at)}},
		{"MCP search is ignored", toolMessage(t, at, "mcp__obsidian__obsidian_search_vault", map[string]string{"query": "x", "path": "claude-memory/a.md"}), nil},
		{"the Read tool is ignored", toolMessage(t, at, "Read", map[string]string{"file_path": "/vault/claude-memory/a.md"}), nil},
		{"Bash carries the message time", toolMessage(t, at, "Bash", map[string]string{"command": `curl "$B/vault/claude-memory/a.md"`}),
			[]NoteTouch{touch("claude-memory/a.md", read, at)}},
		{"a user message is ignored", Message{Role: "user", Timestamp: at, Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1"}]`)}, nil},
		{"string content is ignored", Message{Role: "assistant", Timestamp: at, Content: json.RawMessage(`"hello"`)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, noteTouchesOf(tt.msg))
		})
	}
}

func TestMergeNoteTouches(t *testing.T) {
	read, write := sdk.NoteTouchKindRead, sdk.NoteTouchKindWrite
	now := noteClock

	t.Run("keeps the newest touch per path and kind, newest first", func(t *testing.T) {
		got := mergeNoteTouches(
			[]NoteTouch{touch("a.md", read, now.Add(-5*time.Minute))},
			[]NoteTouch{touch("a.md", read, now.Add(-time.Minute)), touch("a.md", write, now.Add(-2*time.Minute))},
			now,
		)
		assert.Equal(t, []NoteTouch{touch("a.md", read, now.Add(-time.Minute)), touch("a.md", write, now.Add(-2*time.Minute))}, got)
	})

	t.Run("drops touches older than the window and untimed ones", func(t *testing.T) {
		got := mergeNoteTouches(nil, []NoteTouch{
			touch("old.md", read, now.Add(-NoteWindow-time.Second)),
			touch("edge.md", read, now.Add(-NoteWindow)),
			touch("untimed.md", read, time.Time{}),
		}, now)
		assert.Equal(t, []NoteTouch{touch("edge.md", read, now.Add(-NoteWindow))}, got)
	})

	t.Run("caps at the newest maxNoteTouches", func(t *testing.T) {
		var add []NoteTouch
		for i := range maxNoteTouches + 10 {
			add = append(add, touch(fmt.Sprintf("n%03d.md", i), write, now.Add(-time.Duration(i)*time.Second)))
		}
		got := mergeNoteTouches(nil, add, now)
		require.Len(t, got, maxNoteTouches)
		assert.Equal(t, "n000.md", got[0].Path)
		assert.Equal(t, fmt.Sprintf("n%03d.md", maxNoteTouches-1), got[maxNoteTouches-1].Path)
	})
}
```

- [ ] **Step 3: Run the tests to verify they fail**

Run: `cd server && go test ./internal/parser/ -run 'TestCurlNoteTouches|TestNoteTouchesOf|TestMergeNoteTouches' -count=1`
Expected: FAIL — `undefined: curlNoteTouches` (and the other new names).

- [ ] **Step 4: Implement**

Create `server/internal/parser/notes.go`:

```go
package parser

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/sdk"
)

// NoteWindow is how far back an agent's note touches are kept.
const NoteWindow = 10 * time.Minute

// maxNoteTouches caps one session's touches so a loop of writes cannot bloat the SSE payload.
const maxNoteTouches = 50

// NoteTouch is a vault note an agent's tool call read or wrote. Path is
// relative to the whole vault, exactly as the call named it.
type NoteTouch struct {
	Path string
	Kind sdk.NoteTouchKind
	At   time.Time
}

const obsidianMCPPrefix = "mcp__obsidian__obsidian_"

var mcpNoteKinds = map[string]sdk.NoteTouchKind{
	"read_note":   sdk.NoteTouchKindRead,
	"create_note": sdk.NoteTouchKindWrite,
	"edit_note":   sdk.NoteTouchKindWrite,
}

var (
	shellDefaultRe = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*:-([^}]*)\}`)
	shellSegmentRe = regexp.MustCompile(`&&|\|\||[;|\n]`)
	vaultURLRe     = regexp.MustCompile("/vault/([^\\s\"'`?#\\\\]+)")
	writeMethodRe  = regexp.MustCompile(`(?:-X|--request)\s*['"]?(?:PUT|POST|PATCH)\b`)
)

func noteTouchesOf(m Message) []NoteTouch {
	if m.Role != "assistant" || len(m.Content) == 0 || m.Content[0] != '[' {
		return nil
	}
	var blocks []toolUseBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	var out []NoteTouch
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		for _, t := range toolNoteTouches(b.Name, b.Input) {
			t.At = m.Timestamp
			out = append(out, t)
		}
	}
	return out
}

func toolNoteTouches(name string, input json.RawMessage) []NoteTouch {
	if op, isMCP := strings.CutPrefix(name, obsidianMCPPrefix); isMCP {
		kind, known := mcpNoteKinds[op]
		var in struct {
			Path string `json:"path"`
		}
		if !known || json.Unmarshal(input, &in) != nil || !isNotePath(in.Path) {
			return nil
		}
		return []NoteTouch{{Path: in.Path, Kind: kind}}
	}
	if name != "Bash" {
		return nil
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	return curlNoteTouches(in.Command)
}

// curlNoteTouches reads note paths out of a shell command's vault REST URLs.
// Only ${NAME:-default} is knowable without the agent's environment; a path
// still holding a variable after that is dropped rather than guessed.
func curlNoteTouches(command string) []NoteTouch {
	if !strings.Contains(command, "/vault/") {
		return nil
	}
	expanded := shellDefaultRe.ReplaceAllString(command, "$1")
	var out []NoteTouch
	for _, segment := range shellSegmentRe.Split(expanded, -1) {
		kind := sdk.NoteTouchKindRead
		if writeMethodRe.MatchString(segment) {
			kind = sdk.NoteTouchKindWrite
		}
		for _, match := range vaultURLRe.FindAllStringSubmatch(segment, -1) {
			notePath, err := url.PathUnescape(match[1])
			if err != nil || strings.Contains(notePath, "$") || !isNotePath(notePath) {
				continue
			}
			out = append(out, NoteTouch{Path: notePath, Kind: kind})
		}
	}
	return out
}

func isNotePath(p string) bool { return strings.HasSuffix(p, ".md") }

// mergeNoteTouches folds add into touches: one entry per (path, kind) at its
// newest time, nothing older than NoteWindow before now, newest first, capped.
func mergeNoteTouches(touches, add []NoteTouch, now time.Time) []NoteTouch {
	type key struct {
		path string
		kind sdk.NoteTouchKind
	}
	newest := make(map[key]time.Time, len(touches)+len(add))
	for _, list := range [][]NoteTouch{touches, add} {
		for _, t := range list {
			k := key{t.Path, t.Kind}
			if at, seen := newest[k]; !seen || t.At.After(at) {
				newest[k] = t.At
			}
		}
	}
	cutoff := now.Add(-NoteWindow)
	out := make([]NoteTouch, 0, len(newest))
	for k, at := range newest {
		if at.Before(cutoff) {
			continue
		}
		out = append(out, NoteTouch{Path: k.path, Kind: k.kind, At: at})
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	if len(out) > maxNoteTouches {
		out = out[:maxNoteTouches]
	}
	return out
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd server && go test ./internal/parser/ -run 'TestCurlNoteTouches|TestNoteTouchesOf|TestMergeNoteTouches' -count=1 && go vet ./internal/parser/ && cd ../sdk && go vet ./...`
Expected: `ok  github.com/lx-wnk/kontor/server/internal/parser`, vet silent.

- [ ] **Step 6: Commit**

```bash
git checkout -- server/internal/db/ent/
git add sdk/types.go server/internal/parser/notes.go server/internal/parser/notes_test.go
git commit -m "feat: extract vault note touches from agent tool calls"
```

**Acceptance:** all three tests pass; the table covers every row of the spec's Bash rules and the MCP table.

---

### Task 2: Collect note touches in the incremental session scan

**Files:**
- Modify: `server/internal/parser/messages.go:101-148` (`ScanMessagesFrom` takes a callback)
- Modify: `server/internal/parser/parser.go` (`fullScanUsage`, `scanFullFileTokenUsage`, `tokenOffsetCacheEntry`, `tokenUsageForFile`, `SessionData`, `ParseSessionFile`)
- Test: `server/internal/parser/parser_incremental_test.go`

**Interfaces:**
- Consumes: `noteTouchesOf`, `mergeNoteTouches`, `NoteTouch` (Task 1)
- Produces:
  - `func ScanMessagesFrom(path string, offset int64, fn func(m Message)) (int64, error)`
  - `func addMessageUsage(dst *sdk.TokenUsage, m Message)`
  - `fullScanUsage.notes []NoteTouch`
  - `SessionData.RecentNotes []NoteTouch`

- [ ] **Step 1: Write the failing tests**

In `server/internal/parser/parser_incremental_test.go`, add `"encoding/json"`, `"time"` and `"github.com/stretchr/testify/assert"` to the imports, replace the two `ScanMessagesFrom` tests with the versions below, and append the new tests:

```go
func TestScanMessagesFrom_TrailingPartialLineLeftForNextCall(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "partial.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	_, err = f.WriteString(incrementalLineTemplate)
	require.NoError(t, err)
	partial := `{"type":"assistant","timestamp":"2025-01-15T10:30:0`
	_, err = f.WriteString(partial)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var usage sdk.TokenUsage
	newOffset, err := ScanMessagesFrom(path, 0, func(m Message) { addMessageUsage(&usage, m) })
	require.NoError(t, err)
	require.Equal(t, expectedUsage(1), usage)
	require.Equal(t, int64(len(incrementalLineTemplate)), newOffset,
		"offset must stop at the last complete line, leaving the trailing partial line unconsumed")

	var usage2 sdk.TokenUsage
	newOffset2, err := ScanMessagesFrom(path, newOffset, func(m Message) { addMessageUsage(&usage2, m) })
	require.NoError(t, err)
	require.Equal(t, sdk.TokenUsage{}, usage2)
	require.Equal(t, newOffset, newOffset2, "an unchanged partial line must not advance the offset")
}

func TestScanMessagesFrom_NoGrowthCallsNothing(t *testing.T) {
	dir := t.TempDir()
	path := writeIncrementalSession(t, dir, 5)
	info, err := os.Stat(path)
	require.NoError(t, err)

	calls := 0
	newOffset, err := ScanMessagesFrom(path, info.Size(), func(Message) { calls++ })
	require.NoError(t, err)
	require.Zero(t, calls)
	require.Equal(t, info.Size(), newOffset)
}

func appendNoteLine(t *testing.T, f *os.File, at time.Time, command string) {
	t.Helper()
	input, err := json.Marshal(map[string]string{"command": command})
	require.NoError(t, err)
	line, err := json.Marshal(map[string]any{
		"type":      "assistant",
		"timestamp": at.UTC().Format(time.RFC3339Nano),
		"message": map[string]any{
			"role":    "assistant",
			"content": []map[string]any{{"type": "tool_use", "id": "t", "name": "Bash", "input": json.RawMessage(input)}},
		},
	})
	require.NoError(t, err)
	_, err = f.Write(append(line, '\n'))
	require.NoError(t, err)
	require.NoError(t, f.Sync())
}

func notePaths(touches []NoteTouch) []string {
	out := make([]string, len(touches))
	for i, t := range touches {
		out[i] = t.Path
	}
	return out
}

func TestTokenUsageForFile_CollectsNoteTouchesIncrementally(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck

	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/a.md"`)
	first, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/a.md"}, notePaths(first.notes))

	appendNoteLine(t, f, time.Now().Add(time.Second), `curl -X PUT "$B/vault/claude-memory/b.md"`)
	second, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/b.md", "claude-memory/a.md"}, notePaths(second.notes))

	full, err := scanFullFileTokenUsage(path)
	require.NoError(t, err)
	assert.Equal(t, full.notes, second.notes, "the incremental window must equal a full rescan")
}

func TestTokenUsageForFile_RewrittenFileDropsOldNotes(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/old-one.md"`)
	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/old-two.md"`)
	require.NoError(t, f.Close())
	_, err = tokenUsageForFile(path)
	require.NoError(t, err)

	g, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	require.NoError(t, err)
	appendNoteLine(t, g, time.Now(), `curl "$B/vault/claude-memory/new.md"`)
	require.NoError(t, g.Close())

	got, err := tokenUsageForFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/new.md"}, notePaths(got.notes))
}

func TestParseSessionFile_CarriesRecentNotes(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	appendNoteLine(t, f, time.Now(), `curl "$B/vault/claude-memory/a.md"`)
	require.NoError(t, f.Close())

	data, err := ParseSessionFile(path)
	require.NoError(t, err)
	assert.Equal(t, []string{"claude-memory/a.md"}, notePaths(data.RecentNotes))
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd server && go test ./internal/parser/ -count=1`
Expected: build FAIL — `too many arguments in call to ScanMessagesFrom`, `undefined: addMessageUsage`, `first.notes undefined`.

- [ ] **Step 3: Implement `ScanMessagesFrom` with a callback**

In `server/internal/parser/messages.go`, replace the doc comment and function (lines 101-148) with:

```go
// ScanMessagesFrom scans path starting at byte offset and calls fn for every
// message decoded in the appended region, reusing decodeMessageLine so this can
// never diverge from ScanMessages. It returns newOffset — offset plus bytes
// through the last complete line's trailing '\n'; a trailing partial line (a
// write still in progress) is left unconsumed for the next call. size <= offset
// (nothing appended) returns the offset unchanged without calling fn.
func ScanMessagesFrom(path string, offset int64, fn func(m Message)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	info, err := f.Stat()
	if err != nil {
		return offset, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() <= offset {
		return offset, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, fmt.Errorf("seek %s: %w", path, err)
	}

	newOffset := offset
	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			newOffset += int64(len(line))
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if m, ok := decodeMessageLine(trimmed); ok {
					fn(m)
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break // trailing partial line — left for the next call
			}
			return offset, fmt.Errorf("read %s: %w", path, readErr)
		}
	}
	return newOffset, nil
}
```

If `sdk` is no longer referenced in `messages.go` after this, keep the import only if `Message.Usage` still needs it (it does: `Usage *sdk.TokenUsage`).

- [ ] **Step 4: Thread notes through `parser.go`**

1. Add a `notes` field to `fullScanUsage`:

```go
type fullScanUsage struct {
	// hasCompaction is true when at least one compact_boundary line was seen.
	// Used only for diagnostics — the token total does not depend on it.
	hasCompaction bool
	sdk.TokenUsage
	notes []NoteTouch
}
```

2. Add `addMessageUsage` directly above `scanFullFileTokenUsage`:

```go
// addMessageUsage adds one assistant message's per-message usage to dst.
func addMessageUsage(dst *sdk.TokenUsage, m Message) {
	if m.Role != "assistant" || m.Usage == nil {
		return
	}
	dst.InputTokens += m.Usage.InputTokens
	dst.OutputTokens += m.Usage.OutputTokens
	dst.CacheCreationTokens += m.Usage.CacheCreationTokens
	dst.CacheReadTokens += m.Usage.CacheReadTokens
}
```

3. Replace the body of `scanFullFileTokenUsage` (keep its doc comment):

```go
func scanFullFileTokenUsage(path string) (fullScanUsage, error) {
	var total fullScanUsage
	var touches []NoteTouch
	err := ScanMessages(path, 0, func(m Message) error {
		if isCompactBoundaryType(m.Type, m.Subtype) {
			total.hasCompaction = true
			return nil
		}
		addMessageUsage(&total.TokenUsage, m)
		touches = append(touches, noteTouchesOf(m)...)
		return nil
	})
	if err != nil {
		return fullScanUsage{}, fmt.Errorf("scan %s: %w", path, err)
	}
	total.notes = mergeNoteTouches(nil, touches, time.Now())
	return total, nil
}
```

4. Add `notes []NoteTouch` to `tokenOffsetCacheEntry` (after `running`).

5. In `tokenUsageForFile`, replace the incremental branch:

```go
	if ok && entry.inode == inode && size >= entry.offset {
		var usage sdk.TokenUsage
		var added []NoteTouch
		newOffset, scanErr := ScanMessagesFrom(path, entry.offset, func(m Message) {
			addMessageUsage(&usage, m)
			added = append(added, noteTouchesOf(m)...)
		})
		if scanErr == nil {
			tokenOffsetCacheMu.Lock()
			entry.running.InputTokens += usage.InputTokens
			entry.running.OutputTokens += usage.OutputTokens
			entry.running.CacheCreationTokens += usage.CacheCreationTokens
			entry.running.CacheReadTokens += usage.CacheReadTokens
			entry.notes = mergeNoteTouches(entry.notes, added, time.Now())
			entry.offset = newOffset
			running, notes := entry.running, entry.notes
			tokenOffsetCacheMu.Unlock()
			return fullScanUsage{TokenUsage: running, notes: notes}, nil
		}
		slog.Warn("parser: incremental token scan failed — falling back to full rescan", "path", path, "err", scanErr)
	}
```

and the seeding line:

```go
	tokenOffsetCache[path] = &tokenOffsetCacheEntry{inode: inode, offset: size, running: full.TokenUsage, notes: full.notes}
```

6. Add `RecentNotes []NoteTouch` to `SessionData` directly below `LastTools`.

7. In `ParseSessionFile`, in the `else` branch that sets `data.TokenUsage = full.TokenUsage`, add:

```go
		data.RecentNotes = full.notes
```

Update the comment above `tokenUsageForFile` to say it returns the lifetime token total **and the session's note-touch window**.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd server && go test ./internal/parser/ -count=1 -race && go vet ./...`
Expected: `ok  github.com/lx-wnk/kontor/server/internal/parser`, every pre-existing token test included; vet silent.

- [ ] **Step 6: Commit**

```bash
git checkout -- server/internal/db/ent/
git add server/internal/parser/messages.go server/internal/parser/parser.go server/internal/parser/parser_incremental_test.go
git commit -m "feat: keep a ten-minute note-touch window in the incremental session scan"
```

**Acceptance:** the whole `internal/parser` package passes under `-race`; the incremental window equals a full rescan; a rewritten file keeps no old notes.

---

### Task 3: Map vault paths onto the graph's root

**Files:**
- Modify: `server/internal/apps/obsidian/client.go` (add `RootRelative` below `resolveVaultPath`)
- Test: `server/internal/apps/obsidian/client_test.go`

**Interfaces:**
- Produces: `func RootRelative(root, vaultPath string) (string, bool)`

- [ ] **Step 1: Write the failing test**

Append to `server/internal/apps/obsidian/client_test.go`:

```go
func TestRootRelative(t *testing.T) {
	tests := []struct {
		root, vaultPath, want string
		ok                    bool
	}{
		{"claude-memory", "claude-memory/private/x.md", "private/x.md", true},
		{"claude-memory/", "claude-memory/x.md", "x.md", true},
		{"claude-memory", "claude-memory/a/../b.md", "b.md", true},
		{"claude-memory", "claude-memory/../other/x.md", "", false},
		{"claude-memory", "other/x.md", "", false},
		{"claude-memory", "claude-memory-old/x.md", "", false},
		{"claude-memory", "claude-memory", "", false},
		{"", "claude-memory/x.md", "", false},
		{"claude-memory", "", "", false},
	}
	for _, tt := range tests {
		got, ok := obsidian.RootRelative(tt.root, tt.vaultPath)
		if got != tt.want || ok != tt.ok {
			t.Errorf("RootRelative(%q, %q) = (%q, %v), want (%q, %v)", tt.root, tt.vaultPath, got, ok, tt.want, tt.ok)
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `cd server && go test ./internal/apps/obsidian/ -run TestRootRelative -count=1`
Expected: FAIL — `undefined: obsidian.RootRelative`.

- [ ] **Step 3: Implement**

In `server/internal/apps/obsidian/client.go`, directly after `resolveVaultPath`:

```go
// RootRelative maps a path relative to the whole vault — as an agent's REST
// URL or MCP call names it — onto the graph's form, relative to root. ok is
// false for an empty root, the root itself, or anything outside it.
func RootRelative(root, vaultPath string) (string, bool) {
	if root == "" || vaultPath == "" {
		return "", false
	}
	rootClean := path.Clean("/" + root)
	full := path.Clean("/" + vaultPath)
	rel, ok := strings.CutPrefix(full, rootClean+"/")
	return rel, ok && rel != ""
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `cd server && go test ./internal/apps/obsidian/ -count=1 && go vet ./internal/apps/obsidian/`
Expected: `ok  github.com/lx-wnk/kontor/server/internal/apps/obsidian`.

- [ ] **Step 5: Commit**

```bash
git checkout -- server/internal/db/ent/
git add server/internal/apps/obsidian/client.go server/internal/apps/obsidian/client_test.go
git commit -m "feat: map vault-relative note paths onto the configured vault root"
```

**Acceptance:** all nine table rows pass, including the prefix trap `claude-memory-old`.

---

### Task 4: Carry recent notes onto live and finished agents

**Files:**
- Modify: `sdk/types.go` (`Agent.RecentNotes`, below `LastTools`)
- Create: `server/internal/merger/notes.go`
- Test: `server/internal/merger/notes_internal_test.go`
- Modify: `server/internal/merger/merger.go` (field, `buildAgent`, `buildStale` call at line 395)
- Modify: `server/internal/merger/stale.go` (`buildStale`, `buildFinishedAgent`)
- Modify: `server/internal/merger/stale_test.go` (mechanical: every `buildStale(…, 0)` gains `, nil`; one new test)

Five files because `stale.go`/`stale_test.go` are a one-parameter thread-through; the logic lives in `notes.go`.

**Interfaces:**
- Consumes: `parser.NoteTouch`, `parser.NoteWindow`, `SessionData.RecentNotes` (Tasks 1-2), `sdk.NoteTouch` (Task 1)
- Produces:
  - `sdk.Agent.RecentNotes []sdk.NoteTouch` (`json:"recentNotes,omitempty"`)
  - `func WithNotePathFn(fn func(vaultPath string) (string, bool)) merger.Option`
  - `func agentNotes(touches []parser.NoteTouch, notePath func(string) (string, bool), now time.Time) []sdk.NoteTouch`
  - `func (t *staleTracker) buildStale(livePIDs map[int]bool, baselineCost float64, notePath func(string) (string, bool)) []sdk.Agent`

- [ ] **Step 1: Add the Agent field**

In `sdk/types.go`, in `type Agent struct`, directly below the `LastTools` field:

```go
	// RecentNotes are the vault notes this agent read or wrote in the last ten minutes, newest first.
	RecentNotes []NoteTouch `json:"recentNotes,omitempty"`
```

- [ ] **Step 2: Write the failing tests**

Create `server/internal/merger/notes_internal_test.go`:

```go
package merger

import (
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/scanner"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rootRelative(p string) (string, bool) { return strings.CutPrefix(p, "claude-memory/") }

func TestAgentNotes(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	touches := []parser.NoteTouch{
		{Path: "claude-memory/a.md", Kind: sdk.NoteTouchKindRead, At: now.Add(-time.Minute)},
		{Path: "other/b.md", Kind: sdk.NoteTouchKindWrite, At: now.Add(-time.Minute)},
		{Path: "claude-memory/old.md", Kind: sdk.NoteTouchKindRead, At: now.Add(-parser.NoteWindow - time.Second)},
	}

	t.Run("keeps in-window notes under the root, root-relative", func(t *testing.T) {
		assert.Equal(t, []sdk.NoteTouch{
			{Path: "a.md", Kind: sdk.NoteTouchKindRead, At: now.Add(-time.Minute).Format(time.RFC3339)},
		}, agentNotes(touches, rootRelative, now))
	})

	t.Run("no path function means no notes", func(t *testing.T) {
		assert.Nil(t, agentNotes(touches, nil, now))
	})
}

func TestBuildAgentCarriesRecentNotes(t *testing.T) {
	m := New(WithNotePathFn(rootRelative))
	session := &parser.SessionData{
		SessionID:    "s1",
		LastActivity: time.Now(),
		RecentNotes:  []parser.NoteTouch{{Path: "claude-memory/a.md", Kind: sdk.NoteTouchKindRead, At: time.Now()}},
	}
	a := m.buildAgent(scanner.ProcessInfo{PID: 999999, CWD: "/repo/p"}, session, resolveExtra{}, 0)
	require.Len(t, a.RecentNotes, 1)
	assert.Equal(t, "a.md", a.RecentNotes[0].Path)
}
```

In `server/internal/merger/stale_test.go`, change every `tr.buildStale(X, 0)` to `tr.buildStale(X, 0, nil)`:

```bash
sed -i '' -E 's/buildStale\((map\[int\]bool\{[^}]*\}), 0\)/buildStale(\1, 0, nil)/' server/internal/merger/stale_test.go
grep -c 'buildStale(.*, 0, nil)' server/internal/merger/stale_test.go   # expect 9
```

and append:

```go
func TestStaleTracker_FinishedAgentCarriesRecentNotes(t *testing.T) {
	tr := newTestTracker(map[string]*parser.SessionData{
		"/p/s1.jsonl": {
			SessionID:    "s1",
			LastActivity: time.Now(),
			RecentNotes:  []parser.NoteTouch{{Path: "claude-memory/a.md", Kind: sdk.NoteTouchKindWrite, At: time.Now()}},
		},
	})
	tr.record(42, liveSnapshot{sessionID: "s1", path: "/p/s1.jsonl", projectPath: "/proj", provider: sdk.ProviderClaude})

	stale := tr.buildStale(map[int]bool{}, 0, rootRelative)
	if len(stale) != 1 || len(stale[0].RecentNotes) != 1 || stale[0].RecentNotes[0].Path != "a.md" {
		t.Fatalf("want one finished agent carrying a.md, got %+v", stale)
	}
}
```

- [ ] **Step 3: Run them to verify they fail**

Run: `cd server && go test ./internal/merger/ -count=1`
Expected: build FAIL — `undefined: agentNotes`, `undefined: WithNotePathFn`, `too many arguments in call to tr.buildStale`.

- [ ] **Step 4: Implement**

Create `server/internal/merger/notes.go`:

```go
package merger

import (
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// WithNotePathFn sets how a vault-relative note path becomes the graph's
// root-relative form; ok false drops the note. Without it agents carry no notes.
func WithNotePathFn(fn func(vaultPath string) (string, bool)) Option {
	return func(m *Merger) { m.notePath = fn }
}

// agentNotes turns a session's note touches into an agent's recentNotes. The
// parser prunes its window only when the file changes, so an idle session's
// touches must age out here, against now.
func agentNotes(touches []parser.NoteTouch, notePath func(string) (string, bool), now time.Time) []sdk.NoteTouch {
	if notePath == nil {
		return nil
	}
	cutoff := now.Add(-parser.NoteWindow)
	var out []sdk.NoteTouch
	for _, t := range touches {
		if t.At.Before(cutoff) {
			continue
		}
		rel, ok := notePath(t.Path)
		if !ok {
			continue
		}
		out = append(out, sdk.NoteTouch{Path: rel, Kind: t.Kind, At: t.At.Format(time.RFC3339)})
	}
	return out
}
```

In `server/internal/merger/merger.go`:
- add `notePath func(string) (string, bool)` to `type Merger struct`;
- in `buildAgent`'s `sdk.Agent{…}` literal, below `LastTools:`, add
  `RecentNotes: agentNotes(session.RecentNotes, m.notePath, time.Now()),`;
- change line 395 to `for _, s := range m.tracker.buildStale(livePIDs, opts.BaselinePerSessionCostUSD, m.notePath) {`.

In `server/internal/merger/stale.go`:
- `func (t *staleTracker) buildStale(livePIDs map[int]bool, baselineCost float64, notePath func(string) (string, bool)) []sdk.Agent`
- its call becomes `buildFinishedAgent(pid, snap, session, baselineCost, notePath)`;
- `func buildFinishedAgent(pid int, snap liveSnapshot, session *parser.SessionData, baselineCost float64, notePath func(string) (string, bool)) sdk.Agent`, and in its literal below `LastTools:` add
  `RecentNotes: agentNotes(session.RecentNotes, notePath, time.Now()),`.

- [ ] **Step 5: Run them to verify they pass**

Run: `cd server && go test ./internal/merger/ -count=1 -race && go vet ./... && (cd ../sdk && go vet ./...)`
Expected: `ok  github.com/lx-wnk/kontor/server/internal/merger`; vet silent.

- [ ] **Step 6: Commit**

```bash
git checkout -- server/internal/db/ent/
git add sdk/types.go server/internal/merger/notes.go server/internal/merger/notes_internal_test.go server/internal/merger/merger.go server/internal/merger/stale.go server/internal/merger/stale_test.go
git commit -m "feat: carry recent vault notes on live and finished agents"
```

**Acceptance:** live (`buildAgent`) and finished (`buildFinishedAgent`) agents both carry root-relative, in-window notes; a nil path function yields none.

---

### Task 5: Wire the vault root and regenerate the client types

**Files:**
- Modify: `server/serverapp/di.go:287-293`
- Modify: `src/sdk.generated.ts` (generated)
- Modify: `src/types.ts`

**Interfaces:**
- Consumes: `obsidian.RootRelative` (Task 3), `merger.WithNotePathFn` (Task 4)
- Produces (TS): `NoteTouch`, `NoteTouchKind` exported from `@/types`; `Agent.recentNotes?: NoteTouch[]`

- [ ] **Step 1: Wire the merger option**

In `server/serverapp/di.go`, replace the `agentMerger := merger.New(…)` block with:

```go
	noteRoot := settingsSvc.String("obsidian.vaultRoot")
	agentMerger := merger.New(
		merger.WithRegistry(providerRegistry),
		merger.WithScanFn(func(ctx context.Context) ([]scanner.ProcessInfo, error) {
			return scanner.ScanProcessesWithDetector(ctx, providerRegistry)
		}),
		merger.WithScreenProbe(merger.RealScreenProbe),
		merger.WithNotePathFn(func(vaultPath string) (string, bool) {
			return obsidian.RootRelative(noteRoot, vaultPath)
		}),
	)
```

`obsidian` is already imported in `di.go` (`github.com/lx-wnk/kontor/server/internal/apps/obsidian`).

- [ ] **Step 2: Regenerate the TS types (tygo only, not the full `task generate`)**

```bash
$(go env GOPATH)/bin/tygo generate --config tygo.yaml
pnpm eslint --fix src/sdk.generated.ts
git diff --stat src/sdk.generated.ts
git diff src/sdk.generated.ts
```

Expected: the diff only adds `NoteTouchKind`, `NoteTouchKindRead`, `NoteTouchKindWrite`, `NoteTouch` and `recentNotes?: NoteTouch[]` on `Agent`. If it rewrites unrelated lines (line endings, formatting), run `git checkout -- src/sdk.generated.ts` and add exactly those declarations by hand in the style of the surrounding `RecentTool` / `AgentStatus` declarations.

- [ ] **Step 3: Re-export from `src/types.ts`**

Add `NoteTouch` and `NoteTouchKind` to the `import type { … } from './sdk.generated'` list and to the `export type { AgentStatus, HookEvent, PendingPermission, TokenUsage }` line:

```ts
export type { AgentStatus, HookEvent, NoteTouch, NoteTouchKind, PendingPermission, TokenUsage }
```

- [ ] **Step 4: Verify**

Run: `cd server && go build ./... && go vet ./... && cd .. && pnpm typecheck && pnpm lint`
Expected: all exit 0.

- [ ] **Step 5: Commit**

```bash
git checkout -- server/internal/db/ent/
git add server/serverapp/di.go src/sdk.generated.ts src/types.ts
git commit -m "feat: ship recent vault notes to the client"
```

**Acceptance:** the server builds and vets; the client typechecks against `Agent.recentNotes`.

---

### Task 6: Compute hub edges and list rows

**Files:**
- Create: `src/features/hub/hubEdges.ts`
- Test: `src/features/hub/hubEdges.test.ts`

**Interfaces:**
- Consumes: `Agent`, `NoteTouch`, `NoteTouchKind` from `@/types` (Task 5); `HubNote` from `./composables/useObsidianGraph`
- Produces:
  - `EDGE_WINDOW_MS = 600_000`
  - `interface HubEdge { from: [number, number], to: number, kind: NoteTouchKind, alpha: number }`
  - `interface AgentNoteRow { path: string, title: string, kind: NoteTouchKind, at: string }`
  - `edgeAlpha(ageMs: number): number`
  - `liveEdges(placed: ReadonlyArray<{ agent: Agent, x: number, y: number }>, drawn: ReadonlySet<number>, byPath: ReadonlyMap<string, HubNote>, now: number): HubEdge[]`
  - `agentNoteRows(agent: Agent, byPath: ReadonlyMap<string, HubNote>, now: number): AgentNoteRow[]`

- [ ] **Step 1: Write the failing test**

Create `src/features/hub/hubEdges.test.ts`:

```ts
import type { HubNote } from './composables/useObsidianGraph'
import type { Agent } from '@/types'
import { describe, expect, it } from 'vitest'
import { agentNoteRows, EDGE_WINDOW_MS, edgeAlpha, liveEdges } from './hubEdges'

const NOW = Date.parse('2026-09-23T12:00:00Z')
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString()
const note = (index: number, path: string): HubNote => ({ index, path, title: path.replace(/\.md$/, ''), mtimeMs: 0, links: [], backlinks: [] })
const BY_PATH = new Map([note(0, 'a.md'), note(1, 'b.md')].map(n => [n.path, n]))
const agent = (pid: number, recentNotes: Agent['recentNotes']) => ({ pid, recentNotes }) as Agent

describe('liveEdges', () => {
  it('draws one edge per touched note on the map, from the agent to the note', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'read', at: iso(0) }, { path: 'b.md', kind: 'write', at: iso(60_000) }])
    expect(liveEdges([{ agent: a, x: 10, y: 20 }], new Set([1]), BY_PATH, NOW)).toEqual([
      { from: [10, 20], to: 0, kind: 'read', alpha: 0.9 },
      { from: [10, 20], to: 1, kind: 'write', alpha: expect.closeTo(0.83, 5) },
    ])
  })

  it('draws nothing for an agent the rail hides', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'read', at: iso(0) }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set(), BY_PATH, NOW)).toEqual([])
  })

  it('drops a note that left the graph, and draws nothing while the graph is still empty', () => {
    const a = agent(1, [{ path: 'gone.md', kind: 'read', at: iso(0) }, { path: 'a.md', kind: 'read', at: iso(0) }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), BY_PATH, NOW).map(e => e.to)).toEqual([0])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), new Map(), NOW)).toEqual([])
  })

  it('drops an expired or unreadable timestamp', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'read', at: iso(EDGE_WINDOW_MS + 1) }, { path: 'b.md', kind: 'read', at: 'not-a-date' }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), BY_PATH, NOW)).toEqual([])
  })

  it('draws a touch stamped slightly in the future as fresh', () => {
    const a = agent(1, [{ path: 'a.md', kind: 'write', at: iso(-5_000) }])
    expect(liveEdges([{ agent: a, x: 0, y: 0 }], new Set([1]), BY_PATH, NOW)[0].alpha).toBe(0.9)
  })

  it('handles an agent without recentNotes', () => {
    expect(liveEdges([{ agent: agent(1, undefined), x: 0, y: 0 }], new Set([1]), BY_PATH, NOW)).toEqual([])
  })
})

describe('edgeAlpha', () => {
  it('fades linearly from 0.9 to 0.2 across the window', () => {
    expect(edgeAlpha(0)).toBe(0.9)
    expect(edgeAlpha(EDGE_WINDOW_MS / 2)).toBeCloseTo(0.55, 5)
    expect(edgeAlpha(EDGE_WINDOW_MS)).toBeCloseTo(0.2, 5)
  })
})

describe('agentNoteRows', () => {
  it('names each touched note on the map with its title, kind and time', () => {
    const at = iso(0)
    const a = agent(1, [{ path: 'b.md', kind: 'write', at }, { path: 'gone.md', kind: 'read', at }])
    expect(agentNoteRows(a, BY_PATH, NOW)).toEqual([{ path: 'b.md', title: 'b', kind: 'write', at }])
  })
})
```

- [ ] **Step 2: Run it to verify it fails**

Run: `pnpm vitest run src/features/hub/hubEdges.test.ts`
Expected: FAIL — `Failed to resolve import "./hubEdges"`.

- [ ] **Step 3: Implement**

Create `src/features/hub/hubEdges.ts`:

```ts
import type { HubNote } from './composables/useObsidianGraph'
import type { Agent, NoteTouch, NoteTouchKind } from '@/types'

export const EDGE_WINDOW_MS = 10 * 60_000
const ALPHA_FRESH = 0.9
const ALPHA_OLD = 0.2

export interface HubEdge { from: [number, number], to: number, kind: NoteTouchKind, alpha: number }
export interface AgentNoteRow { path: string, title: string, kind: NoteTouchKind, at: string }

export function edgeAlpha(ageMs: number): number {
  const t = Math.min(Math.max(ageMs / EDGE_WINDOW_MS, 0), 1)
  return ALPHA_FRESH + (ALPHA_OLD - ALPHA_FRESH) * t
}

// The server applies the window too; this re-applies it on the client's clock so an edge still
// expires between SSE ticks. An unparsable `at` yields NaN, which fails the comparison and drops.
function notesOnMap(agent: Agent, byPath: ReadonlyMap<string, HubNote>, now: number) {
  return (agent.recentNotes ?? []).flatMap((touch: NoteTouch) => {
    const note = byPath.get(touch.path)
    const ageMs = now - Date.parse(touch.at)
    return note && ageMs <= EDGE_WINDOW_MS ? [{ touch, note, ageMs }] : []
  })
}

export function liveEdges(
  placed: ReadonlyArray<{ agent: Agent, x: number, y: number }>,
  drawn: ReadonlySet<number>,
  byPath: ReadonlyMap<string, HubNote>,
  now: number,
): HubEdge[] {
  return placed
    .filter(p => drawn.has(p.agent.pid))
    .flatMap(p => notesOnMap(p.agent, byPath, now).map(({ touch, note, ageMs }): HubEdge => ({
      from: [p.x, p.y],
      to: note.index,
      kind: touch.kind,
      alpha: edgeAlpha(ageMs),
    })))
}

export function agentNoteRows(agent: Agent, byPath: ReadonlyMap<string, HubNote>, now: number): AgentNoteRow[] {
  return notesOnMap(agent, byPath, now).map(({ touch, note }) => ({ path: touch.path, title: note.title, kind: touch.kind, at: touch.at }))
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `pnpm vitest run src/features/hub/hubEdges.test.ts && pnpm typecheck && pnpm lint`
Expected: all tests pass; typecheck and lint exit 0.

- [ ] **Step 5: Commit**

```bash
git add src/features/hub/hubEdges.ts src/features/hub/hubEdges.test.ts
git commit -m "feat: derive hub edges and list rows from agents' recent notes"
```

**Acceptance:** every case above passes, including clock skew, the empty graph and a missing `recentNotes`.

---

### Task 7: Draw the edges on the hub canvas

**Files:**
- Modify: `src/features/hub/components/HubBrainCanvas.vue`
- Test: `src/features/hub/__tests__/HubBrainCanvas.test.ts`
- Modify: `src/features/hub/components/HubWidget.vue`

**Interfaces:**
- Consumes: `HubEdge`, `liveEdges` (Task 6)
- Produces: `HubBrainCanvas` prop `edges: ReadonlyArray<HubEdge>`; in `HubWidget.vue` the computeds `notesByPath`, `edgeNow`, `edges` (Task 8 reuses `notesByPath` and `edgeNow`)

- [ ] **Step 1: Write the failing tests**

In `src/features/hub/__tests__/HubBrainCanvas.test.ts`, add `edges: [],` to the default props in `mountBrain` (next to `selected: null,`) and append inside `describe('hubBrainCanvas', …)`:

```ts
  it('strokes read edges dashed and write edges dotted, faded by age, then resets the dash', async () => {
    mountBrain({ edges: [
      { from: [0, 0], to: 1, kind: 'read', alpha: 0.9 },
      { from: [0, 0], to: 2, kind: 'write', alpha: 0.5 },
    ] })
    await nextFrame()
    expect(named('setLineDash').map(c => c.args[0])).toEqual([[4, 3], [1, 3], []])
    const lines = named('lineTo')
    expect(lines.map(c => c.state.globalAlpha)).toEqual([0.9, 0.5])
    expect(lines.map(c => c.state.lineCap)).toEqual(['butt', 'round'])
  })

  it('touches no dash state when there are no edges', async () => {
    mountBrain()
    await nextFrame()
    expect(named('setLineDash')).toHaveLength(0)
  })
```

- [ ] **Step 2: Run them to verify they fail**

Run: `pnpm vitest run src/features/hub/__tests__/HubBrainCanvas.test.ts`
Expected: FAIL — the first new test sees no `setLineDash` calls.

- [ ] **Step 3: Implement `drawEdges`**

In `HubBrainCanvas.vue`:

- import: `import type { HubEdge } from '../hubEdges'` and `import type { NoteTouchKind } from '@/types'`;
- add the prop `edges: ReadonlyArray<HubEdge>` to `defineProps`;
- add constants next to `LINK_WIDTH_PX`:

```ts
const EDGE_WIDTH_PX = 1.2
const EDGE_DASH: Record<NoteTouchKind, number[]> = { read: [4, 3], write: [1, 3] }
```

- add the pass after `drawLinks`:

```ts
function drawEdges({ ctx, screen, token }: Scene) {
  if (props.edges.length === 0)
    return
  ctx.strokeStyle = token('--accent')
  ctx.lineWidth = EDGE_WIDTH_PX
  for (const edge of props.edges) {
    ctx.globalAlpha = edge.alpha
    ctx.setLineDash(EDGE_DASH[edge.kind])
    ctx.lineCap = edge.kind === 'write' ? 'round' : 'butt'
    ctx.beginPath()
    ctx.moveTo(...toScreen(props.cam, ...edge.from))
    ctx.lineTo(...screen[edge.to])
    ctx.stroke()
  }
  ctx.setLineDash([])
  ctx.lineCap = 'butt'
}
```

- in `draw()`, call `drawEdges(scene)` directly after `drawLinks(scene)`.

- [ ] **Step 4: Wire it in `HubWidget.vue`**

- imports: add `useNow` to the `@vueuse/core` import (add the import line if the file has none), and `import { liveEdges } from '../hubEdges'`;
- directly after the `drawnAgents` computed, add:

```ts
const EDGE_CLOCK_MS = 30_000
// Edges fade and expire on this clock too: an idle agent sends no SSE tick to redraw them.
const edgeNow = useNow({ interval: EDGE_CLOCK_MS })
const notesByPath = computed(() => new Map(vaultNotes.value.map(n => [n.path, n])))
const edges = computed(() => liveEdges(placed.value, drawnAgents.value, notesByPath.value, edgeNow.value.getTime()))
```

- bind `:edges="edges"` on `<HubBrainCanvas …>` (after `:links`).

- [ ] **Step 5: Run the hub tests to verify they pass**

Run: `pnpm vitest run src/features/hub && pnpm typecheck && pnpm lint`
Expected: all hub tests pass (including `HubWidget.test.ts`); typecheck and lint exit 0.

- [ ] **Step 6: Commit**

```bash
git add src/features/hub/components/HubBrainCanvas.vue src/features/hub/__tests__/HubBrainCanvas.test.ts src/features/hub/components/HubWidget.vue
git commit -m "feat: draw live edges from agents to the notes they touched"
```

**Acceptance:** read and write edges are stroked with their own dash and cap at their own alpha; no dash state changes without edges.

---

### Task 8: List each agent's notes in the list view

**Files:**
- Modify: `src/features/hub/components/HubList.vue`
- Test: `src/features/hub/__tests__/HubList.test.ts`
- Modify: `src/features/hub/components/HubWidget.vue`

**Interfaces:**
- Consumes: `AgentNoteRow`, `agentNoteRows` (Task 6); `notesByPath`, `edgeNow` in `HubWidget.vue` (Task 7)
- Produces: `HubList` prop `agents: ReadonlyArray<{ agent: Agent, state: AgentDisplayStatus, notes?: ReadonlyArray<AgentNoteRow> }>`; rows `data-testid="hub-list-agent-note"`

- [ ] **Step 1: Write the failing test**

Append inside `describe('hubList', …)` in `src/features/hub/__tests__/HubList.test.ts`:

```ts
  it('lists each agent\'s recent notes under it, and picking one emits its path', async () => {
    const at = new Date().toISOString()
    const w = mount(HubList, {
      attachTo: document.body,
      props: {
        agents: [{ agent: a1, state: 'working' as const, notes: [{ path: 'work/plan.md', title: 'plan', kind: 'write' as const, at }] }],
        notes: [],
        graphStatus: 'ready' as GraphStatus,
        graphMessage: '',
        launchers,
      },
      global: { provide: { [OPEN_SETTINGS]: vi.fn() } },
    })
    const rows = w.findAll('[data-testid="hub-list-agent-note"]')
    expect(rows).toHaveLength(1)
    expect(rows[0].attributes('aria-label')).toMatch(/^plan, wrote /)
    await rows[0].trigger('click')
    expect(w.emitted('note')).toEqual([['work/plan.md']])
    w.unmount()
  })
```

- [ ] **Step 2: Run it to verify it fails**

Run: `pnpm vitest run src/features/hub/__tests__/HubList.test.ts`
Expected: FAIL — `expected [] to have a length of 1`.

- [ ] **Step 3: Implement the rows**

In `HubList.vue`:

- imports: `import type { AgentNoteRow } from '../hubEdges'` and `import type { NoteTouchKind } from '@/types'`;
- the `agents` prop becomes `ReadonlyArray<{ agent: Agent, state: AgentDisplayStatus, notes?: ReadonlyArray<AgentNoteRow> }>`;
- add below `ROW`:

```ts
const TOUCH_VERB: Record<NoteTouchKind, string> = { read: 'read', write: 'wrote' }
```

- replace the agent `<button v-for="{ agent, state } in agents" …>…</button>` with:

```vue
    <template v-for="{ agent, state, notes: agentNotes } in agents" :key="agent.pid">
      <button
        type="button"
        data-testid="hub-list-agent"
        :aria-label="`${friendlyProjectName(agent.projectName)}, ${statusLabel(state)}`"
        :class="ROW"
        @click="emit('agent', agent)"
      >
        <span>{{ friendlyProjectName(agent.projectName) }}</span>
        <AppChip :tone="agentStatusTone(state)">
          {{ statusLabel(state) }}
        </AppChip>
      </button>
      <button
        v-for="n in agentNotes ?? []"
        :key="`${n.kind}:${n.path}`"
        type="button"
        data-testid="hub-list-agent-note"
        :aria-label="`${n.title}, ${TOUCH_VERB[n.kind]} ${formatRelativeThenDate(n.at)}`"
        :class="`${ROW} pl-6`"
        @click="emit('note', n.path)"
      >
        <span>{{ n.title }}</span>
        <span class="text-fg-mute">{{ TOUCH_VERB[n.kind] }} · {{ formatRelativeThenDate(n.at) }}</span>
      </button>
    </template>
```

- [ ] **Step 4: Feed it from `HubWidget.vue`**

- import `agentNoteRows` alongside `liveEdges` from `'../hubEdges'`;
- below the `edges` computed:

```ts
const listAgents = computed(() => placed.value.map(p => ({ ...p, notes: agentNoteRows(p.agent, notesByPath.value, edgeNow.value.getTime()) })))
```

- on `<HubList …>`, change `:agents="placed"` to `:agents="listAgents"`.

`@note` already routes to `pickNoteFromList(path)`, which closes the list and flies to the note.

- [ ] **Step 5: Run the hub tests to verify they pass**

Run: `pnpm vitest run src/features/hub && pnpm typecheck && pnpm lint`
Expected: all hub tests pass; typecheck and lint exit 0.

- [ ] **Step 6: Commit**

```bash
git add src/features/hub/components/HubList.vue src/features/hub/__tests__/HubList.test.ts src/features/hub/components/HubWidget.vue
git commit -m "feat: list each agent's recent notes in the hub list view"
```

**Acceptance:** each agent row is followed by focusable note rows naming title, verb and time; clicking one emits its path.

---

### Task 9: End-to-end check and docs

**Files:**
- Modify: `tests/e2e/hub.spec.ts`
- Modify: `CHANGELOG.md`
- Modify: `README.md:38`

- [ ] **Step 1: Write the E2E test**

Append to `tests/e2e/hub.spec.ts`:

```ts
test('L lists the notes an agent touched under that agent, and only notes on the map', async ({ page }) => {
  const at = new Date().toISOString()
  const agents = [{
    ...fakeAgents()[0],
    recentNotes: [
      { path: 'Work/note-0.md', kind: 'read', at },
      { path: 'Private/note-1.md', kind: 'write', at },
      { path: 'Elsewhere/unknown.md', kind: 'read', at },
    ],
  }]
  await page.route('**/api/obsidian/graph', route => route.fulfill({ json: fakeGraph() }))
  await page.route('/api/agents', route => route.fulfill({ json: agents }))
  await page.route('/api/agents/stream', route => route.fulfill({
    status: 200,
    contentType: 'text/event-stream',
    body: `data: ${JSON.stringify({ agents })}\n\n`,
  }))
  await page.goto('/', { waitUntil: 'domcontentloaded' })

  await page.getByTestId('hub-stage').press('l')
  const rows = page.getByRole('dialog', { name: 'Zentrale as a list' }).getByTestId('hub-list-agent-note')
  await expect(rows).toHaveCount(2)
  await expect(rows.nth(0)).toHaveAttribute('aria-label', /^note-0, read /)
  await expect(rows.nth(1)).toHaveAttribute('aria-label', /^note-1, wrote /)
})
```

- [ ] **Step 2: Run it**

Run (nothing else running Go tests at the same time): `pnpm test:e2e tests/e2e/hub.spec.ts`
Expected: all `hub.spec.ts` tests pass, the new one included.

- [ ] **Step 3: Update the docs**

`CHANGELOG.md`, first `### Added` under `## [Unreleased]`, as its first bullet:

```markdown
- **Live edges in the hub.** A dashed line runs from an agent to each vault
  note it read in the last ten minutes, a dotted one to each note it wrote,
  fading as the touch ages. The server reads them from the agent's Obsidian MCP
  calls and its `curl …/vault/…` commands (a path still holding a shell
  variable is skipped, never guessed) and ships them as `recentNotes` on each
  agent; the `L` list names the same notes under each agent.
```

`README.md`, in the **The hub** paragraph (line 38), insert before `The brain needs an Obsidian vault configured`:

```markdown
Dashed and dotted lines connect an agent to the notes it read or wrote in the last ten minutes, read from its Obsidian MCP calls and `curl …/vault/…` commands, and fade as the touch ages; the `L` list names the same notes under each agent.
```

- [ ] **Step 4: Commit**

```bash
git checkout HEAD -- server/frontend/dist/.gitkeep
git checkout -- server/internal/db/ent/
git add tests/e2e/hub.spec.ts CHANGELOG.md README.md
git commit -m "test: cover live-edge notes in the hub list view"
```

**Acceptance:** the E2E test passes in a real browser; README and CHANGELOG describe the feature.

---

### Task 10: Full gates and in-app verification

**Files:** none (evidence only)

- [ ] **Step 1: Run all four gates, one after another**

```bash
pnpm lint; echo "lint=$?"
pnpm typecheck; echo "typecheck=$?"
pnpm test; echo "test=$?"
task test; echo "task-test=$?"
(cd sdk && go vet ./...); echo "vet-sdk=$?"
(cd server && go vet ./...); echo "vet-server=$?"
task lint; echo "task-lint=$?"
git checkout -- server/internal/db/ent/
pnpm test:e2e; echo "e2e=$?"
git checkout HEAD -- server/frontend/dist/.gitkeep
```

Expected: every `…=0`. Paste the raw output.

- [ ] **Step 2: Serve the new build**

```bash
lsof -nP -iTCP:13120 -sTCP:LISTEN     # note the PID(s); stop exactly those, never a pattern-wide pkill
task build:all
stat -f '%Sm %N' server/frontend/dist/index.html bin/* 2>/dev/null
```

Start the freshly built server (or `task dev`) and confirm `curl -s http://127.0.0.1:13120/api/system/health` answers from the new process (its PID differs from the one stopped above).

- [ ] **Step 3: Produce a real touch and look at it**

From a Claude session on this machine, read an existing note and write a dedicated probe note:

```bash
curl -sk -H "Authorization: Bearer $OBSIDIAN_API_KEY" "$OBSIDIAN_BASE_URL/vault/${OBSIDIAN_ROOT:-claude-memory}/private/agent-dashboard/sessions/_index.md" >/dev/null
curl -sk -X PUT -H "Authorization: Bearer $OBSIDIAN_API_KEY" -H "Content-Type: text/markdown" --data "live-edge probe" "$OBSIDIAN_BASE_URL/vault/${OBSIDIAN_ROOT:-claude-memory}/misc/live-edge-probe.md"
```

Open the Zentrale, find that session's agent in the hub, and confirm a dashed edge to `_index` and a dotted edge to `live-edge-probe`; press `L` and confirm both rows under the agent. Screenshot the hub. Check again after ~5 minutes that the edges have visibly faded. Then delete the probe note:

```bash
curl -sk -X DELETE -H "Authorization: Bearer $OBSIDIAN_API_KEY" "$OBSIDIAN_BASE_URL/vault/${OBSIDIAN_ROOT:-claude-memory}/misc/live-edge-probe.md"
```

- [ ] **Step 4: Push**

```bash
git push origin develop
git log origin/develop..HEAD --oneline   # must print nothing
```

**Acceptance:** all gates green with pasted output; the screenshot shows both edges on the real vault; `develop` pushed.
