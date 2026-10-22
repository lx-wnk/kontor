package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema/field"
)

// CatalogueTool is one entry of a server's tools/list, as last read. The hints
// are the server's own claims and are shown, never trusted.
type CatalogueTool struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
}

type MCPApplication struct{ ent.Schema }

func (MCPApplication) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").StorageKey("id").Immutable(),
		field.String("resource_id").Unique().Immutable(),
		field.String("server_name").Immutable(),
		field.Bool("attach_all").Default(false),
		field.JSON("required_env", []string{}).
			Default([]string{}).
			Annotations(entsql.Default("[]")),
		field.JSON("allowed_spawners", []string{}).
			Default([]string{}).
			Annotations(entsql.Default("[]")),
		field.JSON("catalogue", []CatalogueTool{}).
			Default([]CatalogueTool{}).
			Annotations(entsql.Default("[]")),
		field.String("catalogue_error").Default(""),
		field.Time("catalogue_refreshed_at").Optional().Nillable(),
		// entry is the server definition itself (mcpapps.ServerEntry): transport,
		// command, args and non-secret env. Secrets live in application_secret.
		// Bytes, not field.JSON with json.RawMessage: under a toolchain with the
		// jsonv2 experiment that alias resolves to jsontext.Value and the
		// generated code stops compiling on the toolchain CI pins.
		field.Bytes("entry").
			Default([]byte("{}")).
			Annotations(entsql.Default("{}")),
		// export_to_claude mirrors the entry into Claude's own config so plain
		// `claude` sessions see the server; exported_hash is what we last wrote.
		field.Bool("export_to_claude").Default(false),
		field.String("exported_hash").Default(""),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
