// Package history provides cost-trend history import from Claude Code JSONL session logs.
package history

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// ProjectResolver maps a session cwd to a stable grouping key and display name.
// Implementations must be safe for concurrent use.
type ProjectResolver interface {
	Resolve(ctx context.Context, cwd string) (path string, name string)
}

const (
	maxFileSizeBytes   = 100 * 1024 * 1024 // 100 MB
	progressDebounceMs = 100
)

// ImportProgress is the live status snapshot sent to SSE clients.
type ImportProgress struct {
	Total     int  `json:"total"`
	Processed int  `json:"processed"`
	Imported  int  `json:"imported"`
	Errors    int  `json:"errors"`
	Done      bool `json:"done"`
}

// Importer scans CLAUDE_PROJECTS_DIR, extracts per-session cost data, and upserts
// into the agent_cost_trend table. Only one concurrent run is permitted per instance.
type Importer struct {
	costRepo  repo.AgentCostTrendRepo
	collectFn func(string) ([]string, error) // injectable for tests; nil → collectJSONLFiles
	resolver  ProjectResolver                // optional; nil → fallback basename
	mu        sync.Mutex
	running   bool
}

// NewImporter creates an Importer backed by costRepo.
func NewImporter(costRepo repo.AgentCostTrendRepo) *Importer {
	return &Importer{costRepo: costRepo}
}

// WithCollectFn returns a shallow copy of imp with fn as the file-collection function.
// For use in tests only — overrides the default collectJSONLFiles scan.
func (imp *Importer) WithCollectFn(fn func(string) ([]string, error)) *Importer {
	return &Importer{costRepo: imp.costRepo, collectFn: fn, resolver: imp.resolver}
}

// WithProjectResolver returns a shallow copy of imp with r as the project resolver.
// When r is nil, the importer uses a basename fallback (path = cwd, name = filepath.Base(cwd)).
func (imp *Importer) WithProjectResolver(r ProjectResolver) *Importer {
	return &Importer{costRepo: imp.costRepo, collectFn: imp.collectFn, resolver: r}
}

// Run starts the import in a background goroutine. Returns immediately.
// Returns an error if an import is already in progress.
// onProgress is called after each file is processed and when the run completes.
func (imp *Importer) Run(ctx context.Context, onProgress func(ImportProgress)) error {
	imp.mu.Lock()
	if imp.running {
		imp.mu.Unlock()
		return fmt.Errorf("import already in progress")
	}
	imp.running = true
	imp.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("history.import: panic in import goroutine", "panic", r)
				onProgress(ImportProgress{Done: true})
			}
			imp.mu.Lock()
			imp.running = false
			imp.mu.Unlock()
		}()
		imp.runImport(ctx, onProgress)
	}()
	return nil
}

// RunScheduled runs one scan immediately (boot scan), then periodically rescans on
// every ticker tick until ctx is cancelled. If interval <= 0 only the boot scan
// runs and RunScheduled returns immediately after it completes.
//
// Each scan is SILENT — a no-op onProgress is passed to Run. The existing
// single-instance guard in Run means that a scheduled tick arriving while a
// previous scan is still in progress is skipped rather than stacked.
func (imp *Importer) RunScheduled(ctx context.Context, interval time.Duration) {
	noop := func(ImportProgress) {}

	slog.Info("history.scheduler: starting cost-history scanner", "interval", interval)

	// Boot scan — always run once before starting the loop.
	if err := imp.Run(ctx, noop); err != nil {
		slog.Debug("history.scheduler: boot scan skipped", "reason", err)
	}

	if interval <= 0 {
		// Boot-only mode: wait for the goroutine Run launched to finish so
		// callers can treat RunScheduled as synchronous in this mode.
		// We spin-poll the running flag rather than exposing a Done channel,
		// since the goroutine is short-lived in tests.
		for {
			imp.mu.Lock()
			still := imp.running
			imp.mu.Unlock()
			if !still {
				return
			}
			// Yield briefly to avoid a hot spin.
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := imp.Run(ctx, noop); err != nil {
				slog.Debug("history.scheduler: tick skipped", "reason", err)
			}
		}
	}
}

