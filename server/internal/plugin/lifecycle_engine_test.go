package plugin

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lx-wnk/kontor/server/internal/db/ent"
)

type fakePluginRepo struct {
	installedAt  *time.Time
	active       bool
	version      string
	manifestHash string
}

func (f *fakePluginRepo) GetState(_ context.Context, _ string) (State, error) {
	return State{InstalledAt: f.installedAt, Active: f.active, Version: f.version}, nil
}
func (f *fakePluginRepo) SetInstalledAt(_ context.Context, _ string, at *time.Time) error {
	f.installedAt = at
	return nil
}
func (f *fakePluginRepo) SetActive(_ context.Context, _ string, a bool) error {
	f.active = a
	return nil
}
func (f *fakePluginRepo) SetVersion(_ context.Context, _ string, v string) error {
	f.version = v
	return nil
}
func (f *fakePluginRepo) SetManifestHash(_ context.Context, _ string, h string) error {
	f.manifestHash = h
	return nil
}

type recordingHooks struct {
	called []string
	failOn string
}

func (r *recordingHooks) Call(_ context.Context, _ Descriptor, hook string) error {
	r.called = append(r.called, hook)
	if hook == r.failOn {
		return assertErr
	}
	return nil
}

var assertErr = &hookErr{}

type hookErr struct{}

func (*hookErr) Error() string { return "hook failed" }

func desc() Descriptor {
	return Descriptor{ID: "p1", Version: "1.0.0",
		Lifecycle: LifecycleHooks{Install: "/i", Activate: "/a", Deactivate: "/d", Uninstall: "/u"}}
}

func TestEngine_InstallActivateDeactivateUninstall(t *testing.T) {
	pr := &fakePluginRepo{}
	hk := &recordingHooks{}
	settings := &fakeClearer{}
	e := NewLifecycleEngine(pr, hk, settings, nil)
	ctx := context.Background()
	d := desc()

	require.NoError(t, e.Install(ctx, d))
	assert.NotNil(t, pr.installedAt)
	assert.Contains(t, hk.called, "/i")

	require.NoError(t, e.Activate(ctx, d))
	assert.True(t, pr.active)
	assert.Contains(t, hk.called, "/a")

	require.NoError(t, e.Deactivate(ctx, d))
	assert.False(t, pr.active)

	require.NoError(t, e.Uninstall(ctx, d))
	assert.Nil(t, pr.installedAt)
	assert.True(t, settings.cleared)
}

func TestEngine_ActivateOnDiscoveredPlugin_AutoInstalls(t *testing.T) {
	pr := &fakePluginRepo{}
	e := NewLifecycleEngine(pr, &recordingHooks{}, &fakeClearer{}, nil)
	err := e.Activate(context.Background(), desc())
	require.NoError(t, err)
	assert.NotNil(t, pr.installedAt)
	assert.True(t, pr.active)
}

func TestEngine_InstallWhenAlreadyInstalledRejected(t *testing.T) {
	now := time.Now()
	e := NewLifecycleEngine(&fakePluginRepo{installedAt: &now}, &recordingHooks{}, &fakeClearer{}, nil)
	err := e.Install(context.Background(), desc())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIllegalTransition)
}

func TestEngine_HookFailureAbortsTransition(t *testing.T) {
	pr := &fakePluginRepo{}
	e := NewLifecycleEngine(pr, &recordingHooks{failOn: "/i"}, &fakeClearer{}, nil)
	require.Error(t, e.Install(context.Background(), desc()))
	assert.Nil(t, pr.installedAt) // state not changed when hook fails
}

type fakeClearer struct{ cleared bool }

func (f *fakeClearer) Clear(_ context.Context, _ string) error { f.cleared = true; return nil }

// --- ordering / process-manager tests ---

// eventRepo embeds fakePluginRepo and records SetActive calls to a shared
// events slice so call order is assertable across proc + hook + repo.
type eventRepo struct {
	fakePluginRepo
	events *[]string
}

func newInstalledRepo(events *[]string) *eventRepo {
	r := &eventRepo{events: events}
	now := time.Now()
	r.installedAt = &now
	return r
}

