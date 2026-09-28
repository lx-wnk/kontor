package repo

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/remoteregistration"
)

// RemoteRegistrationRepo is the data-access interface for remote registrations.
type RemoteRegistrationRepo interface {
	Create(ctx context.Context, input CreateRemoteInput) (*ent.RemoteRegistration, error)
	ListForUser(ctx context.Context, userID string) ([]*ent.RemoteRegistration, error)
	// Delete removes the registration by id scoped to userID.
	// Returns false (not an error) when the record is not found or belongs to another user.
	Delete(ctx context.Context, id string, userID string) (bool, error)
}

// CreateRemoteInput holds the fields needed to create a RemoteRegistration.
type CreateRemoteInput struct {
	UserID    string
	URL       string
	Name      *string
	BearerKey *string
}

type entRemoteRegistrationRepo struct{ client *ent.Client }

// NewRemoteRegistrationRepo returns a RemoteRegistrationRepo backed by ent.
func NewRemoteRegistrationRepo(client *ent.Client) RemoteRegistrationRepo {
	return &entRemoteRegistrationRepo{client: client}
}

func (r *entRemoteRegistrationRepo) Create(ctx context.Context, in CreateRemoteInput) (*ent.RemoteRegistration, error) {
	reg, err := r.client.RemoteRegistration.Create().
		SetID(uuid.New().String()).
		SetUserID(in.UserID).
		SetURL(in.URL).
		SetNillableName(in.Name).
		SetNillableBearerKey(in.BearerKey).
		Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("remoteRegistration.Create: %w", err)
	}
	return reg, nil
}

func (r *entRemoteRegistrationRepo) ListForUser(ctx context.Context, userID string) ([]*ent.RemoteRegistration, error) {
	regs, err := r.client.RemoteRegistration.Query().
		Where(remoteregistration.UserID(userID)).
		Order(remoteregistration.ByCreatedAt()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("remoteRegistration.ListForUser: %w", err)
	}
	return regs, nil
}

func (r *entRemoteRegistrationRepo) Delete(ctx context.Context, id string, userID string) (bool, error) {
	n, err := r.client.RemoteRegistration.Delete().
		Where(remoteregistration.ID(id), remoteregistration.UserID(userID)).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("remoteRegistration.Delete: %w", err)
	}
	return n > 0, nil
}
