package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/claudeconfig"
	"github.com/lx-wnk/kontor/server/internal/sanitize"
)

const tailBytes = 32768 // 32KB from end

var (
	uuidRE  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	quotaRE = regexp.MustCompile(`(?i)quota exceeded|usage limit|monthly limit`)
	rateRE  = regexp.MustCompile(`(?i)rate limit|429|too many requests|throttl`)
	authRE  = regexp.MustCompile(`(?i)invalid api key|authentication|unauthorized|401`)
)

// classifyAPIError maps a real API error (gated by isApiErrorMessage) to an
// ErrorState. Status takes precedence; keyword regexes are the fallback when
// status is absent (0). Gating on isApiErrorMessage prevents benign assistant
// prose that mentions "authentication" or "rate limit" from being misclassified.
func classifyAPIError(status int, text string) sdk.ErrorState {
	switch status {
	case 401, 403:
		return sdk.ErrorStateAuthFailed
	case 429:
		if quotaRE.MatchString(text) {
			return sdk.ErrorStateQuotaExhausted
		}
		return sdk.ErrorStateRateLimited
	}
	// status absent — keyword fallback, safe here because caller already verified isApiErrorMessage
	switch {
	case quotaRE.MatchString(text):
		return sdk.ErrorStateQuotaExhausted
	case authRE.MatchString(text):
		return sdk.ErrorStateAuthFailed
	case rateRE.MatchString(text):
		return sdk.ErrorStateRateLimited
	}
	return ""
}

// SessionCacheTTL is the maximum age of a cached FindSessionForProject result.
// Set to the SSE broadcast interval (default 3 s) so each tick re-uses the
// cached parse instead of tail-reading every JSONL file again.
// serverapp.Run raises this at startup when a non-default interval is configured.
var SessionCacheTTL = 3 * time.Second

// sessionCacheKey identifies a cached parse result.
// pid is part of the key so two processes sharing one project directory get
// independent cache entries (and independent session resolution) rather than
// colliding on a single cwd-keyed entry.
type sessionCacheKey struct {
	cwd       string
	configDir string
	pid       int
}

// sessionCacheEntry holds a cached result keyed by the winning file's identity.
type sessionCacheEntry struct {
	// file identity — used to detect changes without re-reading content
	path  string
	inode uint64
	mtime time.Time
	// cached parse output. TokenUsage is the authoritative whole-file sum computed
	// by ParseSessionFile's full scan, so a cache hit needs no re-scan. The whole
	// entry is invalidated on inode/mtime change — any write (a new turn, a new
	// compaction) bumps mtime — so a fresh full scan runs exactly when the file
	// grows.
	data *SessionData
	// wall-clock time when this entry was stored
	cachedAt time.Time
}

var (
	sessionCacheMu sync.Mutex
	sessionCache   = make(map[sessionCacheKey]*sessionCacheEntry)
)

// candidateCacheEntry caches a statSessionFiles result for one project directory.
// It is validated by the directory's own mtime: adding or removing a session file
// bumps the directory mtime and forces a full re-stat, while a plain append to the
// active session file does NOT change the directory mtime — so on a hit only the
// newest candidate is re-stat'd to pick that append up.
type candidateCacheEntry struct {
	dirMtime   time.Time
	candidates []sessionFileCandidate
	cachedAt   time.Time
}

var (
	candidateCacheMu sync.Mutex
	candidateCache   = make(map[string]*candidateCacheEntry)
)

// statSessionFilesCalls counts full statSessionFiles directory scans. Test-only
// observability for the candidate-cache hit path (exposed via export_test.go).
var statSessionFilesCalls atomic.Int64

// claudeConfigDir returns the Claude config base directory: the
// claude.configDir setting, else CLAUDE_CONFIG_DIR, else ~/.claude.
func claudeConfigDir() string {
	return claudeconfig.ConfigDir()
}

