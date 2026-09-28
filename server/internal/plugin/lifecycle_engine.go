// Lifecycle engine: plugin state transitions (install/activate/deactivate/
// uninstall/update), persisting state and invoking declared HTTP hooks.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

// ErrIllegalTransition is returned when a lifecycle action cannot be applied in
// the plugin's current state (e.g. install-when-already-installed).
var ErrIllegalTransition = errors.New("pluginlifecycle: illegal transition")

// ErrUnknownPlugin signals that an ID matched no discovered plugin.
// ErrInvalidAction signals an unsupported lifecycle action. Both are raised by
// the runtime and matched by the control plane; the message text is preserved
// verbatim because it is wrapped into the externally-observable HTTP error body.
var (
	ErrUnknownPlugin = errors.New("pluginsctl: unknown plugin")
	ErrInvalidAction = errors.New("pluginsctl: invalid action")
)

// State is a plugin's persisted lifecycle state.
type State struct {
	InstalledAt *time.Time
	Active      bool
	Version     string
}

// StateRepo is the subset of the plugin repo the engine needs.
type StateRepo interface {
	GetState(ctx context.Context, id string) (State, error)
	SetInstalledAt(ctx context.Context, id string, at *time.Time) error
	SetActive(ctx context.Context, id string, active bool) error
	SetVersion(ctx context.Context, id, version string) error
	SetManifestHash(ctx context.Context, id, hash string) error
}

// HookCaller POSTs a lifecycle hook to a plugin. hook is the path (may be empty
// = no-op). The real impl is HTTP; SP2 ensures reachability.
type HookCaller interface {
	Call(ctx context.Context, d Descriptor, hook string) error
}

// SettingsClearer removes a plugin's settings on uninstall.
type SettingsClearer interface {
	Clear(ctx context.Context, pluginID string) error
}

// ProcessManager runs plugin processes for the engine. The registry implements
// it (via an adapter); a nil manager makes every method a no-op so the engine
// is testable in isolation.
type ProcessManager interface {
	Start(ctx context.Context, id string) error
	Stop(ctx context.Context, id string) error
	WithTransient(ctx context.Context, id string, fn func() error) error
}

type Engine struct {
	repo     StateRepo
	hooks    HookCaller
	settings SettingsClearer
	proc     ProcessManager
	// routines retires the routines a module owns when it is taken out of
	// service. Nil means a module's routines outlive it, which is why it is
	// wired wherever schedules exist.
	routines RoutineRetirer
	// dataRoot is where module data directories live. Empty means uninstall
	// leaves nothing to archive, which is the case for an engine built without
	// one.
	dataRoot string
}

// RoutineRetirer takes a module's own routines out of service. It is the
// engine's only view of schedules: a module's routines are disabled, never
// deleted, so a routine that already produced work leaves that work traceable.
type RoutineRetirer interface {
	DisableForModule(ctx context.Context, moduleID string) (int, error)
}

// SetRoutineRetirer wires the retirer used when a module is deactivated or
// uninstalled.
func (e *Engine) SetRoutineRetirer(r RoutineRetirer) { e.routines = r }

// retireRoutines disables the routines the module owns. A failure is reported
// and does not block the transition: a module that cannot be deactivated
// because its routines could not be disabled would be worse than a routine
// that is disabled a moment later by hand.
func (e *Engine) retireRoutines(ctx context.Context, id string) {
	if e.routines == nil {
		return
	}
	n, err := e.routines.DisableForModule(ctx, id)
	if err != nil {
		slog.Warn("module routines not disabled", "id", id, "err", err)
		return
	}
	if n > 0 {
		slog.Info("module routines disabled", "id", id, "count", n)
	}
}

// SetDataRoot tells the engine where module data lives, so uninstalling can
// move a module's directory aside instead of leaving it orphaned.
func (e *Engine) SetDataRoot(root string) { e.dataRoot = root }

func NewLifecycleEngine(repo StateRepo, hooks HookCaller, settings SettingsClearer, proc ProcessManager) *Engine {
	return &Engine{repo: repo, hooks: hooks, settings: settings, proc: proc}
}

func (e *Engine) start(ctx context.Context, id string) error {
	if e.proc == nil {
		return nil
	}
	return e.proc.Start(ctx, id)
}

func (e *Engine) stop(ctx context.Context, id string) error {
	if e.proc == nil {
		return nil
	}
	return e.proc.Stop(ctx, id)
}

func (e *Engine) withTransient(ctx context.Context, id string, fn func() error) error {
	if e.proc == nil {
		return fn()
	}
	return e.proc.WithTransient(ctx, id, fn)
}

