package schedules

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

const maxRoutineRuns = 50

// routineRunView is one task's run summary within a routine's run list.
type routineRunView struct {
	TaskID    string  `json:"taskId"`
	Title     string  `json:"title"`
	Kind      string  `json:"kind"`
	Stage     string  `json:"stage"`
	Status    string  `json:"status"`
	Summary   string  `json:"summary"`
	CostCents int     `json:"costCents"`
	StartedAt *string `json:"startedAt,omitempty"`
	EndedAt   *string `json:"endedAt,omitempty"`
}

func isTerminalStage(stage string) bool {
	return stage == "done" || stage == "cancelled"
}

// ponytail: one stage-run query per task, 50 tasks max; batch by task ids if routine pages get slow.
func toRoutineRunView(t *ent.Task, runs []*ent.StageRun) routineRunView {
	v := routineRunView{
		TaskID: t.ID,
		Title:  t.Title,
		Kind:   t.Kind,
		Stage:  t.CurrentStage,
		Status: t.CurrentStage,
	}
	if len(runs) == 0 {
		return v
	}

	var costCents int
	var earliestStart *ent.StageRun
	for _, run := range runs {
		costCents += run.CostCents
		if run.StartedAt != nil && (earliestStart == nil || run.StartedAt.Before(*earliestStart.StartedAt)) {
			earliestStart = run
		}
	}
	v.CostCents = costCents

	latest := runs[len(runs)-1]
	v.Status = latest.Status
	if summary, ok := latest.Output["summary"].(string); ok {
		v.Summary = summary
	}
	if earliestStart != nil {
		started := earliestStart.StartedAt.UTC().Format(isoFmt)
		v.StartedAt = &started
	}
	if isTerminalStage(t.CurrentStage) && latest.EndedAt != nil {
		e := latest.EndedAt.UTC().Format(isoFmt)
		v.EndedAt = &e
	}
	return v
}

// listRuns returns a routine's task runs, newest first, with a per-task cost
// and status summary derived from that task's stage runs.
func (h *Handler) listRuns(w http.ResponseWriter, r *http.Request) error {
	id := chi.URLParam(r, "id")
	if _, err := h.repo.GetByID(r.Context(), id); err != nil {
		if ent.IsNotFound(err) {
			return apierr.ErrNotFound
		}
		return err
	}
	tasksList, err := h.tasks.ListByRoutine(r.Context(), id, maxRoutineRuns)
	if err != nil {
		return err
	}
	views := make([]routineRunView, len(tasksList))
	for i, t := range tasksList {
		runs, err := h.stageRuns.ListForTask(r.Context(), t.ID)
		if err != nil {
			return err
		}
		views[i] = toRoutineRunView(t, runs)
	}
	return jsonReply(w, http.StatusOK, views)
}
