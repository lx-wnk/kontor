package memory

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/rawrepo"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

const (
	// defaultRetrieveLimit and maxRetrieveLimit bound Query.Limit, the same
	// way GET /api/search bounds its own limit parameter.
	defaultRetrieveLimit = 10
	maxRetrieveLimit     = 50

	// candidateOverfetch pulls more FTS hits than the caller's limit before
	// ranking and resolution trim it back down: a hit can be dropped during
	// resolution (superseded or expired since the index was written, or a
	// space outside the query's scope), so fetching exactly `limit` rows
	// could return fewer than that even when enough valid entries exist.
	//
	// Known limitation, not fixed here: searchFTS ranks by bm25 across every
	// space in the index, not just the ones q.Scope can see — visibility is
	// applied afterward, by dropping hits outside spaceScope. A project with
	// few entries can therefore lose its own matches: if enough higher-bm25
	// entries exist in spaces the caller cannot see, they fill the
	// limit*candidateOverfetch window before the visible ones are ever
	// fetched. Raising the factor narrows the window but cannot close it.
	// A real fix needs the space filter pushed into the FTS query itself.
	candidateOverfetch = 4
)

// Query is the input to Retrieve: what to search for, which scope's spaces
// are visible, and how many results to return.
type Query struct {
	Text  string
	Scope repo.Scope
	Limit int
}

// Entry is a resolved memory entry: a ranking winner with its full summary
// and content restored. Summary and content were already sanitized at write
// time (SanitizeForStore); Retrieve does not sanitize again.
type Entry struct {
	ID         string
	SpaceID    string
	Summary    string
	Content    string
	Kind       string
	Confidence float64
	CreatedAt  time.Time
}

// Retriever is the single retrieval path over the memory store: query the FTS
// index, build Candidates, rank them, and resolve the survivors back to full
// Entry values. The push injector and the memory_search MCP tool each hold
// one of these and call Retrieve rather than reimplementing any part of the
// sequence — the whole reason it exists as one type.
type Retriever struct {
	db   *sql.DB
	repo repo.MemoryRepo
}

// NewRetriever returns a Retriever backed by db (the raw connection, needed
// for bm25() ranking — unavailable through the ent client) and repo (for
// resolving hits back to live entries and spaces).
func NewRetriever(db *sql.DB, memRepo repo.MemoryRepo) *Retriever {
	return &Retriever{db: db, repo: memRepo}
}

// Retrieve runs q against the FTS index, scores and ranks the hits, and
// returns the top-ranked entries still valid at call time.
//
// A hit is dropped rather than ranked when: its entry no longer resolves, it
// belongs to a space outside q.Scope's visibility, it has since been
// superseded, or it has since expired. The FTS index can be stale relative to
// the live table by the time a hit is resolved (an update trigger rewrites
// the indexed row on any column change, including supersession, but the
// index still matched on the old summary/content) — a candidate that fails
// to resolve cleanly must not rank high by accident.
func (r *Retriever) Retrieve(ctx context.Context, q Query) ([]Entry, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = defaultRetrieveLimit
	}
	if limit > maxRetrieveLimit {
		limit = maxRetrieveLimit
	}

	ftsQuery := rawrepo.SanitizeFTSQuery(q.Text)
	if ftsQuery == "" {
		return []Entry{}, nil
	}

	spaceScope, err := r.visibleSpaceScopes(ctx, q.Scope)
	if err != nil {
		return nil, fmt.Errorf("memory.Retrieve: %w", err)
	}
	if len(spaceScope) == 0 {
		return []Entry{}, nil
	}

	hits, err := r.searchFTS(ctx, ftsQuery, limit*candidateOverfetch)
	if err != nil {
		return nil, fmt.Errorf("memory.Retrieve: %w", err)
	}

	now := time.Now()
	candidates := make([]Candidate, 0, len(hits))
	resolved := make(map[string]*ent.MemoryEntry, len(hits))
	for _, h := range hits {
		entry, err := r.repo.GetEntry(ctx, h.entryID)
		if err != nil {
			// The index says this entry exists; the live lookup disagrees
			// (deleted, or a stale index). Drop it rather than surface a
			// candidate with nothing to resolve to.
			continue
		}
		scopeKind, visible := spaceScope[entry.SpaceID]
		if !visible {
			continue
		}
		if entry.SupersededBy != nil {
			continue
		}
		if entry.ValidUntil != nil && !entry.ValidUntil.After(now) {
			continue
		}
		resolved[entry.ID] = entry
		candidates = append(candidates, Candidate{
			EntryID:    entry.ID,
			Lexical:    h.lexical,
			ScopeKind:  scopeKind,
			CreatedAt:  entry.CreatedAt,
			Confidence: entry.Confidence,
			Kind:       entry.Kind,
		})
	}

	ranked := Rank(candidates, now)
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	out := make([]Entry, 0, len(ranked))
	for _, c := range ranked {
		e := resolved[c.EntryID]
		out = append(out, Entry{
			ID:         e.ID,
			SpaceID:    e.SpaceID,
			Summary:    e.Summary,
			Content:    e.Content,
			Kind:       e.Kind,
			Confidence: e.Confidence,
			CreatedAt:  e.CreatedAt,
		})
	}
	return out, nil
}

