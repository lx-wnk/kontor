package refine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

func TestRunner_State_DefaultsToIdle(t *testing.T) {
	r := NewRunner(nil, nil)
	status, errMsg := r.State("task-x")
	if status != StatusNone {
		t.Errorf("default status: got %q, want %q", status, StatusNone)
	}
	if errMsg != "" {
		t.Errorf("default errMsg: got %q, want empty", errMsg)
	}
}

func TestRunner_IsRunning_FalseWhenAbsent(t *testing.T) {
	r := NewRunner(nil, nil)
	if r.IsRunning("task-x") {
		t.Error("IsRunning should be false for an unknown task")
	}
	_ = context.Background()
}

// fakeTurns is a minimal in-memory RefinementTurnRepo for runner tests.
type fakeTurns struct {
	mu      sync.Mutex
	created []repo.CreateTurnInput
}

func (f *fakeTurns) Create(_ context.Context, in repo.CreateTurnInput) (*ent.RefinementTurn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	return &ent.RefinementTurn{}, nil
}

func (f *fakeTurns) ListForTask(context.Context, string, int) ([]*ent.RefinementTurn, error) {
	return nil, nil
}

func (f *fakeTurns) ListForTaskNewest(context.Context, string, int) ([]*ent.RefinementTurn, error) {
	return nil, nil
}

func (f *fakeTurns) DeleteForTask(context.Context, string) error {
	return nil
}

func (f *fakeTurns) assistantCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.created {
		if c.Role == "assistant" {
			n++
		}
	}
	return n
}

// waitFor polls until cond() or the deadline; fails the test on timeout.
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

func TestRunner_Start_PersistsAssistantTurnAndMarksDone(t *testing.T) {
	turns := &fakeTurns{}
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 2)
		ch <- "Hello"
		ch <- "World"
		close(ch)
		return ch, nil
	}
	r := NewRunner(turns, spawn)

	out, err := r.Start("task-1", SpawnConfig{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Drain the tee channel like the HTTP handler would.
	for range out { //nolint:revive
	}
	waitFor(t, func() bool {
		s, _ := r.State("task-1")
		return s == StatusDraftReady
	}, "status done")

	if turns.assistantCount() != 1 {
		t.Errorf("assistant turns persisted: got %d, want 1", turns.assistantCount())
	}
}

func TestRunner_Start_ErrorLineMarksFailed(t *testing.T) {
	turns := &fakeTurns{}
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 1)
		ch <- "[ERROR] claude exited: boom"
		close(ch)
		return ch, nil
	}
	r := NewRunner(turns, spawn)
	out, _ := r.Start("t", SpawnConfig{}, nil)
	for range out { //nolint:revive
	}
	waitFor(t, func() bool { s, _ := r.State("t"); return s == StatusFailed }, "failed")
	if turns.assistantCount() != 0 {
		t.Errorf("error run must not persist an assistant turn, got %d", turns.assistantCount())
	}
	_, errMsg := r.State("t")
	if errMsg == "" {
		t.Error("failed run should record an error message")
	}
}

func TestRunner_Start_EmptyOutputMarksFailed(t *testing.T) {
	turns := &fakeTurns{}
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string)
		close(ch)
		return ch, nil
	}
	r := NewRunner(turns, spawn)
	out, err := r.Start("t-empty", SpawnConfig{}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	for range out { //nolint:revive
	}
	waitFor(t, func() bool { s, _ := r.State("t-empty"); return s == StatusFailed }, "failed on empty output")
	s, errMsg := r.State("t-empty")
	if s != StatusFailed {
		t.Errorf("status: got %q, want failed", s)
	}
	if errMsg == "" {
		t.Error("empty-output run should record a non-empty error message")
	}
	if turns.assistantCount() != 0 {
		t.Errorf("empty-output run must not persist an assistant turn, got %d", turns.assistantCount())
	}
}

func TestRunner_Start_SpawnErrorMarksFailed(t *testing.T) {
	r := NewRunner(&fakeTurns{}, func(context.Context, SpawnConfig, *ent.Spawner) (<-chan string, error) {
		return nil, errors.New("spawn boom")
	})
	if _, err := r.Start("t", SpawnConfig{}, nil); err == nil {
		t.Fatal("Start should return the spawn error")
	}
	s, _ := r.State("t")
	if s != StatusFailed {
		t.Errorf("status after spawn error: got %q, want failed", s)
	}
}

