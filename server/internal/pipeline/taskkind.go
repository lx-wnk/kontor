package pipeline

import (
	"fmt"
	"sync"
)

// taskKindSequences holds the stage sequence a module task kind follows. The
// core's StageOrder is untouched: a kind is added beside it, never in place of
// it, so a task of an unknown kind keeps the progression every task has always
// had.
var (
	taskKindMu        sync.RWMutex
	taskKindSequences = map[string][]string{}
)

// RegisterTaskKindSequence declares the stages a task kind runs through.
//
// The sequence must end in the core's terminal stage. A sequence that never
// reaches it describes a task that can never finish — and a task that cannot
// finish is not a kind of work, it is a leak.
func RegisterTaskKindSequence(kind string, stages []string) error {
	if kind == "" {
		return fmt.Errorf("task kind: a name is required")
	}
	if len(stages) == 0 {
		return fmt.Errorf("task kind %q: a sequence of stages is required", kind)
	}
	if stages[len(stages)-1] != "done" {
		return fmt.Errorf("task kind %q: the sequence must end in \"done\", got %q", kind, stages[len(stages)-1])
	}
	taskKindMu.Lock()
	defer taskKindMu.Unlock()
	taskKindSequences[kind] = append([]string(nil), stages...)
	return nil
}

// ClearTaskKindSequences drops every registered sequence. Tests use it; the
// server registers once at boot.
func ClearTaskKindSequences() {
	taskKindMu.Lock()
	defer taskKindMu.Unlock()
	taskKindSequences = map[string][]string{}
}

// NextStageForKind returns the stage that follows s for a task of this kind,
// falling back to the core sequence for every kind that declared none.
func NextStageForKind(kind, s string) string {
	taskKindMu.RLock()
	sequence, ok := taskKindSequences[kind]
	taskKindMu.RUnlock()
	if !ok {
		return NextStage(s)
	}
	for i, stage := range sequence {
		if stage == s && i < len(sequence)-1 {
			return sequence[i+1]
		}
	}
	return "done"
}
