package capability_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
)

func TestWithinLimit(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		used  int
		want  bool
	}{
		{"zero limit means unlimited", 0, 9999, true},
		{"under the limit", 3, 2, true},
		{"at the limit is exhausted", 3, 3, false},
		{"over the limit", 3, 4, false},
		{"negative limit is exhausted, not unlimited", -1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := capability.GrantView{LimitCount: tt.limit, LimitWindowSeconds: 3600}
			if got := capability.WithinLimit(g, tt.used); got != tt.want {
				t.Errorf("WithinLimit(limit=%d, used=%d) = %v, want %v", tt.limit, tt.used, got, tt.want)
			}
		})
	}
}

// TestServerEnforcerLimitExhaustedAsks proves an exhausted limit on an
// otherwise-allowed decision becomes an ask, not a silent deny and not a
// silent allow — spec §6: the user must be told which cap they hit.
func TestServerEnforcerLimitExhaustedAsks(t *testing.T) {
	asker := &recordingAsker{answer: true}
	e := capability.ServerEnforcer{Asker: asker}
	g := capability.GrantView{ID: "g1", LimitCount: 3, LimitWindowSeconds: 3600}
	d := capability.Decision{Effect: capability.EffectAllow, GrantID: "g1"}

	err := e.Enforce(context.Background(), capability.Request{}, d, g, 3)
	if err != nil {
		t.Fatalf("an asker that grants must let the call through: %v", err)
	}
	if !asker.called {
		t.Error("an exhausted limit must consult the asker, not silently deny or silently allow")
	}
}

// TestServerEnforcerLimitExhaustedReasonNamesTheLimit proves the ask reason
// tells the user which cap they hit, not merely that something was refused.
func TestServerEnforcerLimitExhaustedReasonNamesTheLimit(t *testing.T) {
	asker := &recordingAsker{answer: false}
	e := capability.ServerEnforcer{Asker: asker}
	g := capability.GrantView{ID: "g1", LimitCount: 3, LimitWindowSeconds: 3600}
	d := capability.Decision{Effect: capability.EffectAllow, GrantID: "g1"}

	err := e.Enforce(context.Background(), capability.Request{}, d, g, 3)
	if !errors.Is(err, capability.ErrDenied) {
		t.Fatalf("Enforce = %v, want it to wrap ErrDenied when the asker refuses", err)
	}
	seen := asker.lastDecision.Reason
	if !strings.Contains(seen, "3") {
		t.Errorf("reason %q does not name the limit", seen)
	}
}

// TestServerEnforcerLimitWithinBoundsAllowsWithoutAsking proves a decision
// under its limit is allowed directly — the asker is only a fallback for
// exhaustion, not consulted on every allow.
func TestServerEnforcerLimitWithinBoundsAllowsWithoutAsking(t *testing.T) {
	asker := &recordingAsker{}
	e := capability.ServerEnforcer{Asker: asker}
	g := capability.GrantView{ID: "g1", LimitCount: 3, LimitWindowSeconds: 3600}
	d := capability.Decision{Effect: capability.EffectAllow, GrantID: "g1"}

	if err := e.Enforce(context.Background(), capability.Request{}, d, g, 2); err != nil {
		t.Fatalf("allow under the limit must pass: %v", err)
	}
	if asker.called {
		t.Error("an allow under the limit must not consult the asker")
	}
}

// TestServerEnforcerLimitUnlimitedNeverAsks proves limit_count 0 (unlimited)
// never triggers an ask no matter how high usedInWindow is.
func TestServerEnforcerLimitUnlimitedNeverAsks(t *testing.T) {
	asker := &recordingAsker{}
	e := capability.ServerEnforcer{Asker: asker}
	g := capability.GrantView{ID: "g1", LimitCount: 0, LimitWindowSeconds: 3600}
	d := capability.Decision{Effect: capability.EffectAllow, GrantID: "g1"}

	if err := e.Enforce(context.Background(), capability.Request{}, d, g, 999999); err != nil {
		t.Fatalf("an unlimited grant must pass regardless of usage: %v", err)
	}
	if asker.called {
		t.Error("an unlimited grant must not consult the asker")
	}
}

// TestServerEnforcerLimitIgnoredForNonAllow proves the limit check only
// applies to an allow decision — a deny or ask decision passes through to
// Enforce unmodified regardless of usage.
func TestServerEnforcerLimitIgnoredForNonAllow(t *testing.T) {
	asker := &recordingAsker{}
	e := capability.ServerEnforcer{Asker: asker}
	g := capability.GrantView{ID: "g1", LimitCount: 3, LimitWindowSeconds: 3600}
	d := capability.Decision{Effect: capability.EffectDeny, GrantID: "g1", Reason: "denied elsewhere"}

	err := e.Enforce(context.Background(), capability.Request{}, d, g, 999)
	if !errors.Is(err, capability.ErrDenied) {
		t.Fatalf("Enforce = %v, want it to wrap ErrDenied for a deny decision", err)
	}
	if asker.called {
		t.Error("a deny decision must not consult the asker via the limit check")
	}
}
