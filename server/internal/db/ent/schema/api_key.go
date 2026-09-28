// server/internal/db/ent/schema/api_key.go
package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ApiKey holds the schema definition for the ApiKey entity.
type ApiKey struct{ ent.Schema }

// Fields of the ApiKey.
func (ApiKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("id"),
		field.String("name"),
		field.String("key_hash").Unique().Sensitive(),
		field.JSON("scopes", []string{}).Default([]string{}),
		field.Bool("active").Default(true),
		// kind separates the keys a person created from the ephemeral ones the
		// pipeline mints per stage run. The default keeps every existing row a
		// user key without a backfill.
		field.String("kind").Default("user"),
		// stage_run_id is the attribution a capability context is resolved from.
		// Empty for a user key.
		field.String("stage_run_id").Default(""),
		// session_pid is the process a kontor_session key belongs to. Nil until
		// the spawn returns a pid. Not indexed: SQLite refuses DROP COLUMN on an
		// indexed column, and dropping it is the down path.
		field.Int("session_pid").Optional().Nillable(),
		// expires_at is a hard stop independent of active: the orchestrator
		// revoking on a terminal transition and this timestamp are two nets, and
		// a server that dies between spawn and transition only trips the second.
		field.Time("expires_at").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("last_used_at").Optional().Nillable(),
	}
}

// Indexes of the ApiKey.
func (ApiKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("active"),
		index.Fields("stage_run_id"),
		index.Fields("expires_at"),
	}
}
