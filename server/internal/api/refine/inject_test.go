package refine_test

import (
	"context"
	"testing"

	apirefine "github.com/lx-wnk/kontor/server/internal/api/refine"
	"github.com/lx-wnk/kontor/server/internal/db/ent/refinementturn"
	"github.com/lx-wnk/kontor/server/internal/refine"
)

// TestInjectConcept_RoundTripsViaExtractConcept verifies that InjectConcept persists
// a turn whose content ExtractConcept can parse back to the same spec and plan.
func TestInjectConcept_RoundTripsViaExtractConcept(t *testing.T) {
	turns := &fakeTurnRepo{}
	runner := refine.NewRunner(turns, nil)

	c := refine.Concept{
		Raw:          map[string]any{"spec": "add endpoint", "plan": []any{"step1", "step2"}, "refinedTitle": "My Feature", "sourceBranch": "feat/my"},
		RefinedTitle: "My Feature",
		SourceBranch: "feat/my",
	}

	err := apirefine.InjectConcept(context.Background(), apirefine.InjectDeps{
		Turns:  turns,
		Runner: runner,
	}, "task-inject-1", c)
	if err != nil {
		t.Fatalf("InjectConcept: %v", err)
	}

	// Status must be draft_ready.
	status, _ := runner.State("task-inject-1")
	if status != refine.StatusDraftReady {
		t.Errorf("runner status = %q, want %q", status, refine.StatusDraftReady)
	}

	// ExtractConcept must be able to parse the persisted turn back.
	allTurns, _ := turns.ListForTask(context.Background(), "task-inject-1", 0)
	got, ok := refine.ExtractConcept(allTurns)
	if !ok {
		t.Fatal("ExtractConcept returned ok=false after inject")
	}
	if got.RefinedTitle != "My Feature" {
		t.Errorf("RefinedTitle = %q, want %q", got.RefinedTitle, "My Feature")
	}
	if got.SourceBranch != "feat/my" {
		t.Errorf("SourceBranch = %q, want %q", got.SourceBranch, "feat/my")
	}
	if got.Raw["spec"] != "add endpoint" {
		t.Errorf("spec = %v, want %q", got.Raw["spec"], "add endpoint")
	}
}

// TestInjectConcept_ThenConfirm_PopulatesTaskMetadata verifies the end-to-end path:
// inject a concept → call Confirm → task.Metadata contains spec/plan.
func TestInjectConcept_ThenConfirm_PopulatesTaskMetadata(t *testing.T) {
	turns := &fakeTurnRepo{}
	tasks := newFakeTaskRepo(defaultTask(t, "task-e2e"))
	runner := refine.NewRunner(turns, nil)

	c := refine.Concept{
		Raw:          map[string]any{"spec": "full spec text", "plan": []any{"phase A"}, "refinedTitle": "E2E Title"},
		RefinedTitle: "E2E Title",
	}

	if err := apirefine.InjectConcept(context.Background(), apirefine.InjectDeps{
		Turns:  turns,
		Runner: runner,
	}, "task-e2e", c); err != nil {
		t.Fatalf("InjectConcept: %v", err)
	}

	task, err := apirefine.Confirm(context.Background(), apirefine.ConfirmDeps{
		Turns: turns,
		Tasks: tasks,
	}, "task-e2e")
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if task == nil {
		t.Fatal("Confirm returned nil task")
	}

	up := tasks.lastUpdate
	if up == nil {
		t.Fatal("expected task update")
	}
	if up.Metadata == nil || up.Metadata["spec"] != "full spec text" {
		t.Errorf("Metadata.spec = %v, want %q", up.Metadata["spec"], "full spec text")
	}
	if up.Title == nil || *up.Title != "E2E Title" {
		t.Errorf("Title = %v, want %q", up.Title, "E2E Title")
	}
	// routing keys must not appear in metadata
	if _, present := up.Metadata["refinedTitle"]; present {
		t.Error("routing key refinedTitle must not leak into metadata")
	}
}

// TestInjectConcept_TurnRoleIsAssistant ensures the persisted turn has role=assistant
// (required by ExtractConcept).
func TestInjectConcept_TurnRoleIsAssistant(t *testing.T) {
	turns := &fakeTurnRepo{}
	runner := refine.NewRunner(turns, nil)

	c := refine.Concept{
		Raw: map[string]any{"spec": "s", "plan": []any{"p"}},
	}
	if err := apirefine.InjectConcept(context.Background(), apirefine.InjectDeps{
		Turns:  turns,
		Runner: runner,
	}, "task-role", c); err != nil {
		t.Fatalf("InjectConcept: %v", err)
	}

	for _, turn := range turns.turns {
		if turn.TaskID == "task-role" {
			if string(turn.Role) != string(refinementturn.RoleAssistant) {
				t.Errorf("turn role = %q, want assistant", turn.Role)
			}
		}
	}
}
