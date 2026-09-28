package pipeline_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// TestFilePermissionRequest_AutonomyAnswersForItself proves the orchestrator's
// permission-filing path resolves a request an allow-all task would never have
// been asked, instead of parking an agent in front of a question nobody owes an
// answer to. The HTTP path has always done this; the orchestrator's did not, so
// an ACP-spawned stage raised a pending row for every tool call it made.
func TestFilePermissionRequest_AutonomyAnswersForItself(t *testing.T) {
	cases := []struct {
		name        string
		autonomy    string
		tool        string
		wantGranted bool
	}{
		{"full autonomy answers itself", "full", "Bash", true},
		{"spec_gated autonomy answers itself", "spec_gated", "Bash", true},
		{"manual autonomy still asks", "manual", "Bash", false},
		{"an application tool asks even under full autonomy", "full", "mcp__mail__send", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			bundle := openSharedBundle(t)
			orch, taskRepo := makeOrchFromBundle(t, bundle)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			permRepo := repo.NewPermissionRepo(bundle.Client)

			autonomy := tc.autonomy
			task, err := taskRepo.Create(ctx, repo.CreateTaskInput{
				Slug:                "autogrant-" + tc.autonomy,
				Title:               "Auto-grant",
				Cwd:                 "/tmp",
				CurrentStage:        "implementation",
				Priority:            "medium",
				MaxIterations:       3,
				StageTimeoutSeconds: 1800,
				Autonomy:            &autonomy,
			})
			require.NoError(t, err)

			run, err := srRepo.Create(ctx, repo.CreateStageRunInput{TaskID: task.ID, Stage: "implementation"})
			require.NoError(t, err)

			req := orch.FilePermissionRequestForTest(ctx, task, run.ID, tc.tool, "", "because")
			require.NotNil(t, req, "the row must be written either way — the gate polls it")

			stored, err := permRepo.GetPermissionRequest(ctx, req.ID)
			require.NoError(t, err)

			if tc.wantGranted {
				require.NotNil(t, stored.Outcome, "an allow-all task must not leave a question open")
				require.Equal(t, repo.OutcomeGranted, *stored.Outcome)
			} else {
				require.True(t, stored.Outcome == nil || *stored.Outcome == "",
					"a human still owes this one an answer, got %v", stored.Outcome)
			}
		})
	}
}
