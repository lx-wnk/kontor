package services

import "github.com/lx-wnk/kontor/server/internal/db/ent"

// AdditionalDirsForProject returns the paths of project folders that should be
// passed to the `claude` CLI via --add-dir flags. It excludes:
//   - any folder whose path is empty
//   - any folder whose path equals cwd (already the working directory)
//   - duplicate paths (first occurrence wins)
//
// Order is preserved from the input slice (caller controls ordering; the repo
// returns default-first by convention).
func AdditionalDirsForProject(folders []*ent.ProjectFolder, cwd string) []string {
	if len(folders) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(folders))
	var dirs []string
	for _, f := range folders {
		if f == nil || f.Path == "" {
			continue
		}
		if f.Path == cwd {
			continue
		}
		if _, dup := seen[f.Path]; dup {
			continue
		}
		seen[f.Path] = struct{}{}
		dirs = append(dirs, f.Path)
	}
	return dirs
}
