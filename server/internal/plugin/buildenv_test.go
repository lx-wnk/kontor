package plugin

import (
	"context"
	"strings"
	"testing"
)

func TestBuildPluginEnv_BlocklistWinsOverAllowList(t *testing.T) {
	t.Setenv("MY_PLUGIN_KEY", "hello")
	t.Setenv("DASHBOARD_SECRET_KEY", "should-never-appear")
	t.Setenv("DASHBOARD_JWT_SECRET", "also-blocked")

	env := buildPluginEnv([]string{"MY_PLUGIN_KEY", "DASHBOARD_SECRET_KEY", "DASHBOARD_JWT_SECRET"})

	byKey := make(map[string]string, len(env))
	for _, kv := range env {
		if idx := strings.Index(kv, "="); idx > 0 {
			byKey[kv[:idx]] = kv[idx+1:]
		}
	}

	if byKey["MY_PLUGIN_KEY"] != "hello" {
		t.Errorf("expected MY_PLUGIN_KEY=hello in env, got %q", byKey["MY_PLUGIN_KEY"])
	}
	if _, found := byKey["DASHBOARD_SECRET_KEY"]; found {
		t.Error("DASHBOARD_SECRET_KEY must not be forwarded even when allow-listed")
	}
	if _, found := byKey["DASHBOARD_JWT_SECRET"]; found {
		t.Error("DASHBOARD_JWT_SECRET must not be forwarded even when allow-listed")
	}
}

func TestBuildPluginEnv_AllBlocklistNamesAreBlocked(t *testing.T) {
	// Both prefixes: the server reads configuration under either since the
	// rename, so a secret set under the new name must be blocked as firmly as
	// the old one. An allow-list matches by prefix and grew on its own; this
	// blocklist does not, which is what this case is here to catch.
	var blocked []string
	for _, prefix := range []string{"DASHBOARD_", "KONTOR_"} {
		blocked = append(blocked,
			prefix+"SECRET_KEY",
			prefix+"JWT_SECRET",
			prefix+"AUTH_PLUGIN_SECRET",
			prefix+"MCP_TOKEN",
			prefix+"HOOKS_SECRET",
		)
	}
	for _, k := range blocked {
		t.Setenv(k, "secret-value")
	}

	// Pass all blocked names as the allow-list — blocklist must still win.
	env := buildPluginEnv(blocked)

	byKey := make(map[string]string, len(env))
	for _, kv := range env {
		if idx := strings.Index(kv, "="); idx > 0 {
			byKey[kv[:idx]] = kv[idx+1:]
		}
	}
	for _, k := range blocked {
		if _, found := byKey[k]; found {
			t.Errorf("%s must not appear in plugin env", k)
		}
	}
}

// stubIssuer records what it was asked for and hands back a fixed token.
type stubIssuer struct {
	scopes  []string
	revoked []string
}

func (s *stubIssuer) Issue(_ context.Context, _ string, scopes []string) (string, error) {
	s.scopes = scopes
	return "mcp_moduletoken", nil
}

func (s *stubIssuer) Revoke(_ context.Context, moduleID string) error {
	s.revoked = append(s.revoked, moduleID)
	return nil
}

// A module is handed a credential of its own, carrying exactly the
// capabilities its manifest declares. Without it the module can be called but
// can call nothing back — the shared MCP token is blocked from its environment
// on purpose.
func TestAppendModuleTokenEnv_CarriesTheManifestsUses(t *testing.T) {
	issuer := &stubIssuer{}
	r := New(t.TempDir())
	r.SetCredentialIssuer(issuer)

	env := r.appendModuleTokenEnv(context.Background(), []string{"PATH=/usr/bin"}, Descriptor{
		ID:   "obsidian",
		Uses: []string{"memory.read", "memory.write"},
	})

	var found string
	for _, kv := range env {
		if after, ok := strings.CutPrefix(kv, ModuleTokenEnvVar+"="); ok {
			found = after
		}
	}
	if found != "mcp_moduletoken" {
		t.Fatalf("%s = %q, want the issued token", ModuleTokenEnvVar, found)
	}
	if len(issuer.scopes) != 2 || issuer.scopes[0] != "memory.read" {
		t.Errorf("scopes = %v, want the manifest's uses list", issuer.scopes)
	}
}

// A registry without an issuer still starts modules; they simply get no token.
func TestAppendModuleTokenEnv_WithoutAnIssuerAddsNothing(t *testing.T) {
	r := New(t.TempDir())
	env := r.appendModuleTokenEnv(context.Background(), []string{"PATH=/usr/bin"}, Descriptor{ID: "obsidian"})
	if len(env) != 1 {
		t.Errorf("env = %v, want it untouched", env)
	}
}
