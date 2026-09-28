package analytics

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lx-wnk/kontor/sdk"
)

func TestBuildSankey_LinearFixture(t *testing.T) {
	sessID := "10000000-0000-0000-0000-000000000001"
	root := setupFakeClaudeDir(t, map[string]string{sessID: "single-session-linear.jsonl"})

	got, err := BuildSankey(context.Background(), ScanOpts{}, []string{root})
	if err != nil {
		t.Fatalf("BuildSankey: %v", err)
	}
	if got.Meta.SessionCount != 1 {
		t.Errorf("SessionCount = %d, want 1", got.Meta.SessionCount)
	}
	if got.Meta.CallCount != 3 {
		t.Errorf("CallCount = %d, want 3", got.Meta.CallCount)
	}
	// 3 unique tool names → 3 nodes.
	if len(got.Nodes) != 3 {
		t.Errorf("len(Nodes) = %d, want 3", len(got.Nodes))
	}
	// 2 consecutive transitions: Read→Edit, Edit→Bash.
	if len(got.Links) != 2 {
		t.Fatalf("len(Links) = %d, want 2", len(got.Links))
	}
	want := map[string]int{"Read|Edit": 1, "Edit|Bash": 1}
	for _, l := range got.Links {
		key := l.Source + "|" + l.Target
		if want[key] != l.Value {
			t.Errorf("link %s value = %d, want %d", key, l.Value, want[key])
		}
	}
}

func TestBuildSankey_EmptyWindow(t *testing.T) {
	root := setupFakeClaudeDir(t, map[string]string{})
	got, err := BuildSankey(context.Background(), ScanOpts{}, []string{root})
	if err != nil {
		t.Fatalf("BuildSankey: %v", err)
	}
	if len(got.Nodes) != 0 || len(got.Links) != 0 {
		t.Errorf("expected empty, got %+v", got)
	}
}

func TestBuildDAG_RequiresOneSession(t *testing.T) {
	_, err := BuildDAG(context.Background(), ScanOpts{}, nil)
	if err == nil {
		t.Fatal("expected error for missing session")
	}
}

func TestBuildDAG_LinearFixture(t *testing.T) {
	sessID := "10000000-0000-0000-0000-000000000002"
	root := setupFakeClaudeDir(t, map[string]string{sessID: "single-session-linear.jsonl"})

	got, err := BuildDAG(context.Background(), ScanOpts{Sessions: []string{sessID}}, []string{root})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	// 3 tool nodes + 3 user nodes (one per tool_result line). The user line
	// before any tool calls has only text and does not introduce a node
	// here because BuildDAG only emits user nodes with tool_results OR a
	// generic "user" node when none — the first user line "please help"
	// has neither tool_result nor any blocks our parser keeps, so it lands
	// as a generic user node.
	wantToolCount := 3
	wantResultCount := 3
	toolNodes := 0
	resultLinks := 0
	for _, n := range got.Nodes {
		if n.Type == "tool" {
			toolNodes++
		}
	}
	for _, l := range got.Links {
		if l.Kind == "result" {
			resultLinks++
		}
	}
	if toolNodes != wantToolCount {
		t.Errorf("tool nodes = %d, want %d", toolNodes, wantToolCount)
	}
	if resultLinks != wantResultCount {
		t.Errorf("result links = %d, want %d", resultLinks, wantResultCount)
	}
}

