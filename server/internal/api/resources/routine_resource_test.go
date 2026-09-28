package resources_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	apiresources "github.com/lx-wnk/kontor/server/internal/api/resources"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

const testSecret = "routine-resource-test-secret"

// newRoutineResourceMux wires a handler backed by real schedule+resource repos.
// bypassAuth controls whether routine listing is narrowed to the calling user.
func newRoutineResourceMux(t *testing.T, bypassAuth bool) (*chi.Mux, repo.TaskScheduleRepo, repo.ResourceRepo, context.Context) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	ctx := context.Background()
	repo.SeedCapabilities(ctx, repo.NewCapabilityRepo(bundle.Client))
	resourceRepo := repo.NewResourceRepo(bundle.Client)
	schedRepo := repo.NewTaskScheduleRepo(bundle.Client)

	mux := chi.NewRouter()
	if !bypassAuth {
		mux.Use(auth.RequireAuth(testSecret))
	}
	apiresources.NewHandler(resourceRepo, schedRepo, memory.Gate{
		Capabilities: repo.NewCapabilityRepo(bundle.Client),
		Grants:       repo.NewGrantRepo(bundle.Client),
		GrantUsage:   repo.NewGrantUsageRepo(bundle.Client, bundle.WriteClient),
	}, bypassAuth).Mount(mux)
	return mux, schedRepo, resourceRepo, ctx
}

func mkScheduleWithUser(t *testing.T, r repo.TaskScheduleRepo, name string, enabled bool, userID *string) string {
	t.Helper()
	s, err := r.Create(context.Background(), repo.CreateTaskScheduleInput{
		Name:                name,
		CronExpr:            "0 9 * * *",
		SlugPrefix:          name,
		Title:               name,
		Cwd:                 "/tmp",
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
		UserID:              userID,
	})
	require.NoError(t, err)
	if !enabled {
		_, err = r.SetEnabled(context.Background(), s.ID, false)
		require.NoError(t, err)
	}
	return s.ID
}

func authedGet(t *testing.T, mux *chi.Mux, path, userID string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	tok, err := auth.SignJWT(auth.JWTPayload{Sub: userID, Login: userID}, testSecret, 3600)
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
	mux.ServeHTTP(w, req)
	return w
}

// TestList_RoutineReturnsResourceRows verifies that kind=routine returns
// persisted resource rows (not projections) with correct kind, state and slug.
func TestList_RoutineReturnsResourceRows(t *testing.T) {
	mux, schedules, _, _ := newRoutineResourceMux(t, true)
	id := mkScheduleWithUser(t, schedules, "nightly", true, nil)

	w := get(t, mux, "/api/resources?kind=routine")
	require.Equal(t, http.StatusOK, w.Code)

	rows := decodeList(t, w)
	require.Len(t, rows, 1)
	require.Equal(t, repo.ResourceKindRoutine, rows[0]["kind"])
	require.Equal(t, "nightly", rows[0]["name"])
	require.Equal(t, repo.ResourceStateEnabled, rows[0]["state"])
	require.Equal(t, string(repo.ScopeGlobal), rows[0]["scopeKind"])
	// Slug is the schedule UUID, not slug_prefix
	require.Equal(t, id, rows[0]["slug"])
	// OriginRef is the schedule UUID
	require.Equal(t, id, rows[0]["originRef"])
	// ID is the resource UUID, not the schedule UUID
	require.NotEqual(t, id, rows[0]["id"])
}

// TestList_RoutineReportsDisabledState verifies enabled/disabled distinction.
func TestList_RoutineReportsDisabledState(t *testing.T) {
	mux, schedules, _, _ := newRoutineResourceMux(t, true)
	mkScheduleWithUser(t, schedules, "paused", false, nil)

	rows := decodeList(t, get(t, mux, "/api/resources?kind=routine"))
	require.Len(t, rows, 1)
	require.Equal(t, repo.ResourceStateDisabled, rows[0]["state"])
}

// TestList_RoutineWithNoSchedulesIsAnEmptyArray keeps the null-safety guarantee.
func TestList_RoutineWithNoSchedulesIsAnEmptyArray(t *testing.T) {
	mux, _, _, _ := newRoutineResourceMux(t, true)

	w := get(t, mux, "/api/resources?kind=routine")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "[]\n", w.Body.String())
}

// TestList_RoutineUserIsolation proves that user A's routines are not returned
// to user B. Goes RED if the per-user narrowing is removed from
// routineResources.
func TestList_RoutineUserIsolation(t *testing.T) {
	mux, schedules, _, _ := newRoutineResourceMux(t, false)

	alice := "alice"
	bob := "bob"
	mkScheduleWithUser(t, schedules, "alice-routine", true, &alice)
	mkScheduleWithUser(t, schedules, "bob-routine", true, &bob)

	// Alice sees only her routine
	aliceRows := decodeList(t, authedGet(t, mux, "/api/resources?kind=routine", "alice"))
	require.Len(t, aliceRows, 1, "alice should see exactly 1 routine")
	require.Equal(t, "alice-routine", aliceRows[0]["name"])

	// Bob sees only his routine
	bobRows := decodeList(t, authedGet(t, mux, "/api/resources?kind=routine", "bob"))
	require.Len(t, bobRows, 1, "bob should see exactly 1 routine")
	require.Equal(t, "bob-routine", bobRows[0]["name"])
}