func (r *eventRepo) SetActive(_ context.Context, _ string, active bool) error {
	*r.events = append(*r.events, fmt.Sprintf("setActive:%v", active))
	r.active = active
	return nil
}

// eventHooks records "hook:"+path to a shared events slice.
type eventHooks struct {
	events *[]string
	fail   bool
}

func (h *eventHooks) Call(_ context.Context, _ Descriptor, path string) error {
	*h.events = append(*h.events, "hook:"+path)
	if h.fail {
		return fmt.Errorf("hook failed")
	}
	return nil
}

// fakeProc records start/stop/transient events to a shared events slice.
type fakeProc struct{ events *[]string }

func (f fakeProc) Start(_ context.Context, id string) error {
	*f.events = append(*f.events, "start:"+id)
	return nil
}
func (f fakeProc) Stop(_ context.Context, id string) error {
	*f.events = append(*f.events, "stop:"+id)
	return nil
}
func (f fakeProc) WithTransient(_ context.Context, id string, fn func() error) error {
	*f.events = append(*f.events, "transient-begin:"+id)
	err := fn()
	*f.events = append(*f.events, "transient-end:"+id)
	return err
}

func TestActivateStartsBeforeHookThenSetsActive(t *testing.T) {
	var events []string
	repo := newInstalledRepo(&events)
	hooks := &eventHooks{events: &events}
	eng := NewLifecycleEngine(repo, hooks, &fakeClearer{}, fakeProc{events: &events})

	d := Descriptor{ID: "voice", Lifecycle: LifecycleHooks{Activate: "/lifecycle/activate"}}
	require.NoError(t, eng.Activate(context.Background(), d))

	require.Equal(t, []string{"start:voice", "hook:/lifecycle/activate", "setActive:true"}, events)
}

func TestActivateHookFailureStopsAndDoesNotActivate(t *testing.T) {
	var events []string
	repo := newInstalledRepo(&events)
	hooks := &eventHooks{events: &events, fail: true}
	eng := NewLifecycleEngine(repo, hooks, &fakeClearer{}, fakeProc{events: &events})

	d := Descriptor{ID: "voice", Lifecycle: LifecycleHooks{Activate: "/lifecycle/activate"}}
	require.Error(t, eng.Activate(context.Background(), d))
	require.Equal(t, []string{"start:voice", "hook:/lifecycle/activate", "stop:voice"}, events)
	require.False(t, repo.active)
}

func TestEngine_UpdateRefreshesManifestHash(t *testing.T) {
	now := time.Now()
	pr := &fakePluginRepo{installedAt: &now}
	e := NewLifecycleEngine(pr, &recordingHooks{}, &fakeClearer{}, nil)
	d := Descriptor{
		ID: "p1", Version: "2.0.0",
		Lifecycle: LifecycleHooks{Update: "/update"},
	}
	require.NoError(t, e.Update(context.Background(), d, "hash-v2"))
	assert.Equal(t, "2.0.0", pr.version)
	assert.Equal(t, "hash-v2", pr.manifestHash)
}

func TestEngine_UpdateBeforeInstallRejected(t *testing.T) {
	pr := &fakePluginRepo{version: "1.0.0", manifestHash: "hash-v1"}
	hk := &recordingHooks{}
	e := NewLifecycleEngine(pr, hk, &fakeClearer{}, nil)
	d := Descriptor{
		ID: "p1", Version: "2.0.0",
		Lifecycle: LifecycleHooks{Update: "/update"},
	}
	err := e.Update(context.Background(), d, "hash-v2")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrIllegalTransition)
	assert.NotContains(t, hk.called, "/update")
	assert.Equal(t, "1.0.0", pr.version)
	assert.Equal(t, "hash-v1", pr.manifestHash)
}

// notFoundRepo embeds fakePluginRepo but reports an ent NotFound from GetState,
// modelling a plugin present on disk but never persisted (no DB row).
type notFoundRepo struct{ fakePluginRepo }

func (notFoundRepo) GetState(_ context.Context, _ string) (State, error) {
	return State{}, &ent.NotFoundError{}
}

