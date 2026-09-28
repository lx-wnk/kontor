package schema

import (
	"time"

	"entgo.io/ent"
	entsql "entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type Task struct{ ent.Schema }

func (Task) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("id").Immutable(),
		field.String("slug").Unique(),
		field.String("title").MaxLen(200),
		field.String("description").Optional().Nillable(),
		field.String("cwd").MaxLen(4096),
		field.String("worktree_path").Optional().Nillable(),
		field.String("source_branch").Optional().Nillable(),
		field.String("target_branch").Optional().Nillable(),
		// Defaults below mirror db.Default* (db/defaults.go); the schema cannot
		// import that package without an ent-codegen import cycle. Keep in sync.
		field.String("current_stage").Default("backlog"),
		field.String("priority").Default("medium"),
		field.String("user_id").Optional().Nillable(),
		field.String("parent_task_id").Optional().Nillable(),
		field.Int("max_iterations").Default(20),
		field.Int("token_budget").Optional().Nillable(),
		field.Int("cost_budget_cents").Optional().Nillable(),
		field.Bool("silver_bullet").Default(false),
		field.Bool("plan_mode").Default(false),
		// autonomy controls the permission gate: "spec_gated" and "full" auto-approve
		// all permission requests; "manual" keeps today's gated behaviour. Empty-string
		// rows (pre-migration) are treated as "manual" in IsAllowAll to preserve the
		// gate for old tasks. New tasks default to "spec_gated" via the schema default.
		field.String("autonomy").Default("spec_gated"),
		field.JSON("metadata", map[string]any{}).Optional(),
		field.String("project_id").Optional().Nillable(),
		field.String("spawner_id").Optional().Nillable(),
		// routine_id names the task_schedule that materialized this task.
		// It is the anchor for "routine" capability grants, so it is set by
		// the scheduler only and never accepted from a request body — a
		// caller-writable routine id would let any task claim a routine's grants.
		field.String("routine_id").Optional().Nillable(),
		// kind is "pipeline" (runs the stage order) or "job" (one agent run,
		// no worktree, never on the board). Set by the scheduler only.
		field.String("kind").Default("pipeline"),
		field.JSON("applications", []string{}).
			Default([]string{}).
			Annotations(entsql.Default("[]")),
		field.Float("rank").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (Task) Edges() []ent.Edge {
	cascade := entsql.Annotation{OnDelete: entsql.Cascade}
	return []ent.Edge{
		edge.To("stage_runs", StageRun.Type).Annotations(cascade),
		edge.To("permissions", TaskPermission.Type).Annotations(cascade),
		edge.To("dependencies", TaskDependency.Type).Annotations(cascade),
		edge.To("dependents", TaskDependency.Type).Annotations(cascade),
	}
}

func (Task) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("current_stage"),
		index.Fields("parent_task_id"),
		index.Fields("project_id"),
		index.Fields("silver_bullet", "priority", "rank", "created_at"),
	}
}
