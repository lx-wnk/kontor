package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Large attachment entries can push every assistant entry out of the 32 KB tail.
// LastActivity must then come from the file's mtime, not a made-up time relative to now.
func TestParse_LastActivityFallsBackToMtimeWithoutAssistantInTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	attachment := `{"type":"attachment","timestamp":"2026-10-20T12:51:20.276Z","attachment":"` + strings.Repeat("x", tailBytes) + `"}` + "\n"
	lines := `{"type":"assistant","timestamp":"2026-10-20T12:51:20.253Z","message":{"role":"assistant","content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}}` + "\n" + attachment
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Date(2026, 10, 20, 12, 51, 21, 0, time.UTC)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}

	data, err := ParseSessionFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !data.LastActivity.Equal(mtime) {
		t.Fatalf("LastActivity = %v, want the file mtime %v", data.LastActivity, mtime)
	}
}

// A session whose activity is only known from its mtime must not win the
// content match over an older file with a real assistant reply: another
// process in the same folder may own it.
func TestFindSessionByContent_PrefersAssistantTimestampOverMtime(t *testing.T) {
	dir := t.TempDir()
	recent := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339Nano)
	owned := filepath.Join(dir, "owned.jsonl")
	if err := os.WriteFile(owned, []byte(`{"type":"assistant","timestamp":"`+recent+`","message":{"role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "foreign.jsonl")
	if err := os.WriteFile(foreign, []byte(`{"type":"user","message":{"role":"user","content":"start"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	candidates := []sessionFileCandidate{{path: foreign}, {path: owned}}
	_, path, err := findSessionByContent(candidates, 3600, dir)
	if err != nil {
		t.Fatal(err)
	}
	if path != owned {
		t.Fatalf("picked %s, want the file with an assistant reply %s", filepath.Base(path), filepath.Base(owned))
	}
}
