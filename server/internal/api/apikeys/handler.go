package apikeys

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// Handler handles API key management routes.
type Handler struct {
	repo repo.ApiKeyRepo
}

// NewHandler creates an API key Handler.
func NewHandler(r repo.ApiKeyRepo) *Handler {
	return &Handler{repo: r}
}

// Wrap converts a handler-returns-error function to a chi-compatible HandlerFunc.
func Wrap(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
	return apierr.ErrorMiddleware(apierr.HandlerFunc(fn))
}

// GenerateToken creates a new random MCP API token and its SHA-256 hash.
// Shared by Create, Regenerate, and the onboarding one-click connect flow.
func GenerateToken() (token, hash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("apikeys.GenerateToken: %w", err)
	}
	token = "mcp_" + base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}

// keyView is the JSON shape returned for both List and Create.
// Field names match the ApiKey TypeScript interface (camelCase).
type keyView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	Active     bool     `json:"active"`
	UserID     any      `json:"userId"`
	CreatedAt  string   `json:"createdAt"`
	LastUsedAt *string  `json:"lastUsedAt"`
}

// List returns all active API keys. Never includes key_hash or raw token.
// GET /api/settings/api-keys
func (h *Handler) List(w http.ResponseWriter, r *http.Request) error {
	keys, err := h.repo.List(r.Context())
	if err != nil {
		return fmt.Errorf("apikeys.List: %w", err)
	}
	out := make([]keyView, len(keys))
	for i, k := range keys {
		var lastUsed *string
		if k.LastUsedAt != nil {
			s := k.LastUsedAt.Format("2006-01-02T15:04:05Z")
			lastUsed = &s
		}
		out[i] = keyView{
			ID:         k.ID,
			Name:       k.Name,
			Scopes:     k.Scopes,
			Active:     k.Active,
			UserID:     nil,
			CreatedAt:  k.CreatedAt.Format("2006-01-02T15:04:05Z"),
			LastUsedAt: lastUsed,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(out)
}

// Create creates a new API key. Returns the raw token once — it is not stored.
// POST /api/settings/api-keys  body: {"name":"...","scopes":["tasks:read",...]}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) error {
	var body struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return fmt.Errorf("%w: invalid JSON", apierr.ErrBadRequest)
	}
	if body.Name == "" {
		return fmt.Errorf("%w: name is required", apierr.ErrBadRequest)
	}

	token, hash, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("apikeys.Create: %w", err)
	}

	key, err := h.repo.Create(r.Context(), repo.CreateApiKeyInput{Name: body.Name, Hash: hash, Scopes: body.Scopes})
	if err != nil {
		return fmt.Errorf("apikeys.Create: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	return json.NewEncoder(w).Encode(map[string]any{
		"key": keyView{
			ID:         key.ID,
			Name:       key.Name,
			Scopes:     key.Scopes,
			Active:     key.Active,
			UserID:     nil,
			CreatedAt:  key.CreatedAt.Format("2006-01-02T15:04:05Z"),
			LastUsedAt: nil,
		},
		"token": token,
	})
}

// Delete soft-deletes an API key by ID.
// DELETE /api/settings/api-keys/{id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == "" {
		return fmt.Errorf("%w: id is required", apierr.ErrBadRequest)
	}
	if err := h.repo.Delete(r.Context(), id); err != nil {
		return fmt.Errorf("apikeys.Delete: %w", err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Regenerate rotates the secret for an existing key in-place: same ID/name/scopes,
// new key_hash and raw token. Returns the updated key + the one-time raw token.
// POST /api/settings/api-keys/{id}/regenerate
func (h *Handler) Regenerate(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if id == "" {
		return fmt.Errorf("%w: id is required", apierr.ErrBadRequest)
	}

	token, hash, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("apikeys.Regenerate: %w", err)
	}

	key, err := h.repo.Rotate(r.Context(), id, hash)
	if err != nil {
		return fmt.Errorf("apikeys.Regenerate: %w", err)
	}

	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(map[string]any{
		"key": keyView{
			ID:         key.ID,
			Name:       key.Name,
			Scopes:     key.Scopes,
			Active:     key.Active,
			UserID:     nil,
			CreatedAt:  key.CreatedAt.Format("2006-01-02T15:04:05Z"),
			LastUsedAt: nil,
		},
		"token": token,
	})
}
