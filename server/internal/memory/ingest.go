// Package memory holds the system-owned memory store: spaces, entries, and
// what gets written into them.
package memory

import (
	"errors"

	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/sanitize"
)

// ErrEmptyAfterSanitize is returned when sanitizing summary or content leaves
// either one empty. A silently emptied entry is worse than a rejected write:
// nothing would otherwise surface that anything was lost.
var ErrEmptyAfterSanitize = errors.New("memory: entry emptied by sanitization")

// SanitizeForStore prepares a memory entry's summary and content for
// persistent storage.
//
// The parser's secret scrubber runs on read-out, right before API exposure.
// A memory entry is persistent, so a secret written into it would outlive
// every later scrub — this boundary has to be the write, not the read.
//
// Trojan-source control and bidi-override runes are stripped too: memory is
// a third boundary rendered to a human (the UI) and concatenated into
// prompts, the same two reasons the sanitize package already exists for the
// tool-call boundaries it covers. Sanitizing runs before scrubbing so an
// invisible control rune spliced into the middle of a secret cannot split it
// out of the scrubber's patterns.
//
// summary and content are sanitized asymmetrically, and that asymmetry is
// load-bearing: summary goes through ForDisplay and stays single-line, so it
// can never forge the "\n\n---\n" section boundary the assembled prompt uses
// (buildMemoryBlockSuffix). content goes through ForStorage instead, which
// strips the same invisible/control runes but preserves newlines — a
// summary is prompt furniture, content is retrieved and rendered structured
// (code blocks, PEM bodies, numbered steps), and flattening it would be
// irreversible at write time.
func SanitizeForStore(summary, content string) (string, string, error) {
	cleanSummary := parser.ScrubSecrets(sanitize.ForDisplay(summary))
	cleanContent := parser.ScrubSecrets(sanitize.ForStorage(content))

	if cleanSummary == "" || cleanContent == "" {
		return "", "", ErrEmptyAfterSanitize
	}
	return cleanSummary, cleanContent, nil
}