// allClaudeConfigDirs returns all candidate Claude config directories to search.
// Priority order:
//  1. DASHBOARD_CLAUDE_CONFIG_DIRS — explicit comma-separated list (highest priority)
//  2. claudeConfigDir() — the claude.configDir setting or the server's CLAUDE_CONFIG_DIR
//  3. Default ~/.claude
//  4. Common custom variants that exist on disk (~/.claude-personal, etc.)
func allClaudeConfigDirs() []string {
	seen := make(map[string]bool)
	var dirs []string
	add := func(d string) {
		d = strings.TrimSpace(d)
		if d != "" && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	// 1. Explicit dashboard config — overrides auto-detection for teams/multi-user setups.
	if val := os.Getenv("DASHBOARD_CLAUDE_CONFIG_DIRS"); val != "" {
		for _, p := range strings.Split(val, ",") {
			add(p)
		}
	}
	// 2. claude.configDir setting or server process CLAUDE_CONFIG_DIR.
	add(claudeConfigDir())
	// 3. Standard default.
	home, _ := os.UserHomeDir()
	add(filepath.Join(home, ".claude"))
	// 4. Common custom dir names that exist on disk.
	for _, name := range []string{".claude-personal", ".claude-work", ".claude-dev"} {
		candidate := filepath.Join(home, name)
		if _, err := os.Stat(filepath.Join(candidate, "projects")); err == nil {
			add(candidate)
		}
	}
	return dirs
}

// claudeProjectsDir returns the Claude projects directory.
func claudeProjectsDir() string {
	return filepath.Join(claudeConfigDir(), "projects")
}

// AllClaudeConfigDirs returns all Claude config directories to search.
// Exported for use by packages that need JSONL file discovery (e.g., analytics).
// Deprecated: use AllAgentConfigDirs for multi-provider discovery.
func AllClaudeConfigDirs() []string {
	return allClaudeConfigDirs()
}

// ProviderConfigDir associates a provider identifier with a config directory path.
type ProviderConfigDir struct {
	Provider sdk.Provider
	Path     string
}

// AllAgentConfigDirs returns the Claude config directories. Non-Claude provider
// resolution now lives in the provider registry; this stays Claude-only for the
// projects/-shaped JSONL discovery used by history import.
func AllAgentConfigDirs() []ProviderConfigDir {
	var result []ProviderConfigDir
	for _, d := range allClaudeConfigDirs() {
		result = append(result, ProviderConfigDir{Provider: sdk.ProviderClaude, Path: d})
	}
	return result
}

// ClaudeProjectsDir returns the Claude projects directory — exported for use by pipeline package.
func ClaudeProjectsDir() string {
	return claudeProjectsDir()
}

// sessionMetaDir returns the Claude session-meta directory.
func sessionMetaDir() string {
	return filepath.Join(claudeConfigDir(), "usage-data", "session-meta")
}

// TailRead reads the last tailBytes bytes of a file.
// The first line of the result may be truncated (partial JSON) and should be skipped.
func TailRead(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat %s: %w", filePath, err)
	}
	size := info.Size()
	readSize := int64(tailBytes)
	if readSize > size {
		readSize = size
	}
	if _, err := f.Seek(size-readSize, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek: %w", err)
	}
	buf := make([]byte, readSize)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "", fmt.Errorf("read: %w", err)
	}
	return string(buf[:n]), nil
}

// jsonlMessage is the minimal structure of a JSONL session log entry.
type jsonlMessage struct {
	Type              string          `json:"type"`
	Subtype           string          `json:"subtype"`   // e.g. "compact_boundary" on type=="system" entries
	Timestamp         string          `json:"timestamp"` // ISO 8601, e.g. "2025-01-15T10:30:00.000Z"
	Message           json.RawMessage `json:"message"`
	IsAPIErrorMessage bool            `json:"isApiErrorMessage"` // true only on real Claude API error responses
	APIErrorStatus    int             `json:"apiErrorStatus"`    // HTTP status from the API error (401, 429, …)
}

// isCompactBoundaryType reports whether a (type, subtype) pair is the
// context-compaction marker the Claude CLI writes at the instant of compaction.
// Per-message assistant usage is NOT affected by compaction (only the cumulative
// context-window size resets), so the whole-file token sum ignores the boundary
// for the total. The marker is still detected to flag a session as compacted for
// diagnostics. This is the single discriminant shared by the tail parse and the
// full scan. See CI-4 design.
func isCompactBoundaryType(typ, subtype string) bool {
	return typ == "system" && subtype == "compact_boundary"
}

// usageCounters mirrors the per-message Anthropic `usage` object. It is the
// single canonical shape for extracting token counts; both the tail parse in
// ParseSessionFile and the full scan in scanCompactionBaseline accumulate
// through addUsage so the two paths can never drift.
type usageCounters struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
}

type msgContent struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
	Usage   *usageCounters  `json:"usage"`
}

// addUsage accumulates a per-message usage object into a sdk.TokenUsage total.
// nil usage is a no-op. This is the shared extraction point for both the tail
// parse and scanCompactionBaseline.
func addUsage(dst *sdk.TokenUsage, u *usageCounters) {
	if u == nil {
		return
	}
	dst.InputTokens += u.InputTokens
	dst.OutputTokens += u.OutputTokens
	dst.CacheCreationTokens += u.CacheCreationTokens
	dst.CacheReadTokens += u.CacheReadTokens
}

type toolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Text  string          `json:"text"`
	Input json.RawMessage `json:"input"`
}

// toolResultBlock is the minimal shape of a tool_result entry inside a user message.
type toolResultBlock struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
}

