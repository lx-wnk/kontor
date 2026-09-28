package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/grant"
)

// Grant modes. Free strings rather than a Go enum, matching the capability
// class and resource kind conventions.
const (
	GrantModeAllow = "allow"
	GrantModeDeny  = "deny"
	GrantModeAsk   = "ask"
)

// Grant context kinds a grant can bind to, ranked by specificity per the
// resolution rule (agent_session is most specific, global least).
const (
	GrantContextAgentSession = "agent_session"
	GrantContextTask         = "task"
	GrantContextRoutine      = "routine"
	GrantContextApplication  = "application"
	GrantContextProject      = "project"
	GrantContextGlobal       = "global"
)

// GrantContext is where a grant applies: a context kind plus the instance it
// names. Ref is "" for GrantContextGlobal — the sentinel, not NULL, matching
// repo.Scope's convention.
type GrantContext struct {
	Kind string
	Ref  string
}

// GrantContextFor builds a GrantContext from a kind and ref.
func GrantContextFor(kind, ref string) GrantContext {
	return GrantContext{Kind: kind, Ref: ref}
}

// CreateGrantInput is the named input for Create. Named rather than
// positional because the call has more than four parameters, which is where
// this codebase's convention switches.
type CreateGrantInput struct {
	CapabilityName     string
	Context            GrantContext
	Pattern            string
	Mode               string
	LimitCount         int
	LimitWindowSeconds int
	ExpiresAt          *time.Time
	GrantedBy          string
	Reason             string
}

// GrantRepo persists grants: a capability bound to a context, carrying an
// expiry, a rate limit, and a mode a narrower context can use to overrule a
// broader allow.
type GrantRepo interface {
	Create(ctx context.Context, in CreateGrantInput) (*ent.Grant, error)
	ListForCapability(ctx context.Context, capabilityName string) ([]*ent.Grant, error)
	// ListForCapabilities answers for many capabilities in one query. A caller
	// deciding what to show — a tool list, a settings page — must not issue one
	// authorization per row: Gate.Authorize records usage, so asking it N
	// questions spends N of the budget that bounds real calls.
	ListForCapabilities(ctx context.Context, capabilityNames []string) ([]*ent.Grant, error)
	// List returns every grant row, newest first, across all capabilities.
	List(ctx context.Context) ([]*ent.Grant, error)
	// Revoke tombstones a grant: revoked_at and revoked_by are set, the row
	// stays — history is never lost to a DELETE. revokedBy is required for
	// the same reason granted_by is: a revocation is a security decision,
	// and "who revoked this" must stay answerable.
	Revoke(ctx context.Context, id, revokedBy string) error
}

func GrantViewsFromRows(rows []*ent.Grant) []capability.GrantView {
	views := make([]capability.GrantView, len(rows))
	for i, gr := range rows {
		views[i] = capability.GrantView{
			ID:                 gr.ID,
			Capability:         gr.CapabilityName,
			ContextKind:        gr.ContextKind,
			ContextRef:         gr.ContextRef,
			Pattern:            gr.Pattern,
			Mode:               gr.Mode,
			LimitCount:         gr.LimitCount,
			LimitWindowSeconds: gr.LimitWindowSeconds,
			ExpiresAt:          gr.ExpiresAt,
			RevokedAt:          gr.RevokedAt,
		}
	}
	return views
}

type entGrantRepo struct {
	client *ent.Client
}

// NewGrantRepo returns a GrantRepo backed by the ent client.
func NewGrantRepo(client *ent.Client) GrantRepo {
	return &entGrantRepo{client: client}
}

func (r *entGrantRepo) Create(ctx context.Context, in CreateGrantInput) (*ent.Grant, error) {
	if in.GrantedBy == "" {
		return nil, fmt.Errorf("grant.Create: granted_by is required")
	}
	if _, err := capability.ParsePattern(in.Pattern); err != nil {
		return nil, fmt.Errorf("grant.Create: invalid pattern: %w", err)
	}
	if !capability.IsValidMode(in.Mode) {
		return nil, fmt.Errorf("grant.Create: invalid mode %q (valid: %s)", in.Mode, strings.Join(capability.Modes(), ", "))
	}
	if !capability.IsValidContextKind(in.Context.Kind) {
		return nil, fmt.Errorf("grant.Create: invalid context kind %q (valid: %s)", in.Context.Kind, strings.Join(capability.ContextKinds(), ", "))
	}
	if in.Context.Kind == GrantContextGlobal && in.Context.Ref != "" {
		return nil, fmt.Errorf("grant.Create: context ref must be empty for the global context, got %q", in.Context.Ref)
	}
	if in.Context.Kind != GrantContextGlobal && in.Context.Ref == "" {
		return nil, fmt.Errorf("grant.Create: context ref is required for context kind %q", in.Context.Kind)
	}
	row, err := r.client.Grant.Create().
		SetID(uuid.New().String()).
		SetCapabilityName(in.CapabilityName).
		SetContextKind(in.Context.Kind).
		SetContextRef(in.Context.Ref).
		SetPattern(in.Pattern).
		SetMode(in.Mode).
		SetLimitCount(in.LimitCount).
		SetLimitWindowSeconds(in.LimitWindowSeconds).
		SetNillableExpiresAt(in.ExpiresAt).
		SetGrantedBy(in.GrantedBy).
		SetReason(in.Reason).
		SetNodeID(DefaultNodeID).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("grant.Create: %w", err)
	}
	return row, nil
}

func (r *entGrantRepo) ListForCapability(ctx context.Context, capabilityName string) ([]*ent.Grant, error) {
	rows, err := r.client.Grant.Query().
		Where(grant.CapabilityNameEQ(capabilityName)).
		Order(ent.Asc(grant.FieldGrantedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("grant.ListForCapability: %w", err)
	}
	return rows, nil
}

func (r *entGrantRepo) ListForCapabilities(ctx context.Context, capabilityNames []string) ([]*ent.Grant, error) {
	if len(capabilityNames) == 0 {
		return nil, nil
	}
	rows, err := r.client.Grant.Query().
		Where(grant.CapabilityNameIn(capabilityNames...)).
		Order(ent.Asc(grant.FieldGrantedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("grant.ListForCapabilities: %w", err)
	}
	return rows, nil
}

func (r *entGrantRepo) List(ctx context.Context) ([]*ent.Grant, error) {
	rows, err := r.client.Grant.Query().
		Order(ent.Desc(grant.FieldGrantedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("grant.List: %w", err)
	}
	return rows, nil
}

// Revoke refuses a grant that is already revoked, so a second call can never
// overwrite who revoked it. The guard is a single conditional UPDATE (WHERE
// id = ? AND revoked_at IS NULL) rather than a Get-then-UpdateOneID pair: a
// separate read followed by a write is exactly the shape that races under
// WAL (see repo.WithWriteTx), while one predicated UPDATE has no read step
// to go stale — it either matches and commits, or matches nothing.
func (r *entGrantRepo) Revoke(ctx context.Context, id, revokedBy string) error {
	if revokedBy == "" {
		return fmt.Errorf("grant.Revoke: revoked_by is required")
	}
	n, err := r.client.Grant.Update().
		Where(grant.IDEQ(id), grant.RevokedAtIsNil()).
		SetRevokedAt(time.Now()).
		SetRevokedBy(revokedBy).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("grant.Revoke: %w", err)
	}
	if n == 0 {
		if _, err := r.client.Grant.Get(ctx, id); err != nil {
			return fmt.Errorf("grant.Revoke: %w", err)
		}
		return fmt.Errorf("grant.Revoke: grant %s is already revoked", id)
	}
	return nil
}
