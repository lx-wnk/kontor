package schema

import (
	"time"

	"entgo.io/ent"
	entsql "entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type StageRun struct{ ent.Schema }

func (StageRun) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("id").Immutable(),
		field.String("task_id").Immutable(),
		field.String("stage"),
		field.String("session_id").Optional().Nillable(),
		field.String("session_name").Optional().Nillable(),
		field.Int("pid").Optional().Nillable(),
		field.String("status").Default("pending"),
		field.Int("iteration").Default(0),
		field.JSON("output", map[string]any{}).Optional(),
		field.Int("tokens_used").Default(0),
		field.Int("cost_cents").Default(0),
		field.Time("started_at").Optional().Nillable(),
		field.Time("ended_at").Optional().Nillable(),
		field.Time("last_grant_at").Optional().Nillable(),
		field.Int("retry_count").Default(0),
		field.Int("rate_limit_retry_count").Default(0),
		field.Time("next_retry_at").Optional().Nillable(),
		field.String("pending_user_prompt").Optional().Nillable(),
		field.String("wait_reason").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable().Annotations(entsql.Default("datetime('now')")),
	}
}

func (StageRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("task", Task.Type).Ref("stage_runs").Field("task_id").Unique().Required().Immutable(),
		edge.To("permission_requests", PermissionRequest.Type).Annotations(entsql.Annotation{OnDelete: entsql.Cascade}),
	}
}

func (StageRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("session_id"),
		index.Fields("task_id", "stage", "iteration"),
		index.Fields("task_id", "created_at"),
		// DB-level guard: at most one running stage_run per task.
		// Catches multi-spawn bugs the runtime re-entry guard misses.
		// Also serves as the task_id lookup index for running rows.
		index.Fields("task_id").Unique().Annotations(
			entsql.IndexAnnotation{Where: "status = 'running'"},
		),
	}
}