// pendingToolInput extracts the human-readable pattern for Bash/Edit/Write calls.
type pendingToolInput struct {
	Command  string `json:"command"`   // Bash
	FilePath string `json:"file_path"` // Edit, Write
}

const toolDetailMaxLen = 120

// toolArgument returns the tool's own argument verbatim: the Bash command, or
// the Edit/Write path. A permission preset is matched by exact equality, so
// anything that reaches a grant must use this and never the display form.
func toolArgument(raw json.RawMessage) string {
	var inp pendingToolInput
	_ = json.Unmarshal(raw, &inp)
	if inp.Command != "" {
		return inp.Command
	}
	return inp.FilePath
}

// toolDetail is the display form of toolArgument: stripped of characters that
// can make a command render as a different one, collapsed to one line, and cut
// so a single entry cannot dominate the list. The second return is how many
// characters were cut, 0 when nothing was.
//
// The count is deliberately NOT formatted into the string. A marker inside the
// payload is one the payload can forge -- an agent-authored command ending in
// "… (+400 chars)" would read as a server-truncated prefix of something longer.
// The client renders the count as its own element instead.
func toolDetail(raw json.RawMessage) (string, int) {
	return sanitize.ForDisplayCapped(toolArgument(raw), toolDetailMaxLen)
}

// patternDisplayMaxLen bounds the human-facing twin of a grant pattern. Longer
// than the recent-tool trail's cap because this one is the text someone reads
// while deciding, but still bounded: it rides on every SSE tick.
const patternDisplayMaxLen = 400

func patternDisplayOf(pattern string) string {
	d, _ := sanitize.ForDisplayCapped(pattern, patternDisplayMaxLen)
	return d
}

// todoInput is the input shape for TodoWrite tool calls.
type todoInput struct {
	Todos []struct {
		ID      string `json:"id"`
		Content string `json:"content"`
		Status  string `json:"status"`
	} `json:"todos"`
}

// fullScanUsage is the whole-file token total plus a flag recording whether the
// session was ever compacted. The token total is the authoritative source for
// SessionData.TokenUsage on a cache miss — it sums every assistant message's
// per-message usage across the ENTIRE file, so compaction is irrelevant to the
// total (per-message usage is unaffected by compaction; only the cumulative
// context-window size resets at a boundary). See CI-4 design.
type fullScanUsage struct {
	// hasCompaction is true when at least one compact_boundary line was seen.
	// Used only for diagnostics — the token total does not depend on it.
	hasCompaction bool
	sdk.TokenUsage
	notes       []NoteTouch
	customTitle string
	aiTitle     string
}

// A /rename title beats Claude's generated one.
func (f fullScanUsage) sessionTitle() string {
	if f.customTitle != "" {
		return f.customTitle
	}
	return f.aiTitle
}

// The JSONL is append-only, so the last title line seen is the newest.
func keepTitles(m Message, custom, ai *string) {
	if m.CustomTitle != "" {
		*custom = m.CustomTitle
	}
	if m.AiTitle != "" {
		*ai = m.AiTitle
	}
}

func addMessageUsage(dst *sdk.TokenUsage, m Message) {
	if m.Role != "assistant" || m.Usage == nil {
		return
	}
	dst.InputTokens += m.Usage.InputTokens
	dst.OutputTokens += m.Usage.OutputTokens
	dst.CacheCreationTokens += m.Usage.CacheCreationTokens
	dst.CacheReadTokens += m.Usage.CacheReadTokens
}

// scanFullFileTokenUsage does a single linear pass over path and sums every
// assistant message's per-message usage across the whole file. Because each
// assistant message's usage object is the token count for that one API turn
// (NOT a cumulative running total), the lifetime token total is simply the sum
// of all of them — compaction boundaries do not need special handling for the
// total. This is the correctness fix for CI-4: the 32 KB tail window only ever
// saw the last few messages, so a final epoch larger than 32 KB (or any long
// non-compacted session) was undercounted. Summing the whole file fixes both.
//
// It reuses addUsage so the per-message usage extraction can never diverge from
// the tail parse in ParseSessionFile.
func scanFullFileTokenUsage(path string) (fullScanUsage, error) {
	var total fullScanUsage
	var touches []NoteTouch
	err := ScanMessages(path, 0, func(m Message) error {
		if isCompactBoundaryType(m.Type, m.Subtype) {
			// Record that compaction happened (diagnostics only). The token total
			// is the whole-file sum and is unaffected by the boundary — per-message
			// usage does not reset, only the context-window cumulative size does.
			total.hasCompaction = true
			return nil
		}
		keepTitles(m, &total.customTitle, &total.aiTitle)
		addMessageUsage(&total.TokenUsage, m)
		touches = append(touches, noteTouchesOf(m)...)
		return nil
	})
	if err != nil {
		return fullScanUsage{}, fmt.Errorf("scan %s: %w", path, err)
	}
	total.notes = mergeNoteTouches(nil, touches, time.Now())
	return total, nil
}

