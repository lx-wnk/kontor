package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/cloudsession"
)

// ErrCloudSessionExists is returned when a cloud session with the same
// session_id is already stored.
var ErrCloudSessionExists = errors.New("cloud session already exists")

// CreateCloudSessionInput carries the fields of a new cloud session.
type CreateCloudSessionInput struct {
	SessionID string
	URL       string
	SpawnerID string
	Title     string
	Origin    cloudsession.Origin
}

// CloudSessionRepo manages cloud session persistence.
type CloudSessionRepo interface {
	Create(ctx context.Context, in CreateCloudSessionInput) (*ent.CloudSession, error)
	// List returns the sessions newest first.
	List(ctx context.Context) ([]*ent.CloudSession, error)
	Get(ctx context.Context, id string) (*ent.CloudSession, error)
	// RecordSend stores the last message sent and its outcome; an empty errText
	// clears the previous error.
	RecordSend(ctx context.Context, id, message string, at time.Time, errText string) (*ent.CloudSession, error)
	// RecordError stores errText as the last error without touching the last message.
	RecordError(ctx context.Context, id, errText string) (*ent.CloudSession, error)
	Delete(ctx context.Context, id string) error
}

type entCloudSessionRepo struct {
	client *ent.Client
}

// NewCloudSessionRepo returns a CloudSessionRepo backed by the given ent client.
func NewCloudSessionRepo(client *ent.Client) CloudSessionRepo {
	return &entCloudSessionRepo{client: client}
}

func (r *entCloudSessionRepo) Create(ctx context.Context, in CreateCloudSessionInput) (*ent.CloudSession, error) {
	s, err := r.client.CloudSession.Create().
		SetID(uuid.New().String()).
		SetSessionID(in.SessionID).
		SetURL(in.URL).
		SetSpawnerID(in.SpawnerID).
		SetTitle(in.Title).
		SetOrigin(in.Origin).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return nil, ErrCloudSessionExists
		}
		return nil, fmt.Errorf("cloudSession.Create: %w", err)
	}
	return s, nil
}

func (r *entCloudSessionRepo) List(ctx context.Context) ([]*ent.CloudSession, error) {
	sessions, err := r.client.CloudSession.Query().
		Order(cloudsession.ByCreatedAt(sql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudSession.List: %w", err)
	}
	return sessions, nil
}

func (r *entCloudSessionRepo) Get(ctx context.Context, id string) (*ent.CloudSession, error) {
	s, err := r.client.CloudSession.Get(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("cloudSession.Get: %w", err)
	}
	return s, nil
}

func (r *entCloudSessionRepo) RecordSend(ctx context.Context, id, message string, at time.Time, errText string) (*ent.CloudSession, error) {
	u := r.client.CloudSession.UpdateOneID(id).
		SetLastMessage(message).
		SetLastMessageAt(at)
	if errText == "" {
		u = u.ClearLastError()
	} else {
		u = u.SetLastError(errText)
	}
	s, err := u.Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudSession.RecordSend: %w", err)
	}
	return s, nil
}

func (r *entCloudSessionRepo) RecordError(ctx context.Context, id, errText string) (*ent.CloudSession, error) {
	s, err := r.client.CloudSession.UpdateOneID(id).SetLastError(errText).Save(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudSession.RecordError: %w", err)
	}
	return s, nil
}

func (r *entCloudSessionRepo) Delete(ctx context.Context, id string) error {
	if err := r.client.CloudSession.DeleteOneID(id).Exec(ctx); err != nil {
		return fmt.Errorf("cloudSession.Delete: %w", err)
	}
	return nil
}
