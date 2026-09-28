// Package onboarding serves the first-run setup flow: CLI/MCP status detection,
// one-click MCP registration, and marking setup complete/skipped.
package onboarding

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/api/apikeys"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/cmdscope"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	mcp "github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/settings"
)

const (
	completedKey  = "onboarding.completed"
	onboardingKey = "onboarding"
)

// onboardingScopes grants the minted key the same access as the "Developer"
// role in the API-key UI — enough for the full MCP task API (read, write,
// pipeline control) that the connected session will need.
var onboardingScopes = []string{"tasks:read", "tasks:write", "pipeline:control"}

// CmdRunner executes an external command. Injected so tests don't shell out;
// production uses runClaudeCmd.
type CmdRunner func(ctx context.Context, name string, args ...string) error

func runClaudeCmd(ctx context.Context, name string, args ...string) error {
	return exec.CommandContext(ctx, name, args...).Run()
}

// Handler serves the onboarding routes.
type Handler struct {
	settingsSvc *settings.Service
	apiKeyRepo  repo.ApiKeyRepo
	run         CmdRunner
}

// NewHandler builds the onboarding Handler.
func NewHandler(settingsSvc *settings.Service, apiKeyRepo repo.ApiKeyRepo) *Handler {
	return &Handler{settingsSvc: settingsSvc, apiKeyRepo: apiKeyRepo, run: runClaudeCmd}
}

// SetRunnerForTest overrides the CmdRunner seam so tests don't shell out.
func SetRunnerForTest(h *Handler, run CmdRunner) { h.run = run }

// Mount registers the onboarding routes on r.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/api/onboarding/status", apierr.ErrorMiddleware(h.status))
	r.Post("/api/onboarding/register-mcp", apierr.ErrorMiddleware(h.registerMCP))
	r.Patch("/api/onboarding/status", apierr.ErrorMiddleware(h.complete))
}

type statusResponse struct {
	Completed     bool   `json:"completed"`
	CLIInstalled  bool   `json:"cliInstalled"`
	CLIVersion    string `json:"cliVersion"`
	MCPRegistered bool   `json:"mcpRegistered"`
}

// GET /api/onboarding/status
func (h *Handler) status(w http.ResponseWriter, _ *http.Request) error {
	version, installed := cmdscope.ProbeEngineVersion("claude")
	apierr.WriteJSON(w, http.StatusOK, statusResponse{
		Completed:     h.settingsSvc.Bool(completedKey),
		CLIInstalled:  installed,
		CLIVersion:    version,
		MCPRegistered: mcpRegistered(),
	})
	return nil
}

// mcpRegistered is best-effort: any missing/unreadable/unparseable config file
// is reported as "not registered" rather than failing the whole status call.
func mcpRegistered() bool {
	servers, err := claudeconfig.UserMCPServers()
	if err != nil {
		return false
	}
	_, ok := servers[mcp.ServerName]
	return ok
}

// POST /api/onboarding/register-mcp
func (h *Handler) registerMCP(w http.ResponseWriter, r *http.Request) error {
	token, hash, err := apikeys.GenerateToken()
	if err != nil {
		return fmt.Errorf("onboarding.registerMCP: %w", err)
	}
	if err := h.upsertOnboardingKey(r.Context(), hash); err != nil {
		return fmt.Errorf("onboarding.registerMCP: %w", err)
	}

	url := requestOrigin(r) + mcp.EndpointPath
	header := "Authorization: Bearer " + token
	args := []string{"mcp", "add", "--scope", "user", "--transport", "http", mcp.ServerName, url, "--header", header}
	command := fmt.Sprintf("claude mcp add --scope user --transport http %s %s --header %q", mcp.ServerName, url, header)

	ok := h.run(r.Context(), "claude", args...) == nil

	apierr.WriteJSON(w, http.StatusOK, map[string]any{"ok": ok, "command": command})
	return nil
}

// upsertOnboardingKey rotates the existing "onboarding" API key if one already
// exists, or creates it otherwise — repeated register-mcp calls (retries,
// re-clicks) must not accumulate live task-scoped credentials.
func (h *Handler) upsertOnboardingKey(ctx context.Context, hash string) error {
	keys, err := h.apiKeyRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("list keys: %w", err)
	}
	for _, k := range keys {
		if k.Name == onboardingKey {
			if _, err := h.apiKeyRepo.Rotate(ctx, k.ID, hash); err != nil {
				return fmt.Errorf("rotate key: %w", err)
			}
			return nil
		}
	}
	if _, err := h.apiKeyRepo.Create(ctx, repo.CreateApiKeyInput{Name: onboardingKey, Hash: hash, Scopes: onboardingScopes}); err != nil {
		return fmt.Errorf("create key: %w", err)
	}
	return nil
}

// requestOrigin builds scheme://host from the request. Safe to trust r.Host
// here: this handler is mounted in the same loopback + same-origin guarded
// group as every other mutation route, so Host has already passed
// RequireLoopbackHost.
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// PATCH /api/onboarding/status
func (h *Handler) complete(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Completed bool `json:"completed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: invalid JSON", apierr.ErrBadRequest)
	}
	if err := h.settingsSvc.Set(r.Context(), completedKey, strconv.FormatBool(body.Completed)); err != nil {
		return fmt.Errorf("onboarding.complete: %w", err)
	}
	apierr.WriteJSON(w, http.StatusOK, map[string]bool{"completed": body.Completed})
	return nil
}
