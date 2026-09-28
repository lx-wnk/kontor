package scanner

import (
	"testing"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/stretchr/testify/assert"
)

type fakeDetector struct{}

func (fakeDetector) DetectProvider(comm string) sdk.Provider {
	if comm == "codex" {
		return sdk.ProviderCodex
	}
	return ""
}

func TestDetectVia_UsesInjectedDetector(t *testing.T) {
	got := detectProviderVia(fakeDetector{}, "codex")
	if got != sdk.ProviderCodex {
		t.Fatalf("want codex, got %q", got)
	}
	if detectProviderVia(fakeDetector{}, "claude") != sdk.ProviderClaude {
		t.Fatal("claude must always resolve")
	}
}

func TestDetectVia_NilDetector(t *testing.T) {
	if detectProviderVia(nil, "claude --x") != sdk.ProviderClaude {
		t.Fatal("nil detector must still resolve claude")
	}
	if detectProviderVia(nil, "codex") != "" {
		t.Fatal("nil detector must not resolve non-claude")
	}
}

// `ps ewww` prints the whole environment on one line, so an unanchored match
// finds CLAUDE_CONFIG_DIR inside any variable that merely ends with that name
// (OLD_CLAUDE_CONFIG_DIR, a wrapper's saved copy) and returns the wrong dir —
// silently placing the session on another profile.
func TestParsePSEnvBatchMatchesTheVariableExactlyNotAsASuffix(t *testing.T) {
	out := "1 claude OLD_CLAUDE_CONFIG_DIR=/wrong CLAUDE_CONFIG_DIR=/right\n"

	assert.Equal(t, "/right", parsePSEnvBatch(out)[1])
}

func TestParsePSEnvBatchIgnoresAVariableThatOnlyEndsWithTheName(t *testing.T) {
	out := "1 claude MY_CLAUDE_CONFIG_DIR=/wrong\n"

	assert.Empty(t, parsePSEnvBatch(out)[1], "a differently-named variable must not answer for CLAUDE_CONFIG_DIR")
}

// Presence in the map means the environment was read. A process that simply
// does not set the variable is a read with an empty answer, and a process whose
// environment could not be read at all is absent — the caller can only tell
// "runs on the default profile" from "no idea which profile" if those two
// differ here.
func TestParsePSEnvBatchReportsAProcessWithoutTheVariableAsRead(t *testing.T) {
	out := "1 claude PATH=/usr/bin SHELL=/bin/zsh\n"

	dir, ok := parsePSEnvBatch(out)[1]

	assert.True(t, ok, "the environment was read, the variable is just unset")
	assert.Empty(t, dir)
}
