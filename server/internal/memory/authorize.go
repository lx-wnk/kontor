package memory

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// ScopeKinds lists the scope kind values a caller may name. Shared by every
// transport that accepts a scope from outside (MCP tool args, HTTP query
// params) so the accepted set cannot drift between them.
var ScopeKinds = []string{string(repo.ScopeGlobal), string(repo.ScopeProject), string(repo.ScopeApplication)}

// ParseScope builds a repo.Scope from a caller-supplied kind and ref,
// defaulting an empty kind to global. An unrecognised kind, or a missing ref
// for a non-global scope, fails closed rather than silently resolving to some
// other scope's spaces.
func ParseScope(kind, ref string) (repo.Scope, error) {
	if kind == "" {
		kind = string(repo.ScopeGlobal)
	}
	switch repo.ScopeKind(kind) {
	case repo.ScopeGlobal:
		return repo.GlobalScope(), nil
	case repo.ScopeProject:
		if ref == "" {
			return repo.Scope{}, fmt.Errorf(`scopeRef is required when scope is "project"`)
		}
		return repo.ProjectScope(ref), nil
	case repo.ScopeApplication:
		if ref == "" {
			return repo.Scope{}, fmt.Errorf(`scopeRef is required when scope is "application"`)
		}
		return repo.ApplicationScope(ref), nil
	default:
		return repo.Scope{}, fmt.Errorf("scope must be one of %v", ScopeKinds)
	}
}

// Contexts builds the capability-context chain for a request scoped to
// scope: the scope's own context plus global, the level every grant chain
// falls back to. This lets a project- or application-scoped grant win on
// specificity while a global grant still backs up a scope that has none of
// its own — the same union visibleSpaceScopes (retrieve.go) already applies
// to which spaces are visible, mirrored here for which grants apply.
//
// extra carries contexts the memory scope cannot express because they name
// the caller rather than the data — today that is the routine a task was
// materialized from (RoutineContext below). They are prepended because
// capability.Decide resolves by specificity and a caller-level context is
// narrower than the scope it reads in; ordering within the slice is not what
// Decide ranks on (contextRank is), so this is for readability, not effect.
func Contexts(scope repo.Scope, extra ...capability.Context) []capability.Context {
	scope = scope.Normalize()
	out := make([]capability.Context, 0, len(extra)+2)
	out = append(out, extra...)
	if !scope.IsGlobal() {
		out = append(out, capability.Context{Kind: string(scope.Kind), Ref: scope.Ref})
	}
	return append(out, capability.Context{Kind: string(repo.ScopeGlobal)})
}

// RoutineContext is the context chain entry for work a routine started, or
// nothing when routineID is empty (a task a human created). A routine context
// with an empty ref would match every grant stored with an empty ContextRef,
// so the empty case must yield no context at all rather than a blank one.
func RoutineContext(routineID string) []capability.Context {
	if routineID == "" {
		return nil
	}
	return []capability.Context{{Kind: repo.GrantContextRoutine, Ref: routineID}}
}

// Gate resolves and enforces capability.Decide requests for one caller. This
// is the single gate shared by every transport that reaches a memory action
// in-process (MCP tools, the HTTP API, the pipeline's automatic memory push)
// — the enforcement point capability.EnforcerServer names.
//
// A nil Asker means this caller must never block for a human: an ask
// decision denies, per ServerEnforcer's own fail-closed rule. This makes
// "must never ask" a property of how a Gate is constructed rather than a
// rule every call site has to remember to honour.
type Gate struct {
	Capabilities repo.CapabilityRepo
	Grants       repo.GrantRepo
	GrantUsage   repo.GrantUsageRepo
	Asker        capability.Asker
}

