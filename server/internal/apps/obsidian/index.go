package obsidian

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/memory"
)

// summaryMaxRunes bounds the pointer summary derived from a note's first
// line, so a note whose first paragraph runs to several kilobytes does not
// turn "summary" into a second copy of most of the body.
const summaryMaxRunes = 200

// IndexNotes indexes the notes under client's configured VaultRoot into the
// memory space identified by spaceID, as memory_entry rows of kind
// "pointer" (source_kind "application", source_ref the note's vault-root-
// relative path). The pointer holds a summary and the path, never the note
// body: the vault stays the content, memory is only the index.
//
// Writing requires memory.write against the space, exactly like an agent
// write — the application holds no privileged path around the capability
// gate. Searching the vault requires obsidian.search, and reading a note
// requires obsidian.read, checked the same way. gate is a parameter here
// (rather than the plan's original mem-only signature) because nothing else
// in scope could supply it — a signature that cannot call the authorization
// it is required to perform is not a smaller signature, it is a broken one.
//
// POST /api/obsidian/index (internal/api/obsidian.Handler) is IndexNotes'
// production caller, built with a memory.Gate that carries no Asker (see its
// construction in serverapp/di.go) since nobody is watching that run to
// answer for it. It is not the only path to the vault: the four obsidian_*
// MCP tools (internal/mcp/tools/obsidian.go) reach Client.Read,
// Client.SearchUnderRoot, Client.Write and Client.Delete directly, gated the
// same way but with an Asker. The gate lives in the callers, not in Client
// itself: Client.Read, Client.Write, Client.Search and Client.Delete take no
// capability repos and enforce nothing, so a future caller that reaches the
// vault client directly instead bypasses this check entirely.
//
// A previous run's pointers are reconciled on every call: a note that
// disappeared from the vault since it was indexed is discovered by directly
// re-reading its path, and only a confirmed ErrNotFound (a 404) expires the
// existing entry (repo.MemoryRepo.ExpireEntry) rather than deleting it, so
// the contradiction between "memory says this exists" and "the vault does
// not have it" stays visible instead of quietly vanishing. Any other read
// error — a closed connection, a 500 — is treated as transient and leaves
// the pointer valid, the same as the indexing loop below already does for a
// Read that fails on a note Search just reported.
func IndexNotes(
	ctx context.Context,
	client *Client,
	mem repo.MemoryRepo,
	gate memory.Gate,
	spaceID string,
) (int, error) {
	space, err := mem.GetSpaceByID(ctx, spaceID)
	if err != nil {
		return 0, fmt.Errorf("obsidian.IndexNotes: resolve space: %w", err)
	}
	scope := repo.Scope{Kind: repo.ScopeKind(space.ScopeKind), Ref: space.ScopeRef}.Normalize()

	if err := gate.Authorize(ctx, repo.CapabilityMemoryWrite, space.Slug, scope); err != nil {
		return 0, fmt.Errorf("obsidian.IndexNotes: %s: %w", repo.CapabilityMemoryWrite, err)
	}

	now := time.Now()
	existing, err := mem.ListValid(ctx, space.ID, now)
	if err != nil {
		return 0, fmt.Errorf("obsidian.IndexNotes: list existing pointers: %w", err)
	}
	priorPointers := make(map[string]string, len(existing)) // note path -> entry id
	for _, e := range existing {
		if e.Kind == "pointer" && e.SourceKind == "application" && e.SourceRef != nil {
			priorPointers[*e.SourceRef] = e.ID
		}
	}

	if err := gate.Authorize(ctx, CapabilitySearch, "", scope); err != nil {
		return 0, fmt.Errorf("obsidian.IndexNotes: %s: %w", CapabilitySearch, err)
	}
	// obsidian.read covers every Client.Read call this function makes below
	// — both the new-note reads in the main loop and the existence probes
	// in the reconciliation loop. One check up front, mirroring
	// obsidian.search and memory.write above, rather than one per note: a
	// grant here is a grant to read the vault this run touches, not to any
	// one note, and nothing in the capability schema expresses a
	// per-note-path pattern for it to check instead.
	if err := gate.Authorize(ctx, CapabilityRead, "", scope); err != nil {
		return 0, fmt.Errorf("obsidian.IndexNotes: %s: %w", CapabilityRead, err)
	}

	// Client.Search searches the whole vault, not just VaultRoot (see its
	// doc comment) — this is the boundary question the client's own task
	// left open. SearchUnderRoot is the answer: it confines the results to
	// VaultRoot and hands back root-relative paths, so nothing outside the
	// configured root is read or pointed to. A memory grant's scope
	// (global/project/application) carries no vault-path semantics to
	// narrow this further. An empty query is relied on to mean "every
	// note"; that assumption is exercised here against a fake server. Its
	// proof against a live vault has no test today.
	results, err := client.SearchUnderRoot(ctx, "")
	if err != nil {
		return 0, fmt.Errorf("obsidian.IndexNotes: search: %w", err)
	}

	seen := make(map[string]bool, len(results))
	indexed := 0
	for _, r := range results {
		notePath := r.Path
		seen[notePath] = true
		if _, already := priorPointers[notePath]; already {
			continue
		}

		body, err := client.Read(ctx, notePath)
		if err != nil {
			// A note Search just reported but Read cannot fetch is a
			// transient vault condition, not invalid input to refuse —
			// unlike the cases below, there is no prior entry to protect
			// here, so skipping it leaves nothing unrecorded.
			continue
		}

		summary := firstLine(body, summaryMaxRunes)
		cleanSummary, cleanContent, err := memory.SanitizeForStore(summary, notePath)
		if err != nil {
			// An empty note (or one that sanitizes to empty) is ordinary
			// vault content, not invalid input worth aborting the whole run
			// over — every note after it in iteration order would otherwise
			// never be reached. Skip it; indexed already counts only what
			// was actually written, so the return value stays honest
			// without silently claiming this one succeeded.
			continue
		}

		sourceRef := notePath
		if _, err := mem.CreateEntry(ctx, repo.CreateEntryInput{
			SpaceID:    space.ID,
			Summary:    cleanSummary,
			Content:    cleanContent,
			Kind:       "pointer",
			SourceKind: "application",
			SourceRef:  &sourceRef,
			Confidence: 1,
		}); err != nil {
			return indexed, fmt.Errorf("obsidian.IndexNotes: create entry for %s: %w", notePath, err)
		}
		indexed++
	}

	for notePath, entryID := range priorPointers {
		if seen[notePath] {
			continue
		}
		_, err := client.Read(ctx, notePath)
		if err == nil {
			continue
		}
		if !errors.Is(err, ErrNotFound) {
			// A closed connection, a 500, an expired TLS pin — the same
			// transient conditions the main loop above skips rather than
			// treats as proof of anything. Only a confirmed 404 means the
			// note is actually gone.
			continue
		}
		if expireErr := mem.ExpireEntry(ctx, entryID, now); expireErr != nil {
			return indexed, fmt.Errorf("obsidian.IndexNotes: expire stale pointer for %s: %w", notePath, expireErr)
		}
	}

	return indexed, nil
}

// firstLine derives a short summary from a note body: its first non-blank
// line, capped to maxRunes. SanitizeForStore does the actual safety
// stripping; this only keeps a huge first paragraph from becoming the whole
// summary.
func firstLine(body string, maxRunes int) string {
	trimmed := strings.TrimSpace(body)
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	runes := []rune(trimmed)
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}
