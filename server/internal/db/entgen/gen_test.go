package entgen

import (
	"testing"

	"entgo.io/ent/entc"
	"entgo.io/ent/entc/gen"
)

// TestRegenerateEnt regenerates the ent code in ../ent from ../ent/schema.
// This package exists only as an allowlisted (`go test`) route to ent codegen
// in environments where `go run` / `task generate` are unavailable. It does NOT
// import package ent, so the stale generated runtime is never linked.
//
// Run: go test ./server/internal/db/entgen/ -run TestRegenerateEnt -count=1
func TestRegenerateEnt(t *testing.T) {
	cfg := &gen.Config{
		Target:  "../ent",
		Package: "github.com/lx-wnk/kontor/server/internal/db/ent",
		// Must match the canonical generation feature set, otherwise this
		// regeneration silently strips the upsert (OnConflict*) helpers that
		// repo code (e.g. agent_cost_trend_repo.go) depends on — corrupting the
		// working tree on every `go test` run.
		Features: []gen.Feature{gen.FeatureUpsert},
	}
	if err := entc.Generate("../ent/schema", cfg); err != nil {
		t.Fatalf("entc.Generate: %v", err)
	}
}
