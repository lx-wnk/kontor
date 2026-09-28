package materializer_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/materializer"
)

func TestRenderClaudeSkill_Golden(t *testing.T) {
	got := string(materializer.RenderClaudeSkill(materializer.Skill{
		ResourceID:  "res-42",
		Slug:        "code-review",
		Description: "Review a diff for correctness",
		Body:        "# Code Review\n\nRead the diff before the ticket.",
	}))

	require.Equal(t, `---
name: "code-review"
description: "Review a diff for correctness"
x-dashboard-resource: "res-42"
---

# Code Review

Read the diff before the ticket.
`, got)
}

func TestRenderClaudeSkill_NameIsAlwaysTheSlug(t *testing.T) {
	got := string(materializer.RenderClaudeSkill(materializer.Skill{
		ResourceID: "res-1", Slug: "deploy", Description: "", Body: "x",
	}))
	require.Contains(t, got, "name: \"deploy\"",
		"the directory-name fallback at cmdscope/enumerate.go:286-289 must never have to fire")
}

func TestRenderClaudeSkill_DescriptionCannotBreakOutOfTheFrontmatter(t *testing.T) {
	got := string(materializer.RenderClaudeSkill(materializer.Skill{
		ResourceID: "res-1", Slug: "x",
		Description: "line one\n---\nname: hijacked",
		Body:        "body",
	}))

	require.Equal(t, 2, strings.Count(got, "---\n"), "exactly one opening and one closing fence")
	require.Contains(t, got, `description: "line one\n---\nname: hijacked"`)
}

func TestRenderClaudeSkill_BodyEndsWithExactlyOneNewline(t *testing.T) {
	for _, body := range []string{"text", "text\n", "text\n\n\n"} {
		got := string(materializer.RenderClaudeSkill(materializer.Skill{
			ResourceID: "r", Slug: "s", Body: body,
		}))
		require.True(t, strings.HasSuffix(got, "text\n"), "body %q", body)
		require.False(t, strings.HasSuffix(got, "text\n\n"), "body %q", body)
	}
}

func TestRenderClaudeSkill_IsDeterministic(t *testing.T) {
	s := materializer.Skill{ResourceID: "r", Slug: "s", Description: "d", Body: "b"}
	require.Equal(t,
		materializer.HashBytes(materializer.RenderClaudeSkill(s)),
		materializer.HashBytes(materializer.RenderClaudeSkill(s)),
		"an unstable render would report repaired on every run and rewrite the file forever")
}
