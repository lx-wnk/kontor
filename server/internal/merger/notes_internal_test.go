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
