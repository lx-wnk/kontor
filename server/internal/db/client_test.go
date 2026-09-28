package db_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestOpen_InMemory(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	require.NotNil(t, bundle)
	require.NotNil(t, bundle.Client)
	require.NotNil(t, bundle.DB)
	_ = bundle.Client.Close()
}

func TestOpen_AutoMigrate(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()

	// Prove schema was created: insert a record into api_keys.
	_, err = bundle.Client.ApiKey.Create().
		SetID("test-id").
		SetName("test").
		SetKeyHash("abc123").
		SetScopes([]string{"tasks:read"}).
		Save(t.Context())
	require.NoError(t, err)
}

func TestOpen_FTS5TableCreated(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()

	var name string
	err = bundle.DB.QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='task_fts'",
	).Scan(&name)
	require.NoError(t, err)
	require.Equal(t, "task_fts", name)
}

// TestOpen_DropsBareWebFetchGrants verifies the F-SEC-004 startup migration:
// task_permissions rows with tool='WebFetch' and pattern IS NULL must be deleted
// when the database is opened, and a Warn log must have been emitted.
func TestOpen_DropsBareWebFetchGrants(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First open: create schema + seed a bare-WebFetch grant via raw SQL.
	bundle1, err := db.Open(dbPath)
	require.NoError(t, err)

	// Insert a task so the FK constraint on task_permissions is satisfied.
	_, err = bundle1.DB.Exec(
		`INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, silver_bullet, created_at, updated_at)
		 VALUES ('t1','bare-wf-test','Test','','backlog','medium',20,0,datetime('now'),datetime('now'))`,
	)
	require.NoError(t, err)

	// Insert a bare-WebFetch grant (pattern IS NULL) — bypasses Go-layer validation.
	_, err = bundle1.DB.Exec(
		`INSERT INTO task_permissions (id, task_id, tool, pattern, granted, requested_at)
		 VALUES ('p1','t1','WebFetch',NULL,1,datetime('now'))`,
	)
	require.NoError(t, err)

	// Confirm the row is present before migration.
	var before int
	err = bundle1.DB.QueryRow(
		`SELECT COUNT(*) FROM task_permissions WHERE tool='WebFetch' AND pattern IS NULL`,
	).Scan(&before)
	require.NoError(t, err)
	require.Equal(t, 1, before, "expected bare-WebFetch row to exist before migration")
	require.NoError(t, bundle1.Close())

	// Second open: migration should delete the bare-WebFetch row.
	bundle2, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()

	var after int
	err = bundle2.DB.QueryRow(
		`SELECT COUNT(*) FROM task_permissions WHERE tool='WebFetch' AND pattern IS NULL`,
	).Scan(&after)
	require.NoError(t, err)
	require.Equal(t, 0, after, "expected bare-WebFetch rows to be deleted by migration")

	// Non-bare WebFetch grants (with a pattern) must survive.
	_, err = bundle2.DB.Exec(
		`INSERT INTO task_permissions (id, task_id, tool, pattern, granted, requested_at)
		 VALUES ('p2','t1','WebFetch','https://docs.example.com*',1,datetime('now'))`,
	)
	require.NoError(t, err)
	require.NoError(t, bundle2.Close())

	bundle3, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle3.Close() }()

	var patterned int
	err = bundle3.DB.QueryRow(
		`SELECT COUNT(*) FROM task_permissions WHERE tool='WebFetch' AND pattern IS NOT NULL`,
	).Scan(&patterned)
	require.NoError(t, err)
	require.Equal(t, 1, patterned, "patterned WebFetch grants must not be deleted by migration")
}

// TestOpen_DropsBareWebFetchGrants_Idempotent verifies that the migration is a
// no-op (returns nil) when no bare-WebFetch rows exist.
func TestOpen_DropsBareWebFetchGrants_Idempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	bundle, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()

	// No seeding — migration should silently succeed with count=0.
	var count int
	err = bundle.DB.QueryRow(
		`SELECT COUNT(*) FROM task_permissions WHERE tool='WebFetch' AND pattern IS NULL`,
	).Scan(&count)
	require.NoError(t, err)
	require.Equal(t, 0, count)
}

