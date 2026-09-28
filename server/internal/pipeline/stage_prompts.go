package pipeline

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// sharedContext is prepended to every stage system prompt. It names
// set_stage_output because the system prompt is the instruction that
// survives a resume: the stage prompt carrying the same instruction is
// replaced wholesale by resumeContinueInstruction, so a run past iteration 0
// would otherwise be told only to use the fence. Saying "wrap it in a fence"
// here and "call the tool" in the stage prompt is what made the fence the
// only channel a stage result ever travelled.
const sharedContext = `You are an agent working inside a structured task pipeline. A human orchestrator will review your output at specific stages. Be concise, actionable, and honest about uncertainty. Submit structured output by calling the ` + "`set_stage_output`" + ` MCP tool — it is validated against the stage schema and is the channel the orchestrator reads first. Only if that tool is genuinely unavailable to you, fall back to wrapping the same object in a fenced ` + "```json ... ```" + ` block.`

const upfrontPermissionsDirective = `## Permissions — declare upfront, in bulk (CRITICAL FIRST STEP)

Before any tool call, scan your task description and the work ahead. Build the FULL list of tools you anticipate needing — file ops (Read/Write/Edit/MultiEdit/Glob/Grep/LS), Bash patterns (e.g. ` + "`pnpm test*`" + `, ` + "`pnpm lint*`" + `, ` + "`git commit*`" + `), WebFetch URLs, etc.

Then call the ` + "`request_permission`" + ` MCP tool ONCE with the full ` + "`permissions: [...]`" + ` array. The dashboard auto-resolves any entries already pre-granted on the task — only truly new entries surface as ON HOLD.

NEVER write prose like "please grant me X" — only ` + "`request_permission`" + ` is actionable.`

// ImplementationPrompt builds the system+user prompt for the implementation stage.
func ImplementationPrompt(t *ent.Task, conceptOutput map[string]any, reviewFeedback string, allowGitPushGlobal bool) PromptBundle {
	allowGitPush := IsGitPushAllowed(t, allowGitPushGlobal)
	pushLine := "Commit your work via git when done — but NEVER `git push`; pushing is the user's responsibility."
	if allowGitPush {
		pushLine = "Commit AND push (`git push`) are permitted for this task — push your feature branch when work is complete."
	}
	systemPrompt := fmt.Sprintf("%s\n\nYou are the orchestrator for this task's implementation phase. Use the Task tool to dispatch subagents for parallel work when beneficial. %s\n\n%s",
		sharedContext, pushLine, upfrontPermissionsDirective)

	conceptJSON, _ := json.MarshalIndent(conceptOutput, "", "  ")
	feedbackBlock := ""
	if reviewFeedback != "" {
		feedbackBlock = fmt.Sprintf("\n\n## Review Feedback From Previous Iteration\n%s\n\nAddress this feedback in your next attempt.", reviewFeedback)
	}
	userPrompt := fmt.Sprintf(`## Task: %s

%s

## Concept (spec, plan, toolRequests)
`+"```json\n%s\n```"+`%s

## Your Job: Implement

Work step-by-step through the concept plan. Commit each logical change via git.

When finished, submit your result as your FINAL action by calling the `+"`set_stage_output`"+` MCP tool with an `+"`output`"+` object of exactly this shape:
{"summary": string, "commits": string[], "openItems": string[]}
If `+"`set_stage_output`"+` is unavailable, instead emit the same object as a `+"```json```"+` block.`,
		t.Title,
		strOrEmpty(t.Description),
		string(conceptJSON),
		feedbackBlock,
	)
	return PromptBundle{SystemPrompt: systemPrompt, UserPrompt: userPrompt}
}

