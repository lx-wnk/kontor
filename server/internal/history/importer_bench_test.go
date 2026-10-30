package history

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkExtractCostRow measures the full extractCostRow hot path (stat +
// read + parse) against a large synthetic JSONL file. The fixture is written
// once before the timed loop so only the parse+IO cycle is measured.
//
// Fixture layout: 5000 filler lines (attachment/user) interleaved with 50
// assistant-usage lines → 5050 lines total, well above the 32KB tail window.
func BenchmarkExtractCostRow(b *testing.B) {
	const (
		fillerCount    = 5000
		assistantCount = 50
	)

	filler := `{"type":"attachment","message":{"role":"user"}}` + "\n"
	assistant := buildJSONLLine(120, 45, "claude-opus-4", "2025-06-01T10:00:00.000Z") + "\n"

	// Interleave: one assistant line every 100 filler lines.
	var sb strings.Builder
	assistantsWritten := 0
	for i := 0; i < fillerCount; i++ {
		sb.WriteString(filler)
		if (i+1)%100 == 0 && assistantsWritten < assistantCount {
			sb.WriteString(assistant)
			assistantsWritten++
		}
	}
	fixture := sb.String()
	b.Logf("fixture: %d lines, %d bytes", fillerCount+assistantsWritten, len(fixture))

	// Write fixture once to a temp file; reuse across all b.N iterations.
	dir := b.TempDir()
	path := filepath.Join(dir, "bench-session.jsonl")
	if err := os.WriteFile(path, []byte(fixture), 0o644); err != nil {
		b.Fatalf("write fixture: %v", err)
	}

	imp := NewImporter(&stubCostRepo{})
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = imp.extractCostRow(ctx, path)
	}
}

// BenchmarkParseSession isolates the pure in-memory parse loop,
// removing disk I/O from the measurement so allocations from JSON decoding
// and string splitting can be profiled independently.
func BenchmarkParseSession(b *testing.B) {
	const (
		fillerCount    = 5000
		assistantCount = 50
	)

	filler := `{"type":"attachment","message":{"role":"user"}}` + "\n"
	assistant := buildJSONLLine(120, 45, "claude-opus-4", "2025-06-01T10:00:00.000Z") + "\n"

	var sb strings.Builder
	assistantsWritten := 0
	for i := 0; i < fillerCount; i++ {
		sb.WriteString(filler)
		if (i+1)%100 == 0 && assistantsWritten < assistantCount {
			sb.WriteString(assistant)
			assistantsWritten++
		}
	}
	raw := sb.String()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = parseSession(strings.NewReader(raw))
	}
}
