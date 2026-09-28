package pipeline_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/permissions"
	"github.com/lx-wnk/kontor/server/internal/pipeline"
	"github.com/lx-wnk/kontor/server/internal/taskcontrol"
	"github.com/stretchr/testify/require"
)

// parityGitPushRE mirrors pipeline's unexported gitPushRE (spawner.go). It
// cannot be imported, so it is deliberately duplicated here: git-push
// containment is a validation rule that belongs to the grant-translation
// layer, not to capability.Decide, and this golden-parity test needs its own
// copy to build grants the same way production code does.
var parityGitPushRE = regexp.MustCompile(`(?i)\bgit push\b`)

// toGrant translates one TaskPermission fixture into the GrantView
// capability.Decide would need to resolve it, applying exactly the
// non-Decide-owned validation BuildAllowList applies before it ever reaches
// the append: tool allow-list membership, blanket-Bash rejection, git-push
// containment, Bash command safety, and bare-WebFetch rejection. Decide has
// no notion of any of these — it only resolves context specificity, mode
// ranking, expiry and revocation — so this helper exists to draw exactly
// that line. ok is false when no allow grant should exist for the
// permission at all.
func toGrant(p *ent.TaskPermission, taskID string, allowGitPush bool) (grant capability.GrantView, value string, ok bool) {
	if !p.Granted {
		return capability.GrantView{}, "", false // rule 1: !p.Granted
	}
	if !permissions.IsAllowedTool(p.Tool) {
		return capability.GrantView{}, "", false // rule 3: tool not on the allow-list
	}

	switch p.Tool {
	case "Bash":
		if p.Pattern == nil || *p.Pattern == "" {
			return capability.GrantView{}, "", false // rule 4: blanket Bash forbidden
		}
		normalized := strings.Join(strings.Fields(*p.Pattern), " ")
		if !p.ManualOverride {
			if !allowGitPush && parityGitPushRE.MatchString(normalized) {
				return capability.GrantView{}, "", false // rule 5: git push forbidden
			}
			if safe, _ := permissions.IsSafeBashPattern(normalized); !safe {
				return capability.GrantView{}, "", false // rule 6: unsafe Bash pattern
			}
		}
		value = normalized
	case "WebFetch":
		if p.Pattern == nil || strings.TrimSpace(*p.Pattern) == "" {
			return capability.GrantView{}, "", false // rule 7: bare WebFetch forbidden
		}
		value = strings.TrimSpace(*p.Pattern)
	default:
		if p.Pattern != nil {
			value = *p.Pattern
		}
	}

	return capability.GrantView{
		ID:          p.ID,
		Capability:  p.Tool,
		ContextKind: "task",
		ContextRef:  taskID,
		Pattern:     value,
		Mode:        "allow",
		ExpiresAt:   p.ExpiresAt,
	}, value, true
}

// formatAllowEntry renders a resolved (tool, value) pair the same way
// BuildAllowList formats its --allowedTools entries.
func formatAllowEntry(tool, value string) string {
	switch tool {
	case "Bash":
		return fmt.Sprintf("Bash(%s)", value)
	case "WebFetch":
		return fmt.Sprintf("WebFetch(%s)", value)
	default:
		if value == "" {
			return tool
		}
		return fmt.Sprintf("%s(%s)", tool, value)
	}
}

// buildViaDecide reproduces BuildAllowList's restrictive-path output by
// translating each permission into a grant (toGrant) and resolving it
// through capability.Decide, one capability at a time. Permissions are
// walked in the same order BuildAllowList iterates perms, so list order is
// preserved identically.
func buildViaDecide(perms []*ent.TaskPermission, taskID string, allowGitPush bool) []string {
	type survivor struct{ tool, value string }

	grantsByTool := map[string][]capability.GrantView{}
	var survivors []survivor
	for _, p := range perms {
		g, value, ok := toGrant(p, taskID, allowGitPush)
		if !ok {
			continue
		}
		grantsByTool[p.Tool] = append(grantsByTool[p.Tool], g)
		survivors = append(survivors, survivor{tool: p.Tool, value: value})
	}

	contexts := []capability.Context{{Kind: "task", Ref: taskID}}
	var out []string
	for _, s := range survivors {
		capView := pipeline.CapabilityViewForTest(s.tool)
		req := capability.Request{Capability: s.tool, Value: s.value, Contexts: contexts}
		if capability.Decide(req, grantsByTool[s.tool], capView).Effect == capability.EffectAllow {
			out = append(out, formatAllowEntry(s.tool, s.value))
		}
	}
	return out
}

