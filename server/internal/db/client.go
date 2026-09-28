package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"

	"github.com/google/uuid"

	"github.com/lx-wnk/kontor/server/internal/capability"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/grant"
	"github.com/lx-wnk/kontor/server/internal/db/ent/task"
	"github.com/lx-wnk/kontor/server/internal/db/ent/taskpermission"
)

// WriteClient marks an *ent.Client as opened onto the connection pool wired
// for BEGIN IMMEDIATE (the driver's `_txlock=immediate` DSN parameter).
// repo.WithWriteTx requires this named type rather than a plain *ent.Client
// so that passing the ordinary (deferred-BEGIN) client is a compile error
// instead of a silent downgrade to the semantics WithWriteTx exists to
// prevent — see its doc comment.
type WriteClient struct {
	*ent.Client
}

// DBBundle holds both the ent client and the underlying *sql.DB.
// The raw *sql.DB is needed for repositories that execute hand-written SQL
// (e.g. FTS5 queries, notification_config, push_subscriptions).
// Calling Close() or Client.Close() invalidates both fields.
type DBBundle struct {
	Client *ent.Client
	DB     *sql.DB
	// WriteClient is a second connection pool onto the same database, opened
	// with the driver's `_txlock=immediate` DSN parameter so its transactions
	// take the write lock at BEGIN instead of deferring it to the first write
	// statement. Use it via repo.WithWriteTx for read-then-write sequences
	// that must not race a concurrent committer into SQLITE_BUSY_SNAPSHOT —
	// see the doc comment on WithWriteTx for why a second pool, rather than a
	// per-call option, is what the driver actually offers.
	//
	// Equal to Client when path is ":memory:": that fixture pins the whole
	// pool to a single connection (see below) specifically so every query
	// shares one in-memory database, and a second, independently-opened
	// ":memory:" pool would just be a second, empty database sitting next to
	// it — not a race-safe stand-in for a real second connection.
	WriteClient WriteClient
}

// Close closes the database connection(s). Client, DB, and WriteClient all
// become invalid after this call.
// Note: Client.Close() also closes DB because the ent driver wraps the same *sql.DB.
func (b *DBBundle) Close() error {
	if b.WriteClient.Client != nil && b.WriteClient.Client != b.Client {
		if err := b.WriteClient.Close(); err != nil {
			_ = b.Client.Close()
			return fmt.Errorf("db: close write pool: %w", err)
		}
	}
	return b.Client.Close()
}

