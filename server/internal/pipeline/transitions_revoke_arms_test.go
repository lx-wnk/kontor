package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
)

// TestApplyTransition_EveryEndedRunArmRevokesIt extends
// TestApplyTransition_DoneCommits_RevokesTheCompletedRun to the four other
// arms of applyTransitionWrites that end a stage run: next, fail, and both
// branches of iterate. Each arm appends its own postCommit revocation hook,
// so each needs its own case — deleting any single one of them compiles, vets
// and, without this table, passes.
//
// maxIterations is what selects between the two iterate branches: the run is
// created at iteration 0, so maxIterations 1 hits the limit branch and 5 takes
// the ordinary next-round branch.
func TestApplyTransition_EveryEndedRunArmRevokesIt(t *testing.T) {
	cases := []struct {
		name          string
		maxIterations int
		transition    pipeline.StageTransition
	}{
		{"next", 3, pipeline.NextTransition{Stage: "review"}},
		{"fail", 3, pipeline.FailTransition{Reason: "boom"}},
		{"iterateLimitReached", 1, pipeline.IterateTransition{}},
		{"iterateNextRound", 5, pipeline.IterateTransition{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			bundle := openSharedBundle(t)
			c := bundle.Client

			taskRepo := repo.NewTaskRepo(c)
			srRepo := repo.NewStageRunRepo(c)
			revokes := &recordedRevokes{}

			orch, err := pipeline.NewOrchestrator(pipeline.OrchestratorOptions{
				TaskRepo:          taskRepo,
				StageRunRepo:      srRepo,
				PermissionRepo:    repo.NewPermissionRepo(c),
				AuditRepo:         repo.NewAuditEventRepo(c),
				ConfigRepo:        repo.NewPipelineConfigRepo(c),
				Client:            c,
				RevokeTaskAPIKeys: revokes.revoke,
			})
			require.NoError(t, err)

			task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
				Slug:          "revoke-arm-" + tc.name,
				Title:         "Revoke Arm " + tc.name,
				Cwd:           "/tmp",
				CurrentStage:  "implementation",
				Priority:      "medium",
				MaxIterations: tc.maxIterations,
			})
			require.NoError(t, err)

			sr, err := srRepo.Create(ctx, repo.CreateStageRunInput{
				TaskID:      task.ID,
				Stage:       "implementation",
				Iteration:   0,
				SessionName: "revoke-arm-" + tc.name + "-session",
			})
			require.NoError(t, err)

			orch.SeedTaskLockForTest(task.ID)

			_, applyErr := orch.ApplyTransitionForTest(ctx, task, sr, tc.transition)
			require.NoError(t, applyErr)

			// Exactly the ended run, and only it: the iterate next-round
			// branch creates a successor run whose credential must survive.
			require.Equal(t, []string{sr.ID}, revokes.all(),
				"the %s arm must revoke the credentials of the run it ended", tc.name)
		})
	}
}
