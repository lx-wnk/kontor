package mcpapps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/channelconfig"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// EntryImportMarker gates the one-time copy of servers that already existed
// in ~/.claude.json onto their application rows. Without it, every startup
// would re-import and overwrite an entry edited in the app since.
const EntryImportMarker = "mcp-applications-entry-import"

// EntryHash returns the SHA-256 of entry, hex-encoded, or "" for an empty
// entry. Callers use it to detect drift between the stored entry and what was
// last exported to Claude Code.
//
// The hash is taken over a canonical form, not the bytes as they arrive: the
// entry is decoded and re-encoded, which sorts every object's keys and drops
// whitespace. Claude's config is written with indentation and the database
// holds a compact copy of the same entry, so hashing raw bytes would report
// every exported server as changed.
func EntryHash(entry []byte) string {
	if len(entry) == 0 {
		return ""
	}
	canonical := entry
	var decoded any
	if err := json.Unmarshal(entry, &decoded); err == nil {
		if encoded, err := json.Marshal(decoded); err == nil {
			canonical = encoded
		}
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// ImportEntries copies the raw ~/.claude.json entry for each non-reserved
// server onto its existing application row and marks it exported, so the
// entry a user already configured is not silently dropped once applications
// become the source of truth. Reconcile must have already created the rows;
// a server without one is skipped. Runs once per install, gated by
// EntryImportMarker.
func ImportEntries(ctx context.Context, servers map[string]json.RawMessage, apps repo.MCPApplicationRepo, markers Markers) (int, error) {
	seen, err := markers.Has(ctx, EntryImportMarker)
	if err != nil {
		return 0, fmt.Errorf("mcpapps.ImportEntries: %w", err)
	}
	if seen {
		return 0, nil
	}

	rows, err := apps.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("mcpapps.ImportEntries: %w", err)
	}
	byName := make(map[string]string, len(rows))
	for _, row := range rows {
		byName[row.ServerName] = row.ResourceID
	}

	imported := 0
	for name, raw := range servers {
		if channelconfig.IsReservedServerName(name) {
			continue
		}
		resourceID, ok := byName[name]
		if !ok {
			continue
		}
		if _, err := apps.SetEntry(ctx, resourceID, raw); err != nil {
			return imported, fmt.Errorf("mcpapps.ImportEntries: %w", err)
		}
		if _, err := apps.SetExport(ctx, resourceID, true, EntryHash(raw)); err != nil {
			return imported, fmt.Errorf("mcpapps.ImportEntries: %w", err)
		}
		imported++
	}

	if err := markers.Record(ctx, EntryImportMarker); err != nil {
		return imported, fmt.Errorf("mcpapps.ImportEntries: %w", err)
	}
	return imported, nil
}