// Open returns a DBBundle connected to the SQLite database at path.
// Creates the database file and any missing parent directories if absent.
// Runs ent auto-migrate followed by runRawMigrations for hand-written tables.
// Use ":memory:" as path for in-memory databases (testing).
func Open(path string) (*DBBundle, error) {
	// modernc.org/sqlite uses _pragma=<name>(<value>) URI parameters,
	// not the _fk=1 shorthand used by mattn/go-sqlite3.
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-64000)&_pragma=temp_store(memory)&_pragma=mmap_size(134217728)"
	if path == ":memory:" {
		dsn = "file::memory:?mode=memory&_pragma=foreign_keys(1)"
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, fmt.Errorf("db: create parent dirs for %q: %w", path, err)
		}
	}
	// modernc.org/sqlite registers as "sqlite"; ent's dialect constant is "sqlite3".
	// Use OpenDB with the ent dialect constant so ent recognises the SQLite dialect.
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %q: %w", path, err)
	}
	if path == ":memory:" {
		// A bare ":memory:" database is per-connection: each pooled connection
		// opens its own empty schema, so migrations on one connection are invisible
		// to queries that land on another. Pin to a single connection so the whole
		// pool shares one in-memory database. Test-only path.
		sqlDB.SetMaxOpenConns(1)
	}
	drv := entsql.OpenDB(dialect.SQLite, sqlDB)
	client := ent.NewClient(ent.Driver(drv))
	// Rename github_login → provider_login before ent auto-migrate so ent finds
	// the column under the new name and does not add a blank provider_login column
	// alongside the old one. Idempotent: skipped when provider_login already exists.
	if err := migrateRenameGithubLogin(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: rename github_login: %w", err)
	}
	// Remove duplicate agent_cost_trends rows (same session_id) before ent auto-migrate
	// adds the UNIQUE index on session_id. Without this, the index creation would fail
	// on existing databases that contain duplicates from the pre-upsert BulkInsert era.
	// Idempotent: on a fresh database the table does not yet exist (no-op).
	if err := migrateDedupAgentCostTrends(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: dedup agent_cost_trends: %w", err)
	}
	// Rebuild the legacy (key, value) pipeline_configs table into the scoped
	// (id, key, project_id, value) shape before ent auto-migrate. The id field was
	// historically a phantom (declared in the schema but never materialised because
	// key was the PK); the per-stage-config index change forces a table rebuild whose
	// row-copy cannot populate the NOT NULL id, so we backfill id=key here first.
	// Idempotent: skipped once an id column exists.
	if err := migrateLegacyPipelineConfig(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: migrate legacy pipeline_configs: %w", err)
	}
	// Pre-seed the stage_run.session_id index so ent's auto-migrate sees it
	// already present and does a no-op instead of a table rebuild (which
	// crashes on existing DBs with "NOT NULL constraint failed: ...id" — see
	// PR #207). Idempotent via IF NOT EXISTS.
	if err := migrateEnsureStageRunSessionIndex(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: ensure stage_run session_id index: %w", err)
	}
	if err := migrateEnsureResourceUniqueIndex(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: ensure resource unique index: %w", err)
	}
	if err := migrateEnsureGrantIndexes(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: ensure grant indexes: %w", err)
	}
	if err := migrateEnsureGrantUsageIndex(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: ensure grant_usage index: %w", err)
	}
	if err := migrateEnsureMemoryEntryIndexes(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: ensure memory_entry indexes: %w", err)
	}
	if err := client.Schema.Create(context.Background()); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: auto-migrate: %w", err)
	}
	// Backfill task.rank for legacy rows created before the field existed, seeding
	// from created_at so existing column order is preserved. Idempotent.
	if err := backfillTaskRank(context.Background(), client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: backfill task rank: %w", err)
	}
	// migrateDropBareWebFetchGrants must run after ent auto-migrate (which creates
	// the task_permissions table) and before the server accepts traffic.
	if err := migrateDropBareWebFetchGrants(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: drop bare-WebFetch grants: %w", err)
	}
	// Normalize the legacy "approved" permission_request outcome to "granted".
	// Must run after ent auto-migrate and before the server accepts traffic.
	if err := migrateNormalizeApprovedOutcome(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: normalize approved outcome: %w", err)
	}
	// Copy legacy audit_logs rows into audit_events (forensics consolidation).
	// Idempotent — no-op once rows are migrated. Must run after ent auto-migrate.
	if err := migrateCopyAuditLogsToAuditEvents(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: copy audit_logs → audit_events: %w", err)
	}
	// Backfill task_permissions and permission_presets rows into grants, so the
	// capability gate's grant table starts from the legacy permission tables it
	// is replacing rather than empty. Idempotent — must run after ent
	// auto-migrate (task_permissions, permission_presets, and grants all exist).
	if err := migrateBackfillGrants(context.Background(), client); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: backfill grants: %w", err)
	}
	// Rename the "backlog"/"concept" pipeline stages to "ready"/"backlog" across
	// every table that stores a stage name. Must run after ent auto-migrate
	// (tasks, stage_runs, task_schedules all exist) and is safe to run on every
	// boot only because it records a one-shot marker — the rename itself is not
	// idempotent, see migrateRenameStages.
	if err := migrateRenameStages(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: rename stages: %w", err)
	}
	// Every routine that existed before run modes fired a pipeline task —
	// "job" only became a choice once this column shipped. Must run after
	// ent auto-migrate (task_schedules.run_mode exists) and is safe on every
	// boot only because it records a one-shot marker, same as migrateRenameStages.
	if err := migrateRoutineRunModes(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: routine run modes: %w", err)
	}
	if err := runRawMigrations(sqlDB); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("db: raw migrations: %w", err)
	}
	writeClient := client
	if path != ":memory:" {
		// A second pool onto the same file, opened after migrations so it
		// sees the finished schema. _txlock=immediate is this driver's only
		// way to select BEGIN IMMEDIATE — it is a per-connection setting, not
		// a per-transaction option, hence the separate pool. See WriteClient's
		// doc comment and repo.WithWriteTx.
		writeSQLDB, err := sql.Open("sqlite", dsn+"&_txlock=immediate")
		if err != nil {
			_ = client.Close()
			return nil, fmt.Errorf("db: open write pool for %q: %w", path, err)
		}
		writeClient = ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, writeSQLDB)))
	}
	return &DBBundle{Client: client, DB: sqlDB, WriteClient: WriteClient{Client: writeClient}}, nil
}

