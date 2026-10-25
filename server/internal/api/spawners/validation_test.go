package spawners

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAdapterType_AcceptsAcp(t *testing.T) {
	msg, ok := ValidateAdapterType("acp")
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_AcpAcceptsCommandAndArgs(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{
		"command": "npx",
		"args":    "-y @agentclientprotocol/claude-agent-acp@0.68.0",
	})
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_AcpRejectsUnknownKey(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"host": "localhost"})
	require.False(t, ok)
	require.Equal(t, "unknown adapter_config key: host", msg)
}

func TestValidateAdapterConfig_AcpRejectsUntrustedCommand(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"command": "/tmp/evil"})
	require.False(t, ok)
	require.Contains(t, msg, "could not be resolved")
}

func TestValidateAdapterConfig_AcpRejectsUnknownBareCommand(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"command": "evil-binary"})
	require.False(t, ok)
	require.Contains(t, msg, "not in the allow-list")
}

func TestValidateAdapterConfig_AcpAcceptsAllowlistedBareCommand(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"command": "npx"})
	require.True(t, ok)
	require.Empty(t, msg)
}

// TestValidateAdapterConfig_AcpAcceptsAbsoluteTrustedCommand covers the
// absolute-path accept branch: a path that resolves and whose parent is a
// trusted bin dir. /bin/sh exists on both macOS and Linux.
func TestValidateAdapterConfig_AcpAcceptsAbsoluteTrustedCommand(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"command": "/bin/sh"})
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_AcpAcceptsEmptyCommand(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"command": ""})
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_ClaudeAcceptsEffort(t *testing.T) {
	msg, ok := ValidateAdapterConfig("claude", map[string]string{"effort": "high"})
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_ClaudeRejectsUnrecognisedEffortLevel(t *testing.T) {
	for _, v := range []string{"HIGH", "ultra", " high", "extreme"} {
		msg, ok := ValidateAdapterConfig("claude", map[string]string{"effort": v})
		require.False(t, ok, "effort %q must be refused", v)
		require.Equal(t, "adapter_config.effort: unrecognised effort level "+v, msg)
	}
}

func TestValidateAdapterConfig_ClaudeAcceptsEmptyEffort(t *testing.T) {
	msg, ok := ValidateAdapterConfig("claude", map[string]string{"effort": ""})
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_OllamaRejectsEffort(t *testing.T) {
	msg, ok := ValidateAdapterConfig("ollama", map[string]string{"effort": "high"})
	require.False(t, ok)
	require.Equal(t, "unknown adapter_config key: effort", msg)
}

func TestValidateAdapterConfig_OllamaUnaffectedByCommandCheck(t *testing.T) {
	msg, ok := ValidateAdapterConfig("ollama", map[string]string{"host": "http://localhost:11434"})
	require.True(t, ok)
	require.Empty(t, msg)
}

func TestValidateAdapterConfig_AcpAcceptsTheDocumentedDefaultArgs(t *testing.T) {
	// The bundled ACP default is npx WITH a package argument — the exact shape a
	// naive path rule would wrongly reject.
	msg, ok := ValidateAdapterConfig("acp", map[string]string{
		"args": "-y @agentclientprotocol/claude-agent-acp@0.68.0",
	})
	require.True(t, ok, "documented ACP default args must stay valid, got %q", msg)
}

func TestValidateAdapterConfig_AcpRejectsAbsolutePathArg(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{
		"command": "/bin/sh",
		"args":    "/tmp/payload.sh",
	})
	require.False(t, ok)
	require.Contains(t, msg, "adapter_config.args")
}

func TestValidateAdapterConfig_AcpRejectsRelativePathArg(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{
		"command": "/bin/sh",
		"args":    "./payload.sh",
	})
	require.False(t, ok)
	require.Contains(t, msg, "adapter_config.args")
}

func TestValidateAdapterConfig_AcpAcceptsTrustedPathArg(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"args": "/bin/sh"})
	require.True(t, ok, "a path arg under a trusted bin dir must pass, got %q", msg)
}

func TestValidateAdapterConfig_AcpAcceptsEmptyArgs(t *testing.T) {
	msg, ok := ValidateAdapterConfig("acp", map[string]string{"args": ""})
	require.True(t, ok, "an empty args value must stay valid, got %q", msg)
}

func TestValidateEnv_RejectsKeyThatIsNotAVariableName(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY=x", "1A", "A-B", "Ä"} {
		msg, ok := ValidateEnv(map[string]string{key: "v"})
		require.False(t, ok, key)
		require.Equal(t, "env key must be a variable name (letters, digits, underscore; not starting with a digit)", msg, key)
	}
}

func TestValidateEnv_AcceptsVariableName(t *testing.T) {
	msg, ok := ValidateEnv(map[string]string{"OK_KEY": "v", "_x": "1"})
	require.True(t, ok)
	require.Empty(t, msg)
}
