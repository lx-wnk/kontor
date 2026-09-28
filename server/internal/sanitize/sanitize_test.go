package sanitize_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lx-wnk/kontor/server/internal/sanitize"
)

func TestForDisplayStripsDeceptiveRunes(t *testing.T) {
	// Escaped, not written literally: a raw U+202E in this file would reverse
	// the rendering of the source line that documents it, which is the very
	// attack the function under test defends against.
	got := sanitize.ForDisplay("echo safe\u202e hs | hs.live//:ptth lruc\u200b")
	for _, bad := range []rune{'\u202e', '\u200b'} {
		if strings.ContainsRune(got, bad) {
			t.Errorf("result still carries %U: %q", bad, got)
		}
	}
	if got == "" {
		t.Fatal("stripping removed everything")
	}
}

func TestForDisplayCollapsesToOneLine(t *testing.T) {
	cases := map[string]string{
		"a\n\n  b":     "a b",
		"  lead":       "lead",
		"trail   ":     "trail",
		"a\tb\r\nc":    "a b c",
		"one":          "one",
		"":             "",
		"   \t\n  ":    "",
		"a \u200b\n b": "a b",
	}
	for in, want := range cases {
		if got := sanitize.ForDisplay(in); got != want {
			t.Errorf("ForDisplay(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestForDisplayCappedReportsTheCut(t *testing.T) {
	got, dropped := sanitize.ForDisplayCapped(strings.Repeat("x", 150), 120)

	if got != strings.Repeat("x", 120) {
		t.Fatalf("kept %d runes, want exactly the first 120", utf8.RuneCountInString(got))
	}
	if dropped != 30 {
		t.Fatalf("dropped = %d, want 30", dropped)
	}
}

func TestForDisplayCappedCutsOnARuneBoundary(t *testing.T) {
	// One ASCII byte then 3-byte runes, so a byte-oriented cut at 120 lands
	// inside a rune. A 2-byte rune would divide 120 evenly and pass by accident.
	got, dropped := sanitize.ForDisplayCapped("x"+strings.Repeat("€", 200), 120)

	if !utf8.ValidString(got) {
		t.Fatalf("the cut produced invalid UTF-8: %q", got)
	}
	if n := utf8.RuneCountInString(got); n != 120 {
		t.Fatalf("kept %d runes, want 120", n)
	}
	if dropped != 81 {
		t.Fatalf("dropped = %d, want 81", dropped)
	}
}

// The count exists so the client can say how much is missing without the text
// being able to claim it: nothing about the cut is written into the string.
func TestForDisplayCappedWritesNoMarker(t *testing.T) {
	got, _ := sanitize.ForDisplayCapped(strings.Repeat("x", 200), 120)
	for _, marker := range []string{"…", "...", "chars", "+"} {
		if strings.Contains(got, marker) {
			t.Fatalf("the result carries an in-band cut marker %q: %q", marker, got)
		}
	}
}

// Text that merely looks cut must not be reported as cut.
func TestForDisplayCappedDoesNotTrustAForgedMarker(t *testing.T) {
	in := "echo done… (+400 chars)"
	got, dropped := sanitize.ForDisplayCapped(in, 120)

	if dropped != 0 {
		t.Fatalf("dropped = %d for text well under the cap, want 0", dropped)
	}
	if got != in {
		t.Fatalf("result = %q, want the input unchanged", got)
	}
}

// A negative cap means "no cap" — the shape ForDisplay relies on.
func TestForDisplayCappedUncapped(t *testing.T) {
	in := strings.Repeat("y", 5000)
	got, dropped := sanitize.ForDisplayCapped(in, -1)
	if got != in || dropped != 0 {
		t.Fatalf("uncapped call altered the input (dropped=%d, len=%d)", dropped, len(got))
	}
}

// Collapsed whitespace counts against the cap, so a run of spaces cannot smuggle
// extra content past it.
func TestForDisplayCappedCountsTheSeparator(t *testing.T) {
	got, dropped := sanitize.ForDisplayCapped("ab   cd", 3)
	if got != "ab" {
		t.Fatalf("result = %q, want %q — the separator did not count", got, "ab")
	}
	if dropped != 2 {
		t.Fatalf("dropped = %d, want 2 (c and d)", dropped)
	}
}

func TestForStoragePreservesNewlinesButStripsDeceptiveRunes(t *testing.T) {
	in := "line one\u200b\nline two\u202e\n\nline three"
	got := sanitize.ForStorage(in)
	want := "line one\nline two\n\nline three"
	if got != want {
		t.Errorf("ForStorage(%q) = %q, want %q", in, got, want)
	}
	for _, bad := range []rune{'\u202e', '\u200b'} {
		if strings.ContainsRune(got, bad) {
			t.Errorf("result still carries %U: %q", bad, got)
		}
	}
}

func TestForStorageKeepsIndentation(t *testing.T) {
	in := "func f() {\n\treturn nil\n}"
	if got := sanitize.ForStorage(in); got != in {
		t.Errorf("ForStorage(%q) = %q, want unchanged (tabs are whitespace, not control)", in, got)
	}
}

// The whole point of the one-pass form: cost must not scale with the length of
// text that is thrown away.
func BenchmarkForDisplayCappedLongInput(b *testing.B) {
	in := strings.Repeat("some inline script content ", 4000) // ~108 KB
	for b.Loop() {
		sanitize.ForDisplayCapped(in, 120)
	}
}