// SelfReviewPrompt builds the prompt for the self_review stage.
// Self-review is read-only: no upfrontPermissionsDirective — agents must not
// call request_permission here. Doing so causes an awaiting_user state that the
// dead-PID reaper immediately fails when the reviewer exits normally.
func SelfReviewPrompt(t *ent.Task, implementationOutput map[string]any) PromptBundle {
	implJSON, _ := json.MarshalIndent(implementationOutput, "", "  ")
	return PromptBundle{
		SystemPrompt: sharedContext,
		UserPrompt: fmt.Sprintf(`## Task: %s

%s

## Implementation Output
`+"```json\n%s\n```"+`

## Your Job: Self-Review

Review the implementation against:
1. Original task requirements — are they all met?
2. Security — any injection, XSS, SQL, auth bypass, secrets leaked?
3. Code quality — DRY violations, dead code, missing error handling?
4. Test coverage — are the changes tested?

Submit your result as your FINAL action by calling the `+"`set_stage_output`"+` MCP tool with an `+"`output`"+` object of exactly this shape: {"passed": bool, "findings": [{"severity": "high"|"medium"|"low", "description": string, "file": string|null}], "summary": string}. If `+"`set_stage_output`"+` is unavailable, instead emit the same object as a `+"```json```"+` block.`,
			t.Title, strOrEmpty(t.Description), string(implJSON)),
	}
}

// FinalizationPrompt builds the prompt for the finalization stage.
func FinalizationPrompt(t *ent.Task, stageRuns []*ent.StageRun) PromptBundle {
	var history string
	for _, r := range stageRuns {
		history += fmt.Sprintf("%s (iter %d): %s\n", r.Stage, r.Iteration, r.Status)
	}
	return PromptBundle{
		SystemPrompt: sharedContext,
		UserPrompt: fmt.Sprintf(`## Task: %s

%s

## Stage History
%s

## Your Job: Final Report

Produce a user-facing summary of what was done. Include:
- Short insights or lessons learned
- Known open todos or caveats
- Concrete test steps the user can run to verify the change

Submit your result as your FINAL action by calling the `+"`set_stage_output`"+` MCP tool with an `+"`output`"+` object of exactly this shape: {"summary": string, "insights": string[], "openTodos": string[], "testPlan": string[]}. If `+"`set_stage_output`"+` is unavailable, instead emit the same object as a `+"```json```"+` block.`,
			t.Title, strOrEmpty(t.Description), history),
	}
}

const jobPermissionsDirective = `## Permissions — declare upfront, in bulk

Before any tool call, list every tool this job will need — file tools, Bash patterns, WebFetch URLs, MCP application tools. Then call ` + "`request_permission`" + ` ONCE with the full ` + "`permissions: [...]`" + ` array. A human decides and the job continues afterwards. NEVER write prose like "please grant me X" — only ` + "`request_permission`" + ` is actionable.`

// JobPrompt builds the prompt for a job: one agent run for a routine's
// instruction, with no repository or stage sequence around it.
func JobPrompt(t *ent.Task) PromptBundle {
	systemPrompt := sharedContext + "\n\nYou run one unattended job for a routine. Work in the current directory.\n\n" + jobPermissionsDirective
	userPrompt := fmt.Sprintf(`## Job: %s

%s

When finished, submit your result as your FINAL action by calling the `+"`set_stage_output`"+` MCP tool with an `+"`output`"+` object of exactly this shape:
{"summary": string, "result": string}
"summary" says in one or two sentences what you did; "result" holds what the routine produced.
If `+"`set_stage_output`"+` is unavailable, instead emit the same object as a `+"```json```"+` block.`,
		t.Title, strOrEmpty(t.Description))
	return PromptBundle{SystemPrompt: systemPrompt, UserPrompt: userPrompt}
}

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// BuildFeedbackPrefix prepends a correction block to a stage's user prompt
// when the previous iteration's output was schema-rejected.
func BuildFeedbackPrefix(priorOutput map[string]any) string {
	if priorOutput == nil {
		return ""
	}
	validationErr, ok := priorOutput["validation_error"].(string)
	if !ok {
		return ""
	}
	const rejectedPreviewChars = 2000
	rejectedBlock := ""
	if rejected, hasRejected := priorOutput["rejected_output"]; hasRejected {
		full, _ := json.MarshalIndent(rejected, "", "  ")
		truncated := string(full)
		if len(truncated) > rejectedPreviewChars {
			truncated = truncated[:rejectedPreviewChars] + fmt.Sprintf("\n… (truncated, %d chars elided)", len(truncated)-rejectedPreviewChars)
		}
		rejectedBlock = fmt.Sprintf("\n\nYour previous response was:\n```json\n%s\n```", truncated)
	}
	return fmt.Sprintf("## CORRECTION REQUIRED\n\nYour previous attempt was rejected with: **%s**.%s\n\nStick EXACTLY to the schema described below. Do not add or rename fields.\n\n---\n\n", validationErr, rejectedBlock)
}

