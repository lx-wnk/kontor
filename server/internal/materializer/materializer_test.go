package materializer_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/materializer"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

type harness struct {
	m         *materializer.Materializer
	locks     repo.CoordLockRepo
	resources repo.ResourceRepo
	skills    repo.SkillRepo
	configDir string
	ctx       context.Context
}

// newHarness builds a materializer over an in-memory database and one config
// directory under t.TempDir(). No test in this package ever names a real
// config dir; the injected Resolver seams exist for exactly that reason.
func newHarness(t *testing.T) *harness {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	configDir := filepath.Join(t.TempDir(), ".claude")
	require.NoError(t, os.MkdirAll(configDir, 0o700))

	resolver := materializer.Resolver{
		NodeID:             repo.DefaultNodeID,
		ClaudeConfigDirs:   func() []string { return []string{configDir} },
		ProviderConfigDirs: func() []parser.ProviderConfigDir { return nil },
	}
	h := &harness{
		locks:     repo.NewCoordLockRepo(bundle.Client),
		resources: repo.NewResourceRepo(bundle.Client),
		skills:    repo.NewSkillRepo(bundle.Client),
		configDir: configDir,
		ctx:       context.Background(),
	}
	h.m = materializer.New(h.resources, h.skills, repo.NewMaterializationRepo(bundle.Client), h.locks, resolver)
	return h
}

func (h *harness) addSkill(t *testing.T, slug, body string) string {
	t.Helper()
	res, err := h.resources.Upsert(h.ctx, repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: slug, Name: slug,
		Scope: repo.GlobalScope(), State: repo.ResourceStateEnabled,
	})
	require.NoError(t, err)
	_, err = h.skills.Upsert(h.ctx, repo.UpsertSkillInput{
		ResourceID: res.ID, Description: "Review a diff", Body: body,
	})
	require.NoError(t, err)
	return res.ID
}

func (h *harness) skillPath(slug string) string {
	return filepath.Join(h.configDir, "skills", slug, "SKILL.md")
}

func onlyEntry(t *testing.T, rep materializer.Report) materializer.ReportEntry {
	t.Helper()
	require.Len(t, rep.Entries, 1)
	return rep.Entries[0]
}

func TestRun_CreatesTheFile(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.True(t, rep.Leased)
	require.Equal(t, materializer.OutcomeCreated, onlyEntry(t, rep).Outcome)

	got, rerr := os.ReadFile(h.skillPath("code-review"))
	require.NoError(t, rerr)
	require.Contains(t, string(got), "name: \"code-review\"")
	require.Contains(t, string(got), "v1")
}

func TestRun_SecondRunIsUnchanged(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	_, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeUnchanged, onlyEntry(t, rep).Outcome)
}

func TestRun_RepairsOurOwnDriftedFile(t *testing.T) {
	h := newHarness(t)
	id := h.addSkill(t, "code-review", "v1")
	_, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)

	_, err = h.skills.Upsert(h.ctx, repo.UpsertSkillInput{ResourceID: id, Description: "Review a diff", Body: "v2"})
	require.NoError(t, err)

	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeRepaired, onlyEntry(t, rep).Outcome)

	got, rerr := os.ReadFile(h.skillPath("code-review"))
	require.NoError(t, rerr)
	require.Contains(t, string(got), "v2")
}

func TestRun_AHandEditIsAConflictAndSurvivesEverySubsequentRun(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")
	_, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)

	edited := "---\nname: \"code-review\"\n---\n\nwhat a person actually wants here\n"
	require.NoError(t, os.WriteFile(h.skillPath("code-review"), []byte(edited), 0o600))

	for range 2 {
		rep, rerr := h.m.Run(h.ctx, false)
		require.NoError(t, rerr)
		require.Equal(t, materializer.OutcomeConflict, onlyEntry(t, rep).Outcome)

		got, gerr := os.ReadFile(h.skillPath("code-review"))
		require.NoError(t, gerr)
		require.Equal(t, edited, string(got), "no merge, no overwrite, and no retry that overwrites later")
	}
}

func TestRun_AForeignFileIsNeverTouchedAndIsRememberedAfterTheFirstReport(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	foreign := "---\nname: code-review\n---\n\nsomebody's own skill\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(h.skillPath("code-review")), 0o700))
	require.NoError(t, os.WriteFile(h.skillPath("code-review"), []byte(foreign), 0o600))

	first, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeForeign, onlyEntry(t, first).Outcome)
	require.Contains(t, onlyEntry(t, first).Detail, "first seen")

	second, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.Equal(t, materializer.OutcomeForeign, onlyEntry(t, second).Outcome)
	require.NotContains(t, onlyEntry(t, second).Detail, "first seen", "reported once, then remembered so it does not nag")

	got, rerr := os.ReadFile(h.skillPath("code-review"))
	require.NoError(t, rerr)
	require.Equal(t, foreign, string(got))
}

