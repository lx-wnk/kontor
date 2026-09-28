package plan_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/plan"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// TestApproveRoute_WireFormat asserts the approve route answers the camelCase
// task shape usePlanReview.ts already types the body as, not the raw entity.
func TestApproveRoute_WireFormat(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	turnsRepo := repo.NewRefinementTurnRepo(bundle.Client)
	taskID, _ := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

	h := plan.NewHandler(plan.HandlerDeps{
		Turns:     turnsRepo,
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance:   func(context.Context, string) error { return nil },
	})
	r := chi.NewRouter()
	h.Mount(r)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/plan/"+taskID+"/approve", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var row map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &row))
	for _, k := range []string{"id", "slug", "title", "currentStage", "maxIterations", "silverBullet", "planMode", "createdAt", "updatedAt"} {
		require.Contains(t, row, k, "missing key %q in %v", k, row)
	}
	for _, k := range []string{"current_stage", "max_iterations", "silver_bullet", "plan_mode", "created_at", "updated_at", "edges"} {
		require.NotContains(t, row, k, "unexpected key %q in %v", k, row)
	}
	require.Equal(t, "implementation", row["currentStage"])
}
