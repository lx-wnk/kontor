package materializer_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/materializer"
)

func want(t *testing.T, body string) []byte {
	t.Helper()
	return materializer.RenderClaudeSkill(materializer.Skill{
		ResourceID: "res-1", Slug: "code-review", Description: "Review a diff", Body: body,
	})
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func TestClassify_AbsentFileIsCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")

	got, err := materializer.Classify(path, want(t, "v1"), "")
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeCreated, got)
}

func TestClassify_AbsentFileWeOnceWroteIsCreatedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")

	got, err := materializer.Classify(path, want(t, "v1"), materializer.HashBytes(want(t, "v1")))
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeCreated, got, "a file we wrote and the user deleted may be written again")
}

func TestClassify_OurFileMatchingTheResourceIsUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")
	content := want(t, "v1")
	writeFile(t, path, content)

	got, err := materializer.Classify(path, content, materializer.HashBytes(content))
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeUnchanged, got)
}

func TestClassify_OurFileBehindTheResourceIsRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")
	old := want(t, "v1")
	writeFile(t, path, old)

	got, err := materializer.Classify(path, want(t, "v2"), materializer.HashBytes(old))
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeRepaired, got, "the database is the truth for a file we own")
}

func TestClassify_OurFileEditedByAHumanIsAConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")
	ours := want(t, "v1")
	writeFile(t, path, append(ours, []byte("\nand one line a person added\n")...))

	got, err := materializer.Classify(path, ours, materializer.HashBytes(ours))
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeConflict, got)
}

func TestClassify_AFileWeNeverWroteIsForeign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")
	writeFile(t, path, []byte("---\nname: code-review\n---\n\nsomebody's own skill\n"))

	got, err := materializer.Classify(path, want(t, "v1"), "")
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeForeign, got)
}

func TestClassify_AFileCarryingOurMarkerIsStillForeignWithoutARecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "skills", "code-review", "SKILL.md")
	writeFile(t, path, want(t, "v1"))

	got, err := materializer.Classify(path, want(t, "v1"), "")
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeForeign, got,
		"a marker is not proof of ownership; the record is (cmd/serve/hooks.go:252-263)")
}

func TestClassify_ASymlinkAtTheTargetIsForeign(t *testing.T) {
	root := t.TempDir()
	secret := filepath.Join(root, "elsewhere.md")
	require.NoError(t, os.WriteFile(secret, []byte("do not touch"), 0o600))

	path := filepath.Join(root, "skills", "code-review", "SKILL.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.Symlink(secret, path))

	got, err := materializer.Classify(path, want(t, "v1"), materializer.HashBytes(want(t, "v1")))
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeForeign, got,
		"never follow a symlink — the read side refuses the same shape at cmdscope/enumerate.go:378-382")
}
