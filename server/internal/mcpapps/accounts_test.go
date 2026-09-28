package mcpapps_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/mcpapps"
)

func TestEnvAccountKey_MatchesTheServersOwnRule(t *testing.T) {
	require.Equal(t, "WORK_EXAMPLE_COM", mcpapps.EnvAccountKey("work@example.com"))
	require.Equal(t, "WORK_GMAIL", mcpapps.EnvAccountKey("Work Gmail"))
	require.Equal(t, "A1", mcpapps.EnvAccountKey("a1"))
}

func TestSecretNamesForAccounts_OnePerTemplatePerAccount(t *testing.T) {
	names := mcpapps.SecretNamesForAccounts(
		[]string{"IMAP_MCP_ACCOUNT_{ACCOUNT}_IMAP_PASSWORD", "IMAP_MCP_ACCOUNT_{ACCOUNT}_SMTP_PASSWORD"},
		[]string{"work@example.com", "Work Gmail"},
	)
	require.Equal(t, []string{
		"IMAP_MCP_ACCOUNT_WORK_EXAMPLE_COM_IMAP_PASSWORD",
		"IMAP_MCP_ACCOUNT_WORK_GMAIL_IMAP_PASSWORD",
		"IMAP_MCP_ACCOUNT_WORK_EXAMPLE_COM_SMTP_PASSWORD",
		"IMAP_MCP_ACCOUNT_WORK_GMAIL_SMTP_PASSWORD",
	}, names)
}

func TestSecretNamesForAccounts_SkipsANameWithNothingToNormalise(t *testing.T) {
	names := mcpapps.SecretNamesForAccounts([]string{"X_{ACCOUNT}_Y"}, []string{"@@@", "ok"})
	require.Equal(t, []string{"X_OK_Y"}, names)
}

func TestAccountNames_ReadsTheToolsTextBody(t *testing.T) {
	names, err := mcpapps.AccountNames(json.RawMessage(`{"accounts":[{"id":"a1","name":"work@example.com"},{"id":"a2","name":"Work Gmail"}]}`))
	require.NoError(t, err)
	require.Equal(t, []string{"work@example.com", "Work Gmail"}, names)
}

func TestAccountNames_EmptyListIsAFreshInstallNotAnError(t *testing.T) {
	names, err := mcpapps.AccountNames(json.RawMessage(`{"accounts":[]}`))
	require.NoError(t, err)
	require.Empty(t, names)
}

func TestAccountNames_MalformedBodyIsAnError(t *testing.T) {
	_, err := mcpapps.AccountNames(json.RawMessage(`not json`))
	require.Error(t, err)
}
