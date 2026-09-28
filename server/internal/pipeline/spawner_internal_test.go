package pipeline

import (
	"reflect"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// TestCapabilityViewForFallsBackToSharedDefault pins that capabilityViewFor's
// no-catalogue fallback delegates to repo.DefaultCapabilityView rather than
// holding an independent copy of the class/enforcement literals — the two
// paths sharing one function is what makes them unable to silently drift
// apart.
func TestCapabilityViewForFallsBackToSharedDefault(t *testing.T) {
	capabilityCatalogue = nil // no boot has run in this test process
	for _, tool := range []string{"Bash", "WebFetch", "Read"} {
		got := capabilityViewFor(tool)
		want := repo.DefaultCapabilityView(tool)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("capabilityViewFor(%q) = %+v, want %+v (repo.DefaultCapabilityView)", tool, got, want)
		}
	}
}
