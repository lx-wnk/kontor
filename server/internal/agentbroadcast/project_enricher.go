package agentbroadcast

import (
	"context"
	"log/slog"

	sdk "github.com/lx-wnk/kontor/sdk"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/services"
)

// NewProjectFolderEnricher returns a merger.Enricher that sets ProjectID and
// ProjectName on every agent whose cwd lies in a registered Kontor project
// folder. An agent outside every folder keeps the cwd basename and an empty
// ProjectID.
//
// The folder list is read once per tick rather than cached, so a project
// folder added while the server runs is matched on the next tick. A nil repo
// or a query error leaves every agent untouched.
func NewProjectFolderEnricher(folders repo.ProjectFolderRepo) merger.Enricher {
	return func(ctx context.Context, agents []sdk.Agent) {
		if folders == nil || len(agents) == 0 {
			return
		}
		rows, err := folders.ListAll(ctx)
		if err != nil {
			slog.Debug("project folder enricher: folder lookup failed", "err", err)
			return
		}
		idx := services.NewProjectFolderIndex(rows)
		for i := range agents {
			if id, name, ok := idx.Match(agents[i].CWD); ok {
				agents[i].ProjectID = id
				agents[i].ProjectName = name
			}
		}
	}
}