func TestBuildCoOccurrence_TwoSessions(t *testing.T) {
	rootDir := t.TempDir()
	projectDir := filepath.Join(rootDir, "projects", "-tmp-fixture")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for sessID, fixture := range map[string]string{
		"20000000-0000-0000-0000-000000000001": "two-sessions-branching.jsonl",
		"20000000-0000-0000-0000-000000000002": "two-sessions-branching-2.jsonl",
	} {
		data, err := os.ReadFile(filepath.Join("testdata", fixture))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projectDir, sessID+".jsonl"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := BuildCoOccurrence(context.Background(), ScanOpts{}, []string{rootDir})
	if err != nil {
		t.Fatalf("BuildCoOccurrence: %v", err)
	}
	if got.Meta.SessionCount != 2 {
		t.Errorf("SessionCount = %d, want 2", got.Meta.SessionCount)
	}
	// Tools across both sessions: Read, Edit, Bash, Grep.
	if len(got.Tools) != 4 {
		t.Fatalf("len(Tools) = %d, want 4 (got %v)", len(got.Tools), got.Tools)
	}
	// Matrix must be symmetric.
	for i := range got.Tools {
		for j := range got.Tools {
			if got.Matrix[i][j] != got.Matrix[j][i] {
				t.Errorf("matrix not symmetric at [%d][%d]=%d vs [%d][%d]=%d",
					i, j, got.Matrix[i][j], j, i, got.Matrix[j][i])
			}
		}
	}
	// Read+Bash appear together in both sessions.
	idx := make(map[string]int, len(got.Tools))
	for i, name := range got.Tools {
		idx[name] = i
	}
	if got.Matrix[idx["Read"]][idx["Bash"]] != 2 {
		t.Errorf("Read+Bash co-occurrence = %d, want 2", got.Matrix[idx["Read"]][idx["Bash"]])
	}
	// Diagonal: Read appears in 2 sessions, Edit in 1, Bash in 2, Grep in 1.
	wantDiag := map[string]int{"Read": 2, "Edit": 1, "Bash": 2, "Grep": 1}
	for name, want := range wantDiag {
		if got.Matrix[idx[name]][idx[name]] != want {
			t.Errorf("diagonal[%s] = %d, want %d", name, got.Matrix[idx[name]][idx[name]], want)
		}
	}

	// ---- Lift matrix assertions ----
	// N = 2 sessions. Lift[i][j] = (c_ij * N) / (c_i * c_j).
	if len(got.Lift) != 4 {
		t.Fatalf("len(Lift) = %d, want 4", len(got.Lift))
	}
	for i := range got.Lift {
		if len(got.Lift[i]) != 4 {
			t.Fatalf("len(Lift[%d]) = %d, want 4", i, len(got.Lift[i]))
		}
	}

	// Diagonal must be 0 for all tools.
	for _, name := range got.Tools {
		i := idx[name]
		if got.Lift[i][i] != 0 {
			t.Errorf("Lift diagonal[%s] = %v, want 0", name, got.Lift[i][i])
		}
	}

	// Lift must be symmetric.
	for i := range got.Tools {
		for j := range got.Tools {
			if got.Lift[i][j] != got.Lift[j][i] {
				t.Errorf("Lift not symmetric at [%d][%d]=%v vs [%d][%d]=%v",
					i, j, got.Lift[i][j], j, i, got.Lift[j][i])
			}
		}
	}

	// Verify formula on specific pairs computed by hand (N=2):
	// Session 1: Read, Edit, Bash.  Session 2: Read, Grep, Bash.
	// c_Read=2, c_Edit=1, c_Bash=2, c_Grep=1.
	type liftCase struct {
		a, b string
		cij  int
	}
	liftCases := []liftCase{
		// Read+Bash: both in 2 sessions → lift=(2*2)/(2*2)=1.0
		{"Read", "Bash", 2},
		// Read+Edit: co-occur in 1 session → lift=(1*2)/(2*1)=1.0
		{"Read", "Edit", 1},
		// Read+Grep: co-occur in 1 session → lift=(1*2)/(2*1)=1.0
		{"Read", "Grep", 1},
		// Edit+Grep: never co-occur → lift=0
		{"Edit", "Grep", 0},
	}
	N := 2
	for _, tc := range liftCases {
		i, j := idx[tc.a], idx[tc.b]
		ci := got.Matrix[i][i]
		cj := got.Matrix[j][j]
		var wantLift float64
		if ci > 0 && cj > 0 && N > 0 {
			wantLift = float64(tc.cij*N) / float64(ci*cj)
		}
		if got.Lift[i][j] != wantLift {
			t.Errorf("Lift[%s][%s] = %v, want %v", tc.a, tc.b, got.Lift[i][j], wantLift)
		}
	}
}

func TestBuildSpawnTree_ParentChildFromSubagentDir(t *testing.T) {
	rootDir := t.TempDir()
	projectDir := filepath.Join(rootDir, "projects", "-tmp-fixture")
	parent := "30000000-0000-0000-0000-000000000001"
	child1 := "30000000-0000-0000-0000-0000000000c1"
	child2 := "30000000-0000-0000-0000-0000000000c2"

	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentData, err := os.ReadFile(filepath.Join("testdata", "single-session-linear.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent, child1, child2} {
		if err := os.WriteFile(filepath.Join(projectDir, id+".jsonl"), parentData, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Force the iteration order that previously triggered a parent-clobber
	// bug: DiscoverSessions sorts by mtime descending, so making the parent
	// the newest file means it is processed FIRST. The parent then creates
	// child recs with .parent set; without the fix, the subsequent child
	// iterations would overwrite recs[childID] and drop the parent link.
	// This filesystem-dependent ordering is why CI (different mtime
	// resolution / filesystem) caught the bug while local mac runs masked it.
	base := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	mtimes := map[string]time.Time{
		child1: base,
		child2: base.Add(time.Minute),
		parent: base.Add(2 * time.Minute), // newest → processed first
	}
	for id, ts := range mtimes {
		if err := os.Chtimes(filepath.Join(projectDir, id+".jsonl"), ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	// Subagent dir convention: {projectDir}/{parentID}/subagents/{childID}.jsonl
	subDir := filepath.Join(projectDir, parent, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{child1, child2} {
		if err := os.WriteFile(filepath.Join(subDir, id+".jsonl"), parentData, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, err := BuildSpawnTree(context.Background(), ScanOpts{}, []string{rootDir})
	if err != nil {
		t.Fatalf("BuildSpawnTree: %v", err)
	}
	if len(got.Roots) != 1 || got.Roots[0] != parent {
		t.Errorf("Roots = %v, want [%s]", got.Roots, parent)
	}
	if len(got.Links) != 2 {
		t.Errorf("Links = %d, want 2", len(got.Links))
	}
	depthByID := map[string]int{}
	for _, n := range got.Nodes {
		depthByID[n.ID] = n.Depth
	}
	if depthByID[parent] != 0 {
		t.Errorf("parent depth = %d, want 0", depthByID[parent])
	}
	if depthByID[child1] != 1 || depthByID[child2] != 1 {
		t.Errorf("child depths = %d/%d, want 1/1", depthByID[child1], depthByID[child2])
	}
}

// ---- Pure-function tests for spawn-tree enrichment helpers ----

func TestSpawnTreeLabel_FirstPromptPreferred(t *testing.T) {
	got := spawnTreeLabel("Fix the login bug", "myproject", "abcd1234-0000-0000-0000-000000000000")
	if got != "Fix the login bug" {
		t.Errorf("got %q, want firstPrompt", got)
	}
}

func TestSpawnTreeLabel_ProjectFallback(t *testing.T) {
	got := spawnTreeLabel("", "myproject", "abcd1234-0000-0000-0000-000000000000")
	if got != "myproject" {
		t.Errorf("got %q, want projectName", got)
	}
}

func TestSpawnTreeLabel_ShortLabelFallback(t *testing.T) {
	got := spawnTreeLabel("", "", "abcd1234-0000-0000-0000-000000000000")
	if got != "abcd1234" {
		t.Errorf("got %q, want shortLabel", got)
	}
}

func TestTruncateRunes_ShortPassthrough(t *testing.T) {
	s := "hello"
	if got := truncateRunes(s, 60); got != s {
		t.Errorf("got %q, want %q", got, s)
	}
}

func TestTruncateRunes_TruncatesWithEllipsis(t *testing.T) {
	// 61 'a' runes should be truncated to 60 + "…"
	long := strings.Repeat("a", 61)
	got := truncateRunes(long, 60)
	runes := []rune(got)
	// Last rune must be '…'
	if runes[len(runes)-1] != '…' {
		t.Errorf("no ellipsis: %q", got)
	}
	// Content must be exactly 60 'a' runes + ellipsis = 61 runes total
	if len(runes) != 61 {
		t.Errorf("rune count = %d, want 61", len(runes))
	}
}

func TestTruncateRunes_MultibyteRunes(t *testing.T) {
	// "日本語" has 3 runes; truncating to 2 should give "日本…"
	got := truncateRunes("日本語テスト", 2)
	want := "日本…"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestDerefOr_Nil(t *testing.T) {
	if got := derefOr(nil, "default"); got != "default" {
		t.Errorf("got %q, want \"default\"", got)
	}
}

func TestDerefOr_NonNil(t *testing.T) {
	s := "value"
	if got := derefOr(&s, "default"); got != "value" {
		t.Errorf("got %q, want \"value\"", got)
	}
}

func TestCostCentsConversion(t *testing.T) {
	// $1.50 → 150 cents
	costUSD := 1.50
	got := int(math.Round(costUSD * 100))
	if got != 150 {
		t.Errorf("got %d, want 150", got)
	}
	// $0.0 → 0 cents
	got = int(math.Round(0.0 * 100))
	if got != 0 {
		t.Errorf("got %d, want 0", got)
	}
	// $0.01 → 1 cent
	got = int(math.Round(0.01 * 100))
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

// hasCycleLinks reports whether the directed graph formed by the given
// links contains a cycle (including self-loops). Used to assert that
// acyclicSankeyLinks always emits a DAG — the invariant d3-sankey needs.
func hasCycleLinks(links []struct{ src, tgt string }) bool {
	adj := map[string][]string{}
	for _, l := range links {
		adj[l.src] = append(adj[l.src], l.tgt)
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var dfs func(n string) bool
	dfs = func(n string) bool {
		color[n] = gray
		for _, m := range adj[n] {
			switch color[m] {
			case gray:
				return true
			case white:
				if dfs(m) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for n := range adj {
		if color[n] == white && dfs(n) {
			return true
		}
	}
	return false
}

func toSrcTgt(links []sdk.SankeyLink) []struct{ src, tgt string } {
	out := make([]struct{ src, tgt string }, len(links))
	for i, l := range links {
		out[i] = struct{ src, tgt string }{l.Source, l.Target}
	}
	return out
}

func TestAcyclicSankeyLinks_NetFlowCollapsesBidirectional(t *testing.T) {
	// Read→Edit happened 5×, Edit→Read 2× → net 3 in the Read→Edit direction.
	counts := map[[2]string]int{
		{"Read", "Edit"}: 5,
		{"Edit", "Read"}: 2,
	}
	links := acyclicSankeyLinks(counts)
	if len(links) != 1 {
		t.Fatalf("len(links) = %d, want 1 (net edge)", len(links))
	}
	if links[0].Source != "Read" || links[0].Target != "Edit" || links[0].Value != 3 {
		t.Errorf("got %+v, want Read→Edit value 3", links[0])
	}
}

func TestAcyclicSankeyLinks_EqualBidirectionalDropped(t *testing.T) {
	// Perfectly balanced ping-pong nets to zero → no edge.
	counts := map[[2]string]int{
		{"Read", "Edit"}: 4,
		{"Edit", "Read"}: 4,
	}
	if links := acyclicSankeyLinks(counts); len(links) != 0 {
		t.Errorf("len(links) = %d, want 0 (net zero)", len(links))
	}
}

func TestAcyclicSankeyLinks_BreaksLongerCycle(t *testing.T) {
	// A→B→C→A is a 3-cycle with no bidirectional pairs; net-flow alone
	// leaves it intact, so the back-edge pass must drop exactly one edge.
	counts := map[[2]string]int{
		{"A", "B"}: 1,
		{"B", "C"}: 1,
		{"C", "A"}: 1,
	}
	links := acyclicSankeyLinks(counts)
	if hasCycleLinks(toSrcTgt(links)) {
		t.Fatalf("result still cyclic: %+v", links)
	}
	if len(links) != 2 {
		t.Errorf("len(links) = %d, want 2 (one back-edge removed)", len(links))
	}
}

func TestAcyclicSankeyLinks_LinearPreserved(t *testing.T) {
	counts := map[[2]string]int{
		{"Read", "Edit"}: 3,
		{"Edit", "Bash"}: 2,
	}
	links := acyclicSankeyLinks(counts)
	if hasCycleLinks(toSrcTgt(links)) {
		t.Fatalf("linear chain reported cyclic: %+v", links)
	}
	if len(links) != 2 {
		t.Errorf("len(links) = %d, want 2", len(links))
	}
}
