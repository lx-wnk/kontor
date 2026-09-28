package parser

import (
	"encoding/json"
	"fmt"
	"strings"
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
		{"a backslash continuation keeps the method with its URL", "curl -sk -X PUT \\\n  \"$B/vault/claude-memory/work/x.md\"", []NoteTouch{touch("claude-memory/work/x.md", write, zero)}},
		{"a write through a variable assigned earlier is a write", `F="$OBSIDIAN_BASE_URL/vault/${OBSIDIAN_ROOT:-claude-memory}/private/kontor/sessions/hub.md"; curl -sk -X PUT --data-binary @n.md "$F"`,
			[]NoteTouch{touch("claude-memory/private/kontor/sessions/hub.md", write, zero)}},
		{"a variable built from another resolves", "ROOT=\"${OBSIDIAN_ROOT:-claude-memory}\"\nBASE=\"$OBSIDIAN_BASE_URL/vault/$ROOT/private/sessions\"\ncurl -sk \"${BASE}/_index.md\"",
			[]NoteTouch{touch("claude-memory/private/sessions/_index.md", read, zero)}},
		{"an assignment alone touches nothing", `URL="$B/vault/claude-memory/x.md"`, nil},
		{"a single-quoted value stays literal", `R='$HOME'; curl "$B/vault/$R/x.md"`, nil},
		{"an unassigned variable is still dropped", `F="$B/vault/claude-memory/x.md"; curl "$G"`, nil},
		{"a path over the length cap is dropped", `curl "$B/vault/` + strings.Repeat("a", 600) + `.md"`, nil},
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
		{"a path over the length cap is dropped", toolMessage(t, at, "mcp__obsidian__obsidian_read_note", map[string]string{"path": strings.Repeat("a", 600) + ".md"}), nil},
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
