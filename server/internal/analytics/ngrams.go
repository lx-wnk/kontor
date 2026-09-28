// Package analytics implements workflow pattern discovery via JSONL ngram analysis.
package analytics

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/lx-wnk/kontor/server/internal/parser"
)

const (
	ngramSize   = 3
	maxPatterns = 20
	maxFileSize = 10 * 1024 * 1024 // 10 MB per file cap
)

// patternEntry holds a discovered ngram and its occurrence count.
type patternEntry struct {
	tools     string
	frequency int
}

// ExtractNgrams computes 3-grams from a slice of tool names.
// Returns a map from gram string → occurrence count.
func ExtractNgrams(toolSequence []string) map[string]int {
	counts := make(map[string]int)
	if len(toolSequence) < ngramSize {
		return counts
	}
	for i := 0; i <= len(toolSequence)-ngramSize; i++ {
		gram := strings.Join(toolSequence[i:i+ngramSize], " → ")
		counts[gram]++
	}
	return counts
}

const discoverConcurrency = 8

// DiscoverPatterns iterates all JSONL session files, extracts tool_use events,
// computes 3-grams, and upserts the top-20 patterns into workflow_patterns.
// File reads are parallelized (up to discoverConcurrency goroutines).
func DiscoverPatterns(ctx context.Context, db *sql.DB) error {
	// Collect all JSONL file paths first so the parallel fan-out is clean.
	var filePaths []string
	for _, configDir := range parser.AllClaudeConfigDirs() {
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
				if !f.IsDir() && strings.HasSuffix(f.Name(), ".jsonl") {
					filePaths = append(filePaths, filepath.Join(dirPath, f.Name()))
				}
			}
		}
	}
	if len(filePaths) == 0 {
		return nil
	}

	// Parallel file reads: each goroutine produces a partial count map; merge at the end.
	type partial = map[string]int
	partials := make([]partial, len(filePaths))
	for i := range partials {
		partials[i] = make(partial)
	}

	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, discoverConcurrency)
	for i, path := range filePaths {
		i, path := i, path
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-gctx.Done():
				return gctx.Err()
			}
			tools, err := extractToolsFromJSONL(path)
			if err != nil {
				slog.Debug("analytics: skip file", "path", path, "err", err)
				return nil
			}
			for gram, count := range ExtractNgrams(tools) {
				partials[i][gram] += count
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}

	// Merge is sequential (g.Wait() above ensures all writes are done).
	globalCounts := make(map[string]int)
	for _, p := range partials {
		for gram, count := range p {
			globalCounts[gram] += count
		}
	}

	if len(globalCounts) == 0 {
		return nil
	}

	// Sort by frequency descending, cap at top 20.
	entries := make([]patternEntry, 0, len(globalCounts))
	for gram, count := range globalCounts {
		entries = append(entries, patternEntry{tools: gram, frequency: count})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].frequency != entries[j].frequency {
			return entries[i].frequency > entries[j].frequency
		}
		return entries[i].tools < entries[j].tools
	})
	if len(entries) > maxPatterns {
		entries = entries[:maxPatterns]
	}

	return upsertPatterns(db, entries)
}

// upsertPatterns replaces all workflow_patterns with the given top entries in a
// single transaction, pruning any patterns that fell out of the top-N.
func upsertPatterns(db *sql.DB, top []patternEntry) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec("DELETE FROM workflow_patterns"); err != nil {
		return err
	}
	for _, p := range top {
		if _, err := tx.Exec(
			"INSERT INTO workflow_patterns (tools, frequency, last_seen_at) VALUES (?, ?, ?)",
			p.tools, p.frequency, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// extractToolsFromJSONL reads a JSONL file (capped at maxFileSize) and returns
// all tool names in the order they appear in assistant messages.
func extractToolsFromJSONL(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size > maxFileSize {
		size = maxFileSize
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// If file is large, tail-read the last maxFileSize bytes.
	if info.Size() > maxFileSize {
		if _, err := f.Seek(-maxFileSize, io.SeekEnd); err != nil {
			return nil, err
		}
	}

	buf := make([]byte, size)
	n, err := io.ReadAtLeast(f, buf, 1)
	if err != nil && n == 0 {
		return nil, err
	}
	raw := string(buf[:n])

	return parseToolsFromRaw(raw), nil
}

// jsonlEntry is the minimal parse target for JSONL lines.
type jsonlEntry struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message"`
}

type jsonlMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

type jsonlContentBlock struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

// parseToolsFromRaw extracts tool names from raw JSONL content.
func parseToolsFromRaw(raw string) []string {
	var tools []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry jsonlEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.Type != "assistant" && entry.Type != "message" {
			continue
		}
		if len(entry.Message) == 0 {
			continue
		}
		var msg jsonlMessage
		if err := json.Unmarshal(entry.Message, &msg); err != nil {
			continue
		}
		if msg.Role != "assistant" {
			continue
		}
		for _, rawBlock := range msg.Content {
			var block jsonlContentBlock
			if err := json.Unmarshal(rawBlock, &block); err != nil {
				continue
			}
			if block.Type == "tool_use" && ToolNameRE.MatchString(block.Name) {
				tools = append(tools, block.Name)
			}
		}
	}
	return tools
}
