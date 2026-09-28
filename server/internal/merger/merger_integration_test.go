// Package merger_test provides integration tests for the merger package.
package merger_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/provider"
	"github.com/lx-wnk/kontor/server/internal/scanner"
	"github.com/lx-wnk/kontor/server/internal/testsupport/fakespawn"
)

// TestGetAgents_DoesNotPanic verifies that GetAgents does not panic when called
// in an environment with no running Claude processes.
// It may return an error (e.g. scanner not available) or an empty slice — both are valid.
func TestGetAgents_DoesNotPanic(t *testing.T) {
	agents, err := merger.New().GetAgents(context.Background(), merger.GetAgentsOpts{})
	if err != nil {
		// Scanner may fail in CI — acceptable.
		t.Logf("GetAgents returned error (acceptable in CI): %v", err)
		return
	}
	// If no error, the result must be a valid (possibly empty) slice of the correct type.
	require.NotNil(t, agents)
	assert.IsType(t, []sdk.Agent{}, agents)
}

// fixedScanFn returns a scan override that yields a fixed process list.
func fixedScanFn(procs []scanner.ProcessInfo) func(ctx context.Context) ([]scanner.ProcessInfo, error) {
	return func(ctx context.Context) ([]scanner.ProcessInfo, error) {
		return procs, nil
	}
}

// codexRegistry builds a registry with codex enabled, resolving sessions under
// the given CODEX_HOME root.
func codexRegistry(t *testing.T) *provider.Registry {
	t.Helper()
	reg, err := provider.NewRegistry(provider.Options{
		Ollama:  provider.NewOllamaClassifier("http://127.0.0.1:1"),
		Pricing: merger.PricingAdapter(),
	})
	require.NoError(t, err)
	reg.SetEnabled(provider.DefaultEnabled(reg.Descriptors(), []string{"codex"}))
	return reg
}

// TestGetAgents_CodexProcess_NoFiles verifies a Codex process with no session
// JSONL under its config dir produces zero agents and no error.
func TestGetAgents_CodexProcess_NoFiles(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome) // exists but has no sessions/ files

	m := merger.New(
		merger.WithRegistry(codexRegistry(t)),
		merger.WithScanFn(fixedScanFn([]scanner.ProcessInfo{
			{PID: 4242, CWD: "/some/project", Uptime: 30, Provider: sdk.ProviderCodex},
		})),
	)

	agents, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	assert.Empty(t, agents, "no JSONL under codex dir must yield zero agents")
}

// TestGetAgents_CodexProcess_WithJSONL verifies a Codex process with a session
// JSONL under its config dir surfaces an agent flagged provider=codex and
// CostUnknown=true (no Codex pricing entry for an unknown model).
func TestGetAgents_CodexProcess_WithJSONL(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	nested := filepath.Join(codexHome, "sessions", "2026", "04", "19")
	require.NoError(t, os.MkdirAll(nested, 0o755))

	// Codex rollout line: a token_count payload with usage but no known model —
	// an unknown model forces CostUnknown for the non-Claude provider.
	line := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"output_tokens":5}}}}` + "\n"
	sessionFile := filepath.Join(nested, "rollout-abc.jsonl")
	require.NoError(t, os.WriteFile(sessionFile, []byte(line), 0o644))

	cwd := "/some/project"
	m := merger.New(
		merger.WithRegistry(codexRegistry(t)),
		merger.WithScanFn(fixedScanFn([]scanner.ProcessInfo{
			{PID: 4243, CWD: cwd, Uptime: 30, Provider: sdk.ProviderCodex},
		})),
	)

	agents, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, agents, 1)
	assert.Equal(t, sdk.ProviderCodex, agents[0].Provider)
	assert.True(t, agents[0].CostUnknown, "codex cost must be unknown")
	assert.Equal(t, 0.0, agents[0].CostEstimate)
}

// TestGetAgents_EmitsFinishedAfterProcessExits verifies a controllable agent
// seen while live is re-emitted as a finished card after its process exits,
// driven by the package stale tracker.
func TestGetAgents_EmitsFinishedAfterProcessExits(t *testing.T) {
	fs := fakespawn.New(t)

	ag := fs.Spawn(fakespawn.SpawnOpts{})

	m := merger.New(merger.WithScanFn(fs.ScanFn()))

	// Tick 1: process is live, session resolves, snapshot recorded.
	live, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.Equal(t, ag.SessionID, live[0].SessionID)
	require.NotEqual(t, sdk.AgentStatusFinished, live[0].Status, "agent must be live on tick 1")

	// Tick 2: process is gone; the finished card must be emitted from the tracker.
	fs.Exit(ag.PID)

	finished, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, finished, 1)
	assert.Equal(t, ag.SessionID, finished[0].SessionID)
	assert.Equal(t, sdk.AgentStatusFinished, finished[0].Status)
	assert.Equal(t, ag.PID, finished[0].PID)
}

// TestGetAgents_FinishedSurvivesDiscoveryFileRemoval models the real bridge,
// which removes ~/.claude/dashboard-channel/{pid}.json when the process exits.
// The finished card must still appear, driven by the in-memory tracker rather
// than the discovery file's continued existence.
func TestGetAgents_FinishedSurvivesDiscoveryFileRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "11111111-2222-3333-4444-555555555555"
	const pid = 4242
	cwd := filepath.Join(home, "work", "project")
	projectDir := filepath.Join(home, ".claude", "projects", parser.EncodePath(cwd))
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	ts := time.Now().UTC().Format(time.RFC3339)
	lines := `{"type":"user","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(lines), 0o644))

	discDir := filepath.Join(home, ".claude", "dashboard-channel")
	require.NoError(t, os.MkdirAll(discDir, 0o755))
	discFile := filepath.Join(discDir, "4242.json")
	require.NoError(t, os.WriteFile(discFile, []byte(`{"port":1}`), 0o644))

	// One Merger across both ticks so the tracker persists; a mutable scan source
	// flips the agent from live to gone between ticks.
	var procs []scanner.ProcessInfo
	m := merger.New(merger.WithScanFn(func(context.Context) ([]scanner.ProcessInfo, error) {
		return procs, nil
	}))

	// Tick 1: live + channel-available → recorded.
	procs = []scanner.ProcessInfo{{PID: pid, CWD: cwd, Uptime: 30, Provider: sdk.ProviderClaude}}
	live, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, live, 1)
	require.True(t, live[0].ChannelAvailable)

	// Process exits AND the bridge removes its discovery file (real behaviour).
	require.NoError(t, os.Remove(discFile))
	procs = nil

	finished, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, finished, 1)
	assert.Equal(t, sdk.AgentStatusFinished, finished[0].Status)
	assert.Equal(t, sessionID, finished[0].SessionID)
}

