package pipeline_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

func TestJobPrompt_CarriesTitleDescriptionAndContract(t *testing.T) {
	desc := "Sort new mail"
	bundle := pipeline.JobPrompt(&ent.Task{Title: "Triage inbox", Description: &desc})

	require.Contains(t, bundle.UserPrompt, "Triage inbox")
	require.Contains(t, bundle.UserPrompt, "Sort new mail")
	require.Contains(t, bundle.UserPrompt, "set_stage_output")
	require.Contains(t, bundle.UserPrompt, `"summary"`)
	require.Contains(t, bundle.UserPrompt, `"result"`)

	require.True(t, strings.HasPrefix(bundle.SystemPrompt, "You are an agent working inside a structured task pipeline."))
	lower := strings.ToLower(bundle.SystemPrompt)
	for _, forbidden := range []string{"git", "commit", "worktree", "self_review", "finalization"} {
		require.NotContains(t, lower, forbidden)
	}
}

func TestValidateStageOutput_Job(t *testing.T) {
	ok := pipeline.ValidateStageOutput("job", map[string]any{"summary": "done", "result": "3 mails"})
	require.True(t, ok.OK)

	missingSummary := pipeline.ValidateStageOutput("job", map[string]any{"result": "3 mails"})
	require.False(t, missingSummary.OK)
	require.Contains(t, missingSummary.Error, "summary")

	emptySummary := pipeline.ValidateStageOutput("job", map[string]any{"summary": "", "result": "3 mails"})
	require.False(t, emptySummary.OK)

	missingResult := pipeline.ValidateStageOutput("job", map[string]any{"summary": "done"})
	require.False(t, missingResult.OK)

	wrongResultType := pipeline.ValidateStageOutput("job", map[string]any{"summary": "done", "result": 3})
	require.False(t, wrongResultType.OK)
}

func TestStageHandlers_RegistersJob(t *testing.T) {
	handler := pipeline.HandlersByStage["job"]
	require.NotNil(t, handler)
	require.True(t, handler.RequiresAgent())
}
