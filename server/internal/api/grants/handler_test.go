package grants_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/grants"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// grantResponseKeys are the exact top-level keys the grant DTO must carry —
// used to assert presence directly on the decoded map, since a typed decode
// into a struct passes even when a JSON key was silently dropped.
var grantResponseKeys = []string{
	"id", "capabilityName", "contextKind", "contextRef", "pattern", "mode",
	"limitCount", "limitWindowSeconds", "expiresAt", "grantedBy", "grantedAt",
	"revokedAt", "revokedBy", "reason", "nodeId",
}

const testJWTSecret = "test-secret-for-grants"

const testCapability = "test.capability"

// jwtMiddleware signs a JWT for "user-1" and injects it via cookie so
// RequireAuth populates auth.PayloadFromContext for the handlers under test.
func jwtMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1"}, testJWTSecret, 3600)
		if err != nil {
			http.Error(w, "test setup: sign jwt: "+err.Error(), http.StatusInternalServerError)
			return
		}
		r.AddCookie(&http.Cookie{Name: "auth_token", Value: tok})
		auth.RequireAuth(testJWTSecret)(next).ServeHTTP(w, r)
	})
}

// testDeps bundles the handler under test with the repos backing it, so a
// test can assert against the write path directly instead of only the HTTP
// response.
type testDeps struct {
	mux       *chi.Mux
	grantRepo repo.GrantRepo
	capRepo   repo.CapabilityRepo
}

func setupGrantsHandler(t *testing.T) testDeps {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	grantRepo := repo.NewGrantRepo(bundle.Client)
	capRepo := repo.NewCapabilityRepo(bundle.Client)
	_, err = capRepo.Upsert(context.Background(), repo.UpsertCapabilityInput{
		Name:          testCapability,
		Class:         repo.CapClassTool,
		EnforceableBy: []string{"spawn"},
	})
	require.NoError(t, err)

	h := grants.NewHandler(grantRepo, capRepo)
	mux := chi.NewRouter()
	mux.Group(func(r chi.Router) {
		r.Use(jwtMiddleware)
		h.Mount(r)
	})
	return testDeps{mux: mux, grantRepo: grantRepo, capRepo: capRepo}
}

// validCreateBody returns a request body that passes every boundary check,
// so each refusal test can start from it and break exactly one field.
func validCreateBody() map[string]any {
	pattern := "git status*"
	return map[string]any{
		"capabilityName": testCapability,
		"contextKind":    repo.GrantContextGlobal,
		"contextRef":     "",
		"pattern":        &pattern,
		"mode":           repo.GrantModeAllow,
	}
}

func postGrant(t *testing.T, mux *chi.Mux, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	require.NoError(t, err)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/grants", bytes.NewReader(b)))
	return w
}

func TestCreate_MissingPattern(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	delete(body, "pattern")
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestCreate_UnknownCapability(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["capabilityName"] = "does.not.exist"
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "unknown capability")
}

func TestCreate_InvalidMode(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["mode"] = "sometimes"
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid mode")
}

func TestCreate_InvalidContextKind(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["contextKind"] = "planet"
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid context kind")
}

func TestCreate_RefOnGlobal(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["contextKind"] = repo.GrantContextGlobal
	body["contextRef"] = "/some/project"
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "must be empty")
}

func TestCreate_MissingRefOnNonGlobal(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["contextKind"] = repo.GrantContextProject
	body["contextRef"] = ""
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "is required")
}

func TestCreate_LimitCountWithoutWindow(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["limitCount"] = 5
	body["limitWindowSeconds"] = 0
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "limitWindowSeconds")
}

func TestCreate_NonPositiveExpiry(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	zero := 0
	body["expiresInSeconds"] = &zero
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "expiresInSeconds")
}

