package refine

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/refine"
)

// InjectDeps are the dependencies required by InjectConcept.
type InjectDeps struct {
	Turns  repo.RefinementTurnRepo
	Runner *refine.Runner
}

// InjectConcept persists a finished concept as the refinement turn that
// ExtractConcept expects, then marks the refine status as draft_ready.
// After this call, approve_spec / Confirm can freeze the concept onto the task
// without any agent round-trip.
func InjectConcept(ctx context.Context, d InjectDeps, taskID string, c refine.Concept) error {
	// Serialize Raw (which includes routing keys) into the fenced JSON block that
	// ExtractConcept parses. Raw may be nil if the caller omitted it — build it
	// from the typed fields in that case.
	raw := c.Raw
	if raw == nil {
		raw = make(map[string]any)
	}
	// Ensure routing fields are present in raw so ExtractConcept can lift them.
	if c.RefinedTitle != "" {
		raw["refinedTitle"] = c.RefinedTitle
	}
	if c.SourceBranch != "" {
		raw["sourceBranch"] = c.SourceBranch
	}
	if c.TargetBranch != "" {
		raw["targetBranch"] = c.TargetBranch
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("inject: marshal concept: %w", err)
	}

	// ExtractConcept matches "(?s)```json\s*\n(.*?)```" and parses the capture
	// group; wrap the JSON in exactly that fence.
	content := "```json\n" + string(encoded) + "\n```"

	if _, err := d.Turns.Create(ctx, repo.CreateTurnInput{
		TaskID:  taskID,
		Role:    "assistant",
		Content: content,
	}); err != nil {
		return fmt.Errorf("inject: persist turn: %w", err)
	}

	if d.Runner != nil {
		d.Runner.MarkDraftReady(taskID)
	}
	return nil
}
