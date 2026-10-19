package repo

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuditOrigins_UseTheRenderedActorRolesAndNoLogin(t *testing.T) {
	ui := UIAuditOrigin("local")
	require.Equal(t, map[string]any{"source": AuditSourceUI, "actor": AuditActorUser}, ui.Metadata())
	require.Equal(t, "local", *ui.UserID)

	person := MCPAuditOrigin("key-1", "", "")
	require.Equal(t, map[string]any{"source": AuditSourceMCP, "actor": AuditActorUser, "apiKeyId": "key-1"}, person.Metadata())

	stage := MCPAuditOrigin("key-2", "run-9", "")
	require.Equal(t, map[string]any{"source": AuditSourceMCP, "actor": AuditActorAgent, "apiKeyId": "key-2", "callerStageRunId": "run-9"}, stage.Metadata())

	module := MCPAuditOrigin("key-3", "", "mod-1")
	require.Equal(t, AuditActorAgent, module.Actor)
	require.Equal(t, "mod-1", module.Metadata()["moduleId"])
}
