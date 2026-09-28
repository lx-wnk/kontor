package pipeline_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// The two prompts that tell a stage agent how to hand its result back. Between
// them they have to state the primary channel on every run, including a resume.
//
// Measured 2026-09-05 on a build where the tool was permitted and discoverable:
// zero of twelve completed runs called it, because the system prompt said to
// wrap output in a fence and never named the tool, while the instruction that
// did name it lived only in the stage prompt — which resumeContinueInstruction
// replaces wholesale from iteration 1 onward.

func TestSharedContext_NamesTheOutputToolAndDemotesTheFence(t *testing.T) {
	sc := pipeline.SharedContextForTest

	require.Contains(t, sc, channelconfig.ToolSetStageOutput,
		"the system prompt is the only instruction that survives a resume, so it must name the primary channel")

	toolAt := strings.Index(sc, channelconfig.ToolSetStageOutput)
	fenceAt := strings.Index(sc, "```json")
	require.NotEqual(t, -1, fenceAt, "the fence must still be offered as the fallback")
	require.Less(t, toolAt, fenceAt,
		"the tool has to be stated before the fence, or the fence reads as the primary instruction")
}

func TestResumeContinueInstruction_RepeatsHowToSubmit(t *testing.T) {
	ri := pipeline.ResumeContinueInstructionForTest

	require.Contains(t, ri, channelconfig.ToolSetStageOutput,
		"a resume replaces the stage prompt, so this text is the only place left to state the channel")
	require.Contains(t, strings.ToLower(ri), "final action",
		"the submission must be stated as the closing step, matching the stage prompts")
}
