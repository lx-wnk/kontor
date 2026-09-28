package refine

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/ent/refinementturn"
)

func turn(role, content string, phase *string) *ent.RefinementTurn {
	return &ent.RefinementTurn{Role: refinementturn.Role(role), Content: content, Phase: phase}
}

func TestExtractConcept_ParsesFinalJSONBlock(t *testing.T) {
	confirmed := "confirmed"
	turns := []*ent.RefinementTurn{
		turn("user", "build the EPS price serializer", nil),
		turn("assistant", "Here is the analysis…", strPtr("analysis")),
		turn("assistant", "Plan ready.\n\n```json\n{\n  \"refinedTitle\": \"Switch BocPrice to JSON\",\n  \"spec\": \"serialize→JSON on both ends\",\n  \"plan\": [\"add reindex\", \"flip serializer\"],\n  \"toolRequests\": [\"Bash\", \"Edit\"],\n  \"sourceBranch\": \"users/claude/eps-search-review-fixes\"\n}\n```\n", strPtr("approval")),
		// The confirm sentinel must be ignored.
		turn("assistant", "confirmed", &confirmed),
	}

	c, ok := ExtractConcept(turns)
	if !ok {
		t.Fatal("expected a concept to be extracted")
	}
	if c.RefinedTitle != "Switch BocPrice to JSON" {
		t.Errorf("refinedTitle = %q", c.RefinedTitle)
	}
	if c.SourceBranch != "users/claude/eps-search-review-fixes" {
		t.Errorf("sourceBranch = %q", c.SourceBranch)
	}

	meta := c.Metadata()
	if _, present := meta["refinedTitle"]; present {
		t.Error("routing key refinedTitle must NOT be in metadata")
	}
	if _, present := meta["sourceBranch"]; present {
		t.Error("routing key sourceBranch must NOT be in metadata")
	}
	if meta["spec"] != "serialize→JSON on both ends" {
		t.Errorf("metadata.spec = %v", meta["spec"])
	}
	plan, ok := meta["plan"].([]any)
	if !ok || len(plan) != 2 {
		t.Errorf("metadata.plan = %v", meta["plan"])
	}
}

func TestExtractConcept_NoBlockReturnsFalse(t *testing.T) {
	turns := []*ent.RefinementTurn{
		turn("user", "do something", nil),
		turn("assistant", "Sure, here is some prose with no json block.", strPtr("approval")),
	}
	if _, ok := ExtractConcept(turns); ok {
		t.Error("expected ok=false when no json concept block is present")
	}
}

func strPtr(s string) *string { return &s }
