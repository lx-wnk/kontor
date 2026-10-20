package validation

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxVaultFolderLen caps a project's vault folder, in runes.
const MaxVaultFolderLen = 255

// VaultFolderMessage describes the IsValidVaultFolder rule.
const VaultFolderMessage = "vaultFolder must be a category/project folder of the vault, such as work/shop"

// IsValidVaultFolder reports whether f names a vault project folder the hub can seat
// agents in: exactly two non-empty segments, no "." or ".." segment, no backslash, no
// control character and no whitespace around a segment.
func IsValidVaultFolder(f string) bool {
	if utf8.RuneCountInString(f) > MaxVaultFolderLen || strings.Contains(f, `\`) || strings.IndexFunc(f, unicode.IsControl) >= 0 {
		return false
	}
	segments := strings.Split(f, "/")
	if len(segments) != 2 {
		return false
	}
	for _, s := range segments {
		if s == "" || s == "." || s == ".." || s != strings.TrimSpace(s) {
			return false
		}
	}
	return true
}
