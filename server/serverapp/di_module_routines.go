package serverapp

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// moduleRoutine is a routine definition a module ships. It carries what a
// module can know — what the work is and how often — and not what it cannot:
// the working directory is the operator's, so a materialised routine starts
// disabled until they point it somewhere and turn it on.
type moduleRoutine struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	CronExpr    string `yaml:"cronExpr"`
	SlugPrefix  string `yaml:"slugPrefix"`
	Description string `yaml:"description"`
	Timezone    string `yaml:"timezone"`
}

// registerModuleRoutines materialises the routines loaded modules ship.
//
// Creating is once per module and name: a second boot finds the routine already
// there and leaves it alone, including whatever the operator changed about it.
// A module that wants to change a routine it already created does so through
// the schedule tools, where ownership is checked.
func registerModuleRoutines(ctx context.Context, registry *plugin.Registry, schedules repo.TaskScheduleRepo) {
	if registry == nil || schedules == nil {
		return
	}
	for _, entry := range registry.All() {
		dir := entry.Dir()
		if dir == "" || len(entry.Descriptor.Routines) == 0 {
			continue
		}
		existing, err := schedules.ListForModule(ctx, entry.Descriptor.ID)
		if err != nil {
			slog.Warn("module routines not read", "module", entry.Descriptor.ID, "err", err)
			continue
		}
		known := make(map[string]bool, len(existing))
		for _, row := range existing {
			known[row.Name] = true
		}

		for _, pattern := range entry.Descriptor.Routines {
			matches, err := filepath.Glob(filepath.Join(dir, pattern))
			if err != nil {
				slog.Warn("module routine pattern is invalid", "module", entry.Descriptor.ID, "pattern", pattern, "err", err)
				continue
			}
			for _, match := range matches {
				createModuleRoutine(ctx, schedules, entry.Descriptor.ID, match, known)
			}
		}
	}
}

func createModuleRoutine(ctx context.Context, schedules repo.TaskScheduleRepo, moduleID, path string, known map[string]bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("module routine not read", "module", moduleID, "file", path, "err", err)
		return
	}
	var def moduleRoutine
	if err := yaml.Unmarshal(raw, &def); err != nil {
		slog.Warn("module routine is not readable", "module", moduleID, "file", path, "err", err)
		return
	}
	if def.Name == "" || def.CronExpr == "" || def.Title == "" {
		slog.Warn("module routine is incomplete", "module", moduleID, "file", path)
		return
	}
	if known[def.Name] {
		return
	}

	disabled := false
	slugPrefix := def.SlugPrefix
	if slugPrefix == "" {
		slugPrefix = def.Name
	}
	description := def.Description
	if description == "" {
		description = "Brought by the " + moduleID + " module. Set its working directory before enabling it."
	}
	// Disabled, and with the operator's own home as a placeholder directory: a
	// module cannot know where this work belongs, and a routine that fires
	// against a guessed path is worse than one that waits to be pointed
	// somewhere.
	home, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("module routine not created: home is unknown", "module", moduleID, "routine", def.Name, "err", err)
		return
	}
	if _, err := schedules.Create(ctx, repo.CreateTaskScheduleInput{
		OwnerModule:         moduleID,
		Name:                def.Name,
		Enabled:             &disabled,
		CronExpr:            def.CronExpr,
		Timezone:            def.Timezone,
		SlugPrefix:          slugPrefix,
		Title:               def.Title,
		Description:         &description,
		Cwd:                 home,
		MaxIterations:       20,
		StageTimeoutSeconds: 1800,
	}); err != nil {
		slog.Warn("module routine not created", "module", moduleID, "routine", def.Name, "err", err)
		return
	}
	known[def.Name] = true
	slog.Info("module routine created, disabled until its directory is set", "module", moduleID, "routine", def.Name)
}
