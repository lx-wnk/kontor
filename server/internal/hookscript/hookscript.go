// Package hookscript ships the permission-bridge hook script.
//
// The script is embedded rather than shipped beside the binary because Claude
// Code executes it by absolute path, on every gated tool call, for as long as
// the hook stays registered in the user's settings. A path into a release
// archive or a repo checkout is not stable enough for that: the archive did not
// carry the file at all, and a checkout's contents follow every branch switch.
// Materialising it under the user's own config directory gives one path that
// survives binary upgrades, moved checkouts and deleted ones.
package hookscript

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
)

//go:embed kontor-permission.sh
var script []byte

// Dir is where the script is materialised, relative to the Claude config dir.
const Dir = "kontor-hooks"

// LegacyDir and LegacyName are what the directory and the script were called
// before the rename. They are not written any more, but an installation that
// ran an older binary has an entry in its settings file pointing at them, and
// the installer has to recognise it as its own — otherwise it appends a second
// entry and the permission hook fires twice.
const (
	LegacyDir  = "dashboard-hooks"
	LegacyName = "dashboard-permission.sh"
)

// Name is the script's filename. It doubles as the marker that identifies the
// entries `hooks install` owns.
const Name = "kontor-permission.sh"

// Install writes the script under configDir and returns its absolute path.
// Rewriting on every install is deliberate: it is how an upgraded binary
// replaces a script shipped by an older one.
func Install(configDir string) (string, error) {
	dir := filepath.Join(configDir, Dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, Name)
	if err := writeExecutable(path, script); err != nil {
		return "", err
	}
	return path, nil
}

// writeExecutable replaces path atomically. The script is executed before every
// gated tool call, so a half-written file would be a broken hook on every
// session rather than a one-off error.
func writeExecutable(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dashboard-permission-*")
	if err != nil {
		return fmt.Errorf("create temp file next to %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	// Owner-only, executable: it runs as the user and carries their session data.
	if err := os.Chmod(tmp.Name(), 0o700); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}
