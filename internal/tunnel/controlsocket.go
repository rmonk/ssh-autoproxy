package tunnel

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const controlPathTimeout = 5 * time.Second

// removeStaleControlSocket deletes the jump host's ControlMaster socket if
// it's left over from an ssh master that died without cleaning up (e.g.
// killed with SIGKILL). OpenSSH won't replace an existing socket file:
// a new master just logs "ControlSocket ... already exists, disabling
// multiplexing" and runs with no control socket at all, so every later
// ProxyJump through this jump host opens its own fresh connection
// instead of reusing the daemon's master. A socket that something is
// still listening on (e.g. the user's own ControlPersist master) is left
// alone.
func (s *Supervisor) removeStaleControlSocket(ctx context.Context) {
	path, err := resolveControlPath(ctx, s.ssh.ControlPath, s.route.JumpHost)
	if err != nil {
		slog.Debug("resolving control path failed", "route", s.route.Name, "error", err)
		return
	}
	removed, err := removeIfStale(path)
	if err != nil {
		slog.Warn("removing stale control socket failed", "route", s.route.Name, "path", path, "error", err)
		return
	}
	if removed {
		slog.Info("removed stale control socket", "route", s.route.Name, "path", path)
	}
}

// resolveControlPath asks ssh for the ControlPath it would use for host,
// with every %-token and ~ expanded under the user's ssh_config (which may
// change the user, hostname or port the tokens refer to).
func resolveControlPath(ctx context.Context, controlPath, host string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, controlPathTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "ssh", "-G", "-o", "ControlPath="+controlPath, host).Output()
	if err != nil {
		return "", fmt.Errorf("ssh -G %s: %w", host, err)
	}
	return parseControlPath(out)
}

// parseControlPath extracts the controlpath value from `ssh -G` output.
func parseControlPath(out []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), " ")
		if ok && strings.EqualFold(key, "controlpath") {
			if value == "" || strings.EqualFold(value, "none") {
				return "", errors.New("no control path configured")
			}
			return value, nil
		}
	}
	return "", errors.New("ssh -G output has no controlpath")
}

// removeIfStale removes the socket at path if it exists and nothing is
// listening on it. It reports whether it removed anything.
func removeIfStale(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode().Type() != os.ModeSocket {
		return false, nil // not ours to delete
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		conn.Close()
		return false, nil // a live master owns it
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return false, nil // can't tell; leave it
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}
