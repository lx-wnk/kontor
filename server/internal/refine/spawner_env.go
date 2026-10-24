// Package refine helpers for env-merge precedence used by RunRefinementTurn.
package refine

import (
	"log/slog"
	"os"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/envsec"
)

// blockedKeys are never forwarded to spawned processes regardless of what a
// custom spawner declares in its env map. Mirrors the stage handler policy.
var blockedKeys = map[string]struct{}{
	"DASHBOARD_JWT_SECRET":   {},
	"DASHBOARD_HOOKS_SECRET": {},
}

// mergeEnv applies precedence: custom spawner env first, then dashboard
// process env overlays and always wins. Blocked keys are stripped at the end.
func mergeEnv(sp *ent.Spawner) []string {
	merged := make(map[string]string)
	if sp != nil {
		for k, v := range sp.Env {
			if _, blocked := blockedKeys[k]; blocked {
				continue
			}
			if !envsec.IsValidName(k) {
				slog.Warn("spawner env key skipped: not a variable name", "spawner", sp.ID)
				continue
			}
			merged[k] = v
		}
	}
	for _, kv := range os.Environ() {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				k := kv[:i]
				if _, blocked := blockedKeys[k]; blocked {
					break
				}
				merged[k] = kv[i+1:]
				break
			}
		}
	}
	out := make([]string, 0, len(merged))
	for k, v := range merged {
		out = append(out, k+"="+v)
	}
	return out
}
