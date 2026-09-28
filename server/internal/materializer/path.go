package materializer

import (
	"fmt"
	"path/filepath"

	"github.com/lx-wnk/kontor/server/internal/validation"
)

// SkillPath builds the SKILL.md path for slug in t, following the layout
// cmdscope enumerates (spec §3, read from cmdscope/enumerate.go:255-260):
//
//	user    <ConfigDir>/skills/<slug>/SKILL.md
//	project <ProjectCwd>/.claude/skills/<slug>/SKILL.md
//
// This is the path-traversal boundary. The slug is not a path segment a caller
// chooses; it is a validated identifier, and it is validated before any join
// happens — PUT /api/config/file gets the same guarantee from a different
// direction, by refusing every path that is not already in its enumerated
// allow-list (api/config/file.go:151-174).
func SkillPath(t Target, slug string) (string, error) {
	if t.Adapter != AdapterClaude {
		return "", fmt.Errorf("target %s has no skill format", t.Key())
	}
	if !validation.IsValidSlug(slug) {
		return "", fmt.Errorf("skill slug %q refused: %s", slug, validation.SlugPatternMessage)
	}
	switch t.Layer {
	case LayerUser:
		return filepath.Join(t.Root, "skills", slug, "SKILL.md"), nil
	case LayerProject:
		return filepath.Join(t.Root, ".claude", "skills", slug, "SKILL.md"), nil
	default:
		return "", fmt.Errorf("target %s: layer %q is not writable", t.Key(), t.Layer)
	}
}
