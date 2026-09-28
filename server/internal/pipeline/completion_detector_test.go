package pipeline_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func stageRun(stage string, pid *int, sessionID *string, startedAt *time.Time) *ent.StageRun {
	return &ent.StageRun{Stage: stage, Pid: pid, SessionID: sessionID, StartedAt: startedAt}
}

func TestDetectCompletion_StillRunning(t *testing.T) {
	sr := stageRun("implementation", ptr(1234), nil, ptr(time.Now()))
	deps := pipeline.CompletionDeps{IsPidAlive: func(pid int) bool { return pid == 1234 }}
	result, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "still_running", result.Kind)
}

func TestDetectCompletion_NoSession(t *testing.T) {
	now := time.Now()
	sr := stageRun("implementation", ptr(0), nil, &now)
	deps := pipeline.CompletionDeps{
		IsPidAlive:  func(int) bool { return false },
		FindSession: func(cwd, afterISO string) (string, error) { return "", nil },
	}
	result, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Kind)
}

func TestDetectCompletion_CompletedValid(t *testing.T) {
	now := time.Now()
	sessionID := "abc123"
	sr := stageRun("implementation", ptr(0), &sessionID, &now)
	deps := pipeline.CompletionDeps{
		IsPidAlive: func(int) bool { return false },
		ReadOutput: func(cwd, sid string) (pipeline.StageOutputRead, error) {
			return pipeline.StageOutputRead{
				Output:  map[string]any{"summary": "done", "commits": []any{"abc"}, "openItems": []any{}},
				RawText: "```json\n{}\n```",
			}, nil
		},
	}
	result, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "completed", result.Kind)
}

func TestDetectCompletion_SelfReviewSchemaFail_Retryable(t *testing.T) {
	now := time.Now()
	sessionID := "sid1"
	sr := stageRun("self_review", ptr(0), &sessionID, &now)
	deps := pipeline.CompletionDeps{
		IsPidAlive: func(int) bool { return false },
		ReadOutput: func(cwd, sid string) (pipeline.StageOutputRead, error) {
			return pipeline.StageOutputRead{
				Output:  map[string]any{"summary": "ok"},
				RawText: "```json\n{}\n```",
			}, nil
		},
	}
	result, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "failed", result.Kind)
	require.True(t, result.Retryable)
	require.Contains(t, result.Error, "passed")
}

func TestValidateStageOutput_SelfReview_Valid(t *testing.T) {
	v := pipeline.ValidateStageOutput("self_review", map[string]any{
		"passed":   true,
		"findings": []any{},
		"summary":  "all good",
	})
	require.True(t, v.OK)
}

func TestValidateStageOutput_SelfReview_MissingPassed(t *testing.T) {
	v := pipeline.ValidateStageOutput("self_review", map[string]any{
		"findings": []any{},
		"summary":  "ok",
	})
	require.False(t, v.OK)
	require.Contains(t, v.Error, "passed")
}

func TestValidateStageOutput_Finalization_Valid(t *testing.T) {
	v := pipeline.ValidateStageOutput("finalization", map[string]any{
		"summary":   "all done",
		"insights":  []any{"lesson one"},
		"openTodos": []any{},
		"testPlan":  []any{"run pnpm test"},
	})
	require.True(t, v.OK)
}

func TestValidateStageOutput_Finalization_MissingSummary(t *testing.T) {
	v := pipeline.ValidateStageOutput("finalization", map[string]any{
		"insights":  []any{},
		"openTodos": []any{},
		"testPlan":  []any{},
	})
	require.False(t, v.OK)
	require.Contains(t, v.Error, "summary")
}

func TestValidateStageOutput_Finalization_MissingInsights(t *testing.T) {
	v := pipeline.ValidateStageOutput("finalization", map[string]any{
		"summary":   "done",
		"openTodos": []any{},
		"testPlan":  []any{},
	})
	require.False(t, v.OK)
	require.Contains(t, v.Error, "insights")
}

func TestValidateStageOutput_Finalization_MissingOpenTodos(t *testing.T) {
	v := pipeline.ValidateStageOutput("finalization", map[string]any{
		"summary":  "done",
		"insights": []any{},
		"testPlan": []any{},
	})
	require.False(t, v.OK)
}

func TestValidateStageOutput_Finalization_MissingTestPlan(t *testing.T) {
	v := pipeline.ValidateStageOutput("finalization", map[string]any{
		"summary":   "done",
		"insights":  []any{},
		"openTodos": []any{},
	})
	require.False(t, v.OK)
}

func TestValidateStageOutput_Finalization_WrongType(t *testing.T) {
	// summary as int instead of string → must fail
	v := pipeline.ValidateStageOutput("finalization", map[string]any{
		"summary":   42,
		"insights":  []any{},
		"openTodos": []any{},
		"testPlan":  []any{},
	})
	require.False(t, v.OK)
}

