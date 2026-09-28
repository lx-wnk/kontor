package materializer_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/materializer"
)

func userTarget(root string) materializer.Target {
	return materializer.Target{
		NodeID: "local", Provider: "claude",
		Layer: materializer.LayerUser, Root: root, Adapter: materializer.AdapterClaude,
	}
}

func TestSkillPath_UserLayer(t *testing.T) {
	got, err := materializer.SkillPath(userTarget("/home/u/.claude"), "code-review")
	require.NoError(t, err)
	require.Equal(t, "/home/u/.claude/skills/code-review/SKILL.md", got)
}

func TestSkillPath_ProjectLayer(t *testing.T) {
	target := materializer.Target{
		NodeID: "local", Provider: "claude",
		Layer: materializer.LayerProject, Root: "/work/repo", Adapter: materializer.AdapterClaude,
	}
	got, err := materializer.SkillPath(target, "deploy")
	require.NoError(t, err)
	require.Equal(t, "/work/repo/.claude/skills/deploy/SKILL.md", got)
}

func TestSkillPath_RefusesTraversalBeforeBuildingAnything(t *testing.T) {
	for _, slug := range []string{
		"../escape",
		"..",
		"a/b",
		"/absolute",
		"Upper",
		"with space",
		"trailing/",
		"",
	} {
		got, err := materializer.SkillPath(userTarget("/home/u/.claude"), slug)
		require.Error(t, err, "slug %q", slug)
		require.Equal(t, "", got, "no path may be returned for a refused slug: %q", slug)
	}
}

func TestSkillPath_RefusesATargetWithNoSkillFormat(t *testing.T) {
	target := materializer.Target{
		NodeID: "local", Provider: "codex",
		Layer: materializer.LayerUser, Root: "/home/u/.codex", Adapter: materializer.AdapterNone,
	}
	_, err := materializer.SkillPath(target, "code-review")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no skill format")
}

func TestSkillPath_RefusesAnUnknownLayer(t *testing.T) {
	target := userTarget("/home/u/.claude")
	target.Layer = "plugin"
	_, err := materializer.SkillPath(target, "code-review")
	require.Error(t, err, "plugin and builtin sources are not editable (cmdscope/enumerate.go:96-98)")
}
