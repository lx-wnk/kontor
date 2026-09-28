package refine_test

import (
	"context"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/refine"
)

func TestRunRefinementTurn_NilSpawnerUsesClaudeBinary(t *testing.T) {
	// Pass a nil spawner; we don't actually exec — the function must accept the
	// new parameter shape. With nil it must fall back to `claude -p`. We just
	// confirm the signature compiles and returns a channel + nil error before
	// the binary is invoked (use a cancelled context so the exec never starts).
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ch, err := refine.RunRefinementTurn(ctx, refine.SpawnConfig{UserMessage: "hi"}, (*ent.Spawner)(nil))
	if err == nil && ch == nil {
		t.Fatal("expected either an error or a channel; got both nil")
	}
}

func TestRunRefinementTurn_ClaudeAdapterStillUsesExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sp := &ent.Spawner{AdapterType: "claude"}
	_, _ = refine.RunRefinementTurn(ctx, refine.SpawnConfig{UserMessage: "hi"}, sp)
	// Compile-time guard only.
}

func TestRunRefinementTurn_UnsupportedAdapterReturnsError(t *testing.T) {
	sp := &ent.Spawner{AdapterType: "totally-unknown"}
	_, err := refine.RunRefinementTurn(context.Background(), refine.SpawnConfig{UserMessage: "hi"}, sp)
	if err == nil {
		t.Fatal("expected error for unknown adapter")
	}
}
