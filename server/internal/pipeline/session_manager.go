package pipeline

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/proc"
)

type RecoveryDecision struct {
	Kind   string
	Reason string
}

// stageRunPid returns the PID recorded on the stage_run, or 0 when none was.
// proc.IsPidAlive(0) is false, so an absent PID reads as "not alive" — correct
// only where a PID-less run really is a zombie. It is not correct for an
// HTTP-adapter stage, which runs with no local process at all; see the
// process-lifetime guard in sweepOrphanRuns case 4.
func stageRunPid(sr *ent.StageRun) int {
	if sr.Pid == nil {
		return 0
	}
	return *sr.Pid
}

func DecideRecovery(sr *ent.StageRun) RecoveryDecision {
	pid := stageRunPid(sr)
	if proc.IsPidAlive(pid) {
		return RecoveryDecision{Kind: "alive", Reason: fmt.Sprintf("PID %d still running", pid)}
	}
	if sr.SessionID != nil && *sr.SessionID != "" {
		return RecoveryDecision{Kind: "resume", Reason: fmt.Sprintf("session %s available for --resume", *sr.SessionID)}
	}
	return RecoveryDecision{Kind: "restart", Reason: "no live PID and no session — must start fresh"}
}

func BuildSessionName(taskSlug, stage string, iteration int) string {
	return fmt.Sprintf("%s-%s-iter-%d", taskSlug, stage, iteration)
}

func AttachSessionID(ctx context.Context, stageRunID, sessionID string, srRepo repo.StageRunRepo) error {
	existing, err := srRepo.GetBySessionID(ctx, sessionID)
	if err == nil && existing != nil {
		return nil
	}
	sid := sessionID
	_, err = srRepo.Update(ctx, stageRunID, repo.UpdateStageRunInput{SessionID: &sid})
	if err != nil {
		return fmt.Errorf("AttachSessionID: %w", err)
	}
	return nil
}
