package pipeline

import "github.com/lx-wnk/kontor/server/internal/db/ent"

const maxStageWalk = 32

// remainingAgentStages lists the agent stages a task still runs, from its
// current stage on. The walk is bounded so a malformed kind sequence cannot loop.
func remainingAgentStages(task *ent.Task, requiresAgent func(stage string) bool) []string {
	var stages []string
	stage := task.CurrentStage
	for range maxStageWalk {
		if IsTerminalStage(stage) {
			break
		}
		if requiresAgent(stage) {
			stages = append(stages, stage)
		}
		stage = nextStage(task, stage)
	}
	return stages
}
