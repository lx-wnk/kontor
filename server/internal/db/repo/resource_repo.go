package repo

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/lx-wnk/agent-dashboard/server/internal/db/ent"
	"github.com/lx-wnk/agent-dashboard/server/internal/db/ent/resource"
	"github.com/lx-wnk/agent-dashboard/server/internal/validation"
)

// Resource kinds. Free strings rather than a Go enum so adding a kind is not a
// schema migration.
const (
	ResourceKindApplication = "application"
	ResourceKindRoutine     = "routine"
	ResourceKindSkill       = "skill"
	ResourceKindMemorySpace = "memory_space"
)

// Resource lifecycle states. Generalises the derivation the plugin table
// documents today (no installed_at = discovered, installed but inactive =
// disabled, active = enabled) into an explicit column.
const (
	ResourceStateDiscovered = "discovered"
	ResourceStateInstalled  = "installed"
	ResourceStateEnabled    = "enabled"
	ResourceStateDisabled   = "disabled"
	ResourceStateOrphaned   = "orphaned"
)

// Resource origins.
const (
	ResourceOriginBuiltin = "builtin"
	ResourceOriginLocal   = "local"
	ResourceOriginRemote  = "remote"
)

// DefaultNodeID is written into every resource until the node registry
// lands. Exported because a projected resource (api/resources reports
// task_schedule rows as routines) has to report the same node.
const DefaultNodeID = "local"

// UpsertResourceInput is the named input for Upsert. Named rather than
// positional because the call has more than four parameters, which is where
// this codebase's convention switches.
type UpsertResourceInput struct {
	Kind      string
	Slug      string
	Name      string
	Scope     Scope
	State     string
	Version   string
	Origin    string
	OriginRef string
}

// ResourceRepo persists the identity row shared by every managed ARMS resource.
type ResourceRepo interface {
	Upsert(ctx context.Context, in UpsertResourceInput) (*ent.Resource, error)
	Get(ctx context.Context, kind string, scope Scope, slug string) (*ent.Resource, error)
	// Resolve returns the row for scope, falling back to the global row when the
	// scope has none. This is the effective-value lookup.
	Resolve(ctx context.Context, kind string, scope Scope, slug string) (*ent.Resource, error)
	// ListMerged returns global rows merged with scope rows, scope winning on a
	// slug collision.
	ListMerged(ctx context.Context, kind string, scope Scope) ([]*ent.Resource, error)
	ListForKind(ctx context.Context, kind string) ([]*ent.Resource, error)
	ListForScope(ctx context.Context, kind string, scope Scope) ([]*ent.Resource, error)
	SetState(ctx context.Context, id, state string) (*ent.Resource, error)
	Delete(ctx context.Context, id string) error
}

type entResourceRepo struct {
	client *ent.Client
}

// NewResourceRepo returns a ResourceRepo backed by the ent client.
func NewResourceRepo(client *ent.Client) ResourceRepo {
	return &entResourceRepo{client: client}
}

// Upsert creates the identity row or refreshes its mutable metadata on
// conflict. It deliberately does not touch state or origin on conflict: those
// are owned by SetState and the original discovery respectively, so a
// re-discovery upsert (which defaults an empty State to "discovered") cannot
// silently reset a resource that has since been enabled or disabled.
func (r *entResourceRepo) Upsert(ctx context.Context, in UpsertResourceInput) (*ent.Resource, error) {
	scope := in.Scope.Normalize()
	if err := scope.Validate(); err != nil {
		return nil, fmt.Errorf("resource.Upsert: %w", err)
	}
	if !validation.IsValidSlug(in.Slug) {
		return nil, fmt.Errorf("resource.Upsert: %s", validation.SlugPatternMessage)
	}
	if in.Kind == "" {
		return nil, fmt.Errorf("resource.Upsert: kind is required")
	}

	state := in.State
	if state == "" {
		state = ResourceStateDiscovered
	}
	origin := in.Origin
	if origin == "" {
		origin = ResourceOriginLocal
	}
	// Every writer routes through here, and a plugin manifest is not required to
	// carry a name yet, so a nameless row would reach the UI as a blank line no
	// one can identify. The slug is always present and identifies the row.
	name := in.Name
	if name == "" {
		name = in.Slug
	}

	err := r.client.Resource.Create().
		SetID(uuid.New().String()).
		SetKind(in.Kind).
		SetSlug(in.Slug).
		SetName(name).
		SetScopeKind(string(scope.Kind)).
		SetScopeRef(scope.Ref).
		SetNodeID(DefaultNodeID).
		SetState(state).
		SetVersion(in.Version).
		SetOrigin(origin).
		SetOriginRef(in.OriginRef).
		OnConflictColumns(
			resource.FieldKind,
			resource.FieldScopeKind,
			resource.FieldScopeRef,
			resource.FieldSlug,
		).
		// On re-discovery, refresh metadata but DO NOT reset state/origin.
		UpdateName().UpdateVersion().UpdateOriginRef().UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource.Upsert: %w", err)
	}
	return r.Get(ctx, in.Kind, scope, in.Slug)
}

