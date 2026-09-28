package scanner_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/scanner"
	"github.com/stretchr/testify/require"
)

// TestParseLsofBatch_SingleEntry verifies a single pid→cwd pair.
func TestParseLsofBatch_SingleEntry(t *testing.T) {
	input := "p42\nn/home/dev/myproject\n"
	got := scanner.ParseLsofBatch(input)
	require.Equal(t, "/home/dev/myproject", got[42])
	require.Len(t, got, 1)
}

// TestParseLsofBatch_MultipleEntries verifies multiple pid→cwd pairs.
func TestParseLsofBatch_MultipleEntries(t *testing.T) {
	input := "p100\nn/srv/api\np200\nn/home/user/code\np300\nn/tmp/scratch\n"
	got := scanner.ParseLsofBatch(input)
	require.Equal(t, "/srv/api", got[100])
	require.Equal(t, "/home/user/code", got[200])
	require.Equal(t, "/tmp/scratch", got[300])
	require.Len(t, got, 3)
}

// TestParseLsofBatch_PWithNoN verifies that a p-line without a following n-line
// is ignored (no entry in the result).
func TestParseLsofBatch_PWithNoN(t *testing.T) {
	input := "p123\n"
	got := scanner.ParseLsofBatch(input)
	require.Empty(t, got)
}

// TestParseLsofBatch_NWithNoPrecedingP verifies that a stray n-line without
// a preceding p-line is ignored.
func TestParseLsofBatch_NWithNoPrecedingP(t *testing.T) {
	input := "n/some/path\np999\nn/valid/path\n"
	got := scanner.ParseLsofBatch(input)
	require.Len(t, got, 1)
	require.Equal(t, "/valid/path", got[999])
}

// TestParseLsofBatch_InvalidPID verifies that a malformed pid line is skipped.
func TestParseLsofBatch_InvalidPID(t *testing.T) {
	input := "pNaN\nn/some/path\np42\nn/real/path\n"
	got := scanner.ParseLsofBatch(input)
	require.Len(t, got, 1)
	require.Equal(t, "/real/path", got[42])
}

// TestParseLsofBatch_PathWithSpaces verifies that paths containing spaces are preserved.
func TestParseLsofBatch_PathWithSpaces(t *testing.T) {
	input := "p77\nn/home/user/my project\n"
	got := scanner.ParseLsofBatch(input)
	require.Equal(t, "/home/user/my project", got[77])
}

// TestParseElapsedTime_AdditionalEdgeCases verifies boundary values not in the main table.
func TestParseElapsedTime_AdditionalEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{"23h59m59s", "23:59:59", 86399},
		{"5 days", "5-00:00:00", 432000},
		{"1 day 2 hours", "1-02:00:00", 93600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanner.ParseElapsedTime(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestProjectName_EdgeCases verifies ProjectName behaviour for unusual paths.
func TestProjectName_EdgeCases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/", "/"},
		{"/single", "single"},
		{"/a/b/c/d", "d"},
		{"relative/path", "path"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := scanner.ProjectName(tt.input)
			require.Equal(t, tt.want, got)
		})
	}
}
