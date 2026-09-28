package scanner_test

import (
	"testing"

	"github.com/lx-wnk/kontor/server/internal/scanner"
	"github.com/stretchr/testify/require"
)

func TestParseElapsedTime(t *testing.T) {
	tests := []struct {
		name  string
		etime string
		want  int64
	}{
		// Core formats from ps(1) etime output
		{"seconds only", "42", 42},
		{"minutes and seconds", "05:30", 330},
		{"hours minutes seconds", "01:05:30", 3930},
		{"days hours minutes seconds", "2-01:05:30", 176730},
		// Edge cases
		{"leading space", "  12", 12},
		{"trailing space", "30  ", 30},
		// Edge cases
		{"zero", "0", 0},
		{"empty string", "", 0},
		{"whitespace only", "   ", 0},
		{"one second", "1", 1},
		{"zero seconds", "00", 0},
		{"zero minutes and seconds", "00:00", 0},
		{"single minute", "0:05", 5},
		{"exactly one minute", "01:00", 60},
		{"one minute thirty seconds", "1:30", 90},
		{"exactly one hour", "01:00:00", 3600},
		{"exact hour", "1:00:00", 3600},
		{"90 minutes", "1:30:00", 5400},
		{"one day", "1-00:00:00", 86400},
		{"large days", "10-00:00:00", 864000},
		{"all zeros with days", "0-00:00:00", 0},
		// 1 day, 1 hour, 5 minutes, 30 seconds = 86400 + 3600 + 330 = 90330
		{"one day one hour", "1-01:05:30", 90330},
		// Malformed: "1-" → days=1, remainder="" → 0 parsed for HMS → result=1*86400=86400
		{"malformed trailing dash", "1-", 86400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanner.ParseElapsedTime(tt.etime)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseLsofBatch(t *testing.T) {
	input := "p1234\nn/home/user/project\np5678\nn/tmp/other\n"
	got := scanner.ParseLsofBatch(input)
	require.Equal(t, "/home/user/project", got[1234])
	require.Equal(t, "/tmp/other", got[5678])
}

func TestParseLsofBatch_Empty(t *testing.T) {
	got := scanner.ParseLsofBatch("")
	require.Empty(t, got)
}

func TestProjectName(t *testing.T) {
	require.Equal(t, "project", scanner.ProjectName("/home/user/project"))
	require.Equal(t, "agent-dashboard", scanner.ProjectName("/Users/alex/code/agent-dashboard"))
}
