package pipeline

import (
	"fmt"
	"os"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/proc"
)

const agentMessageMaxChars = 2000

// isRateLimitError returns true when the API error represents a rate or usage limit.
// Matches by HTTP status (429/529/503) or by the structured error kind field.
func isRateLimitError(e *APIError) bool {
	if e == nil {
		return false
	}
	if e.Status == 429 || e.Status == 529 || e.Status == 503 {
		return true
	}
	return e.Kind == "rate_limit" || e.Kind == "overloaded_error"
}

type ValidationResult struct {
	OK    bool
	Error string
}

func ValidateStageOutput(stage string, output map[string]any) ValidationResult {
	switch stage {
	case "self_review":
		return validateSelfReview(output)
	case "finalization":
		return validateFinalization(output)
	case StageJob:
		return validateJob(output)
	default:
		return ValidationResult{OK: true}
	}
}

func missing(field string) ValidationResult {
	return ValidationResult{OK: false, Error: fmt.Sprintf("missing required field: %s", field)}
}

func validateSelfReview(o map[string]any) ValidationResult {
	if _, ok := o["passed"].(bool); !ok {
		return missing("passed (boolean)")
	}
	if _, ok := o["findings"].([]any); !ok {
		return missing("findings (array)")
	}
	if _, ok := o["summary"].(string); !ok {
		return missing("summary (string)")
	}
	return ValidationResult{OK: true}
}

func validateFinalization(o map[string]any) ValidationResult {
	if _, ok := o["summary"].(string); !ok {
		return missing("summary (string)")
	}
	if _, ok := o["insights"].([]any); !ok {
		return missing("insights (string array)")
	}
	if _, ok := o["openTodos"].([]any); !ok {
		return missing("openTodos (string array)")
	}
	if _, ok := o["testPlan"].([]any); !ok {
		return missing("testPlan (string array)")
	}
	return ValidationResult{OK: true}
}

func validateJob(o map[string]any) ValidationResult {
	if s, ok := o["summary"].(string); !ok || s == "" {
		return missing("summary (non-empty string)")
	}
	if _, ok := o["result"].(string); !ok {
		return missing("result (string)")
	}
	return ValidationResult{OK: true}
}

type CompletionResult struct {
	Kind        string
	Output      map[string]any
	Error       string
	Retryable   bool
	Infra       bool
	RateLimited bool
}

type CompletionDeps struct {
	IsPidAlive  func(pid int) bool
	ReadOutput  func(cwd, sessionID string) (StageOutputRead, error)
	FindSession func(cwd, afterISO string) (string, error)
	PersistSID  func(stageRunID, sessionID string) error
	Validate    func(stage string, output map[string]any) ValidationResult
}

