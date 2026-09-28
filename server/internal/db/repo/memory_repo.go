package repo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/memoryentry"
	"github.com/lx-wnk/kontor/server/internal/db/ent/memoryinjection"
)

// MemoryKinds and MemorySourceKinds list the values CreateEntry accepts for
// a memory entry's Kind and SourceKind. Shared by every transport that
// accepts one from outside (the memory_write MCP tool's InputSchema enum,
// the HTTP API) so the advertised set can never drift from what CreateEntry
// actually enforces — an unrecognised value used to store cleanly and only
// silently derank at read time (score.go's kindWeightUnknown), forever.
var (
	MemoryKinds       = []string{"fact", "preference", "lesson", "entity", "pointer"}
	MemorySourceKinds = []string{"agent", "user", "application", "import"}
)

// ErrInvalidKind and ErrInvalidSourceKind mean CreateEntry was called with a
// Kind or SourceKind outside MemoryKinds / MemorySourceKinds. Named so a
// caller can distinguish this from any other validation failure.
var (
	ErrInvalidKind       = errors.New("memory: kind must be one of " + strings.Join(MemoryKinds, ", "))
	ErrInvalidSourceKind = errors.New("memory: source_kind must be one of " + strings.Join(MemorySourceKinds, ", "))
)

// CreateSpaceInput is the named input for CreateSpace.
type CreateSpaceInput struct {
	Slug  string
	Name  string
	Scope Scope
}

// CreateEntryInput is the named input for CreateEntry.
type CreateEntryInput struct {
	SpaceID    string
	Summary    string
	Content    string
	Kind       string
	SourceKind string
	SourceRef  *string
	Confidence float64
	// ValidFrom overrides the schema default of now when set.
	ValidFrom *time.Time
	UserID    *string
}

// RecordInjectionInput is the named input for RecordInjection.
type RecordInjectionInput struct {
	StageRunID     string
	EntryIDs       []string
	CharBudget     int
	CharsUsed      int
	CandidateCount int
}

// MemoryRepo persists memory spaces (as resource rows), the entries held
// inside them, and the record of what got injected into a spawn.
type MemoryRepo interface {
	// CreateSpace creates or refreshes the resource row that is a memory
	// space's identity. There is no memory_space table: a space IS a
	// resource row of kind ResourceKindMemorySpace.
	CreateSpace(ctx context.Context, in CreateSpaceInput) (*ent.Resource, error)
	GetSpace(ctx context.Context, scope Scope, slug string) (*ent.Resource, error)
	// GetSpaceByID resolves a space by its bare resource id, for a caller
	// that only holds an entry's space_id (e.g. an HTTP handler authorizing a
	// mutation on one entry) and needs the owning space's scope and slug to
	// check a capability grant against.
	GetSpaceByID(ctx context.Context, id string) (*ent.Resource, error)
	ListSpaces(ctx context.Context, scope Scope) ([]*ent.Resource, error)
	CreateEntry(ctx context.Context, in CreateEntryInput) (*ent.MemoryEntry, error)
	GetEntry(ctx context.Context, id string) (*ent.MemoryEntry, error)
	// SupersedeEntry marks oldID as replaced by newID. It writes a pointer on
	// the old row rather than mutating or deleting it, so the chain of what
	// replaced what survives as an audit trail.
	SupersedeEntry(ctx context.Context, oldID, newID string) error
	// ExpireEntry marks id as no longer valid as of at, without deleting it —
	// same reasoning as SupersedeEntry.
	ExpireEntry(ctx context.Context, id string, at time.Time) error
	// ListValid returns the entries of spaceID that are neither expired as of
	// now nor superseded.
	ListValid(ctx context.Context, spaceID string, now time.Time) ([]*ent.MemoryEntry, error)
	RecordInjection(ctx context.Context, in RecordInjectionInput) (*ent.MemoryInjection, error)
	// ListInjectionsByStageRun returns the injection records for one stage
	// run, for surfacing what memory was offered to a spawn.
	ListInjectionsByStageRun(ctx context.Context, stageRunID string) ([]*ent.MemoryInjection, error)
}

type entMemoryRepo struct {
	client      *ent.Client
	writeClient db.WriteClient
	resources   ResourceRepo
}

// NewMemoryRepo returns a MemoryRepo backed by the ent client. Space
// operations are delegated to a ResourceRepo built on the same client, rather
// than reimplementing resource identity handling here. writeClient must be
// db.DBBundle.WriteClient — SupersedeEntry is a read-then-write and needs
// the BEGIN IMMEDIATE pool WithWriteTx requires; see its doc comment.
func NewMemoryRepo(client *ent.Client, writeClient db.WriteClient) MemoryRepo {
	return &entMemoryRepo{client: client, writeClient: writeClient, resources: NewResourceRepo(client)}
}

