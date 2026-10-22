package cloudsessions

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/lx-wnk/kontor/server/internal/apierr"
	"github.com/lx-wnk/kontor/server/internal/cloud"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/pathutil"
)

type accountSpawner struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type accountView struct {
	ConfigDir string            `json:"configDir"`
	Spawners  []accountSpawner  `json:"spawners"`
	Auth      *cloud.AuthStatus `json:"auth"`
	Error     string            `json:"error,omitempty"`
}

// Accounts lists each Claude account once, with whether it can run cloud
// sessions. A failing lookup becomes that row's error, never a failed response.
// GET /api/cloud-accounts
func (h *Handler) Accounts(w http.ResponseWriter, r *http.Request) error {
	spawners, err := h.spawners.List(r.Context())
	if err != nil {
		return err
	}
	slices.SortFunc(spawners, func(a, b *ent.Spawner) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.ID, b.ID))
	})

	refresh := r.URL.Query().Get("refresh") == "1"
	byDir := map[string]*accountView{}
	first := map[string]*ent.Spawner{}
	rows := []*accountView{}
	for _, sp := range spawners {
		if !runsClaude(sp) {
			continue
		}
		key := pathutil.Canonical(ConfigDir(sp))
		row, ok := byDir[key]
		if !ok {
			row = &accountView{ConfigDir: pathutil.ExpandLeadingTilde(ConfigDir(sp))}
			byDir[key] = row
			first[key] = sp
			rows = append(rows, row)
		}
		row.Spawners = append(row.Spawners, accountSpawner{ID: sp.ID, Name: sp.Name})
	}

	// ponytail: sequential, so the response takes up to rows x the runner timeout (60 s each); look rows up in parallel if accounts grow.
	for key, row := range byDir {
		st, err := h.auth.Status(r.Context(), first[key], refresh)
		if err != nil {
			row.Error = err.Error()
			continue
		}
		row.Auth = &st
	}

	slices.SortFunc(rows, func(a, b *accountView) int { return cmp.Compare(a.ConfigDir, b.ConfigDir) })
	apierr.WriteJSON(w, http.StatusOK, rows)
	return nil
}
