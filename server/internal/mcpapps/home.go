package mcpapps

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var resourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// HomeRootUnder is the directory holding every isolated application's HOME.
func HomeRootUnder(userHome string) string {
	return filepath.Join(userHome, ".claude", "kontor", "apps")
}

// IsolatedHomeEnv creates homeRoot/resourceID (0700) and returns its HOME and npm cache env.
func IsolatedHomeEnv(homeRoot, resourceID string) (map[string]string, error) {
	if homeRoot == "" {
		return nil, fmt.Errorf("mcpapps: home root is not configured")
	}
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
