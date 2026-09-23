package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"ssh-autoproxy/internal/update"
)

const updateTimeout = 2 * time.Minute

func newUpdateCmd() *cobra.Command {
	var checkOnly, force, noRestart bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update to the latest GitHub release, verifying its SHA256 checksum",
		Long: `Check GitHub for the latest ssh-autoproxy release and, if it's newer than
this binary, download the build for this platform, verify it against the
release's SHA256SUMS, and replace this executable in place. If the systemd
user service is running, it's restarted onto the new binary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			ctx, cancel := context.WithTimeout(cmd.Context(), updateTimeout)
			defer cancel()

			client := update.NewClient()
			rel, err := client.Latest(ctx)
			if err != nil {
				return err
			}

			newer, cmpErr := update.IsNewer(version, rel.Tag)
			switch {
			case cmpErr != nil && checkOnly:
				fmt.Fprintf(out, "Current version: %s (not a release build)\nLatest release:  %s\n", version, rel.Tag)
				return nil
			case cmpErr != nil && !force:
				return fmt.Errorf("%w; use --force to install %s anyway", cmpErr, rel.Tag)
			case cmpErr == nil && !newer && !force:
				fmt.Fprintf(out, "Already up to date (%s).\n", version)
				return nil
			case checkOnly:
				if newer {
					fmt.Fprintf(out, "Update available: %s -> %s\n", version, rel.Tag)
				} else {
					fmt.Fprintf(out, "Already up to date (%s).\n", version)
				}
				return nil
			}

			exe, err := os.Executable()
			if err != nil {
				return err
			}
			// Replace the real file, not a symlink pointing at it.
			if resolved, err := filepath.EvalSymlinks(exe); err == nil {
				exe = resolved
			}

			fmt.Fprintf(out, "Downloading %s for %s/%s...\n", rel.Tag, runtime.GOOS, runtime.GOARCH)
			if err := client.Apply(ctx, rel, runtime.GOOS, runtime.GOARCH, exe); err != nil {
				return err
			}
			fmt.Fprintf(out, "Checksum verified. Updated %s: %s -> %s\n", exe, version, rel.Tag)

			if !noRestart {
				restartServiceIfRunning(out)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether an update is available")
	cmd.Flags().BoolVar(&force, "force", false, "install the latest release even if it isn't newer (or this is a dev build)")
	cmd.Flags().BoolVar(&noRestart, "no-restart", false, "don't restart the systemd user service after updating")
	return cmd
}

// restartServiceIfRunning restarts the systemd user service onto the new
// binary, but only if it's currently running (try-restart). Systems
// without systemctl (e.g. macOS) are skipped silently.
func restartServiceIfRunning(out io.Writer) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return
	}
	if err := exec.Command("systemctl", "--user", "is-active", "--quiet", "ssh-autoproxy.service").Run(); err != nil {
		return
	}
	if err := exec.Command("systemctl", "--user", "try-restart", "ssh-autoproxy.service").Run(); err != nil {
		fmt.Fprintf(out, "Warning: restarting ssh-autoproxy.service failed: %v\n", err)
		fmt.Fprintln(out, "Run `systemctl --user restart ssh-autoproxy` to pick up the new binary.")
		return
	}
	fmt.Fprintln(out, "Restarted ssh-autoproxy.service.")
}
