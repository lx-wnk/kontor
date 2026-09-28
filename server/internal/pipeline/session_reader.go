package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

var jsonBlockRE = regexp.MustCompile("(?s)```json\\b([\\s\\S]*?)```")

func ResolvedProjectDir(cwd string) (string, error) {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		resolved = cwd
	}
	return filepath.Join(parser.ClaudeProjectsDir(), parser.EncodePath(resolved)), nil
}

// resolvedProjectDirs returns the project dir for cwd under EVERY known agent
// config dir — the server's CLAUDE_CONFIG_DIR, the ~/.claude default, and any
// on-disk custom dir (~/.claude-work, ~/.claude-personal, …). A spawner may set
// a custom CLAUDE_CONFIG_DIR for its agents (e.g. the "claude-work" spawner), so
// an agent's session JSONL can land under any of these. Searching only the
// server's own dir silently misses every custom-config-dir spawner — the agent
// runs fine but the orchestrator never finds its session and fails the stage.
func resolvedProjectDirs(cwd string) []string {
	resolved, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		resolved = cwd
	}
	enc := parser.EncodePath(resolved)
	cfgDirs := parser.AllClaudeConfigDirs()
	dirs := make([]string, 0, len(cfgDirs))
	for _, cfg := range cfgDirs {
		dirs = append(dirs, filepath.Join(cfg, "projects", enc))
	}
	return dirs
}