// runRawMigrations creates tables and FTS5 virtual tables that are not managed
// by ent. Each statement is executed individually — SQLite does not support
// multi-statement Exec calls.
func runRawMigrations(db *sql.DB) error {
	// Always drop and recreate the FTS sync triggers so that schema changes to
	// trigger bodies take effect on existing databases. CREATE TRIGGER IF NOT EXISTS
	// is a no-op when the trigger already exists, so an explicit DROP is required.
	for _, stmt := range []string{
		`DROP TRIGGER IF EXISTS tasks_ai`,
		`DROP TRIGGER IF EXISTS tasks_au`,
		`DROP TRIGGER IF EXISTS tasks_ad`,
		`DROP TRIGGER IF EXISTS memory_entries_ai`,
		`DROP TRIGGER IF EXISTS memory_entries_au`,
		`DROP TRIGGER IF EXISTS memory_entries_ad`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("raw migration (drop trigger): %w\nstatement: %s", err, stmt)
		}
	}

	stmts := []string{
		// notification_config: key-value store for VAPID keys etc.
		`CREATE TABLE IF NOT EXISTS notification_config (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,

		// push_subscriptions: Web Push browser subscriptions.
		`CREATE TABLE IF NOT EXISTS push_subscriptions (
			id TEXT PRIMARY KEY,
			endpoint TEXT UNIQUE NOT NULL,
			p256dh TEXT NOT NULL,
			auth TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,

		// FTS5 virtual table for full-text search over tasks.
		`CREATE VIRTUAL TABLE IF NOT EXISTS task_fts USING fts5(
			task_id UNINDEXED,
			title,
			description
		)`,

		// Sync trigger: INSERT on tasks.
		`CREATE TRIGGER IF NOT EXISTS tasks_ai AFTER INSERT ON tasks BEGIN
			INSERT INTO task_fts(rowid, task_id, title, description)
			VALUES (new.rowid, new.id, new.title, COALESCE(new.description, ''));
		END`,

		// Sync trigger: UPDATE on tasks (delete old index entry, insert new one).
		// task_fts is a regular (content-owning) FTS5 table, so plain DELETE by
		// rowid is required; the "INSERT INTO ft(ft, rowid) VALUES('delete', ...)"
		// form is only valid for contentless FTS5 tables (content='').
		`CREATE TRIGGER IF NOT EXISTS tasks_au AFTER UPDATE ON tasks BEGIN
			DELETE FROM task_fts WHERE rowid = old.rowid;
			INSERT INTO task_fts(rowid, task_id, title, description)
			VALUES (new.rowid, new.id, new.title, COALESCE(new.description, ''));
		END`,

		// Sync trigger: DELETE on tasks.
		`CREATE TRIGGER IF NOT EXISTS tasks_ad AFTER DELETE ON tasks BEGIN
			DELETE FROM task_fts WHERE rowid = old.rowid;
		END`,

		// FTS5 virtual table for full-text search over memory entries.
		`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
			entry_id UNINDEXED,
			summary,
			content
		)`,

		// Sync trigger: INSERT on memory_entries.
		`CREATE TRIGGER IF NOT EXISTS memory_entries_ai AFTER INSERT ON memory_entries BEGIN
			INSERT INTO memory_fts(rowid, entry_id, summary, content)
			VALUES (new.rowid, new.id, new.summary, new.content);
		END`,

		// Sync trigger: UPDATE on memory_entries (delete old index entry, insert new one).
		// memory_fts is a regular (content-owning) FTS5 table like task_fts, so plain
		// DELETE by rowid is required; the "INSERT INTO ft(ft, rowid) VALUES('delete', ...)"
		// form is only valid for contentless FTS5 tables (content='').
		`CREATE TRIGGER IF NOT EXISTS memory_entries_au AFTER UPDATE ON memory_entries BEGIN
			DELETE FROM memory_fts WHERE rowid = old.rowid;
			INSERT INTO memory_fts(rowid, entry_id, summary, content)
			VALUES (new.rowid, new.id, new.summary, new.content);
		END`,

		// Sync trigger: DELETE on memory_entries. No repository method deletes
		// a memory_entry row today (MemoryRepo only supersedes/expires them,
		// and DeleteSpace was removed as dead code), so this trigger is
		// unreached in practice. It stays: it is correct at the database
		// layer, and a future deletion path will need it to keep memory_fts
		// from accumulating rows for entries that no longer exist. Proven
		// directly against the ent client in client_test.go, bypassing the
		// (currently nonexistent) repository layer.
		`CREATE TRIGGER IF NOT EXISTS memory_entries_ad AFTER DELETE ON memory_entries BEGIN
			DELETE FROM memory_fts WHERE rowid = old.rowid;
		END`,

		// workflow_patterns: top ngrams discovered from JSONL session files.
		`CREATE TABLE IF NOT EXISTS workflow_patterns (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tools TEXT NOT NULL UNIQUE,
			frequency INTEGER NOT NULL DEFAULT 1,
			last_seen_at TEXT NOT NULL
		)`,

		// api_keys.name no longer requires uniqueness — names are human labels and
		// the token hash (key_hash) is the real unique credential identifier.
		// This index was created by the original ent schema; drop it idempotently
		// so existing databases are migrated without a table rebuild.
		`DROP INDEX IF EXISTS api_keys_name_key`,

		// audit_logs table is superseded by audit_events (issue #102). The copy
		// migration runs before this statement; dropping here ensures no further
		// writes can land in the legacy table.
		`DROP TABLE IF EXISTS audit_logs`,
	}

	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("raw migration failed: %w\nstatement: %s", err, stmt)
		}
	}
	return nil
}

