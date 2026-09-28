package onboarding_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/onboarding"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

type fakeSettingsRepo struct{ m map[string]string }

func (f *fakeSettingsRepo) Get(_ context.Context, k string) (string, bool, error) {
	v, ok := f.m[k]
	return v, ok, nil
}
func (f *fakeSettingsRepo) Set(_ context.Context, k, v string) error { f.m[k] = v; return nil }
func (f *fakeSettingsRepo) SetSecret(_ context.Context, k, ciphertext, _ string) error {
	f.m[k] = ciphertext
	return nil
}
func (f *fakeSettingsRepo) GetSecret(_ context.Context, k string) (string, string, bool, error) {
	v, ok := f.m[k]
	return v, "", ok, nil
}
func (f *fakeSettingsRepo) ListAll(_ context.Context) (map[string]string, error) { return f.m, nil }

func setupHandler(t *testing.T) (*onboarding.Handler, *chi.Mux, repo.ApiKeyRepo) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })
	apiKeyRepo := repo.NewApiKeyRepo(bundle.Client)

	settingsSvc := settings.New(&fakeSettingsRepo{m: map[string]string{}}, nil)
	require.NoError(t, settingsSvc.Load(context.Background()))

	h := onboarding.NewHandler(settingsSvc, apiKeyRepo)
	mux := chi.NewRouter()
	h.Mount(mux)
	return h, mux, apiKeyRepo
}

// withNoMCPConfig points CLAUDE_CONFIG_DIR at an empty temp dir so
// mcpRegistered has a deterministic (absent) ~/.claude.json to read.
func withNoMCPConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	return dir
}

func TestStatus_DefaultsToIncompleteAndUnregistered(t *testing.T) {
	withNoMCPConfig(t)
	_, mux, _ := setupHandler(t)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/onboarding/status", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var got map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	require.Equal(t, false, got["completed"])
	require.Equal(t, false, got["mcpRegistered"])
	require.Contains(t, got, "cliInstalled")
	require.Contains(t, got, "cliVersion")
}

func TestStatus_MCPRegisteredWhenServerPresentInConfig(t *testing.T) {
	dir := withNoMCPConfig(t)
	_, mux, _ := setupHandler(t)

	cfg := map[string]any{
		"mcpServers": map[string]any{
			mcp.ServerName: map[string]any{"type": "http", "url": "http://127.0.0.1:13120/api/mcp"},
		},
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), data, 0o600))

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/onboarding/status", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var got map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	require.Equal(t, true, got["mcpRegistered"])
}

func TestRegisterMCP_BuildsArgvMintsKeyAndReturnsCommand(t *testing.T) {
	withNoMCPConfig(t)
	h, mux, apiKeyRepo := setupHandler(t)

	var gotName string
	var gotArgs []string
	onboarding.SetRunnerForTest(h, func(_ context.Context, name string, args ...string) error {
		gotName = name
		gotArgs = args
		return nil
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/onboarding/register-mcp", nil))
	require.Equal(t, http.StatusOK, w.Code)

	var got map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	require.Equal(t, true, got["ok"])
	command, ok := got["command"].(string)
	require.True(t, ok)
	require.Contains(t, command, "claude mcp add")
	require.Contains(t, command, mcp.ServerName)
	require.Contains(t, command, mcp.EndpointPath)
	require.Contains(t, command, "Authorization: Bearer ")

	require.Equal(t, "claude", gotName)
	require.Equal(t, []string{
		"mcp", "add", "--scope", "user", "--transport", "http", mcp.ServerName,
	}, gotArgs[:7])
	require.Len(t, gotArgs, 10)
	require.Contains(t, gotArgs[7], mcp.EndpointPath)
	require.True(t, containsPrefixed(gotArgs, "http://"))
	require.Contains(t, gotArgs, "--header")

	keys, err := apiKeyRepo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, "onboarding", keys[0].Name)
	require.ElementsMatch(t, []string{"tasks:read", "tasks:write", "pipeline:control"}, keys[0].Scopes)
}

func TestRegisterMCP_RepeatedCallsReuseTheSameOnboardingKey(t *testing.T) {
	withNoMCPConfig(t)
	h, mux, apiKeyRepo := setupHandler(t)

	onboarding.SetRunnerForTest(h, func(_ context.Context, _ string, _ ...string) error { return nil })

	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/onboarding/register-mcp", nil))
		require.Equal(t, http.StatusOK, w.Code)
	}

	keys, err := apiKeyRepo.List(context.Background())
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.Equal(t, "onboarding", keys[0].Name)
	require.ElementsMatch(t, []string{"tasks:read", "tasks:write", "pipeline:control"}, keys[0].Scopes)
}

func TestRegisterMCP_ExecFailureReturnsOkFalseWithFallbackCommand(t *testing.T) {
	withNoMCPConfig(t)
	h, mux, _ := setupHandler(t)

	onboarding.SetRunnerForTest(h, func(_ context.Context, _ string, _ ...string) error {
		return context.DeadlineExceeded
	})

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/onboarding/register-mcp", nil))
	require.Equal(t, http.StatusOK, w.Code) // domain outcome, not a server error

	var got map[string]any
	require.NoError(t, json.NewDecoder(w.Body).Decode(&got))
	require.Equal(t, false, got["ok"])
	command, ok := got["command"].(string)
	require.True(t, ok)
	require.NotEmpty(t, command)
}

func TestComplete_SetsOnboardingCompletedSetting(t *testing.T) {
	withNoMCPConfig(t)
	_, mux, _ := setupHandler(t)

	body, _ := json.Marshal(map[string]bool{"completed": true})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/onboarding/status", bytes.NewReader(body)))
	require.Equal(t, http.StatusOK, w.Code)

	w2 := httptest.NewRecorder()
	mux.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, "/api/onboarding/status", nil))
	var got map[string]any
	require.NoError(t, json.NewDecoder(w2.Body).Decode(&got))
	require.Equal(t, true, got["completed"])
}

func containsPrefixed(args []string, prefix string) bool {
	for _, a := range args {
		if len(a) >= len(prefix) && a[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
