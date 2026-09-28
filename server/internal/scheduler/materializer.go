package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/permissions"
)

// NewTaskSpec is the resolved template the materializer hands to the injected
// task creator. It mirrors api/tasks.CreateTaskParams but lives here so the
// scheduler never imports the api/tasks package (leaf boundary).
type NewTaskSpec struct {
	Slug            string
	Title           string
	Description     *string
	Cwd             string
	Priority        string
	SilverBullet    bool
	MaxIterations   int
	SourceBranch    *string
	TargetBranch    *string
	TokenBudget     *int
	CostBudgetCents *int
	ProjectID       string
	SpawnerID       string
	RoutineID       string
	UserID          *string
	Metadata        map[string]any
	Applications    []string
	Kind            string
	Stage           string
	Autonomy        *string
}

// TaskCreateFunc materializes a pipeline task and returns its ID. Wired at the
// composition root to api/tasks.Handler.CreateTaskFromInput.
type TaskCreateFunc func(ctx context.Context, spec NewTaskSpec) (taskID string, err error)

// PermissionGranter applies a permission template to a freshly created task.
// Satisfied by repo.PermissionRepo.
type PermissionGranter interface {
	BulkGrantPermissions(ctx context.Context, taskID string, entries []repo.GrantEntry) ([]*ent.TaskPermission, error)
}

// slugLookup checks slug uniqueness. Satisfied by repo.TaskRepo.
type slugLookup interface {
	GetBySlug(ctx context.Context, slug string) (*ent.Task, error)
}

// Materializer turns a TaskSchedule into a concrete pipeline task: it derives a
// unique slug, creates the task via the injected creator, and applies the
// schedule's permission template.
type Materializer struct {
	create TaskCreateFunc
	tasks  slugLookup
	perms  PermissionGranter
}

// NewMaterializer builds a Materializer. perms may be nil (no template grant).
func NewMaterializer(create TaskCreateFunc, tasks slugLookup, perms PermissionGranter) *Materializer {
	return &Materializer{create: create, tasks: tasks, perms: perms}
}

// Materialize creates one task from the schedule, stamped at fireTime. It
// returns the new task ID. The permission template, when set, is applied after
// creation; a template-grant failure is returned (the caller decides whether to
// keep the task).
func (m *Materializer) Materialize(ctx context.Context, s *ent.TaskSchedule, fireTime time.Time) (string, error) {
	slug, err := m.uniqueSlug(ctx, s.SlugPrefix, fireTime)
	if err != nil {
		return "", err
	}
	// job run mode skips the pipeline entirely: the task starts and stays on
	// the "job" stage instead of entering "ready".
	kind, stage := "pipeline", "ready"
	if s.RunMode == repo.RunModeJob {
		kind, stage = "job", "job"
	}
	full := "full"
	spec := NewTaskSpec{
		Slug:            slug,
		Title:           s.Title,
		Description:     s.Description,
		Cwd:             s.Cwd,
		Priority:        s.Priority,
		SilverBullet:    s.SilverBullet,
		MaxIterations:   s.MaxIterations,
		SourceBranch:    s.SourceBranch,
		TargetBranch:    s.TargetBranch,
		TokenBudget:     s.TokenBudget,
		CostBudgetCents: s.CostBudgetCents,
		UserID:          s.UserID,
		Metadata:        s.Metadata,
		// The schedule IS the routine: its id is what a "routine" capability
		// grant is anchored to, so every task it materializes carries it.
		RoutineID:    s.ID,
		Applications: s.Applications,
		Kind:         kind,
		Stage:        stage,
		Autonomy:     &full,
	}
	if s.ProjectID != nil {
		spec.ProjectID = *s.ProjectID
	}
	if s.SpawnerID != nil {
		spec.SpawnerID = *s.SpawnerID
	}

	taskID, err := m.create(ctx, spec)
	if err != nil {
		return "", fmt.Errorf("materialize %q: %w", s.ID, err)
	}

	if s.PermissionTemplate != nil && *s.PermissionTemplate != "" && m.perms != nil {
		if err := m.applyTemplate(ctx, taskID, *s.PermissionTemplate); err != nil {
			return taskID, fmt.Errorf("materialize %q: apply template: %w", s.ID, err)
		}
	}
	return taskID, nil
}

func (m *Materializer) applyTemplate(ctx context.Context, taskID, templateName string) error {
	tools, err := permissions.ResolveTemplate(templateName)
	if err != nil {
		return err
	}
	entries := make([]repo.GrantEntry, len(tools))
	for i, t := range tools {
		entries[i] = repo.GrantEntry{Tool: t}
	}
	_, err = m.perms.BulkGrantPermissions(ctx, taskID, entries)
	return err
}

// slugTimestampLayout produces <prefix>-YYYYMMDD-HHMM. Minute granularity is
// enough: a schedule fires at most once per minute.
const slugTimestampLayout = "20060102-1504"

// uniqueSlug derives <prefix>-<timestamp>, appending -2, -3, … on collision so a
// catch-up fire in the same minute as a manual run-now still gets a fresh slug.
func (m *Materializer) uniqueSlug(ctx context.Context, prefix string, fireTime time.Time) (string, error) {
	base := fmt.Sprintf("%s-%s", prefix, fireTime.Format(slugTimestampLayout))
	candidate := base
	for n := 2; n < 100; n++ {
		if m.tasks == nil {
			return candidate, nil
		}
		if _, err := m.tasks.GetBySlug(ctx, candidate); err != nil {
			// Not found (or lookup error) → slug is free to use.
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	return "", fmt.Errorf("could not derive a unique slug for prefix %q", prefix)
}
