package repo

import (
	"context"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/auditevent"
)

// AuditAction is the set of valid security-relevant action strings.
const (
	AuditActionSpawn              = "spawn"
	AuditActionSpawnRejected      = "spawn_rejected"
	AuditActionPermissionGrant    = "permission_grant"
	AuditActionPermissionRevoke   = "permission_revoke"
	AuditActionKeyCreate          = "key_create"
	AuditActionKeyDelete          = "key_delete"
	AuditActionLiveInject         = "live_inject"
	AuditActionLiveInjectRejected = "live_inject_rejected"
	AuditActionCapabilityAllow    = "capability_allow"
	AuditActionCapabilityDeny     = "capability_deny"
	AuditActionPlanApproved       = "plan_approved"
	AuditActionPlanRejected       = "plan_rejected"
	AuditActionSpecApproved       = "spec_approved"
	AuditActionTaskCompleted      = "task_completed"
)

// Audit sources: the channel an approval arrived on.
const (
	AuditSourceUI  = "ui"
	AuditSourceMCP = "mcp"
)

// Audit actors: the roles the task Audit tab renders (see auditActor in api/tasks).
const (
	AuditActorUser  = "user"
	AuditActorAgent = "agent"
)

// AuditOrigin identifies who triggered an auditable action and through which channel.
type AuditOrigin struct {
	Source           string
	Actor            string
	UserID           *string
	APIKeyID         string
	CallerStageRunID string
	ModuleID         string
}

// UIAuditOrigin is a person acting through the dashboard.
func UIAuditOrigin(userID string) AuditOrigin {
	return AuditOrigin{Source: AuditSourceUI, Actor: AuditActorUser, UserID: &userID}
}

// MCPAuditOrigin is an MCP key; one issued to a stage run or module acts as an agent.
func MCPAuditOrigin(keyID, stageRunID, moduleID string) AuditOrigin {
	actor := AuditActorUser
	if stageRunID != "" || moduleID != "" {
		actor = AuditActorAgent
	}
	return AuditOrigin{Source: AuditSourceMCP, Actor: actor, APIKeyID: keyID, CallerStageRunID: stageRunID, ModuleID: moduleID}
}

// Metadata is the origin part of an audit event's metadata; empty identifiers are left out.
func (o AuditOrigin) Metadata() map[string]any {
	meta := map[string]any{"source": o.Source}
	if o.Actor != "" {
		meta["actor"] = o.Actor
	}
	if o.APIKeyID != "" {
		meta["apiKeyId"] = o.APIKeyID
	}
	if o.CallerStageRunID != "" {
		meta["callerStageRunId"] = o.CallerStageRunID
	}
	if o.ModuleID != "" {
		meta["moduleId"] = o.ModuleID
	}
	return meta
}

// AuditEventRepo persists and queries security-relevant audit events.
type AuditEventRepo interface {
	// RecordAudit writes a single audit event. userID may be nil for system actions.
	RecordAudit(ctx context.Context, userID *string, action, target string, metadata map[string]any) error
	// RecordTaskAudit writes a task-scoped audit event. Sets task_id for indexed
	// per-task queries via ListForTask. userID may be nil for system actions.
	RecordTaskAudit(ctx context.Context, taskID string, userID *string, action, target string, metadata map[string]any) error
	// List returns audit events matching the given filters, newest-first.
	List(ctx context.Context, f AuditEventFilters) ([]*ent.AuditEvent, error)
	// ListForTask returns all audit events for the given task, oldest-first.
	ListForTask(ctx context.Context, taskID string) ([]*ent.AuditEvent, error)
	// ListAll returns events newest-first with pagination. Convenience wrapper.
	ListAll(ctx context.Context, limit, offset int) ([]*ent.AuditEvent, error)
}

// AuditEventFilters controls which events are returned by List.
type AuditEventFilters struct {
	UserID *string
	Action *string
	Since  *time.Time
	Limit  int // 0 → default 100
	Offset int
}

type entAuditEventRepo struct{ client *ent.Client }

// NewAuditEventRepo creates an AuditEventRepo backed by the given ent client.
func NewAuditEventRepo(client *ent.Client) AuditEventRepo {
	return &entAuditEventRepo{client: client}
}

func (r *entAuditEventRepo) RecordAudit(ctx context.Context, userID *string, action, target string, metadata map[string]any) error {
	q := r.client.AuditEvent.Create().
		SetID(uuid.New().String()).
		SetAction(action).
		SetTarget(target).
		SetNillableUserID(userID)
	if metadata != nil {
		q = q.SetMetadata(metadata)
	}
	if _, err := q.Save(ctx); err != nil {
		return fmt.Errorf("audit_event.RecordAudit: %w", err)
	}
	return nil
}

func (r *entAuditEventRepo) RecordTaskAudit(ctx context.Context, taskID string, userID *string, action, target string, metadata map[string]any) error {
	q := r.client.AuditEvent.Create().
		SetID(uuid.New().String()).
		SetAction(action).
		SetTarget(target).
		SetTaskID(taskID).
		SetNillableUserID(userID)
	if metadata != nil {
		q = q.SetMetadata(metadata)
	}
	if _, err := q.Save(ctx); err != nil {
		return fmt.Errorf("audit_event.RecordTaskAudit: %w", err)
	}
	return nil
}

func (r *entAuditEventRepo) ListForTask(ctx context.Context, taskID string) ([]*ent.AuditEvent, error) {
	events, err := r.client.AuditEvent.Query().
		Where(auditevent.TaskID(taskID)).
		Order(auditevent.ByTs()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("audit_event.ListForTask: %w", err)
	}
	return events, nil
}

func (r *entAuditEventRepo) ListAll(ctx context.Context, limit, offset int) ([]*ent.AuditEvent, error) {
	return r.List(ctx, AuditEventFilters{Limit: limit, Offset: offset})
}

func (r *entAuditEventRepo) List(ctx context.Context, f AuditEventFilters) ([]*ent.AuditEvent, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	q := r.client.AuditEvent.Query()

	if f.UserID != nil {
		q = q.Where(auditevent.UserID(*f.UserID))
	}
	if f.Action != nil {
		q = q.Where(auditevent.Action(*f.Action))
	}
	if f.Since != nil {
		q = q.Where(auditevent.TsGTE(*f.Since))
	}

	events, err := q.
		Order(auditevent.ByTs(sql.OrderDesc())).
		Limit(limit).
		Offset(f.Offset).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("audit_event.List: %w", err)
	}
	return events, nil
}
