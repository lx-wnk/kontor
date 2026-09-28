package plugin

import "github.com/lx-wnk/kontor/server/internal/validation"

// ValidID reports whether id is a well-formed plugin id. Plugin ids are slugs
// and use the canonical rule — validation.SlugRE — rather than a local copy.
// The id becomes a path segment under the plugin directory, so rejecting
// anything outside the pattern is also the traversal guard.
func ValidID(id string) bool {
	return validation.IsValidSlug(id)
}
