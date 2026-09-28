// Package serverask implements capability.Asker for the in-process
// ServerEnforcer, backed by askgate.Store. It lives outside askgate, which is
// deliberately generic and must not depend on the policy package, and
// outside capability, which must not depend on a concrete hold-and-answer
// mechanism — an asker necessarily imports both, so it gets its own package.
package serverask

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/askgate"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/sanitize"
)

// askHoldTimeout is how long Ask waits for a human decision before failing
// closed. It is bounded by MCP and browser client request timeouts, not by
// Claude Code's own hook timeout — unlike the hook's permissionHoldTimeout,
// which is sized against that instead. The two happen to match today; that
// is coincidence, not a shared constant, and either may change independently.
const askHoldTimeout = 25 * time.Second

// maxDisplayRunes bounds Value and Context, mirroring the hook bridge's
// maxPatternRunes (permission.go): both sit next to an Allow button, and both
// are authored by the party the decision judges.
const maxDisplayRunes = 400

// Pending is one server-point decision waiting for a human, as shown in the UI.
type Pending struct {
	Capability string
	// Value and Context come from the caller being judged (capability.Request,
	// mostSpecificContext) and are rendered next to an Allow button, so both
	// are sanitized and capped before storage; ValueElided/ContextElided carry
	// the cut count separately so a truncated value cannot forge its own "…".
	Value         string
	ValueElided   int
	Context       string // the most specific context of the request, rendered
	ContextElided int
	Reason        string
	RequestedAt   time.Time
}

// Asker answers capability.EffectAsk decisions raised by ServerEnforcer by
// holding the call open until a human resolves it through Resolve.
type Asker struct {
	store *askgate.Store[Pending]
}

var _ capability.Asker = (*Asker)(nil)

// New returns an Asker whose Ask waits up to askHoldTimeout for a decision.
// onChange, which may be nil, is called whenever the pending set changes.
func New(onChange func()) *Asker {
	return &Asker{store: askgate.New[Pending](askHoldTimeout, onChange)}
}

// Ask registers req and d as a pending decision and blocks for a human answer.
//
// Unlike HookEnforcer, which fails OPEN on no decision because its fallback
// is Claude Code's own terminal prompt, this point has no such fallback —
// ServerEnforcer is the complete backstop — so no decision here means
// refuse, never proceed.
func (a *Asker) Ask(ctx context.Context, req capability.Request, d capability.Decision) (bool, error) {
	id := uuid.New().String()
	value, valueElided := sanitize.ForDisplayCapped(req.Value, maxDisplayRunes)
	askContext, contextElided := sanitize.ForDisplayCapped(mostSpecificContext(req.Contexts), maxDisplayRunes)
	meta := Pending{
		Capability:    req.Capability,
		Value:         value,
		ValueElided:   valueElided,
		Context:       askContext,
		ContextElided: contextElided,
		Reason:        d.Reason,
		RequestedAt:   time.Now(),
	}
	decision, ok := a.store.Ask(ctx, id, meta)
	if !ok {
		return false, nil
	}
	return decision == "allow", nil
}

// mostSpecificContext renders the context a human approving this ask should
// see. req.Contexts' order is documented as not load-bearing, so the ranking
// comes from capability itself rather than from a second copy of it here.
func mostSpecificContext(contexts []capability.Context) string {
	best, ok := capability.MostSpecific(contexts)
	if !ok {
		return ""
	}
	if best.Ref == "" {
		return best.Kind
	}
	return best.Kind + " " + best.Ref
}

// Pending returns a snapshot of every ask currently waiting for a decision.
func (a *Asker) Pending() []askgate.Entry[Pending] {
	return a.store.List()
}

// SetOnChange installs the callback fired whenever the pending set changes.
// The asker is built in the DI container, before the router that owns the
// debounced rescan exists, so the callback arrives afterwards — the same
// split HookEnforcer already uses.
func (a *Asker) SetOnChange(fn func()) {
	a.store.SetOnChange(fn)
}

// ErrInvalidDecision means Resolve was given a decision string that is
// neither "allow" nor "deny".
var ErrInvalidDecision = errors.New("serverask: decision must be \"allow\" or \"deny\"")

// Resolve delivers a decision to the ask named by id and returns what was
// pending under it. The id is a UUID minted per ask and names nothing after
// the hold is released, so a caller recording the decision has to be handed
// the metadata here or it has no way to say what was approved.
func (a *Asker) Resolve(id, decision string) (Pending, error) {
	if decision != "allow" && decision != "deny" {
		return Pending{}, ErrInvalidDecision
	}
	var resolved Pending
	err := a.store.Resolve(id, func(meta Pending) (string, error) {
		resolved = meta
		return decision, nil
	})
	return resolved, err
}