// visibleSpaceScopes returns the scope_kind of every memory space visible to
// scope: every global space, plus every space scoped exactly to scope when it
// is not itself global. This is a union, not resource.ListMerged's
// shadow-by-slug: memory is additive rather than overriding, so a
// project-scoped space does not shadow a same-named global one the way a
// routine or skill resource would.
func (r *Retriever) visibleSpaceScopes(ctx context.Context, scope repo.Scope) (map[string]string, error) {
	global, err := r.repo.ListSpaces(ctx, repo.GlobalScope())
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(global))
	for _, s := range global {
		out[s.ID] = s.ScopeKind
	}

	s := scope.Normalize()
	if s.IsGlobal() {
		return out, nil
	}
	scoped, err := r.repo.ListSpaces(ctx, s)
	if err != nil {
		return nil, err
	}
	for _, sp := range scoped {
		out[sp.ID] = sp.ScopeKind
	}
	return out, nil
}

type ftsHit struct {
	entryID string
	lexical float64
}

// searchFTS runs the sanitized MATCH query directly against memory_fts.
// bm25() ranking is only computable when the FTS5 table itself is queried —
// not through a rowid join back to memory_entries the way the plain
// existence-check shape from the indexing task works — so this reads
// entry_id off memory_fts directly. memory_fts already carries entry_id
// verbatim, so nothing is lost by not joining.
func (r *Retriever) searchFTS(ctx context.Context, ftsQuery string, limit int) ([]ftsHit, error) {
	const q = `
SELECT entry_id, bm25(memory_fts) AS relevance
FROM memory_fts
WHERE memory_fts MATCH ?
ORDER BY relevance
LIMIT ?`

	rows, err := r.db.QueryContext(ctx, q, ftsQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entryIDs []string
	var bm25s []float64
	for rows.Next() {
		var id string
		var bm25 float64
		if err := rows.Scan(&id, &bm25); err != nil {
			continue
		}
		entryIDs = append(entryIDs, id)
		bm25s = append(bm25s, bm25)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	lexical := bm25ToLexical(bm25s)
	hits := make([]ftsHit, len(entryIDs))
	for i, id := range entryIDs {
		hits[i] = ftsHit{entryID: id, lexical: lexical[i]}
	}
	return hits, nil
}

// bm25ToLexical min-max normalises raw bm25 values (SQLite: lower is more
// relevant) into 0..1, where 1 is the best match in this candidate set. Only
// relative order within one retrieval matters — bm25 has no fixed absolute
// scale to calibrate against.
func bm25ToLexical(scores []float64) []float64 {
	out := make([]float64, len(scores))
	if len(scores) == 0 {
		return out
	}
	minV, maxV := scores[0], scores[0]
	for _, s := range scores {
		if s < minV {
			minV = s
		}
		if s > maxV {
			maxV = s
		}
	}
	spread := maxV - minV
	for i, s := range scores {
		if spread <= 0 {
			// No discriminating signal in this batch — treat every hit as
			// equally relevant rather than arbitrarily ranking one last.
			out[i] = 1
			continue
		}
		out[i] = (maxV - s) / spread
	}
	return out
}
