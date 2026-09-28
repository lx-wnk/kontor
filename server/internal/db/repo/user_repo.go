package repo

import (
	"context"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/permissionpreset"
	"github.com/lx-wnk/kontor/server/internal/db/ent/remoteregistration"
	"github.com/lx-wnk/kontor/server/internal/db/ent/task"
	entuser "github.com/lx-wnk/kontor/server/internal/db/ent/user"
)

// ProviderUserInfo holds fields from the OAuth provider user API.
type ProviderUserInfo struct {
	ID          string
	Login       string
	DisplayName string
	AvatarURL   string
}

// UserRepo manages user persistence.
type UserRepo interface {
	Upsert(ctx context.Context, info ProviderUserInfo) (*ent.User, error)
	GetByID(ctx context.Context, id string) (*ent.User, error)
	Delete(ctx context.Context, id string) error
}

type entUserRepo struct {
	client *ent.Client
}

// NewUserRepo returns a UserRepo backed by the given ent client.
func NewUserRepo(client *ent.Client) UserRepo {
	return &entUserRepo{client: client}
}

// Upsert creates or updates a user by Provider ID.
// Provider ID is stable across username renames, making it the correct PK.
func (r *entUserRepo) Upsert(ctx context.Context, info ProviderUserInfo) (*ent.User, error) {
	now := time.Now()

	existing, err := r.client.User.Get(ctx, info.ID)
	if err != nil && !ent.IsNotFound(err) {
		return nil, fmt.Errorf("user.Upsert lookup %s: %w", info.ID, err)
	}

	if existing != nil {
		// Update the existing record with latest login data.
		update := r.client.User.UpdateOneID(info.ID).
			SetProviderLogin(info.Login).
			SetLastLoginAt(now)
		if info.DisplayName != "" {
			update = update.SetDisplayName(info.DisplayName)
		}
		if info.AvatarURL != "" {
			update = update.SetAvatarURL(info.AvatarURL)
		}
		u, err := update.Save(ctx)
		if err != nil {
			return nil, fmt.Errorf("user.Upsert update %s: %w", info.ID, err)
		}
		return u, nil
	}

	// Insert new user.
	create := r.client.User.Create().
		SetID(info.ID).
		SetProviderLogin(info.Login).
		SetLastLoginAt(now)
	if info.DisplayName != "" {
		create = create.SetDisplayName(info.DisplayName)
	}
	if info.AvatarURL != "" {
		create = create.SetAvatarURL(info.AvatarURL)
	}
	u, err := create.Save(ctx)
	if err != nil {
		// Handle concurrent insert race: retry as update.
		if ent.IsConstraintError(err) {
			return r.retryAsUpdate(ctx, info, now)
		}
		return nil, fmt.Errorf("user.Upsert create %s: %w", info.ID, err)
	}
	return u, nil
}

// retryAsUpdate handles the concurrent-insert race by falling back to an update.
func (r *entUserRepo) retryAsUpdate(ctx context.Context, info ProviderUserInfo, now time.Time) (*ent.User, error) {
	update := r.client.User.UpdateOneID(info.ID).
		SetProviderLogin(info.Login).
		SetLastLoginAt(now)
	if info.DisplayName != "" {
		update = update.SetDisplayName(info.DisplayName)
	}
	if info.AvatarURL != "" {
		update = update.SetAvatarURL(info.AvatarURL)
	}
	u, err := update.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("user.Upsert retry-update %s: %w", info.ID, err)
	}
	return u, nil
}

// GetByID returns a user by their Provider ID.
func (r *entUserRepo) GetByID(ctx context.Context, id string) (*ent.User, error) {
	u, err := r.client.User.Query().
		Where(entuser.IDEQ(id)).
		Only(ctx)
	if err != nil {
		return nil, fmt.Errorf("user.GetByID %s: %w", id, err)
	}
	return u, nil
}

// Delete permanently removes a user and anonymizes their linked data (GDPR Art. 17).
// Within a single transaction:
//   - Tasks owned by the user have user_id nulled (work history preserved, owner anonymized).
//   - PermissionPresets owned by the user have user_id nulled.
//   - RemoteRegistrations owned by the user are deleted.
//   - The user row itself is deleted last.
//
// API keys are not user-scoped (no user_id column) and are therefore not touched.
func (r *entUserRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("user.Delete begin tx %s: %w", id, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.Task.Update().
		Where(task.UserIDEQ(id)).
		ClearUserID().
		Exec(ctx); err != nil {
		return fmt.Errorf("user.Delete nullify tasks %s: %w", id, err)
	}
	if err := tx.PermissionPreset.Update().
		Where(permissionpreset.UserIDEQ(id)).
		ClearUserID().
		Exec(ctx); err != nil {
		return fmt.Errorf("user.Delete nullify presets %s: %w", id, err)
	}
	if _, err := tx.RemoteRegistration.Delete().
		Where(remoteregistration.UserIDEQ(id)).
		Exec(ctx); err != nil {
		return fmt.Errorf("user.Delete remotes %s: %w", id, err)
	}
	if err := tx.User.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("user.Delete %s: %w", id, err)
	}
	return tx.Commit()
}