func TestRun_WithoutTheLeaseItWritesNothingAndNamesTheHolder(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	acquired, _, _, err := h.locks.Acquire(h.ctx, materializer.LeaseNamespace, repo.DefaultNodeID, "the-other-instance", materializer.DefaultLeaseTTL)
	require.NoError(t, err)
	require.True(t, acquired)

	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.False(t, rep.Leased)
	require.Equal(t, "the-other-instance", rep.LeaseHolder)
	require.Equal(t, materializer.OutcomeCreated, onlyEntry(t, rep).Outcome, "it still reports what it would write")
	require.Contains(t, onlyEntry(t, rep).Detail, "the-other-instance")

	_, statErr := os.Stat(h.skillPath("code-review"))
	require.True(t, os.IsNotExist(statErr), "the loser of a lease writes nothing")
}

func TestRun_DryRunWritesNothingAndTakesNoLease(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	rep, err := h.m.Run(h.ctx, true)
	require.NoError(t, err)
	require.True(t, rep.DryRun)
	require.False(t, rep.Leased)
	require.Equal(t, materializer.OutcomeCreated, onlyEntry(t, rep).Outcome)
	require.Contains(t, onlyEntry(t, rep).Detail, "dry run")

	_, statErr := os.Stat(h.skillPath("code-review"))
	require.True(t, os.IsNotExist(statErr))

	held, lerr := h.locks.ListActive(h.ctx, materializer.LeaseNamespace)
	require.NoError(t, lerr)
	require.Empty(t, held, "a run that writes nothing has no reason to lock the node")
}

func TestRun_TheLeaseIsReleasedForTheNextRun(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	_, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)

	held, lerr := h.locks.ListActive(h.ctx, materializer.LeaseNamespace)
	require.NoError(t, lerr)
	require.Empty(t, held)
}

func TestRun_AProviderWithNoSkillFormatIsARecordedNoOp(t *testing.T) {
	h := newHarness(t)
	codexDir := filepath.Join(t.TempDir(), ".codex")
	require.NoError(t, os.MkdirAll(codexDir, 0o700))
	h.m.Resolver.ProviderConfigDirs = func() []parser.ProviderConfigDir {
		return []parser.ProviderConfigDir{{Provider: "codex", Path: codexDir}}
	}
	h.addSkill(t, "code-review", "v1")

	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.Len(t, rep.Entries, 2)

	var codex materializer.ReportEntry
	for _, e := range rep.Entries {
		if e.Provider == "codex" {
			codex = e
		}
	}
	require.Equal(t, materializer.OutcomeUnsupported, codex.Outcome)
	require.Contains(t, codex.Detail, "no skill format")

	entries, rerr := os.ReadDir(codexDir)
	require.NoError(t, rerr)
	require.Empty(t, entries, "a no-op target touches no filesystem")
}

func TestRun_ADisabledResourceIsNotMaterialized(t *testing.T) {
	h := newHarness(t)
	id := h.addSkill(t, "code-review", "v1")
	_, err := h.resources.SetState(h.ctx, id, repo.ResourceStateDisabled)
	require.NoError(t, err)

	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.Empty(t, rep.Entries)
}

func TestRun_OneFailedTargetDoesNotStopTheOthersAndTheRunReportsPartial(t *testing.T) {
	h := newHarness(t)
	blocked := filepath.Join(t.TempDir(), ".claude-work")
	require.NoError(t, os.MkdirAll(blocked, 0o700))
	require.NoError(t, os.Mkdir(filepath.Join(blocked, "skills"), 0o500))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(blocked, "skills"), 0o700) })
	h.m.Resolver.ClaudeConfigDirs = func() []string { return []string{h.configDir, blocked} }
	h.addSkill(t, "code-review", "v1")

	rep, err := h.m.Run(h.ctx, false)
	require.NoError(t, err)
	require.True(t, rep.Partial, "a partial materialization is reported as partial")
	require.Len(t, rep.Entries, 2)

	got, rerr := os.ReadFile(h.skillPath("code-review"))
	require.NoError(t, rerr)
	require.Contains(t, string(got), "v1", "the writable target proceeded")
}

func TestRun_ReportsEveryTargetItConsidered(t *testing.T) {
	h := newHarness(t)
	h.addSkill(t, "code-review", "v1")

	rep, err := h.m.Run(h.ctx, true)
	require.NoError(t, err)
	require.Equal(t, []string{"claude|user|" + h.configDir}, rep.Targets)
}
