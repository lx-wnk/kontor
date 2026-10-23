package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// CloudRoutine is a claude.ai routine the user configured: where to fire it and
// the bearer token for it, encrypted at rest.
type CloudRoutine struct{ ent.Schema }

// Mixin of the CloudRoutine.
func (CloudRoutine) Mixin() []ent.Mixin {
	return []ent.Mixin{IDTimestampsMixin{}}
}

// Fields of the CloudRoutine.
func (CloudRoutine) Fields() []ent.Field {
	return []ent.Field{
		field.String("name"),
		field.String("fire_url"),
		field.String("spawner_id"),
		field.String("token_ciphertext").Sensitive(),
		field.String("token_nonce").Sensitive(),
	}
}
