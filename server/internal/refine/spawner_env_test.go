package refine

import (
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func TestMergeEnv_SkipsKeysThatAreNotVariableNames(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	sp := &ent.Spawner{Env: map[string]string{"ANTHROPIC_API_KEY=x": "y", "OK_KEY": "v"}}

	got := mergeEnv(sp)

	var hasOK bool
	for _, kv := range got {
		name, _, _ := strings.Cut(kv, "=")
		if name == "OK_KEY" {
			hasOK = true
		}
		if strings.Contains(kv, "x=y") {
			t.Fatalf("malformed spawner key leaked into env: %q", kv)
		}
	}
	if !hasOK {
		t.Fatal("valid key OK_KEY missing from merged env")
	}
}
