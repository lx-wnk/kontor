package pipeline

import (
	"strings"
	"testing"
)

type fakeKindHandler struct{ stage string }

func (f fakeKindHandler) Stage() string       { return f.stage }
func (f fakeKindHandler) RequiresAgent() bool { return false }
func (f fakeKindHandler) Execute(*StageContext) (StageTransition, error) {
	return NextTransition{}, nil
}

// A module may add a kind of stage, never redefine one of the core's. The core
// stages carry the lifecycle every task depends on; a module silently taking
// one over would change what "implementation" means for every task at once.
func TestRegisterStageKind_RefusesACoreStageName(t *testing.T) {
	o := &PipelineOrchestrator{}

	if err := o.RegisterStageKind("gather", fakeKindHandler{stage: "gather"}); err != nil {
		t.Fatalf("a new kind must register: %v", err)
	}
	if got := o.resolveHandler("gather"); got == nil {
		t.Fatal("the registered kind is not resolvable")
	}

	for _, core := range StageOrder {
		err := o.RegisterStageKind(core, fakeKindHandler{stage: core})
		if err == nil {
			t.Fatalf("registering the core stage %q must be refused", core)
		}
		if !strings.Contains(err.Error(), core) {
			t.Errorf("error %q does not name the stage it refused", err)
		}
	}
}

// A module task kind brings its own sequence of stages. The core's sequence is
// unchanged for every other task: a kind is added beside it, never in place of
// it.
func TestTaskKindSequence(t *testing.T) {
	t.Cleanup(ClearTaskKindSequences)

	if err := RegisterTaskKindSequence("research", []string{"intake", "gather", "done"}); err != nil {
		t.Fatalf("a sequence ending in done must register: %v", err)
	}

	if got := NextStageForKind("research", "intake"); got != "gather" {
		t.Errorf("after intake = %q, want gather", got)
	}
	if got := NextStageForKind("research", "gather"); got != "done" {
		t.Errorf("after gather = %q, want done", got)
	}
	// An unknown kind keeps the core sequence.
	if got := NextStageForKind("pipeline", "backlog"); got != NextStage("backlog") {
		t.Errorf("a core task = %q, want the core sequence", got)
	}

	// A sequence that never reaches done would leave a task running forever.
	if err := RegisterTaskKindSequence("endless", []string{"intake", "gather"}); err == nil {
		t.Error("a sequence that does not end in done must be refused")
	}
	if err := RegisterTaskKindSequence("empty", nil); err == nil {
		t.Error("an empty sequence must be refused")
	}
}
