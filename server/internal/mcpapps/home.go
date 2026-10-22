package mcpapps

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
)

var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// ValidHomeResourceID reports whether id is safe to use as one path element under the home root.
func ValidHomeResourceID(id string) bool {
	return resourceIDPattern.MatchString(id) && id != "." && id != ".."
}

// HomeRootUnder is the directory holding every isolated application's HOME.
func HomeRootUnder(userHome string) string {
	return filepath.Join(userHome, ".claude", "kontor", "apps")
}

// IsolatedHomeEnv creates homeRoot/resourceID (0700) and returns its HOME and npm cache env.
func IsolatedHomeEnv(homeRoot, resourceID string) (map[string]string, error) {
	if homeRoot == "" {
		return nil, fmt.Errorf("mcpapps: home root is not configured")
	}
	if !ValidHomeResourceID(resourceID) {
		return nil, fmt.Errorf("mcpapps: invalid resource id %q for home directory", resourceID)
	}
	dir := filepath.Join(homeRoot, resourceID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mcpapps: create home %q: %w", dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mcpapps: restrict home %q: %w", dir, err)
	}
	cache := os.Getenv("npm_config_cache")
	if cache == "" {
		serverHome, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("mcpapps: locate npm cache: %w", err)
		}
		cache = filepath.Join(serverHome, ".npm")
	}
	return map[string]string{"HOME": dir, "npm_config_cache": cache}, nil
}

// WithIsolatedHome returns env with the preset's isolated HOME merged last, or env itself when the preset does not isolate.
func WithIsolatedHome(env map[string]string, preset Preset, homeRoot, resourceID string) (map[string]string, error) {
	if !preset.IsolateHome {
		return env, nil
	}
	home, err := IsolatedHomeEnv(homeRoot, resourceID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(env)+len(home))
	maps.Copy(out, env)
	maps.Copy(out, home)
	return out, nil
}
