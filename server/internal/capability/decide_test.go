package capability_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/capability"
)

func TestDecideSpecificityAndDenyPrecedence(t *testing.T) {
	cap := capability.CapabilityView{Name: "Bash", Class: "tool", EnforceableBy: []string{"spawn"}}

	tests := []struct {
		name   string
		grants []capability.GrantView
		want   capability.Effect
		why    string
	}{
		{
			name:   "no grants falls back to ask",
			grants: nil,
			want:   capability.EffectAsk,
			why:    "a tool capability defaults to asking",
		},
		{
			name: "a global allow is honoured",
			grants: []capability.GrantView{
				{ID: "g1", Capability: "Bash", ContextKind: "global", Mode: "allow", Pattern: "git status*"},
			},
			want: capability.EffectAllow,
		},
		{
			name: "a task deny overrules a global allow",
			grants: []capability.GrantView{
				{ID: "g1", Capability: "Bash", ContextKind: "global", Mode: "allow", Pattern: "git status*"},
				{ID: "g2", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "deny", Pattern: "git status*"},
			},
			want: capability.EffectDeny,
			why:  "the more specific context wins outright, it does not merge",
		},
		{
			name: "a global deny does NOT overrule a task allow",
			grants: []capability.GrantView{
				{ID: "g1", Capability: "Bash", ContextKind: "global", Mode: "deny", Pattern: "git status*"},
				{ID: "g2", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "allow", Pattern: "git status*"},
			},
			want: capability.EffectAllow,
			why:  "specificity is decided before mode; deny only wins within a level",
		},
		{
			name: "deny beats allow inside one level",
			grants: []capability.GrantView{
				{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "allow", Pattern: "git status*"},
				{ID: "g2", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "deny", Pattern: "git status*"},
			},
			want: capability.EffectDeny,
		},
		{
			name: "a non-matching pattern does not count as a grant",
			grants: []capability.GrantView{
				{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "allow", Pattern: "git push*"},
			},
			want: capability.EffectAsk,
			why:  "the grant exists but does not cover this value, so the level is empty",
		},
	}

	req := capability.Request{
		Capability: "Bash",
		Value:      "git status --short",
		Contexts: []capability.Context{
			{Kind: "task", Ref: "t1"},
			{Kind: "project", Ref: "/p"},
			{Kind: "global", Ref: ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := capability.Decide(req, tt.grants, cap)
			if got.Effect != tt.want {
				t.Errorf("Effect = %v, want %v (%s)", got.Effect, tt.want, tt.why)
			}
		})
	}
}

func TestDecideExpiredAndRevokedAreIgnored(t *testing.T) {
	cap := capability.CapabilityView{Name: "Bash", Class: "tool"}
	past := time.Now().Add(-time.Hour)
	req := capability.Request{
		Capability: "Bash",
		Value:      "git status",
		Contexts:   []capability.Context{{Kind: "global"}},
	}

	expired := []capability.GrantView{
		{ID: "g1", Capability: "Bash", ContextKind: "global", Mode: "allow", Pattern: "", ExpiresAt: &past},
	}
	if got := capability.Decide(req, expired, cap); got.Effect != capability.EffectAsk {
		t.Errorf("expired grant: Effect = %v, want ask", got.Effect)
	}

	revoked := []capability.GrantView{
		{ID: "g1", Capability: "Bash", ContextKind: "global", Mode: "allow", Pattern: "", RevokedAt: &past},
	}
	if got := capability.Decide(req, revoked, cap); got.Effect != capability.EffectAsk {
		t.Errorf("revoked grant: Effect = %v, want ask", got.Effect)
	}
}

func TestDecideDefaultEffectPerClass(t *testing.T) {
	req := capability.Request{
		Capability: "whatever",
		Value:      "whatever",
		Contexts:   []capability.Context{{Kind: "global"}},
	}

	tests := []struct {
		class string
		want  capability.Effect
	}{
		{"tool", capability.EffectAsk},
		{"reach", capability.EffectAsk},
		{"resource", capability.EffectAsk},
		{"spend", capability.EffectDeny},
		{"nonsense", capability.EffectDeny},
	}

	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			cap := capability.CapabilityView{Name: "whatever", Class: tt.class}
			got := capability.Decide(req, nil, cap)
			if got.Effect != tt.want {
				t.Errorf("class %q: Effect = %v, want %v", tt.class, got.Effect, tt.want)
			}
		})
	}
}

