package repo

import (
	"context"
	"errors"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// IsNotFound reports whether err is ent's not-found error. It exists so callers
// outside this package do not have to import ent just to classify an error.
func IsNotFound(err error) bool { return ent.IsNotFound(err) }

// rollback aborts tx and returns cause, preserving the original failure when the
// rollback itself also fails.
func rollback(tx *ent.Tx, cause error) error {
	if rerr := tx.Rollback(); rerr != nil {
		return fmt.Errorf("%w (rollback failed: %v)", cause, rerr)
	}
	return cause
}

// WithTx runs fn inside a transaction, committing on success and rolling back on
// error or panic. This is the shape new transactional code in this package
// should use; adopting it in the repositories that already hand-roll their own
// transaction (a deferred blind rollback in some, an explicit per-branch
// rollback in others) has not happened yet.
func WithTx(ctx context.Context, client *ent.Client, fn func(tx *ent.Tx) error) error {
	tx, err := client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		return rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// WithWriteTx runs fn inside a transaction opened with BEGIN IMMEDIATE rather
// than the BEGIN (deferred) that WithTx uses. Use it for read-then-write
// sequences — count usage then insert, count references then delete — where
// a deferred transaction that reads and later writes can race a concurrent
// writer's commit into SQLITE_BUSY_SNAPSHOT: a snapshot invalidation, not
// lock contention, so busy_timeout does not retry it and the operation just
// fails. Beginning the write lock up front avoids the race entirely —
// busy_timeout's wait-and-retry applies to ordinary lock contention, which is
// what BEGIN IMMEDIATE turns this into.
//
// client must be db.DBBundle.WriteClient — a client opened onto a connection
// pool started with the driver's `_txlock=immediate` DSN parameter, not the
// plain client WithTx takes. The db.WriteClient type makes that a compile-time
// requirement rather than a convention: it cannot be constructed from a bare
// *ent.Client without an explicit db.WriteClient{Client: ...} conversion at
// the one place (db.Open) that knows the pool was actually opened that way —
// see db.WriteClient's doc comment. This is a distinct entry point rather
// than an option on WithTx because modernc.org/sqlite (the driver in use)
// only exposes BEGIN IMMEDIATE as a per-connection DSN setting: sql.TxOptions'
// Isolation field is ignored by the driver's transaction begin (verified by
// reading modernc.org/sqlite@v1.57.0's tx.go — only opts.ReadOnly is
// consulted), so there is no per-call ent or database/sql option that
// selects it. Given that, scoping immediate mode to specific call sites
// instead of every WithTx caller requires a second pool, which is what
// WriteClient is.
//
// This cannot be exercised on the ":memory:" test fixture: that fixture
// pins the whole pool to one connection (db.Open) specifically so every
// query shares one database, which also means there is never a second,
// concurrent connection to race against — WriteClient equals Client there.
// Do not add a ":memory:"-backed test that claims to cover the race; it
// cannot fail even if BEGIN IMMEDIATE were silently dropped.
func WithWriteTx(ctx context.Context, client db.WriteClient, fn func(tx *ent.Tx) error) error {
	return WithTx(ctx, client.Client, fn)
}

// Deletion refusals shared by managed resources. Only the spawner repository
// had refusals of this shape; they are shared so every kind can refuse for a
// named reason rather than inventing its own error string.
var (
	// ErrResourceBuiltIn means the resource ships with the dashboard and is not
	// the user's to delete.
	ErrResourceBuiltIn = errors.New("resource is built in and cannot be deleted")
	// ErrResourceReferenced means something else still points at this resource.
	ErrResourceReferenced = errors.New("resource is still referenced")
)