// tokenOffsetCacheEntry tracks the incremental token-scan position for one
// session file, keyed by path and pinned to the inode it was seeded from. The
// inode alone cannot key the cache: the kernel recycles inode numbers, so a
// deleted session's entry would otherwise be resumed for the unrelated file
// that inherits its number. offset only ever advances while the inode holds —
// the JSONL is append-only (CI-4), so a lifetime total is an exact running sum
// of appended bytes and never needs to re-read history.
type tokenOffsetCacheEntry struct {
	inode       uint64
	offset      int64
	running     sdk.TokenUsage
	notes       []NoteTouch
	customTitle string
	aiTitle     string
}

var (
	tokenOffsetCacheMu sync.Mutex
	tokenOffsetCache   = make(map[string]*tokenOffsetCacheEntry)
)

// tokenOffsetCacheMaxEntries bounds tokenOffsetCache so idle/rotated session
// files don't accumulate forever. Exceeding it drops the whole cache — crude,
// but session file counts stay far below this in practice, so it never fires
// in normal operation.
const tokenOffsetCacheMaxEntries = 4096

// tokenUsageForFile returns the lifetime token total and the session's
// note-touch window for the session file at path, scanning only the bytes
// appended since the previous call when the file's inode is unchanged and has
// not shrunk. Falls back to a full rescan (scanFullFileTokenUsage) — and
// reseeds the cache entry — on first sighting, an inode change, a truncation
// (size < cached offset), or any incremental-scan error: a partial delta is
// never trusted, per CI-4 / PERF-HOT1.
func tokenUsageForFile(path string) (fullScanUsage, error) {
	info, err := os.Stat(path)
	if err != nil {
		return fullScanUsage{}, fmt.Errorf("stat %s: %w", path, err)
	}
	inode := inodeOf(info)
	size := info.Size()

	// Concurrent GetAgents callers (broadcast tick, hook rescan, HTTP reads) can
	// scan the same path at once. The scan runs unlocked, so a delta is applied
	// only if no other caller advanced the entry meanwhile; the loser returns the
	// winner's total instead of adding the same bytes a second time.
	tokenOffsetCacheMu.Lock()
	entry, ok := tokenOffsetCache[path]
	var startOffset int64
	if ok {
		startOffset = entry.offset
	}
	tokenOffsetCacheMu.Unlock()

	if ok && entry.inode == inode && size >= startOffset {
		var usage sdk.TokenUsage
		var added []NoteTouch
		var newCustomTitle, newAiTitle string
		newOffset, scanErr := ScanMessagesFrom(path, startOffset, func(m Message) {
			addMessageUsage(&usage, m)
			added = append(added, noteTouchesOf(m)...)
			keepTitles(m, &newCustomTitle, &newAiTitle)
		})
		if scanErr == nil {
			tokenOffsetCacheMu.Lock()
			if tokenOffsetCache[path] != entry || entry.offset != startOffset {
				running, notes := entry.running, entry.notes
				customTitle, aiTitle := entry.customTitle, entry.aiTitle
				tokenOffsetCacheMu.Unlock()
				return fullScanUsage{TokenUsage: running, notes: notes, customTitle: customTitle, aiTitle: aiTitle}, nil
			}
			entry.running.InputTokens += usage.InputTokens
			entry.running.OutputTokens += usage.OutputTokens
			entry.running.CacheCreationTokens += usage.CacheCreationTokens
			entry.running.CacheReadTokens += usage.CacheReadTokens
			entry.notes = mergeNoteTouches(entry.notes, added, time.Now())
			if newCustomTitle != "" {
				entry.customTitle = newCustomTitle
			}
			if newAiTitle != "" {
				entry.aiTitle = newAiTitle
			}
			entry.offset = newOffset
			running, notes := entry.running, entry.notes
			customTitle, aiTitle := entry.customTitle, entry.aiTitle
			tokenOffsetCacheMu.Unlock()
			return fullScanUsage{TokenUsage: running, notes: notes, customTitle: customTitle, aiTitle: aiTitle}, nil
		}
		slog.Warn("parser: incremental token scan failed — falling back to full rescan", "path", path, "err", scanErr)
	}

	full, err := scanFullFileTokenUsage(path)
	if err != nil {
		return fullScanUsage{}, err
	}
	tokenOffsetCacheMu.Lock()
	if !ok && len(tokenOffsetCache) >= tokenOffsetCacheMaxEntries {
		tokenOffsetCache = make(map[string]*tokenOffsetCacheEntry, tokenOffsetCacheMaxEntries)
	}
	tokenOffsetCache[path] = &tokenOffsetCacheEntry{
		inode:       inode,
		offset:      size,
		running:     full.TokenUsage,
		notes:       full.notes,
		customTitle: full.customTitle,
		aiTitle:     full.aiTitle,
	}
	tokenOffsetCacheMu.Unlock()
	return full, nil
}