// backfillTaskRank assigns a rank to every task whose rank is still NULL, seeding
// it from created_at (as microseconds) so the column's existing order is preserved
// and new midpoint inserts have room to subdivide. Idempotent: a second run finds
// no NULL-rank rows and does nothing.
func backfillTaskRank(ctx context.Context, client *ent.Client) error {
	tasks, err := client.Task.Query().Where(task.RankIsNil()).All(ctx)
	if err != nil {
		return fmt.Errorf("query unranked tasks: %w", err)
	}
	if len(tasks) == 0 {
		return nil
	}
	slog.Warn("migration: backfilling task.rank from created_at", "count", len(tasks))
	for _, t := range tasks {
		seed := float64(t.CreatedAt.UnixMicro())
		if _, err := client.Task.UpdateOneID(t.ID).SetRank(seed).Save(ctx); err != nil {
			return fmt.Errorf("backfill rank for task %q: %w", t.ID, err)
		}
	}
	return nil
}

// migrateDropBareWebFetchGrants removes task_permissions rows where tool='WebFetch'
// and pattern IS NULL. Such rows were created before F-SEC-004 enforcement was added
// (PR #86). They are silently ignored at spawn time — making them visible here via
// slog.Warn lets operators know how many legacy grants were cleaned up.
// Idempotent: harmless on a fresh database or after a prior run.
func migrateDropBareWebFetchGrants(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM task_permissions WHERE tool = 'WebFetch' AND pattern IS NULL`,
	).Scan(&count); err != nil {
		return fmt.Errorf("count bare-WebFetch grants: %w", err)
	}
	if count > 0 {
		slog.Warn("migration: removing bare-WebFetch task_permissions grants", "count", count)
		if _, err := db.Exec(
			`DELETE FROM task_permissions WHERE tool = 'WebFetch' AND pattern IS NULL`,
		); err != nil {
			return fmt.Errorf("delete bare-WebFetch grants: %w", err)
		}
	}
	return nil
}

// migrateNormalizeApprovedOutcome rewrites the legacy permission_request outcome
// "approved" to "granted". ApproveAllPending wrote "approved" while every other
// resolver wrote "granted", and the ACP gate authorizes on "granted" alone — so a
// request approved through that path was read back as a refusal. Historical rows
// carry the same ambiguity, hence the repair.
// Idempotent: harmless on a fresh database or after a prior run.
func migrateNormalizeApprovedOutcome(db *sql.DB) error {
	var count int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM permission_requests WHERE outcome = 'approved'`,
	).Scan(&count); err != nil {
		return fmt.Errorf("count approved outcomes: %w", err)
	}
	if count > 0 {
		slog.Warn("migration: normalizing legacy \"approved\" permission_request outcomes to \"granted\"", "count", count)
		if _, err := db.Exec(
			`UPDATE permission_requests SET outcome = 'granted' WHERE outcome = 'approved'`,
		); err != nil {
			return fmt.Errorf("normalize approved outcomes: %w", err)
		}
	}
	return nil
}

// migrateRenameStages rewrites the stored "backlog"/"concept" stage names to
// "ready"/"backlog" in every table that carries a stage column. Rename order
// is collision-safe: "backlog"→"ready" runs first, then "concept"→"backlog",
// so a row already named "backlog" is never re-caught by the second rename.
// It runs exactly once, and it has to: after the first pass the rows that were
// "concept" are named "backlog", which the first rename would catch on a second
// pass and push to "ready" — the stage the scheduler picks up. Every task parked
// in the refinement holding pen would start running by itself after a restart.
// The data cannot answer "did this already run" either: a database that happens
// to hold no "concept" row is indistinguishable from a migrated one. Hence the
// marker row.
func migrateRenameStages(db *sql.DB) error {
	const marker = "rename-stages-concept-backlog"
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS applied_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		return fmt.Errorf("create applied_migrations: %w", err)
	}
	var applied int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM applied_migrations WHERE name = ?`, marker,
	).Scan(&applied); err != nil {
		return fmt.Errorf("check marker %q: %w", marker, err)
	}
	if applied > 0 {
		return nil
	}

	renames := []struct{ table, column, from, to string }{
		{"tasks", "current_stage", "backlog", "ready"},
		{"tasks", "current_stage", "concept", "backlog"},
		{"stage_runs", "stage", "backlog", "ready"},
		{"stage_runs", "stage", "concept", "backlog"},
	}
	for _, r := range renames {
		var count int
		if err := db.QueryRow(
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE %s = ?`, r.table, r.column), r.from,
		).Scan(&count); err != nil {
			return fmt.Errorf("count %s.%s=%q: %w", r.table, r.column, r.from, err)
		}
		if count > 0 {
			slog.Warn("migration: renaming stage", "table", r.table, "column", r.column, "from", r.from, "to", r.to, "count", count)
			if _, err := db.Exec(
				fmt.Sprintf(`UPDATE %s SET %s = ? WHERE %s = ?`, r.table, r.column, r.column), r.to, r.from,
			); err != nil {
				return fmt.Errorf("rename %s.%s %q→%q: %w", r.table, r.column, r.from, r.to, err)
			}
		}
	}

	if _, err := db.Exec(
		`INSERT INTO applied_migrations (name, applied_at) VALUES (?, datetime('now'))`, marker,
	); err != nil {
		return fmt.Errorf("record marker %q: %w", marker, err)
	}
	return nil
}

