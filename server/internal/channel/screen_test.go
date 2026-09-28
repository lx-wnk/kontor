package channel

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lx-wnk/kontor/server/internal/askq"
)

func TestEmulatorCROverwritesLine(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("Hello\rWorld"))
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	if rows[0] != "World" {
		t.Errorf("row = %q, want %q", rows[0], "World")
	}
}

func TestEmulatorLFAdvancesRow(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("one\r\ntwo"))
	rows := s.Rows()
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want 2 rows", rows)
	}
	if rows[0] != "one" || rows[1] != "two" {
		t.Errorf("rows = %v, want [one two]", rows)
	}
}

func TestEmulatorAbsoluteCUPPlacesText(t *testing.T) {
	s := newScreen(20)
	// CUP row 3, col 5 (1-based) -> row idx 2, col idx 4.
	s.Write([]byte("\x1b[3;5Hhi"))
	rows := s.Rows()
	if len(rows) != 3 {
		t.Fatalf("rows = %v, want 3 rows", rows)
	}
	if rows[2] != "    hi" {
		t.Errorf("row[2] = %q, want %q", rows[2], "    hi")
	}
}

func TestEmulatorCUPHomeDefaultsToOrigin(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("abc\x1b[Hxy"))
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	if rows[0] != "xyc" {
		t.Errorf("row = %q, want %q", rows[0], "xyc")
	}
}

func TestEmulatorEraseDisplayClearsScreen(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("one\r\ntwo\r\nthree"))
	s.Write([]byte("\x1b[2J"))
	rows := s.Rows()
	if len(rows) != 0 {
		t.Errorf("rows = %v, want empty after ESC[2J", rows)
	}
}

func TestEmulatorEraseLineToEOL(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("Hello World"))
	// Move cursor back to col 5 (0-based) then erase to end of line.
	s.Write([]byte("\x1b[6G\x1b[K"))
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	if rows[0] != "Hello" {
		t.Errorf("row = %q, want %q", rows[0], "Hello")
	}
}

func TestEmulatorSGRStripped(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("\x1b[91mred\x1b[39m plain\x1b[0m"))
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	if rows[0] != "red plain" {
		t.Errorf("row = %q, want %q", rows[0], "red plain")
	}
}

func TestEmulatorOSCConsumedWithoutCorruption(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("\x1b]0;window title\x07visible"))
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	if rows[0] != "visible" {
		t.Errorf("row = %q, want %q", rows[0], "visible")
	}
}

func TestEmulatorUnknownPrivateCSIIgnored(t *testing.T) {
	s := newScreen(20)
	s.Write([]byte("\x1b[?25h\x1b[?2026l\x1b[<u\x1b[>1u\x1b[>4;2mvisible"))
	rows := s.Rows()
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want 1 row", rows)
	}
	if rows[0] != "visible" {
		t.Errorf("row = %q, want %q", rows[0], "visible")
	}
}

func TestRenderRowsDetectsAskUserQuestionFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "askq_raw_v2_1_205.bin"))
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}

	rows := renderRows(raw)

	q := askq.DetectQuestion(rows)
	if q == nil {
		t.Fatalf("DetectQuestion returned nil; rendered rows:\n%s", dumpRows(rows))
	}
	if q.MultiSelect {
		t.Errorf("MultiSelect = true, want false")
	}

	gotLabels := make([]string, len(q.Options))
	for i, o := range q.Options {
		gotLabels[i] = o.Label
	}
	wantLabels := []string{"Red", "Green", "Blue"}
	if len(gotLabels) != len(wantLabels) {
		t.Fatalf("labels = %v, want %v", gotLabels, wantLabels)
	}
	for i := range wantLabels {
		if gotLabels[i] != wantLabels[i] {
			t.Errorf("labels = %v, want %v", gotLabels, wantLabels)
			break
		}
	}

	if q.TypeSomethingIndex != 4 {
		t.Errorf("TypeSomethingIndex = %d, want 4", q.TypeSomethingIndex)
	}
	if q.ChatAboutIndex != 5 {
		t.Errorf("ChatAboutIndex = %d, want 5", q.ChatAboutIndex)
	}
}

func dumpRows(rows []string) string {
	out := ""
	for _, r := range rows {
		out += r + "\n"
	}
	return out
}
