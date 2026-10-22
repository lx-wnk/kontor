// Package pathutil provides filesystem path helpers shared across packages.
package pathutil

import (
	"os"
	"path/filepath"
	"strings"
)

// ExpandLeadingTilde replaces a leading `~` (bare or `~/`) with the user's home
// directory, mirroring shell tilde expansion. Other values pass through unchanged.
func ExpandLeadingTilde(v string) string {
	if v != "~" && !strings.HasPrefix(v, "~/") {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return v
	}
	if v == "~" {
		return home
	}
	return filepath.Join(home, v[2:])
}

// Canonical resolves a leading ~, relative segments and symlinks, so that two
// spellings of one directory (~/.claude and the store it links to) compare
// equal. A path that does not exist yet keeps its expanded, cleaned form.
func Canonical(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	dir = ExpandLeadingTilde(dir)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(dir)
}