// CreateSpace creates or refreshes a memory space's resource row.
//
// There is no delete path for a space: memory_entry.space_id is a loose
// reference with no ent edge or foreign key, so deleting the resource row out
// from under existing entries would not fail — it would leave them pointing
// at an id that no longer resolves, silently orphaned rather than deleted, in
// a store whose whole point is durable history. Any future deletion path
// must count referencing entries and refuse while any exist, exactly as
// CreateEntry's mustBeSpace refuses a write that references a space that does
// not exist. That count must include expired and superseded entries, not
// just currently-valid ones: expiry and superseding govern visibility
// (ListValid), not existence — the row is still there, still referencing the
// space, until something actually deletes it.
func (r *entMemoryRepo) CreateSpace(ctx context.Context, in CreateSpaceInput) (*ent.Resource, error) {
	row, err := r.resources.Upsert(ctx, UpsertResourceInput{
		Kind:  ResourceKindMemorySpace,
		Slug:  in.Slug,
		Name:  in.Name,
		Scope: in.Scope,
	})
	if err != nil {
		return nil, fmt.Errorf("memory.CreateSpace: %w", err)
	}
	return row, nil
}

func (r *entMemoryRepo) GetSpace(ctx context.Context, scope Scope, slug string) (*ent.Resource, error) {
	row, err := r.resources.Get(ctx, ResourceKindMemorySpace, scope, slug)
	if err != nil {
		return nil, fmt.Errorf("memory.GetSpace: %w", err)
	}
	return row, nil
}

func (r *entMemoryRepo) GetSpaceByID(ctx context.Context, id string) (*ent.Resource, error) {
	row, err := r.client.Resource.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("memory.GetSpaceByID: %w", err)
	}
	if row.Kind != ResourceKindMemorySpace {
		return nil, fmt.Errorf("memory.GetSpaceByID: %s: not a memory space", id)
	}
	return row, nil
}

func (r *entMemoryRepo) ListSpaces(ctx context.Context, scope Scope) ([]*ent.Resource, error) {
	rows, err := r.resources.ListForScope(ctx, ResourceKindMemorySpace, scope)
	if err != nil {
		return nil, fmt.Errorf("memory.ListSpaces: %w", err)
	}
	return rows, nil
}

// mustBeSpace fails closed on a space_id that is not a live memory_space
// resource row. space_id is a loose string reference (no ent edge, no FK), so
// nothing at the database level stops an unrecognised or wrong-kind id from
// being written; this is the one place that refuses it.
func (r *entMemoryRepo) mustBeSpace(ctx context.Context, spaceID string) error {
	row, err := r.client.Resource.Get(ctx, spaceID)
	if err != nil {
		return fmt.Errorf("space %s: %w", spaceID, err)
	}
	if row.Kind != ResourceKindMemorySpace {
		return fmt.Errorf("space %s: not a memory space", spaceID)
	}
	return nil
}

func (r *entMemoryRepo) CreateEntry(ctx context.Context, in CreateEntryInput) (*ent.MemoryEntry, error) {
	if in.SpaceID == "" {
		return nil, fmt.Errorf("memory.CreateEntry: space_id is required")
	}
	if in.Summary == "" {
		return nil, fmt.Errorf("memory.CreateEntry: summary is required")
	}
	if in.Kind == "" {
		return nil, fmt.Errorf("memory.CreateEntry: kind is required")
	}
	if in.SourceKind == "" {
		return nil, fmt.Errorf("memory.CreateEntry: source_kind is required")
	}
	// Both transports (memory_write's InputSchema enum, the HTTP API) only
	// checked non-empty, so an unrecognised value stored cleanly and then
	// silently deranked forever at read time (score.go's kindComponent
	// default). Reject it here instead — the one place both transports
	// route through.
	if !slices.Contains(MemoryKinds, in.Kind) {
		return nil, fmt.Errorf("memory.CreateEntry: %w: %q", ErrInvalidKind, in.Kind)
	}
	if !slices.Contains(MemorySourceKinds, in.SourceKind) {
		return nil, fmt.Errorf("memory.CreateEntry: %w: %q", ErrInvalidSourceKind, in.SourceKind)
	}
	if err := r.mustBeSpace(ctx, in.SpaceID); err != nil {
		return nil, fmt.Errorf("memory.CreateEntry: %w", err)
	}

	create := r.client.MemoryEntry.Create().
		SetID(uuid.New().String()).
		SetSpaceID(in.SpaceID).
		SetSummary(in.Summary).
		SetContent(in.Content).
		SetKind(in.Kind).
		SetSourceKind(in.SourceKind).
		SetNillableSourceRef(in.SourceRef).
		SetConfidence(in.Confidence).
		SetNillableUserID(in.UserID)
	if in.ValidFrom != nil {
		create = create.SetValidFrom(*in.ValidFrom)
	}
	row, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("memory.CreateEntry: %w", err)
	}
	return row, nil
}

func (r *entMemoryRepo) GetEntry(ctx context.Context, id string) (*ent.MemoryEntry, error) {
	row, err := r.client.MemoryEntry.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("memory.GetEntry: %w", err)
	}
	return row, nil
}

