package tools

import (
	"testing"

	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/stretchr/testify/require"
)

func TestRegisterKeyTools_AllToolsPresent(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterKeyTools(registry, KeyDeps{})
	for _, name := range []string{
		"list_api_keys",
		"create_api_key",
		"revoke_api_key",
	} {
		require.Contains(t, registry, name, "expected tool %q to be registered", name)
	}
}

// TestCreateAPIKeySchemaMatchesValidKeyScopes closes the "two places, one
// value" bug class this project keeps hitting: create_api_key's InputSchema
// enum and the validKeyScopes validator must always name the exact same set.
// A scope added to one and forgotten in the other must fail here instead of
// shipping — one accepted by the validator but invisible to a client reading
// the tool's own schema, the other visible but rejected.
func TestCreateAPIKeySchemaMatchesValidKeyScopes(t *testing.T) {
	registry := mcp.ToolRegistry{}
	RegisterKeyTools(registry, KeyDeps{})

	props := registry["create_api_key"].InputSchema["properties"].(map[string]any)
	scopesProp := props["scopes"].(map[string]any)
	items := scopesProp["items"].(map[string]any)
	enumList := items["enum"].([]string)

	enumSet := make(map[string]bool, len(enumList))
	for _, s := range enumList {
		enumSet[s] = true
	}
	require.Equal(t, validKeyScopes, enumSet, "create_api_key's schema enum and validKeyScopes must list the exact same scopes")
}