func TestCreate_ValidGrantReachesRepoWithActingUser(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody()
	body["reason"] = "smoke test"
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusCreated, w.Code)

	var created map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	require.Equal(t, testCapability, created["capabilityName"])
	require.Equal(t, "user-1", created["grantedBy"])
	require.Equal(t, "smoke test", created["reason"])

	rows, err := deps.grantRepo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "user-1", rows[0].GrantedBy)
}

// TestCreate_ResponseHasZeroValueKeys guards against ent's omitempty tags
// dropping meaningful zero values: limitCount 0 means unlimited and a blank
// contextRef is what a global-scoped grant has, so both must still be sent.
// It decodes into map[string]any and checks for key presence, since a typed
// struct decode passes even when a key is missing from the JSON entirely.
func TestCreate_ResponseHasZeroValueKeys(t *testing.T) {
	deps := setupGrantsHandler(t)
	body := validCreateBody() // limitCount 0, contextKind global, contextRef ""
	w := postGrant(t, deps.mux, body)
	require.Equal(t, http.StatusCreated, w.Code)

	var created map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))
	for _, key := range grantResponseKeys {
		require.Contains(t, created, key, "response missing key %q", key)
	}
	require.NotContains(t, created, "edges")
	require.Equal(t, float64(0), created["limitCount"])
	require.Equal(t, "", created["contextRef"])
}

// TestCreate_ResponseShapeMatchesGet asserts POST /api/grants answers the
// same DTO shape GET /api/grants does — same keys, same casing.
func TestCreate_ResponseShapeMatchesGet(t *testing.T) {
	deps := setupGrantsHandler(t)
	postResp := postGrant(t, deps.mux, validCreateBody())
	require.Equal(t, http.StatusCreated, postResp.Code)
	var created map[string]any
	require.NoError(t, json.NewDecoder(postResp.Body).Decode(&created))

	getResp := httptest.NewRecorder()
	deps.mux.ServeHTTP(getResp, httptest.NewRequest(http.MethodGet, "/api/grants", nil))
	require.Equal(t, http.StatusOK, getResp.Code)
	var listed []map[string]any
	require.NoError(t, json.NewDecoder(getResp.Body).Decode(&listed))
	require.Len(t, listed, 1)

	createdKeys := make([]string, 0, len(created))
	for k := range created {
		createdKeys = append(createdKeys, k)
	}
	listedKeys := make([]string, 0, len(listed[0]))
	for k := range listed[0] {
		listedKeys = append(listedKeys, k)
	}
	require.ElementsMatch(t, grantResponseKeys, createdKeys)
	require.ElementsMatch(t, grantResponseKeys, listedKeys)
}

func TestList_FiltersByCapabilityQueryParam(t *testing.T) {
	deps := setupGrantsHandler(t)
	_, err := deps.capRepo.Upsert(context.Background(), repo.UpsertCapabilityInput{
		Name: "other.capability", Class: repo.CapClassTool, EnforceableBy: []string{"spawn"},
	})
	require.NoError(t, err)

	body1 := validCreateBody()
	require.Equal(t, http.StatusCreated, postGrant(t, deps.mux, body1).Code)

	body2 := validCreateBody()
	body2["capabilityName"] = "other.capability"
	require.Equal(t, http.StatusCreated, postGrant(t, deps.mux, body2).Code)

	w := httptest.NewRecorder()
	deps.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/grants?capability=other.capability", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var rows []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rows))
	require.Len(t, rows, 1)
	require.Equal(t, "other.capability", rows[0]["capabilityName"])
}

// TestList_NewestFirst asserts the response order survives the response-DTO
// change: the most recently granted row must still come first.
func TestList_NewestFirst(t *testing.T) {
	deps := setupGrantsHandler(t)
	first := postGrant(t, deps.mux, validCreateBody())
	require.Equal(t, http.StatusCreated, first.Code)
	var firstCreated map[string]any
	require.NoError(t, json.NewDecoder(first.Body).Decode(&firstCreated))

	second := postGrant(t, deps.mux, validCreateBody())
	require.Equal(t, http.StatusCreated, second.Code)
	var secondCreated map[string]any
	require.NoError(t, json.NewDecoder(second.Body).Decode(&secondCreated))

	w := httptest.NewRecorder()
	deps.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/grants", nil))
	require.Equal(t, http.StatusOK, w.Code)
	var rows []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rows))
	require.Len(t, rows, 2)
	require.Equal(t, secondCreated["id"], rows[0]["id"])
	require.Equal(t, firstCreated["id"], rows[1]["id"])
}

