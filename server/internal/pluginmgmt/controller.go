// Package pluginmgmt is the control plane behind the /api/plugins
// lifecycle + settings endpoints. It wires the discovery state (plugin repo),
// the lifecycle engine, and the settings service, reading each plugin's manifest
// from disk for its descriptor + settings schema.
package pluginmgmt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/lx-wnk/kontor/server/internal/api/plugins"
	"github.com/lx-wnk/kontor/server/internal/db/ent"
	"github.com/lx-wnk/kontor/server/internal/db/repo"
	"github.com/lx-wnk/kontor/server/internal/plugin"
)

// Repo is the subset of repo.PluginRepo the controller reads.
type Repo interface {
	List(ctx context.Context) ([]*ent.Plugin, error)
	Get(ctx context.Context, id string) (*ent.Plugin, error)
}

// Engine is the lifecycle transition surface (satisfied by *plugin.Engine).
type Engine interface {
	Install(ctx context.Context, d plugin.Descriptor) error
	Activate(ctx context.Context, d plugin.Descriptor) error
	Deactivate(ctx context.Context, d plugin.Descriptor) error
	Uninstall(ctx context.Context, d plugin.Descriptor) error
	Update(ctx context.Context, d plugin.Descriptor, manifestHash string) error
}

// Settings is the per-plugin settings surface (satisfied by *plugin.Service).
type Settings interface {
	Get(ctx context.Context, pluginID string, schema []plugin.SettingField) (map[string]string, error)
	Put(ctx context.Context, pluginID string, schema []plugin.SettingField, values map[string]string) error
}

// ManifestLoader resolves a plugin's on-disk descriptor and the hash of its
// manifest bytes. path is the stored plugin directory; when empty, the loader
// falls back to <dir>/<id>.
type ManifestLoader interface {
	Load(id, path string) (desc plugin.Descriptor, hash string, err error)
}

// FileManifestLoader reads plugin.json from disk.
type FileManifestLoader struct{ Dir string }

func (l FileManifestLoader) Load(id, path string) (plugin.Descriptor, string, error) {
	dir := path
	if dir == "" {
		dir = filepath.Join(l.Dir, id)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return plugin.Descriptor{}, "", err
	}
	var d plugin.Descriptor
	if err := json.Unmarshal(raw, &d); err != nil {
		return plugin.Descriptor{}, "", fmt.Errorf("pluginmgmt: parse manifest %s: %w", id, err)
	}
	sum := sha256.Sum256(raw)
	return d, hex.EncodeToString(sum[:]), nil
}

// HealthProbe reports the runtime status of a plugin. A nil probe always
// returns false, false (plugin considered not running / not healthy).
type HealthProbe func(id string) (running bool, healthy bool)

// Controller derives plugin state, dispatches lifecycle transitions, and proxies
// settings reads/writes.
type Controller struct {
	repo     Repo
	engine   Engine
	settings Settings
	loader   ManifestLoader
	probe    HealthProbe

	locksMu        sync.Mutex
	perPluginLocks map[string]*sync.Mutex
}

// New builds a Controller that reads manifests from the filesystem, with dir as
// the fallback plugin directory when a row has no stored path.
func New(r Repo, engine Engine, settings Settings, dir string, probe HealthProbe) *Controller {
	return NewWithLoader(r, engine, settings, FileManifestLoader{Dir: dir}, probe)
}

// NewWithLoader builds a Controller with an explicit manifest loader (tests).
func NewWithLoader(r Repo, engine Engine, settings Settings, loader ManifestLoader, probe HealthProbe) *Controller {
	return &Controller{
		repo:           r,
		engine:         engine,
		settings:       settings,
		loader:         loader,
		probe:          probe,
		perPluginLocks: make(map[string]*sync.Mutex),
	}
}

// pluginLock returns the per-plugin mutex for id, creating it on first use.
func (c *Controller) pluginLock(id string) *sync.Mutex {
	c.locksMu.Lock()
	defer c.locksMu.Unlock()
	if _, ok := c.perPluginLocks[id]; !ok {
		c.perPluginLocks[id] = &sync.Mutex{}
	}
	return c.perPluginLocks[id]
}

// deriveState maps the persisted columns to the public state string. No
// installed_at = discovered; installed + inactive = inactive; active = active.
func deriveState(p *ent.Plugin) string {
	if p.InstalledAt == nil {
		return "discovered"
	}
	if p.Active {
		return "active"
	}
	return "inactive"
}

func nonNilCaps(caps []string) []string {
	if caps == nil {
		return []string{}
	}
	return caps
}

// fillHealthy sets view.Healthy from the probe (running && healthy). A nil probe
// leaves Healthy at its zero value.
func (c *Controller) fillHealthy(view *plugins.PluginView, id string) {
	if c.probe == nil {
		return
	}
	running, healthy := c.probe(id)
	view.Healthy = running && healthy
}