// SessionData is the parsed output of a Claude Code JSONL session log.
type SessionData struct {
	SessionID           string
	Path                string
	ProjectPath         string
	Entrypoint          sdk.Entrypoint
	LastActivity        time.Time
	CurrentAction       string
	LastTools           []sdk.RecentTool
	RecentNotes         []NoteTouch
	SessionTitle        string
	Tasks               []sdk.TaskInfo
	TokenUsage          sdk.TokenUsage
	Model               string
	ConversationTurns   int
	ToolCounts          map[string]int
	LastOutput          string
	ConvergenceAlert    bool
	ConvergenceToolName string
	ErrorState          sdk.ErrorState
	Meta                *sdk.SessionMeta
	LastBtw             *sdk.BtwMessage
	PendingToolUse      *sdk.PendingToolUse
	TurnOpen            bool
}

// sessionFileCandidate holds mtime + inode info gathered via os.Stat (cheap).
type sessionFileCandidate struct {
	path  string
	mtime time.Time
	inode uint64
}

// statSessionFiles lists JSONL session files in projectDir ordered by mtime desc.
// Only os.Stat is called — no file content is read.
func statSessionFiles(projectDir string) ([]sessionFileCandidate, error) {
	statSessionFilesCalls.Add(1)
	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", projectDir, err)
	}
	var out []sessionFileCandidate
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !uuidRE.MatchString(id) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, sessionFileCandidate{
			path:  filepath.Join(projectDir, name),
			mtime: info.ModTime(),
			inode: inodeOf(info),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].mtime.After(out[j].mtime)
	})
	return out, nil
}

// cachedStatSessionFiles returns the JSONL candidate list for projectDir, reusing
// a cached statSessionFiles result while the directory mtime is unchanged and the
// entry is within SessionCacheTTL. This collapses the per-tick ~N-stat ReadDir
// scan to a single dir stat plus one re-stat of the newest candidate.
//
// On a hit only the newest candidate is re-stat'd, because a content append to the
// active session file does not bump the directory mtime but must still invalidate
// the downstream sessionCache. Any other staleness (e.g. a resumed session whose
// file pre-existed and is not the newest) is bounded by SessionCacheTTL: the full
// scan re-runs at the latest one TTL after the directory last changed.
func cachedStatSessionFiles(projectDir string) ([]sessionFileCandidate, error) {
	di, err := os.Stat(projectDir)
	if err != nil {
		return nil, fmt.Errorf("stat dir %s: %w", projectDir, err)
	}
	dirMtime := di.ModTime()
	now := time.Now()

	candidateCacheMu.Lock()
	entry := candidateCache[projectDir]
	candidateCacheMu.Unlock()

	if entry != nil &&
		now.Sub(entry.cachedAt) < SessionCacheTTL &&
		entry.dirMtime.Equal(dirMtime) &&
		len(entry.candidates) > 0 {
		// entry.candidates is read/written here outside candidateCacheMu; safe only
		// because the merger serializes resolution per projectDir (one goroutine per
		// directory group). Parallel callers for the same projectDir would race.
		out := make([]sessionFileCandidate, len(entry.candidates))
		copy(out, entry.candidates)
		// Re-stat only the newest file to surface an append (dir mtime unchanged).
		if info, statErr := os.Stat(out[0].path); statErr == nil {
			out[0].mtime = info.ModTime()
			out[0].inode = inodeOf(info)
			candidateCacheMu.Lock()
			entry.candidates[0] = out[0]
			candidateCacheMu.Unlock()
			return out, nil
		}
		// Newest file vanished — fall through to a full re-stat.
	}

	candidates, err := statSessionFiles(projectDir)
	if err != nil {
		return nil, err
	}
	candidateCacheMu.Lock()
	candidateCache[projectDir] = &candidateCacheEntry{
		dirMtime:   dirMtime,
		candidates: candidates,
		cachedAt:   now,
	}
	candidateCacheMu.Unlock()
	return candidates, nil
}

// FindSessionForProject locates the most recently active JSONL session for cwd.
// claudeConfigDir, if non-empty, overrides the default ~/.claude config directory
// (use the value of CLAUDE_CONFIG_DIR from the process environment).
//
// Results are cached per (cwd, configDir) keyed by the winning file's path,
// inode, and mtime.  A cached result is reused when:
//   - the same file is still the newest JSONL in the project directory, AND
//   - its inode and mtime are unchanged (no write happened), AND
//   - the entry was stored within SessionCacheTTL.
//
// This eliminates repeated 32 KB tail-reads per SSE tick on long session histories.
func FindSessionForProject(cwd string, uptimeSeconds int64, claudeConfigDir string) (*SessionData, error) {
	return findSessionForProjectFiltered(cwd, 0, uptimeSeconds, claudeConfigDir, nil, "")
}

