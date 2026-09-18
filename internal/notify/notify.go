// Package notify sends rate-limited desktop notifications via
// notify-send, treating an unreachable D-Bus session (e.g. this daemon
// started headless via systemd-logind lingering, before any graphical
// login) as an expected, silent no-op rather than an error.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

const sendTimeout = 2 * time.Second

// Send fires a desktop notification if a D-Bus session bus looks
// reachable. Any failure (no bus, missing binary, exec error, nonzero
// exit) is logged at debug level and swallowed — never propagated as an
// error, never retried here. The caller decides when to try again (e.g.
// on the next distinct failure category).
func Send(summary, body string) {
	if !shouldAttemptNotify(os.Getenv("DBUS_SESSION_BUS_ADDRESS"), socketExists) {
		slog.Debug("no D-Bus session bus reachable, skipping notification", "summary", summary)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "notify-send", "--app-name=ssh-autoproxy", summary, body)
	if err := cmd.Run(); err != nil {
		slog.Debug("notify-send failed", "error", err)
	}
}

func shouldAttemptNotify(busEnv string, socketExists func(string) bool) bool {
	if busEnv != "" {
		return true
	}
	return socketExists(fmt.Sprintf("/run/user/%d/bus", os.Getuid()))
}

func socketExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
