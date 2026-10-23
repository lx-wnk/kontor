package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// CloudSession is a claude.ai cloud session Kontor knows about, whether it
// started the session, imported it, or learned of it from a routine.
type CloudSession struct{ ent.Schema }

// Mixin of the CloudSession.
func (CloudSession) Mixin() []ent.Mixin {
	return []ent.Mixin{IDTimestampsMixin{}}
}

// Fields of the CloudSession.
func (CloudSession) Fields() []ent.Field {
	return []ent.Field{
		field.String("session_id").Unique(),
		field.String("url"),
		field.String("spawner_id"),
		field.String("title"),
		field.Enum("origin").Values("started", "imported", "routine"),
		field.String("last_message").Optional(),
		field.Time("last_message_at").Optional().Nillable(),
		field.String("last_error").Optional(),
	}
}
