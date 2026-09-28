package merger

import (
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/scanner"
)

func TestBuildAgent_WorkingFromTurnOpen(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	m := New()
	open := &parser.SessionData{SessionID: "s1", LastActivity: time.Now(), TurnOpen: true}
	a := m.buildAgent(scanner.ProcessInfo{PID: 1, CWD: "/p", Provider: sdk.ProviderClaude}, open, resolveExtra{}, 0)
	if !a.Working {
		t.Error("TurnOpen → agent.Working must be true")
	}
	done := &parser.SessionData{SessionID: "s2", LastActivity: time.Now(), TurnOpen: false}
	b := m.buildAgent(scanner.ProcessInfo{PID: 2, CWD: "/p", Provider: sdk.ProviderClaude}, done, resolveExtra{}, 0)
	if b.Working {
		t.Error("closed turn (no B signal here) → agent.Working must be false")
	}
}
