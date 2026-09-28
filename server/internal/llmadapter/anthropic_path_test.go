package llmadapter

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func TestResolveAnthropicSpawnerPath_FromEnv(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "anthropic-spawner")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DASHBOARD_ANTHROPIC_SPAWNER_CMD", bin)
	got, err := resolveAnthropicSpawnerPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != bin {
		t.Fatalf("want %q, got %q", bin, got)
	}
}

func TestResolveAnthropicSpawnerPath_Unset(t *testing.T) {
	t.Setenv("DASHBOARD_ANTHROPIC_SPAWNER_CMD", "")
	t.Setenv("PATH", t.TempDir())
	if _, err := resolveAnthropicSpawnerPath(); err == nil {
		t.Fatal("expected error when binary is unresolvable")
	}
}

func TestFactory_AnthropicAdapter(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "anthropic-spawner")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DASHBOARD_ANTHROPIC_SPAWNER_CMD", bin)
	sp, err := NewLLMSpawnerFromSpawner(&ent.Spawner{AdapterType: "anthropic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	cc, ok := sp.(*CustomCommandSpawner)
	if !ok {
		t.Fatalf("anthropic adapter must be a CustomCommandSpawner, got %T", sp)
	}
	if cc.Command != bin {
		t.Fatalf("want command %q, got %q", bin, cc.Command)
	}
}
