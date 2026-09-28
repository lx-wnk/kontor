package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/permissionrequest"
	"github.com/lx-wnk/kontor/server/internal/db/ent/taskpermission"
)

// Outcome is the set of terminal states a permission_request may resolve to.
// The column is a plain string, so these constants are the only guard against a
// dialect drifting apart: the ACP gate authorizes on OutcomeGranted alone and
// reads every other value as a refusal.
const (
	OutcomeGranted = "granted"
	OutcomeDenied  = "denied"
	OutcomeExpired = "expired"
)

type PermissionRepo interface {
	CreateTaskPermission(ctx context.Context, input CreateTaskPermissionInput) (*ent.TaskPermission, error)
	BulkGrantPermissions(ctx context.Context, taskID string, entries []GrantEntry) ([]*ent.TaskPermission, error)
	ListTaskPermissions(ctx context.Context, taskID string) ([]*ent.TaskPermission, error)
	ListEffectiveTaskPermissions(ctx context.Context, taskID string) ([]*ent.TaskPermission, error)
	InheritPermissionsFromParent(ctx context.Context, taskID, parentTaskID string) ([]*ent.TaskPermission, error)
	DeleteTaskPermission(ctx context.Context, id string) error
	CreatePermissionRequest(ctx context.Context, input CreatePermissionRequestInput) (*ent.PermissionRequest, error)
	GetPermissionRequest(ctx context.Context, id string) (*ent.PermissionRequest, error)
	ListPendingForStageRun(ctx context.Context, stageRunID string) ([]*ent.PermissionRequest, error)
	// ListPendingForStageRuns returns pending (outcome IS NULL) permission
	// requests for all given stage run IDs in a single query. Used by the
	// broadcast enricher to batch-resolve pending permissions for a whole
	// tick's worth of agents instead of one query per agent per tick.
	ListPendingForStageRuns(ctx context.Context, stageRunIDs []string) ([]*ent.PermissionRequest, error)
	ListPendingForTask(ctx context.Context, taskID string, stageRunIDs []string) ([]*ent.PermissionRequest, error)
	ResolvePermissionRequest(ctx context.Context, id, outcome string) error
	CountForStageRun(ctx context.Context, stageRunID string) (int, error)
	CountForStageRunsBulk(ctx context.Context, stageRunIDs []string) (map[string]int, error)
	ExpirePendingForStageRun(ctx context.Context, stageRunID string) (int, error)
}

// GrantEntry describes a single permission to bulk-grant.
type GrantEntry struct {
	Tool           string
	Pattern        *string
	ExpiresAt      *time.Time
	ManualOverride bool
	// DecidedBy identifies the authenticated caller who approved this grant
	// (its identity, not a role placeholder). Left empty for system-derived
	// grants (preset replay, template materialization, parent inheritance)
	// where no one actually decided anything.
	DecidedBy string
}

type CreateTaskPermissionInput struct {
	TaskID         string
	Tool           string
	Pattern        *string
	Granted        bool
	ManualOverride bool
	ExpiresAt      *time.Time
	// DecidedBy identifies who approved this grant. An empty value is treated
	// as "no decision recorded" rather than written as a placeholder, so
	// decided_by and decided_at are only ever set together.
	DecidedBy string
}

type CreatePermissionRequestInput struct {
	StageRunID string
	Tool       string
	Pattern    *string
	Reason     *string
}

type entPermissionRepo struct{ client *ent.Client }

func NewPermissionRepo(client *ent.Client) PermissionRepo {
	return &entPermissionRepo{client: client}
}

func (r *entPermissionRepo) CreateTaskPermission(ctx context.Context, in CreateTaskPermissionInput) (*ent.TaskPermission, error) {
	q := r.client.TaskPermission.Create().
		SetID(uuid.New().String()).
		SetTaskID(in.TaskID).
		SetTool(in.Tool).
		SetGranted(in.Granted).
		SetManualOverride(in.ManualOverride)
	if in.Pattern != nil {
		q = q.SetPattern(*in.Pattern)
	}
	if in.ExpiresAt != nil {
		q = q.SetExpiresAt(*in.ExpiresAt)
	}
	if in.DecidedBy != "" {
		q = q.SetDecidedBy(in.DecidedBy).SetDecidedAt(time.Now())
	}
	p, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.CreateTaskPermission: %w", err)
	}
	return p, nil
}

func (r *entPermissionRepo) ListTaskPermissions(ctx context.Context, taskID string) ([]*ent.TaskPermission, error) {
	perms, err := r.client.TaskPermission.Query().
		Where(taskpermission.TaskID(taskID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.ListTaskPermissions: %w", err)
	}
	return perms, nil
}

func (r *entPermissionRepo) BulkGrantPermissions(ctx context.Context, taskID string, entries []GrantEntry) ([]*ent.TaskPermission, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	results := make([]*ent.TaskPermission, 0, len(entries))
	for _, e := range entries {
		p, err := r.CreateTaskPermission(ctx, CreateTaskPermissionInput{
			TaskID:         taskID,
			Tool:           e.Tool,
			Pattern:        e.Pattern,
			Granted:        true,
			ManualOverride: e.ManualOverride,
			ExpiresAt:      e.ExpiresAt,
			DecidedBy:      e.DecidedBy,
		})
		if err != nil {
			return results, fmt.Errorf("permission.BulkGrantPermissions: %w", err)
		}
		results = append(results, p)
	}
	return results, nil
}

func (r *entPermissionRepo) ListEffectiveTaskPermissions(ctx context.Context, taskID string) ([]*ent.TaskPermission, error) {
	now := time.Now()
	perms, err := r.client.TaskPermission.Query().
		Where(
			taskpermission.TaskID(taskID),
			taskpermission.Granted(true),
			taskpermission.Or(
				taskpermission.ExpiresAtIsNil(),
				taskpermission.ExpiresAtGT(now),
			),
		).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.ListEffectiveTaskPermissions: %w", err)
	}
	return perms, nil
}

