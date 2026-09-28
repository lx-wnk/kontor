package mcpapps

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

//go:embed presets/*.json
var presetFiles embed.FS

// PresetSetup starts the MCP server's own setup wizard so it can collect
// account credentials before the dashboard first attaches it.
type PresetSetup struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"` // one element may contain the literal {port}
	Readiness string   `json:"readiness"`
}

type Preset struct {
	Server          string       `json:"server"`
	Version         string       `json:"version"`
	Match           string       `json:"match"` // substring of the entry's command line
	DenyGlobal      []string     `json:"denyGlobal"`
	Setup           *PresetSetup `json:"setup,omitempty"`
	SecretTemplates []string     `json:"secretTemplates,omitempty"`
}

func LoadPreset(name string) (Preset, error) {
	data, err := presetFiles.ReadFile("presets/" + name + ".json")
	if err != nil {
		return Preset{}, fmt.Errorf("mcpapps: unknown preset %q", name)
	}
	var p Preset
	if err := json.Unmarshal(data, &p); err != nil {
		return Preset{}, fmt.Errorf("mcpapps: preset %q: %w", name, err)
	}
	return p, nil
}

// allPresets loads every embedded preset once, sorted by Match length
// descending so FindPreset checks the most specific match first.
var allPresets = sync.OnceValue(loadAllPresets)

func loadAllPresets() []Preset {
	entries, err := fs.ReadDir(presetFiles, "presets")
	if err != nil {
		panic(fmt.Sprintf("mcpapps: read embedded presets: %v", err))
	}
	presets := make([]Preset, 0, len(entries))
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		p, err := LoadPreset(name)
		if err != nil {
			panic(fmt.Sprintf("mcpapps: load preset %q: %v", name, err))
		}
		if p.Match == "" {
			panic(fmt.Sprintf("mcpapps: preset %q has no match — it would never be found", name))
		}
		presets = append(presets, p)
	}
	sort.Slice(presets, func(i, j int) bool { return len(presets[i].Match) > len(presets[j].Match) })
	return presets
}

// FindPreset returns the preset whose Match occurs in the entry's command line
// (Command and Args joined by a space), longest Match first so a specific
// package beats a generic one.
func FindPreset(entry ServerEntry) (p Preset, ok bool) {
	line := entry.Command
	if len(entry.Args) > 0 {
		line += " " + strings.Join(entry.Args, " ")
	}
	for _, p := range allPresets() {
		if strings.Contains(line, p.Match) {
			return p, true
		}
	}
	return Preset{}, false
}

type DenyResult struct {
	Preset   string   `json:"preset"`
	Created  []string `json:"created"`
	Existing []string `json:"existing"`
}

// ApplyDefaultDenies writes a global deny for every tool the application's
// preset names. It is idempotent and applies to tools the catalogue does not
// list yet, so the window between adding a server and refreshing its tools is
// closed. An application whose entry matches no preset is a no-op.
func ApplyDefaultDenies(ctx context.Context, grants repo.GrantRepo, app *ent.MCPApplication, grantedBy string) (DenyResult, error) {
	res := DenyResult{Created: []string{}, Existing: []string{}}
	if IsEmptyEntry(app.Entry) {
		return res, nil
	}
	entry, err := ParseEntry(app.Entry)
	if err != nil {
		return DenyResult{}, fmt.Errorf("mcpapps.ApplyDefaultDenies: %w", err)
	}
	p, ok := FindPreset(entry)
	if !ok {
		return res, nil
	}
	res.Preset = p.Server
	for _, tool := range p.DenyGlobal {
		name := CapabilityName(app.ServerName, tool)
		created, err := EnsureGrant(ctx, grants, repo.CreateGrantInput{
			CapabilityName: name,
			Context:        repo.GrantContextFor(repo.GrantContextGlobal, ""),
			Mode:           repo.GrantModeDeny,
			GrantedBy:      grantedBy,
			Reason:         "preset " + p.Server,
		})
		if err != nil {
			return DenyResult{}, fmt.Errorf("mcpapps.ApplyDefaultDenies: %w", err)
		}
		if created {
			res.Created = append(res.Created, name)
		} else {
			res.Existing = append(res.Existing, name)
		}
	}
	return res, nil
}