// runImport performs the full scan + insert and reports progress via onProgress.
func (imp *Importer) runImport(ctx context.Context, onProgress func(ImportProgress)) {
	collect := imp.collectFn
	if collect == nil {
		collect = collectJSONLFiles
	}

	// Collect JSONL files from every configured provider/account directory.
	// Errors on individual dirs are logged and skipped — one missing provider
	// must not prevent scanning the remaining dirs.
	seen := make(map[string]struct{})
	var files []string

	configDirs := parser.AllAgentConfigDirs()
	for _, entry := range configDirs {
		projectsDir := filepath.Join(entry.Path, "projects")
		dirFiles, err := collect(projectsDir)
		if err != nil {
			slog.Warn("history.import: failed to collect jsonl files",
				"provider", entry.Provider, "dir", projectsDir, "err", err)
			continue
		}
		for _, f := range dirFiles {
			clean := filepath.Clean(f)
			if _, dup := seen[clean]; dup {
				continue
			}
			seen[clean] = struct{}{}
			files = append(files, f)
		}
	}

	progress := ImportProgress{Total: len(files)}
	onProgress(progress)

	// Debounce — report at most once per progressDebounceMs, but always after each file.
	var (
		lastReport = time.Now()
		debounce   = time.Duration(progressDebounceMs) * time.Millisecond
	)
	reportProgress := func(final bool) {
		now := time.Now()
		if final || now.Sub(lastReport) >= debounce {
			onProgress(progress)
			lastReport = now
		}
	}

	// Load stored source mtimes once; used to skip files whose content has not changed.
	storedMtimes, mtimeErr := imp.costRepo.ListSourceMtimes(ctx)
	if mtimeErr != nil {
		slog.Warn("history.import: could not load source mtimes — all files will be (re-)parsed", "err", mtimeErr)
		storedMtimes = map[string]int64{}
	}

	var rows []repo.AgentCostRow

	for _, filePath := range files {
		if ctx.Err() != nil {
			break
		}

		// Skip-unchanged: if the stored mtime matches the file's current mtime, we can
		// safely skip parsing — the content has not changed since the last import.
		sessionID := sessionIDFromPath(filePath)
		if stored, ok := storedMtimes[sessionID]; ok && stored != 0 {
			if newestMtime(filePath, subagentFiles(filePath)) == stored {
				progress.Processed++
				reportProgress(false)
				continue
			}
		}

		row, err := imp.extractCostRow(ctx, filePath)
		if err != nil {
			slog.Debug("history.import: skip file", "path", filePath, "err", err)
			progress.Errors++
			progress.Processed++
			reportProgress(false)
			continue
		}
		if row != nil {
			rows = append(rows, *row)
			progress.Imported++
		}
		progress.Processed++
		reportProgress(false)
	}

	// Collapse rows that resolved to the same session_id (e.g. the same session
	// present under more than one configured config dir) keeping the latest
	// recorded_at. Mirrors the dedup migration's keep-latest rule and makes the
	// upsert result independent of directory scan order.
	rows = dedupRowsBySession(rows)
	progress.Imported = len(rows)

	// Upsert all collected rows — idempotent per session_id.
	if len(rows) > 0 {
		if err := imp.costRepo.Upsert(ctx, rows); err != nil {
			slog.Warn("history.import: upsert failed", "err", err)
			// Whole batch failed — reflect that in error count.
			progress.Errors += len(rows)
			progress.Imported = 0
		}
	}

	progress.Done = true
	reportProgress(true)
}

// dedupRowsBySession returns one row per session_id, keeping the row with the
// latest recorded_at (later occurrence wins on an exact tie). Input order is
// otherwise preserved for the surviving rows. This guarantees a deterministic
// upsert when the same session appears under more than one configured config
// dir, instead of relying on os.ReadDir ordering.
func dedupRowsBySession(rows []repo.AgentCostRow) []repo.AgentCostRow {
	if len(rows) < 2 {
		return rows
	}
	idx := make(map[string]int, len(rows))
	out := make([]repo.AgentCostRow, 0, len(rows))
	for _, row := range rows {
		if i, ok := idx[row.SessionID]; ok {
			if !row.RecordedAt.Before(out[i].RecordedAt) {
				out[i] = row // newer (or equal) wins
			}
			continue
		}
		idx[row.SessionID] = len(out)
		out = append(out, row)
	}
	return out
}

// collectJSONLFiles returns all *.jsonl paths one level deep inside projectsDir.
func collectJSONLFiles(projectsDir string) ([]string, error) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // nothing to import
		}
		return nil, fmt.Errorf("readdir %s: %w", projectsDir, err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		subDir := filepath.Join(projectsDir, entry.Name())
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if sub.IsDir() || !strings.HasSuffix(sub.Name(), ".jsonl") {
				continue
			}
			info, err := sub.Info()
			if err != nil {
				continue
			}
			if info.Size() > maxFileSizeBytes {
				continue // skip files larger than 100 MB
			}
			files = append(files, filepath.Join(subDir, sub.Name()))
		}
	}
	return files, nil
}

