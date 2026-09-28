package memory_test

import (
	"testing"
	"time"

	"github.com/lx-wnk/kontor/server/internal/memory"
)

func TestScopeSpecificityOutranksEqualRelevance(t *testing.T) {
	now := time.Now()
	global := memory.Candidate{EntryID: "g", Lexical: 0.5, ScopeKind: "global", CreatedAt: now, Confidence: 0.8, Kind: "fact"}
	project := memory.Candidate{EntryID: "p", Lexical: 0.5, ScopeKind: "project", CreatedAt: now, Confidence: 0.8, Kind: "fact"}
	if memory.Score(project, now) <= memory.Score(global, now) {
		t.Error("with equal lexical relevance the project-scoped entry must win")
	}
}

func TestRankIsDeterministic(t *testing.T) {
	now := time.Now()
	// a and b tie exactly on every scoring input, so the outcome depends
	// entirely on the tie-break rule rather than on relevance.
	a := memory.Candidate{EntryID: "a", Lexical: 0.5, ScopeKind: "project", CreatedAt: now, Confidence: 0.7, Kind: "fact"}
	b := memory.Candidate{EntryID: "b", Lexical: 0.5, ScopeKind: "project", CreatedAt: now, Confidence: 0.7, Kind: "fact"}
	c := memory.Candidate{EntryID: "c", Lexical: 0.9, ScopeKind: "application", CreatedAt: now, Confidence: 0.95, Kind: "preference"}

	order1 := memory.Rank([]memory.Candidate{a, b, c}, now)
	order2 := memory.Rank([]memory.Candidate{c, b, a}, now)

	idsOf := func(cs []memory.Candidate) []string {
		ids := make([]string, len(cs))
		for i, cand := range cs {
			ids[i] = cand.EntryID
		}
		return ids
	}
	got1, got2 := idsOf(order1), idsOf(order2)
	if len(got1) != len(got2) {
		t.Fatalf("length mismatch: %v vs %v", got1, got2)
	}
	for i := range got1 {
		if got1[i] != got2[i] {
			t.Fatalf("Rank is not deterministic under reordering: %v vs %v — a scorer resolving ties by map iteration would pass a single-run test and fail this one", got1, got2)
		}
	}
}

func TestPreferenceOutranksFactAtEqualScoreElsewhere(t *testing.T) {
	now := time.Now()
	// A preference changes behaviour; a fact is usually looked up on demand.
	// The push budget should prefer the one that changes what the agent does.
	fact := memory.Candidate{EntryID: "f", Lexical: 0.6, ScopeKind: "project", CreatedAt: now, Confidence: 0.8, Kind: "fact"}
	preference := memory.Candidate{EntryID: "p", Lexical: 0.6, ScopeKind: "project", CreatedAt: now, Confidence: 0.8, Kind: "preference"}
	if memory.Score(preference, now) <= memory.Score(fact, now) {
		t.Error("a preference must outrank an otherwise identical fact")
	}
}

func TestUnresolvableCandidateScoresZero(t *testing.T) {
	now := time.Now()
	// Every other component is maximal — only EntryID is missing. The hard
	// cap must still floor the score: a candidate that cannot be tied back to
	// a real entry must not survive ranking on the strength of its other
	// fields.
	c := memory.Candidate{EntryID: "", Lexical: 1, ScopeKind: "application", CreatedAt: now, Confidence: 1, Kind: "preference"}
	if got := memory.Score(c, now); got != 0 {
		t.Errorf("Score = %v, want 0 for a candidate with no entry id", got)
	}
}

func TestUnknownScopeDoesNotOutrankKnownScopes(t *testing.T) {
	now := time.Now()
	known := memory.Candidate{EntryID: "k", Lexical: 0.5, ScopeKind: "global", CreatedAt: now, Confidence: 0.5, Kind: "fact"}
	unknown := memory.Candidate{EntryID: "u", Lexical: 0.5, ScopeKind: "bogus", CreatedAt: now, Confidence: 0.5, Kind: "fact"}
	if memory.Score(unknown, now) >= memory.Score(known, now) {
		t.Error("an unrecognised scope_kind must fail closed to the bottom, not tie or beat a recognised one")
	}
}
