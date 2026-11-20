package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// AttentionMark hides a needs-you item until its source posts something newer
// than seen_through.
type AttentionMark struct{ ent.Schema }

// Mixin of the AttentionMark.
func (AttentionMark) Mixin() []ent.Mixin { return []ent.Mixin{IDTimestampsMixin{}} }

// Fields of the AttentionMark.
func (AttentionMark) Fields() []ent.Field {
	return []ent.Field{
		field.String("item_key").Unique().NotEmpty().MaxLen(160),
		field.Time("seen_through"),
	}
}

// AttentionBaseline is a one-row table that records when attention marking
// started.
type AttentionBaseline struct{ ent.Schema }

// Mixin of the AttentionBaseline.
func (AttentionBaseline) Mixin() []ent.Mixin { return []ent.Mixin{IDTimestampsMixin{}} }

// Fields of the AttentionBaseline.
func (AttentionBaseline) Fields() []ent.Field {
	return []ent.Field{
		field.Time("started_at").Immutable(),
	}
}
