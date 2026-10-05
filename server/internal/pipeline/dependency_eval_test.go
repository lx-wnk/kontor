package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

func TestEvaluateDependency(t *testing.T) {
	cases := []struct {
		name          string
		required      string
		onCancel      string
		upstreamStage string
		want          DepStatus
	}{
		{"satisfied at required done", "done", "on_hold", "done", DepSatisfied},
		{"blocked while in progress", "done", "on_hold", "implementation", DepBlocked},
		{"blocked before start", "done", "on_hold", "backlog", DepBlocked},
		{"unsatisfiable when cancelled and action on_hold", "done", "on_hold", "cancelled", DepUnsatisfiable},
		{"cancelled rescued by start action", "done", "start", "cancelled", DepSatisfied},
		{"required cancelled is satisfied when cancelled", "cancelled", "on_hold", "cancelled", DepSatisfied},
		{"required cancelled unsatisfiable when done", "cancelled", "on_hold", "done", DepUnsatisfiable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dep := &ent.TaskDependency{RequiredStage: c.required, OnCancelAction: c.onCancel}
			if got := EvaluateDependency(dep, c.upstreamStage); got != c.want {
				t.Fatalf("EvaluateDependency(%q req=%q action=%q) = %v, want %v",
					c.upstreamStage, c.required, c.onCancel, got, c.want)
			}
		})
	}
}

type fakeDepRepo struct {
	upstream map[string][]*ent.TaskDependency
	listErr  error
}

func (f *fakeDepRepo) Add(context.Context, string, string, string, string) (*ent.TaskDependency, error) {
	return nil, nil
}
func (f *fakeDepRepo) Remove(context.Context, string, string) (bool, error) { return false, nil }
func (f *fakeDepRepo) ListUpstream(_ context.Context, taskID string) ([]*ent.TaskDependency, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.upstream[taskID], nil
}
func (f *fakeDepRepo) ListDownstream(context.Context, string) ([]*ent.TaskDependency, error) {
	return nil, nil
}
func (f *fakeDepRepo) RemoveByID(context.Context, string) error { return nil }

func TestEvaluateTaskDeps(t *testing.T) {
	stages := map[string]string{
		"up-done":    "done",
		"up-running": "implementation",
		"up-cancel":  "cancelled",
	}
	resolve := func(_ context.Context, id string) (string, error) {
		s, ok := stages[id]
		if !ok {
			return "", errors.New("unknown task")
		}
		return s, nil
	}

	t.Run("no upstreams is satisfied", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{}}
		sat, blocked, unsat, err := EvaluateTaskDeps(context.Background(), "t", f, resolve)
		if err != nil || !sat || blocked || unsat {
			t.Fatalf("got sat=%v blocked=%v unsat=%v err=%v", sat, blocked, unsat, err)
		}
	})

	t.Run("all upstreams done is satisfied", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{
			"t": {{DependsOnID: "up-done", RequiredStage: "done", OnCancelAction: "on_hold"}},
		}}
		sat, blocked, unsat, _ := EvaluateTaskDeps(context.Background(), "t", f, resolve)
		if !sat || blocked || unsat {
			t.Fatalf("got sat=%v blocked=%v unsat=%v", sat, blocked, unsat)
		}
	})

	t.Run("in-progress upstream blocks", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{
			"t": {{DependsOnID: "up-running", RequiredStage: "done", OnCancelAction: "on_hold"}},
		}}
		sat, blocked, unsat, _ := EvaluateTaskDeps(context.Background(), "t", f, resolve)
		if sat || !blocked || unsat {
			t.Fatalf("got sat=%v blocked=%v unsat=%v", sat, blocked, unsat)
		}
	})

	t.Run("cancelled upstream is unsatisfiable", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{
			"t": {{DependsOnID: "up-cancel", RequiredStage: "done", OnCancelAction: "on_hold"}},
		}}
		sat, _, unsat, _ := EvaluateTaskDeps(context.Background(), "t", f, resolve)
		if sat || !unsat {
			t.Fatalf("got sat=%v unsat=%v", sat, unsat)
		}
	})

	t.Run("resolve error treated as blocked", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{
			"t": {{DependsOnID: "missing", RequiredStage: "done", OnCancelAction: "on_hold"}},
		}}
		sat, blocked, _, err := EvaluateTaskDeps(context.Background(), "t", f, resolve)
		if err != nil || sat || !blocked {
			t.Fatalf("got sat=%v blocked=%v err=%v", sat, blocked, err)
		}
	})

	t.Run("ListUpstream error propagates", func(t *testing.T) {
		f := &fakeDepRepo{listErr: errors.New("db down")}
		_, _, _, err := EvaluateTaskDeps(context.Background(), "t", f, resolve)
		if err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestEvaluateTaskDepsDetail(t *testing.T) {
	stages := map[string]string{
		"up-done":    "done",
		"up-running": "implementation",
		"up-cancel":  "cancelled",
	}
	resolveInfo := func(_ context.Context, id string) (string, string, error) {
		s, ok := stages[id]
		if !ok {
			return "", "", errors.New("unknown task")
		}
		return s, id, nil // slug == id for test simplicity
	}

	t.Run("unsatisfiable entry has Unsatisfiable=true, blocked entry has Unsatisfiable=false", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{
			"t": {
				{DependsOnID: "up-running", RequiredStage: "done", OnCancelAction: "on_hold"},
				{DependsOnID: "up-cancel", RequiredStage: "done", OnCancelAction: "on_hold"},
			},
		}}
		_, blocked, unsat, ups, err := EvaluateTaskDepsDetail(context.Background(), "t", f, resolveInfo)
		if err != nil {
			t.Fatal(err)
		}
		if !blocked || !unsat {
			t.Fatalf("want blocked+unsat, got blocked=%v unsat=%v", blocked, unsat)
		}
		if len(ups) != 2 {
			t.Fatalf("want 2 blocking upstreams, got %d", len(ups))
		}
		var sawBlocked, sawUnsat bool
		for _, u := range ups {
			if u.Unsatisfiable {
				sawUnsat = true
				if u.Slug != "up-cancel" {
					t.Errorf("unsatisfiable entry: want slug up-cancel, got %s", u.Slug)
				}
				if u.Stage != "cancelled" {
					t.Errorf("unsatisfiable entry: want stage cancelled, got %s", u.Stage)
				}
			} else {
				sawBlocked = true
				if u.Slug != "up-running" {
					t.Errorf("blocked entry: want slug up-running, got %s", u.Slug)
				}
				if u.Stage != "implementation" {
					t.Errorf("blocked entry: want stage implementation, got %s", u.Stage)
				}
			}
		}
		if !sawBlocked {
			t.Error("expected an entry with Unsatisfiable=false")
		}
		if !sawUnsat {
			t.Error("expected an entry with Unsatisfiable=true")
		}
	})

	t.Run("resolve error sets blocked=true but leaves upstreams empty", func(t *testing.T) {
		f := &fakeDepRepo{upstream: map[string][]*ent.TaskDependency{
			"t": {{DependsOnID: "missing", RequiredStage: "done", OnCancelAction: "on_hold"}},
		}}
		_, blocked, _, ups, err := EvaluateTaskDepsDetail(context.Background(), "t", f, resolveInfo)
		if err != nil {
			t.Fatal(err)
		}
		if !blocked {
			t.Fatal("want blocked=true when resolve fails")
		}
		if len(ups) != 0 {
			t.Fatalf("want empty upstreams on resolve error, got %d", len(ups))
		}
	})
}
