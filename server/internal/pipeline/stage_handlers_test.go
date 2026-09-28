package pipeline_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/llmadapter"
	"github.com/lx-wnk/kontor/server/internal/mcpapps"
	"github.com/lx-wnk/kontor/server/internal/memory"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/stretchr/testify/require"
)

func TestReadyHandler_TransitionsToImplementation(t *testing.T) {
	h := pipeline.GetHandlerForStage("ready")
	require.NotNil(t, h)
	require.False(t, h.RequiresAgent())

	audited := false
	ctx := &pipeline.StageContext{
		Ctx:      context.Background(),
		Task:     &ent.Task{Slug: "my-task", CurrentStage: "ready"},
		StageRun: &ent.StageRun{Stage: "ready"},
		RecordAudit: func(action string, _ map[string]any) {
			audited = true
		},
		RequestPermission: func(tool, pattern, reason string) *ent.PermissionRequest { return nil },
	}
	transition, err := h.Execute(ctx)
	require.NoError(t, err)
	next, ok := transition.(pipeline.NextTransition)
	require.True(t, ok)
	require.Equal(t, "implementation", next.Stage)
	require.True(t, audited)
}

func TestBacklogHandler_WaitsUser(t *testing.T) {
	h := pipeline.GetHandlerForStage("backlog")
	require.NotNil(t, h)
	require.False(t, h.RequiresAgent())

	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{},
		StageRun:          &ent.StageRun{},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
	}
	transition, err := h.Execute(ctx)
	require.NoError(t, err)
	_, ok := transition.(pipeline.WaitUserTransition)
	require.True(t, ok)
}

func TestBuildFeedbackPrefix_WithValidationError(t *testing.T) {
	prefix := pipeline.BuildFeedbackPrefix(map[string]any{
		"validation_error": "missing field: passed",
		"rejected_output":  map[string]any{"summary": "ok"},
	})
	require.Contains(t, prefix, "CORRECTION REQUIRED")
	require.Contains(t, prefix, "missing field: passed")
}

func TestBuildFeedbackPrefix_NoError(t *testing.T) {
	prefix := pipeline.BuildFeedbackPrefix(nil)
	require.Empty(t, prefix)
}

func TestBuildStageUserPrompt(t *testing.T) {
	const fullSpec = "## Task Spec\n\nDo the full thing."
	const extraInstruction = "also do this extra thing"

	tests := []struct {
		name             string
		resumeSessionID  string
		priorOutput      map[string]any
		additionalPrompt string
		wantContains     []string
		wantAbsent       []string
	}{
		{
			name:         "fresh run passes full spec through",
			wantContains: []string{fullSpec},
		},
		{
			name:            "resume omits full spec, injects continue instruction",
			resumeSessionID: "sess-abc123",
			wantAbsent:      []string{fullSpec},
			wantContains:    []string{pipeline.ResumeContinueInstructionForTest},
		},
		{
			name:             "user additional prompt appended on fresh run",
			additionalPrompt: extraInstruction,
			wantContains:     []string{fullSpec, extraInstruction},
		},
		{
			name:             "user additional prompt appended on resume",
			resumeSessionID:  "sess-def456",
			additionalPrompt: extraInstruction,
			wantContains:     []string{pipeline.ResumeContinueInstructionForTest, extraInstruction},
			wantAbsent:       []string{fullSpec},
		},
		{
			name: "prior iteration feedback preserved on fresh run",
			priorOutput: map[string]any{
				"validation_error": "missing field: passed",
				"rejected_output":  map[string]any{"x": 1},
			},
			wantContains: []string{"CORRECTION REQUIRED", "missing field: passed", fullSpec},
		},
		{
			name:            "prior iteration feedback preserved on resume",
			resumeSessionID: "sess-ghi789",
			priorOutput: map[string]any{
				"validation_error": "missing field: passed",
				"rejected_output":  map[string]any{"x": 1},
			},
			wantContains: []string{"CORRECTION REQUIRED", "missing field: passed", pipeline.ResumeContinueInstructionForTest},
			wantAbsent:   []string{fullSpec},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &pipeline.StageContext{
				ResumeSessionID:      tc.resumeSessionID,
				PriorIterationOutput: tc.priorOutput,
				UserAdditionalPrompt: tc.additionalPrompt,
			}
			bundle := pipeline.PromptBundle{UserPrompt: fullSpec}
			result := pipeline.BuildStageUserPromptForTest(ctx, bundle, pipeline.BuildFeedbackPrefix(ctx.PriorIterationOutput))
			for _, want := range tc.wantContains {
				require.Contains(t, result, want)
			}
			for _, absent := range tc.wantAbsent {
				require.NotContains(t, result, absent)
			}
		})
	}
}