func DetectCompletion(sr *ent.StageRun, cwd string, deps CompletionDeps) (CompletionResult, error) {
	isPidAliveFn := deps.IsPidAlive
	if isPidAliveFn == nil {
		isPidAliveFn = proc.IsPidAlive
	}
	readOutputFn := deps.ReadOutput
	if readOutputFn == nil {
		readOutputFn = ReadLastStageJsonOutput
	}
	findSessionFn := deps.FindSession
	if findSessionFn == nil {
		findSessionFn = FindNewestSessionID
	}
	validateFn := deps.Validate
	if validateFn == nil {
		validateFn = ValidateStageOutput
	}

	pid := stageRunPid(sr)
	if isPidAliveFn(pid) {
		return CompletionResult{Kind: "still_running"}, nil
	}

	// Tool-written stage output: the agent submitted its result via the
	// set_stage_output MCP tool and the endpoint already validated it against
	// the per-stage schema. Use it directly — no JSONL scrape, no retry loop.
	// The synthetic-adapter marker (synthetic_session_file) is handled by the
	// block below, so exclude it here.
	if len(sr.Output) > 0 {
		if _, isSynthetic := sr.Output["synthetic_session_file"]; !isSynthetic {
			return CompletionResult{Kind: "completed", Output: sr.Output}, nil
		}
	}

	// Non-Claude adapters store the synthetic JSONL path in stage_run.output.
	// Use it directly instead of scanning ~/.claude/projects/...
	if sr.Output != nil {
		if syntheticFile, ok := sr.Output["synthetic_session_file"].(string); ok && syntheticFile != "" {
			if _, statErr := os.Stat(syntheticFile); statErr == nil {
				defer func() { _ = os.Remove(syntheticFile) }() // clean up synthetic session after reading
				read, err := ReadLastStageJsonOutputFromFile(syntheticFile)
				if err != nil {
					return CompletionResult{Kind: "failed", Error: fmt.Sprintf("synthetic session read error: %s", err), Infra: true}, nil
				}
				// A recovered, parseable output supersedes an earlier transient API error;
				// only requeue as rate-limited when no output was produced.
				if read.Output == nil && isRateLimitError(read.APIError) {
					out := map[string]any{}
					if read.RawText != "" {
						msg := read.RawText
						if len(msg) > agentMessageMaxChars {
							msg = msg[len(msg)-agentMessageMaxChars:]
						}
						out["agentMessage"] = msg
					}
					return CompletionResult{
						Kind:        "failed",
						Infra:       true,
						RateLimited: true,
						Error:       fmt.Sprintf("agent hit API rate/usage limit (status %d)", read.APIError.Status),
						Output:      out,
					}, nil
				}
				if read.Output == nil {
					if read.RawText != "" {
						trimmed := read.RawText
						if len(trimmed) > agentMessageMaxChars {
							trimmed = trimmed[len(trimmed)-agentMessageMaxChars:]
						}
						return CompletionResult{
							Kind:      "failed",
							Error:     "adapter did not produce a stage output block: it called neither set_stage_output nor emitted a ```json fence",
							Output:    map[string]any{"agentMessage": trimmed},
							Retryable: true,
						}, nil
					}
					return CompletionResult{Kind: "failed", Error: "no parseable json output in synthetic session", Infra: true}, nil
				}
				v := validateFn(sr.Stage, read.Output)
				if !v.OK {
					return CompletionResult{Kind: "failed", Error: v.Error, Output: read.Output, Retryable: true}, nil
				}
				return CompletionResult{Kind: "completed", Output: read.Output}, nil
			}
		}
	}

	sessionID := ""
	if sr.SessionID != nil {
		sessionID = *sr.SessionID
	}
	if sessionID == "" {
		if sr.StartedAt == nil {
			return CompletionResult{Kind: "failed", Error: "stage_run never started — cannot locate session", Infra: true}, nil
		}
		found, err := findSessionFn(cwd, sr.StartedAt.Format("2006-01-02T15:04:05Z"))
		if err != nil {
			return CompletionResult{Kind: "failed", Error: fmt.Sprintf("session lookup error: %s", err), Infra: true}, nil
		}
		sessionID = found
		if sessionID != "" && deps.PersistSID != nil {
			_ = deps.PersistSID(sr.ID, sessionID)
		}
	}

	if sessionID == "" {
		projectDir, _ := ResolvedProjectDir(cwd)
		return CompletionResult{
			Kind:  "failed",
			Error: fmt.Sprintf("no session JSONL found in %s after %v (cwd=%s)", projectDir, sr.StartedAt, cwd),
			Infra: true,
		}, nil
	}

	read, err := readOutputFn(cwd, sessionID)
	if err != nil {
		return CompletionResult{Kind: "failed", Error: fmt.Sprintf("session read error: %s", err), Infra: true}, nil
	}
	// A recovered, parseable output supersedes an earlier transient API error;
	// only requeue as rate-limited when no output was produced.
	if read.Output == nil && isRateLimitError(read.APIError) {
		out := map[string]any{}
		if read.RawText != "" {
			msg := read.RawText
			if len(msg) > agentMessageMaxChars {
				msg = msg[len(msg)-agentMessageMaxChars:]
			}
			out["agentMessage"] = msg
		}
		return CompletionResult{
			Kind:        "failed",
			Infra:       true,
			RateLimited: true,
			Error:       fmt.Sprintf("agent hit API rate/usage limit (status %d)", read.APIError.Status),
			Output:      out,
		}, nil
	}
	if read.Output == nil {
		if read.RawText != "" {
			trimmed := read.RawText
			if len(trimmed) > agentMessageMaxChars {
				trimmed = trimmed[len(trimmed)-agentMessageMaxChars:]
			}
			// Retryable, not Infra: the agent ran and produced text, it just
			// missed both output channels. Infra routes to a blind requeue that
			// burns the whole auto-retry budget without ever telling the agent
			// what was wrong; Retryable routes to IterateTransition, whose
			// feedback prefix quotes this error and the text below back to it,
			// and escalates to the user on the second miss instead of the Nth.
			return CompletionResult{
				Kind:      "failed",
				Error:     "agent did not produce a stage output block: it called neither set_stage_output nor emitted a ```json fence",
				Output:    map[string]any{"agentMessage": trimmed},
				Retryable: true,
			}, nil
		}
		return CompletionResult{Kind: "failed", Error: "no parseable json output in session tail", Infra: true}, nil
	}

	v := validateFn(sr.Stage, read.Output)
	if !v.OK {
		return CompletionResult{Kind: "failed", Error: v.Error, Output: read.Output, Retryable: true}, nil
	}
	return CompletionResult{Kind: "completed", Output: read.Output}, nil
}