// TestGetAgents_NonChannelAgentNoFinishedCard verifies an agent that was never
// channel-available (no discovery file) does not get a finished card after exit.
func TestGetAgents_NonChannelAgentNoFinishedCard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "22222222-2222-3333-4444-555555555555"
	const pid = 5252
	cwd := filepath.Join(home, "work", "p2")
	projectDir := filepath.Join(home, ".claude", "projects", parser.EncodePath(cwd))
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	ts := time.Now().UTC().Format(time.RFC3339)
	lines := `{"type":"user","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":1,"output_tokens":1}}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(lines), 0o644))
	// NO discovery file → not channel-available.

	var procs []scanner.ProcessInfo
	m := merger.New(merger.WithScanFn(func(context.Context) ([]scanner.ProcessInfo, error) {
		return procs, nil
	}))
	procs = []scanner.ProcessInfo{{PID: pid, CWD: cwd, Uptime: 30, Provider: sdk.ProviderClaude}}
	_, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)

	procs = nil
	finished, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	for _, a := range finished {
		if a.SessionID == sessionID {
			t.Fatalf("non-channel agent must not get a finished card")
		}
	}
}

// "The process sets no CLAUDE_CONFIG_DIR" and "its environment could not be
// read" both leave ClaudeConfigDir empty, so the read flag is the only thing
// carrying the difference to spawner attribution — including onto the finished
// card, where the process is gone but the answer was taken while it was alive.
func TestGetAgents_CarriesTheConfigDirReadFlagOntoLiveAndFinishedAgents(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	const sessionID = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	const pid = 5150
	cwd := filepath.Join(home, "work", "project")
	projectDir := filepath.Join(home, ".claude", "projects", parser.EncodePath(cwd))
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	ts := time.Now().UTC().Format(time.RFC3339)
	lines := `{"type":"user","sessionId":"` + sessionID + `","timestamp":"` + ts +
		`","message":{"role":"user","content":"hi"}}` + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(projectDir, sessionID+".jsonl"), []byte(lines), 0o644))

	discDir := filepath.Join(home, ".claude", "dashboard-channel")
	require.NoError(t, os.MkdirAll(discDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(discDir, "5150.json"), []byte(`{"port":1}`), 0o644))

	var procs []scanner.ProcessInfo
	m := merger.New(merger.WithScanFn(func(context.Context) ([]scanner.ProcessInfo, error) {
		return procs, nil
	}))

	// Environment read, variable unset: the session runs on the default dir.
	procs = []scanner.ProcessInfo{{
		PID: pid, CWD: cwd, Uptime: 30, Provider: sdk.ProviderClaude,
		ClaudeConfigDir: "", ClaudeConfigDirKnown: true,
	}}
	live, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.True(t, live[0].ClaudeConfigDirKnown, "the scan read this process's environment")

	procs = nil
	finished, err := m.GetAgents(context.Background(), merger.GetAgentsOpts{})
	require.NoError(t, err)
	require.Len(t, finished, 1)
	require.Equal(t, sdk.AgentStatusFinished, finished[0].Status)
	assert.True(t, finished[0].ClaudeConfigDirKnown, "the answer was read while the process was alive")
}