// findSessionFilePath locates <sessionID>.jsonl across all candidate project
// dirs and returns the first match, or "" if none exists.
func findSessionFilePath(cwd, sessionID string) string {
	for _, d := range resolvedProjectDirs(cwd) {
		p := filepath.Join(d, sessionID+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// SessionFileExists reports whether a <sessionID>.jsonl exists under any of
// cwd's candidate project dirs. The retry path uses it to decide whether a
// prior stage_run's session can actually be resumed before passing --resume to
// claude — a recorded session_id whose JSONL was deleted must fall back to a
// fresh spawn instead of handing claude a --resume id it can't load.
func SessionFileExists(cwd, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	return findSessionFilePath(cwd, sessionID) != ""
}

// findCutoffToleranceMs is how far before the recorded start a session file may
// be modified and still count as "this run's" session — absorbs the small skew
// between when the stage_run row is stamped and when claude first writes its
// session JSONL, plus clock granularity.
const findCutoffToleranceMs = 5_000

// FindNewestSessionID returns the newest session JSONL under cwd's project dir.
// When afterISO is non-empty it excludes sessions last modified before that
// cutoff (minus a small tolerance) — without this, a re-iterated stage whose
// freshly-spawned agent dies before writing its own session would resurrect the
// PRIOR iteration's session and mis-validate stale output. afterISO is parsed in
// the local zone because callers format it with a literal "Z" over local clock
// values (StartedAt.Format("2006-01-02T15:04:05Z")), not as a real UTC offset.
func FindNewestSessionID(cwd, afterISO string) (string, error) {
	hasCutoff := false
	var cutoffMs int64
	if afterISO != "" {
		if t, perr := time.ParseInLocation("2006-01-02T15:04:05Z", afterISO, time.Local); perr == nil {
			cutoffMs = t.UnixMilli() - findCutoffToleranceMs
			hasCutoff = true
		}
	}
	bestID := ""
	var bestMtime int64
	found := false
	for _, projectDir := range resolvedProjectDirs(cwd) {
		entries, err := os.ReadDir(projectDir)
		if err != nil {
			continue // dir may not exist under every config dir — that's fine
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			mtime := info.ModTime().UnixMilli()
			if hasCutoff && mtime < cutoffMs {
				continue // session predates this run — belongs to a prior iteration
			}
			if !found || mtime > bestMtime {
				found = true
				bestMtime = mtime
				bestID = strings.TrimSuffix(e.Name(), ".jsonl")
			}
		}
	}
	return bestID, nil
}

// APIError carries the structured API error from a rate/usage-limit JSONL entry.
type APIError struct {
	Status int
	Kind   string
}

type StageOutputRead struct {
	Output   map[string]any
	RawText  string
	APIError *APIError
}

func ExtractJsonBlock(text string) map[string]any {
	matches := jsonBlockRE.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	last := matches[len(matches)-1][1]
	var result map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(last)), &result); err != nil {
		return nil
	}
	return result
}

type JsonlEntry struct {
	Type              string `json:"type"`
	IsAPIErrorMessage bool   `json:"isApiErrorMessage"`
	APIErrorStatus    int    `json:"apiErrorStatus"`
	Error             string `json:"error"`
	Message           *struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// lastAPIError scans entries from the end and returns the last API error entry, nil if none.
func lastAPIError(entries []JsonlEntry) *APIError {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.IsAPIErrorMessage {
			return &APIError{Status: e.APIErrorStatus, Kind: e.Error}
		}
	}
	return nil
}

func lastAssistantText(entries []JsonlEntry) string {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(e.Message.Content, &parts); err != nil {
			continue
		}
		var texts []string
		for _, p := range parts {
			if p.Type == "text" && p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

func ReadLastStageJsonOutput(cwd, sessionID string) (StageOutputRead, error) {
	filePath := findSessionFilePath(cwd, sessionID)
	if filePath == "" {
		return StageOutputRead{}, nil
	}
	raw, err := parser.TailRead(filePath)
	if err != nil {
		return StageOutputRead{}, nil
	}
	entries := parseJsonlLines(raw)
	apiErr := lastAPIError(entries)
	text := lastAssistantText(entries)
	if text == "" && apiErr == nil {
		return StageOutputRead{}, nil
	}
	return StageOutputRead{Output: ExtractJsonBlock(text), RawText: text, APIError: apiErr}, nil
}

// ReadLastStageJsonOutputFromFile reads the JSONL at the given absolute path
// directly, bypassing the normal ~/.claude/projects/... discovery. Used by
// non-Claude adapters that write their own synthetic JSONL sessions.
func ReadLastStageJsonOutputFromFile(filePath string) (StageOutputRead, error) {
	raw, err := parser.TailRead(filePath)
	if err != nil {
		return StageOutputRead{}, fmt.Errorf("reading synthetic session file: %w", err)
	}
	entries := parseJsonlLines(raw)
	apiErr := lastAPIError(entries)
	text := lastAssistantText(entries)
	if text == "" && apiErr == nil {
		return StageOutputRead{}, nil
	}
	return StageOutputRead{Output: ExtractJsonBlock(text), RawText: text, APIError: apiErr}, nil
}

type SessionTokenSummary struct {
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	Model               string
}

func ReadSessionTokenSummary(cwd, sessionID string) (SessionTokenSummary, error) {
	filePath := findSessionFilePath(cwd, sessionID)
	if filePath == "" {
		return SessionTokenSummary{}, nil
	}
	raw, err := parser.TailRead(filePath)
	if err != nil {
		return SessionTokenSummary{}, nil
	}
	entries := parseJsonlLines(raw)
	var summary SessionTokenSummary
	for _, e := range entries {
		if e.Type != "assistant" || e.Message == nil {
			continue
		}
		if e.Message.Model != "" && summary.Model == "" {
			summary.Model = e.Message.Model
		}
		if u := e.Message.Usage; u != nil {
			summary.InputTokens += u.InputTokens
			summary.OutputTokens += u.OutputTokens
			summary.CacheCreationTokens += u.CacheCreationInputTokens
			summary.CacheReadTokens += u.CacheReadInputTokens
		}
	}
	return summary, nil
}

func parseJsonlLines(raw string) []JsonlEntry {
	var entries []JsonlEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e JsonlEntry
		if err := json.Unmarshal([]byte(line), &e); err == nil {
			entries = append(entries, e)
		}
	}
	return entries
}
