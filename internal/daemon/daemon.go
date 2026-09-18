// Package daemon wires netstate, tunnel, pac, notify and state together
// into the long-running background process. One tunnel.Supervisor runs
// per configured route. Run also watches the config file itself and
// transparently restarts the inner daemon loop whenever it changes.
package daemon

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"ssh-autoproxy/internal/config"
	"ssh-autoproxy/internal/netstate"
	"ssh-autoproxy/internal/notify"
	"ssh-autoproxy/internal/pac"
	"ssh-autoproxy/internal/state"
	"ssh-autoproxy/internal/tunnel"
)

const (
	debounceInterval   = 500 * time.Millisecond
	configPollInterval = 2 * time.Second
	pacShutdownTimeout = 3 * time.Second
)

type routeError struct {
	route    string
	category tunnel.ErrorCategory
}

// Run loads the config at configPath and runs the daemon, automatically
// reloading whenever the config file changes on disk. It blocks until ctx
// is canceled. A config edit that fails to parse/validate is logged and
// ignored — the daemon keeps running on its last-known-good config rather
// than crashing on a mid-edit typo. When verbose is set, each route's ssh
// subprocess is run with -v and logs the individual SOCKS requests it
// handles.
func Run(ctx context.Context, configPath string, verbose bool) error {
	resolvedPath, err := config.ResolvePath(configPath)
	if err != nil {
		return fmt.Errorf("resolving config path: %w", err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	reload := make(chan struct{}, 1)
	go watchConfigFile(ctx, resolvedPath, reload)

	for {
		runCtx, cancel := context.WithCancel(ctx)
		errCh := make(chan error, 1)
		go func(cfg *config.Config) { errCh <- runOnce(runCtx, cfg, verbose) }(cfg)

		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				cancel()
				<-errCh
				return nil

			case err := <-errCh:
				// runOnce only returns on its own for a fatal startup
				// error (e.g. a listener failed to bind) — otherwise it
				// blocks until runCtx is canceled.
				cancel()
				return err

			case <-reload:
				newCfg, err := config.Load(configPath)
				if err != nil {
					slog.Warn("config reload failed, keeping previous config running", "path", resolvedPath, "error", err)
					continue
				}
				slog.Info("config file changed, reloading", "path", resolvedPath)
				cancel()
				<-errCh
				cfg = newCfg
				restart = true
			}
		}
	}
}

// watchConfigFile polls path's mtime and signals reload (non-blocking,
// coalescing rapid edits into one pending signal) whenever it changes.
// Runs for the whole lifetime of ctx, independent of individual reload
// cycles, so it keeps watching even while a bad edit is being ignored.
func watchConfigFile(ctx context.Context, path string, reload chan<- struct{}) {
	var lastMod time.Time
	if info, err := os.Stat(path); err == nil {
		lastMod = info.ModTime()
	}
	ticker := time.NewTicker(configPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			info, err := os.Stat(path)
			if err != nil {
				continue // e.g. momentarily missing during an atomic save
			}
			if info.ModTime().After(lastMod) {
				lastMod = info.ModTime()
				select {
				case reload <- struct{}{}:
				default:
				}
			}
		}
	}
}