// Authorize resolves and enforces a capability.Decide request for capName
// against value in scope's context chain. A capability catalogue lookup miss
// resolves to a zero-value CapabilityView (empty Class), which
// capability.Decide's defaultEffect sends to deny — the fail-closed
// behaviour SeedCapabilities exists to make the normal case.
func (g Gate) Authorize(ctx context.Context, capName, value string, scope repo.Scope, extra ...capability.Context) error {
	var capView capability.CapabilityView
	if row, err := g.Capabilities.Get(ctx, capName); err == nil {
		capView = capability.CapabilityView{Name: row.Name, Class: row.Class, EnforceableBy: row.EnforceableBy}
	}

	grantRows, err := g.Grants.ListForCapability(ctx, capName)
	if err != nil {
		return fmt.Errorf("list grants for %s: %w", capName, err)
	}
	grantViews := repo.GrantViewsFromRows(grantRows)

	req := capability.Request{Capability: capName, Value: value, Contexts: Contexts(scope, extra...)}
	decision := capability.Decide(req, grantViews, capView)

	grantView, usedInWindow, err := rateLimitUsage(ctx, g.GrantUsage, decision, grantViews)
	if err != nil {
		return err
	}
	enforceErr := capability.ServerEnforcer{Asker: g.Asker}.Enforce(ctx, req, decision, grantView, usedInWindow)
	if enforceErr == nil && decision.Effect == capability.EffectAllow && !capability.WithinLimit(grantView, usedInWindow) {
		// Reaching here means Enforce downgraded this allow to ask because
		// the grant's limit was exhausted (same condition it applies
		// internally) and a human then approved it. The limit already did
		// its job of bounding unattended use; a consciously approved use is
		// recorded unconditionally so a second exhausted call still asks
		// again rather than reading as "one below the limit" forever. A
		// write failure here must not undo the human's already-given
		// permission, so it is logged, not returned.
		if recErr := g.GrantUsage.RecordUsage(ctx, grantView.ID); recErr != nil {
			slog.Error("record human-approved rate-limited usage", "grant_id", grantView.ID, "capability", capName, "error", recErr)
		}
	}
	return enforceErr
}

// rateLimitUsage recovers the grant that won decision (by Decision.GrantID,
// trivially findable in grantViews since Authorize just built it from the
// same rows Decide ranked) and reports its rate-limit usage for
// ServerEnforcer.Enforce. A grant with no rate limit configured (LimitCount
// 0) needs no usage lookup at all — this is the "callers with nothing to
// rate-limit" case Enforce's own doc comment names, so it returns
// GrantView{}, 0 unchanged, same as before this existed.
//
// A limited grant's usage is recorded, not merely read:
// GrantUsageRepo.RecordIfWithinLimit atomically checks the window and
// inserts one use in the same write transaction, so two concurrent callers
// against the same grant cannot both observe "one below the limit" and both
// proceed — a plain count-then-compare here would reopen exactly the race
// RecordIfWithinLimit exists to close. Enforce still re-checks WithinLimit
// itself (only EffectAllow is limit-checked, per its own doc comment), so
// the returned usedInWindow is translated from RecordIfWithinLimit's
// permitted bool rather than the raw count: 0 when permitted (always under
// any positive limit), the limit itself when not (guaranteed to fail
// Enforce's own check identically, with a real "grant %s allows %d..."
// reason instead of a fabricated one).
func rateLimitUsage(ctx context.Context, grantUsage repo.GrantUsageRepo, decision capability.Decision, grantViews []capability.GrantView) (capability.GrantView, int, error) {
	if decision.Effect != capability.EffectAllow || decision.GrantID == "" {
		return capability.GrantView{}, 0, nil
	}
	var winner capability.GrantView
	found := false
	for _, g := range grantViews {
		if g.ID == decision.GrantID {
			winner = g
			found = true
			break
		}
	}
	if !found || winner.LimitCount == 0 {
		return capability.GrantView{}, 0, nil
	}

	window := time.Duration(winner.LimitWindowSeconds) * time.Second
	permitted, err := grantUsage.RecordIfWithinLimit(ctx, winner.ID, winner.LimitCount, window)
	if err != nil {
		return capability.GrantView{}, 0, fmt.Errorf("rate-limit check for grant %s: %w", winner.ID, err)
	}
	if permitted {
		return winner, 0, nil
	}
	return winner, winner.LimitCount, nil
}