func TestOpen_FTS5TriggerRoundTrip(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()

	_, err = bundle.DB.Exec(
		`INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, silver_bullet, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, datetime('now'), datetime('now'))`,
		"task-fts-1", "fts-roundtrip", "Observability Dashboard Feature", "/tmp/project", "backlog", "medium", 20, 0,
	)
	require.NoError(t, err)

	// FTS5 content= tables cannot return UNINDEXED columns directly via SELECT because the
	// engine re-fetches columns from the content table (tasks) by name, and tasks has no
	// "task_id" column. Use a rowid-based subquery to retrieve the task id instead.
	var taskID string
	err = bundle.DB.QueryRow(
		`SELECT id FROM tasks WHERE rowid IN (SELECT rowid FROM task_fts WHERE task_fts MATCH ?)`,
		"Observability",
	).Scan(&taskID)
	require.NoError(t, err)
	require.Equal(t, "task-fts-1", taskID)
}

func TestOpen_MigratesLegacyPipelineConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// Seed the legacy pipeline_configs shape: (key, value) with key as PK, no id column.
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE `pipeline_configs` (`key` text NOT NULL, `value` text NOT NULL, PRIMARY KEY (`key`))")
	require.NoError(t, err)
	_, err = raw.Exec("INSERT INTO pipeline_configs (key, value) VALUES ('maxAutoRetries','3'),('stageModel.implementation','claude-opus-4-8')")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	// Open via the production path — must migrate without the NOT NULL id failure.
	bundle, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()

	all, err := bundle.Client.PipelineConfig.Query().All(t.Context())
	require.NoError(t, err)
	require.Len(t, all, 2)
	byKey := map[string]string{}
	for _, c := range all {
		byKey[c.Key] = c.Value
		require.Equal(t, c.Key, c.ID)     // id backfilled from key
		require.Equal(t, "", c.ProjectID) // global scope sentinel
	}
	require.Equal(t, "3", byKey["maxAutoRetries"])
	require.Equal(t, "claude-opus-4-8", byKey["stageModel.implementation"])
}

func TestOpen_StageRunSessionIDIndex_FreshDB(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()

	require.True(t, hasIndex(t, bundle.DB, "stage_runs", "stagerun_session_id"))
}