func TestRunner_Start_PersistsPhaseAndStripsMarker(t *testing.T) {
	turns := &fakeTurns{}
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 2)
		ch <- "Here is the spec."
		ch <- "__phase_done: spec"
		close(ch)
		return ch, nil
	}
	r := NewRunner(turns, spawn)
	out, _ := r.Start("task-1", SpawnConfig{}, nil)
	for range out { //nolint:revive
	}
	waitFor(t, func() bool { s, _ := r.State("task-1"); return s == StatusDraftReady }, "done")

	turns.mu.Lock()
	defer turns.mu.Unlock()
	var asst *repo.CreateTurnInput
	for i := range turns.created {
		if turns.created[i].Role == "assistant" {
			asst = &turns.created[i]
		}
	}
	if asst == nil {
		t.Fatal("no assistant turn persisted")
	}
	if asst.Phase == nil || *asst.Phase != "spec" {
		t.Errorf("persisted phase: got %v, want spec", asst.Phase)
	}
	if strings.Contains(asst.Content, "__phase_done") {
		t.Errorf("persisted content still contains marker: %q", asst.Content)
	}
}

func TestRunner_Start_PersistsOptionsAndStripsBlock(t *testing.T) {
	turns := &fakeTurns{}
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 6)
		ch <- "Here is a question?"
		ch <- "__options_start"
		ch <- "Yes, do it"
		ch <- "No, skip it"
		ch <- "Let me think"
		ch <- "__options_end"
		close(ch)
		return ch, nil
	}
	r := NewRunner(turns, spawn)
	out, _ := r.Start("task-1", SpawnConfig{}, nil)
	for range out { //nolint:revive
	}
	waitFor(t, func() bool { s, _ := r.State("task-1"); return s == StatusDraftReady }, "done")

	turns.mu.Lock()
	defer turns.mu.Unlock()
	var asst *repo.CreateTurnInput
	for i := range turns.created {
		if turns.created[i].Role == "assistant" {
			asst = &turns.created[i]
		}
	}
	if asst == nil {
		t.Fatal("no assistant turn persisted")
	}
	wantOptions := []string{"Yes, do it", "No, skip it", "Let me think"}
	if len(asst.Options) != len(wantOptions) {
		t.Fatalf("persisted options: got %v, want %v", asst.Options, wantOptions)
	}
	for i, want := range wantOptions {
		if asst.Options[i] != want {
			t.Errorf("option %d: got %q, want %q", i, asst.Options[i], want)
		}
	}
	for _, marker := range []string{"__options_start", "__options_end", "Yes, do it", "No, skip it", "Let me think"} {
		if strings.Contains(asst.Content, marker) {
			t.Errorf("persisted content still contains %q: %q", marker, asst.Content)
		}
	}
	if asst.Content != "Here is a question?" {
		t.Errorf("persisted content: got %q, want %q", asst.Content, "Here is a question?")
	}
}

func TestRunner_Start_NoOptionsBlockPersistsNilOptions(t *testing.T) {
	turns := &fakeTurns{}
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string, 2)
		ch <- "Here is a question?"
		ch <- "No options here."
		close(ch)
		return ch, nil
	}
	r := NewRunner(turns, spawn)
	out, _ := r.Start("task-1", SpawnConfig{}, nil)
	for range out { //nolint:revive
	}
	waitFor(t, func() bool { s, _ := r.State("task-1"); return s == StatusDraftReady }, "done")

	turns.mu.Lock()
	defer turns.mu.Unlock()
	var asst *repo.CreateTurnInput
	for i := range turns.created {
		if turns.created[i].Role == "assistant" {
			asst = &turns.created[i]
		}
	}
	if asst == nil {
		t.Fatal("no assistant turn persisted")
	}
	if asst.Options != nil {
		t.Errorf("persisted options: got %v, want nil", asst.Options)
	}
}

func TestRunner_Start_RejectsConcurrentRun(t *testing.T) {
	release := make(chan struct{})
	spawn := func(_ context.Context, _ SpawnConfig, _ *ent.Spawner) (<-chan string, error) {
		ch := make(chan string)
		go func() { <-release; close(ch) }()
		return ch, nil
	}
	r := NewRunner(&fakeTurns{}, spawn)
	if _, err := r.Start("t", SpawnConfig{}, nil); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	if _, err := r.Start("t", SpawnConfig{}, nil); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start: got %v, want ErrAlreadyRunning", err)
	}
	close(release)
}