// runOnce runs the daemon for a single, fixed config: one tunnel.Supervisor
// per route, the PAC server, and network-state watching. It blocks until
// ctx is canceled, at which point it tears down every supervisor and the
// PAC listener before returning.
func runOnce(ctx context.Context, cfg *config.Config, verbose bool) error {
	if err := ensureControlDir(cfg.SSH.ControlPath); err != nil {
		return fmt.Errorf("preparing control dir: %w", err)
	}

	errCh := make(chan routeError, 16)
	supervisors := make([]*tunnel.Supervisor, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		sup := tunnel.NewSupervisor(route, cfg.SSH, cfg.Backoff, verbose)
		supervisors = append(supervisors, sup)
		go forwardErrors(ctx, sup, errCh)
	}

	pacServer := pac.NewServer(cfg.PAC.Path)

	st, err := state.Load("")
	if err != nil {
		return fmt.Errorf("loading state: %w", err)
	}

	if cfg.PAC.Enabled {
		addr := net.JoinHostPort(cfg.PAC.Bind, strconv.Itoa(cfg.PAC.Port))
		if err := pacServer.Start(addr); err != nil {
			return fmt.Errorf("starting PAC server: %w", err)
		}
		slog.Info("PAC server listening", "addr", addr, "path", cfg.PAC.Path)
	}

	recheck := make(chan struct{}, 1)
	triggerRecheck := func() {
		select {
		case recheck <- struct{}{}:
		default:
		}
	}
	go watchNetwork(ctx, cfg.Daemon.PollInterval.Duration, triggerRecheck)

	lastProxyNeeded := map[string]bool{}
	applyState := func() {
		ns, err := netstate.Query()
		if err != nil {
			slog.Warn("network state query failed", "error", err)
			return
		}

		changed := false
		decisions := make(map[string]bool, len(supervisors))
		for _, sup := range supervisors {
			route := sup.Route()
			_, proxyNeeded := netstate.Evaluate(route.DirectProfiles, ns)
			decisions[route.Name] = proxyNeeded

			if prev, ok := lastProxyNeeded[route.Name]; !ok || prev != proxyNeeded {
				changed = true
				lastProxyNeeded[route.Name] = proxyNeeded
				slog.Info("network state changed", "route", route.Name, "proxy_needed", proxyNeeded, "ssid", ns.SSID, "gateway", ns.Gateway)
			}
			sup.Ensure(proxyNeeded || route.KeepTunnelWhenDirect)
		}

		if changed && cfg.PAC.Enabled {
			pacServer.Update(pac.Generate(cfg.Routes, decisions))
		}
	}
	applyState()

	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	pending := false

	for {
		select {
		case <-ctx.Done():
			for _, sup := range supervisors {
				sup.Stop()
			}
			if cfg.PAC.Enabled {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), pacShutdownTimeout)
				if err := pacServer.Shutdown(shutdownCtx); err != nil {
					slog.Warn("PAC server shutdown failed", "error", err)
				}
				cancel()
			}
			if err := state.Save("", st); err != nil {
				slog.Warn("saving state on shutdown failed", "error", err)
			}
			return nil

		case <-recheck:
			pending = true
			debounce.Reset(debounceInterval)

		case <-debounce.C:
			if pending {
				pending = false
				applyState()
			}

		case re := <-errCh:
			handleErrorCategory(cfg, st, re.route, re.category)
		}
	}
}

// forwardErrors fans a supervisor's per-route Errors channel into the
// daemon's single shared channel, tagging each one with the route name.
// This lets the main loop's select stay a fixed, static set of cases
// regardless of how many routes are configured.
func forwardErrors(ctx context.Context, sup *tunnel.Supervisor, out chan<- routeError) {
	routeName := sup.Route().Name
	for {
		select {
		case <-ctx.Done():
			return
		case cat, ok := <-sup.Errors:
			if !ok {
				return
			}
			select {
			case out <- routeError{route: routeName, category: cat}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func ensureControlDir(controlPath string) error {
	expanded := controlPath
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(controlPath, "~/") {
		expanded = filepath.Join(home, controlPath[2:])
	}
	return os.MkdirAll(filepath.Dir(expanded), 0o700)
}

func handleErrorCategory(cfg *config.Config, st *state.State, routeName string, cat tunnel.ErrorCategory) {
	if !cfg.Notify.Enabled {
		return
	}
	now := time.Now()
	category := routeName + ":" + string(cat)
	if !state.ShouldNotify(st, category, now, cfg.Notify.Cooldown.Duration) {
		return
	}
	state.RecordNotified(st, category, now)
	if err := state.Save("", st); err != nil {
		slog.Warn("saving notification state failed", "error", err)
	}
	notify.Send(fmt.Sprintf("ssh-autoproxy: %s", routeName), describeCategory(cat))
}

func describeCategory(cat tunnel.ErrorCategory) string {
	switch cat {
	case tunnel.CategoryAuthPublicKey:
		return "Permission denied (publickey) connecting to the jump host. Check ssh-add / your key."
	case tunnel.CategoryAuthAgent:
		return "Could not reach your SSH agent. Is it running?"
	case tunnel.CategoryHostKeyVerification:
		return "Host key verification failed for the jump host."
	case tunnel.CategoryHostKeyChanged:
		return "WARNING: the jump host's identification has changed."
	default:
		return "SSH tunnel problem: " + string(cat)
	}
}

// watchNetwork triggers recheck on nmcli monitor events, and periodically
// as a fallback (also covering the case where the nmcli monitor
// subprocess itself dies).
func watchNetwork(ctx context.Context, pollInterval time.Duration, recheck func()) {
	if pollInterval <= 0 {
		pollInterval = 30 * time.Second
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	go runNMCLIMonitor(ctx, recheck)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recheck()
		}
	}
}

func runNMCLIMonitor(ctx context.Context, recheck func()) {
	for {
		if ctx.Err() != nil {
			return
		}
		cmd := exec.CommandContext(ctx, "nmcli", "monitor")
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			slog.Debug("nmcli monitor unavailable", "error", err)
			return
		}
		if err := cmd.Start(); err != nil {
			slog.Debug("nmcli monitor unavailable", "error", err)
			return
		}
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			recheck()
		}
		_ = cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}