// TestDetectCompletion_ToolOutput_UsedDirectly verifies that when sr.Output is
// populated (e.g. via set_stage_output MCP tool), DetectCompletion returns it
// directly without touching the JSONL scrape path.
func TestDetectCompletion_ToolOutput_UsedDirectly(t *testing.T) {
	sr := &ent.StageRun{
		ID:    "sr-1",
		Stage: "implementation",
		Pid:   ptr(1),
		Output: map[string]any{
			"summary":   "from tool",
			"commits":   []any{},
			"openItems": []any{},
		},
	}
	deps := pipeline.CompletionDeps{
		IsPidAlive: func(int) bool { return false },
		ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
			t.Fatal("scrape must not run when tool output is present")
			return pipeline.StageOutputRead{}, nil
		},
	}
	res, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "completed", res.Kind)
	require.Equal(t, "from tool", res.Output["summary"])
}

// TestDetectCompletion_SyntheticMarker_NotTreatedAsToolOutput verifies that an
// sr.Output containing "synthetic_session_file" is NOT short-circuited as
// tool output — it must fall through to the synthetic-file handling path.
// With a non-existent path and no session found, the result must be "failed".
func TestDetectCompletion_SyntheticMarker_NotTreatedAsToolOutput(t *testing.T) {
	sr := &ent.StageRun{
		ID:    "sr-2",
		Stage: "implementation",
		Pid:   ptr(1),
		Output: map[string]any{
			"synthetic_session_file": "/nonexistent/x.jsonl",
		},
	}
	now := time.Now()
	sr.StartedAt = &now
	deps := pipeline.CompletionDeps{
		IsPidAlive:  func(int) bool { return false },
		ReadOutput:  func(string, string) (pipeline.StageOutputRead, error) { return pipeline.StageOutputRead{}, nil },
		FindSession: func(string, string) (string, error) { return "", nil },
	}
	res, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "failed", res.Kind)
}

func TestDetectCompletion_InfraAxis(t *testing.T) {
	type tc struct {
		name      string
		sr        func() *ent.StageRun
		deps      pipeline.CompletionDeps
		wantKind  string
		wantInfra bool
		wantRetry bool
	}

	now := time.Now()
	sid := "sid-infra"

	cases := []tc{
		{
			name: "schema_validation_reject_retryable_not_infra",
			sr:   func() *ent.StageRun { return stageRun("self_review", ptr(0), &sid, &now) },
			deps: pipeline.CompletionDeps{
				IsPidAlive: func(int) bool { return false },
				ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
					return pipeline.StageOutputRead{
						Output:  map[string]any{"summary": "ok"},
						RawText: "```json\n{}\n```",
					}, nil
				},
			},
			wantKind:  "failed",
			wantRetry: true,
			wantInfra: false,
		},
		{
			name: "no_session_jsonl_found_is_infra",
			sr: func() *ent.StageRun {
				return stageRun("implementation", ptr(0), nil, &now)
			},
			deps: pipeline.CompletionDeps{
				IsPidAlive:  func(int) bool { return false },
				FindSession: func(string, string) (string, error) { return "", nil },
			},
			wantKind:  "failed",
			wantInfra: true,
		},
		{
			// The agent ran and produced text; it just missed both output
			// channels. That is a retryable protocol miss, not an
			// infrastructure fault — Infra would route it to a blind requeue
			// that never tells the agent what was wrong.
			name: "agent_produced_text_but_no_envelope_is_retryable_not_infra",
			sr:   func() *ent.StageRun { return stageRun("implementation", ptr(0), &sid, &now) },
			deps: pipeline.CompletionDeps{
				IsPidAlive: func(int) bool { return false },
				ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
					return pipeline.StageOutputRead{RawText: "some agent output without json block"}, nil
				},
			},
			wantKind:  "failed",
			wantRetry: true,
			wantInfra: false,
		},
		{
			// No text at all is a different thing: there is nothing to feed
			// back, so it stays on the infra path.
			name: "no_text_at_all_stays_infra",
			sr:   func() *ent.StageRun { return stageRun("implementation", ptr(0), &sid, &now) },
			deps: pipeline.CompletionDeps{
				IsPidAlive: func(int) bool { return false },
				ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
					return pipeline.StageOutputRead{}, nil
				},
			},
			wantKind:  "failed",
			wantInfra: true,
		},
		{
			name: "session_read_error_is_infra",
			sr:   func() *ent.StageRun { return stageRun("implementation", ptr(0), &sid, &now) },
			deps: pipeline.CompletionDeps{
				IsPidAlive: func(int) bool { return false },
				ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
					return pipeline.StageOutputRead{}, fmt.Errorf("disk read failed")
				},
			},
			wantKind:  "failed",
			wantInfra: true,
		},
		{
			name: "clean_valid_json_not_infra",
			sr:   func() *ent.StageRun { return stageRun("implementation", ptr(0), &sid, &now) },
			deps: pipeline.CompletionDeps{
				IsPidAlive: func(int) bool { return false },
				ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
					return pipeline.StageOutputRead{
						Output:  map[string]any{"summary": "done", "commits": []any{}, "openItems": []any{}},
						RawText: "```json\n{}\n```",
					}, nil
				},
			},
			wantKind:  "completed",
			wantInfra: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := pipeline.DetectCompletion(c.sr(), "/tmp", c.deps)
			require.NoError(t, err)
			require.Equal(t, c.wantKind, res.Kind)
			require.Equal(t, c.wantInfra, res.Infra, "Infra mismatch")
			require.Equal(t, c.wantRetry, res.Retryable, "Retryable mismatch")
		})
	}
}

