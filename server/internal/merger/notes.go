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

// The parser prunes only when the file changes, so an idle session's touches age out here.
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