func TestEngine_UpdateUndiscoveredReturnsUnknownPlugin(t *testing.T) {
	e := NewLifecycleEngine(&notFoundRepo{}, &recordingHooks{}, &fakeClearer{}, nil)
	d := Descriptor{ID: "p1", Version: "2.0.0", Lifecycle: LifecycleHooks{Update: "/update"}}
	err := e.Update(context.Background(), d, "hash-v2")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrUnknownPlugin)
}

func TestInstallWrapsHooksInTransient(t *testing.T) {
	var events []string
	repo := &eventRepo{events: &events}
	hooks := &eventHooks{events: &events}
	eng := NewLifecycleEngine(repo, hooks, &fakeClearer{}, fakeProc{events: &events})

	d := Descriptor{ID: "voice", Lifecycle: LifecycleHooks{Install: "/lifecycle/install"}}
	require.NoError(t, eng.Install(context.Background(), d))

	require.Equal(t, "transient-begin:voice", events[0])
	require.Equal(t, "transient-end:voice", events[len(events)-1])
}

// TestActivate_DiscoveredPlugin_AutoInstallsAndActivates verifies that calling
// Activate on a plugin with no installed_at (state=discovered) implicitly
// installs it first then activates it, without requiring a separate Install call.
func TestActivate_DiscoveredPlugin_AutoInstallsAndActivates(t *testing.T) {
	pr := &fakePluginRepo{} // installedAt=nil → discovered
	hk := &recordingHooks{}
	e := NewLifecycleEngine(pr, hk, &fakeClearer{}, nil)
	ctx := context.Background()

	d := Descriptor{
		ID:      "voice-whisper",
		Version: "1.0.0",
		Lifecycle: LifecycleHooks{
			Install:  "/install",
			Activate: "/activate",
		},
	}

	require.NoError(t, e.Activate(ctx, d))
	assert.NotNil(t, pr.installedAt, "installedAt must be set by the implicit install")
	assert.True(t, pr.active, "plugin must be active after Activate")
	assert.Contains(t, hk.called, "/install", "install hook must run")
	assert.Contains(t, hk.called, "/activate", "activate hook must run")
}

// TestActivate_InactivePlugin_NoDoubleInstall verifies that calling Activate on
// an already-installed (inactive) plugin does not re-run the install hook.
func TestActivate_InactivePlugin_NoDoubleInstall(t *testing.T) {
	now := time.Now()
	pr := &fakePluginRepo{installedAt: &now, active: false}
	hk := &recordingHooks{}
	e := NewLifecycleEngine(pr, hk, &fakeClearer{}, nil)
	ctx := context.Background()

	d := Descriptor{
		ID:      "voice-whisper",
		Version: "1.0.0",
		Lifecycle: LifecycleHooks{
			Install:  "/install",
			Activate: "/activate",
		},
	}

	require.NoError(t, e.Activate(ctx, d))
	assert.True(t, pr.active)
	assert.NotContains(t, hk.called, "/install", "install hook must NOT run for an already-installed plugin")
	assert.Contains(t, hk.called, "/activate")
}

// TestActivate_DiscoveredPlugin_InstallHookFail_AbortsBeforeActivate verifies
// that a failing install hook during auto-install aborts before the activate
// hook runs and before the plugin is marked active.
func TestActivate_DiscoveredPlugin_InstallHookFail_AbortsBeforeActivate(t *testing.T) {
	pr := &fakePluginRepo{}
	hk := &recordingHooks{failOn: "/install"}
	e := NewLifecycleEngine(pr, hk, &fakeClearer{}, nil)
	ctx := context.Background()

	d := Descriptor{
		ID:      "voice-whisper",
		Version: "1.0.0",
		Lifecycle: LifecycleHooks{
			Install:  "/install",
			Activate: "/activate",
		},
	}

	require.Error(t, e.Activate(ctx, d))
	assert.Nil(t, pr.installedAt, "installedAt must not be set when install hook fails")
	assert.False(t, pr.active, "plugin must not be active when install aborts")
	assert.NotContains(t, hk.called, "/activate", "activate hook must not run when install fails")
}