// migrateRoutineRunModes sets every routine that existed before run modes to
// "pipeline", which is what those routines always fired. It runs once: the
// column default is "job", so after the upgrade an existing row and a newly
// created one look the same, and a second pass would turn new job routines
// into pipeline routines.
func migrateRoutineRunModes(db *sql.DB) error {
	const marker = "routine-run-mode-pipeline"
	if _, err := db.Exec(
		`CREATE TABLE IF NOT EXISTS applied_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`,
	); err != nil {
		return fmt.Errorf("create applied_migrations: %w", err)
	}
	var applied int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM applied_migrations WHERE name = ?`, marker,
	).Scan(&applied); err != nil {
		return fmt.Errorf("check marker %q: %w", marker, err)
	}
	if applied > 0 {
		return nil
	}

	if _, err := db.Exec(`UPDATE task_schedules SET run_mode = 'pipeline'`); err != nil {
		return fmt.Errorf("set existing routines to pipeline run mode: %w", err)
	}

	if _, err := db.Exec(
		`INSERT INTO applied_migrations (name, applied_at) VALUES (?, datetime('now'))`, marker,
	); err != nil {
		return fmt.Errorf("record marker %q: %w", marker, err)
	}
	return nil
}

// migrateCopyAuditLogsToAuditEvents copies legacy audit_logs rows into audit_events
// as part of the audit-table consolidation (issue #102). Idempotent — uses a
// NOT EXISTS guard keyed on (task_id, action, timestamp) to avoid duplicates on
// reruns. The actual DROP of audit_logs is handled in runRawMigrations after
// the AuditLog schema is removed from ent.
func migrateCopyAuditLogsToAuditEvents(db *sql.DB) error {
	// audit_logs may not exist (fresh DB after schema removal). Detect first.
	var hasTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='audit_logs'`,
	).Scan(&hasTable); err != nil {
		return fmt.Errorf("check audit_logs table: %w", err)
	}
	if hasTable == 0 {
		return nil // legacy table already dropped
	}
	var pending int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM audit_logs al
		WHERE NOT EXISTS (
			SELECT 1 FROM audit_events ae
			WHERE ae.task_id = al.task_id
			  AND ae.action = al.action
			  AND ae.ts = al.timestamp
		)
	`).Scan(&pending); err != nil {
		return fmt.Errorf("count pending audit_logs: %w", err)
	}
	if pending == 0 {
		return nil
	}
	slog.Warn("migration: copying audit_logs into audit_events", "count", pending)
	if _, err := db.Exec(`
		INSERT INTO audit_events (id, ts, user_id, action, target, metadata, task_id)
		SELECT
			lower(hex(randomblob(16))),
			al.timestamp,
			NULL,
			al.action,
			'task:' || al.task_id,
			json_object('actor', al.actor, 'details', al.details),
			al.task_id
		FROM audit_logs al
		WHERE NOT EXISTS (
			SELECT 1 FROM audit_events ae
			WHERE ae.task_id = al.task_id
			  AND ae.action = al.action
			  AND ae.ts = al.timestamp
		)
	`); err != nil {
		return fmt.Errorf("copy audit_logs: %w", err)
	}
	return nil
}

// Legacy backfill constants. They mirror repo's GrantContext*/GrantMode*
// vocabulary, which package db cannot import: db/repo imports db back.
// insertLegacyGrant validates them through capability, the package that
// owns the vocabulary, so a divergence fails here rather than writing a row
// Decide would drop.
const (
	legacyGrantContextTask    = "task"
	legacyGrantContextProject = "project"
	legacyGrantMode           = "allow"
	legacyGrantedBy           = "migration:legacy"
	legacyGrantNodeID         = "local"
)

// legacyGrant is one candidate row reduced to the grant columns it maps to.
type legacyGrant struct {
	capabilityName string
	contextKind    string
	contextRef     string
	pattern        string
	expiresAt      *time.Time
}

// insertLegacyGrant writes one backfilled grant, refusing what
// capability.Decide would silently drop: an unparseable pattern, an unknown
// context kind or mode, or a missing context ref. The checks are the ones
// repo.GrantRepo.Create applies — package db cannot call it without an
// import cycle, so the rules come from capability, which owns them, rather
// than from a second copy of the list.
func insertLegacyGrant(ctx context.Context, client *ent.Client, in legacyGrant) error {
	if _, err := capability.ParsePattern(in.pattern); err != nil {
		return fmt.Errorf("invalid pattern %q: %w", in.pattern, err)
	}
	if !capability.IsValidMode(legacyGrantMode) {
		return fmt.Errorf("invalid mode %q (valid: %s)", legacyGrantMode, strings.Join(capability.Modes(), ", "))
	}
	if !capability.IsValidContextKind(in.contextKind) {
		return fmt.Errorf("invalid context kind %q (valid: %s)", in.contextKind, strings.Join(capability.ContextKinds(), ", "))
	}
	if in.contextRef == "" {
		return fmt.Errorf("context ref is required for context kind %q", in.contextKind)
	}
	_, err := client.Grant.Create().
		SetID(uuid.New().String()).
		SetCapabilityName(in.capabilityName).
		SetContextKind(in.contextKind).
		SetContextRef(in.contextRef).
		SetPattern(in.pattern).
		SetMode(legacyGrantMode).
		SetNillableExpiresAt(in.expiresAt).
		SetGrantedBy(legacyGrantedBy).
		SetNodeID(legacyGrantNodeID).
		Save(ctx)
	return err
}

// migrateBackfillGrants converts existing task_permissions and
// permission_presets rows into grants, so the capability gate's grant table
// starts from the legacy permission tables it is replacing instead of empty.
//
// task_permissions rows are backfilled only when granted = 1: an ungranted
// row records a pending or denied request, not a decision the gate should
// honour as an allow — ListEffectiveTaskPermissions already treats
// granted = 0 the same way. permission_presets carries no such flag; a
// preset row is the allow decision by definition, so every preset row is
// backfilled unconditionally.
//
// context_kind/context_ref: task_permissions map to ("task", task_id),
// permission_presets map to ("project", project_cwd). mode is always
// "allow". granted_by is "migration:legacy" rather than an empty string —
// granted_by is required, and an empty string would be indistinguishable
// from a bug; the marker says "unknown because it predates identity" out
// loud.
//
// Every candidate row goes through insertLegacyGrant rather than a bulk
// INSERT ... SELECT, so a row that cannot become a working grant (an
// unparseable pattern, an empty context ref) is skipped and logged instead
// of written as a grant capability.Decide can never match. One bad legacy row must not
// abort the migration — the server still needs to boot — so a Create error
// is a slog.Warn and a continue, never a returned error.
//
// Idempotent via an existence check on the grant's identifying columns
// (capability_name, context_kind, context_ref, pattern), mirroring
// migrateCopyAuditLogsToAuditEvents above. Must run after ent auto-migrate,
// once task_permissions, permission_presets, and grants all exist.
func migrateBackfillGrants(ctx context.Context, client *ent.Client) error {
	permRows, err := client.TaskPermission.Query().Where(taskpermission.GrantedEQ(true)).All(ctx)
	if err != nil {
		return fmt.Errorf("query granted task_permissions for backfill: %w", err)
	}
	backfilledPerms := 0
	for _, tp := range permRows {
		pattern := ""
		if tp.Pattern != nil {
			pattern = *tp.Pattern
		}
		exists, err := client.Grant.Query().Where(
			grant.CapabilityNameEQ(tp.Tool),
			grant.ContextKindEQ(legacyGrantContextTask),
			grant.ContextRefEQ(tp.TaskID),
			grant.PatternEQ(pattern),
		).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check existing grant for task_permission %q: %w", tp.ID, err)
		}
		if exists {
			continue
		}
		if err := insertLegacyGrant(ctx, client, legacyGrant{
			capabilityName: tp.Tool,
			contextKind:    legacyGrantContextTask,
			contextRef:     tp.TaskID,
			pattern:        pattern,
			expiresAt:      tp.ExpiresAt,
		}); err != nil {
			slog.Warn("migration: skipping task_permission row that cannot become a valid grant",
				"task_permission_id", tp.ID, "task_id", tp.TaskID, "tool", tp.Tool, "pattern", pattern, "error", err)
			continue
		}
		backfilledPerms++
	}
	if backfilledPerms > 0 {
		slog.Warn("migration: backfilled grants from task_permissions", "count", backfilledPerms)
	}

	presetRows, err := client.PermissionPreset.Query().All(ctx)
	if err != nil {
		return fmt.Errorf("query permission_presets for backfill: %w", err)
	}
	backfilledPresets := 0
	for _, pp := range presetRows {
		pattern := ""
		if pp.Pattern != nil {
			pattern = *pp.Pattern
		}
		exists, err := client.Grant.Query().Where(
			grant.CapabilityNameEQ(pp.Tool),
			grant.ContextKindEQ(legacyGrantContextProject),
			grant.ContextRefEQ(pp.ProjectCwd),
			grant.PatternEQ(pattern),
		).Exist(ctx)
		if err != nil {
			return fmt.Errorf("check existing grant for permission_preset %q: %w", pp.ID, err)
		}
		if exists {
			continue
		}
		if err := insertLegacyGrant(ctx, client, legacyGrant{
			capabilityName: pp.Tool,
			contextKind:    legacyGrantContextProject,
			contextRef:     pp.ProjectCwd,
			pattern:        pattern,
		}); err != nil {
			slog.Warn("migration: skipping permission_preset row that cannot become a valid grant",
				"permission_preset_id", pp.ID, "project_cwd", pp.ProjectCwd, "tool", pp.Tool, "pattern", pattern, "error", err)
			continue
		}
		backfilledPresets++
	}
	if backfilledPresets > 0 {
		slog.Warn("migration: backfilled grants from permission_presets", "count", backfilledPresets)
	}

	return nil
}

// migrateDedupAgentCostTrends removes duplicate rows in agent_cost_trends that share
// the same session_id, keeping the row with the latest recorded_at (highest rowid as
// tie-breaker). Must run before ent auto-migrate adds the UNIQUE index on session_id,
// otherwise the index creation would fail on existing databases. Idempotent: on a fresh
// database the table does not exist yet (no-op). Second run deletes nothing.
func migrateDedupAgentCostTrends(db *sql.DB) error {
	var hasTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='agent_cost_trends'`,
	).Scan(&hasTable); err != nil {
		return fmt.Errorf("check agent_cost_trends table: %w", err)
	}
	if hasTable == 0 {
		return nil // table doesn't exist yet; ent will create it
	}
	if _, err := db.Exec(`
		DELETE FROM agent_cost_trends
		WHERE rowid NOT IN (
			SELECT t2.rowid
			FROM agent_cost_trends t2
			WHERE t2.rowid = (
				SELECT t3.rowid
				FROM agent_cost_trends t3
				WHERE t3.session_id = t2.session_id
				ORDER BY t3.recorded_at DESC, t3.rowid DESC
				LIMIT 1
			)
		)
	`); err != nil {
		return fmt.Errorf("dedup agent_cost_trends: %w", err)
	}
	return nil
}