// extractCostRow parses filePath plus its subagent files into one AgentCostRow.
// Returns (nil, nil) when the session has no usable token data.
func (imp *Importer) extractCostRow(ctx context.Context, filePath string) (*repo.AgentCostRow, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if info.Size() > maxFileSizeBytes {
		return nil, fmt.Errorf("file too large (%d bytes)", info.Size())
	}
	stats, err := parseFile(filePath)
	if err != nil {
		return nil, err
	}

	usage := stats.Usage
	estimate := parser.EstimateCost(stats.Usage, stats.Model)
	subs := subagentFiles(filePath)
	for _, sub := range subs {
		subStats, err := parseFile(sub)
		if err != nil {
			slog.Debug("history.import: skip subagent file", "path", sub, "err", err)
			continue
		}
		usage.InputTokens += subStats.Usage.InputTokens
		usage.OutputTokens += subStats.Usage.OutputTokens
		usage.CacheReadTokens += subStats.Usage.CacheReadTokens
		usage.CacheCreationTokens += subStats.Usage.CacheCreationTokens
		estimate += parser.EstimateCost(subStats.Usage, subStats.Model)
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return nil, nil
	}

	recordedAt := stats.LastActivity
	if recordedAt.IsZero() {
		recordedAt = info.ModTime()
	}

	cwd := stats.Cwd
	projectPath, projectName := cwd, filepath.Base(cwd)
	if cwd == "" {
		projectName = ""
	}
	if imp.resolver != nil && cwd != "" {
		projectPath, projectName = imp.resolver.Resolve(ctx, cwd)
	}

	row := &repo.AgentCostRow{
		SessionID:           sessionIDFromPath(filePath),
		Model:               stats.Model,
		InputTokens:         usage.InputTokens,
		OutputTokens:        usage.OutputTokens,
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		CostUSD:             estimate,
		CostSource:          repo.SourceEstimate,
		AgentMs:             stats.EstimateMs,
		AgentTimeSource:     repo.SourceEstimate,
		RecordedAt:          recordedAt,
		StartedAt:           stats.StartedAt,
		Title:               stats.Title,
		Turns:               stats.Turns,
		Cwd:                 cwd,
		ProjectPath:         projectPath,
		ProjectName:         projectName,
		SourceMtime:         newestMtime(filePath, subs),
	}
	if cs := stats.CostState; cs != nil {
		row.CostUSD, row.CostSource = cs.TotalCostUSD, repo.SourceCostState
		row.AgentMs, row.AgentTimeSource = cs.APIDurationMs+cs.ToolDurationMs, repo.SourceCostState
		row.LinesAdded, row.LinesRemoved = cs.LinesAdded, cs.LinesRemoved
		row.CostUnknownModel = cs.UnknownModelCost
		row.ModelCosts = cs.ModelCosts
	}
	return row, nil
}

func parseFile(path string) (sessionStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return sessionStats{}, fmt.Errorf("open: %w", err)
	}
	defer f.Close()
	return parseSession(f)
}

// subagentFiles returns the *.jsonl files in <dir>/<sessionId>/subagents/ next to parentPath.
func subagentFiles(parentPath string) []string {
	dir := filepath.Join(strings.TrimSuffix(parentPath, ".jsonl"), "subagents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if info, err := entry.Info(); err != nil || info.Size() > maxFileSizeBytes {
			continue
		}
		files = append(files, filepath.Join(dir, entry.Name()))
	}
	return files
}

// newestMtime returns the latest mtime (unix nanoseconds) of parentPath and subs; 0 when the parent cannot be stat'ed.
func newestMtime(parentPath string, subs []string) int64 {
	info, err := os.Stat(parentPath)
	if err != nil {
		return 0
	}
	newest := info.ModTime().UnixNano()
	for _, sub := range subs {
		if info, err := os.Stat(sub); err == nil && info.ModTime().UnixNano() > newest {
			newest = info.ModTime().UnixNano()
		}
	}
	return newest
}

// sessionIDFromPath extracts the session UUID from a JSONL filename.
func sessionIDFromPath(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, ".jsonl")
}
