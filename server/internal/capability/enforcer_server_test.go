package capability_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/capability"
)

type recordingAsker struct {
	called       bool
	answer       bool
	err          error
	lastRequest  capability.Request
	lastDecision capability.Decision
}

func (a *recordingAsker) Ask(_ context.Context, req capability.Request, d capability.Decision) (bool, error) {
	a.called = true
	a.lastRequest = req
	a.lastDecision = d
	return a.answer, a.err
}

func TestServerEnforcerAllowPasses(t *testing.T) {
	asker := &recordingAsker{}
	e := capability.ServerEnforcer{Asker: asker}
	if err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{Effect: capability.EffectAllow}, capability.GrantView{}, 0); err != nil {
		t.Fatalf("allow must pass: %v", err)
	}
	if asker.called {
		t.Error("an allow must not consult the asker")
	}
}

func TestServerEnforcerDenyReturnsSentinel(t *testing.T) {
	e := capability.ServerEnforcer{Asker: &recordingAsker{}}
	err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{
		Effect: capability.EffectDeny,
		Reason: "denied by a task-scoped grant",
	}, capability.GrantView{}, 0)
	if !errors.Is(err, capability.ErrDenied) {
		t.Fatalf("Enforce = %v, want it to wrap ErrDenied", err)
	}
	if !strings.Contains(err.Error(), "denied by a task-scoped grant") {
		t.Errorf("error text lost the reason: %v", err)
	}
}

func TestServerEnforcerAskConsultsAndHonoursTheAnswer(t *testing.T) {
	granted := &recordingAsker{answer: true}
	e := capability.ServerEnforcer{Asker: granted}
	if err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{Effect: capability.EffectAsk}, capability.GrantView{}, 0); err != nil {
		t.Fatalf("a granted ask must pass: %v", err)
	}
	if !granted.called {
		t.Error("an ask must consult the asker")
	}

	refused := &recordingAsker{answer: false}
	e = capability.ServerEnforcer{Asker: refused}
	if err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{Effect: capability.EffectAsk}, capability.GrantView{}, 0); !errors.Is(err, capability.ErrDenied) {
		t.Fatalf("a refused ask must deny, got %v", err)
	}
}

// TestServerEnforcerAskPassesRequestToAsker proves the asker receives the
// capability name and value being asked about, not merely the decision —
// the whole point of threading Request through Enforce.
func TestServerEnforcerAskPassesRequestToAsker(t *testing.T) {
	asker := &recordingAsker{answer: true}
	e := capability.ServerEnforcer{Asker: asker}
	req := capability.Request{Capability: "memory.write", Value: "space/foo", Contexts: []capability.Context{{Kind: "global"}}}

	if err := e.Enforce(context.Background(), req, capability.Decision{Effect: capability.EffectAsk}, capability.GrantView{}, 0); err != nil {
		t.Fatalf("a granted ask must pass: %v", err)
	}
	if asker.lastRequest.Capability != "memory.write" {
		t.Errorf("asker.lastRequest.Capability = %q, want %q", asker.lastRequest.Capability, "memory.write")
	}
	if asker.lastRequest.Value != "space/foo" {
		t.Errorf("asker.lastRequest.Value = %q, want %q", asker.lastRequest.Value, "space/foo")
	}
}

// TestServerEnforcerWithoutAskerFailsClosed proves a misconfigured enforcer
// (no Asker wired) refuses an ask-effect decision rather than silently
// letting it through. A missing asker is a wiring bug, and the worst
// possible failure mode for this component is one that allows everything.
func TestServerEnforcerWithoutAskerFailsClosed(t *testing.T) {
	e := capability.ServerEnforcer{}
	err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{Effect: capability.EffectAsk}, capability.GrantView{}, 0)
	if !errors.Is(err, capability.ErrAskRequired) {
		t.Fatalf("Enforce = %v, want it to wrap ErrAskRequired", err)
	}
}

func TestServerEnforcerAskWrapsAskerError(t *testing.T) {
	sentinel := errors.New("asker transport failed")
	e := capability.ServerEnforcer{Asker: &recordingAsker{err: sentinel}}
	err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{Effect: capability.EffectAsk}, capability.GrantView{}, 0)
	if err == nil {
		t.Fatal("Enforce = nil, want a non-nil error when the asker itself fails")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("Enforce = %v, want it to wrap the asker's error", err)
	}
}

// TestServerEnforcerIgnoresEnforceable pins that this enforcement point does
// not filter on Decision.Enforceable, unlike SpawnEnforcer. It is the
// complete backstop and must judge every decision handed to it.
func TestServerEnforcerIgnoresEnforceable(t *testing.T) {
	e := capability.ServerEnforcer{Asker: &recordingAsker{}}
	err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{
		Effect:      capability.EffectDeny,
		Enforceable: []string{capability.EnforcerHook},
	}, capability.GrantView{}, 0)
	if !errors.Is(err, capability.ErrDenied) {
		t.Fatalf("Enforce = %v, want ErrDenied even though EnforcerServer is absent from Enforceable", err)
	}
}

func TestServerEnforcerUnknownEffectDenies(t *testing.T) {
	e := capability.ServerEnforcer{Asker: &recordingAsker{}}
	err := e.Enforce(context.Background(), capability.Request{}, capability.Decision{Effect: capability.Effect("bogus")}, capability.GrantView{}, 0)
	if !errors.Is(err, capability.ErrDenied) {
		t.Fatalf("Enforce = %v, want it to wrap ErrDenied for an unknown effect", err)
	}
}

func TestServerEnforcerPoint(t *testing.T) {
	if got := (capability.ServerEnforcer{}).Point(); got != capability.EnforcerServer {
		t.Errorf("Point() = %q, want %q", got, capability.EnforcerServer)
	}
}
