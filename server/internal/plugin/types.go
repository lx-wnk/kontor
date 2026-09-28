// Package plugin provides runtime plugin discovery and lifecycle management.
package plugin

import (
	"context"
	"fmt"

	"github.com/lx-wnk/kontor/server/internal/validation"
)

// CurrentContract is the module contract this core implements. A module
// declares the contract it was built against and is refused when the two do not
// match: an integer handshake cannot be half-satisfied, and a module that
// cannot state what it expects fails at runtime rather than at load.
const CurrentContract = 1

// Descriptor is read from plugin.json in each plugin directory.
type Descriptor struct {
	// Contract is the module contract version this module was built against.
	// Required.
	Contract     int      `json:"contract"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Capabilities []string `json:"capabilities"`
	// Addr is the HTTP address the plugin listens on (e.g. "127.0.0.1:13200").
	Addr string `json:"addr"`
	// Command is the executable + args to start the plugin process.
	// If empty, the plugin is expected to already be running.
	Command []string `json:"command"`
	// Env lists env var names the plugin reads from the parent environment.
	Env []string `json:"env"`
	// Uses lists the capabilities this module may exercise when it calls back
	// into the server. They become the scopes of the credential it is issued;
	// anything outside the list is refused by the same check that governs every
	// other caller.
	Uses []string `json:"uses"`
	// Tools are the tools this module offers to agents. They reach an agent
	// namespaced by the module id, so one module's `search` cannot be mistaken
	// for another's.
	Tools []ToolDecl `json:"tools"`
	// Providers are globs, relative to the module's directory, naming provider
	// descriptors it ships.
	Providers []string `json:"providers"`
	// Routines are globs naming routine definitions the module brings. They are
	// created once, owned by the module, and taken out of service when it is
	// removed — never deleted, because what they produced is the operator's.
	Routines []string `json:"routines"`
	// TaskKinds are kinds of work this module defines, each with its own
	// sequence of stages. The core's sequence is untouched.
	TaskKinds []TaskKindDecl `json:"taskKinds"`
	// StageKinds are the kinds of pipeline stage this module can run. Core
	// lifecycle states stay core's; a module adds a kind that plugs into them.
	StageKinds []StageKindDecl `json:"stageKinds"`
	Settings   []SettingField  `json:"settings"`
	Lifecycle  LifecycleHooks  `json:"lifecycle"`
}

// ToolDecl declares one tool a module offers.
type ToolDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// TaskKindDecl declares one kind of work and the stages it runs through.
type TaskKindDecl struct {
	Name   string   `json:"name"`
	Stages []string `json:"stages"`
}

// StageKindDecl declares one kind of stage a module can run.
type StageKindDecl struct {
	Name           string `json:"name"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

// SettingField declares one configurable setting. Secret fields are encrypted at
// rest and masked in the API.
type SettingField struct {
	Key    string   `json:"key"`
	Type   string   `json:"type"` // string|url|int|bool|enum
	Label  string   `json:"label"`
	Secret bool     `json:"secret"`
	Enum   []string `json:"enum,omitempty"`
}

// LifecycleHooks are optional HTTP paths (on the plugin's Addr) invoked on state
// transitions. An empty path means the transition runs without a hook.
type LifecycleHooks struct {
	Install     string `json:"install"`
	PostInstall string `json:"postInstall"`
	Activate    string `json:"activate"`
	Deactivate  string `json:"deactivate"`
	Update      string `json:"update"`
	Uninstall   string `json:"uninstall"`
}

// HasCapability reports whether the plugin declares the given capability.
func (d Descriptor) HasCapability(capability string) bool {
	for _, c := range d.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Capability constants used in plugin.json.
const (
	CapAuthProvider   = "auth_provider"
	CapRouteExtension = "route_extension"
	// CapUIExtension marks a plugin that contributes frontend UI into named slots
	// via a ui-manifest.json + per-slot JS modules served by the plugin proxy.
	CapUIExtension = "ui_extension"
)

// SettingsProvider fetches decrypted settings for a plugin by ID.
// Called at every subprocess spawn; errors are logged and the plugin starts
// without settings so a DB failure never blocks plugin availability.
type SettingsProvider func(ctx context.Context, id string) (map[string]string, error)

// Validate refuses a manifest that this core cannot honour, naming the field
// that is wrong. Refusing is the point: skipping silently is how a module ends
// up half-loaded, and reporting at runtime is how the failure reaches a user
// instead of the operator installing it.
func (d Descriptor) Validate() error {
	if d.Contract == 0 {
		return fmt.Errorf("plugin %q: contract is required (this core implements contract %d)", d.ID, CurrentContract)
	}
	if d.Contract != CurrentContract {
		return fmt.Errorf("plugin %q: contract %d is not supported (this core implements contract %d)", d.ID, d.Contract, CurrentContract)
	}
	if !validation.IsValidSlug(d.ID) {
		return fmt.Errorf("plugin id %q: %s", d.ID, validation.SlugPatternMessage)
	}
	if d.Name == "" {
		return fmt.Errorf("plugin %q: name is required", d.ID)
	}
	return nil
}
