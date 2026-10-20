package schema

import (
	"time"

	"entgo.io/ent"
	entsql "entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Project holds the schema definition for the Project entity.
type Project struct{ ent.Schema }

// Fields of the Project.
func (Project) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("id").Immutable(),
		field.String("slug").Unique(),
		field.String("name"),
		field.String("description").Optional().Nillable(),
		field.String("color").Optional().Nillable(),
		field.String("default_spawner_id").Optional().Nillable(),
		field.String("setup_command").Optional().Nillable(),
		field.String("vault_folder").Optional().Nillable(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

// Edges of the Project.
func (Project) Edges() []ent.Edge {
	cascade := entsql.Annotation{OnDelete: entsql.Cascade}
	return []ent.Edge{
		edge.To("folders", ProjectFolder.Type).Annotations(cascade),
	}
}

// Indexes of the Project.
func (Project) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("slug"),
	}
}
