package refine

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// jsonBlockRE matches the body of a fenced ```json … ``` block. The agent emits
// one such block at approval describing the finalized concept (see the prompt in
// spawner.go). DOTALL so the block may span multiple lines.
var jsonBlockRE = regexp.MustCompile("(?s)```json\\s*\\n(.*?)```")

// conceptRoutingKeys are lifted out of the raw concept JSON onto dedicated task
// columns; everything else stays in task metadata for the implementation prompt.
var conceptRoutingKeys = []string{"refinedTitle", "sourceBranch", "targetBranch"}

// Concept is the finalized refinement output the implementation stage consumes.
type Concept struct {
	// Raw is the full parsed JSON object the agent emitted.
	Raw map[string]any
	// RefinedTitle / SourceBranch / TargetBranch are routing fields applied to
	// task columns (the rest of Raw becomes task metadata).
	RefinedTitle string
	SourceBranch string
	TargetBranch string
}

// ExtractConcept scans turns newest-first for an assistant turn containing a
// fenced ```json concept block and returns the parsed concept. ok is false when
// no parseable concept block exists (older agents, or an aborted refinement) so
// callers can fall back gracefully instead of clobbering the task with empty data.
func ExtractConcept(turns []*ent.RefinementTurn) (Concept, bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if string(t.Role) != "assistant" {
			continue
		}
		if t.Phase != nil && *t.Phase == "confirmed" {
			continue
		}
		// Use the LAST json block in the turn — if the agent showed an example
		// earlier in the same message, the finalized concept comes last.
		matches := jsonBlockRE.FindAllStringSubmatch(t.Content, -1)
		if len(matches) == 0 {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(matches[len(matches)-1][1]), &raw); err != nil || raw == nil {
			continue
		}
		c := Concept{Raw: raw}
		if s, ok := raw["refinedTitle"].(string); ok {
			c.RefinedTitle = strings.TrimSpace(s)
		}
		if s, ok := raw["sourceBranch"].(string); ok {
			c.SourceBranch = strings.TrimSpace(s)
		}
		if s, ok := raw["targetBranch"].(string); ok {
			c.TargetBranch = strings.TrimSpace(s)
		}
		return c, true
	}
	return Concept{}, false
}

// Metadata returns Raw without the routing keys — the payload stored verbatim on
// the task so the implementation stage prompt renders spec/plan/toolRequests.
func (c Concept) Metadata() map[string]any {
	out := make(map[string]any, len(c.Raw))
	for k, v := range c.Raw {
		out[k] = v
	}
	for _, k := range conceptRoutingKeys {
		delete(out, k)
	}
	return out
}
