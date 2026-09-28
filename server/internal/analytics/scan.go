package analytics

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

// sessionFileRE matches a session JSONL filename (UUID + .jsonl) — used to
// filter out hidden / temp files when walking project directories.
var sessionFileRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.jsonl$`)

// SessionFile is a discovered JSONL file with its session ID and mtime.
type SessionFile struct {
	SessionID string
	Path      string
	ModTime   time.Time
}

// DiscoverSessions walks every configured Claude config dir and returns
// session JSONL files filtered by mtime and the optional Sessions
// allow-list. Results are sorted by mtime descending and truncated to
// MaxSessions (or DefaultMaxSessions if zero).
//
// The dirs argument is provided for tests; production callers pass nil to
// fall back to parser.AllClaudeConfigDirs().
func DiscoverSessions(opts ScanOpts, dirs []string) []SessionFile {
	if dirs == nil {
		dirs = parser.AllClaudeConfigDirs()
	}
	allow := map[string]bool{}
	for _, id := range opts.Sessions {
		if id != "" {
			allow[id] = true
		}
	}

	var out []SessionFile
	for _, configDir := range dirs {
		projectsDir := filepath.Join(configDir, "projects")
		projectDirs, err := os.ReadDir(projectsDir)
		if err != nil {
			continue
		}
		for _, pDir := range projectDirs {
			if !pDir.IsDir() {
				continue
			}
			dirPath := filepath.Join(projectsDir, pDir.Name())
			files, err := os.ReadDir(dirPath)
			if err != nil {
				continue
			}
			for _, f := range files {
				name := f.Name()
				if f.IsDir() || !sessionFileRE.MatchString(name) {
					continue
				}
				info, err := f.Info()
				if err != nil {
					continue
				}
				mtime := info.ModTime()
				if !opts.From.IsZero() && mtime.Before(opts.From) {
					continue
				}
				if !opts.To.IsZero() && mtime.After(opts.To) {
					continue
				}
				sessID := strings.TrimSuffix(name, ".jsonl")
				if len(allow) > 0 && !allow[sessID] {
					continue
				}
				out = append(out, SessionFile{
					SessionID: sessID,
					Path:      filepath.Join(dirPath, name),
					ModTime:   mtime,
				})
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })

	limit := opts.MaxSessions
	if limit <= 0 {
		limit = DefaultMaxSessions
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// scanSessionsForTools loads ToolCalls across the sessions selected by
// DiscoverSessions. Files larger than maxFileSize are skipped with a
// slog warning. Returns a map keyed by session ID where each value is
// the in-order tool_use timeline for that session.
func scanSessionsForTools(ctx context.Context, opts ScanOpts, dirs []string) (map[string][]ToolCall, error) {
	files := DiscoverSessions(opts, dirs)
	result := make(map[string][]ToolCall, len(files))
	for _, sf := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		calls, err := readToolCallsFromFile(sf.Path, sf.SessionID, opts.From, opts.To)
		if err != nil {
			slog.Debug("analytics: skip session file", "path", sf.Path, "err", err)
			continue
		}
		if len(calls) > 0 {
			result[sf.SessionID] = calls
		}
	}
	return result, nil
}

// readToolCallsFromFile streams a single JSONL file and emits one
// ToolCall per assistant tool_use block. Honors the per-file size cap
// from ngrams.go and the time bounds from ScanOpts.
func readToolCallsFromFile(path, sessionID string, from, to time.Time) ([]ToolCall, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > maxFileSize {
		slog.Warn("analytics: session file exceeds cap, tailing", "path", path, "size", info.Size())
	}

	var calls []ToolCall
	err = parser.ScanMessages(path, maxFileSize, func(m parser.Message) error {
		if m.Role != "assistant" {
			return nil
		}
		if !m.Timestamp.IsZero() {
			if !from.IsZero() && m.Timestamp.Before(from) {
				return nil
			}
			if !to.IsZero() && m.Timestamp.After(to) {
				return nil
			}
		}
		var blocks []struct {
			Type string `json:"type"`
			Name string `json:"name"`
			ID   string `json:"id"`
		}
		if err := json.Unmarshal(m.Content, &blocks); err != nil {
			return nil
		}
		for _, b := range blocks {
			if b.Type != "tool_use" || !ToolNameRE.MatchString(b.Name) {
				continue
			}
			calls = append(calls, ToolCall{
				SessionID: sessionID,
				Name:      b.Name,
				ID:        b.ID,
				Timestamp: m.Timestamp,
			})
		}
		return nil
	})
	return calls, err
}
