package plugin

import "context"

// ModuleTokenEnvVar carries the credential into the module's process. It is
// deliberately not the shared MCP token, which is blocked from module
// environments: this one names the module, carries only the capabilities its
// manifest declares, and dies when the module is deactivated.
//
//nolint:gosec // G101: the name of an environment variable, not a credential.
const ModuleTokenEnvVar = "KONTOR_MODULE_TOKEN"

// CredentialIssuer mints and retires the credential a module calls back with.
// The registry holds it as a nil-safe seam, the same shape as its process
// manager and settings provider, so a registry built without one simply starts
// modules that cannot call back.
type CredentialIssuer interface {
	// Issue replaces any credential the module holds and returns the new token.
	Issue(ctx context.Context, moduleID string, scopes []string) (string, error)
	// Revoke retires every credential the module holds.
	Revoke(ctx context.Context, moduleID string) error
}
