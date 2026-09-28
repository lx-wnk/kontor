package restart

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// pluginHealth reports the live health of a plugin by id.
type pluginHealth interface {
	Lookup(id string) (plugin.Entry, bool)
}

// AuthProviderValidator refuses a restart that would brick boot. The boot
// fatal-safety check aborts startup when a configured auth_provider plugin is
// not healthy. We must predict that for the NEXT boot: for every plugin marked
// active in the DB whose on-disk manifest declares auth_provider, require a
// currently-healthy registry entry. Checking only running entries would miss an
// auth_provider that is active-in-DB but failed to start (absent from the
// registry) — exactly the case that locks the user out.
type AuthProviderValidator struct {
	reg       pluginHealth
	activeIDs func(ctx context.Context) ([]string, error)
	dir       string
}

func NewAuthProviderValidator(reg pluginHealth, activeIDs func(ctx context.Context) ([]string, error), dir string) *AuthProviderValidator {
	return &AuthProviderValidator{reg: reg, activeIDs: activeIDs, dir: dir}
}

func (v *AuthProviderValidator) Validate(ctx context.Context) error {
	if v.activeIDs == nil {
		return nil
	}
	ids, err := v.activeIDs(ctx)
	if err != nil {
		return fmt.Errorf("restart validate: list active plugins: %w", err)
	}
	for _, id := range ids {
		desc, err := readDescriptor(v.dir, id)
		if err != nil {
			// Can't read the manifest -> can't assert it declares auth_provider.
			// Skip; a missing/broken non-auth manifest does not brick boot.
			continue
		}
		if !desc.HasCapability(plugin.CapAuthProvider) {
			continue
		}
		e, ok := v.reg.Lookup(id)
		if !ok || !e.Healthy() {
			return fmt.Errorf("auth_provider plugin %q is not healthy — restart would lock out auth", id)
		}
	}
	return nil
}

func readDescriptor(dir, id string) (plugin.Descriptor, error) {
	var d plugin.Descriptor
	data, err := os.ReadFile(filepath.Join(dir, id, "plugin.json"))
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return d, err
	}
	return d, nil
}
