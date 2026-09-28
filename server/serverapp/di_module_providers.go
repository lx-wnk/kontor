package serverapp

import (
	"log/slog"
	"path/filepath"

	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/provider"
)

// registerModuleProviders loads the provider descriptors a module ships. They
// are resolved relative to the module's own directory, so a module carries its
// providers with it instead of asking the operator to copy files into a shared
// directory the module knows nothing about.
//
// A descriptor that cannot be read is logged and skipped, matching how the
// registry already treats a bad descriptor in the user directory: one broken
// file must not cost every other provider.
func registerModuleProviders(registry *plugin.Registry, providers *provider.Registry) {
	if registry == nil || providers == nil {
		return
	}
	for _, entry := range registry.All() {
		dir := entry.Dir()
		if dir == "" {
			continue
		}
		for _, pattern := range entry.Descriptor.Providers {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				slog.Warn("module provider pattern is invalid", "module", entry.Descriptor.ID, "pattern", pattern, "err", err)
				continue
			}
			for _, match := range matches {
				if err := providers.LoadFile(match); err != nil {
					slog.Warn("module provider not loaded", "module", entry.Descriptor.ID, "file", match, "err", err)
				}
			}
		}
	}
}
