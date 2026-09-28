package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TaskSchedule stores a recurring pipeline-task definition: a task template plus
// a validated cron expression and firing policy. The scheduler materializes a
// fresh task from this template on each due tick. NL→cron translation happens at
// create/edit; firing reads only cron_expr (deterministic, offline-safe).
type TaskSchedule struct{ ent.Schema }

func (TaskSchedule) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("id").Immutable(),
		field.String("name").MaxLen(200),
		field.Bool("enabled").Default(true),

		// Schedule definition.
		field.String("nl_text").Optional().Nillable(), // original natural-language phrase
		field.String("cron_expr"),                     // validated 5-field cron
		field.String("timezone").Default("UTC"),       // IANA tz name
		field.String("catchup").Default("none"),       // none | once (post-downtime policy)

		// Task template (mirrors repo.CreateTaskInput). The materializer derives a
		// unique slug per fire from slug_prefix.
		field.String("slug_prefix").MaxLen(100),
		field.String("title").MaxLen(200),
		field.String("description").Optional().Nillable(),
		field.String("cwd").MaxLen(4096),
		field.String("source_branch").Optional().Nillable(),
		field.String("target_branch").Optional().Nillable(),
		field.String("priority").Default("medium"),
		// run_mode "job" fires a job task; "pipeline" fires a pipeline task
		// that starts in ready. Existing rows were migrated to "pipeline".
		field.String("run_mode").Default("job"),
		field.Int("max_iterations").Default(20),
		field.Int("token_budget").Optional().Nillable(),
		field.Int("cost_budget_cents").Optional().Nillable(),
		field.Bool("silver_bullet").Default(false),
		field.String("project_id").Optional().Nillable(),
		field.String("spawner_id").Optional().Nillable(),
		field.String("permission_template").Optional().Nillable(),
		field.JSON("metadata", map[string]any{}).Optional(),

		// Fire state.
		field.Time("next_run_at").Optional().Nillable(),
		field.Time("last_run_at").Optional().Nillable(),
		field.String("last_task_id").Optional().Nillable(),
		// Fires refused because the previous run was still in flight.
		field.Time("last_skipped_at").Optional().Nillable(),
		field.Int("skipped_count").Default(0),

		field.String("resource_id").Optional().Default(""),

		// owner_module names the module that created this routine, empty for
		// one a human created. It is a column rather than a metadata key
		// because it decides who may change the row: a module may act on its
		// own routines and on no others, and a permission that lives in a JSON
		// blob anything can write is not a permission.
		field.String("owner_module").Optional().Default(""),
		field.JSON("applications", []string{}).
			Default([]string{}).
			Annotations(entsql.Default("[]")),

		field.String("user_id").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (TaskSchedule) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("enabled"),
		index.Fields("next_run_at"),
	}
}