// TestPlanReviewHandler_RegisteredAndRequiresAgent verifies the plan_review entry
// in NewStageHandlers is wired as an agent stage.
func TestPlanReviewHandler_RegisteredAndRequiresAgent(t *testing.T) {
	h := pipeline.GetHandlerForStage("plan_review")
	require.NotNil(t, h, "plan_review must be registered in NewStageHandlers")
	require.True(t, h.RequiresAgent(), "plan_review must be an agent-driven stage")
}

// TestPlanReviewBuilder_PromptContainsConceptAndSelfReview verifies that the
// prompt builder injects the task's concept metadata AND a self-review instruction.
func TestPlanReviewBuilder_PromptContainsConceptAndSelfReview(t *testing.T) {
	const conceptSpec = "Build the feature with these steps: A, B, C"
	task := &ent.Task{
		Title:    "My Plan Task",
		PlanMode: true,
		Metadata: map[string]any{"spec": conceptSpec},
	}
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              task,
		StageRun:          &ent.StageRun{Stage: "plan_review"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
	}
	bundle := pipeline.PlanReviewBuilderForTest(ctx)
	combined := bundle.SystemPrompt + "\n" + bundle.UserPrompt
	require.Contains(t, combined, conceptSpec, "prompt must embed the concept/spec content")
	require.Truef(t,
		strings.Contains(combined, "self-review") || strings.Contains(combined, "critique") || strings.Contains(combined, "self_review"),
		"prompt must contain a self-review or critique instruction, got:\n%s", combined)
}

// TestPlanReviewBuilder_IncludesFeedbackWhenPresent verifies that reject feedback
// stored under the metadata key is injected into the prompt.
func TestPlanReviewBuilder_IncludesFeedbackWhenPresent(t *testing.T) {
	const feedbackText = "The plan is missing error-handling steps"
	task := &ent.Task{
		Title:    "Feedback Task",
		PlanMode: true,
		Metadata: map[string]any{"planReviewFeedback": feedbackText},
	}
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              task,
		StageRun:          &ent.StageRun{Stage: "plan_review"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
	}
	bundle := pipeline.PlanReviewBuilderForTest(ctx)
	combined := bundle.SystemPrompt + "\n" + bundle.UserPrompt
	require.Contains(t, combined, feedbackText, "prompt must embed planReviewFeedback when present")
}

// TestPlanReviewBuilder_FeedbackSurfacesInPrompt is a parity regression test that
// pins the metadata key name used by planReviewBuilder to read reviewer feedback.
// If "planReviewFeedback" is ever renamed on the read side the sentinel will
// silently disappear from the prompt and this test will fail.
func TestPlanReviewBuilder_FeedbackSurfacesInPrompt(t *testing.T) {
	const sentinel = "SENTINEL_REVIEW_FEEDBACK_XYZ"
	task := &ent.Task{
		Title:    "Sentinel Task",
		PlanMode: true,
		Metadata: map[string]any{"planReviewFeedback": sentinel},
	}
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              task,
		StageRun:          &ent.StageRun{Stage: "plan_review"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
	}
	bundle := pipeline.PlanReviewBuilderForTest(ctx)
	combined := bundle.SystemPrompt + "\n" + bundle.UserPrompt
	require.Contains(t, combined, sentinel,
		"planReviewFeedback metadata value must reach the prompt — key mismatch would break the reject→rerun feedback loop")
}

// TestPlanReviewBuilder_NoFeedbackIsDefensive verifies that an absent
// planReviewFeedback key still produces a valid non-empty prompt.
func TestPlanReviewBuilder_NoFeedbackIsDefensive(t *testing.T) {
	task := &ent.Task{
		Title:    "No Feedback Task",
		PlanMode: true,
		Metadata: map[string]any{"spec": "some spec"},
	}
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              task,
		StageRun:          &ent.StageRun{Stage: "plan_review"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
	}
	bundle := pipeline.PlanReviewBuilderForTest(ctx)
	require.NotEmpty(t, bundle.UserPrompt, "prompt must be non-empty even without feedback")
}

