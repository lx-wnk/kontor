package validation_test

import (
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/validation"
)

func TestIsValidVaultFolder(t *testing.T) {
	valid := []string{"work/babyone-shop", "private/kontor", "Work/My Shop"}
	for _, f := range valid {
		if !validation.IsValidVaultFolder(f) {
			t.Errorf("IsValidVaultFolder(%q) = false, want true", f)
		}
	}

	invalid := []string{
		"",
		"work",
		"work/",
		"/work/shop",
		"work/shop/",
		"work/shop/notes",
		"../shop",
		"work/..",
		"./shop",
		" work/shop",
		"work/shop ",
		`work\shop`,
		"work/a\nb",
		"work/a\x00b",
		"work/" + strings.Repeat("s", validation.MaxVaultFolderLen),
	}
	for _, f := range invalid {
		if validation.IsValidVaultFolder(f) {
			t.Errorf("IsValidVaultFolder(%q) = true, want false", f)
		}
	}
}
