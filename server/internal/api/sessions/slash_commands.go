package sessions

import (
	"encoding/json"
	"net/http"

	"github.com/lx-wnk/kontor/server/internal/cmdscope"
)

// CommandsHandler serves slash-command enumeration scoped to a spawner or a live
// session, rather than the global ~/.claude view.
type CommandsHandler struct {
	spawners cmdscope.SpawnerGetter
	agents   cmdscope.AgentsFn
}

// NewCommandsHandler constructs the handler from the spawner repo and the live
// agents accessor (e.g. merger.GetAgents). Either may be nil; resolution then
// falls back to the process-default scope.
func NewCommandsHandler(spawners cmdscope.SpawnerGetter, agents cmdscope.AgentsFn) *CommandsHandler {
	return &CommandsHandler{spawners: spawners, agents: agents}
}

// slashCommandsResponse is the enriched payload: the scoped command list plus
// the engine version (so the UI can show which spawner/session and flag a
// possibly-stale built-in list).
type slashCommandsResponse struct {
	Commands           []cmdscope.SlashCommand `json:"commands"`
	EngineVersion      string                  `json:"engineVersion,omitempty"`
	BuiltinsMayBeStale bool                    `json:"builtinsMayBeStale"`
	ScopeSource        string                  `json:"scopeSource"`
	ScopeLabel         string                  `json:"scopeLabel"`
}

// SlashCommands handles GET /api/slash-commands.
//
// Scope is resolved (in precedence order) from ?sessionId (a live session's
// detected config dir), ?spawnerId (a spawner row), else the default spawner —
// so the returned set always reflects a concrete spawner/session, never an
// unfiltered global read. ?cwd adds project-local <cwd>/.claude/commands.
func (h *CommandsHandler) SlashCommands(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cwd := cmdscope.SanitizeProjectCwd(q.Get("cwd"))
	scope := cmdscope.ResolveRequestScope(r.Context(), q.Get("sessionId"), q.Get("spawnerId"), cwd, h.spawners, h.agents)

	// Only Claude scopes have a slash-command surface; skip the version probe
	// (an exec of scope.Command) for unsupported adapters, which enumerate empty.
	var version string
	var ok bool
	if scope.Supported {
		version, ok = cmdscope.ProbeEngineVersion(scope.Command)
	}
	resp := slashCommandsResponse{
		Commands:           scope.SlashCommands(),
		EngineVersion:      version,
		BuiltinsMayBeStale: cmdscope.BuiltinsMayBeStale(version, ok),
		ScopeSource:        scope.Source,
		ScopeLabel:         scope.Label,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