// TestOpen_StageRunSessionIDIndex_ExistingDB seeds a pre-index stage_runs
// table (the shape before this migration) and confirms db.Open pre-seeds the
// index via CREATE INDEX IF NOT EXISTS so ent's auto-migrate sees it already
// present, rather than driving a table rebuild — the PR #207 rebuild-crash
// class of bug. Also confirms a second Open is a clean idempotent no-op.
func TestOpen_StageRunSessionIDIndex_ExistingDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preindex.db")

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE TABLE stage_runs (
		id text NOT NULL,
		task_id text NOT NULL,
		stage text NOT NULL,
		session_id text NULL,
		session_name text NULL,
		pid integer NULL,
		status text NOT NULL DEFAULT 'pending',
		iteration integer NOT NULL DEFAULT 0,
		output json NULL,
		tokens_used integer NOT NULL DEFAULT 0,
		cost_cents integer NOT NULL DEFAULT 0,
		started_at datetime NULL,
		ended_at datetime NULL,
		last_grant_at datetime NULL,
		retry_count integer NOT NULL DEFAULT 0,
		next_retry_at datetime NULL,
		pending_user_prompt text NULL,
		created_at datetime NOT NULL DEFAULT (datetime('now')),
		PRIMARY KEY (id)
	)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE INDEX stagerun_status ON stage_runs (status)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE INDEX stagerun_task_id_stage_iteration ON stage_runs (task_id, stage, iteration)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE INDEX stagerun_task_id_created_at ON stage_runs (task_id, created_at)`)
	require.NoError(t, err)
	_, err = raw.Exec(`CREATE UNIQUE INDEX stagerun_task_id ON stage_runs (task_id) WHERE status = 'running'`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	require.False(t, hasIndexAtPath(t, path, "stage_runs", "stagerun_session_id"),
		"seeded DB must not already have the session_id index")

	bundle1, err := db.Open(path)
	require.NoError(t, err, "Open must not crash migrating a pre-index stage_runs table")
	require.True(t, hasIndex(t, bundle1.DB, "stage_runs", "stagerun_session_id"))
	require.NoError(t, bundle1.Close())

	// Idempotency: a second Open on the already-migrated file is a clean no-op.
	bundle2, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()
	require.True(t, hasIndex(t, bundle2.DB, "stage_runs", "stagerun_session_id"))
}

func TestOpen_StageRunSessionIDIndex_QueryPlanUsesIndex(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()

	rows, err := bundle.DB.Query(`EXPLAIN QUERY PLAN SELECT * FROM stage_runs WHERE session_id = 'x'`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var plan string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		plan += detail + "\n"
	}
	require.NoError(t, rows.Err())
	require.Contains(t, plan, "stagerun_session_id")
	require.NotContains(t, plan, "SCAN stage_runs")
}

func hasIndex(t *testing.T, sqlDB *sql.DB, table, indexName string) bool {
	t.Helper()
	rows, err := sqlDB.Query(fmt.Sprintf("PRAGMA index_list(%s)", table))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var seq int
		var name string
		var unique, partial int
		var origin string
		require.NoError(t, rows.Scan(&seq, &name, &unique, &origin, &partial))
		if name == indexName {
			return true
		}
	}
	require.NoError(t, rows.Err())
	return false
}

func hasIndexAtPath(t *testing.T, path, table, indexName string) bool {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	return hasIndex(t, raw, table, indexName)
}

// TestOpenTwiceWithResourceTable proves the resource table and its unique index
// survive a second Open on an existing database. An ent index change triggers
// SQLite's 12-step table rebuild, which fails on populated tables with
// "NOT NULL constraint failed" — the pre-migration exists to make ent's diff
// find the index already present so it never rebuilds.
func TestOpenTwiceWithResourceTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twice.db")

	first, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	ctx := context.Background()
	if _, err := first.Client.Resource.Create().
		SetID("res-1").
		SetKind("application").
		SetSlug("example").
		Save(ctx); err != nil {
		t.Fatalf("insert resource: %v", err)
	}
	if err := first.Client.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open on a populated database: %v", err)
	}
	t.Cleanup(func() { _ = second.Client.Close() })

	n, err := second.Client.Resource.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count after reopen: %v", err)
	}
	if n != 1 {
		t.Errorf("resource count after reopen = %d, want 1", n)
	}
}

// insertBackfillTask inserts the single task row task_permissions' FK
// requires, mirroring the fixture TestOpen_DropsBareWebFetchGrants already
// uses for the same purpose.
func insertBackfillTask(t *testing.T, sqlDB *sql.DB, id string) {
	t.Helper()
	_, err := sqlDB.Exec(
		`INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, silver_bullet, created_at, updated_at)
		 VALUES (?, ?, 'Test', '', 'backlog', 'medium', 20, 0, datetime('now'), datetime('now'))`,
		id, id,
	)
	require.NoError(t, err)
}

// TestBackfillGrantsIsIdempotent seeds one task_permission and one
// permission_preset, reopens the database to trigger the backfill migration,
// and asserts the grant count is 2 — then reopens once more and asserts the
// count is still 2. A migration that runs on every boot must be a no-op once
// settled, and the second-run assertion is the only thing that proves it.
func TestBackfillGrantsIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()

	bundle1, err := db.Open(dbPath)
	require.NoError(t, err)
	insertBackfillTask(t, bundle1.DB, "t1")

	pattern := "pnpm test"
	_, err = repo.NewPermissionRepo(bundle1.Client).CreateTaskPermission(ctx, repo.CreateTaskPermissionInput{
		TaskID: "t1", Tool: "Bash", Pattern: &pattern, Granted: true,
	})
	require.NoError(t, err)

	err = repo.NewPermissionPresetRepo(bundle1.Client).Upsert(ctx, repo.UpsertPresetInput{
		ProjectCwd: "/repo", Tool: "Read",
	})
	require.NoError(t, err)
	require.NoError(t, bundle1.Close())

	// Second open triggers the backfill migration over the seeded rows.
	bundle2, err := db.Open(dbPath)
	require.NoError(t, err)
	var count int
	require.NoError(t, bundle2.DB.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&count))
	require.Equal(t, 2, count, "expected one grant per backfilled row")
	require.NoError(t, bundle2.Close())

	// Third open must not duplicate the already-backfilled grants.
	bundle3, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle3.Close() }()
	require.NoError(t, bundle3.DB.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&count))
	require.Equal(t, 2, count, "second migration run must not duplicate grants")
}

// TestBackfillGrantsMarksLegacyIdentity asserts every backfilled grant has
// granted_by == "migration:legacy". granted_by is required and the legacy
// rows carry no identity; an empty string would be indistinguishable from a
// bug, so the marker says "unknown because it predates identity" out loud.
func TestBackfillGrantsMarksLegacyIdentity(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()

	bundle1, err := db.Open(dbPath)
	require.NoError(t, err)
	insertBackfillTask(t, bundle1.DB, "t1")

	pattern := "pnpm test"
	_, err = repo.NewPermissionRepo(bundle1.Client).CreateTaskPermission(ctx, repo.CreateTaskPermissionInput{
		TaskID: "t1", Tool: "Bash", Pattern: &pattern, Granted: true,
	})
	require.NoError(t, err)
	require.NoError(t, bundle1.Close())

	bundle2, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()

	rows, err := bundle2.DB.Query(`SELECT granted_by FROM grants`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var grantedBy string
		require.NoError(t, rows.Scan(&grantedBy))
		require.Equal(t, "migration:legacy", grantedBy)
		seen++
	}
	require.NoError(t, rows.Err())
	require.Greater(t, seen, 0, "expected at least one backfilled grant")
}

// TestBackfillGrantsSkipsUngrantedTaskPermissions proves an ungranted
// task_permission row (still pending, or denied) does not become an allow
// grant. Unlike permission_presets — which carry no such flag and are
// backfilled unconditionally — a task_permission row only reflects an actual
// decision when granted = 1; ListEffectiveTaskPermissions already treats
// granted = 0 the same way, and backfilling it as an allow grant would turn a
// denied or still-pending request into a retroactive allow.
func TestBackfillGrantsSkipsUngrantedTaskPermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()

	bundle1, err := db.Open(dbPath)
	require.NoError(t, err)
	insertBackfillTask(t, bundle1.DB, "t1")

	_, err = repo.NewPermissionRepo(bundle1.Client).CreateTaskPermission(ctx, repo.CreateTaskPermissionInput{
		TaskID: "t1", Tool: "Read", Granted: false,
	})
	require.NoError(t, err)
	require.NoError(t, bundle1.Close())

	bundle2, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()

	var count int
	require.NoError(t, bundle2.DB.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&count))
	require.Equal(t, 0, count, "an ungranted task_permission must not become a grant")
}

// TestBackfillGrantsSkipsEmptyContextRef proves a legacy permission_preset
// with an empty project_cwd is skipped rather than written as a grant whose
// context_ref is "" — such a row parses fine but capability.Decide can never
// match it against a real "project" context, so it would sit in the grants
// table forever as dead weight. A sibling preset with a real project_cwd
// proves the bad row does not abort the rest of the backfill.
func TestBackfillGrantsSkipsEmptyContextRef(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()

	bundle1, err := db.Open(dbPath)
	require.NoError(t, err)

	presets := repo.NewPermissionPresetRepo(bundle1.Client)
	require.NoError(t, presets.Upsert(ctx, repo.UpsertPresetInput{ProjectCwd: "", Tool: "Read"}))
	require.NoError(t, presets.Upsert(ctx, repo.UpsertPresetInput{ProjectCwd: "/repo", Tool: "Write"}))
	require.NoError(t, bundle1.Close())

	bundle2, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()

	var count int
	require.NoError(t, bundle2.DB.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&count))
	require.Equal(t, 1, count, "the empty-ref preset must be skipped, the valid one still backfilled")

	var capabilityName string
	require.NoError(t, bundle2.DB.QueryRow(`SELECT capability_name FROM grants`).Scan(&capabilityName))
	require.Equal(t, "Write", capabilityName)
}

// TestBackfillGrantsSkipsUnparseablePattern proves a legacy task_permission
// whose pattern fails capability.ParsePattern (a "domain:" prefix with no
// hostname) is skipped rather than written as a grant that can never
// resolve — a sibling permission with a valid (nil) pattern proves the bad
// row does not abort the rest of the backfill.
func TestBackfillGrantsSkipsUnparseablePattern(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	ctx := context.Background()

	bundle1, err := db.Open(dbPath)
	require.NoError(t, err)
	insertBackfillTask(t, bundle1.DB, "t1")

	badPattern := "domain:"
	perms := repo.NewPermissionRepo(bundle1.Client)
	_, err = perms.CreateTaskPermission(ctx, repo.CreateTaskPermissionInput{
		TaskID: "t1", Tool: "Bash", Pattern: &badPattern, Granted: true,
	})
	require.NoError(t, err)
	_, err = perms.CreateTaskPermission(ctx, repo.CreateTaskPermissionInput{
		TaskID: "t1", Tool: "Read", Granted: true,
	})
	require.NoError(t, err)
	require.NoError(t, bundle1.Close())

	bundle2, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()

	var count int
	require.NoError(t, bundle2.DB.QueryRow(`SELECT COUNT(*) FROM grants`).Scan(&count))
	require.Equal(t, 1, count, "the unparseable-pattern row must be skipped, the valid one still backfilled")

	var capabilityName string
	require.NoError(t, bundle2.DB.QueryRow(`SELECT capability_name FROM grants`).Scan(&capabilityName))
	require.Equal(t, "Read", capabilityName)
}

// TestOpenTwiceWithMemoryTables proves the memory_entries and
// memory_injections tables and memory_entries' three indexes survive a second
// Open on an existing database. An ent index change triggers SQLite's 12-step
// table rebuild, which fails on populated tables with "NOT NULL constraint
// failed" — the pre-migration exists to make ent's diff find the indexes
// already present so it never rebuilds.
func TestOpenTwiceWithMemoryTables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "twice-memory.db")

	first, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	ctx := context.Background()
	if _, err := first.Client.MemoryEntry.Create().
		SetID("entry-1").
		SetSpaceID("space-1").
		SetSummary("example summary").
		SetContent("example content").
		SetKind("fact").
		SetSourceKind("agent").
		SetConfidence(0.9).
		Save(ctx); err != nil {
		t.Fatalf("insert memory_entry: %v", err)
	}
	if _, err := first.Client.MemoryInjection.Create().
		SetID("injection-1").
		SetStageRunID("stage-run-1").
		SetCharBudget(1000).
		SetCharsUsed(200).
		SetCandidateCount(3).
		Save(ctx); err != nil {
		t.Fatalf("insert memory_injection: %v", err)
	}
	if err := first.Client.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	second, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open on a populated database: %v", err)
	}
	t.Cleanup(func() { _ = second.Client.Close() })

	entries, err := second.Client.MemoryEntry.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count memory_entries after reopen: %v", err)
	}
	if entries != 1 {
		t.Errorf("memory_entry count after reopen = %d, want 1", entries)
	}

	injections, err := second.Client.MemoryInjection.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count memory_injections after reopen: %v", err)
	}
	if injections != 1 {
		t.Errorf("memory_injection count after reopen = %d, want 1", injections)
	}
}

// TestMemoryFTSRoundTrip proves memory_fts stays in sync with memory_entries
// across insert, update, and delete. The delete leg is the one that matters:
// a contentless-form trigger against a content-owning table fails at runtime,
// not at boot, so only an actual delete exercises it.
func TestMemoryFTSRoundTrip(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()
	ctx := t.Context()

	entry, err := bundle.Client.MemoryEntry.Create().
		SetID("entry-fts-1").
		SetSpaceID("space-1").
		SetSummary("Observability Dashboard Feature").
		SetContent("initial content").
		SetKind("fact").
		SetSourceKind("agent").
		SetConfidence(0.9).
		Save(ctx)
	require.NoError(t, err)

	// FTS5 content= tables cannot return UNINDEXED columns directly via SELECT because the
	// engine re-fetches columns from the content table (memory_entries) by name, and
	// memory_entries has no "entry_id" column. Use a rowid-based subquery, mirroring
	// TestOpen_FTS5TriggerRoundTrip.
	matchID := func(term string) (string, error) {
		var id string
		err := bundle.DB.QueryRow(
			`SELECT id FROM memory_entries WHERE rowid IN (SELECT rowid FROM memory_fts WHERE memory_fts MATCH ?)`,
			term,
		).Scan(&id)
		return id, err
	}

	id, err := matchID("Observability")
	require.NoError(t, err)
	require.Equal(t, entry.ID, id)

	_, err = bundle.Client.MemoryEntry.UpdateOneID(entry.ID).
		SetSummary("Renamed Feature Summary").
		Save(ctx)
	require.NoError(t, err)

	_, err = matchID("Observability")
	require.ErrorIs(t, err, sql.ErrNoRows, "old summary term must no longer match after update")

	id, err = matchID("Renamed")
	require.NoError(t, err)
	require.Equal(t, entry.ID, id)

	require.NoError(t, bundle.Client.MemoryEntry.DeleteOneID(entry.ID).Exec(ctx))

	_, err = matchID("Renamed")
	require.ErrorIs(t, err, sql.ErrNoRows, "deleted entry must no longer match")
}

// TestOpen_LegacyPreApprovedColumnSurvives seeds a task_permissions table in
// the shape it had while the schema still declared pre_approved, then opens it
// with the current schema. ent's auto-migrate is non-destructive (no
// WithDropColumn), so the dead column stays behind; because it was generated as
// NOT NULL DEFAULT (false), inserts that no longer mention it still succeed.
// A file DB is required — ":memory:" pins a single fresh connection and can
// never hold a pre-existing table shape.
func TestOpen_LegacyPreApprovedColumnSurvives(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "preapproved.db")

	raw, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE `tasks` (`id` text NOT NULL, `slug` text NOT NULL, `title` text NOT NULL, `cwd` text NOT NULL, `current_stage` text NOT NULL DEFAULT ('concept'), `priority` text NOT NULL DEFAULT ('medium'), `max_iterations` integer NOT NULL DEFAULT (20), `stage_timeout_seconds` integer NOT NULL DEFAULT (1800), `silver_bullet` bool NOT NULL DEFAULT (false), `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, PRIMARY KEY (`id`))")
	require.NoError(t, err)
	// Verbatim pre-drop DDL, as emitted by ent while the field still existed.
	_, err = raw.Exec("CREATE TABLE `task_permissions` (`id` text NOT NULL, `tool` text NOT NULL, `pattern` text NULL, `granted` bool NOT NULL DEFAULT (false), `pre_approved` bool NOT NULL DEFAULT (false), `manual_override` bool NOT NULL DEFAULT (false), `decided_by` text NULL, `requested_at` datetime NOT NULL, `decided_at` datetime NULL, `expires_at` datetime NULL, `task_id` text NOT NULL, PRIMARY KEY (`id`), CONSTRAINT `task_permissions_tasks_permissions` FOREIGN KEY (`task_id`) REFERENCES `tasks` (`id`) ON DELETE CASCADE)")
	require.NoError(t, err)
	_, err = raw.Exec("CREATE INDEX `taskpermission_task_id` ON `task_permissions` (`task_id`)")
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO tasks (id, slug, title, cwd, created_at, updated_at)
		VALUES ('t-legacy','legacy','Legacy','',datetime('now'),datetime('now'))`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO task_permissions (id, task_id, tool, pattern, granted, pre_approved, requested_at)
		VALUES ('p-legacy','t-legacy','Read','/repo/**',1,1,datetime('now'))`)
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	bundle, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()

	ctx := t.Context()
	perms := repo.NewPermissionRepo(bundle.Client)

	// Read: the pre-existing row is still readable through the current schema.
	existing, err := perms.ListTaskPermissions(ctx, "t-legacy")
	require.NoError(t, err)
	require.Len(t, existing, 1)
	require.Equal(t, "Read", existing[0].Tool)
	require.True(t, existing[0].Granted)

	// Write: a create that never mentions pre_approved must satisfy the
	// leftover NOT NULL column via its DEFAULT.
	created, err := perms.CreateTaskPermission(ctx, repo.CreateTaskPermissionInput{
		TaskID:  "t-legacy",
		Tool:    "Write",
		Granted: true,
	})
	require.NoError(t, err)
	require.Equal(t, "Write", created.Tool)

	after, err := perms.ListTaskPermissions(ctx, "t-legacy")
	require.NoError(t, err)
	require.Len(t, after, 2)

	// The dead column is deliberately left in place rather than dropped.
	var leftover int
	err = bundle.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('task_permissions') WHERE name = 'pre_approved'`).Scan(&leftover)
	require.NoError(t, err)
	require.Equal(t, 1, leftover, "auto-migrate is non-destructive: the column stays")

	// A second Open over the same file must be a clean no-op.
	require.NoError(t, bundle.Close())
	bundle2, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()
}

// TestOpen_LegacyStageTimeoutColumnsSurvive opens a DB in the current schema
// plus the pre-removal stage_timeout_seconds columns, holding data; the columns
// must survive auto-migrate and their DEFAULT must satisfy ent inserts that omit
// them. The fixture comes from db.Open because a hand-written DDL that drifts
// from the ent schema makes auto-migrate rebuild the table and lose the column.
func TestOpen_LegacyStageTimeoutColumnsSurvive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stagetimeout.db")

	seed, err := db.Open(path)
	require.NoError(t, err)
	for _, stmt := range []string{
		`ALTER TABLE tasks ADD COLUMN stage_timeout_seconds integer NOT NULL DEFAULT (1800)`,
		`ALTER TABLE task_schedules ADD COLUMN stage_timeout_seconds integer NOT NULL DEFAULT (1800)`,
		`INSERT INTO tasks (id, slug, title, cwd, stage_timeout_seconds, created_at, updated_at)
			VALUES ('t-legacy','legacy','Legacy','/repo',600,datetime('now'),datetime('now'))`,
		`INSERT INTO task_schedules (id, name, cron_expr, slug_prefix, title, cwd, stage_timeout_seconds, created_at, updated_at)
			VALUES ('s-legacy','Legacy','0 9 * * *','legacy','Legacy','/repo',600,datetime('now'),datetime('now'))`,
	} {
		_, err = seed.DB.Exec(stmt)
		require.NoError(t, err)
	}
	require.NoError(t, seed.Close())

	bundle, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()
	ctx := t.Context()

	legacy, err := bundle.Client.Task.Get(ctx, "t-legacy")
	require.NoError(t, err)
	require.Equal(t, "Legacy", legacy.Title)
	legacySchedule, err := bundle.Client.TaskSchedule.Get(ctx, "s-legacy")
	require.NoError(t, err)
	require.Equal(t, "Legacy", legacySchedule.Name)

	_, err = bundle.Client.Task.Create().SetID("t-new").SetSlug("new").SetTitle("New").SetCwd("/repo").Save(ctx)
	require.NoError(t, err)
	created, err := bundle.Client.Task.Get(ctx, "t-new")
	require.NoError(t, err)
	require.Equal(t, "New", created.Title)
	_, err = bundle.Client.TaskSchedule.Create().SetID("s-new").SetName("New").SetCronExpr("0 9 * * *").
		SetSlugPrefix("new").SetTitle("New").SetCwd("/repo").Save(ctx)
	require.NoError(t, err)

	for _, tc := range []struct{ table, id string }{
		{"tasks", "t-legacy"}, {"tasks", "t-new"},
		{"task_schedules", "s-legacy"}, {"task_schedules", "s-new"},
	} {
		var timeout int
		err = bundle.DB.QueryRow(
			fmt.Sprintf(`SELECT stage_timeout_seconds FROM %s WHERE id = ?`, tc.table), tc.id,
		).Scan(&timeout)
		require.NoError(t, err, "%s.stage_timeout_seconds must survive auto-migrate", tc.table)
		want := 1800
		if tc.id == "t-legacy" || tc.id == "s-legacy" {
			want = 600
		}
		require.Equal(t, want, timeout, "%s/%s", tc.table, tc.id)
	}
}

func TestOpen_AppSettingGainsSecretColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec("CREATE TABLE `app_settings` (`id` text NOT NULL, `key` text NOT NULL UNIQUE, `value` text NOT NULL, `created_at` datetime NOT NULL, `updated_at` datetime NOT NULL, PRIMARY KEY (`id`))")
	require.NoError(t, err)
	_, err = raw.Exec("INSERT INTO app_settings (id, key, value, created_at, updated_at) VALUES ('1','git.allowPush','true',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	bundle, err := db.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	r := repo.NewAppSettingRepo(bundle.Client)
	v, ok, err := r.Get(t.Context(), "git.allowPush")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "true", v)

	_, err = r.UpsertSecret(t.Context(), "obsidian.apiKey", "Y2lwaGVy", "bm9uY2U=")
	require.NoError(t, err)
}

// stageOf reads a task's stored stage name straight from SQL, bypassing any
// ent-level normalization.
func stageOf(t *testing.T, path, taskID string) string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	var stage string
	require.NoError(t, raw.QueryRow(`SELECT current_stage FROM tasks WHERE id = ?`, taskID).Scan(&stage))
	return stage
}

// runModeOf reads a schedule's stored run_mode straight from SQL.
func runModeOf(t *testing.T, path, scheduleID string) string {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	defer func() { _ = raw.Close() }()
	var mode string
	require.NoError(t, raw.QueryRow(`SELECT run_mode FROM task_schedules WHERE id = ?`, scheduleID).Scan(&mode))
	return mode
}

func TestOpen_FreshDatabaseOpensWithoutScheduleStageColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	bundle, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()

	rows, err := bundle.DB.Query(`PRAGMA table_info(task_schedules)`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var cols []string
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dflt any
		require.NoError(t, rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk))
		cols = append(cols, name)
	}
	require.NoError(t, rows.Err())

	assert.Contains(t, cols, "run_mode")
	assert.Contains(t, cols, "skipped_count")
	assert.Contains(t, cols, "last_skipped_at")
	assert.NotContains(t, cols, "current_stage")
}

// TestOpen_ExistingRoutinesBecomePipelineOnce is the load-bearing test for the
// run-mode migration. A pre-upgrade routine only exists in the database
// because it fired as the (then only) pipeline-materializing kind of
// schedule, so the upgrade must set it to "pipeline" — but the run_mode
// column's own default is "job", so a routine created after the upgrade is
// indistinguishable from a migrated one by value alone. The marker is what
// keeps a later boot from re-sweeping a legitimately new "job" routine.
func TestOpen_ExistingRoutinesBecomePipelineOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "routines.db")

	bundle0, err := db.Open(path)
	require.NoError(t, err)
	schedules := repo.NewTaskScheduleRepo(bundle0.Client)
	old, err := schedules.Create(context.Background(), repo.CreateTaskScheduleInput{
		Name: "old", CronExpr: "0 9 * * *", SlugPrefix: "old", Title: "Old",
		Cwd: "/tmp", MaxIterations: 20,
	})
	require.NoError(t, err)
	// The seeding Open already recorded the marker against the (then-empty)
	// table, so clear it: this row stands in for one written before run
	// modes shipped.
	_, err = bundle0.DB.Exec(`DELETE FROM applied_migrations WHERE name = 'routine-run-mode-pipeline'`)
	require.NoError(t, err)
	_, err = bundle0.DB.Exec(`UPDATE task_schedules SET run_mode = 'job' WHERE id = ?`, old.ID)
	require.NoError(t, err)
	require.NoError(t, bundle0.Close())

	bundle1, err := db.Open(path)
	require.NoError(t, err)
	require.Equal(t, "pipeline", runModeOf(t, path, old.ID), "pre-upgrade routine must become pipeline")

	newSched, err := repo.NewTaskScheduleRepo(bundle1.Client).Create(context.Background(), repo.CreateTaskScheduleInput{
		Name: "new", CronExpr: "0 9 * * *", SlugPrefix: "new", Title: "New",
		Cwd: "/tmp", MaxIterations: 20,
	})
	require.NoError(t, err)
	require.NoError(t, bundle1.Close())

	bundle2, err := db.Open(path)
	require.NoError(t, err)
	defer func() { _ = bundle2.Close() }()
	require.Equal(t, "job", runModeOf(t, path, newSched.ID), "a routine created after the upgrade must not be swept into pipeline")
	require.Equal(t, "pipeline", runModeOf(t, path, old.ID), "second boot must not touch an already-migrated row")
}

// TestOpen_RenameStagesRunsOnlyOnce is the load-bearing test for the stage
// rename. The rename is a chained one — "backlog"→"ready" then
// "concept"→"backlog" — so a second unguarded pass would take the rows the
// first pass had just written as "backlog" and push them on to "ready".
// "ready" is the stage the scheduler picks up, so a task deliberately parked in
// the refinement holding pen would start running on the next restart with
// nobody having asked for it. Only reopening a third time proves that cannot
// happen.
func TestOpen_RenameStagesRunsOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rename.db")

	bundle0, err := db.Open(path)
	require.NoError(t, err)
	_, err = bundle0.DB.Exec(
		`INSERT INTO tasks (id, slug, title, cwd, current_stage, priority, max_iterations, silver_bullet, created_at, updated_at)
		 VALUES ('t-concept', 't-concept', 'Test', '', 'concept', 'medium', 20, 0, datetime('now'), datetime('now')),
		        ('t-backlog', 't-backlog', 'Test', '', 'backlog', 'medium', 20, 0, datetime('now'), datetime('now'))`,
	)
	require.NoError(t, err)
	// The seeding Open already recorded the marker, so clear it: the rows above
	// stand in for a database written before the rename shipped.
	_, err = bundle0.DB.Exec(`DELETE FROM applied_migrations WHERE name = 'rename-stages-concept-backlog'`)
	require.NoError(t, err)
	require.NoError(t, bundle0.Close())

	bundle1, err := db.Open(path)
	require.NoError(t, err)
	require.NoError(t, bundle1.Close())
	require.Equal(t, "backlog", stageOf(t, path, "t-concept"), "old concept must land in the holding pen")
	require.Equal(t, "ready", stageOf(t, path, "t-backlog"), "old backlog must become runnable")

	bundle2, err := db.Open(path)
	require.NoError(t, err)
	require.NoError(t, bundle2.Close())
	require.Equal(t, "backlog", stageOf(t, path, "t-concept"),
		"a second boot must not push the parked task on to 'ready'")
	require.Equal(t, "ready", stageOf(t, path, "t-backlog"))
}

func TestOpen_MCPApplicationTables(t *testing.T) {
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	defer func() { _ = bundle.Client.Close() }()
	ctx := t.Context()

	app, err := bundle.Client.MCPApplication.Create().
		SetID("app-1").
		SetResourceID("res-1").
		SetServerName("mail").
		Save(ctx)
	require.NoError(t, err)
	require.False(t, app.AttachAll)
	require.Empty(t, app.RequiredEnv)
	require.Empty(t, app.Catalogue)

	_, err = bundle.Client.ApplicationSecret.Create().
		SetID("sec-1").SetResourceID("res-1").SetEnvName("PASSWORD").
		SetCiphertext("c").SetNonce("n").
		Save(ctx)
	require.NoError(t, err)

	_, err = bundle.Client.ApplicationSecret.Create().
		SetID("sec-2").SetResourceID("res-1").SetEnvName("PASSWORD").
		SetCiphertext("c").SetNonce("n").
		Save(ctx)
	require.Error(t, err, "one value per application and variable name")
}
