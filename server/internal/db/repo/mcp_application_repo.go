package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/mcpapplication"
	"github.com/lx-wnk/kontor/server/internal/db/ent/schema"
)

type UpsertMCPApplicationInput struct {
	ResourceID string
	ServerName string
	// AttachAll applies when the row is created; an existing row keeps its value.
	AttachAll bool
	// Entry applies when the row is created; an existing row keeps its value.
	Entry json.RawMessage
}

type MCPApplicationRepo interface {
	Upsert(ctx context.Context, in UpsertMCPApplicationInput) (*ent.MCPApplication, error)
	GetByResourceID(ctx context.Context, resourceID string) (*ent.MCPApplication, error)
	List(ctx context.Context) ([]*ent.MCPApplication, error)
	SetAttachAll(ctx context.Context, resourceID string, attachAll bool) (*ent.MCPApplication, error)
	SetRequiredEnv(ctx context.Context, resourceID string, names []string) (*ent.MCPApplication, error)
	RecordCatalogue(ctx context.Context, resourceID string, tools []schema.CatalogueTool, catalogueErr string, at time.Time) error
	SetEntry(ctx context.Context, resourceID string, entry json.RawMessage) (*ent.MCPApplication, error)
	SetExport(ctx context.Context, resourceID string, export bool, exportedHash string) (*ent.MCPApplication, error)
	Delete(ctx context.Context, resourceID string) error
}

type entMCPApplicationRepo struct{ client *ent.Client }

func NewMCPApplicationRepo(client *ent.Client) MCPApplicationRepo {
	return &entMCPApplicationRepo{client: client}
}

func (r *entMCPApplicationRepo) Upsert(ctx context.Context, in UpsertMCPApplicationInput) (*ent.MCPApplication, error) {
	existing, err := r.GetByResourceID(ctx, in.ResourceID)
	if err == nil {
		return existing, nil
	}
	if !ent.IsNotFound(err) {
		return nil, fmt.Errorf("mcpapplication.Upsert: %w", err)
	}
	create := r.client.MCPApplication.Create().
		SetID(uuid.New().String()).
		SetResourceID(in.ResourceID).
		SetServerName(in.ServerName).
		SetAttachAll(in.AttachAll)
	if in.Entry != nil {
		create = create.SetEntry(in.Entry)
	}
	row, err := create.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcpapplication.Upsert: %w", err)
	}
	return row, nil
}

func (r *entMCPApplicationRepo) GetByResourceID(ctx context.Context, resourceID string) (*ent.MCPApplication, error) {
	return r.client.MCPApplication.Query().Where(mcpapplication.ResourceID(resourceID)).Only(ctx)
}

func (r *entMCPApplicationRepo) List(ctx context.Context) ([]*ent.MCPApplication, error) {
	rows, err := r.client.MCPApplication.Query().Order(ent.Asc(mcpapplication.FieldServerName)).All(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcpapplication.List: %w", err)
	}
	return rows, nil
}

func (r *entMCPApplicationRepo) SetAttachAll(ctx context.Context, resourceID string, attachAll bool) (*ent.MCPApplication, error) {
	row, err := r.GetByResourceID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	return row.Update().SetAttachAll(attachAll).Save(ctx)
}

func (r *entMCPApplicationRepo) SetRequiredEnv(ctx context.Context, resourceID string, names []string) (*ent.MCPApplication, error) {
	row, err := r.GetByResourceID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	if names == nil {
		names = []string{}
	}
	return row.Update().SetRequiredEnv(names).Save(ctx)
}

func (r *entMCPApplicationRepo) RecordCatalogue(ctx context.Context, resourceID string, tools []schema.CatalogueTool, catalogueErr string, at time.Time) error {
	row, err := r.GetByResourceID(ctx, resourceID)
	if err != nil {
		return fmt.Errorf("mcpapplication.RecordCatalogue: %w", err)
	}
	upd := row.Update().SetCatalogueError(catalogueErr).SetCatalogueRefreshedAt(at)
	if catalogueErr == "" {
		if tools == nil {
			tools = []schema.CatalogueTool{}
		}
		upd = upd.SetCatalogue(tools)
	}
	return upd.Exec(ctx)
}

func (r *entMCPApplicationRepo) SetEntry(ctx context.Context, resourceID string, entry json.RawMessage) (*ent.MCPApplication, error) {
	row, err := r.GetByResourceID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	return row.Update().SetEntry(entry).Save(ctx)
}

func (r *entMCPApplicationRepo) SetExport(ctx context.Context, resourceID string, export bool, exportedHash string) (*ent.MCPApplication, error) {
	row, err := r.GetByResourceID(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	return row.Update().SetExportToClaude(export).SetExportedHash(exportedHash).Save(ctx)
}

func (r *entMCPApplicationRepo) Delete(ctx context.Context, resourceID string) error {
	_, err := r.client.MCPApplication.Delete().Where(mcpapplication.ResourceID(resourceID)).Exec(ctx)
	return err
}