// buildCustomSystemPrompt queries the SystemPromptRepo for global prompts matching the given stage,
// combines them highest-priority-first, and returns the combined text separated by dividers.
// Returns "" if the repo is nil or no prompts match.
func buildCustomSystemPrompt(sc *StageContext, stage string) string {
	if sc.SystemPromptRepo == nil {
		return ""
	}
	prompts, err := sc.SystemPromptRepo.ListForStage(sc.Ctx, stage)
	if err != nil {
		slog.Warn("buildCustomSystemPrompt: DB query failed", "stage", stage, "err", err)
		return ""
	}
	if len(prompts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(prompts))
	for _, p := range prompts {
		parts = append(parts, p.Content)
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// SummarizeReviewFindings extracts a short actionable feedback string from a self_review output.
func SummarizeReviewFindings(output map[string]any) string {
	summary, _ := output["summary"].(string)
	findings, _ := output["findings"].([]any)
	var lines []string
	for _, f := range findings {
		fm, ok := f.(map[string]any)
		if !ok {
			continue
		}
		severity, _ := fm["severity"].(string)
		description, _ := fm["description"].(string)
		file, _ := fm["file"].(string)
		fileStr := ""
		if file != "" {
			fileStr = fmt.Sprintf(" (%s)", file)
		}
		if severity == "" {
			severity = "ISSUE"
		}
		lines = append(lines, fmt.Sprintf("- [%s] %s%s", severity, description, fileStr))
	}
	result := summary
	if len(lines) > 0 {
		if result != "" {
			result += "\n"
		}
		for i, l := range lines {
			if i > 0 {
				result += "\n"
			}
			result += l
		}
	}
	return result
}

// PlanReviewPrompt builds the prompt for the plan_review stage.
// The agent drafts a concrete execution plan, self-reviews it for completeness
// and concept fidelity, then emits only the final vetted plan as output.
func PlanReviewPrompt(t *ent.Task, conceptOutput map[string]any, reviewFeedback string) PromptBundle {
	conceptJSON, _ := json.MarshalIndent(conceptOutput, "", "  ")

	feedbackBlock := ""
	if reviewFeedback != "" {
		feedbackBlock = fmt.Sprintf("\n\n## Reviewer Feedback — Incorporate This\n%s", reviewFeedback)
	}

	userPrompt := fmt.Sprintf(`## Task: %s

%s

## Approved Concept (spec, plan, context)
`+"```json\n%s\n```"+`%s

## Your Job: Draft → Self-Review → Finalize

**Phase 1 — Draft:** Produce a concrete execution plan: files to create/modify, ordered implementation steps, and test approach.

**Phase 2 — Self-Review (critique your draft):** Check for:
1. Completeness — does the plan cover every requirement in the concept?
2. Concept fidelity — does every step follow from the approved spec?
3. Missing or incorrect files?
4. Test gaps — are new behaviours covered?

**Phase 3 — Rewrite:** Incorporate your critique and produce a final vetted plan.

Output ONLY the final vetted plan as your FINAL action by calling the `+"`set_stage_output`"+` MCP tool with an `+"`output`"+` object of exactly this shape:
{"summary": string, "steps": string[], "filesTouched": string[], "testApproach": string}
If `+"`set_stage_output`"+` is unavailable, instead emit the same object as a `+"```json```"+` block.`,
		t.Title,
		strOrEmpty(t.Description),
		string(conceptJSON),
		feedbackBlock,
	)
	return PromptBundle{SystemPrompt: sharedContext, UserPrompt: userPrompt}
}
