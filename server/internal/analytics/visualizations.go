package analytics

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/parser"
)

// maxCoOccurrenceTools caps the matrix dimension returned by
// BuildCoOccurrence. Excess tools are dropped after sorting by session
// count and Meta.Truncated is flipped on.
const maxCoOccurrenceTools = 50

// BuildSankey aggregates consecutive (prev → curr) tool transitions
// across all sessions discovered by ScanOpts. Nodes are the unique tool
// names that participated in at least one transition; links carry the
// summed transition count.
func BuildSankey(ctx context.Context, opts ScanOpts, dirs []string) (sdk.SankeyData, error) {
	byID, err := scanSessionsForTools(ctx, opts, dirs)
	if err != nil {
		return sdk.SankeyData{}, err
	}
	transitions := make(map[[2]string]int)
	totalCalls := 0
	for _, calls := range byID {
		totalCalls += len(calls)
		for i := 1; i < len(calls); i++ {
			prev := calls[i-1].Name
			curr := calls[i].Name
			if prev == curr {
				// A tool immediately following itself is not an inter-tool
				// transition, and d3-sankey rejects self-loops outright.
				continue
			}
			transitions[[2]string{prev, curr}]++
		}
	}

	// d3-sankey can only lay out a DAG; raw tool transitions are cyclic
	// (Read⇄Edit ping-pong, longer loops). acyclicSankeyLinks collapses
	// bidirectional pairs to net flow and breaks any remaining cycles.
	links := acyclicSankeyLinks(transitions)

	// Nodes are derived from the surviving links so a tool whose only edges
	// were dropped (or that only self-looped) does not appear as an island.
	nodeNames := make(map[string]bool, len(links)*2)
	for _, l := range links {
		nodeNames[l.Source] = true
		nodeNames[l.Target] = true
	}
	names := make([]string, 0, len(nodeNames))
	for n := range nodeNames {
		names = append(names, n)
	}
	sort.Strings(names)
	nodes := make([]sdk.SankeyNode, 0, len(names))
	for _, n := range names {
		nodes = append(nodes, sdk.SankeyNode{ID: n, Name: n})
	}

	return sdk.SankeyData{
		Nodes: nodes,
		Links: links,
		Meta: sdk.SankeyMeta{
			SessionCount: len(byID),
			CallCount:    totalCalls,
		},
	}, nil
}

// acyclicSankeyLinks turns raw directed transition counts into a sorted,
// acyclic link set suitable for d3-sankey. Self-loops must already be
// excluded by the caller. Two steps:
//  1. each bidirectional pair A⇄B is collapsed into one net-flow edge in
//     the direction of the larger count (equal counts net to zero → no edge);
//  2. any remaining directed cycle (e.g. A→B→C→A) is broken by greedily
//     dropping back-edges until the graph is acyclic.
func acyclicSankeyLinks(counts map[[2]string]int) []sdk.SankeyLink {
	seen := make(map[[2]string]bool, len(counts))
	links := make([]sdk.SankeyLink, 0, len(counts))
	for key, fwd := range counts {
		rev := [2]string{key[1], key[0]}
		if seen[key] || seen[rev] {
			continue
		}
		seen[key] = true
		seen[rev] = true
		switch back := counts[rev]; {
		case fwd > back:
			links = append(links, sdk.SankeyLink{Source: key[0], Target: key[1], Value: fwd - back})
		case back > fwd:
			links = append(links, sdk.SankeyLink{Source: key[1], Target: key[0], Value: back - fwd})
		}
	}
	// Sort before cycle-breaking so back-edge removal is reproducible.
	sort.Slice(links, func(i, j int) bool {
		if links[i].Source != links[j].Source {
			return links[i].Source < links[j].Source
		}
		return links[i].Target < links[j].Target
	})
	for {
		idx := firstBackEdgeIndex(links)
		if idx < 0 {
			break
		}
		links = append(links[:idx], links[idx+1:]...)
	}
	return links
}

