package askq

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T, name string) []string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return strings.Split(string(content), "\n")
}

func optionLabels(q *DetectedQuestion) []string {
	labels := make([]string, len(q.Options))
	for i, o := range q.Options {
		labels[i] = o.Label
	}
	return labels
}

func findOption(q *DetectedQuestion, label string) *DetectedOption {
	for i := range q.Options {
		if q.Options[i].Label == label {
			return &q.Options[i]
		}
	}
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDetectQuestionFixtures(t *testing.T) {
	t.Run("single-select", func(t *testing.T) {
		q := DetectQuestion(loadFixture(t, "askq-single.txt"))
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if q.MultiSelect {
			t.Error("expected MultiSelect=false")
		}
		if got := optionLabels(q); !equalStrings(got, []string{"Red", "Green", "Blue"}) {
			t.Errorf("labels = %v, want [Red Green Blue]", got)
		}
		if q.TypeSomethingIndex != 4 {
			t.Errorf("TypeSomethingIndex = %d, want 4", q.TypeSomethingIndex)
		}
		if q.ChatAboutIndex != 5 {
			t.Errorf("ChatAboutIndex = %d, want 5", q.ChatAboutIndex)
		}
		red := findOption(q, "Red")
		if red == nil || red.Description != "A warm colour" {
			t.Errorf("Red description = %+v, want 'A warm colour'", red)
		}
		if q.Question != "What is your favourite colour?" {
			t.Errorf("Question = %q, want 'What is your favourite colour?'", q.Question)
		}
	})

	t.Run("multi-select", func(t *testing.T) {
		q := DetectQuestion(loadFixture(t, "askq-multi.txt"))
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if !q.MultiSelect {
			t.Error("expected MultiSelect=true")
		}
		if got := optionLabels(q); !equalStrings(got, []string{"Apples", "Bananas", "Cherries"}) {
			t.Errorf("labels = %v, want [Apples Bananas Cherries]", got)
		}
		if q.TypeSomethingIndex != 4 {
			t.Errorf("TypeSomethingIndex = %d, want 4", q.TypeSomethingIndex)
		}
		if q.ChatAboutIndex != 5 {
			t.Errorf("ChatAboutIndex = %d, want 5", q.ChatAboutIndex)
		}
		apples := findOption(q, "Apples")
		if apples == nil || apples.Description != "Crisp and sweet" {
			t.Errorf("Apples description = %+v, want 'Crisp and sweet'", apples)
		}
	})

	t.Run("nonmodal", func(t *testing.T) {
		q := DetectQuestion(loadFixture(t, "askq-nonmodal.txt"))
		if q != nil {
			t.Errorf("expected nil, got %+v", q)
		}
	})

	t.Run("v2.1.205 render with trailing-period meta-row", func(t *testing.T) {
		q := DetectQuestion(loadFixture(t, "askq-v2_1_205.txt"))
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if q.MultiSelect {
			t.Error("expected MultiSelect=false")
		}
		if got := optionLabels(q); !equalStrings(got, []string{"Red", "Green", "Blue"}) {
			t.Errorf("labels = %v, want [Red Green Blue]", got)
		}
		if q.TypeSomethingIndex != 4 {
			t.Errorf("TypeSomethingIndex = %d, want 4", q.TypeSomethingIndex)
		}
		if q.ChatAboutIndex != 5 {
			t.Errorf("ChatAboutIndex = %d, want 5", q.ChatAboutIndex)
		}
		red := findOption(q, "Red")
		if red == nil || red.Description != "Warm, high-energy hue." {
			t.Errorf("Red description = %+v, want 'Warm, high-energy hue.'", red)
		}
	})
}

func TestDetectQuestionInline(t *testing.T) {
	t.Run("no meta rows returns nil", func(t *testing.T) {
		q := DetectQuestion([]string{"1. Red", "2. Green", "3. Blue"})
		if q != nil {
			t.Errorf("expected nil, got %+v", q)
		}
	})

	t.Run("no border, leading/trailing whitespace", func(t *testing.T) {
		raw := []string{
			"  Pick a colour  ",
			"",
			"  What is your favourite colour?  ",
			"",
			"  1. Red",
			"  2. Green",
			"  3. Type something",
			"  4. Chat about this",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if got := optionLabels(q); !equalStrings(got, []string{"Red", "Green"}) {
			t.Errorf("labels = %v, want [Red Green]", got)
		}
		if q.MultiSelect {
			t.Error("expected MultiSelect=false")
		}
	})

	t.Run("no modal present", func(t *testing.T) {
		q := DetectQuestion([]string{"just a prompt", "> "})
		if q != nil {
			t.Errorf("expected nil, got %+v", q)
		}
	})

	t.Run("type something without chat about this returns nil", func(t *testing.T) {
		raw := []string{
			"To add an icon:",
			"1. Click icon",
			"2. Type something",
			"3. Press enter",
		}
		q := DetectQuestion(raw)
		if q != nil {
			t.Errorf("expected nil, got %+v", q)
		}
	})

	t.Run("toggle wording in question does not flip multiSelect", func(t *testing.T) {
		raw := []string{
			"Would you like to toggle X?",
			"",
			"1. Yes",
			"   press space to confirm later",
			"2. No",
			"3. Type something",
			"4. Chat about this",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if q.MultiSelect {
			t.Error("expected MultiSelect=false")
		}
	})

	t.Run("index invariant for 1-option modal", func(t *testing.T) {
		raw := []string{
			"Confirm",
			"Proceed?",
			"1. Yes",
			"2. Type something",
			"3. Chat about this",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if len(q.Options) != 1 {
			t.Errorf("len(Options) = %d, want 1", len(q.Options))
		}
		if q.TypeSomethingIndex != len(q.Options)+1 {
			t.Errorf("TypeSomethingIndex = %d, want %d", q.TypeSomethingIndex, len(q.Options)+1)
		}
		if q.ChatAboutIndex != len(q.Options)+2 {
			t.Errorf("ChatAboutIndex = %d, want %d", q.ChatAboutIndex, len(q.Options)+2)
		}
	})

	t.Run("index invariant for 5-option modal", func(t *testing.T) {
		raw := []string{
			"Pick one",
			"Which number?",
			"1. One",
			"2. Two",
			"3. Three",
			"4. Four",
			"5. Five",
			"6. Type something",
			"7. Chat about this",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if len(q.Options) != 5 {
			t.Errorf("len(Options) = %d, want 5", len(q.Options))
		}
		if q.TypeSomethingIndex != 6 {
			t.Errorf("TypeSomethingIndex = %d, want 6", q.TypeSomethingIndex)
		}
		if q.ChatAboutIndex != 7 {
			t.Errorf("ChatAboutIndex = %d, want 7", q.ChatAboutIndex)
		}
	})

	t.Run("rejects frame with numbered description line (index desync)", func(t *testing.T) {
		raw := []string{
			"Pick a colour",
			"What is your favourite colour?",
			"1. Red",
			"   2. items match",
			"2. Green",
			"3. Type something",
			"4. Chat about this",
		}
		q := DetectQuestion(raw)
		if q != nil {
			t.Errorf("expected nil, got %+v", q)
		}
	})

	t.Run("multi-select from checkboxes alone with no footer hint", func(t *testing.T) {
		raw := []string{
			"Pick fruits",
			"Which fruits?",
			"1. [ ] Apples",
			"2. [✔] Bananas",
			"3. Type something",
			"4. Chat about this",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if !q.MultiSelect {
			t.Error("expected MultiSelect=true")
		}
	})

	t.Run("falls back to footer toggle hint when no checkboxes", func(t *testing.T) {
		raw := []string{
			"Pick fruits",
			"Which fruits?",
			"1. Apples",
			"2. Bananas",
			"3. Type something",
			"4. Chat about this",
			"Space to toggle · Enter to confirm",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if !q.MultiSelect {
			t.Error("expected MultiSelect=true")
		}
	})

	t.Run("does not flip multiSelect when only some options carry checkboxes", func(t *testing.T) {
		raw := []string{
			"Pick fruits",
			"Which fruits?",
			"1. [ ] Apples",
			"2. Bananas",
			"3. Type something",
			"4. Chat about this",
		}
		q := DetectQuestion(raw)
		if q == nil {
			t.Fatal("expected non-nil DetectedQuestion")
		}
		if q.MultiSelect {
			t.Error("expected MultiSelect=false")
		}
	})
}

func TestDetectConfirmScreen(t *testing.T) {
	t.Run("detects the review/submit screen from a real render", func(t *testing.T) {
		c := DetectConfirmScreen(loadFixture(t, "askq-confirm.txt"))
		if c == nil {
			t.Fatal("expected non-nil DetectedConfirm")
		}
		if c.Question != "Ready to submit your answers?" {
			t.Errorf("Question = %q, want the prompt line, not an answer recap line", c.Question)
		}
		if len(c.Options) != 2 {
			t.Fatalf("len(Options) = %d, want 2", len(c.Options))
		}
		if c.Options[0].Index != 1 || c.Options[0].Label != "Submit answers" {
			t.Errorf("Options[0] = %+v, want {1 Submit answers}", c.Options[0])
		}
		if c.Options[1].Index != 2 || c.Options[1].Label != "Cancel" {
			t.Errorf("Options[1] = %+v, want {2 Cancel}", c.Options[1])
		}
	})

	// The confirm screen is what DetectQuestion is designed to reject (no
	// meta-rows); asserting it here pins WHY the second detector has to exist.
	t.Run("is rejected by DetectQuestion", func(t *testing.T) {
		if q := DetectQuestion(loadFixture(t, "askq-confirm.txt")); q != nil {
			t.Fatalf("DetectQuestion matched the confirm screen: %+v", q)
		}
	})

	t.Run("tolerates copy drift in the submit label", func(t *testing.T) {
		raw := []string{
			"Ready to submit your answers?",
			"❯ 1. Submit",
			"  2. Cancel",
		}
		if c := DetectConfirmScreen(raw); c == nil {
			t.Fatal("expected non-nil DetectedConfirm for a bare \"Submit\" label")
		}
	})

	t.Run("rejects a real question modal", func(t *testing.T) {
		if c := DetectConfirmScreen(loadFixture(t, "askq-single.txt")); c != nil {
			t.Fatalf("matched a question modal: %+v", c)
		}
	})

	t.Run("rejects an ordinary two-item numbered list", func(t *testing.T) {
		raw := []string{
			"Files changed:",
			"1. server/main.go",
			"2. README.md",
		}
		if c := DetectConfirmScreen(raw); c != nil {
			t.Fatalf("matched ordinary output: %+v", c)
		}
	})

	t.Run("rejects a submit/cancel pair with no preamble", func(t *testing.T) {
		raw := []string{
			"❯ 1. Submit answers",
			"  2. Cancel",
		}
		if c := DetectConfirmScreen(raw); c != nil {
			t.Fatalf("matched a bare option pair: %+v", c)
		}
	})

	t.Run("rejects a two-option modal that still carries meta-rows", func(t *testing.T) {
		raw := []string{
			"Submit?",
			"1. Submit answers",
			"2. Cancel",
			"3. Type something",
			"4. Chat about this",
		}
		if c := DetectConfirmScreen(raw); c != nil {
			t.Fatalf("matched a question modal: %+v", c)
		}
	})
}

func TestDetectScreen(t *testing.T) {
	t.Run("reports a question modal", func(t *testing.T) {
		s := DetectScreen(loadFixture(t, "askq-single.txt"))
		if s == nil || s.Question == nil {
			t.Fatalf("expected a question screen, got %+v", s)
		}
		if s.Confirm != nil {
			t.Error("expected Confirm to stay nil")
		}
	})

	t.Run("reports the confirm screen", func(t *testing.T) {
		s := DetectScreen(loadFixture(t, "askq-confirm.txt"))
		if s == nil || s.Confirm == nil {
			t.Fatalf("expected a confirm screen, got %+v", s)
		}
		if s.Question != nil {
			t.Error("expected Question to stay nil")
		}
	})

	t.Run("reports nil for ordinary output", func(t *testing.T) {
		if s := DetectScreen(loadFixture(t, "askq-nonmodal.txt")); s != nil {
			t.Fatalf("matched ordinary output: %+v", s)
		}
	})
}

// A borderless v2.1.220 render can bleed the modal's right border into a
// content line; the detector must not carry that into the question text.
func TestDetectQuestion_TrimsBorderBleed(t *testing.T) {
	q := DetectQuestion([]string{
		"Welches Tier soll es sein?─────────────────────────────────────────────╯",
		"❯ 1. Katze",
		"  2. Hund",
		"  3. Type something.",
		"  4. Chat about this",
	})
	if q == nil {
		t.Fatal("expected non-nil DetectedQuestion")
	}
	if q.Question != "Welches Tier soll es sein?" {
		t.Errorf("Question = %q, want the border run stripped", q.Question)
	}
	if q.Options[0].Label != "Katze" {
		t.Errorf("Options[0].Label = %q", q.Options[0].Label)
	}
}

// ASCII hyphens are outside the box-drawing range, so a label that legitimately
// ends in one must survive the trim.
func TestDetectQuestion_KeepsTrailingAsciiHyphen(t *testing.T) {
	q := DetectQuestion([]string{
		"Which flag?",
		"❯ 1. --dry-run",
		"  2. Type something.",
		"  3. Chat about this",
	})
	if q == nil {
		t.Fatal("expected non-nil DetectedQuestion")
	}
	if q.Options[0].Label != "--dry-run" {
		t.Errorf("Options[0].Label = %q, want --dry-run", q.Options[0].Label)
	}
}

// Unrelated numbered output can still sit in the viewport above the modal; a
// strict "exactly two numbered rows" count would silently disable detection.
func TestDetectConfirmScreen_IgnoresUnrelatedNumberedLinesAbove(t *testing.T) {
	c := DetectConfirmScreen([]string{
		"Files changed:",
		"1. server/main.go",
		"2. README.md",
		"Ready to submit your answers?",
		"❯ 1. Submit answers",
		"  2. Cancel",
	})
	if c == nil {
		t.Fatal("expected non-nil DetectedConfirm")
	}
	if c.Question != "Ready to submit your answers?" {
		t.Errorf("Question = %q", c.Question)
	}
}

// Adjacency is what keeps the pair a real option block — a content line between
// them means they are not one selector.
func TestDetectConfirmScreen_RejectsNonAdjacentPair(t *testing.T) {
	c := DetectConfirmScreen([]string{
		"Ready to submit your answers?",
		"1. Submit answers",
		"some unrelated line",
		"2. Cancel",
	})
	if c != nil {
		t.Fatalf("matched a non-adjacent pair: %+v", c)
	}
}

func TestDetectDialog(t *testing.T) {
	cases := []struct {
		fixture string
		want    bool
	}{
		{"dialog-login.txt", true},
		{"dialog-model.txt", true},
		{"dialog-hooks.txt", true},
		{"dialog-config.txt", true},
		{"dialog-resume-wrapped.txt", true},
		{"dialog-subview.txt", true},
		{"dialog-trailing-hint.txt", true},
		{"dialog-wrapped-split.txt", true},
		{"dialog-idle.txt", false},
		{"dialog-statusline.txt", false},
		{"dialog-press-esc.txt", true},
		{"dialog-paren.txt", true},
		{"dialog-or-esc.txt", true},
		{"dialog-idle-hint.txt", false},
		{"dialog-double-press.txt", false},
		{"dialog-draft.txt", false},
		{"askq-nonmodal.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			if got := DetectDialog(loadFixture(t, tc.fixture)); got != tc.want {
				t.Errorf("DetectDialog(%s) = %v, want %v", tc.fixture, got, tc.want)
			}
		})
	}
}

func TestDetectScreenPrefersQuestionOverDialog(t *testing.T) {
	screen := DetectScreen(loadFixture(t, "askq-v2_1_205.txt"))
	if screen == nil || screen.Question == nil || screen.Dialog {
		t.Fatalf("an AskUserQuestion modal must be reported as a question, got %+v", screen)
	}
	screen = DetectScreen(loadFixture(t, "dialog-login.txt"))
	if screen == nil || !screen.Dialog || screen.Question != nil {
		t.Fatalf("a CLI picker must be reported as a dialog, got %+v", screen)
	}
}
