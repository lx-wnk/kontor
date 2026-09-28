package refine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	apirefine "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/auth"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/refinementturn"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/refine"
	"github.com/lx-wnk/kontor/server/internal/services"
)

const testJWTSecret = "test-secret-32-chars-minimum-here"

// --- fake repos ---

type fakeTurnRepo struct {
	turns   []*ent.RefinementTurn
	counter atomic.Int64 // F055: avoid ID collisions
}

func (f *fakeTurnRepo) Create(_ context.Context, inp repo.CreateTurnInput) (*ent.RefinementTurn, error) {
	n := f.counter.Add(1)
	t := &ent.RefinementTurn{
		ID:        fmt.Sprintf("turn-%d", n), // F055: monotonic counter avoids content-based ID collisions
		TaskID:    inp.TaskID,
		Role:      refinementturn.Role(inp.Role),
		Content:   inp.Content,
		Phase:     inp.Phase,
		CreatedAt: time.Now(),
	}
	f.turns = append(f.turns, t)
	return t, nil
}

func (f *fakeTurnRepo) ListForTask(_ context.Context, taskID string, _ int) ([]*ent.RefinementTurn, error) {
	var out []*ent.RefinementTurn
	for _, t := range f.turns {
		if t.TaskID == taskID {
			out = append(out, t)
		}
	}
	return out, nil
}

// F026: respect limit and return newest-first.
func (f *fakeTurnRepo) ListForTaskNewest(_ context.Context, taskID string, limit int) ([]*ent.RefinementTurn, error) {
	var out []*ent.RefinementTurn
	for i := len(f.turns) - 1; i >= 0; i-- {
		if f.turns[i].TaskID == taskID {
			out = append(out, f.turns[i])
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func (f *fakeTurnRepo) DeleteForTask(_ context.Context, _ string) error { return nil }

// F027: map-based lookup so not-found is testable.
type fakeTaskRepo struct {
	byID       map[string]*ent.Task
	lastUpdate *repo.UpdateTaskInput // captured by Update for assertions
}

func newFakeTaskRepo(tasks ...*ent.Task) *fakeTaskRepo {
	m := make(map[string]*ent.Task, len(tasks))
	for _, t := range tasks {
		m[t.ID] = t
	}
	return &fakeTaskRepo{byID: m}
}

func (f *fakeTaskRepo) GetByID(_ context.Context, id string) (*ent.Task, error) {
	if t, ok := f.byID[id]; ok {
		return t, nil
	}
	return nil, errors.New("task not found")
}
func (f *fakeTaskRepo) Create(_ context.Context, _ repo.CreateTaskInput) (*ent.Task, error) {
	return nil, nil
}
func (f *fakeTaskRepo) GetBySlug(_ context.Context, _ string) (*ent.Task, error) { return nil, nil }
func (f *fakeTaskRepo) Update(_ context.Context, _ string, in repo.UpdateTaskInput) (*ent.Task, error) {
	f.lastUpdate = &in
	return nil, nil
}
func (f *fakeTaskRepo) RerankBetween(_ context.Context, _, _, _ string) (*ent.Task, error) {
	return nil, nil
}
func (f *fakeTaskRepo) Delete(_ context.Context, _ string) error { return nil }
func (f *fakeTaskRepo) ListForUser(_ context.Context, _ string, _ bool) ([]*ent.Task, error) {
	return nil, nil
}
func (f *fakeTaskRepo) ListPickable(_ context.Context) ([]*ent.Task, error)          { return nil, nil }
func (f *fakeTaskRepo) ListByStage(_ context.Context, _ string) ([]*ent.Task, error) { return nil, nil }
func (f *fakeTaskRepo) ListByIDs(_ context.Context, _ []string) ([]*ent.Task, error) { return nil, nil }
func (f *fakeTaskRepo) CountActiveBySourceBranch(_ context.Context, _, _ string) (int, error) {
	return 0, nil
}
func (f *fakeTaskRepo) ListByRoutine(_ context.Context, _ string, _ int) ([]*ent.Task, error) {
	return nil, nil
}

// --- helpers ---

func authToken(t *testing.T) string {
	t.Helper()
	tok, err := auth.SignJWT(auth.JWTPayload{Sub: "user-1", Login: "tester"}, testJWTSecret, 3600)
	if err != nil {
		t.Fatalf("SignJWT: %v", err)
	}
	return tok
}

func makeRouter(turns *fakeTurnRepo, tasks *fakeTaskRepo, spawner func(context.Context, refine.SpawnConfig, *ent.Spawner) (<-chan string, error)) http.Handler {
	h := apirefine.NewHandler(apirefine.Deps{
		Turns:   turns,
		Tasks:   tasks,
		Spawner: spawner,
		Runner:  refine.NewRunner(turns, spawner),
	})
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)
	return r
}

func withAuth(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: authToken(t)})
	return req
}