func (r *entResourceRepo) Get(ctx context.Context, kind string, scope Scope, slug string) (*ent.Resource, error) {
	s := scope.Normalize()
	row, err := r.client.Resource.Query().
		Where(
			resource.KindEQ(kind),
			resource.ScopeKindEQ(string(s.Kind)),
			resource.ScopeRefEQ(s.Ref),
			resource.SlugEQ(slug),
		).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource.Get: %w", err)
	}
	return row, nil
}

func (r *entResourceRepo) ListForKind(ctx context.Context, kind string) ([]*ent.Resource, error) {
	rows, err := r.client.Resource.Query().
		Where(resource.KindEQ(kind)).
		Order(ent.Asc(resource.FieldSlug)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource.ListForKind: %w", err)
	}
	return rows, nil
}

func (r *entResourceRepo) ListForScope(ctx context.Context, kind string, scope Scope) ([]*ent.Resource, error) {
	s := scope.Normalize()
	rows, err := r.client.Resource.Query().
		Where(
			resource.KindEQ(kind),
			resource.ScopeKindEQ(string(s.Kind)),
			resource.ScopeRefEQ(s.Ref),
		).
		Order(ent.Asc(resource.FieldSlug)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource.ListForScope: %w", err)
	}
	return rows, nil
}

func (r *entResourceRepo) SetState(ctx context.Context, id, state string) (*ent.Resource, error) {
	row, err := r.client.Resource.UpdateOneID(id).SetState(state).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("resource.SetState: %w", err)
	}
	return row, nil
}

func (r *entResourceRepo) Delete(ctx context.Context, id string) error {
	row, err := r.client.Resource.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("resource.Delete: %w", err)
	}
	if row.Origin == ResourceOriginBuiltin {
		return fmt.Errorf("resource.Delete %s: %w", row.Slug, ErrResourceBuiltIn)
	}
	if err := r.client.Resource.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("resource.Delete: %w", err)
	}
	return nil
}

func (r *entResourceRepo) Resolve(ctx context.Context, kind string, scope Scope, slug string) (*ent.Resource, error) {
	s := scope.Normalize()
	if !s.IsGlobal() {
		row, err := r.Get(ctx, kind, s, slug)
		if err == nil {
			return row, nil
		}
		if !ent.IsNotFound(err) {
			return nil, err
		}
	}
	row, err := r.Get(ctx, kind, GlobalScope(), slug)
	if err != nil {
		return nil, fmt.Errorf("resource.Resolve: %w", err)
	}
	return row, nil
}

func (r *entResourceRepo) ListMerged(ctx context.Context, kind string, scope Scope) ([]*ent.Resource, error) {
	globals, err := r.ListForScope(ctx, kind, GlobalScope())
	if err != nil {
		return nil, fmt.Errorf("resource.ListMerged: %w", err)
	}
	s := scope.Normalize()
	if s.IsGlobal() {
		return globals, nil
	}
	scoped, err := r.ListForScope(ctx, kind, s)
	if err != nil {
		return nil, fmt.Errorf("resource.ListMerged: %w", err)
	}

	bySlug := make(map[string]*ent.Resource, len(globals)+len(scoped))
	for _, row := range globals {
		bySlug[row.Slug] = row
	}
	// Scoped rows overwrite globals of the same slug — the merge rule.
	for _, row := range scoped {
		bySlug[row.Slug] = row
	}

	out := make([]*ent.Resource, 0, len(bySlug))
	for _, row := range bySlug {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}
