package mcpapps

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// IsolatedHomeEnv creates homeRoot/resourceID (0700) and returns the env that
// points an application at it as HOME. npm_config_cache keeps the server's real
// npm cache so a fresh HOME does not force a full re-download on every spawn.
func IsolatedHomeEnv(homeRoot, resourceID string) (map[string]string, error) {
	if !resourceIDPattern.MatchString(resourceID) || resourceID == "." || resourceID == ".." {
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
