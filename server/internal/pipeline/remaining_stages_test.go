package pipeline

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func TestRemainingAgentStages(t *testing.T) {
	agentStages := []string{"plan_review", "implementation", "self_review", "finalization", "job"}
	requiresAgent := func(stage string) bool { return slices.Contains(agentStages, stage) }

	require.NoError(t, RegisterTaskKindSequence("k", []string{"ready", "implementation", "done"}))
	t.Cleanup(ClearTaskKindSequences)

	tests := []struct {
		name string
		task *ent.Task
		want []string
	}{
		{"ready without plan mode", &ent.Task{CurrentStage: "ready"}, []string{"implementation", "self_review", "finalization"}},
		{"ready with plan mode", &ent.Task{CurrentStage: "ready", PlanMode: true}, []string{"plan_review", "implementation", "self_review", "finalization"}},
		{"self_review", &ent.Task{CurrentStage: "self_review"}, []string{"self_review", "finalization"}},
		{"job kind", &ent.Task{CurrentStage: "job", Kind: TaskKindJob}, []string{"job"}},
		{"registered kind", &ent.Task{CurrentStage: "ready", Kind: "k"}, []string{"implementation"}},
		{"terminal stage", &ent.Task{CurrentStage: "done"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, remainingAgentStages(tc.task, requiresAgent))
		})
	}
}
