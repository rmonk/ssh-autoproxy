// Package tunnel supervises a persistent `ssh -M -N [-D]` subprocess to a
// route's jump host, restarting it with backoff on failure and reporting
// classified auth/key failures for the daemon to notify on. One
// Supervisor manages exactly one route/jump host; a daemon with multiple
// routes runs one Supervisor per route.
package tunnel

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"ssh-autoproxy/internal/config"
)

const stabilityThreshold = 60 * time.Second

// Supervisor owns a single persistent ssh subprocess to one route's jump
// host, starting/stopping it to match Ensure's desired state and
// restarting it with backoff whenever it exits while still desired.
type Supervisor struct {
	route   config.Route
	ssh     config.SSHConfig
	backoff config.BackoffConfig
	verbose bool

	mu      sync.Mutex
	desired bool
	cancel  context.CancelFunc
	done    chan struct{}

	// Errors receives classified stderr failure categories as they're
	// observed. Buffered and non-blocking: a slow consumer never stalls
	// the subprocess's stderr reader.
	Errors chan ErrorCategory
}

// NewSupervisor creates a supervisor for route. When verbose is set, the
// ssh subprocess is run with -v and each decoded SOCKS request it logs is
// reported via slog at info level (see scanStderr).
func NewSupervisor(route config.Route, ssh config.SSHConfig, backoff config.BackoffConfig, verbose bool) *Supervisor {
	return &Supervisor{
		route:   route,
		ssh:     ssh,
		backoff: backoff,
		verbose: verbose,
		Errors:  make(chan ErrorCategory, 16),
	}
}

// Route returns the route this supervisor manages.
func (s *Supervisor) Route() config.Route {
	return s.route
}

// Ensure starts or stops the tunnel to match desired. It's a no-op if the
// tunnel is already in the requested state.
func (s *Supervisor) Ensure(desired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if desired == s.desired {
		return
	}
	s.desired = desired
	if desired {
		s.startLocked()
	} else {
		s.stopLocked()
	}
}

// Running reports whether the tunnel is currently desired to be up (not a
// guarantee the ssh subprocess is connected right now — it may be
// mid-backoff after a failure).
func (s *Supervisor) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.desired
}

func (s *Supervisor) startLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	done := make(chan struct{})
	s.done = done
	go s.run(ctx, done)
}

func (s *Supervisor) stopLocked() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// Stop tears down the tunnel unconditionally and waits for it to exit.
// Used on daemon shutdown.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	s.desired = false
	s.stopLocked()
	done := s.done
	s.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (s *Supervisor) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0xa5a5a5a5))
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		err := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > stabilityThreshold {
			attempt = 0
		} else {
			attempt++
		}
		if err != nil {
			slog.Warn("ssh tunnel exited", "route", s.route.Name, "error", err)
		}
		delay := nextDelay(attempt, s.backoff.Base.Duration, s.backoff.Max.Duration, rng)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (s *Supervisor) runOnce(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "ssh", s.sshArgs()...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go s.scanStderr(stderr)
	return cmd.Wait()
}

func (s *Supervisor) scanStderr(r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		slog.Debug("ssh tunnel stderr", "route", s.route.Name, "line", line)
		if s.verbose {
			if req, ok := parseSocksRequest(line); ok {
				slog.Info("socks request", "route", s.route.Name, "host", req.Host, "port", req.Port)
			}
		}
		if cat, ok := classifyStderrLine(line); ok {
			select {
			case s.Errors <- cat:
			default:
				slog.Debug("dropped error category, channel full", "route", s.route.Name, "category", cat)
			}
		}
	}
}

func (s *Supervisor) sshArgs() []string {
	args := []string{
		"-M", "-N",
		"-o", "ControlPath=" + s.ssh.ControlPath,
		// Explicitly forced off, not just omitted: with -M, ControlPersist
		// makes ssh detach into an independent background process once
		// connected, which would escape this supervisor's process tree
		// entirely (it'd survive context cancellation on shutdown, and
		// cmd.Wait() would return as soon as it backgrounds rather than
		// tracking the real connection's lifetime — every later reconnect
		// attempt would then collide with the orphaned master's still-live
		// control socket and fail). This Go-level supervisor already
		// provides the "keep it alive, restart on failure" behavior
		// ControlPersist exists for, so the master process must stay in
		// the foreground and fully owned by us. An explicit -o is required
		// here, not just leaving ControlPersist unset: the jump host's own
		// entry in the user's ~/.ssh/config (see the snippet `install`
		// prints, which deliberately keeps ControlPersist there as a
		// self-healing fallback if this daemon isn't running) also matches
		// this connection and would otherwise silently apply its
		// ControlPersist to the daemon's own master too.
		"-o", "ControlPersist=no",
		"-o", "ConnectTimeout=" + strconv.Itoa(int(s.ssh.ConnectTimeout.Seconds())),
		"-o", "ServerAliveInterval=" + strconv.Itoa(int(s.ssh.ServerAliveInterval.Seconds())),
		"-o", "ServerAliveCountMax=" + strconv.Itoa(s.ssh.ServerAliveCountMax),
		"-o", "ExitOnForwardFailure=yes",
	}
	if s.verbose {
		// -vv (debug2), not -v (debug1): ssh only logs the "dynamic
		// request: socks..." line scanStderr parses at debug2 and above —
		// confirmed against OpenSSH_10.2's channels.c behavior, a single
		// -v logs channel open/free but never the decoded SOCKS request.
		args = append(args, "-vv")
	}
	if s.route.SocksProxy != nil {
		args = append(args, "-D", fmt.Sprintf("%s:%d", s.route.SocksProxy.Bind, s.route.SocksProxy.Port))
	}
	args = append(args, s.route.JumpHost)
	return args
}