func TestIsRateLimitError(t *testing.T) {
	cases := []struct {
		name string
		e    *pipeline.APIError
		want bool
	}{
		{"nil", nil, false},
		{"status_429", &pipeline.APIError{Status: 429, Kind: ""}, true},
		{"status_529", &pipeline.APIError{Status: 529, Kind: ""}, true},
		{"status_503", &pipeline.APIError{Status: 503, Kind: ""}, true},
		{"kind_rate_limit", &pipeline.APIError{Status: 0, Kind: "rate_limit"}, true},
		{"kind_overloaded_error", &pipeline.APIError{Status: 0, Kind: "overloaded_error"}, true},
		{"status_500_not_rl", &pipeline.APIError{Status: 500, Kind: ""}, false},
		{"empty_kind_not_rl", &pipeline.APIError{Status: 0, Kind: ""}, false},
		{"unknown_kind", &pipeline.APIError{Status: 0, Kind: "internal_error"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pipeline.IsRateLimitErrorForTest(c.e)
			require.Equal(t, c.want, got)
		})
	}
}

func TestDetectCompletion_RateLimited_ReturnsRateLimitedAndInfra(t *testing.T) {
	now := time.Now()
	sessionID := "rl-session"
	sr := stageRun("implementation", ptr(0), &sessionID, &now)
	deps := pipeline.CompletionDeps{
		IsPidAlive: func(int) bool { return false },
		ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
			return pipeline.StageOutputRead{
				APIError: &pipeline.APIError{Status: 429, Kind: "rate_limit"},
				RawText:  "You've hit your session limit · resets in 1h",
			}, nil
		},
	}
	res, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "failed", res.Kind)
	require.True(t, res.RateLimited, "RateLimited must be true for 429 API error")
	require.True(t, res.Infra, "Infra must be true for rate-limited result")
	require.NotEmpty(t, res.Error)
}

func TestDetectCompletion_RateLimitedThenRecovered_Completes(t *testing.T) {
	now := time.Now()
	sessionID := "rl-recovered"
	sr := stageRun("implementation", ptr(0), &sessionID, &now)
	deps := pipeline.CompletionDeps{
		IsPidAlive: func(int) bool { return false },
		ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
			return pipeline.StageOutputRead{
				APIError: &pipeline.APIError{Status: 429, Kind: "rate_limit"},
				Output:   map[string]any{"summary": "done", "commits": []any{"abc"}, "openItems": []any{}},
				RawText:  "```json\n{}\n```",
			}, nil
		},
	}
	res, err := pipeline.DetectCompletion(sr, "/tmp", deps)
	require.NoError(t, err)
	require.Equal(t, "completed", res.Kind, "a recovered output must complete despite an earlier API error")
	require.False(t, res.RateLimited)
}

// TestDetectCompletion_EnvelopeMiss_CarriesFeedback pins what makes the retry
// informed rather than blind. Routing to Retryable is only half the fix: the
// IterateTransition built from this result feeds `validation_error` and
// `rejected_output` into BuildFeedbackPrefix, so the error has to name what was
// missing and the output has to carry the agent's own text back to it. Drop
// either and the next attempt repeats the miss.
func TestDetectCompletion_EnvelopeMiss_CarriesFeedback(t *testing.T) {
	now := time.Now()
	sid := "sid-envelope"
	sr := stageRun("implementation", ptr(0), &sid, &now)

	res, err := pipeline.DetectCompletion(sr, "/tmp", pipeline.CompletionDeps{
		IsPidAlive: func(int) bool { return false },
		ReadOutput: func(string, string) (pipeline.StageOutputRead, error) {
			return pipeline.StageOutputRead{RawText: "I finished the work but forgot the envelope"}, nil
		},
	})
	require.NoError(t, err)

	require.True(t, res.Retryable, "an envelope miss must take the feedback path")
	require.False(t, res.Infra, "an envelope miss is not an infrastructure fault")
	require.Contains(t, res.Error, "set_stage_output",
		"the error is quoted back to the agent, so it must name the primary channel it skipped")
	require.Equal(t, "I finished the work but forgot the envelope", res.Output["agentMessage"],
		"the agent's own text must survive into the feedback, else the retry is blind")
}