func TestRevoke_PassesActingUserThrough(t *testing.T) {
	deps := setupGrantsHandler(t)
	w := postGrant(t, deps.mux, validCreateBody())
	require.Equal(t, http.StatusCreated, w.Code)
	var created map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&created))

	del := httptest.NewRecorder()
	deps.mux.ServeHTTP(del, httptest.NewRequest(http.MethodDelete, "/api/grants/"+created["id"].(string), nil))
	require.Equal(t, http.StatusNoContent, del.Code)

	rows, err := deps.grantRepo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotNil(t, rows[0].RevokedAt)
	require.Equal(t, "user-1", rows[0].RevokedBy)
}

func TestRevoke_UnknownIDIs404(t *testing.T) {
	deps := setupGrantsHandler(t)
	w := httptest.NewRecorder()
	deps.mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/grants/does-not-exist", nil))
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestListCapabilities_ReturnsCatalogue(t *testing.T) {
	deps := setupGrantsHandler(t)
	w := httptest.NewRecorder()
	deps.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var rows []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rows))
	names := make([]string, len(rows))
	for i, c := range rows {
		names[i] = c["name"].(string)
	}
	require.Contains(t, names, testCapability)
}

// TestListCapabilities_CamelCaseNoEdgesEmptyEnforceableBy asserts the
// catalogue response is camelCased, carries no ent artifacts (snake_case
// keys, the "edges" object), and encodes a nil enforceableBy as [] not null.
func TestListCapabilities_CamelCaseNoEdgesEmptyEnforceableBy(t *testing.T) {
	deps := setupGrantsHandler(t)
	_, err := deps.capRepo.Upsert(context.Background(), repo.UpsertCapabilityInput{
		Name: "bare.capability", Class: repo.CapClassTool,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	deps.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/capabilities", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var rows []map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&rows))
	var bare map[string]any
	for _, row := range rows {
		if row["name"] == "bare.capability" {
			bare = row
		}
	}
	require.NotNil(t, bare, "bare.capability not found in response")

	for _, key := range []string{"id", "name", "class", "enforceableBy", "requiresPattern", "reversible", "description"} {
		require.Contains(t, bare, key)
	}
	for _, row := range rows {
		require.NotContains(t, row, "edges")
		for key := range row {
			require.NotContains(t, key, "_", "response key %q looks like snake_case", key)
		}
	}
	require.Equal(t, []any{}, bare["enforceableBy"])
}

// TestCreate_BypassAuth_ActingUserIsLocalAdmin exercises the router without
// JWT middleware — the shape DASHBOARD_AUTH=none mounts the handler in
// (router.go's protected group skips RequireAuth but still mounts
// GrantsHandler). GrantedBy must still be non-empty.
func TestCreate_BypassAuth_ActingUserIsLocalAdmin(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	grantRepo := repo.NewGrantRepo(bundle.Client)
	capRepo := repo.NewCapabilityRepo(bundle.Client)
	_, err = capRepo.Upsert(context.Background(), repo.UpsertCapabilityInput{
		Name: testCapability, Class: repo.CapClassTool, EnforceableBy: []string{"spawn"},
	})
	require.NoError(t, err)

	h := grants.NewHandler(grantRepo, capRepo)
	mux := chi.NewRouter()
	h.Mount(mux) // no JWT middleware — simulates bypass auth mode

	w := postGrant(t, mux, validCreateBody())
	require.Equal(t, http.StatusCreated, w.Code)

	rows, err := grantRepo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, auth.BypassUserID, rows[0].GrantedBy)
}