func (r *entMemoryRepo) SupersedeEntry(ctx context.Context, oldID, newID string) error {
	if oldID == "" || newID == "" {
		return fmt.Errorf("memory.SupersedeEntry: old and new entry ids are required")
	}
	if oldID == newID {
		return fmt.Errorf("memory.SupersedeEntry: an entry cannot supersede itself")
	}
	// WithWriteTx (BEGIN IMMEDIATE), not WithTx: this is a read-then-write —
	// verify newID exists, then write a pointer at it — and a deferred
	// transaction can race a concurrent delete of newID into
	// SQLITE_BUSY_SNAPSHOT rather than failing the way a dangling
	// superseded_by must never be observable requires.
	err := WithWriteTx(ctx, r.writeClient, func(tx *ent.Tx) error {
		// Verify the replacement exists before writing a pointer at it, in the
		// same transaction as the write: a dangling superseded_by must never
		// be observable, not even under a concurrent delete of newID.
		if _, err := tx.MemoryEntry.Get(ctx, newID); err != nil {
			return fmt.Errorf("replacement entry: %w", err)
		}
		return tx.MemoryEntry.UpdateOneID(oldID).SetSupersededBy(newID).Exec(ctx)
	})
	if err != nil {
		return fmt.Errorf("memory.SupersedeEntry: %w", err)
	}
	return nil
}

func (r *entMemoryRepo) ExpireEntry(ctx context.Context, id string, at time.Time) error {
	if err := r.client.MemoryEntry.UpdateOneID(id).SetValidUntil(at).Exec(ctx); err != nil {
		return fmt.Errorf("memory.ExpireEntry: %w", err)
	}
	return nil
}

func (r *entMemoryRepo) ListValid(ctx context.Context, spaceID string, now time.Time) ([]*ent.MemoryEntry, error) {
	rows, err := r.client.MemoryEntry.Query().
		Where(
			memoryentry.SpaceIDEQ(spaceID),
			memoryentry.SupersededByIsNil(),
			memoryentry.Or(
				memoryentry.ValidUntilIsNil(),
				memoryentry.ValidUntilGT(now),
			),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("memory.ListValid: %w", err)
	}
	return rows, nil
}

// mustBeStageRun fails closed on a stage_run_id that does not reference a
// real stage_run row. Same reasoning as mustBeSpace: stage_run_id is a loose
// string reference (no ent edge, no FK), so nothing at the database level
// stops RecordInjection from writing an audit-trail row tied to a stage run
// that never existed.
func (r *entMemoryRepo) mustBeStageRun(ctx context.Context, stageRunID string) error {
	if _, err := r.client.StageRun.Get(ctx, stageRunID); err != nil {
		return fmt.Errorf("stage run %s: %w", stageRunID, err)
	}
	return nil
}

// mustBeEntries fails closed if any of entryIDs does not reference a real
// memory_entry row — entry_ids is stored as a loose JSON array with no ent
// edge or FK, same gap mustBeStageRun closes for stage_run_id.
func (r *entMemoryRepo) mustBeEntries(ctx context.Context, entryIDs []string) error {
	for _, id := range entryIDs {
		if _, err := r.client.MemoryEntry.Get(ctx, id); err != nil {
			return fmt.Errorf("entry %s: %w", id, err)
		}
	}
	return nil
}

func (r *entMemoryRepo) RecordInjection(ctx context.Context, in RecordInjectionInput) (*ent.MemoryInjection, error) {
	if in.StageRunID == "" {
		return nil, fmt.Errorf("memory.RecordInjection: stage_run_id is required")
	}
	if err := r.mustBeStageRun(ctx, in.StageRunID); err != nil {
		return nil, fmt.Errorf("memory.RecordInjection: %w", err)
	}
	entryIDs := in.EntryIDs
	if entryIDs == nil {
		entryIDs = []string{}
	}
	if err := r.mustBeEntries(ctx, entryIDs); err != nil {
		return nil, fmt.Errorf("memory.RecordInjection: %w", err)
	}
	row, err := r.client.MemoryInjection.Create().
		SetID(uuid.New().String()).
		SetStageRunID(in.StageRunID).
		SetEntryIds(entryIDs).
		SetCharBudget(in.CharBudget).
		SetCharsUsed(in.CharsUsed).
		SetCandidateCount(in.CandidateCount).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("memory.RecordInjection: %w", err)
	}
	return row, nil
}

func (r *entMemoryRepo) ListInjectionsByStageRun(ctx context.Context, stageRunID string) ([]*ent.MemoryInjection, error) {
	rows, err := r.client.MemoryInjection.Query().
		Where(memoryinjection.StageRunIDEQ(stageRunID)).
		Order(ent.Asc(memoryinjection.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("memory.ListInjectionsByStageRun: %w", err)
	}
	return rows, nil
}
