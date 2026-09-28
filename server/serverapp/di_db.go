package serverapp

import (
	"github.com/lx-wnk/kontor/server/internal/config"
	"github.com/lx-wnk/kontor/server/internal/db"
)

func provideDB(cfg config.Config) (*db.DBBundle, error) {
	return db.Open(cfg.DBPath)
}
