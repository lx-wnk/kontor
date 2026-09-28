package ranking

import "github.com/lx-wnk/kontor/server/internal/db/ent"

// EffectiveRank returns the task's stored rank, falling back to its creation
// time (as microseconds) so unranked legacy rows still order deterministically.
func EffectiveRank(t *ent.Task) float64 {
	if t.Rank != nil {
		return *t.Rank
	}
	return float64(t.CreatedAt.UnixMicro())
}
