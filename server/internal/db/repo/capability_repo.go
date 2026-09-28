package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/capability"
)

// Capability classes. Free strings rather than a Go enum so adding a class is
// not a schema migration.
const (
	CapClassTool     = "tool"
	CapClassReach    = "reach"
	CapClassResource = "resource"
	CapClassSpend    = "spend"
)

// Memory capability names. Unlike Claude Code tool names (seeded from
// permissions.GrantableToolNames), these gate resource access with no
// on-disk tool to enumerate, so they are named here directly and shared by
// the seeder (capability_seed.go) and the memory MCP tools — one definition
// rather than the same two literals copied into both places.
const (
	CapabilityMemoryRead  = "memory.read"
	CapabilityMemoryWrite = "memory.write"
)

// UpsertCapabilityInput is the named input for Upsert. Named rather than
// positional because the call has more than four parameters, which is where
// this codebase's convention switches.
type UpsertCapabilityInput struct {
	Name            string
	Class           string
	EnforceableBy   []string
	RequiresPattern bool
	Reversible      bool
	Description     string
}

// CapabilityRepo persists the capability catalogue: named permissions coarser
// than a tool name, so an action like sending mail can be expressed at all.
type CapabilityRepo interface {
	Upsert(ctx context.Context, in UpsertCapabilityInput) (*ent.Capability, error)
	Get(ctx context.Context, name string) (*ent.Capability, error)
	List(ctx context.Context) ([]*ent.Capability, error)
}

type entCapabilityRepo struct {
	client *ent.Client
}

// NewCapabilityRepo returns a CapabilityRepo backed by the ent client.
func NewCapabilityRepo(client *ent.Client) CapabilityRepo {
	return &entCapabilityRepo{client: client}
}

func (r *entCapabilityRepo) Upsert(ctx context.Context, in UpsertCapabilityInput) (*ent.Capability, error) {
	err := r.client.Capability.Create().
		SetID(uuid.New().String()).
		SetName(in.Name).
		SetClass(in.Class).
		SetEnforceableBy(in.EnforceableBy).
		SetRequiresPattern(in.RequiresPattern).
		SetReversible(in.Reversible).
		SetDescription(in.Description).
		OnConflictColumns(capability.FieldName).
		UpdateClass().
		UpdateEnforceableBy().
		UpdateRequiresPattern().
		UpdateReversible().
		UpdateDescription().
		UpdateUpdatedAt().
		Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("capability.Upsert: %w", err)
	}
	return r.Get(ctx, in.Name)
}

func (r *entCapabilityRepo) Get(ctx context.Context, name string) (*ent.Capability, error) {
	row, err := r.client.Capability.Query().
		Where(capability.NameEQ(name)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("capability.Get: %w", err)
	}
	return row, nil
}

func (r *entCapabilityRepo) List(ctx context.Context) ([]*ent.Capability, error) {
	rows, err := r.client.Capability.Query().
		Order(ent.Asc(capability.FieldName)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("capability.List: %w", err)
	}
	return rows, nil
}
