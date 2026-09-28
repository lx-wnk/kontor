package fakespawn

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/sdk"
)

func TestSpawnWritesArtifacts(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{})

	if _, err := os.Stat(a.JSONLPath); err != nil {
		t.Fatalf("JSONL not written: %v", err)
	}
	if _, err := os.Stat(s.DiscoveryPath(a.PID)); err != nil {
		t.Fatalf("discovery file not written: %v", err)
	}

	procs, err := s.ScanFn()(context.Background())
	if err != nil {
		t.Fatalf("ScanFn error: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("want 1 process, got %d", len(procs))
	}
	if procs[0].PID != a.PID {
		t.Errorf("PID: want %d, got %d", a.PID, procs[0].PID)
	}
	if procs[0].CWD != a.CWD {
		t.Errorf("CWD: want %q, got %q", a.CWD, procs[0].CWD)
	}
	if procs[0].Provider != sdk.ProviderClaude {
		t.Errorf("Provider: want %q, got %q", sdk.ProviderClaude, procs[0].Provider)
	}
}

func TestSpawnNoChannel(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{NoChannel: true})

	if _, err := os.Stat(s.DiscoveryPath(a.PID)); !os.IsNotExist(err) {
		t.Fatalf("discovery file should not exist, stat err: %v", err)
	}
	if _, err := os.Stat(a.JSONLPath); err != nil {
		t.Fatalf("JSONL not written: %v", err)
	}

	procs, err := s.ScanFn()(context.Background())
	if err != nil {
		t.Fatalf("ScanFn error: %v", err)
	}
	if len(procs) != 1 {
		t.Fatalf("want 1 process, got %d", len(procs))
	}
}

func TestSpawnPtyWritesBothFiles(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{Pty: true})

	if _, err := os.Stat(s.DiscoveryPtyPath(a.PID)); err != nil {
		t.Errorf("pty file should be written: %v", err)
	}
	if _, err := os.Stat(s.DiscoveryPath(a.PID)); err != nil {
		t.Errorf("bridge file should be written: %v", err)
	}
}

func TestSpawnPtyOnly(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{NoChannel: true, Pty: true})

	if _, err := os.Stat(s.DiscoveryPtyPath(a.PID)); err != nil {
		t.Errorf("pty file should be written: %v", err)
	}
	if _, err := os.Stat(s.DiscoveryPath(a.PID)); !os.IsNotExist(err) {
		t.Errorf("bridge file should not exist, stat err: %v", err)
	}
}

func TestSpawnLiveInjectable(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{LiveInjectable: true})

	data, err := os.ReadFile(s.DiscoveryPath(a.PID))
	if err != nil {
		t.Fatalf("read discovery: %v", err)
	}
	if !strings.Contains(string(data), "tmuxPane") {
		t.Errorf("discovery file missing tmuxPane: %s", data)
	}
}

func TestExitLeavesDiscoveryFile(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{})

	s.Exit(a.PID)

	procs, err := s.ScanFn()(context.Background())
	if err != nil {
		t.Fatalf("ScanFn error: %v", err)
	}
	if len(procs) != 0 {
		t.Fatalf("want 0 processes after exit, got %d", len(procs))
	}
	if _, err := os.Stat(s.DiscoveryPath(a.PID)); err != nil {
		t.Errorf("discovery file should remain after exit: %v", err)
	}
}

func TestDismissRemovesDiscoveryFile(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{Pty: true})

	if err := s.Dismiss(a.PID); err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if _, err := os.Stat(s.DiscoveryPath(a.PID)); !os.IsNotExist(err) {
		t.Errorf("discovery file should be removed, stat err: %v", err)
	}
	if _, err := os.Stat(s.DiscoveryPtyPath(a.PID)); !os.IsNotExist(err) {
		t.Errorf("pty file should be removed, stat err: %v", err)
	}
}

func TestSpawnDistinctPIDsAndSessions(t *testing.T) {
	s := New(t)
	a := s.Spawn(SpawnOpts{})
	b := s.Spawn(SpawnOpts{})

	if a.PID == b.PID {
		t.Errorf("expected distinct PIDs, both %d", a.PID)
	}
	if a.SessionID == b.SessionID {
		t.Errorf("expected distinct session IDs, both %q", a.SessionID)
	}
}