func noopSpawner(_ context.Context, _ refine.SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, nil
}

func defaultTask(t *testing.T, id string) *ent.Task {
	t.Helper()
	return &ent.Task{ID: id, Title: "Test Task", Cwd: t.TempDir()} // F053: t.TempDir() instead of /tmp
}

// --- tests ---

func TestListTurns_Empty(t *testing.T) {
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(&fakeTurnRepo{}, tasks, noopSpawner)
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/refine/task-1/turns", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var turns []any
	if err := json.Unmarshal(rr.Body.Bytes(), &turns); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(turns) != 0 {
		t.Errorf("want empty list, got %d items", len(turns))
	}
}

func TestListTurns_ReturnsTurns(t *testing.T) {
	phase := "drafting"
	repo := &fakeTurnRepo{
		turns: []*ent.RefinementTurn{
			{ID: "t1", TaskID: "task-1", Content: "Hello", CreatedAt: time.Now()},
			{ID: "t2", TaskID: "task-1", Content: "Hi", Phase: &phase, CreatedAt: time.Now()},
		},
	}
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(repo, tasks, noopSpawner)
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/refine/task-1/turns", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var turns []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &turns); err != nil {
		t.Fatalf("unmarshal turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("want 2 turns, got %d", len(turns))
	}
}

// waitFor polls cond() until it returns true or the 2s deadline expires.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", msg)
}

// F025: verify SSE framing and turn persistence, not just status 200.
func TestSubmitTurn_StreamsResponse(t *testing.T) {
	spawner := func(_ context.Context, _ refine.SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 2)
		ch <- "Hello"
		ch <- " world"
		close(ch)
		return ch, nil
	}
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(turns, tasks, spawner)
	body, _ := json.Marshal(map[string]string{"message": "test prompt"})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-1/turn", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	ct := rr.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("want SSE content-type, got %q", ct)
	}
	// F025: verify SSE frame format, not just text presence.
	sseBody := rr.Body.String()
	if !strings.Contains(sseBody, "data: Hello") {
		t.Errorf("SSE body missing 'data: Hello' frame: %s", sseBody)
	}
	// F025: verify assistant turn was persisted after streaming.
	// Persistence is async (runner goroutine) — poll with a short deadline.
	waitFor(t, func() bool {
		for _, turn := range turns.turns {
			if turn.TaskID == "task-1" && strings.Contains(turn.Content, "Hello") {
				return true
			}
		}
		return false
	}, "assistant turn persisted")
}

func TestSubmitTurn_RequiresMessage(t *testing.T) {
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(&fakeTurnRepo{}, tasks, noopSpawner)
	body, _ := json.Marshal(map[string]string{"message": "   "})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-1/turn", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", rr.Code)
	}
}

// F027: test the not-found branch in submitTurn.
func TestSubmitTurn_TaskNotFound(t *testing.T) {
	r := makeRouter(&fakeTurnRepo{}, newFakeTaskRepo(), noopSpawner)
	body, _ := json.Marshal(map[string]string{"message": "hello"})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/missing-task/turn", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("want 404 for unknown task, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestSubmitTurn_CallsResolverAndForwardsSpawnerToSpawnFunc(t *testing.T) {
	resolved := &ent.Spawner{ID: "sp-1", AdapterType: "claude"}
	var gotTaskID string
	var gotSpawner *ent.Spawner
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-under-test"))
	deps := apirefine.Deps{
		Turns: turns,
		Tasks: tasks,
		ResolveSpawner: func(_ context.Context, taskID string) (*ent.Spawner, services.SpawnerSource, error) {
			gotTaskID = taskID
			return resolved, services.SpawnerSourceTask, nil
		},
		Spawner: func(_ context.Context, _ refine.SpawnConfig, sp *ent.Spawner) (<-chan string, error) {
			gotSpawner = sp
			ch := make(chan string)
			close(ch)
			return ch, nil
		},
	}
	h := apirefine.NewHandler(deps)
	r := chi.NewRouter()
	r.Use(auth.RequireAuth(testJWTSecret))
	h.Mount(r)

	body, _ := json.Marshal(map[string]string{"message": "hello"})
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-under-test/turn", bytes.NewReader(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if gotTaskID != "task-under-test" {
		t.Errorf("resolver task id: got %q want %q", gotTaskID, "task-under-test")
	}
	if gotSpawner != resolved {
		t.Errorf("spawner forwarded to Spawn fn: got %v want %v", gotSpawner, resolved)
	}
}