// callHook runs a hook only when its path is non-empty.
func (e *Engine) callHook(ctx context.Context, d Descriptor, path string) error {
	if path == "" {
		return nil
	}
	return e.hooks.Call(ctx, d, path)
}

// performInstall runs the install/post-install hooks and stamps InstalledAt.
// It does not guard against already-installed — callers are responsible for
// that check. This is the shared install step used by both Install and the
// auto-install path in Activate.
func (e *Engine) performInstall(ctx context.Context, d Descriptor) error {
	if err := e.withTransient(ctx, d.ID, func() error {
		if err := e.callHook(ctx, d, d.Lifecycle.Install); err != nil {
			return fmt.Errorf("install hook: %w", err)
		}
		return e.callHook(ctx, d, d.Lifecycle.PostInstall)
	}); err != nil {
		return err
	}
	now := time.Now()
	return e.repo.SetInstalledAt(ctx, d.ID, &now)
}

func (e *Engine) Install(ctx context.Context, d Descriptor) error {
	st, err := e.repo.GetState(ctx, d.ID)
	if err != nil {
		return err
	}
	if st.InstalledAt != nil {
		return fmt.Errorf("%w: %s already installed", ErrIllegalTransition, d.ID)
	}
	return e.performInstall(ctx, d)
}

func (e *Engine) Activate(ctx context.Context, d Descriptor) error {
	st, err := e.repo.GetState(ctx, d.ID)
	if err != nil {
		return err
	}
	if st.InstalledAt == nil {
		if err := e.performInstall(ctx, d); err != nil {
			return fmt.Errorf("auto-install on activate: %w", err)
		}
	}
	if err := e.start(ctx, d.ID); err != nil {
		return fmt.Errorf("activate start: %w", err)
	}
	if err := e.callHook(ctx, d, d.Lifecycle.Activate); err != nil {
		_ = e.stop(ctx, d.ID)
		return fmt.Errorf("activate hook: %w", err)
	}
	if err := e.repo.SetActive(ctx, d.ID, true); err != nil {
		_ = e.stop(ctx, d.ID)
		return err
	}
	return nil
}

func (e *Engine) Deactivate(ctx context.Context, d Descriptor) error {
	if err := e.callHook(ctx, d, d.Lifecycle.Deactivate); err != nil {
		return fmt.Errorf("deactivate hook: %w", err)
	}
	if err := e.repo.SetActive(ctx, d.ID, false); err != nil {
		return err
	}
	// A deactivated module must stop causing work: its routines would
	// otherwise keep firing into a module that is no longer running.
	e.retireRoutines(ctx, d.ID)
	return e.stop(ctx, d.ID)
}

func (e *Engine) Update(ctx context.Context, d Descriptor, manifestHash string) error {
	st, err := e.repo.GetState(ctx, d.ID)
	if err != nil {
		if ent.IsNotFound(err) {
			return fmt.Errorf("%w: %s", ErrUnknownPlugin, d.ID)
		}
		return err
	}
	if st.InstalledAt == nil {
		return fmt.Errorf("%w: %s must be installed before update", ErrIllegalTransition, d.ID)
	}
	if err := e.withTransient(ctx, d.ID, func() error {
		return e.callHook(ctx, d, d.Lifecycle.Update)
	}); err != nil {
		return fmt.Errorf("update hook: %w", err)
	}
	if err := e.repo.SetVersion(ctx, d.ID, d.Version); err != nil {
		return err
	}
	return e.repo.SetManifestHash(ctx, d.ID, manifestHash)
}

func (e *Engine) Uninstall(ctx context.Context, d Descriptor) error {
	st, err := e.repo.GetState(ctx, d.ID)
	if err != nil {
		return err
	}
	if st.Active {
		if err := e.Deactivate(ctx, d); err != nil {
			return err
		}
	}
	if err := e.withTransient(ctx, d.ID, func() error {
		return e.callHook(ctx, d, d.Lifecycle.Uninstall)
	}); err != nil {
		return fmt.Errorf("uninstall hook: %w", err)
	}
	if err := e.repo.SetInstalledAt(ctx, d.ID, nil); err != nil {
		return err
	}
	// Archived rather than deleted, and after the state change rather than
	// before: an uninstall that fails halfway must not already have taken the
	// module's data with it. A failure here is reported but does not undo the
	// uninstall, which has otherwise succeeded.
	if e.dataRoot != "" {
		if archive, err := ArchiveModuleDataDir(e.dataRoot, d.ID); err != nil {
			slog.Warn("module data not archived", "id", d.ID, "err", err)
		} else if archive != "" {
			slog.Info("module data archived", "id", d.ID, "archive", archive)
		}
	}
	return e.settings.Clear(ctx, d.ID)
}