// assertParity runs both paths — BuildAllowList's own grant-translation
// (resolvePermissionDecisions) and this file's independent
// grant-translation-plus-Decide path (buildViaDecide) — over the same
// fixtures under autonomy "manual" (the only autonomy where BuildAllowList
// reads perms at all, see exception A below) and requires an identical,
// identically ordered result. There is no separate legacy filter chain
// being compared here any more: buildViaDecide duplicates
// resolvePermissionDecisions's own validation, so a passing run pins that
// duplication rather than proving Decide replaced an older code path.
func assertParity(t *testing.T, perms []*ent.TaskPermission, allowGitPush bool) {
	t.Helper()
	const taskID = "t1"
	want := pipeline.BuildAllowList("manual", perms, false, allowGitPush, nil)
	got := buildViaDecide(perms, taskID, allowGitPush)
	require.Equal(t, want, got, "capability.Decide must reproduce BuildAllowList's output element for element and order for order")
}

// TestAllowListParity is a change-detector, not a proof against a legacy
// implementation: it pins the seven drop rules BuildAllowList's
// grant-translation layer implements (spawner.go's
// resolvePermissionDecisions) against this file's independent
// re-implementation of the same rules (toGrant, buildViaDecide), so a
// future edit that silently changes one of those rules turns this test red.
// It also covers the two carve-outs below that are deliberately out of
// scope for capability.Decide.
func TestAllowListParity(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	safePattern := "pnpm test"
	expiredPattern := "pnpm build"
	overridePattern := "git push origin HEAD"
	gitPushPattern := "git push origin HEAD"
	unsafePattern := "chmod +x ./x.sh"
	webfetchPattern := "https://docs.example.com*"
	grepPattern := "src/**/*.go"

	t.Run("combined fixture set matches element for element and order for order", func(t *testing.T) {
		perms := []*ent.TaskPermission{
			{ID: "p1", Tool: "Bash", Pattern: &safePattern, Granted: true},                           // survives
			{ID: "p2", Tool: "Bash", Pattern: &expiredPattern, Granted: true, ExpiresAt: &past},      // rule 2
			{ID: "p3", Tool: "Bash", Pattern: &overridePattern, Granted: true, ManualOverride: true}, // override bypasses rules 5+6
			{ID: "p4", Tool: "WebFetch", Granted: true},                                              // rule 7
			{ID: "p5", Tool: "Bash", Granted: true},                                                  // rule 4
			{ID: "p6", Tool: "Read", Granted: false},                                                 // rule 1
			{ID: "p7", Tool: "ShellExec", Granted: true},                                             // rule 3
			{ID: "p8", Tool: "Bash", Pattern: &gitPushPattern, Granted: true},                        // rule 5
			{ID: "p9", Tool: "Bash", Pattern: &unsafePattern, Granted: true},                         // rule 6
			{ID: "p10", Tool: "WebFetch", Pattern: &webfetchPattern, Granted: true},                  // survives
			{ID: "p11", Tool: "Read", Granted: true},                                                 // survives, bare tool
			{ID: "p12", Tool: "Grep", Pattern: &grepPattern, Granted: true},                          // survives, tool(pattern)
		}
		assertParity(t, perms, false)
	})

	t.Run("rule 1: ungranted permission is dropped", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{{ID: "p1", Tool: "Read", Granted: false}}, false)
	})

	t.Run("rule 2: expired permission is dropped", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{
			{ID: "p1", Tool: "Bash", Pattern: &safePattern, Granted: true, ExpiresAt: &past},
		}, false)
	})

	t.Run("rule 3: tool not on the allow-list is dropped", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{{ID: "p1", Tool: "ShellExec", Granted: true}}, false)
	})

	t.Run("rule 4: blanket Bash with no pattern is dropped", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{{ID: "p1", Tool: "Bash", Granted: true}}, false)
	})

	t.Run("rule 4: empty-string Bash pattern is dropped (currently backstopped by rule 6)", func(t *testing.T) {
		// Pattern is a non-nil empty string, distinct from Pattern: nil above — this
		// pins the "*p.Pattern == \"\"" half of rule 4 on its own. Today it is not
		// actually rule 4 doing the work in a hypothetical narrowed-rule-4 world:
		// an empty pattern also fails permissions.IsSafeBashPattern (rule 6, "empty
		// pattern"), so removing rule 4's empty-string check alone would NOT turn
		// this fixture red. That backstop is an accident of rule ordering, not a
		// guarantee — record it here so a future reader does not mistake this
		// fixture for proof that rule 4's empty-string half is independently
		// covered.
		assertParity(t, []*ent.TaskPermission{
			{ID: "bash-empty-pattern", Tool: "Bash", Pattern: ptr(""), Granted: true},
		}, false)
	})

	t.Run("rule 5: git-push Bash pattern is dropped when push is disallowed", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{
			{ID: "p1", Tool: "Bash", Pattern: &gitPushPattern, Granted: true},
		}, false)
	})

	t.Run("rule 6: unsafe Bash pattern is dropped", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{
			{ID: "p1", Tool: "Bash", Pattern: &unsafePattern, Granted: true},
		}, false)
	})

	t.Run("rule 7: bare WebFetch with no pattern is dropped", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{{ID: "p1", Tool: "WebFetch", Granted: true}}, false)
	})

	t.Run("rule 7: whitespace-only WebFetch pattern is dropped", func(t *testing.T) {
		// Pattern is non-nil but blank, distinct from Pattern: nil above — this
		// pins the "strings.TrimSpace(*p.Pattern) == \"\"" half of rule 7 on its
		// own. WebFetch has no downstream safety net the way Bash has
		// IsSafeBashPattern, so if this half of the guard is ever dropped, the
		// result is an unrestricted WebFetch() allow entry with nothing else to
		// catch it.
		assertParity(t, []*ent.TaskPermission{
			{ID: "webfetch-whitespace-pattern", Tool: "WebFetch", Pattern: ptr("   "), Granted: true},
		}, false)
	})

	t.Run("manual_override bypasses the git-push and Bash-safety gates", func(t *testing.T) {
		assertParity(t, []*ent.TaskPermission{
			{ID: "p1", Tool: "Bash", Pattern: &overridePattern, Granted: true, ManualOverride: true},
		}, false)
	})

	// Exception A: the allow-all short-circuit is deliberately out of scope
	// for the Decider. BuildAllowList never reads perms on this path, so
	// there is nothing to translate into grants — synthesising wildcard
	// grants to make it comparable would fabricate policy nobody authored.
	// Asserted here as its own claim: the perms argument has zero effect on
	// the result for spec_gated/full autonomy.
	t.Run("exception A: allow-all autonomy is out of scope for the Decider", func(t *testing.T) {
		perms := []*ent.TaskPermission{{ID: "p1", Tool: "Bash", Pattern: &safePattern, Granted: true}}
		for _, autonomy := range []string{"spec_gated", "full"} {
			got := pipeline.BuildAllowList(autonomy, perms, false, false, nil)
			require.Equal(t, taskcontrol.PermissiveAllowList(false), got,
				"autonomy=%s must return the permissive allow-list untouched by perms", autonomy)
		}
	})

	// Exception B: channel entries bypass the gate by design —
	// enableChannel prepends them unconditionally, consulting no grant.
	// Asserted separately; only the tail is compared against the
	// grant-resolved list.
	t.Run("exception B: channel tools bypass the gate and are asserted separately", func(t *testing.T) {
		perms := []*ent.TaskPermission{{ID: "p1", Tool: "Bash", Pattern: &safePattern, Granted: true}}
		got := pipeline.BuildAllowList("manual", perms, true, false, nil)
		// Derived from the bridge's own tool list rather than written out here:
		// spelling the entries a second time is what let set_stage_output go
		// missing from the real allow-list without any test noticing.
		channel := channelconfig.AllowListEntries()
		require.GreaterOrEqual(t, len(got), len(channel))
		require.Equal(t, channel, got[:len(channel)],
			"channel entries are prepended unconditionally, consulting no grant")
		require.Equal(t, buildViaDecide(perms, "t1", false), got[len(channel):],
			"the remainder must still match the grant-resolved list")
	})
}
