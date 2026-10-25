package cloud

import (
	"strings"

	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// AccountDir is the CLAUDE_CONFIG_DIR value the spawner's CLI child receives,
// "" when it gets none. It is never canonicalised: on macOS the keychain item
// is keyed by this literal string, so a symlink and its target, or unset and
// an explicit ~/.claude, are separate logins.
func AccountDir(sp *ent.Spawner) string {
	return childEnvVars(sp)[claudeconfig.EnvVar]
}

// LoginHint is the shell command that signs the spawner's account in.
func LoginHint(sp *ent.Spawner) string {
	dir := AccountDir(sp)
	if dir == "" {
		return "claude auth login"
	}
	return claudeconfig.EnvVar + "=" + shellQuote(dir) + " claude auth login"
}

// TrustHint is the shell command that opens dir in claude under the spawner's
// account; claude stores folder trust per CLAUDE_CONFIG_DIR.
func TrustHint(sp *ent.Spawner, dir string) string {
	hint := "cd " + shellQuote(dir) + " && "
	if account := AccountDir(sp); account != "" {
		hint += claudeconfig.EnvVar + "=" + shellQuote(account) + " "
	}
	return hint + "claude"
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
