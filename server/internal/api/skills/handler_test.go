package skills_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/api/skills"
	"github.com/lx-wnk/kontor/server/internal/db"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/materializer"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

func newRouter(t *testing.T) (http.Handler, string) {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	configDir := filepath.Join(t.TempDir(), ".claude")
	require.NoError(t, os.MkdirAll(configDir, 0o700))

	resources := repo.NewResourceRepo(bundle.Client)
	res, err := resources.Upsert(context.Background(), repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "code-review", Name: "Code Review",
		Scope: repo.GlobalScope(), State: repo.ResourceStateEnabled,
	})
	require.NoError(t, err)
	_, err = repo.NewSkillRepo(bundle.Client).Upsert(context.Background(), repo.UpsertSkillInput{
		ResourceID: res.ID, Description: "Review a diff", Body: "v1",
	})
	require.NoError(t, err)

	m := materializer.New(
		resources,
		repo.NewSkillRepo(bundle.Client),
		repo.NewMaterializationRepo(bundle.Client),
		repo.NewCoordLockRepo(bundle.Client),
		materializer.Resolver{
			NodeID:             repo.DefaultNodeID,
			ClaudeConfigDirs:   func() []string { return []string{configDir} },
			ProviderConfigDirs: func() []parser.ProviderConfigDir { return nil },
		},
	)

	r := chi.NewRouter()
	skills.NewHandler(m).Mount(r)
	return r, configDir
}

// blockingResourceRepo wraps a real ResourceRepo and calls a hook the first
// time ListForKind is called — the earliest database access inside
// Materializer.Run after the handler's CompareAndSwap guard.
type blockingResourceRepo struct {
	repo.ResourceRepo
	hook func()
}

func (b *blockingResourceRepo) ListForKind(ctx context.Context, kind string) ([]*ent.Resource, error) {
	if b.hook != nil {
		b.hook()
	}
	return b.ResourceRepo.ListForKind(ctx, kind)
}

func newBlockingRouter(t *testing.T, hook func()) http.Handler {
	t.Helper()
	bundle, err := db.Open(":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bundle.Client.Close() })

	configDir := filepath.Join(t.TempDir(), ".claude")
	require.NoError(t, os.MkdirAll(configDir, 0o700))

	resources := repo.NewResourceRepo(bundle.Client)
	res, err := resources.Upsert(context.Background(), repo.UpsertResourceInput{
		Kind: repo.ResourceKindSkill, Slug: "code-review", Name: "Code Review",
		Scope: repo.GlobalScope(), State: repo.ResourceStateEnabled,
	})
	require.NoError(t, err)
	_, err = repo.NewSkillRepo(bundle.Client).Upsert(context.Background(), repo.UpsertSkillInput{
		ResourceID: res.ID, Description: "Review a diff", Body: "v1",
	})
	require.NoError(t, err)

	blocking := &blockingResourceRepo{ResourceRepo: resources, hook: hook}

	m := materializer.New(
		blocking,
		repo.NewSkillRepo(bundle.Client),
		repo.NewMaterializationRepo(bundle.Client),
		repo.NewCoordLockRepo(bundle.Client),
		materializer.Resolver{
			NodeID:             repo.DefaultNodeID,
			ClaudeConfigDirs:   func() []string { return []string{configDir} },
			ProviderConfigDirs: func() []parser.ProviderConfigDir { return nil },
		},
	)

	r := chi.NewRouter()
	skills.NewHandler(m).Mount(r)
	return r
}

func post(t *testing.T, r http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/skills/materialize", reader)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func postHook(t *testing.T, r http.Handler, body string, _ func()) *httptest.ResponseRecorder {
	t.Helper()
	// The hook is installed on the router's blockingResourceRepo, not here.
	return post(t, r, body)
}

func TestMaterialize_MissingDryRunDefaultsToADryRun(t *testing.T) {
	r, configDir := newRouter(t)

	rec := post(t, r, `{}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var rep materializer.Report
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rep))
	require.True(t, rep.DryRun, "a route that can overwrite a hand-edited file defaults to writing nothing")

	_, err := os.Stat(filepath.Join(configDir, "skills", "code-review", "SKILL.md"))
	require.True(t, os.IsNotExist(err))
}

func TestMaterialize_AnEmptyBodyIsADryRunToo(t *testing.T) {
	r, _ := newRouter(t)

	rec := post(t, r, "")
	require.Equal(t, http.StatusOK, rec.Code)

	var rep materializer.Report
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &rep))
	require.True(t, rep.DryRun)
}

func TestMaterialize_DryRunFalseWrites(t *testing.T) {
	r, configDir := newRouter(t)

	rec := post(t, r, `{"dryRun": false}`)
	require.Equal(t, http.StatusOK, rec.Code)

	got, err := os.ReadFile(filepath.Join(configDir, "skills", "code-review", "SKILL.md"))
	require.NoError(t, err)
	require.Contains(t, string(got), "v1")
}

func TestMaterialize_ResponseIsCamelCase(t *testing.T) {
	r, _ := newRouter(t)

	rec := post(t, r, `{}`)
	var raw map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))

	for _, key := range []string{"nodeId", "dryRun", "leased", "partial", "targets", "entries"} {
		require.Contains(t, raw, key)
	}
	entries, ok := raw["entries"].([]any)
	require.True(t, ok)
	require.Len(t, entries, 1)
	first, ok := entries[0].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"resourceId", "targetKey", "outcome"} {
		require.Contains(t, first, key)
	}
}

func TestMaterialize_RejectsAnInvalidBody(t *testing.T) {
	r, _ := newRouter(t)

	rec := post(t, r, `not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// The single-flight guard of Design note two has no other anchor: deleting the
// CompareAndSwap is a one-line change, and every other test in this file issues
// one request at a time, so all of them stay green without it. Two concurrent
// runs would both hold the lease — it is re-entrant for the same owner, and the
// owner is per process — and race each other into the same files.
func TestMaterialize_ASecondConcurrentRequestIsRejected(t *testing.T) {
	var (
		started = make(chan struct{})
		release = make(chan struct{})
		codes   = make(chan int, 1)
	)
	// The first request blocks inside materialize() until release is closed,
	// so the second one provably arrives while the guard is held rather than
	// after it — a sequential pair would pass with no guard at all.
	t.Cleanup(func() { close(release) })

	r := newBlockingRouter(t, func() {
		close(started)
		<-release
	})

	go func() {
		rec := postHook(t, r, `{"dryRun":true}`, nil)
		codes <- rec.Code
	}()

	<-started
	second := post(t, r, `{"dryRun":true}`)
	require.Equal(t, http.StatusConflict, second.Code,
		"a request arriving while a run is in flight must be refused, not queued")
	require.Contains(t, second.Body.String(), "already running")
}
