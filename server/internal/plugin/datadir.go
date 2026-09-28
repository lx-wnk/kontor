package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ModuleDataDirEnvVar names the directory a module owns. Its store lives here
// rather than in the module's own directory because a module is installed and
// updated with git: data kept in the checkout would share a tree with the
// update mechanism, so every reinstall would take it along.
const ModuleDataDirEnvVar = "KONTOR_MODULE_DATA_DIR"

// ModuleDataDir returns the directory belonging to one module under root.
func ModuleDataDir(root, moduleID string) string {
	return filepath.Join(root, moduleID)
}

// EnsureModuleDataDir creates the module's directory if it is missing and
// returns it. 0700 because what a module stores is the operator's, not the
// machine's.
func EnsureModuleDataDir(root, moduleID string) (string, error) {
	dir := ModuleDataDir(root, moduleID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("module data dir for %q: %w", moduleID, err)
	}
	return dir, nil
}

// ArchiveModuleDataDir moves the module's directory aside instead of deleting
// it. An uninstall is a decision about the module, not about what it collected:
// the archive is what makes that decision reversible. It reports the archive
// path, or an empty string when the module had no directory.
func ArchiveModuleDataDir(root, moduleID string) (string, error) {
	dir := ModuleDataDir(root, moduleID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("archive module data for %q: %w", moduleID, err)
	}
	archive := dir + ".removed-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.Rename(dir, archive); err != nil {
		return "", fmt.Errorf("archive module data for %q: %w", moduleID, err)
	}
	return archive, nil
}
