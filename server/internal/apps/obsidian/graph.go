package obsidian

import (
	"context"
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
)

// GraphNote is one Markdown note under VaultRoot.
type GraphNote struct {
	Path    string // relative to VaultRoot
	MtimeMs int64
}

// Graph is the vault's notes under VaultRoot and the links between them.
type Graph struct {
	Notes []GraphNote // sorted by Path
	Links [][2]int    // indices into Notes, from → to, unique, no self-links
}

// Graph reads every note's mtime and resolved outgoing links in two searches; no note body is read.
func (c *Client) Graph(ctx context.Context) (Graph, error) {
	mtimes, err := c.SearchJSONLogic(ctx, `{"var":"stat.mtime"}`)
	if err != nil {
		return Graph{}, err
	}
	links, err := c.SearchJSONLogic(ctx, `{"var":"links"}`)
	if err != nil {
		return Graph{}, err
	}
	var g Graph
	for _, hit := range mtimes {
		rel, ok := pathUnderRoot(c.vaultRoot, hit.Filename)
		if !ok || !strings.HasSuffix(rel, ".md") {
			continue
		}
		var mtime float64
		// One note's malformed mtime must not blank the whole graph, same as a malformed links value below.
		if err := json.Unmarshal(hit.Result, &mtime); err != nil {
			slog.Warn("obsidian: graph: skipping note with malformed mtime", "path", rel)
			continue
		}
		g.Notes = append(g.Notes, GraphNote{Path: rel, MtimeMs: int64(mtime)})
	}
	sort.Slice(g.Notes, func(i, j int) bool { return g.Notes[i].Path < g.Notes[j].Path })
	index := make(map[string]int, len(g.Notes))
	for i, n := range g.Notes {
		index[n.Path] = i
	}
	seen := make(map[[2]int]bool)
	for _, hit := range links {
		fromRel, ok := pathUnderRoot(c.vaultRoot, hit.Filename)
		from, known := index[fromRel]
		if !ok || !known {
			continue
		}
		var targets []string
		// One note's malformed links value must not blank the whole graph.
		if err := json.Unmarshal(hit.Result, &targets); err != nil {
			continue
		}
		for _, t := range targets {
			toRel, ok := pathUnderRoot(c.vaultRoot, t)
			to, known := index[toRel]
			if !ok || !known || to == from {
				continue
			}
			pair := [2]int{from, to}
			if !seen[pair] {
				seen[pair] = true
				g.Links = append(g.Links, pair)
			}
		}
	}
	return g, nil
}
