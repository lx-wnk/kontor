package pipeline

import (
	"context"

	"fmt"
	"github.com/lx-wnk/kontor/server/internal/acp"
	"log/slog"
	"maps"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/llmadapter"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// SpawnFunc launches the agent process for native (claude) stages.
type SpawnFunc func(opts SpawnAgentOptions) (SpawnResult, error)

// agentStageHandler is the generic stage handler for agent-driven stages.
type agentStageHandler struct {
	stage       string
	buildPrompt func(ctx *StageContext) PromptBundle
	spawnFn     SpawnFunc
}

func (h *agentStageHandler) Stage() string       { return h.stage }
func (h *agentStageHandler) RequiresAgent() bool { return true }

func (h *agentStageHandler) Execute(ctx *StageContext) (StageTransition, error) {
	bundle := h.buildPrompt(ctx)
	// Prepend any custom system prompts from DB (highest priority first).
	if custom := buildCustomSystemPrompt(ctx, h.stage); custom != "" {
		bundle.SystemPrompt = custom + "\n\n---\n\n" + bundle.SystemPrompt
	}
	feedback := BuildFeedbackPrefix(ctx.PriorIterationOutput)
	fullUserPrompt := buildStageUserPrompt(ctx, bundle, feedback)

	// Resolve the effective DB spawner immediately before exec. Failure to
	// resolve is fatal — we do NOT silently fall back to the bare `claude`
	// binary when the task or its project named a spawner that could not be
	// loaded (e.g. it was deleted out from under us). Propagate the error.
	var resolved *ent.Spawner
	if ctx.ResolveSpawner != nil {
		sp, err := ctx.ResolveSpawner(ctx.Ctx, ctx.Task.ID, h.stage)
		if err != nil {
			return nil, fmt.Errorf("agentStageHandler.Execute(%s): resolve spawner: %w", h.stage, err)
		}
		resolved = sp
	}

	// LLM-adapter path: when the resolved row declares a non-Claude adapter
	// type, build the corresponding LLMSpawner and dispatch via the HTTP/
	// custom-command abstraction. The adapter writes its own synthetic JSONL
	// session file so the completion detector can read the output unchanged.
	if resolved != nil && resolved.AdapterType != "" && resolved.AdapterType != "claude" {
		adapter, err := llmadapter.NewLLMSpawnerFromSpawner(resolved, acpPermissionGate(ctx))
		if err != nil {
			return nil, fmt.Errorf("agentStageHandler.Execute(%s): build adapter: %w", h.stage, err)
		}
		if adapter == nil {
			// Defensive: factory returned nil for a non-claude type, which
			// should never happen unless the catalog and factory drift apart.
			return nil, fmt.Errorf("agentStageHandler.Execute(%s): adapter factory returned nil for adapter_type %q", h.stage, resolved.AdapterType)
		}

		// Precedence (highest wins): spawner.ModelOverride > task.Metadata["model"]
		// > per-stage config/default. Per-stage default is only the floor.
		model := ""
		if ctx.StageModelFn != nil {
			model = ctx.StageModelFn(ctx.Ctx, h.stage, ctx.Task.ProjectID)
		}
		if ctx.Task.Metadata != nil {
			if m, ok := ctx.Task.Metadata["model"].(string); ok && m != "" {
				model = m
			}
		}
		// Per-spawner ModelOverride wins over everything — mirrors native-path.
		if resolved.ModelOverride != nil && *resolved.ModelOverride != "" {
			model = *resolved.ModelOverride
		}
		cwd := ctx.Task.Cwd
		if ctx.Task.WorktreePath != nil && *ctx.Task.WorktreePath != "" {
			cwd = *ctx.Task.WorktreePath
		}
		allowedTools := buildAllowedToolsList(ctx)
		spawnArgs := llmadapter.LLMSpawnArgs{
			TaskID:       ctx.Task.ID,
			StageRunID:   ctx.StageRun.ID,
			Stage:        h.stage,
			SystemPrompt: bundle.SystemPrompt,
			UserPrompt:   fullUserPrompt,
			Model:        model,
			WorkDir:      cwd,
			AllowedTools: allowedTools,
		}
		stageRunID := ctx.StageRun.ID
		taskID := ctx.Task.ID

		ctx.RecordAudit(h.stage+"_dispatched", map[string]any{
			"spawner":     adapter.Name(),
			"adapterType": resolved.AdapterType,
			"iteration":   ctx.StageRun.Iteration,
			"hasFeedback": len(feedback) > 0,
		})

		if ctx.DispatchHTTPSpawn != nil {
			// Async path: dispatch to goroutine pool and return immediately. The
			// seam supplies its own long-lived context — ctx.Ctx is the HTTP
			// request context and is cancelled the instant the handler returns.
			ctx.DispatchHTTPSpawn(stageRunID, taskID, func(spawnCtx context.Context) (string, error) {
				result, err := adapter.Spawn(spawnCtx, spawnArgs)
				if err != nil {
					return "", fmt.Errorf("spawner %s: %w", adapter.Name(), err)
				}
				return result.SessionFile, nil
			})
			return AsyncRunningTransition{PID: 0}, nil
		}

		// Synchronous fallback for tests or environments without a live orchestrator:
		// the caller blocks on this call, so ctx.Ctx is correct here.
		llmResult, err := adapter.Spawn(ctx.Ctx, spawnArgs)
		if err != nil {
			return nil, fmt.Errorf("agentStageHandler.Execute(%s): spawner %s: %w", h.stage, adapter.Name(), err)
		}
		return AsyncRunningTransition{PID: llmResult.PID, SessionID: llmResult.SessionID, SessionFile: llmResult.SessionFile}, nil
	}

	// Native Claude path: resolved is either nil, claude-default, or any row
	// with AdapterType=="claude" / "". Behaviour is byte-identical to the
	// pre-adapter-merge code path.
	if h.spawnFn == nil {
		return nil, fmt.Errorf("agentStageHandler.Execute(%s): spawnFn not set", h.stage)
	}

	// Resolve the native-path model following full precedence:
	// per-stage default → task.Metadata["model"] → BUT spawner.ModelOverride
	// must still win. Since BuildSpawnArgs only inserts spawner.ModelOverride
	// when opts.Model is empty, we must NOT set opts.Model when the spawner
	// declares its own override — otherwise opts.Model would silently win.
	nativeModel := ""
	spawnerHasOverride := resolved != nil && resolved.ModelOverride != nil && *resolved.ModelOverride != ""
	if !spawnerHasOverride {
		// Start with per-stage default (coded + DB row), then let task override it.
		if ctx.StageModelFn != nil {
			nativeModel = ctx.StageModelFn(ctx.Ctx, h.stage, ctx.Task.ProjectID)
		}
		if ctx.Task.Metadata != nil {
			if m, ok := ctx.Task.Metadata["model"].(string); ok && m != "" {
				nativeModel = m
			}
		}
	}

	// Reasoning effort: read straight off the spawner already resolved above
	// rather than calling services.ResolveEffort again — resolving a second
	// time through the SpawnerResolver would repeat the same DB lookups this
	// function just made to get `resolved`. Reaching this point in the
	// function already means resolved is nil, "", or "claude" (any other
	// adapter_type took the LLM-adapter branch above and returned), which is
	// exactly the set services.effortSupportingAdapterTypes encodes, so no
	// separate "supported" check is needed here. A resolver error can never
	// leave effort unresolved silently — it already aborted the spawn above,
	// same as it does for the model. An unrecognized value is dropped rather
	// than guessed, so a bad stored string can never break the CLI call.
	nativeEffort := ""
	if resolved != nil {
		if v, ok := resolved.AdapterConfig[services.AdapterConfigEffortKey]; ok && services.IsValidEffortLevel(v) {
			nativeEffort = v
		}
	}

	var apps mcpapps.RunApplications
	if ctx.ResolveApplications != nil {
		resolved, err := ctx.ResolveApplications(ctx.Ctx, ctx.Task)
		if err != nil {
			return FailTransition{Reason: "MCP applications: " + err.Error()}, nil
		}
		apps = resolved
	}

	// A credential that cannot be minted is not a reason to refuse the spawn:
	// the agent then runs without task API access at all, since the spawn is
	// strict-scoped to its own MCP config.
	taskAPIToken := ""
	if ctx.IssueTaskAPIKey != nil {
		if tok, err := ctx.IssueTaskAPIKey(ctx.Ctx, ctx.StageRun.ID, ctx.StageTimeout); err != nil {
			slog.Warn("pipeline: issuing the stage-run MCP credential failed — agent runs without task API access",
				"stageRun", ctx.StageRun.ID, "err", err)
		} else {
			taskAPIToken = tok
		}
	}

	result, err := h.spawnFn(SpawnAgentOptions{
		Task:            ctx.Task,
		StageRun:        ctx.StageRun,
		SystemPrompt:    bundle.SystemPrompt,
		Prompt:          fullUserPrompt,
		Permissions:     ctx.Permissions,
		EnableChannel:   true,
		ResumeSessionID: ctx.ResumeSessionID,
		MCPToken:        ctx.MCPToken,
		MCPUrl:          ctx.MCPUrl,
		TaskAPIToken:    taskAPIToken,
		Spawner:         resolved,
		Model:           nativeModel,
		Effort:          nativeEffort,
		AdditionalDirs:  ctx.AdditionalDirs,
		AllowGitPush:    ctx.AllowGitPush,
		Applications:    apps,
	})
	if err != nil {
		return nil, fmt.Errorf("agentStageHandler.Execute(%s): %w", h.stage, err)
	}

	if ctx.RegisterSpawnCleanup != nil {
		ctx.RegisterSpawnCleanup(ctx.StageRun.ID, result.Cleanup)
	}

	ctx.RecordAudit(h.stage+"_spawned", map[string]any{
		"pid":              result.PID,
		"iteration":        ctx.StageRun.Iteration,
		"hasFeedback":      len(feedback) > 0,
		"resumedSessionId": ctx.ResumeSessionID,
	})

	return AsyncRunningTransition{PID: result.PID}, nil
}

// buildAllowedToolsList returns the allow-list strings for the given context,
// mirroring the logic in BuildAllowList so non-Claude adapters receive the
// same tool constraints.
func buildAllowedToolsList(ctx *StageContext) []string {
	raw := BuildAllowList(ctx.Task.Autonomy, ctx.Permissions, false, ctx.AllowGitPush, nil)
	// Strip the Bash(...) wrapper — non-Claude adapters receive plain tool names.
	tools := make([]string, 0, len(raw))
	for _, entry := range raw {
		if strings.HasPrefix(entry, "Bash(") {
			tools = append(tools, "Bash")
			continue
		}
		// Strip pattern suffix, e.g. "Read(*.go)" → "Read"
		if idx := strings.IndexByte(entry, '('); idx >= 0 {
			tools = append(tools, entry[:idx])
			continue
		}
		tools = append(tools, entry)
	}
	return tools
}

// resumeContinueInstruction replaces the full task spec on resume spawns to
// avoid re-sending the entire spec that the session already has in context.
// It repeats how to submit the result, because replacing the stage prompt also
// drops that prompt's set_stage_output instruction — leaving a resumed run with
// no statement of the primary channel at all.
const resumeContinueInstruction = "Continue your previous attempt on this task. Your earlier session context is already loaded — do not restart from scratch or re-read the full specification. Review where you left off, fix what caused the previous failure, and complete the remaining work. Submit your result as your FINAL action by calling the `set_stage_output` MCP tool, in the same shape your original instructions specified; only if that tool is unavailable, emit the same object as a fenced json block."

// buildStageUserPrompt assembles the user-facing prompt for a stage execution.
// On resume, the full task spec (bundle.UserPrompt) is swapped for a short
// "continue" instruction; feedback and additional-prompt suffix are
// preserved.
//
// The memory block goes here, in the user prompt, not the system prompt.
// Custom system-prompt content is prepended at the top of Execute
// (custom + "\n\n---\n\n" + bundle.SystemPrompt), and BuildSpawnArgs
// silently truncates the system prompt head-first at systemPromptMaxChars —
// a memory block added there would compete with, and could push out, the
// tail of the bundle's own system prompt. The user prompt has no such cut,
// and placing the block after the stage instructions and before the user's
// additional prompt keeps instruction primacy and gives human input the
// last word.
func buildStageUserPrompt(ctx *StageContext, bundle PromptBundle, feedback string) string {
	userPrompt := bundle.UserPrompt
	if ctx.ResumeSessionID != "" {
		userPrompt = resumeContinueInstruction
	}
	memoryBlock := injectMemoryBlock(ctx)
	return feedback + userPrompt + buildMemoryBlockSuffix(memoryBlock) + buildAdditionalPromptSuffix(ctx.UserAdditionalPrompt)
}

// injectMemoryBlock retrieves a budgeted slice of memory for the stage's
// task and packs it into ctx.MemoryBudget characters. Memory being
// unavailable must never block a spawn: a nil InjectMemory or a budget that
// cannot fit even one entry (<= 0) disables the push outright, an
// authorization denial or a retrieval error degrades to no block rather than
// failing the stage.
func injectMemoryBlock(ctx *StageContext) string {
	if ctx.InjectMemory == nil || ctx.AuthorizeMemory == nil || ctx.MemoryBudget <= 0 {
		return ""
	}

	scope := repo.GlobalScope()
	if ctx.Task.Cwd != "" {
		scope = repo.ProjectScope(ctx.Task.Cwd)
	}

	// The automatic push is otherwise the one memory read in the system
	// with no capability gate: every human-facing route (MCP tools, HTTP
	// API) already calls memory.Authorize before reading. Gate this one the
	// same way, and degrade to no block on denial rather than failing the
	// spawn.
	//
	// An unwired gate means no memory, not ungated memory — the same way an
	// unwired InjectMemory above means no memory. A caller that wants the
	// push wires both; a test that wants it wires a permissive authorizer
	// explicitly rather than relying on nil to skip the check.
	if err := ctx.AuthorizeMemory(ctx.Ctx, scope, strOrEmpty(ctx.Task.RoutineID)); err != nil {
		ctx.RecordAudit(ctx.StageRun.Stage+"_memory_denied", map[string]any{"error": err.Error()})
		return ""
	}

	queryText := ctx.Task.Title
	if ctx.Task.Description != nil {
		queryText += " " + *ctx.Task.Description
	}

	entries, err := ctx.InjectMemory(ctx.Ctx, memory.Query{Text: queryText, Scope: scope})
	if err != nil {
		ctx.RecordAudit(ctx.StageRun.Stage+"_memory_retrieve_failed", map[string]any{"error": err.Error()})
		return ""
	}

	sel := memory.Select(entries, ctx.MemoryBudget)
	if ctx.RecordMemoryInjection != nil {
		entryIDs := make([]string, len(sel.Entries))
		for i, e := range sel.Entries {
			entryIDs[i] = e.ID
		}
		if _, err := ctx.RecordMemoryInjection(ctx.Ctx, repo.RecordInjectionInput{
			StageRunID:     ctx.StageRun.ID,
			EntryIDs:       entryIDs,
			CharBudget:     ctx.MemoryBudget,
			CharsUsed:      sel.Used,
			CandidateCount: len(entries),
		}); err != nil {
			ctx.RecordAudit(ctx.StageRun.Stage+"_memory_record_failed", map[string]any{"error": err.Error()})
		}
	}
	return sel.Block
}

// buildMemoryBlockSuffix wraps a non-empty memory block in the same
// "\n\n---\n" separator buildAdditionalPromptSuffix uses, so the assembled
// prompt reads as a sequence of clearly bounded sections. An empty block
// contributes nothing — no separator, no empty heading.
func buildMemoryBlockSuffix(block string) string {
	if block == "" {
		return ""
	}
	return "\n\n---\n" + block
}

func buildAdditionalPromptSuffix(prompt string) string {
	if prompt == "" {
		return ""
	}
	return fmt.Sprintf("\n\n---\nAdditional instruction from user: %s", prompt)
}

// createAgentStage returns an agent-driven StageHandler for the given stage.
func createAgentStage(stage string, buildPrompt func(*StageContext) PromptBundle, spawnFn SpawnFunc) StageHandler {
	return &agentStageHandler{
		stage:       stage,
		buildPrompt: buildPrompt,
		spawnFn:     spawnFn,
	}
}

type staticHandler struct {
	stage     string
	executeFn func(ctx *StageContext) (StageTransition, error)
}

func (h *staticHandler) Stage() string       { return h.stage }
func (h *staticHandler) RequiresAgent() bool { return false }
func (h *staticHandler) Execute(ctx *StageContext) (StageTransition, error) {
	return h.executeFn(ctx)
}

var backlogHandler StageHandler = &staticHandler{
	stage: "backlog",
	executeFn: func(ctx *StageContext) (StageTransition, error) {
		ctx.RecordAudit("backlog_chat_pending", nil)
		return WaitUserTransition{Reason: "Refinement chat in progress"}, nil
	},
}

var readyHandler StageHandler = &staticHandler{
	stage: "ready",
	executeFn: func(ctx *StageContext) (StageTransition, error) {
		ctx.RecordAudit("ready_entered", nil)
		return NextTransition{Stage: stageAfterReady(ctx.Task)}, nil
	},
}

func implementationBuilder(ctx *StageContext) PromptBundle {
	feedback := ""
	if ctx.Task.Metadata != nil {
		if fb, ok := ctx.Task.Metadata["review_feedback"].(string); ok {
			feedback = fb
		}
	}
	conceptOutput := map[string]any{}
	maps.Copy(conceptOutput, ctx.Task.Metadata)
	return ImplementationPrompt(ctx.Task, conceptOutput, feedback, ctx.AllowGitPush)
}

func selfReviewBuilder(ctx *StageContext) PromptBundle {
	return SelfReviewPrompt(ctx.Task, ctx.PreviousOutput)
}

func finalizationBuilder(ctx *StageContext) PromptBundle {
	return FinalizationPrompt(ctx.Task, ctx.AllStageRuns)
}

func jobBuilder(ctx *StageContext) PromptBundle {
	return JobPrompt(ctx.Task)
}

func planReviewBuilder(ctx *StageContext) PromptBundle {
	feedback := ""
	if ctx.Task.Metadata != nil {
		if fb, ok := ctx.Task.Metadata["planReviewFeedback"].(string); ok {
			feedback = fb
		}
	}
	conceptOutput := map[string]any{}
	maps.Copy(conceptOutput, ctx.Task.Metadata)
	return PlanReviewPrompt(ctx.Task, conceptOutput, feedback)
}

// NewStageHandlers builds a stage registry with the given spawn function wired
// into all agent-driven stages.
func NewStageHandlers(spawnFn SpawnFunc) map[string]StageHandler {
	return map[string]StageHandler{
		"backlog":        backlogHandler,
		"ready":          readyHandler,
		"implementation": createAgentStage("implementation", implementationBuilder, spawnFn),
		"self_review":    createAgentStage("self_review", selfReviewBuilder, spawnFn),
		"plan_review":    createAgentStage("plan_review", planReviewBuilder, spawnFn),
		"finalization":   createAgentStage("finalization", finalizationBuilder, spawnFn),
		StageJob:         createAgentStage(StageJob, jobBuilder, spawnFn),
	}
}

// HandlersByStage is the default package-level registry. Agent stages use
// syntheticSpawn so this registry is safe for any code that does not wire a
// real orchestrator (tests, static analysis, etc.).
var HandlersByStage = NewStageHandlers(syntheticSpawn)

// GetHandlerForStage returns the handler for stage, or nil if unregistered.
func GetHandlerForStage(stage string) StageHandler {
	return HandlersByStage[stage]
}

// acpPermissionGate routes an ACP agent's permission requests into the same
// approval flow the native Claude path uses. Returns nil when the stage context
// carries no permission callbacks (tests without a live orchestrator); the
// adapter then reports an unwired gate rather than silently refusing.
func acpPermissionGate(ctx *StageContext) func(context.Context, acp.PermissionRequest) (acp.PermissionDecision, error) {
	if ctx.RequestPermission == nil || ctx.PermissionStatus == nil {
		return nil
	}
	gate := &acp.PollingGate{
		File: func(_ context.Context, req acp.PermissionRequest) (string, error) {
			row := ctx.RequestPermission(req.ToolName, "", req.Title)
			if row == nil {
				return "", fmt.Errorf("acp gate: filing permission request for %q returned no row", req.ToolName)
			}
			return row.ID, nil
		},
		Status: func(pollCtx context.Context, id string) (acp.RequestStatus, error) {
			row, err := ctx.PermissionStatus(pollCtx, id)
			if err != nil {
				return acp.StatusPending, err
			}
			// outcome stays unset until a human resolves the request;
			// repo.OutcomeGranted is the only value that authorizes the call, and
			// an expired one is a refusal like any other.
			if row == nil || row.Outcome == nil || *row.Outcome == "" {
				return acp.StatusPending, nil
			}
			if *row.Outcome == repo.OutcomeGranted {
				return acp.StatusGranted, nil
			}
			return acp.StatusDenied, nil
		},
	}
	return gate.Decide
}