// findSessionForProjectFiltered is the cache-backed core of session resolution.
//
//   - pid is folded into the cache key so concurrent processes in the same
//     project directory never share a cache entry.
//   - forcedID, when non-empty, pins resolution to exactly that session file
//     (used when an authoritative pid→session mapping or a --resume arg is
//     known); the file is located across all config dirs if absent from cwd's
//     own project directory.
//   - claimed, when non-nil, excludes session IDs already bound to other
//     processes so two same-folder agents never collapse onto one session.
func findSessionForProjectFiltered(cwd string, pid int, uptimeSeconds int64, claudeConfigDir string, claimed map[string]bool, forcedID string) (*SessionData, error) {
	baseDir := claudeProjectsDir()
	if claudeConfigDir != "" {
		baseDir = filepath.Join(claudeConfigDir, "projects")
	}
	encoded := EncodePath(cwd)
	projectDir := filepath.Join(baseDir, encoded)

	candidates, _ := cachedStatSessionFiles(projectDir)

	if forcedID != "" {
		candidates = filterToID(candidates, forcedID)
		if len(candidates) == 0 {
			// The pinned session file is not under cwd's encoded directory
			// (e.g. a resumed session, or the process changed directories).
			// Search every candidate config dir for it.
			if c, ok := locateSessionFile(forcedID, candidateConfigDirs(claudeConfigDir)); ok {
				candidates = []sessionFileCandidate{c}
			}
		}
	} else if len(claimed) > 0 {
		candidates = filterOutClaimed(candidates, claimed)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no session files for %s (pid %d, forcedID %q)", projectDir, pid, forcedID)
	}

	cacheKey := sessionCacheKey{cwd: cwd, configDir: claudeConfigDir, pid: pid}
	now := time.Now()

	// Check cache against the top candidate (most recently modified file).
	top := candidates[0]
	sessionCacheMu.Lock()
	entry := sessionCache[cacheKey]
	sessionCacheMu.Unlock()

	if entry != nil &&
		now.Sub(entry.cachedAt) < SessionCacheTTL &&
		entry.path == top.path &&
		entry.inode == top.inode &&
		entry.mtime.Equal(top.mtime) {
		// Cache hit: return a shallow copy so callers can safely mutate fields.
		// Clone ToolCounts so the caller cannot mutate the cached map.
		cp := *entry.data
		cp.ToolCounts = maps.Clone(entry.data.ToolCounts)
		return &cp, nil
	}

	// Cache miss — fall back to full scan with content reads.
	data, chosenPath, err := findSessionByContent(candidates, uptimeSeconds, cwd)
	if err != nil {
		return nil, err
	}

	// Populate session ID and meta before caching.
	data.SessionID = strings.TrimSuffix(filepath.Base(chosenPath), ".jsonl")
	data.Path = chosenPath
	data.ProjectPath = cwd
	data.Meta = loadSessionMeta(data.SessionID)

	// Store in cache keyed by the chosen file's identity (the file whose content
	// was actually parsed).  When the active session file advances (new write →
	// mtime bump on chosenPath) we get a cache miss on the next tick.
	// Only fall back to top's pre-fetched stat when chosenPath == top.path to
	// avoid a redundant syscall.
	var entryPath string
	var entryInode uint64
	var entryMtime time.Time
	if chosenPath == top.path {
		entryPath = top.path
		entryInode = top.inode
		entryMtime = top.mtime
	} else {
		info, statErr := os.Stat(chosenPath)
		if statErr == nil {
			entryPath = chosenPath
			entryInode = inodeOf(info)
			entryMtime = info.ModTime()
		} else {
			// Stat failed — fall back to top so we always have a valid entry.
			entryPath = top.path
			entryInode = top.inode
			entryMtime = top.mtime
		}
	}
	newEntry := &sessionCacheEntry{
		path:     entryPath,
		inode:    entryInode,
		mtime:    entryMtime,
		data:     data,
		cachedAt: now,
	}
	sessionCacheMu.Lock()
	sessionCache[cacheKey] = newEntry
	sessionCacheMu.Unlock()

	cp := *data
	cp.ToolCounts = maps.Clone(data.ToolCounts)
	return &cp, nil
}

