package agentbroadcast

import (
	"context"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/merger"
)

// PermissionBridgeReader is the read side of the hook permission bridge.
type PermissionBridgeReader interface {
	StateForSession(sessionID string) (held []sdk.PendingPermission, atTerminal bool, terminalToolUseID string, armed bool)
	// SweepExpired drops armed marks and notices that have aged out. Expiry is
	// driven from this tick rather than from the read, so it does not depend on
	// someone happening to look at a session.
	SweepExpired()
}

// NewPermissionBridgeEnricher annotates each agent with the permission prompts
// the bridge knows about.
//
// Unlike the pipeline enricher next to it, this is positive evidence rather than
// inference: the request came from the session itself, through its PreToolUse
// hook, carrying the tool and the exact argument it is asking about. Nothing is
// reconstructed from the transcript, where a tool call blocked on approval and
// one merely still running leave the same trace.
//
// A nil reader returns a nil enricher, which merger.ChainEnrichers skips.
func NewPermissionBridgeEnricher(bridge PermissionBridgeReader) merger.Enricher {
	if bridge == nil {
		return nil
	}
	return func(_ context.Context, agents []sdk.Agent) {
		bridge.SweepExpired()
		for i := range agents {
			sid := agents[i].SessionID
			if sid == "" {
				continue
			}
			held, atTerminal, terminalToolUseID, armed := bridge.StateForSession(sid)
			// A held hook call goes in its own field, never into
			// PendingPermissions: that one carries pipeline stage-run rows,
			// which are database-backed and resolve through a different
			// endpoint. Sharing the slice let a hook UUID ride along in the
			// pipeline's bulk-resolve payload.
			agents[i].HeldPermissions = held
			agents[i].AwaitingTerminalPermission = atTerminal
			agents[i].TerminalPermissionToolUseID = terminalToolUseID
			agents[i].PermissionBridgeArmed = armed
		}
	}
}
