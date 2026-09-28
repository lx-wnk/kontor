package serverapp

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/mcp"
)

// moduleCredentials mints and revokes the credential a module uses to call back
// into the server. It is the module-shaped twin of the pipeline's stage-run
// keys: scoped to what the manifest declares, replaced whenever the module
// starts, and gone the moment it is deactivated.
type moduleCredentials struct {
	keys repo.ApiKeyRepo
}

// Issue replaces any credential the module already holds and returns the new
// token in clear — the only moment it exists in that form. Replacing rather
// than adding means a restarted module never leaves a usable credential behind.
func (m moduleCredentials) Issue(ctx context.Context, moduleID string, scopes []string) (string, error) {
	if err := m.Revoke(ctx, moduleID); err != nil {
		return "", err
	}
	token := mcp.GenerateAPIToken()
	if _, err := m.keys.Create(ctx, repo.CreateApiKeyInput{
		Name:   repo.ModuleKeyName(moduleID),
		Hash:   mcp.HashToken(token),
		Scopes: scopes,
		Kind:   repo.ApiKeyKindModule,
	}); err != nil {
		return "", fmt.Errorf("module credential for %q: %w", moduleID, err)
	}
	return token, nil
}

// Revoke deactivates every credential issued to the module, the same way the
// pipeline retires a stage run's key. The authenticator only accepts active
// rows, so a revoked credential stops working immediately.
func (m moduleCredentials) Revoke(ctx context.Context, moduleID string) error {
	if _, err := m.keys.RevokeForModule(ctx, moduleID); err != nil {
		return fmt.Errorf("module credential for %q: %w", moduleID, err)
	}
	return nil
}