// TestAgentStageHandler_IssueTaskAPIKeyErrorIsNotFatal proves the rule from
// stage_handlers.go: a credential-mint failure must not kill the spawn — the
// agent runs with the channel bridge alone (TaskAPIToken empty) instead.
func TestAgentStageHandler_IssueTaskAPIKeyErrorIsNotFatal(t *testing.T) {
	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 123}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Fix the retry loop", Cwd: "/tmp/proj-key-err", StageTimeoutSeconds: 1800},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-key-err"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		IssueTaskAPIKey: func(context.Context, string, time.Duration) (string, error) {
			return "", errors.New("mint boom")
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err, "a credential-mint failure must not fail the spawn")
	require.Empty(t, captured.TaskAPIToken, "a failed mint must leave TaskAPIToken empty")
}

// TestAgentStageHandler_IssueTaskAPIKeySuccessReachesSpawnOptions proves the
// minted token actually reaches SpawnAgentOptions.TaskAPIToken, with the
// stage run's own ID passed to the issuer.
func TestAgentStageHandler_IssueTaskAPIKeySuccessReachesSpawnOptions(t *testing.T) {
	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 123}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	var gotStageRunID string
	var gotTimeout time.Duration
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Fix the retry loop", Cwd: "/tmp/proj-key-ok", StageTimeoutSeconds: 1800},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-key-ok"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		IssueTaskAPIKey: func(_ context.Context, stageRunID string, timeout time.Duration) (string, error) {
			gotStageRunID = stageRunID
			gotTimeout = timeout
			return "tok", nil
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, "tok", captured.TaskAPIToken)
	require.Equal(t, "sr-key-ok", gotStageRunID)
	// StageTimeoutSeconds is an int count of SECONDS: taking the duration as
	// `_` here let the unit on the conversion drift unnoticed, and a key
	// minted with a millisecond TTL dies before its agent's first call.
	require.Equal(t, 1800*time.Second, gotTimeout,
		"the stage timeout must reach the issuer as seconds, not as raw units")
}

// TestAgentStageHandler_MemoryBlockInNativeUserPromptNotSystemPrompt is the
// seam test the brief asks for: the memory block must land in the final user
// prompt sent to the native `claude` spawn, and never in the system prompt —
// BuildSpawnArgs silently truncates the system prompt head-first at 10000
// characters, and since custom system-prompt content is prepended, a block
// there would risk deleting the stage instructions.
func TestAgentStageHandler_MemoryBlockInNativeUserPromptNotSystemPrompt(t *testing.T) {
	const memorySummary = "past lesson: always check the retry budget before requeueing"

	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 123}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	var recordedCandidates int
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Fix the retry loop", Cwd: "/tmp/proj-a"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-native-1"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		AuthorizeMemory:   func(_ context.Context, _ repo.Scope, _ string) error { return nil },
		InjectMemory: func(_ context.Context, _ memory.Query) ([]memory.Entry, error) {
			return []memory.Entry{{ID: "mem-1", Summary: memorySummary}}, nil
		},
		MemoryBudget: 2000,
		RecordMemoryInjection: func(_ context.Context, in repo.RecordInjectionInput) (*ent.MemoryInjection, error) {
			recordedCandidates = in.CandidateCount
			require.Equal(t, "sr-native-1", in.StageRunID)
			require.Equal(t, []string{"mem-1"}, in.EntryIDs)
			require.Equal(t, 2000, in.CharBudget)
			require.Greater(t, in.CharsUsed, 0)
			return &ent.MemoryInjection{}, nil
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)
	require.Contains(t, captured.Prompt, memorySummary, "memory block must land in the user prompt")
	require.NotContains(t, captured.SystemPrompt, memorySummary, "memory block must never land in the system prompt — that is the truncation trap")
	require.Equal(t, 1, recordedCandidates, "the injection record must know what was offered, not just what fit")
}

