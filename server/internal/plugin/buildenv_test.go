package plugin

import (
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