// findSessionByContent reads JSONL content for each candidate until it finds a
// session whose LastActivity is within the process uptime window.
// Returns the chosen *SessionData and the file path used.
func findSessionByContent(candidates []sessionFileCandidate, uptimeSeconds int64, cwd string) (*SessionData, string, error) {
	var bestByContent *SessionData
	var bestByContentPath string
	for _, c := range candidates {
		data, err := ParseSessionFile(c.path)
		if err != nil {
			continue
		}
		age := time.Since(data.LastActivity)
		if age < time.Duration(uptimeSeconds+10)*time.Second {
			return data, c.path, nil
		}
		// Keep the first (most-recently modified) as fallback.
		if bestByContent == nil {
			bestByContent = data
			bestByContentPath = c.path
		}
	}
	if bestByContent != nil {
		return bestByContent, bestByContentPath, nil
	}
	return nil, "", fmt.Errorf("no active session for %s", cwd)
}

// ParseSessionFile parses a single JSONL session file and returns its SessionData.
func ParseSessionFile(path string) (*SessionData, error) {
	content, err := TailRead(path)
	if err != nil {
		return nil, err
	}

	data := &SessionData{
		ToolCounts:   make(map[string]int),
		Entrypoint:   sdk.EntrypointUnknown,
		LastActivity: time.Now().Add(-24 * time.Hour), // default: old
	}

	// Name and raw input only: the display form costs a JSON unmarshal, a rune
	// scan and two string copies, and all but the last five entries are dropped
	// below. Deriving it here would compute it for every tool_use in the tail.
	type trackedRecentTool struct {
		name  string
		input json.RawMessage
	}
	var recentTools []trackedRecentTool

	// pendingToolUses tracks assistant tool_use blocks (id → block) in order; the
	// last one with no matching tool_result is the pending tool. Ordered slice
	// preserves insertion order for the "last unmatched" check.
	type trackedToolUse struct {
		id    string
		name  string
		input json.RawMessage
	}
	var toolUseOrder []trackedToolUse
	resolvedToolUseIDs := make(map[string]bool)

	scanner := bufio.NewScanner(bytes.NewReader([]byte(content)))
	var lastEntryType string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry jsonlMessage
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type == "user" || entry.Type == "assistant" || entry.Type == "message" {
			lastEntryType = entry.Type
		}
		// compact_boundary lines carry no per-message usage and no tool/model/
		// activity data the tail parse needs — skip them. Token totals come from
		// the authoritative whole-file scan (scanFullFileTokenUsage) below, so the
		// tail loop no longer touches data.TokenUsage and no longer needs to reset
		// on a boundary.
		if isCompactBoundaryType(entry.Type, entry.Subtype) {
			continue
		}

		// User messages carry tool_result blocks that resolve outstanding tool_use IDs.
		if entry.Type == "user" {
			var msg msgContent
			if err := json.Unmarshal(entry.Message, &msg); err == nil && msg.Role == "user" {
				var blocks []toolResultBlock
				if err := json.Unmarshal(msg.Content, &blocks); err == nil {
					for _, b := range blocks {
						if b.Type == "tool_result" && b.ToolUseID != "" {
							resolvedToolUseIDs[b.ToolUseID] = true
						}
					}
				}
			}
		}

		// Accept both the legacy "message" envelope and the current direct "assistant" type.
		if entry.Type != "assistant" && entry.Type != "message" {
			continue
		}
		var msg msgContent
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}

		if msg.Role == "assistant" {
			data.ConversationTurns++
			if msg.Model != "" {
				data.Model = msg.Model
			}
			// Token usage is NOT accumulated here. It comes exclusively from the
			// whole-file scan below so the tail's 32 KB limit can never undercount.
			if ts, parseErr := time.Parse(time.RFC3339Nano, entry.Timestamp); parseErr == nil {
				if ts.After(data.LastActivity) {
					data.LastActivity = ts
				}
			}
			// If parse fails, LastActivity stays at its default (-24h old) — agent looks idle, which is correct.

			var blocks []toolUseBlock
			if err := json.Unmarshal(msg.Content, &blocks); err == nil {
				var btwText string
				hasToolUse := false
				for _, b := range blocks {
					switch b.Type {
					case "tool_use":
						hasToolUse = true
						data.ToolCounts[b.Name]++
						recentTools = append(recentTools, trackedRecentTool{name: b.Name, input: b.Input})
						data.CurrentAction = b.Name
						if b.ID != "" {
							toolUseOrder = append(toolUseOrder, trackedToolUse{id: b.ID, name: b.Name, input: b.Input})
						}
						if b.Name == "TodoWrite" {
							var inp todoInput
							if err := json.Unmarshal(b.Input, &inp); err == nil {
								tasks := make([]sdk.TaskInfo, 0, len(inp.Todos))
								for _, td := range inp.Todos {
									tasks = append(tasks, sdk.TaskInfo{
										ID:      td.ID,
										Subject: td.Content,
										Status:  sdk.TaskInfoStatus(td.Status),
									})
								}
								data.Tasks = tasks
							}
						}
					case "text":
						if b.Text != "" {
							btwText = scrubSecrets(b.Text)
							data.LastOutput = btwText
							if entry.IsAPIErrorMessage {
								if es := classifyAPIError(entry.APIErrorStatus, b.Text); es != "" {
									data.ErrorState = es
								}
							}
						}
					}
				}
				if hasToolUse && btwText != "" {
					data.LastBtw = &sdk.BtwMessage{Message: btwText}
				}
			}
		}
	}

	// Pending tool use: the agent is blocked on its most recent tool request when
	// the last assistant tool_use has no matching tool_result yet.
	if n := len(toolUseOrder); n > 0 {
		tu := toolUseOrder[n-1]
		if !resolvedToolUseIDs[tu.id] {
			pattern := toolArgument(tu.input)
			data.PendingToolUse = &sdk.PendingToolUse{
				ID:   tu.id,
				Tool: tu.name,
				// Verbatim: this is the grant identity, matched by exact
				// equality. The sanitized twin is what a human is shown.
				Pattern: pattern,
				// Capped as well as sanitized: the display twin is not the
				// grant identity, and shipping a multi-kilobyte command twice
				// on every SSE tick buys nothing a human can read.
				PatternDisplay: patternDisplayOf(pattern),
			}
		}
	}

	// TurnOpen: the agent owes the next step when the trailing entry is a user
	// message (prompt or tool_result), or a tool_use is still unresolved.
	data.TurnOpen = lastEntryType == "user" || data.PendingToolUse != nil

	if err := scanner.Err(); err != nil {
		slog.Warn("parser: session scan error — partial data returned", "err", err)
	}

	// Authoritative token totals: a single linear pass sums every assistant
	// message's per-message usage across the WHOLE file. The 32 KB tail above
	// only ever sees the last handful of messages, so it cannot be trusted for
	// the token total — this is true for compacted sessions whose final epoch
	// exceeds 32 KB AND for any long non-compacted session over 32 KB. The full
	// scan is the only source of TokenUsage; the tail loop deliberately left it
	// untouched. This runs on the cache-miss path only — ParseSessionFile is not
	// called on an SSE cache hit.
	if full, scanErr := tokenUsageForFile(path); scanErr != nil {
		// Non-fatal: fall back to a tail-only sum rather than dropping the whole
		// session. Undercounting a large session is better than no data at all.
		slog.Warn("parser: full token scan failed — falling back to tail-only token totals (may undercount)", "path", path, "err", scanErr)
		data.TokenUsage = tailOnlyTokenUsage(content)
	} else {
		if full.hasCompaction {
			slog.Debug("parser: whole-file token total (compacted session)",
				"path", path,
				"input", full.InputTokens,
				"output", full.OutputTokens)
		}
		data.TokenUsage = full.TokenUsage
		data.RecentNotes = full.notes
		data.SessionTitle = full.sessionTitle()
	}

	kept := recentTools
	if len(kept) > 5 {
		kept = kept[len(kept)-5:]
	}
	data.LastTools = make([]sdk.RecentTool, len(kept))
	for i, t := range kept {
		detail, elided := toolDetail(t.input)
		data.LastTools[i] = sdk.RecentTool{Name: t.name, Detail: detail, Elided: elided}
	}

	// Convergence detection: last 5 tools identical. Compares names only -- the
	// same tool with different arguments is progress, not a loop.
	if len(recentTools) >= 5 {
		last5 := recentTools[len(recentTools)-5:]
		allSame := true
		for _, t := range last5[1:] {
			if t.name != last5[0].name {
				allSame = false
				break
			}
		}
		if allSame {
			data.ConvergenceAlert = true
			data.ConvergenceToolName = last5[0].name
		}
	}

	return data, nil
}

// tailOnlyTokenUsage sums per-message assistant usage from the already-read tail
// content. It is the degraded fallback used only when the authoritative
// whole-file scan errors (e.g. the file became unreadable between TailRead and
// the scan). It mirrors the old tail-accumulation behaviour — undercounting a
// large session, but better than zero. compact_boundary lines carry no usage and
// are simply skipped (no reset: the whole-file scan is the correct path, this is
// damage control).
func tailOnlyTokenUsage(content string) sdk.TokenUsage {
	var total sdk.TokenUsage
	scanner := bufio.NewScanner(bytes.NewReader([]byte(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry jsonlMessage
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" && entry.Type != "message" {
			continue
		}
		var msg msgContent
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		if msg.Role == "assistant" {
			addUsage(&total, msg.Usage)
		}
	}
	return total
}

func loadSessionMeta(sessionID string) *sdk.SessionMeta {
	path := filepath.Join(sessionMetaDir(), sessionID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var meta sdk.SessionMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil
	}
	return &meta
}
