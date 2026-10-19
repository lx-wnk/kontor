package plan_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/plan"
	"github.com/lx-wnk/kontor/server/internal/auth"
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

// newRunningPlanRouter mounts the plan routes over a plan_review task whose run
// is still running with no output.
func newRunningPlanRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	taskRepo := repo.NewTaskRepo(bundle.Client)
	srRepo := repo.NewStageRunRepo(bundle.Client)
	taskID, _ := seedPlanReviewRun(t, ctx, taskRepo, srRepo, "running", nil)

	h := plan.NewHandler(plan.HandlerDeps{
		Turns:     repo.NewRefinementTurnRepo(bundle.Client),
		Tasks:     taskRepo,
		StageRuns: srRepo,
		Advance:   func(context.Context, string) error { return nil },
		Requeue:   func(context.Context, string, string) (bool, error) { return true, nil },
	})
	r := chi.NewRouter()
	h.Mount(r)
	return r, taskID
}

func TestApproveRoute_RunningRun_Returns409(t *testing.T) {
	r, taskID := newRunningPlanRouter(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/plan/"+taskID+"/approve", nil))

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Contains(t, body, "error")
}

func TestRejectRoute_RunningRun_Returns409(t *testing.T) {
	r, taskID := newRunningPlanRouter(t)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/plan/"+taskID+"/reject", strings.NewReader(`{"feedback":"needs more detail"}`))
	r.ServeHTTP(w, req)

	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	var body map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Contains(t, body, "error")
}

// The approving person is the JWT subject, or the bypass identity when auth is off.
func TestApproveRoute_AuditsTheSignedInUser(t *testing.T) {
	for name, tc := range map[string]struct {
		secret   string
		wantUser string
	}{
		"bypass": {wantUser: auth.BypassUserID},
		"jwt":    {secret: "test-secret", wantUser: "gh-77"},
	} {
		t.Run(name, func(t *testing.T) {
			bundle, err := db.Open(":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = bundle.Client.Close() })

			ctx := context.Background()
			taskRepo := repo.NewTaskRepo(bundle.Client)
			srRepo := repo.NewStageRunRepo(bundle.Client)
			auditRepo := repo.NewAuditEventRepo(bundle.Client)
			taskID, _ := seedPlanReviewTask(t, ctx, taskRepo, srRepo)

			r := chi.NewRouter()
			if tc.secret != "" {
				r.Use(auth.RequireAuth(tc.secret))
			}
			plan.NewHandler(plan.HandlerDeps{
				Turns:     repo.NewRefinementTurnRepo(bundle.Client),
				Tasks:     taskRepo,
				StageRuns: srRepo,
				Advance:   func(context.Context, string) error { return nil },
				Audit:     auditRepo,
			}).Mount(r)

			req := httptest.NewRequest(http.MethodPost, "/api/plan/"+taskID+"/approve", nil)
			if tc.secret != "" {
				token, err := auth.SignJWT(auth.JWTPayload{Sub: "gh-77", Login: "octo@example.com"}, tc.secret, 60)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+token)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())

			events, err := auditRepo.ListForTask(ctx, taskID)
			require.NoError(t, err)
			require.Len(t, events, 1)
			require.Equal(t, tc.wantUser, *events[0].UserID)
			require.Equal(t, "user", events[0].Metadata["actor"])
			require.NotContains(t, events[0].Metadata, "login", "no login or email may be copied into the audit log")
			for _, v := range events[0].Metadata {
				require.NotEqual(t, "octo@example.com", v)
			}
		})
	}
}