// List returns the lifecycle view for every persisted plugin. State comes from
// the DB row; capabilities/hasSettings/updateAvailable come from the on-disk
// manifest (a manifest read failure degrades to DB-only fields).
func (c *Controller) List(ctx context.Context) ([]plugins.PluginView, error) {
	rows, err := c.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("pluginmgmt.List: %w", err)
	}
	out := make([]plugins.PluginView, 0, len(rows))
	for _, p := range rows {
		view := plugins.PluginView{
			ID:           p.ID,
			Name:         p.Name,
			Version:      p.Version,
			State:        deriveState(p),
			Capabilities: []string{},
		}
		if desc, hash, lerr := c.loader.Load(p.ID, p.Path); lerr == nil {
			view.Capabilities = nonNilCaps(desc.Capabilities)
			view.HasSettings = len(desc.Settings) > 0
			view.UpdateAvailable = hash != "" && hash != p.ManifestHash
		}
		c.fillHealthy(&view, p.ID)
		out = append(out, view)
	}
	return out, nil
}

// descriptorFor resolves a plugin's descriptor + manifest hash, mapping a
// missing plugin to plugin.ErrUnknownPlugin.
func (c *Controller) descriptorFor(ctx context.Context, id string) (plugin.Descriptor, string, error) {
	path := ""
	row, err := c.repo.Get(ctx, id)
	switch {
	case err == nil:
		path = row.Path
	case repo.IsNotFound(err):
		// fall through: loader fallback dir may still resolve it
	default:
		return plugin.Descriptor{}, "", fmt.Errorf("pluginmgmt: get %q: %w", id, err)
	}
	desc, hash, lerr := c.loader.Load(id, path)
	if lerr != nil {
		return plugin.Descriptor{}, "", fmt.Errorf("%w: %q", plugin.ErrUnknownPlugin, id)
	}
	if desc.ID == "" {
		desc.ID = id
	}
	return desc, hash, nil
}

// Transition loads the descriptor, dispatches to the engine, and returns the
// refreshed view. An unsupported action yields plugin.ErrInvalidAction.
// Transitions on the same plugin ID are serialized via a per-plugin lock to
// prevent concurrent Activate/Deactivate from racing on process and DB state.
func (c *Controller) Transition(ctx context.Context, id, action string) (plugins.PluginView, error) {
	mu := c.pluginLock(id)
	mu.Lock()
	defer mu.Unlock()

	desc, hash, err := c.descriptorFor(ctx, id)
	if err != nil {
		return plugins.PluginView{}, err
	}
	switch action {
	case "install":
		err = c.engine.Install(ctx, desc)
	case "activate":
		err = c.engine.Activate(ctx, desc)
	case "deactivate":
		err = c.engine.Deactivate(ctx, desc)
	case "uninstall":
		err = c.engine.Uninstall(ctx, desc)
	case "update":
		err = c.engine.Update(ctx, desc, hash)
	default:
		return plugins.PluginView{}, fmt.Errorf("%w: %q", plugin.ErrInvalidAction, action)
	}
	if err != nil {
		return plugins.PluginView{}, fmt.Errorf("pluginmgmt: %s %q: %w", action, id, err)
	}
	row, err := c.repo.Get(ctx, id)
	if err != nil {
		return plugins.PluginView{}, fmt.Errorf("pluginmgmt: reload %q: %w", id, err)
	}
	view := plugins.PluginView{
		ID:              row.ID,
		Name:            row.Name,
		Version:         row.Version,
		State:           deriveState(row),
		UpdateAvailable: hash != "" && hash != row.ManifestHash,
		Capabilities:    nonNilCaps(desc.Capabilities),
		HasSettings:     len(desc.Settings) > 0,
	}
	c.fillHealthy(&view, row.ID)
	return view, nil
}

// GetSettings returns the manifest schema and the (masked) stored values.
func (c *Controller) GetSettings(ctx context.Context, id string) ([]plugin.SettingField, map[string]string, error) {
	desc, _, err := c.descriptorFor(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	values, err := c.settings.Get(ctx, id, desc.Settings)
	if err != nil {
		return nil, nil, fmt.Errorf("pluginmgmt: get settings %q: %w", id, err)
	}
	return desc.Settings, values, nil
}

// PutSettings persists values against the manifest schema.
func (c *Controller) PutSettings(ctx context.Context, id string, values map[string]string) error {
	desc, _, err := c.descriptorFor(ctx, id)
	if err != nil {
		return err
	}
	if err := c.settings.Put(ctx, id, desc.Settings, values); err != nil {
		return fmt.Errorf("pluginmgmt: put settings %q: %w", id, err)
	}
	return nil
}
