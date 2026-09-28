package merger

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

func writeSubagentFixture(t *testing.T, dir, name string, ts time.Time) {
	t.Helper()
	line := fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","id":"t1"}],"usage":{"input_tokens":1}}}`,
		ts.UTC().Format(time.RFC3339Nano),
	)
	if err := os.WriteFile(filepath.Join(dir, name), []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

// TestBuildSubagents_OrdersActiveFirstThenRecent pins the ordering contract:
// active subagents come first, then completed ones by most recent activity.
// Filenames are chosen so lexical order (the os.ReadDir default) contradicts
// both status and recency, so a passing test rules out directory order as a
// coincidental match.
func TestBuildSubagents_OrdersActiveFirstThenRecent(t *testing.T) {
	tmp := t.TempDir()
	subDir := filepath.Join(tmp, "sess1", "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	now := time.Now()
	writeSubagentFixture(t, subDir, "agent-aaa-recent-completed.jsonl", now.Add(-2*time.Minute))
	writeSubagentFixture(t, subDir, "agent-mmm-older-completed.jsonl", now.Add(-10*time.Minute))
	writeSubagentFixture(t, subDir, "agent-zzz-active.jsonl", now.Add(-5*time.Second))

	session := &parser.SessionData{
		SessionID: "sess1",
		Path:      filepath.Join(tmp, "parent.jsonl"),
	}

	out := buildSubagents(session)

	want := []string{
		"agent-zzz-active",
		"agent-aaa-recent-completed",
		"agent-mmm-older-completed",
	}
	if len(out) != len(want) {
		t.Fatalf("got %d subagents, want %d: %+v", len(out), len(want), out)
	}
	for i, id := range want {
		if out[i].ID != id {
			t.Errorf("position %d: got %q, want %q (full order: %v)", i, out[i].ID, id, subagentIDs(out))
		}
	}
	if out[0].Status != sdk.SubAgentStatusActive {
		t.Errorf("position 0 status = %s, want active", out[0].Status)
	}
}

func subagentIDs(subagents []sdk.SubAgent) []string {
	out := make([]string, len(subagents))
	for i, sa := range subagents {
		out[i] = sa.ID
	}
	return out
}
