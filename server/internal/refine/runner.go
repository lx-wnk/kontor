package refine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// ErrAlreadyRunning is returned by Start when a run is already in flight.
var ErrAlreadyRunning = errors.New("refine: a run is already in progress for this task")

const runTimeout = 5 * time.Minute

// Run status values surfaced to the UI via the enriched task. The lifecycle is
// none → refining → draft_ready (→ task advances on approve_spec), or → failed.
const (
	StatusNone       = "none"
	StatusRefining   = "refining"
	StatusFailed     = "failed"
	StatusDraftReady = "draft_ready" // refinement complete or concept injected — ready for approve_spec
)

// SpawnFunc spawns a refinement turn and returns a line stream. Matches
// RunRefinementTurn so the real spawner is injectable (and stubbable in tests).
type SpawnFunc func(ctx context.Context, cfg SpawnConfig, sp *ent.Spawner) (<-chan string, error)

type runState struct {
	status string
	errMsg string
}

// Runner owns refinement runs: it spawns claude in a background goroutine
// (decoupled from any HTTP request), tracks per-task status in memory, and
// persists the assistant turn on completion.
type Runner struct {
	turns repo.RefinementTurnRepo
	spawn SpawnFunc

	mu          sync.Mutex
	runs        map[string]*runState
	onRunChange func(taskID string)
}

// NewRunner builds a Runner. spawn defaults to RunRefinementTurn when nil.
func NewRunner(turns repo.RefinementTurnRepo, spawn SpawnFunc) *Runner {
	if spawn == nil {
		spawn = RunRefinementTurn
	}
	return &Runner{turns: turns, spawn: spawn, runs: make(map[string]*runState)}
}

// SetOnRunChange late-binds the status-change callback (the composition root
// wires this to a Task-SSE broadcast after the tasks handler is constructed).
func (r *Runner) SetOnRunChange(fn func(taskID string)) { r.onRunChange = fn }

// State returns the current status and last error for a task.
func (r *Runner) State(taskID string) (status, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.runs[taskID]
	if !ok {
		return StatusNone, ""
	}
	return s.status, s.errMsg
}

// IsRunning reports whether a run is currently in flight for the task.
func (r *Runner) IsRunning(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.runs[taskID]
	return ok && s.status == StatusRefining
}

// Start spawns a refinement run in a detached background goroutine and returns
// a tee channel of output lines for the caller (HTTP handler) to forward live.
// The run owns persistence: it writes the assistant turn and updates status
// even if the caller stops reading the tee channel (e.g. client disconnect).
func (r *Runner) Start(taskID string, cfg SpawnConfig, sp *ent.Spawner) (<-chan string, error) {
	r.mu.Lock()
	if s, ok := r.runs[taskID]; ok && s.status == StatusRefining {
		r.mu.Unlock()
		return nil, ErrAlreadyRunning
	}
	r.runs[taskID] = &runState{status: StatusRefining}
	r.mu.Unlock()
	if r.onRunChange != nil {
		r.onRunChange(taskID)
	}

	// Background context — NOT tied to the request. Bounded by runTimeout.
	runCtx, cancel := context.WithTimeout(context.Background(), runTimeout)

	stream, err := r.spawn(runCtx, cfg, sp)
	if err != nil {
		cancel()
		r.setState(taskID, StatusFailed, err.Error())
		return nil, err
	}

	out := make(chan string, 64)
	go func() {
		defer cancel()
		defer close(out)

		var sb strings.Builder
		for line := range stream {
			sb.WriteString(line)
			sb.WriteString("\n")
			// Best-effort tee: never block persistence on a slow/absent reader.
			select {
			case out <- line:
			default:
			}
		}

		resp := strings.TrimRight(sb.String(), "\n")
		switch {
		case resp == "":
			r.setState(taskID, StatusFailed, "no output from refinement agent")
		case strings.HasPrefix(resp, "[ERROR]"):
			r.setState(taskID, StatusFailed, resp)
		default:
			cleaned, phases := ExtractPhases(resp)
			cleaned, options := ExtractOptions(cleaned)
			in := repo.CreateTurnInput{
				TaskID:  taskID,
				Role:    "assistant",
				Content: cleaned,
				Options: options,
			}
			if len(phases) > 0 {
				last := phases[len(phases)-1]
				in.Phase = &last
			}
			_, _ = r.turns.Create(context.Background(), in)
			r.setState(taskID, StatusDraftReady, "")
		}
	}()

	return out, nil
}

func (r *Runner) setState(taskID, status, errMsg string) {
	r.mu.Lock()
	r.runs[taskID] = &runState{status: status, errMsg: errMsg}
	r.mu.Unlock()
	if r.onRunChange != nil {
		r.onRunChange(taskID)
	}
}

// MarkDraftReady sets the task's refine status to StatusDraftReady without
// spawning an agent. Called by InjectConcept after persisting the concept turn.
func (r *Runner) MarkDraftReady(taskID string) {
	r.setState(taskID, StatusDraftReady, "")
}
