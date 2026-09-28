package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// moduleStageTimeout bounds one module-run stage. The stage run's own timeout
// still governs the task; this only stops a module that has gone quiet from
// holding the orchestrator's goroutine forever.
const moduleStageTimeout = 10 * time.Minute

// moduleStageHandler runs one stage by asking a module instead of spawning an
// agent. The stage run row, its states and its retry path stay core machinery:
// the module answers what to do next, it does not decide how a stage is
// recorded.
type moduleStageHandler struct {
	kind     string
	moduleID string
	registry *plugin.Registry
	client   *http.Client
}

func (h moduleStageHandler) Stage() string { return h.kind }

// RequiresAgent is false: nothing is spawned, so the orchestrator must not wait
// for an agent process that will never exist.
func (h moduleStageHandler) RequiresAgent() bool { return false }

// moduleStageReply is what a module answers. Deliberately small: a verdict and
// what it produced. Anything richer would make a module's answer part of the
// pipeline's state machine.
type moduleStageReply struct {
	Transition string         `json:"transition"`
	Reason     string         `json:"reason"`
	Output     map[string]any `json:"output"`
}

func (h moduleStageHandler) Execute(ctx *pipeline.StageContext) (pipeline.StageTransition, error) {
	entry, ok := h.registry.Lookup(h.moduleID)
	if !ok || !entry.Healthy() {
		return pipeline.FailTransition{Reason: fmt.Sprintf("module %q is not available for stage %q", h.moduleID, h.kind)}, nil
	}

	payload, err := json.Marshal(map[string]any{
		"taskId":         ctx.Task.ID,
		"stage":          h.kind,
		"cwd":            ctx.Task.Cwd,
		"previousOutput": ctx.PreviousOutput,
	})
	if err != nil {
		return pipeline.FailTransition{Reason: err.Error()}, nil
	}

	base := ctx.Ctx
	if base == nil {
		base = context.Background()
	}
	reqCtx, cancel := context.WithTimeout(base, moduleStageTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, entry.BaseURL+"/stages/"+h.kind+"/run", bytes.NewReader(payload))
	if err != nil {
		return pipeline.FailTransition{Reason: err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return pipeline.FailTransition{Reason: fmt.Sprintf("module %q: %v", h.moduleID, err)}, nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return pipeline.FailTransition{Reason: fmt.Sprintf("module %q: %v", h.moduleID, err)}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return pipeline.FailTransition{Reason: fmt.Sprintf("module %q returned HTTP %d: %s", h.moduleID, resp.StatusCode, string(body))}, nil
	}

	var reply moduleStageReply
	if err := json.Unmarshal(body, &reply); err != nil {
		return pipeline.FailTransition{Reason: fmt.Sprintf("module %q returned an unreadable answer: %v", h.moduleID, err)}, nil
	}
	switch reply.Transition {
	case "next", "":
		return pipeline.NextTransition{Output: reply.Output}, nil
	case "done":
		return pipeline.DoneTransition{Output: reply.Output}, nil
	case "fail":
		return pipeline.FailTransition{Reason: reply.Reason, Output: reply.Output}, nil
	default:
		// An unknown verdict fails the stage rather than being guessed at: a
		// module inventing a transition must not silently become "carry on".
		return pipeline.FailTransition{Reason: fmt.Sprintf("module %q answered with an unknown transition %q", h.moduleID, reply.Transition)}, nil
	}
}

// registerModuleStageKinds makes every stage kind a loaded module declares
// resolvable by the orchestrator. A kind colliding with a core stage is refused
// by the orchestrator and reported here.
func registerModuleStageKinds(orch *pipeline.PipelineOrchestrator, registry *plugin.Registry) {
	if orch == nil || registry == nil {
		return
	}
	client := &http.Client{Timeout: moduleStageTimeout}
	for _, entry := range registry.All() {
		for _, kind := range entry.Descriptor.TaskKinds {
			if err := pipeline.RegisterTaskKindSequence(kind.Name, kind.Stages); err != nil {
				slog.Warn("module task kind refused", "module", entry.Descriptor.ID, "kind", kind.Name, "err", err)
			}
		}
		for _, kind := range entry.Descriptor.StageKinds {
			h := moduleStageHandler{kind: kind.Name, moduleID: entry.Descriptor.ID, registry: registry, client: client}
			if err := orch.RegisterStageKind(kind.Name, h); err != nil {
				slog.Warn("module stage kind refused", "module", entry.Descriptor.ID, "kind", kind.Name, "err", err)
			}
		}
	}
}
