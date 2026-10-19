package mcpapps

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
)

// ErrPresetUnconfirmed is returned by ApplyDefaultDenies (and ApplyPresetDenies)
// when the preset's deny list has not been verified against a live tool catalogue.
// Set confirmed:true in the preset JSON after running the live probe and
// cross-referencing every denyGlobal entry against tools/list.
var ErrPresetUnconfirmed = errors.New("mcpapps: preset not confirmed against live tool catalogue")

//go:embed presets/*.json
var presetFiles embed.FS

// PresetSetup starts the MCP server's own setup wizard so it can collect
// account credentials before the dashboard first attaches it.
type PresetSetup struct {
	Command   string   `json:"command"`
	Args      []string `json:"args"` // one element may contain the literal {port}
	Readiness string   `json:"readiness"`
}

// ArgDeny denies one tool only when a top-level scalar parameter matches one of
// Values. Claude Code matches exactly and case-sensitively; `*` is a wildcard.
type ArgDeny struct {
	Tool   string   `json:"tool"`
	Param  string   `json:"param"`
	Values []string `json:"values"`
}

type Preset struct {
	Server          string       `json:"server"`
	Version         string       `json:"version"`
	Match           string       `json:"match"`     // substring of the entry's command line
	Confirmed       bool         `json:"confirmed"` // true once denyGlobal is verified against a live tool catalogue
	DenyGlobal      []string     `json:"denyGlobal"`
	DenyArgs        []ArgDeny    `json:"denyArgs,omitempty"`
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
	if err := p.ValidateDenyArgs(); err != nil {
		return Preset{}, fmt.Errorf("mcpapps: preset %q: %w", name, err)
	}
	return p, nil
}

// argRuleSyntax cannot appear in a tool, param or value: it would end or split
// the `tool(param:value)` rule early.
const argRuleSyntax = "():"

// ValidateDenyArgs rejects a denyArgs entry that would render a malformed rule.
func (p Preset) ValidateDenyArgs() error {
	for i, d := range p.DenyArgs {
		if d.Tool == "" || d.Param == "" || len(d.Values) == 0 {
			return fmt.Errorf("denyArgs[%d]: tool, param and values are all required", i)
		}
		for _, field := range append([]string{d.Tool, d.Param}, d.Values...) {
			if field == "" || strings.ContainsAny(field, argRuleSyntax) {
				return fmt.Errorf("denyArgs[%d]: %q must be non-empty and free of %q", i, field, argRuleSyntax)
			}
		}
	}
	return nil
}

// ArgDenyRules renders DenyArgs as `mcp__<server>__<tool>(<param>:<value>)`
// rules in declaration order, for --disallowedTools. Claude Code ignores such
// rules in settings.json.
func (p Preset) ArgDenyRules(serverName string) []string {
	var rules []string
	for _, d := range p.DenyArgs {
		for _, v := range d.Values {
			rules = append(rules, CapabilityName(serverName, d.Tool)+"("+d.Param+":"+v+")")
		}
	}
	return rules
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

// PresetFinder resolves the preset that applies to a server entry.
type PresetFinder func(ServerEntry) (Preset, bool)

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

// CheckConfirmed returns ErrPresetUnconfirmed when p's deny list has not been
// verified against a live tool catalogue. It is pure, so a caller can refuse
// before persisting anything the denies depend on.
func (p Preset) CheckConfirmed() error {
	if !p.Confirmed {
		return fmt.Errorf("mcpapps: preset %q: %w", p.Server, ErrPresetUnconfirmed)
	}
	return nil
}

// ApplyPresetDenies writes a global deny for every tool p names. It is
// idempotent and returns ErrPresetUnconfirmed when p.Confirmed is false.
// Callers that already have a Preset value (e.g. tests) call this directly;
// ApplyDefaultDenies is the convenience wrapper that resolves the preset first.
func ApplyPresetDenies(ctx context.Context, grants repo.GrantRepo, p Preset, app *ent.MCPApplication, grantedBy string) (DenyResult, error) {
	if err := p.CheckConfirmed(); err != nil {
		return DenyResult{}, fmt.Errorf("mcpapps.ApplyPresetDenies: %w", err)
	}
	res := DenyResult{Preset: p.Server, Created: []string{}, Existing: []string{}}
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
			return DenyResult{}, fmt.Errorf("mcpapps.ApplyPresetDenies: %w", err)
		}
		if created {
			res.Created = append(res.Created, name)
		} else {
			res.Existing = append(res.Existing, name)
		}
	}
	return res, nil
}

// ApplyDefaultDenies writes a global deny for every tool the application's
// preset names. It is idempotent and applies to tools the catalogue does not
// list yet, so the window between adding a server and refreshing its tools is
// closed. An application whose entry matches no preset is a no-op.
// Returns ErrPresetUnconfirmed when the matching preset has not been verified
// against a live tool catalogue — set confirmed:true in the preset JSON after
// running the probe.
func ApplyDefaultDenies(ctx context.Context, grants repo.GrantRepo, app *ent.MCPApplication, grantedBy string) (DenyResult, error) {
	return ApplyDefaultDeniesWith(ctx, grants, FindPreset, app, grantedBy)
}

// ApplyDefaultDeniesWith is ApplyDefaultDenies with the preset lookup supplied
// by the caller, so the lookup a caller preflights with is the one it applies.
func ApplyDefaultDeniesWith(ctx context.Context, grants repo.GrantRepo, find PresetFinder, app *ent.MCPApplication, grantedBy string) (DenyResult, error) {
	empty := DenyResult{Created: []string{}, Existing: []string{}}
	if IsEmptyEntry(app.Entry) {
		return empty, nil
	}
	entry, err := ParseEntry(app.Entry)
	if err != nil {
		return DenyResult{}, fmt.Errorf("mcpapps.ApplyDefaultDenies: %w", err)
	}
	p, ok := find(entry)
	if !ok {
		return empty, nil
	}
	return ApplyPresetDenies(ctx, grants, p, app, grantedBy)
}