// TestAgentStageHandler_MemoryBlockInAdapterUserPromptNotSystemPrompt proves
// the same seam holds for the LLM-adapter dispatch path, which consumes the
// identical fullUserPrompt value as the native path.
func TestAgentStageHandler_MemoryBlockInAdapterUserPromptNotSystemPrompt(t *testing.T) {
	const memorySummary = "past lesson: rotate the API key before it expires"

	dir := t.TempDir()
	captureFile := filepath.Join(dir, "capture.json")
	scriptPath := filepath.Join(dir, "fake-adapter.sh")
	script := "#!/bin/sh\ncat > " + captureFile + "\necho '{}'\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	handler := pipeline.NewAgentStageHandlerForTest("implementation", func(pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		t.Fatal("native spawnFn must not be called on the adapter path")
		return pipeline.SpawnResult{}, nil
	})

	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Rotate credentials", Cwd: "/tmp/proj-b"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-adapter-1"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		ResolveSpawner: func(context.Context, string, string) (*ent.Spawner, error) {
			return &ent.Spawner{AdapterType: "custom", Command: scriptPath}, nil
		},
		AuthorizeMemory: func(_ context.Context, _ repo.Scope, _ string) error { return nil },
		InjectMemory: func(_ context.Context, _ memory.Query) ([]memory.Entry, error) {
			return []memory.Entry{{ID: "mem-2", Summary: memorySummary}}, nil
		},
		MemoryBudget: 2000,
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)

	raw, err := os.ReadFile(captureFile)
	require.NoError(t, err, "the adapter must have been invoked with args on stdin")
	var sent llmadapter.LLMSpawnArgs
	require.NoError(t, json.Unmarshal(raw, &sent))

	require.Contains(t, sent.UserPrompt, memorySummary, "memory block must land in the adapter's user prompt")
	require.NotContains(t, sent.SystemPrompt, memorySummary, "memory block must never land in the adapter's system prompt")
}

// TestAgentStageHandler_MemoryRetrievalErrorDoesNotBlockSpawn is this task's
// other fail-closed decision: a spawn must not be blocked by memory being
// unavailable. A retrieval error degrades to no block, not a failed stage.
func TestAgentStageHandler_MemoryRetrievalErrorDoesNotBlockSpawn(t *testing.T) {
	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 123}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	recordCalled := false
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Some task", Cwd: "/tmp/proj-c"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-err-1"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		AuthorizeMemory:   func(_ context.Context, _ repo.Scope, _ string) error { return nil },
		InjectMemory: func(_ context.Context, _ memory.Query) ([]memory.Entry, error) {
			return nil, errors.New("fts index unavailable")
		},
		MemoryBudget: 2000,
		RecordMemoryInjection: func(context.Context, repo.RecordInjectionInput) (*ent.MemoryInjection, error) {
			recordCalled = true
			return &ent.MemoryInjection{}, nil
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err, "a memory retrieval failure must not fail the spawn")
	require.Equal(t, "test", captured.Prompt, "prompt must fall back to the bundle's own content with no memory block")
	require.False(t, recordCalled, "nothing was retrieved, so there is nothing to record")
}

// TestAgentStageHandler_ZeroMemoryBudgetDisablesInjection is the other
// boundary named in the brief: a budget that cannot fit even one entry must
// disable the push outright rather than being read as "unbounded".
func TestAgentStageHandler_ZeroMemoryBudgetDisablesInjection(t *testing.T) {
	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 123}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	retrieveCalled := false
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Some task", Cwd: "/tmp/proj-d"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-zero-1"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		AuthorizeMemory:   func(_ context.Context, _ repo.Scope, _ string) error { return nil },
		InjectMemory: func(_ context.Context, _ memory.Query) ([]memory.Entry, error) {
			retrieveCalled = true
			return []memory.Entry{{ID: "mem-3", Summary: "should never be reached"}}, nil
		},
		MemoryBudget: 0,
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, "test", captured.Prompt)
	require.False(t, retrieveCalled, "a non-positive budget disables injection before retrieval is even attempted")
}

// TestAgentStageHandler_EffortFromAdapterConfigReachesSpawnArgs verifies the
// per-stage reasoning-effort setting travels from the resolved spawner's
// adapter_config through SpawnAgentOptions into the actual claude CLI args.
func TestAgentStageHandler_EffortFromAdapterConfigReachesSpawnArgs(t *testing.T) {
	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 1}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "t", Cwd: "/tmp/effort-a"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-effort-1"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		ResolveSpawner: func(context.Context, string, string) (*ent.Spawner, error) {
			return &ent.Spawner{AdapterType: "claude", AdapterConfig: map[string]string{"effort": "high"}}, nil
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)
	require.Equal(t, "high", captured.Effort)
	require.Contains(t, pipeline.BuildSpawnArgs(captured), "--effort")
}