func TestDecideScopeFilterDistinguishesSameKindDifferentRef(t *testing.T) {
	// Two contexts share the "task" kind with different refs (e.g. a
	// sub-task nested under a parent task). A grant scoped to the
	// first-listed ref must still be considered — a scope filter keyed on
	// kind alone would silently keep only the last ref and drop this grant,
	// falling through to the capability default (ask) instead of deny.
	cap := capability.CapabilityView{Name: "Bash", Class: "tool"}
	req := capability.Request{
		Capability: "Bash",
		Value:      "git status",
		Contexts: []capability.Context{
			{Kind: "task", Ref: "t1"},
			{Kind: "task", Ref: "t2"},
		},
	}

	grants := []capability.GrantView{
		{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "deny"},
	}

	got := capability.Decide(req, grants, cap)
	if got.Effect != capability.EffectDeny {
		t.Errorf("Effect = %v, want deny — the t1-scoped grant must be considered even though t2 is also in the chain", got.Effect)
	}
}

func TestDecideReasonNamesModeAndContextKind(t *testing.T) {
	cap := capability.CapabilityView{Name: "Bash", Class: "tool"}
	req := capability.Request{
		Capability: "Bash",
		Value:      "git status",
		Contexts:   []capability.Context{{Kind: "task", Ref: "t1"}},
	}
	got := capability.Decide(req, []capability.GrantView{
		{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "deny"},
	}, cap)
	if !strings.Contains(got.Reason, "deny") {
		t.Errorf("Reason = %q, want it to name the winning mode (deny)", got.Reason)
	}
	if !strings.Contains(got.Reason, "task") {
		t.Errorf("Reason = %q, want it to name the context kind (task)", got.Reason)
	}
}

func TestDecideUnknownContextKindIsDroppedNotSilent(t *testing.T) {
	cap := capability.CapabilityView{Name: "Bash", Class: "tool"}
	req := capability.Request{
		Capability: "Bash",
		Value:      "git status",
		Contexts:   []capability.Context{{Kind: "bogus", Ref: "x"}},
	}
	got := capability.Decide(req, []capability.GrantView{
		{ID: "g1", Capability: "Bash", ContextKind: "bogus", ContextRef: "x", Mode: "allow"},
	}, cap)
	if got.Effect != capability.EffectAsk {
		t.Errorf("Effect = %v, want the class default (ask) — an unrecognised context kind must not apply", got.Effect)
	}
	if !strings.Contains(got.Reason, "unrecognised") {
		t.Errorf("Reason = %q, want it to mention the grant dropped for an unrecognised context kind", got.Reason)
	}
}

func TestDecisionCarriesEnforceability(t *testing.T) {
	cap := capability.CapabilityView{Name: "mail.send", Class: "reach", EnforceableBy: []string{"server"}}
	req := capability.Request{Capability: "mail.send", Contexts: []capability.Context{{Kind: "global"}}}
	got := capability.Decide(req, []capability.GrantView{
		{ID: "g1", Capability: "mail.send", ContextKind: "global", Mode: "allow"},
	}, cap)
	if len(got.Enforceable) != 1 || got.Enforceable[0] != "server" {
		t.Errorf("Enforceable = %v, want the capability's own list — the UI states this where the grant is made", got.Enforceable)
	}
}