// firstBackEdgeIndex returns the slice index of the first link that closes
// a directed cycle, or -1 if the graph is already acyclic. The DFS visits
// nodes and out-edges in sorted order so "first" — and therefore which
// edge acyclicSankeyLinks drops — is deterministic across runs.
func firstBackEdgeIndex(links []sdk.SankeyLink) int {
	type out struct {
		tgt string
		idx int
	}
	adj := make(map[string][]out)
	nodeSet := make(map[string]struct{})
	for i, l := range links {
		adj[l.Source] = append(adj[l.Source], out{l.Target, i})
		nodeSet[l.Source] = struct{}{}
		nodeSet[l.Target] = struct{}{}
	}
	nodes := make([]string, 0, len(nodeSet))
	for n := range nodeSet {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(nodes))
	found := -1
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		for _, e := range adj[n] {
			switch color[e.tgt] {
			case gray:
				found = e.idx
				return true
			case white:
				if dfs(e.tgt) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for _, n := range nodes {
		if color[n] == white && dfs(n) {
			return found
		}
	}
	return -1
}

// BuildDAG walks one session JSONL end-to-end and emits chronological
// node + edge data. ScanOpts.Sessions must contain exactly one entry; the
// helper looks the file up via DiscoverSessions to keep config-dir
// resolution centralized.
func BuildDAG(ctx context.Context, opts ScanOpts, dirs []string) (sdk.DAGData, error) {
	if len(opts.Sessions) != 1 || opts.Sessions[0] == "" {
		return sdk.DAGData{}, errors.New("dag requires exactly one session id")
	}
	files := DiscoverSessions(opts, dirs)
	if len(files) == 0 {
		return sdk.DAGData{}, fmt.Errorf("session %s not found", opts.Sessions[0])
	}
	if err := ctx.Err(); err != nil {
		return sdk.DAGData{}, err
	}
	return readDAGFromFile(files[0].Path)
}

// readDAGFromFile streams a single JSONL file and produces nodes for
// each assistant/user message and each tool_use block, plus chrono edges
// between consecutive nodes and result edges from tool_use → tool_result.
func readDAGFromFile(path string) (sdk.DAGData, error) {
	f, err := os.Open(path)
	if err != nil {
		return sdk.DAGData{}, err
	}
	defer f.Close() //nolint:errcheck

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	var nodes []sdk.DAGNode
	var links []sdk.DAGLink
	// Track tool_use_id → node ID so tool_result lines can wire back.
	toolUseNodeID := make(map[string]string)
	lineIdx := 0
	prevNodeID := ""

	type dagEnv struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
	}
	type dagMsg struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	type dagBlock struct {
		Type       string `json:"type"`
		Name       string `json:"name"`
		ID         string `json:"id"`
		Text       string `json:"text"`
		ToolUseID  string `json:"tool_use_id"`
		ContentTxt string `json:"content"`
	}

	addNode := func(node sdk.DAGNode) {
		nodes = append(nodes, node)
		if prevNodeID != "" {
			links = append(links, sdk.DAGLink{Source: prevNodeID, Target: node.ID, Kind: "chrono"})
		}
		prevNodeID = node.ID
	}

	for scanner.Scan() {
		lineIdx++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var env dagEnv
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			continue
		}
		if env.Type != "assistant" && env.Type != "user" && env.Type != "message" {
			continue
		}
		var msg dagMsg
		if err := json.Unmarshal(env.Message, &msg); err != nil {
			continue
		}
		switch msg.Role {
		case "assistant":
			var hasToolUse bool
			turnID := fmt.Sprintf("assist-%d", lineIdx)
			for _, raw := range msg.Content {
				var b dagBlock
				if err := json.Unmarshal(raw, &b); err != nil {
					continue
				}
				if b.Type == "tool_use" && ToolNameRE.MatchString(b.Name) {
					hasToolUse = true
					nodeID := fmt.Sprintf("tool-%d-%s", lineIdx, b.ID)
					toolUseNodeID[b.ID] = nodeID
					addNode(sdk.DAGNode{
						ID:    nodeID,
						Type:  "tool",
						Label: b.Name,
						Ts:    env.Timestamp,
					})
				}
			}
			// If the turn contained only text (no tool_use), still emit an
			// assistant node so chrono edges connect across pure text turns.
			if !hasToolUse {
				addNode(sdk.DAGNode{ID: turnID, Type: "assistant", Label: "assistant", Ts: env.Timestamp})
			}
		case "user":
			// User messages may carry tool_result blocks — wire them back.
			userTurnID := fmt.Sprintf("user-%d", lineIdx)
			hadResult := false
			for _, raw := range msg.Content {
				var b dagBlock
				if err := json.Unmarshal(raw, &b); err != nil {
					continue
				}
				if b.Type == "tool_result" && b.ToolUseID != "" {
					if src, ok := toolUseNodeID[b.ToolUseID]; ok {
						resultNodeID := fmt.Sprintf("result-%d-%s", lineIdx, b.ToolUseID)
						addNode(sdk.DAGNode{
							ID:    resultNodeID,
							Type:  "user",
							Label: "tool_result",
							Ts:    env.Timestamp,
						})
						links = append(links, sdk.DAGLink{Source: src, Target: resultNodeID, Kind: "result"})
						hadResult = true
					}
				}
			}
			if !hadResult {
				addNode(sdk.DAGNode{ID: userTurnID, Type: "user", Label: "user", Ts: env.Timestamp})
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return sdk.DAGData{}, err
	}
	return sdk.DAGData{Nodes: nodes, Links: links}, nil
}

// BuildCoOccurrence emits a symmetric session-count matrix. Tools are
// ordered by descending session count; ties broken alphabetically.
func BuildCoOccurrence(ctx context.Context, opts ScanOpts, dirs []string) (sdk.CoOccurrenceData, error) {
	byID, err := scanSessionsForTools(ctx, opts, dirs)
	if err != nil {
		return sdk.CoOccurrenceData{}, err
	}
	// Per-tool session set so multiple calls in one session count once.
	bySet := make(map[string]map[string]bool)
	for sessID, calls := range byID {
		for _, c := range calls {
			if _, ok := bySet[c.Name]; !ok {
				bySet[c.Name] = make(map[string]bool)
			}
			bySet[c.Name][sessID] = true
		}
	}

	type toolCount struct {
		name  string
		count int
	}
	ranked := make([]toolCount, 0, len(bySet))
	for name, sessSet := range bySet {
		ranked = append(ranked, toolCount{name: name, count: len(sessSet)})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].name < ranked[j].name
	})

	truncated := false
	if len(ranked) > maxCoOccurrenceTools {
		ranked = ranked[:maxCoOccurrenceTools]
		truncated = true
	}

	tools := make([]string, len(ranked))
	for i, r := range ranked {
		tools[i] = r.name
	}
	n := len(tools)
	matrix := make([][]int, n)
	for i := range matrix {
		matrix[i] = make([]int, n)
	}
	for i, a := range tools {
		setA := bySet[a]
		for j := i; j < n; j++ {
			setB := bySet[tools[j]]
			count := 0
			// Iterate smaller set for efficiency.
			small, large := setA, setB
			if len(setB) < len(setA) {
				small, large = setB, setA
			}
			for s := range small {
				if large[s] {
					count++
				}
			}
			matrix[i][j] = count
			matrix[j][i] = count
		}
	}

	// Compute lift matrix: Lift[i][j] = (c_ij × N) / (c_i × c_j).
	// Diagonal is set to 0 (self-lift is meaningless).
	// Guards: if c_i == 0 || c_j == 0 || N == 0 → lift = 0.
	N := len(byID)
	lift := make([][]float64, n)
	for i := range lift {
		lift[i] = make([]float64, n)
	}
	for i := 0; i < n; i++ {
		ci := matrix[i][i]
		for j := i + 1; j < n; j++ {
			cj := matrix[j][j]
			cij := matrix[i][j]
			var v float64
			if ci > 0 && cj > 0 && N > 0 {
				v = float64(cij*N) / float64(ci*cj)
			}
			lift[i][j] = v
			lift[j][i] = v
		}
		// Diagonal stays 0 (already zero-initialized).
	}

	return sdk.CoOccurrenceData{
		Tools:  tools,
		Matrix: matrix,
		Lift:   lift,
		Meta: sdk.CoOccurrenceMeta{
			SessionCount: len(byID),
			Truncated:    truncated,
		},
	}, nil
}

