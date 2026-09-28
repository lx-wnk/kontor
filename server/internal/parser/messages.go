package parser

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/lx-wnk/kontor/sdk"
)

// Message is the decoded shape of one entry line from a Claude session JSONL file.
// ScanMessages yields one Message per successfully decoded line; malformed lines
// are silently skipped.
type Message struct {
	Type      string
	Subtype   string
	Timestamp time.Time
	Role      string
	Model     string
	Usage     *sdk.TokenUsage
	Content   json.RawMessage
}

// decodeMessageLine decodes one JSONL line into a Message. ok is false for a
// malformed outer envelope; callers skip such lines rather than erroring, since
// a single bad line must never abort a scan. This is the single decode path
// shared by ScanMessages and ScanMessagesFrom so the two scans can never diverge.
func decodeMessageLine(line []byte) (Message, bool) {
	var outer struct {
		Type      string          `json:"type"`
		Subtype   string          `json:"subtype"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(line, &outer); err != nil {
		return Message{}, false
	}
	var inner struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Usage   *usageCounters  `json:"usage"`
		Content json.RawMessage `json:"content"`
	}
	// inner decode failure is non-fatal: some line types have no message field
	_ = json.Unmarshal(outer.Message, &inner)

	var ts time.Time
	if outer.Timestamp != "" {
		if parsed, perr := time.Parse(time.RFC3339Nano, outer.Timestamp); perr == nil {
			ts = parsed
		}
	}

	var usage *sdk.TokenUsage
	if inner.Usage != nil {
		usage = &sdk.TokenUsage{
			InputTokens:         inner.Usage.InputTokens,
			OutputTokens:        inner.Usage.OutputTokens,
			CacheCreationTokens: inner.Usage.CacheCreationTokens,
			CacheReadTokens:     inner.Usage.CacheReadTokens,
		}
	}

	return Message{
		Type:      outer.Type,
		Subtype:   outer.Subtype,
		Timestamp: ts,
		Role:      inner.Role,
		Model:     inner.Model,
		Usage:     usage,
		Content:   inner.Content,
	}, true
}

// ScanMessages opens path, reads up to maxBytes (0 = whole file), decodes each
// JSONL line into a Message, and calls fn per decoded line. Malformed lines are
// silently skipped. ErrStopScan stops iteration without error; any other fn error
// propagates. Message.Content aliases the scanner's line buffer — copy it if it
// must outlive the fn call.
func ScanMessages(path string, maxBytes int64, fn func(m Message) error) error {
	rc, err := OpenJSONLReader(path, maxBytes)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer rc.Close() //nolint:errcheck

	return ScanJSONLLines(rc, func(line []byte) error {
		m, ok := decodeMessageLine(line)
		if !ok {
			return nil
		}
		return fn(m)
	})
}

// ScanMessagesFrom scans path starting at byte offset and sums every assistant
// message's per-message usage found in the appended region, reusing
// decodeMessageLine so this can never diverge from ScanMessages. It returns the
// summed usage and newOffset — offset plus bytes through the last complete
// line's trailing '\n'; a trailing partial line (a write still in progress) is
// left unconsumed for the next call. size <= offset (nothing appended) returns
// zero usage and the offset unchanged.
func ScanMessagesFrom(path string, offset int64) (sdk.TokenUsage, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return sdk.TokenUsage{}, offset, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close() //nolint:errcheck

	info, err := f.Stat()
	if err != nil {
		return sdk.TokenUsage{}, offset, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.Size() <= offset {
		return sdk.TokenUsage{}, offset, nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return sdk.TokenUsage{}, offset, fmt.Errorf("seek %s: %w", path, err)
	}

	var total sdk.TokenUsage
	newOffset := offset
	reader := bufio.NewReaderSize(f, 256*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			newOffset += int64(len(line))
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				if m, ok := decodeMessageLine(trimmed); ok && m.Role == "assistant" && m.Usage != nil {
					total.InputTokens += m.Usage.InputTokens
					total.OutputTokens += m.Usage.OutputTokens
					total.CacheCreationTokens += m.Usage.CacheCreationTokens
					total.CacheReadTokens += m.Usage.CacheReadTokens
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break // trailing partial line — left for the next call
			}
			return sdk.TokenUsage{}, offset, fmt.Errorf("read %s: %w", path, readErr)
		}
	}
	return total, newOffset, nil
}
