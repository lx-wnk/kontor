package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func titleLine(t *testing.T, typ, field, title string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]string{"type": typ, field: title, "sessionId": "s1"})
	require.NoError(t, err)
	return append(b, '\n')
}

func customTitleLine(t *testing.T, title string) []byte {
	return titleLine(t, "custom-title", "customTitle", title)
}

func aiTitleLine(t *testing.T, title string) []byte {
	return titleLine(t, "ai-title", "aiTitle", title)
}

func writeSessionLines(t *testing.T, path string, lines ...[]byte) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close() //nolint:errcheck
	for _, l := range lines {
		_, err := f.Write(l)
		require.NoError(t, err)
	}
	require.NoError(t, f.Sync())
}

func TestParseSessionFile_SessionTitle(t *testing.T) {
	cases := []struct {
		name  string
		lines [][]byte
		want  string
	}{
		{
			name:  "custom beats ai when ai comes first",
			lines: [][]byte{aiTitleLine(t, "Ai title"), customTitleLine(t, "Custom title")},
			want:  "Custom title",
		},
		{
			name:  "custom beats ai when custom comes first",
			lines: [][]byte{customTitleLine(t, "Custom title"), aiTitleLine(t, "Ai title")},
			want:  "Custom title",
		},
		{
			name:  "newest custom wins",
			lines: [][]byte{customTitleLine(t, "First"), customTitleLine(t, "Second")},
			want:  "Second",
		},
		{
			name:  "newest ai wins",
			lines: [][]byte{aiTitleLine(t, "First"), aiTitleLine(t, "Second")},
			want:  "Second",
		},
		{
			name:  "ai only",
			lines: [][]byte{aiTitleLine(t, "Only ai title")},
			want:  "Only ai title",
		},
		{
			name:  "none present",
			lines: nil,
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetTokenOffsetCache(t)
			path := filepath.Join(t.TempDir(), "session.jsonl")
			writeSessionLines(t, path, tc.lines...)

			data, err := ParseSessionFile(path)
			require.NoError(t, err)
			require.Equal(t, tc.want, data.SessionTitle)
		})
	}
}

// TestParseSessionFile_SessionTitle_AppendedAfterInitialScanIsPickedUp verifies
// a title line appended after the file was already scanned once is picked up
// by the incremental scan on the next call, not just a fresh full scan.
func TestParseSessionFile_SessionTitle_AppendedAfterInitialScanIsPickedUp(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeSessionLines(t, path, []byte(incrementalLineTemplate))

	first, err := ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, "", first.SessionTitle)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	require.NoError(t, err)
	_, err = f.Write(customTitleLine(t, "Renamed later"))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	second, err := ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, "Renamed later", second.SessionTitle)
}

// TestParseSessionFile_SessionTitle_SurvivesIncrementalAppend verifies a title
// set during the initial (full) scan is not lost when a later incremental scan
// covers only appended bytes that carry no title line of their own — proof the
// title lives in the per-file cache entry rather than being re-derived by
// rescanning the whole file every call.
func TestParseSessionFile_SessionTitle_SurvivesIncrementalAppend(t *testing.T) {
	resetTokenOffsetCache(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	writeSessionLines(t, path, customTitleLine(t, "Original title"))

	first, err := ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, "Original title", first.SessionTitle)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o640)
	require.NoError(t, err)
	_, err = f.Write([]byte(incrementalLineTemplate))
	require.NoError(t, err)
	require.NoError(t, f.Close())

	second, err := ParseSessionFile(path)
	require.NoError(t, err)
	require.Equal(t, "Original title", second.SessionTitle)
}
