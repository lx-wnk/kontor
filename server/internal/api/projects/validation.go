// Package projects implements CRUD endpoints for Projects and ProjectFolders.
package projects

import (
	"strings"

	"github.com/lx-wnk/kontor/server/internal/validation"
)

// ValidateSlug returns true when s is a valid slug.
// Delegates to the canonical validation package — do not define a local copy.
func ValidateSlug(s string) bool {
	return validation.IsValidSlug(s)
}

// ValidateColor returns true when c is a valid 3- or 6-digit hex color.
// Delegates to the canonical validation package — do not define a local copy.
func ValidateColor(c string) bool {
	return validation.IsValidColor(c)
}

// ValidateName returns true when n is within the shared project-name limit.
// Delegates to the canonical validation package — do not define a local copy.
func ValidateName(n string) bool {
	return validation.IsValidProjectName(n)
}

// ValidateDescription returns true when d is within the shared project-description limit.
// Delegates to the canonical validation package — do not define a local copy.
func ValidateDescription(d string) bool {
	return validation.IsValidProjectDescription(d)
}

// ValidateAbsolutePath returns true when p is an absolute POSIX path with no
// ".." segment. Empty paths are rejected. Trailing slashes are tolerated.
func ValidateAbsolutePath(p string) bool {
	if p == "" {
		return false
	}
	if !strings.HasPrefix(p, "/") {
		return false
	}
	// Reject any ".." segment (handles "..", "/..", "/foo/..", "/foo/../bar").
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return false
		}
	}
	return true
}