// migrateRenameGithubLogin renames the users.github_login column to provider_login.
// Runs before ent auto-migrate so ent sees the new name. Idempotent: no-op when
// provider_login already exists (column was already renamed or DB is brand-new).
func migrateRenameGithubLogin(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(users)`)
	if err != nil {
		return fmt.Errorf("PRAGMA table_info: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var hasOld, hasNew bool
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan table_info: %w", err)
		}
		switch name {
		case "github_login":
			hasOld = true
		case "provider_login":
			hasNew = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if hasNew || !hasOld {
		return nil // already migrated or users table doesn't exist yet
	}
	_, err = db.Exec(`ALTER TABLE users RENAME COLUMN github_login TO provider_login`)
	return err
}

// migrateLegacyPipelineConfig rebuilds the legacy (key, value) pipeline_configs
// table into the scoped (id, key, project_id, value) shape ent now expects.
// Older databases created pipeline_configs with key as the primary key and no id
// column; the per-stage-config index change forces a SQLite table rebuild whose
// row-copy fails the NOT NULL id constraint. We pre-empt that by rebuilding the
// table here with id backfilled from key (key was unique, so ids stay unique).
// Idempotent: a no-op once an id column is present, or when the table is absent.
func migrateLegacyPipelineConfig(db *sql.DB) error {
	var hasTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='pipeline_configs'`,
	).Scan(&hasTable); err != nil {
		return fmt.Errorf("check pipeline_configs table: %w", err)
	}
	if hasTable == 0 {
		return nil // fresh database; ent will create the table
	}
	var hasID int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('pipeline_configs') WHERE name='id'`,
	).Scan(&hasID); err != nil {
		return fmt.Errorf("inspect pipeline_configs columns: %w", err)
	}
	if hasID > 0 {
		return nil // already migrated
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	stmts := []string{
		`ALTER TABLE pipeline_configs RENAME TO pipeline_configs_legacy`,
		"CREATE TABLE `pipeline_configs` (`id` text NOT NULL, `key` text NOT NULL, `project_id` text NOT NULL DEFAULT (''), `value` text NOT NULL, PRIMARY KEY (`id`))",
		`INSERT INTO pipeline_configs (id, key, project_id, value) SELECT key, key, '', value FROM pipeline_configs_legacy`,
		`DROP TABLE pipeline_configs_legacy`,
		"CREATE UNIQUE INDEX `pipelineconfig_project_id_key` ON `pipeline_configs` (`project_id`, `key`)",
	}
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("rebuild pipeline_configs (%s): %w", s, err)
		}
	}
	return tx.Commit()
}

// migrateEnsureStageRunSessionIndex pre-creates the stage_run.session_id index
// under the exact name ent generates (stagerun_session_id) before ent
// auto-migrate runs. Without this, ent's diff would find the index missing on
// existing databases and add it via SQLite's 12-step table rebuild, which
// crashes with "NOT NULL constraint failed: stage_runs.id" (PR #207).
// Idempotent via IF NOT EXISTS; on a fresh database the table does not yet
// exist, so the CREATE INDEX simply targets ent's own subsequently-created table.
func migrateEnsureStageRunSessionIndex(db *sql.DB) error {
	var hasTable int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='stage_runs'`,
	).Scan(&hasTable); err != nil {
		return fmt.Errorf("check stage_runs table: %w", err)
	}
	if hasTable == 0 {
		return nil // fresh database; ent will create the table with the index already declared
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS stagerun_session_id ON stage_runs(session_id)`,
	); err != nil {
		return fmt.Errorf("create stage_run session_id index: %w", err)
	}
	return nil
}

// migrateEnsureResourceUniqueIndex pre-creates the resource unique index under
// the exact name ent generates, before ent auto-migrate runs. Without this, a
// later change to the index would make ent's diff add it via SQLite's 12-step
// table rebuild, which crashes on existing databases with
// "NOT NULL constraint failed: resources.id" (PR #207).
// Idempotent via IF NOT EXISTS; on a fresh database the table does not yet
// exist, so the statement is a no-op and ent creates its own index.
func migrateEnsureResourceUniqueIndex(db *sql.DB) error {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'resources'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("probe resources table: %w", err)
	}
	if exists == 0 {
		return nil
	}
	if _, err := db.Exec(
		`CREATE UNIQUE INDEX IF NOT EXISTS resource_kind_scope_kind_scope_ref_slug ` +
			`ON resources (kind, scope_kind, scope_ref, slug)`,
	); err != nil {
		return fmt.Errorf("pre-create resource unique index: %w", err)
	}
	return nil
}

// migrateEnsureGrantIndexes pre-creates the grant table's two named indexes
// under ent's exact generated names before ent auto-migrate runs. The grants
// table is new in this change, so today's fresh databases hit the no-op path
// below and ent creates the table with both indexes already declared. The
// guard exists for the databases that will exist once this ships: a later
// change to either index would otherwise make ent's diff add it via SQLite's
// 12-step table rebuild, which crashes on populated databases with
// "NOT NULL constraint failed" (PR #207) — the same hazard
// migrateEnsureResourceUniqueIndex guards against, applied here to both of
// this table's Indexes() entries rather than just the compound one.
// Idempotent via IF NOT EXISTS.
func migrateEnsureGrantIndexes(db *sql.DB) error {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'grants'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("probe grants table: %w", err)
	}
	if exists == 0 {
		return nil
	}
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS grant_capability_name_context_kind_context_ref ` +
			`ON grants (capability_name, context_kind, context_ref)`,
		`CREATE INDEX IF NOT EXISTS grant_revoked_at ON grants (revoked_at)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("pre-create grant index: %w\nstatement: %s", err, stmt)
		}
	}
	return nil
}

// migrateEnsureGrantUsageIndex pre-creates the grant_usages table's compound
// index under ent's exact generated name before ent auto-migrate runs. The
// grant_usages table is new in this change, so today's fresh databases hit
// the no-op path below and ent creates the table with the index already
// declared. The guard exists for the databases that will exist once this
// ships, exactly like migrateEnsureGrantIndexes above: a brand-new table is
// not exempt from the hazard, because it is a later index change on a
// populated database that triggers SQLite's 12-step table rebuild, which
// crashes on existing databases with "NOT NULL constraint failed" (PR #207).
// Idempotent via IF NOT EXISTS.
func migrateEnsureGrantUsageIndex(db *sql.DB) error {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'grant_usages'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("probe grant_usages table: %w", err)
	}
	if exists == 0 {
		return nil
	}
	if _, err := db.Exec(
		`CREATE INDEX IF NOT EXISTS grantusage_grant_id_used_at ` +
			`ON grant_usages (grant_id, used_at)`,
	); err != nil {
		return fmt.Errorf("pre-create grant_usage index: %w", err)
	}
	return nil
}

// migrateEnsureMemoryEntryIndexes pre-creates the memory_entries table's three
// named indexes under ent's exact generated names before ent auto-migrate
// runs. The memory_entries table is new in this change, so today's fresh
// databases hit the no-op path below and ent creates the table with all three
// indexes already declared. The guard exists for the databases that will
// exist once this ships: a later change to any of these indexes would
// otherwise make ent's diff add it via SQLite's 12-step table rebuild, which
// crashes on populated databases with "NOT NULL constraint failed" (PR #207)
// — the same hazard migrateEnsureGrantIndexes guards against, applied here to
// a new table with only non-unique indexes.
// Idempotent via IF NOT EXISTS.
func migrateEnsureMemoryEntryIndexes(db *sql.DB) error {
	var exists int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'memory_entries'`,
	).Scan(&exists); err != nil {
		return fmt.Errorf("probe memory_entries table: %w", err)
	}
	if exists == 0 {
		return nil
	}
	stmts := []string{
		`CREATE INDEX IF NOT EXISTS memoryentry_space_id_valid_until ` +
			`ON memory_entries (space_id, valid_until)`,
		`CREATE INDEX IF NOT EXISTS memoryentry_space_id_kind ` +
			`ON memory_entries (space_id, kind)`,
		`CREATE INDEX IF NOT EXISTS memoryentry_superseded_by ` +
			`ON memory_entries (superseded_by)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("pre-create memory_entry index: %w\nstatement: %s", err, stmt)
		}
	}
	return nil
}
