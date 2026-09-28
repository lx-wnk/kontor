package api

import (
	"context"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	apiplugins "github.com/lx-wnk/kontor/server/internal/api/plugins"
	"github.com/lx-wnk/kontor/server/internal/merger"
	"github.com/lx-wnk/kontor/server/internal/plugin"
	"github.com/lx-wnk/kontor/server/internal/serverask"
	"github.com/lx-wnk/kontor/server/internal/sse"
)

// stubPluginController satisfies the unexported controller interface consumed by
// apiplugins.NewLifecycle. It exists only so the plugin lifecycle routes mount in
// the test router — route registration is static and never drives these methods.
type stubPluginController struct{}

func (stubPluginController) List(context.Context) ([]apiplugins.PluginView, error) { return nil, nil }
func (stubPluginController) Transition(context.Context, string, string) (apiplugins.PluginView, error) {
	return apiplugins.PluginView{}, nil
}
func (stubPluginController) GetSettings(context.Context, string) ([]plugin.SettingField, map[string]string, error) {
	return nil, nil, nil
}
func (stubPluginController) PutSettings(context.Context, string, map[string]string) error { return nil }

var updateGolden = flag.Bool("update-golden", false, "rewrite the route golden fixture")

// routesOf walks router and returns every registered route as a sorted
// "METHOD PATH" line.
func routesOf(t *testing.T, router http.Handler) []string {
	t.Helper()
	routes, ok := router.(chi.Routes)
	if !ok {
		t.Fatalf("NewRouter did not return a chi.Routes; got %T", router)
	}

	var lines []string
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		lines = append(lines, method+" "+route)
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	sort.Strings(lines)
	return lines
}

// collectRoutes walks the fully-wired production router and returns every
// registered route as a sorted "METHOD PATH" line. It is the behavior-preserving
// tripwire for structural refactors (plugin domain consolidation): the route
// table must be byte-identical before and after a package move.
func collectRoutes(t *testing.T) []string {
	t.Helper()
	return routesOf(t, buildBypassRouter(t))
}

// stubCapabilityAsker satisfies the RouterDeps.CapabilityAsker interface
// without pulling in serverask.Asker; route registration only checks it for
// nilness, it is never invoked while building the router.
type stubCapabilityAsker struct{}

func (stubCapabilityAsker) SetOnChange(func()) {}
func (stubCapabilityAsker) Resolve(id, decision string) (serverask.Pending, error) {
	return serverask.Pending{}, nil
}

// buildMinimalRouter builds a router with the smallest RouterDeps able to
// exercise the routes gated on Config.BypassAuth / CapabilityAsker. Every
// other handler is left nil, which is safe because each is mounted behind its
// own nil-check in NewRouter and has no bearing on the auth-only gates below.
func buildMinimalRouter(t *testing.T, bypassAuth bool, asker interface {
	SetOnChange(func())
	Resolve(id, decision string) (serverask.Pending, error)
}) http.Handler {
	t.Helper()
	return NewRouter(RouterDeps{
		Ctx: context.Background(),
		Config: RouterConfig{
			BypassAuth:  bypassAuth,
			IsLoopback:  true,
			HooksSecret: "test-hooks-secret-please-ignore-32b",
		},
		AgentBroadcaster: sse.NewBroadcaster(),
		Merger:           merger.New(),
		CapabilityAsker:  asker,
	})
}

// diffRoutes returns the lines present in a but not in b. Both must already
// be sorted (as routesOf returns them).
func diffRoutes(a, b []string) []string {
	inB := make(map[string]bool, len(b))
	for _, line := range b {
		inB[line] = true
	}
	var out []string
	for _, line := range a {
		if !inB[line] {
			out = append(out, line)
		}
	}
	return out
}

// TestRouteGolden asserts the production route set matches the checked-in golden
// fixture. Run with -update-golden to regenerate after an intentional route change.
func TestRouteGolden(t *testing.T) {
	got := strings.Join(collectRoutes(t), "\n") + "\n"
	goldenPath := filepath.Join("testdata", "routes.golden")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("route set drifted from golden.\nRegenerate intentionally with:\n  go test ./internal/api -run TestRouteGolden -update-golden\n\n--- got ---\n%s\n--- want ---\n%s", got, string(want))
	}
}

// TestRouteGolden_AuthOnlyRoutes pins the routes that Router.go mounts only
// when auth is on (deps.Config.BypassAuth == false / deps.CapabilityAsker !=
// nil) — TestRouteGolden's bypass-auth router never sees them, so without
// this test they could be added or removed unnoticed. It builds two minimal
// routers differing only in those two knobs and pins their difference against
// a golden fixture; it also asserts nothing goes the other way (a route
// disappearing when auth turns on), which the current router.go guards never
// produce. Run with -update-golden to regenerate after an intentional change.
func TestRouteGolden_AuthOnlyRoutes(t *testing.T) {
	bypassRoutes := routesOf(t, buildMinimalRouter(t, true, nil))
	authOnRoutes := routesOf(t, buildMinimalRouter(t, false, stubCapabilityAsker{}))

	if vanished := diffRoutes(bypassRoutes, authOnRoutes); len(vanished) > 0 {
		t.Fatalf("route(s) present with bypass auth but gone with auth on — a guard now mounts a route ONLY in bypass mode, which this test does not expect:\n%s", strings.Join(vanished, "\n"))
	}

	got := strings.Join(diffRoutes(authOnRoutes, bypassRoutes), "\n") + "\n"
	goldenPath := filepath.Join("testdata", "routes-auth-only.golden")

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		t.Logf("wrote %s", goldenPath)
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update-golden to create): %v", err)
	}
	if got != string(want) {
		t.Errorf("auth-only route set drifted from golden.\nRegenerate intentionally with:\n  go test ./internal/api -run TestRouteGolden_AuthOnlyRoutes -update-golden\n\n--- got ---\n%s\n--- want ---\n%s", got, string(want))
	}
}