// TestDecideUnrecognisedModeIsDroppedNotRanked proves that a grant whose Mode
// is not a key in modeRank does not silently rank as "deny" (the zero value
// of the missing-key lookup) and so cannot hide behind, or outrank, a real
// deny at the same context level. It must be dropped from ranking entirely,
// mirroring how an unrecognised ContextKind is dropped above.
func TestDecideUnrecognisedModeIsDroppedNotRanked(t *testing.T) {
	cap := capability.CapabilityView{Name: "Bash", Class: "tool"}
	req := capability.Request{
		Capability: "Bash",
		Value:      "git status",
		Contexts:   []capability.Context{{Kind: "task", Ref: "t1"}},
	}

	t.Run(`capitalised "Deny" ahead of a real deny still denies`, func(t *testing.T) {
		grants := []capability.GrantView{
			{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "Deny"},
			{ID: "g2", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "deny"},
		}
		got := capability.Decide(req, grants, cap)
		if got.Effect != capability.EffectDeny {
			t.Errorf("Effect = %v, want deny — an unrecognised mode must not outrank a real deny", got.Effect)
		}
	})

	t.Run("empty mode ahead of a real deny still denies", func(t *testing.T) {
		grants := []capability.GrantView{
			{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: ""},
			{ID: "g2", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "deny"},
		}
		got := capability.Decide(req, grants, cap)
		if got.Effect != capability.EffectDeny {
			t.Errorf("Effect = %v, want deny — an unrecognised mode must not outrank a real deny", got.Effect)
		}
	})

	t.Run("an unrecognised mode as the only candidate at its level never wins", func(t *testing.T) {
		grants := []capability.GrantView{
			{ID: "g1", Capability: "Bash", ContextKind: "task", ContextRef: "t1", Mode: "bogus"},
		}
		got := capability.Decide(req, grants, cap)
		if got.GrantID != "" {
			t.Errorf("GrantID = %q, want empty — a grant with an unrecognised mode must not participate in ranking, let alone win", got.GrantID)
		}
		if got.Effect != capability.EffectAsk {
			t.Errorf("Effect = %v, want ask (the class default, since no grant survives to decide)", got.Effect)
		}
	})
}

// TestDecideCapabilityMismatchNeverResolves proves a grant for one capability
// cannot resolve a request for another, even when the grant's Pattern
// happens to match the requested Value — the capability axis is filtered in
// Decide itself rather than left to caller discipline (spawner.go buckets
// grants by tool today, but nothing in Decide enforced that boundary).
func TestDecideCapabilityMismatchNeverResolves(t *testing.T) {
	cap := capability.CapabilityView{Name: "WebFetch", Class: "tool"}
	req := capability.Request{
		Capability: "WebFetch",
		Value:      "example.com",
		Contexts:   []capability.Context{{Kind: "global"}},
	}
	grants := []capability.GrantView{
		{ID: "g1", Capability: "Bash", ContextKind: "global", Mode: "allow", Pattern: "example.com"},
	}

	got := capability.Decide(req, grants, cap)
	if got.GrantID != "" {
		t.Errorf("GrantID = %q, want empty — a Bash grant must not resolve a WebFetch request", got.GrantID)
	}
	if got.Effect != capability.EffectAsk {
		t.Errorf("Effect = %v, want ask (the class default) — the pattern match alone must not be enough", got.Effect)
	}
}

func TestMostSpecific(t *testing.T) {
	cases := []struct {
		name   string
		in     []capability.Context
		want   capability.Context
		wantOK bool
		why    string
	}{
		{
			name:   "narrowest wins regardless of slice order",
			in:     []capability.Context{{Kind: "global"}, {Kind: "task", Ref: "t1"}, {Kind: "project", Ref: "/p"}},
			want:   capability.Context{Kind: "task", Ref: "t1"},
			wantOK: true,
			why:    "Contexts' order is documented as not load-bearing; the ranking must decide",
		},
		{
			name:   "an unrecognised kind is skipped, not treated as least specific",
			in:     []capability.Context{{Kind: "nope", Ref: "x"}, {Kind: "global"}},
			want:   capability.Context{Kind: "global"},
			wantOK: true,
			why:    "Decide drops an unknown kind entirely; naming one here would label a level Decide never honours",
		},
		{
			name:   "only unrecognised kinds reports not-ok",
			in:     []capability.Context{{Kind: "nope"}, {Kind: "alsonope"}},
			wantOK: false,
			why:    "there is no level to name",
		},
		{
			name:   "empty input reports not-ok",
			in:     nil,
			wantOK: false,
			why:    "same reason, without the typo",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := capability.MostSpecific(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v — %s", ok, tc.wantOK, tc.why)
			}
			if ok && got != tc.want {
				t.Errorf("MostSpecific = %+v, want %+v — %s", got, tc.want, tc.why)
			}
		})
	}
}