// TestAgentStageHandler_UnrecognizedEffortOmitted is the fail-closed half of
// the same decision: a stored value outside the CLI's known levels must not
// be guessed into a flag the CLI would reject — the spawn proceeds without
// --effort entirely.
func TestAgentStageHandler_UnrecognizedEffortOmitted(t *testing.T) {
	var captured pipeline.SpawnAgentOptions
	spawnFn := func(opts pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		captured = opts
		return pipeline.SpawnResult{PID: 1}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "t", Cwd: "/tmp/effort-b"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-effort-2"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		ResolveSpawner: func(context.Context, string, string) (*ent.Spawner, error) {
			return &ent.Spawner{AdapterType: "claude", AdapterConfig: map[string]string{"effort": "ultra-mega"}}, nil
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)
	require.Empty(t, captured.Effort, "an unrecognised effort value must never be forwarded")
	require.NotContains(t, pipeline.BuildSpawnArgs(captured), "--effort")
}

// TestAgentStageHandler_UnsupportedAdapterEffortNeverForwarded proves effort
// never reaches a non-claude adapter, even when adapter_config carries a
// value — LLMSpawnArgs has no effort field, so this is enforced structurally,
// but the test pins the observable behaviour at the wire.
func TestAgentStageHandler_UnsupportedAdapterEffortNeverForwarded(t *testing.T) {
	dir := t.TempDir()
	captureFile := filepath.Join(dir, "capture.json")
	scriptPath := filepath.Join(dir, "fake-adapter.sh")
	script := "#!/bin/sh\ncat > " + captureFile + "\necho '{}'\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o755))

	handler := pipeline.NewAgentStageHandlerForTest("implementation", func(pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		t.Fatal("native spawnFn must not be called on the adapter path")
		return pipeline.SpawnResult{}, nil
	})

	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "t", Cwd: "/tmp/effort-c"},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-effort-3"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		ResolveSpawner: func(context.Context, string, string) (*ent.Spawner, error) {
			return &ent.Spawner{AdapterType: "custom", Command: scriptPath, AdapterConfig: map[string]string{"effort": "high"}}, nil
		},
	}

	_, err := handler.Execute(ctx)
	require.NoError(t, err)

	raw, err := os.ReadFile(captureFile)
	require.NoError(t, err, "the adapter must have been invoked with args on stdin")
	var sent map[string]any
	require.NoError(t, json.Unmarshal(raw, &sent))
	_, hasEffort := sent["effort"]
	require.False(t, hasEffort, "unsupported adapter must not receive the effort argument at all")
}
func TestAgentStageHandler_ApplicationResolutionFailureFailsBeforeSpawn(t *testing.T) {
	spawned := false
	spawnFn := func(pipeline.SpawnAgentOptions) (pipeline.SpawnResult, error) {
		spawned = true
		return pipeline.SpawnResult{PID: 123}, nil
	}
	handler := pipeline.NewAgentStageHandlerForTest("implementation", spawnFn)

	minted := false
	ctx := &pipeline.StageContext{
		Ctx:               context.Background(),
		Task:              &ent.Task{Title: "Reply to mail", Cwd: "/tmp/proj-apps", StageTimeoutSeconds: 1800},
		StageRun:          &ent.StageRun{Stage: "implementation", ID: "sr-apps"},
		RecordAudit:       func(string, map[string]any) {},
		RequestPermission: func(string, string, string) *ent.PermissionRequest { return nil },
		IssueTaskAPIKey: func(context.Context, string, time.Duration) (string, error) {
			minted = true
			return "key", nil
		},
		ResolveApplications: func(context.Context, *ent.Task) (mcpapps.RunApplications, error) {
			return mcpapps.RunApplications{}, &mcpapps.MissingSecretError{Server: "mail", EnvName: "MAIL_PASSWORD"}
		},
	}

	tr, err := handler.Execute(ctx)
	require.NoError(t, err)
	fail, ok := tr.(pipeline.FailTransition)
	require.True(t, ok, "got %T", tr)
	require.Contains(t, fail.Reason, "MAIL_PASSWORD")
	require.False(t, spawned, "a run must never start without the mailbox it was given")
	require.False(t, minted, "no credential is minted for a run that will not start")
}