func TestConfirm_StoresSentinel(t *testing.T) {
	repo := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(repo, tasks, noopSpawner)
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-1/confirm", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	found := false
	for _, turn := range repo.turns {
		if turn.Phase != nil && *turn.Phase == "confirmed" {
			found = true
		}
	}
	if !found {
		t.Error("want confirmed sentinel turn to be stored")
	}
}

func TestConfirm_PersistsConceptOntoTask(t *testing.T) {
	concept := "Plan ready.\n\n" +
		"```json\n" +
		"{\"refinedTitle\":\"Switch BocPrice to JSON\",\"spec\":\"serialize to JSON\"," +
		"\"plan\":[\"add reindex\",\"flip serializer\"],\"toolRequests\":[\"Bash\",\"Edit\"]," +
		"\"sourceBranch\":\"users/claude/eps-fix\"}\n" +
		"```\n"
	turnRepo := &fakeTurnRepo{turns: []*ent.RefinementTurn{
		{ID: "t1", TaskID: "task-1", Role: refinementturn.Role("user"), Content: "build it"},
		{ID: "t2", TaskID: "task-1", Role: refinementturn.Role("assistant"), Content: concept, Phase: strPtr("approval")},
	}}
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(turnRepo, tasks, noopSpawner)

	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-1/confirm", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	up := tasks.lastUpdate
	if up == nil {
		t.Fatal("expected confirm to update the task")
	}
	if up.Title == nil || *up.Title != "Switch BocPrice to JSON" {
		t.Errorf("Title = %v, want refined title applied", up.Title)
	}
	if up.SourceBranch == nil || *up.SourceBranch != "users/claude/eps-fix" {
		t.Errorf("SourceBranch = %v, want it set (triggers auto-worktree)", up.SourceBranch)
	}
	if up.CurrentStage == nil || *up.CurrentStage != "ready" {
		t.Errorf("CurrentStage = %v, want ready", up.CurrentStage)
	}
	if up.Metadata == nil || up.Metadata["spec"] != "serialize to JSON" {
		t.Errorf("Metadata.spec = %v, want concept spec persisted", up.Metadata)
	}
	if _, present := up.Metadata["refinedTitle"]; present {
		t.Error("routing key refinedTitle must not leak into metadata")
	}
}

func strPtr(s string) *string { return &s }

func TestStatus_ReturnsNoneForUnknownTask(t *testing.T) {
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo()
	router := makeRouter(turns, tasks, noopSpawner)
	req := withAuth(t, httptest.NewRequest(http.MethodGet, "/api/refine/task-1/status", nil))
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "none" {
		t.Errorf("status field: got %v, want none", body["status"])
	}
}

// TestConfirm_WireFormat asserts the confirm route answers the camelCase task
// shape rather than the raw ent entity.
func TestConfirm_WireFormat(t *testing.T) {
	tasks := newFakeTaskRepo(defaultTask(t, "task-1"))
	r := makeRouter(&fakeTurnRepo{}, tasks, noopSpawner)
	req := withAuth(t, httptest.NewRequest(http.MethodPost, "/api/refine/task-1/confirm", nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var row map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal response: %v (body: %s)", err, rr.Body.String())
	}
	for _, k := range []string{"id", "title", "currentStage", "maxIterations", "silverBullet", "createdAt"} {
		if _, ok := row[k]; !ok {
			t.Errorf("missing key %q in %v", k, row)
		}
	}
	for _, k := range []string{"current_stage", "max_iterations", "silver_bullet", "created_at", "edges"} {
		if _, ok := row[k]; ok {
			t.Errorf("unexpected key %q in %v", k, row)
		}
	}
}
