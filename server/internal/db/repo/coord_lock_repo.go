package repo

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/coordlock"
)

type CoordLockRepo interface {
	Acquire(ctx context.Context, namespace, key, owner string, ttl time.Duration) (acquired bool, curOwner string, expiresAt time.Time, err error)
	Release(ctx context.Context, namespace, key, owner string) error
	ListActive(ctx context.Context, namespace string) ([]*ent.CoordLock, error)
}

type entCoordLockRepo struct{ client *ent.Client }

func NewCoordLockRepo(client *ent.Client) CoordLockRepo { return &entCoordLockRepo{client: client} }

func (r *entCoordLockRepo) Acquire(ctx context.Context, namespace, key, owner string, ttl time.Duration) (bool, string, time.Time, error) {
	const maxRetries = 10
	for attempt := range maxRetries {
		ok, cur, exp, err := r.tryAcquire(ctx, namespace, key, owner, ttl)
		if err == nil {
			return ok, cur, exp, nil
		}
		// SQLite busy/locked under concurrent writers — retry with linear back-off
		if isBusyError(err) && attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Millisecond)
			continue
		}
		return false, "", time.Time{}, err
	}
	return false, "", time.Time{}, fmt.Errorf("coordlock.Acquire: exhausted retries")
}

func (r *entCoordLockRepo) tryAcquire(ctx context.Context, namespace, key, owner string, ttl time.Duration) (bool, string, time.Time, error) {
	now := time.Now()
	exp := now.Add(ttl)

	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, "", time.Time{}, fmt.Errorf("coordlock.Acquire tx: %w", err)
	}

	existing, err := tx.CoordLock.Query().Where(coordlock.Namespace(namespace), coordlock.Key(key)).Only(ctx)
	switch {
	case ent.IsNotFound(err):
		_, cerr := tx.CoordLock.Create().
			SetID(uuid.New().String()).
			SetNamespace(namespace).SetKey(key).SetOwnerTaskID(owner).
			SetAcquiredAt(now).SetExpiresAt(exp).
			Save(ctx)
		if cerr != nil {
			_ = tx.Rollback()
			if ent.IsConstraintError(cerr) {
				cur, cexp := r.read(ctx, namespace, key)
				return false, cur, cexp, nil
			}
			return false, "", time.Time{}, fmt.Errorf("coordlock.Acquire insert: %w", cerr)
		}
		return true, owner, exp, tx.Commit()

	case err != nil:
		_ = tx.Rollback()
		return false, "", time.Time{}, fmt.Errorf("coordlock.Acquire query: %w", err)

	default:
		if existing.ExpiresAt.Before(now) || existing.OwnerTaskID == owner {
			_, uerr := tx.CoordLock.UpdateOne(existing).
				SetOwnerTaskID(owner).SetAcquiredAt(now).SetExpiresAt(exp).
				Save(ctx)
			if uerr != nil {
				_ = tx.Rollback()
				return false, "", time.Time{}, fmt.Errorf("coordlock.Acquire update: %w", uerr)
			}
			return true, owner, exp, tx.Commit()
		}
		_ = tx.Rollback()
		return false, existing.OwnerTaskID, existing.ExpiresAt, nil
	}
}

func (r *entCoordLockRepo) read(ctx context.Context, namespace, key string) (string, time.Time) {
	row, err := r.client.CoordLock.Query().Where(coordlock.Namespace(namespace), coordlock.Key(key)).Only(ctx)
	if err != nil {
		return "", time.Time{}
	}
	return row.OwnerTaskID, row.ExpiresAt
}

func (r *entCoordLockRepo) Release(ctx context.Context, namespace, key, owner string) error {
	n, err := r.client.CoordLock.Delete().
		Where(coordlock.Namespace(namespace), coordlock.Key(key), coordlock.OwnerTaskID(owner)).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("coordlock.Release: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("coordlock.Release: not held by %s", owner)
	}
	return nil
}

func (r *entCoordLockRepo) ListActive(ctx context.Context, namespace string) ([]*ent.CoordLock, error) {
	return r.client.CoordLock.Query().
		Where(coordlock.Namespace(namespace), coordlock.ExpiresAtGT(time.Now())).
		Order(ent.Asc(coordlock.FieldKey)).
		All(ctx)
}

// isBusyError detects SQLite "database is locked" / "database is busy" errors
// that are safe to retry under high concurrency.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database is busy") ||
		strings.Contains(msg, "SQLITE_BUSY")
}
