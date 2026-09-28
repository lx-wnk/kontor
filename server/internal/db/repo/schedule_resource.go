package repo

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// ReconcileScheduleResources gives every task_schedule row a registry identity.
// Mirrors ReconcilePluginResources line for line, including its sweep over every
// row rather than only the unlinked ones: the registry row carries the
// schedule's name, and skipping linked rows left a renamed routine labelled with
// whatever the first boot saw. The return value counts newly linked schedules.
func ReconcileScheduleResources(ctx context.Context, resources ResourceRepo, client *ent.Client) (int, error) {
	rows, err := client.TaskSchedule.Query().All(ctx)
	if err != nil {
		return 0, fmt.Errorf("reconcile schedules: query: %w", err)
	}

	linked, skipped := 0, 0
	for _, s := range rows {
		resID, err := UpsertScheduleResource(ctx, resources, client, s)
		if err != nil {
			skipped++
			slog.Warn("reconcile schedule: skipped", "schedule_id", s.ID, "err", err)
			continue
		}
		if s.ResourceID != resID {
			linked++
		}
	}
	if skipped > 0 {
		slog.Warn("reconcile schedules: some schedules were not linked", "linked", linked, "skipped", skipped)
	}
	return linked, nil
}

// UpsertScheduleResource creates or refreshes a resource row for the given
// schedule and backlinks the schedule with the resource ID. Both the reconciler
// and Create share this path.
func UpsertScheduleResource(ctx context.Context, resources ResourceRepo, client *ent.Client, s *ent.TaskSchedule) (string, error) {
	state := ResourceStateDisabled
	if s.Enabled {
		state = ResourceStateEnabled
	}

	res, err := resources.Upsert(ctx, UpsertResourceInput{
		Kind:      ResourceKindRoutine,
		Slug:      s.ID,
		Name:      s.Name,
		Scope:     GlobalScope(),
		State:     state,
		Origin:    ResourceOriginLocal,
		OriginRef: s.ID,
	})
	if err != nil {
		return "", fmt.Errorf("upsert schedule resource: %w", err)
	}

	if err := client.TaskSchedule.UpdateOneID(s.ID).SetResourceID(res.ID).Exec(ctx); err != nil {
		return "", fmt.Errorf("backlink schedule resource: %w", err)
	}
	return res.ID, nil
}

// OrphanScheduleResource marks the resource row for a deleted schedule as
// orphaned. The resource row persists so existing grants still resolve.
func OrphanScheduleResource(ctx context.Context, resources ResourceRepo, resourceID string) error {
	if _, err := resources.SetState(ctx, resourceID, ResourceStateOrphaned); err != nil {
		return fmt.Errorf("orphan schedule resource: %w", err)
	}
	return nil
}