// BuildSpawnTree discovers parent → subagent relationships by inspecting
// the ~/.claude/projects/{encoded}/{sessionId}/subagents/*.jsonl
// directories that the merger writes for every spawned subagent. Each
// session in the scan window becomes a node; depth is the BFS distance
// from a root (a session with no parent).
func BuildSpawnTree(ctx context.Context, opts ScanOpts, dirs []string) (sdk.SpawnTreeData, error) {
	files := DiscoverSessions(opts, dirs)
	if err := ctx.Err(); err != nil {
		return sdk.SpawnTreeData{}, err
	}

	// Best-effort enrichment: fetch session metadata once. Errors are logged
	// but never propagate — enrichment is additive and must not break the tree.
	sessionIndex := make(map[string]parser.SessionInfo)
	if sessions, err := parser.GetSessions(ctx); err != nil {
		slog.Warn("analytics: spawn-tree enrichment skipped", "err", err)
	} else {
		for _, si := range sessions {
			sessionIndex[si.SessionID] = si
		}
	}

	type rec struct {
		sessionID string
		parent    string
		toolCount int
	}
	recs := make(map[string]*rec, len(files))

	for _, sf := range files {
		toolCount := 0
		// Tool count is the number of tool_use entries.
		calls, err := readToolCallsFromFile(sf.Path, sf.SessionID, opts.From, opts.To)
		if err == nil {
			toolCount = len(calls)
		}
		// Preserve any parent already recorded by an earlier iteration —
		// a sibling that processed this session's parent first would have
		// created the rec with .parent set. Overwriting unconditionally
		// would clobber that link whenever DiscoverSessions returns the
		// parent before the child (filesystem-dependent mtime ordering).
		if existing, ok := recs[sf.SessionID]; ok {
			existing.toolCount = toolCount
		} else {
			recs[sf.SessionID] = &rec{sessionID: sf.SessionID, toolCount: toolCount}
		}

		// Look for subagent directories next to this jsonl, plus any
		// nested subagents/*.jsonl that this session itself spawned.
		// Convention (per .agent-context/conventions.md):
		//   ~/.claude/projects/{encoded}/{sessionId}/subagents/*.jsonl
		// where {sessionId} matches the JSONL file basename.
		subDir := filepath.Join(filepath.Dir(sf.Path), sf.SessionID, "subagents")
		entries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
				continue
			}
			childID := strings.TrimSuffix(name, ".jsonl")
			if _, exists := recs[childID]; !exists {
				recs[childID] = &rec{sessionID: childID}
			}
			recs[childID].parent = sf.SessionID
		}
	}

	// Build node/link slices. Roots have empty parent.
	var nodes []sdk.SpawnTreeNode
	var links []sdk.SpawnTreeLink
	var roots []string
	depth := make(map[string]int, len(recs))
	// BFS from each root to compute depth.
	for id, r := range recs {
		if r.parent == "" {
			roots = append(roots, id)
			depth[id] = 0
		}
	}
	sort.Strings(roots)
	queue := append([]string{}, roots...)
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		for childID, r := range recs {
			if r.parent == head {
				depth[childID] = depth[head] + 1
				queue = append(queue, childID)
			}
		}
	}

	ids := make([]string, 0, len(recs))
	for id := range recs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := recs[id]
		node := sdk.SpawnTreeNode{
			ID:        id,
			Label:     shortLabel(id),
			Depth:     depth[id],
			ToolCount: r.toolCount,
		}
		if info, ok := sessionIndex[id]; ok {
			node.Project = info.ProjectName
			node.Model = derefOr(info.Model, "")
			node.FirstPrompt = derefOr(info.FirstPrompt, "")
			node.CostCents = int(math.Round(info.CostEstimate * 100))
			node.Label = spawnTreeLabel(node.FirstPrompt, info.ProjectName, id)
		}
		nodes = append(nodes, node)
		if r.parent != "" {
			links = append(links, sdk.SpawnTreeLink{Source: r.parent, Target: id})
		}
	}
	return sdk.SpawnTreeData{Roots: roots, Nodes: nodes, Links: links}, nil
}

// spawnTreeLabel returns the best human-readable label for a spawn-tree node.
// Priority: firstPrompt (truncated to 60 runes) > projectName > shortLabel(id).
func spawnTreeLabel(firstPrompt, projectName, id string) string {
	if firstPrompt != "" {
		return truncateRunes(firstPrompt, 60)
	}
	if projectName != "" {
		return projectName
	}
	return shortLabel(id)
}

// truncateRunes returns s truncated to maxRunes runes. If s is longer, an
// ellipsis ("…") is appended after truncation.
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
}

// derefOr dereferences a *string pointer; returns fallback if the pointer is nil.
func derefOr(p *string, fallback string) string {
	if p == nil {
		return fallback
	}
	return *p
}

// shortLabel renders an 8-char prefix of a UUID so the spawn tree shows
// a readable label without truncating client-side.
func shortLabel(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
