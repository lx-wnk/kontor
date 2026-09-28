package parser_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

func writeLines(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	for _, l := range lines {
		fmt.Fprintln(f, l)
	}
	return p
}

func TestScanMessages_UsageAndTimestamp(t *testing.T) {
	ts := "2026-06-29T10:00:00Z"
	path := writeLines(t, []string{
		`{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","model":"claude-sonnet-4-6","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":10,"cache_read_input_tokens":5}}}`,
	})
	var got []parser.Message
	if err := parser.ScanMessages(path, 0, func(m parser.Message) error {
		got = append(got, m)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	m := got[0]
	if m.Type != "assistant" {
		t.Errorf("Type = %q, want assistant", m.Type)
	}
	if m.Role != "assistant" {
		t.Errorf("Role = %q, want assistant", m.Role)
	}
	if m.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6", m.Model)
	}
	if m.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if m.Usage.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", m.Usage.InputTokens)
	}
	if m.Usage.OutputTokens != 50 {
		t.Errorf("OutputTokens = %d, want 50", m.Usage.OutputTokens)
	}
	if m.Usage.CacheCreationTokens != 10 {
		t.Errorf("CacheCreationTokens = %d, want 10", m.Usage.CacheCreationTokens)
	}
	if m.Usage.CacheReadTokens != 5 {
		t.Errorf("CacheReadTokens = %d, want 5", m.Usage.CacheReadTokens)
	}
	want, _ := time.Parse(time.RFC3339, ts)
	if !m.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", m.Timestamp, want)
	}
}

func TestScanMessages_CompactBoundaryPassthrough(t *testing.T) {
	path := writeLines(t, []string{
		`{"type":"system","subtype":"compact_boundary"}`,
	})
	var got []parser.Message
	_ = parser.ScanMessages(path, 0, func(m parser.Message) error {
		got = append(got, m)
		return nil
	})
	if len(got) != 1 {
		t.Fatalf("want 1 message, got %d", len(got))
	}
	if got[0].Type != "system" || got[0].Subtype != "compact_boundary" {
		t.Errorf("unexpected type/subtype: %q/%q", got[0].Type, got[0].Subtype)
	}
}

func TestScanMessages_MalformedLinesSkipped(t *testing.T) {
	path := writeLines(t, []string{
		`{not valid json}`,
		`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":1}}}`,
	})
	var count int
	_ = parser.ScanMessages(path, 0, func(_ parser.Message) error {
		count++
		return nil
	})
	if count != 1 {
		t.Errorf("want 1 message (malformed skipped), got %d", count)
	}
}

func TestScanMessages_ContentRawJSON(t *testing.T) {
	path := writeLines(t, []string{
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","id":"tu_1"}]}}`,
	})
	var got []parser.Message
	_ = parser.ScanMessages(path, 0, func(m parser.Message) error {
		got = append(got, m)
		return nil
	})
	if len(got) != 1 || len(got[0].Content) == 0 {
		t.Fatal("Content should carry the raw JSON array")
	}
	var blocks []struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(got[0].Content, &blocks); err != nil {
		t.Fatalf("unmarshal Content: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Name != "Read" {
		t.Errorf("unexpected blocks: %+v", blocks)
	}
}

func TestScanMessages_ErrStopScan(t *testing.T) {
	path := writeLines(t, []string{
		`{"type":"assistant","message":{"role":"assistant"}}`,
		`{"type":"assistant","message":{"role":"assistant"}}`,
		`{"type":"assistant","message":{"role":"assistant"}}`,
	})
	var count int
	err := parser.ScanMessages(path, 0, func(_ parser.Message) error {
		count++
		if count == 1 {
			return parser.ErrStopScan
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 1 {
		t.Errorf("want 1 call before stop, got %d", count)
	}
}

func TestScanMessages_MissingFile(t *testing.T) {
	err := parser.ScanMessages("/no/such/file.jsonl", 0, func(_ parser.Message) error { return nil })
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestScanMessages_AbsentTimestampIsZero(t *testing.T) {
	path := writeLines(t, []string{
		`{"type":"assistant","message":{"role":"assistant"}}`,
	})
	var got parser.Message
	_ = parser.ScanMessages(path, 0, func(m parser.Message) error {
		got = m
		return parser.ErrStopScan
	})
	if !got.Timestamp.IsZero() {
		t.Errorf("want zero Timestamp for absent field, got %v", got.Timestamp)
	}
}

func TestScanMessages_MaxBytesLimitsRead(t *testing.T) {
	line1 := `{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":999}}}`
	line2 := `{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":1}}}`
	path := writeLines(t, []string{line1, line2})

	maxBytes := int64(len(line2) + 1)
	var tokens []int
	_ = parser.ScanMessages(path, maxBytes, func(m parser.Message) error {
		if m.Usage != nil {
			tokens = append(tokens, m.Usage.InputTokens)
		}
		return nil
	})
	for _, tok := range tokens {
		if tok == 999 {
			t.Errorf("maxBytes tail not respected: decoded first line token count 999")
		}
	}
}
