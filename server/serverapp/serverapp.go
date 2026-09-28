// Package serverapp wires and runs the dashboard HTTP server. It is imported
// both by the cmd/serve CLI (out-of-process) and, in future, by a desktop
// shell module that starts the server in-process as a goroutine — hence it
// lives outside internal/ so it is importable across module boundaries.
package serverapp

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/lx-wnk/kontor/server/internal/agentbroadcast"
	"github.com/lx-wnk/kontor/server/internal/config"
	"github.com/lx-wnk/kontor/server/internal/mcp"
	"github.com/lx-wnk/kontor/server/internal/parser"
	"github.com/lx-wnk/kontor/server/internal/restart"
)

// Run wires the server via initializeServer and blocks until ctx is
// cancelled or a fatal error occurs in any of the run-loop members. It
// returns after graceful shutdown and cleanup.
//
// The caller owns ctx cancellation (the CLI derives it from OS signals; an
// in-process host such as a desktop shell derives it from window-close).
// Restart is handled internally: on a restart trigger, Run cancels its own
// derived context (not the caller's ctx) so cleanup and restart.Execute run
// the same way whether the process is asked to restart or the caller simply
// cancels ctx.
func Run(ctx context.Context, cfg config.Config, cfgFile string, restartCtl *restart.Controller) error {
	return runOn(ctx, cfg, cfgFile, restartCtl, nil)
}

// runOn is Run on an optional pre-bound listener. A nil listener means the HTTP
// server binds cfg.Addr() itself when it starts; a non-nil one was bound by
// Listen before any other startup work and is served as-is, so nothing else can
// take the address in between.
func runOn(ctx context.Context, cfg config.Config, cfgFile string, restartCtl *restart.Controller, ln net.Listener) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if ln != nil {
		// Serve and Shutdown both close it; this only covers the paths that
		// never reach the HTTP server at all.
		defer ln.Close() //nolint:errcheck
	}

	comps, err := initializeServer(runCtx, cfg, cfgFile, restartCtl, ln)
	if err != nil {
		if comps != nil && comps.Cleanup != nil {
			comps.Cleanup()
		}
		return err
	}

	return runComponents(runCtx, cancel, comps, restartCtl)
}

// runComponents wires comps' run-loop members into an errgroup and blocks
// until runCtx is cancelled or a fatal error occurs. Split out of runOn so a
// test can call initializeServer directly and then invoke this exact wiring
// against the live ServerComponents it returned.
func runComponents(runCtx context.Context, cancel context.CancelFunc, comps *ServerComponents, restartCtl *restart.Controller) error {
	cleanup := comps.Cleanup

	g, gCtx := errgroup.WithContext(runCtx)

	// Orchestrator.Start sets the base context synchronously before returning
	// the loop closure — a pre-bound listener can otherwise serve a request
	// via the API.Run goroutine below before an Orchestrator.Run goroutine
	// gets scheduled, and DispatchHTTPSpawn must never see baseContext() fall
	// back to context.Background().
	orchestratorLoop := comps.Orchestrator.Start(gCtx)

	interval := time.Duration(comps.Settings.Int("sse.intervalMs")) * time.Millisecond
	parser.SessionCacheTTL = max(interval, parser.SessionCacheTTL)
	g.Go(func() error {
		agentbroadcast.Run(gCtx, agentbroadcast.RunOptions{
			Merger:              comps.Merger,
			Broadcaster:         comps.Broadcaster,
			Interval:            interval,
			Baseline:            comps.Baseline,
			Enricher:            comps.Enricher,
			CapabilityDecisions: comps.CapabilityDecisions,
		})
		return nil
	})

	g.Go(func() error {
		return comps.API.Run(gCtx)
	})

	g.Go(orchestratorLoop)

	if comps.Scheduler != nil {
		g.Go(func() error {
			return comps.Scheduler.Run(gCtx)
		})
	}

	if comps.HistImporter != nil {
		g.Go(func() error {
			comps.HistImporter.RunScheduled(gCtx, time.Duration(comps.Settings.Int("cost.scanIntervalMs"))*time.Millisecond)
			return nil
		})
	}

	if comps.Eval != nil {
		g.Go(func() error {
			comps.Eval.RunLoop(gCtx, time.Duration(comps.Settings.Int("eval.scanIntervalMs"))*time.Millisecond)
			return nil
		})
	}

	if comps.SetupManager != nil {
		g.Go(func() error {
			// One minute: the limits themselves are minutes and hours, so the
			// loop only has to be finer than they are.
			comps.SetupManager.RunSweeps(gCtx, time.Minute)
			return nil
		})
	}

	if comps.ApiKeyRepo != nil {
		g.Go(func() error {
			// One hour: these rows are already unusable the moment they expire
			// (GetByHash refuses them), so the sweep is housekeeping, not
			// enforcement.
			mcp.SweepExpiredKeys(gCtx, comps.ApiKeyRepo, time.Hour)
			return nil
		})
	}

	restarting := false
	g.Go(func() error {
		select {
		case <-gCtx.Done():
			return nil
		case <-restartCtl.C():
			restarting = true
			cancel() // cancel our own derived context → graceful shutdown of all g.Go members
			return nil
		}
	})

	err := g.Wait()
	cleanup() // stop plugins etc. BEFORE any re-exec (deferred funcs won't run after Exec)
	if restarting {
		slog.Info("restart: relaunching", "mode", restartCtl.Mode())
		restart.Execute(restartCtl.Mode(), restart.OSRestarter{})
	}
	return err
}

// Instance is a dashboard server that already owns its address but is not
// serving yet. Splitting the bind from the run lets a host (the desktop shell)
// take the address before it loads config, plugins and the database, so a
// second launch fails at once instead of racing through that window and then
// adopting the first instance's server.
//
// It exposes no internal types, so callers outside the server module can hold
// one. Exactly one of Serve or Close must be called.
type Instance struct {
	cfg     config.Config
	cfgFile string
	ln      net.Listener
}

// Listen loads configuration from cfgFile (empty for defaults) and binds its
// address, returning a server that is holding it. The address is held until
// Serve returns or Close is called.
func Listen(cfgFile string) (*Instance, error) {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		return nil, fmt.Errorf("bind %s: %w", cfg.Addr(), err)
	}
	return &Instance{cfg: cfg, cfgFile: cfgFile, ln: ln}, nil
}

// Addr is the address the instance holds — the one source of truth for callers
// that need to reach it (health polling, a webview's target URL), including
// when the configured port was resolved from the environment or a config file.
func (i *Instance) Addr() string { return i.ln.Addr().String() }

// Serve runs the server on the held listener until ctx is cancelled. It returns
// after graceful shutdown and cleanup, with the listener closed.
func (i *Instance) Serve(ctx context.Context) error {
	restartCtl := restart.NewController(i.cfg.RestartMode)
	return runOn(ctx, i.cfg, i.cfgFile, restartCtl, i.ln)
}

// Close releases the address without serving, for a host that aborts startup
// between Listen and Serve.
func (i *Instance) Close() error { return i.ln.Close() }

// Serve loads configuration from cfgFile (empty for defaults), binds the
// configured address, and runs the server until ctx is cancelled. It is the
// entrypoint for callers outside the server module (e.g. the desktop shell),
// which cannot construct the internal config/restart types themselves. A host
// that needs the address before serving starts uses Listen instead.
func Serve(ctx context.Context, cfgFile string) error {
	inst, err := Listen(cfgFile)
	if err != nil {
		return err
	}
	return inst.Serve(ctx)
}