func (r *entPermissionRepo) InheritPermissionsFromParent(ctx context.Context, taskID, parentTaskID string) ([]*ent.TaskPermission, error) {
	parentPerms, err := r.ListEffectiveTaskPermissions(ctx, parentTaskID)
	if err != nil {
		return nil, fmt.Errorf("permission.InheritPermissionsFromParent: %w", err)
	}
	entries := make([]GrantEntry, 0, len(parentPerms))
	for _, p := range parentPerms {
		// ManualOverride is deliberately not inherited: a human's allow-list
		// override is consent for the parent's scope only, so the child is
		// re-gated and must obtain its own override approval.
		entries = append(entries, GrantEntry{
			Tool:      p.Tool,
			Pattern:   p.Pattern,
			ExpiresAt: p.ExpiresAt,
		})
	}
	return r.BulkGrantPermissions(ctx, taskID, entries)
}

func (r *entPermissionRepo) DeleteTaskPermission(ctx context.Context, id string) error {
	if err := r.client.TaskPermission.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("permission.DeleteTaskPermission: %w", err)
	}
	return nil
}

func (r *entPermissionRepo) CreatePermissionRequest(ctx context.Context, in CreatePermissionRequestInput) (*ent.PermissionRequest, error) {
	q := r.client.PermissionRequest.Create().
		SetID(uuid.New().String()).
		SetStageRunID(in.StageRunID).
		SetTool(in.Tool)
	if in.Pattern != nil {
		q = q.SetPattern(*in.Pattern)
	}
	if in.Reason != nil {
		q = q.SetReason(*in.Reason)
	}
	req, err := q.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.CreatePermissionRequest: %w", err)
	}
	return req, nil
}

func (r *entPermissionRepo) GetPermissionRequest(ctx context.Context, id string) (*ent.PermissionRequest, error) {
	req, err := r.client.PermissionRequest.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("permission.GetPermissionRequest: %w", err)
	}
	return req, nil
}

func (r *entPermissionRepo) ListPendingForStageRun(ctx context.Context, stageRunID string) ([]*ent.PermissionRequest, error) {
	reqs, err := r.client.PermissionRequest.Query().
		Where(permissionrequest.StageRunID(stageRunID), permissionrequest.OutcomeIsNil()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.ListPendingForStageRun: %w", err)
	}
	return reqs, nil
}

func (r *entPermissionRepo) ResolvePermissionRequest(ctx context.Context, id, outcome string) error {
	n, err := r.client.PermissionRequest.Update().
		Where(permissionrequest.ID(id), permissionrequest.OutcomeIsNil()).
		SetOutcome(outcome).
		SetResolvedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("permission_request.Resolve: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("permission_request.Resolve: already resolved or not found")
	}
	return nil
}

func (r *entPermissionRepo) ListPendingForTask(ctx context.Context, _ string, stageRunIDs []string) ([]*ent.PermissionRequest, error) {
	return r.ListPendingForStageRuns(ctx, stageRunIDs)
}

func (r *entPermissionRepo) ListPendingForStageRuns(ctx context.Context, stageRunIDs []string) ([]*ent.PermissionRequest, error) {
	if len(stageRunIDs) == 0 {
		return nil, nil
	}
	reqs, err := r.client.PermissionRequest.Query().
		Where(permissionrequest.StageRunIDIn(stageRunIDs...), permissionrequest.OutcomeIsNil()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.ListPendingForStageRuns: %w", err)
	}
	return reqs, nil
}

func (r *entPermissionRepo) CountForStageRun(ctx context.Context, stageRunID string) (int, error) {
	n, err := r.client.PermissionRequest.Query().
		Where(permissionrequest.StageRunID(stageRunID), permissionrequest.OutcomeIsNil()).
		Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("permission.CountForStageRun: %w", err)
	}
	return n, nil
}

// CountForStageRunsBulk returns the count of pending (outcome IS NULL) permission
// requests for each stage run ID in a single query, reducing N queries to 1.
func (r *entPermissionRepo) CountForStageRunsBulk(ctx context.Context, stageRunIDs []string) (map[string]int, error) {
	counts := make(map[string]int, len(stageRunIDs))
	if len(stageRunIDs) == 0 {
		return counts, nil
	}
	reqs, err := r.client.PermissionRequest.Query().
		Where(permissionrequest.StageRunIDIn(stageRunIDs...), permissionrequest.OutcomeIsNil()).
		Select(permissionrequest.FieldStageRunID).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("permission.CountForStageRunsBulk: %w", err)
	}
	for _, req := range reqs {
		counts[req.StageRunID]++
	}
	return counts, nil
}

// ExpirePendingForStageRun sets outcome="expired" and resolvedAt=now on all
// pending (outcome IS NULL) permission_requests for the given stage run.
// Returns the number of rows updated; 0 with nil error means none were pending.
func (r *entPermissionRepo) ExpirePendingForStageRun(ctx context.Context, stageRunID string) (int, error) {
	n, err := r.client.PermissionRequest.Update().
		Where(permissionrequest.StageRunID(stageRunID), permissionrequest.OutcomeIsNil()).
		SetOutcome(OutcomeExpired).
		SetResolvedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return 0, fmt.Errorf("permission.ExpirePendingForStageRun: %w", err)
	}
	return n, nil
}
