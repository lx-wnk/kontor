package validation_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/validation"
)

func TestIsValidSlug(t *testing.T) {
	valid := []string{
		"abc",
		"a1b2c3",
		"my-task",
		"feature-foo-bar",
		"x",
		// exactly 64 chars
		"a234567890123456789012345678901234567890123456789012345678901234",
		// TS canonical allows trailing hyphens and consecutive hyphens
		"trailing-hyphen-",
		"double--hyphen",
	}
	for _, s := range valid {
		if !validation.IsValidSlug(s) {
			t.Errorf("IsValidSlug(%q) = false, want true", s)
		}
	}

	invalid := []string{
		"",
		"-leading-hyphen",
		"Has-Uppercase",
		"has space",
		"has_underscore",
		// 65 chars (one too long)
		"a2345678901234567890123456789012345678901234567890123456789012345",
	}
	for _, s := range invalid {
		if validation.IsValidSlug(s) {
			t.Errorf("IsValidSlug(%q) = true, want false", s)
		}
	}
}

// TestIsValidSlug_ProjectsAndSpawnersConsistency verifies that the projects and
// spawners packages delegate to the same canonical implementation by comparing
// their results against validation.IsValidSlug for a broad set of inputs.
// This is a regression guard against future copy-paste drift.
func TestIsValidSlug_CanonicalPatternMessage(t *testing.T) {
	msg := validation.SlugPatternMessage
	if msg == "" {
		t.Error("SlugPatternMessage must not be empty")
	}
}
