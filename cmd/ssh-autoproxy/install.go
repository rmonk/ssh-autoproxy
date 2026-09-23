package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"ssh-autoproxy/internal/config"
)

const unitTemplate = `[Unit]
Description=ssh-autoproxy background tunnel/proxy daemon
After=network-online.target ssh-agent.socket
Wants=network-online.target

[Service]
Type=simple
%sExecStart=%s daemon
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
`

func newInstallCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install and enable the systemd user service, and print the manual setup steps",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(controlDirFromConfig(cfg)), 0o700); err != nil {
				return fmt.Errorf("creating control dir: %w", err)
			}
			if !printOnly {
				if err := installUnit(cmd.OutOrStdout(), cfg); err != nil {
					return err
				}
			}
			printManualSteps(cmd.OutOrStdout(), cfg, printOnly)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print what would be installed/pasted without making systemd changes")
	return cmd
}

func controlDirFromConfig(cfg *config.Config) string {
	return expandHome(cfg.SSH.ControlPath)
}

func installUnit(out io.Writer, cfg *config.Config) error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return err
	}
	unitPath := filepath.Join(unitDir, "ssh-autoproxy.service")
	authSock := os.Getenv("SSH_AUTH_SOCK")
	content := renderUnit(exePath, authSock, os.Getenv("XDG_RUNTIME_DIR"))
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return err
	}

	if err := runSystemctl(out, "daemon-reload"); err != nil {
		return err
	}
	if err := runSystemctl(out, "enable", "--now", "ssh-autoproxy.service"); err != nil {
		return err
	}
	fmt.Fprintf(out, "Installed and started %s\n", unitPath)
	if authSock == "" {
		fmt.Fprintln(out, "WARNING: SSH_AUTH_SOCK is not set in this shell, so the service has no")
		fmt.Fprintln(out, "SSH agent configured. Start your agent and re-run `ssh-autoproxy install`.")
	}
	fmt.Fprintln(out)
	return nil
}

// renderUnit builds the systemd unit file. The installer's SSH_AUTH_SOCK is
// baked in as an explicit Environment= line: the service otherwise inherits
// the user manager's environment as of when it starts, which at boot is
// usually before the desktop session has exported SSH_AUTH_SOCK — leaving
// every ssh subprocess without an agent for the daemon's whole lifetime.
// A socket under runtimeDir is written relative to %t so the unit isn't
// tied to a specific UID's /run/user path.
func renderUnit(exePath, authSock, runtimeDir string) string {
	env := ""
	if authSock != "" {
		if runtimeDir != "" {
			if rel, ok := strings.CutPrefix(authSock, strings.TrimSuffix(runtimeDir, "/")+"/"); ok {
				authSock = "%t/" + rel
			}
		}
		env = "Environment=SSH_AUTH_SOCK=" + authSock + "\n"
	}
	return fmt.Sprintf(unitTemplate, env, exePath)
}

func runSystemctl(out io.Writer, args ...string) error {
	cmd := exec.Command("systemctl", append([]string{"--user"}, args...)...)
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}

func printManualSteps(out io.Writer, cfg *config.Config, dryRun bool) {
	if dryRun {
		fmt.Fprintln(out, "(--print: no systemd changes made)")
		fmt.Fprintln(out)
	}

	if len(cfg.Routes) == 0 {
		fmt.Fprintln(out, "No routes configured yet — add one or more entries under `routes:` in")
		fmt.Fprintln(out, "your config, then re-run `ssh-autoproxy install --print`.")
		return
	}

	fmt.Fprintln(out, "=== ~/.ssh/config — paste this yourself, ssh-autoproxy never edits it ===")
	fmt.Fprintln(out)

	printedJumpHosts := map[string]bool{}
	for _, r := range cfg.Routes {
		if printedJumpHosts[r.JumpHost] {
			continue
		}
		printedJumpHosts[r.JumpHost] = true
		fmt.Fprintf(out, "Host %s\n", r.JumpHost)
		fmt.Fprintln(out, "    ControlMaster auto")
		fmt.Fprintf(out, "    ControlPath %s\n", cfg.SSH.ControlPath)
		fmt.Fprintf(out, "    ControlPersist %ds\n\n", int(cfg.SSH.ControlPersist.Seconds()))
	}
	for _, r := range cfg.Routes {
		if len(r.HostPatterns) == 0 {
			continue
		}
		fmt.Fprintf(out, "Match exec \"ssh-autoproxy check-ssh %s\" host %s\n", r.Name, strings.Join(r.HostPatterns, " "))
		fmt.Fprintf(out, "    ProxyJump %s\n\n", r.JumpHost)
	}
	fmt.Fprintln(out, "(Separate Host blocks because ProxyJump opens its own connection to the")
	fmt.Fprintln(out, "jump host itself, matched against ITS OWN hostname — the")
	fmt.Fprintln(out, "ControlPath/ControlPersist settings that let it reuse the daemon's")
	fmt.Fprintln(out, "already-open control socket have to live there, not inside the Match")
	fmt.Fprintln(out, "block. Each route gets its own Match block, named by `check-ssh <route>`,")
	fmt.Fprintln(out, "so ssh_config's own `host` criterion picks the right route per destination.)")
	fmt.Fprintln(out)

	anySocks := false
	for _, r := range cfg.Routes {
		if r.SocksProxy != nil {
			anySocks = true
			break
		}
	}
	if !anySocks && !cfg.PAC.Enabled {
		return
	}

	fmt.Fprintln(out, "=== Browser / OS SOCKS5 + PAC proxy ===")
	fmt.Fprintln(out)
	for _, r := range cfg.Routes {
		if r.SocksProxy == nil {
			continue
		}
		fmt.Fprintf(out, "SOCKS5 (%s):  %s:%d\n", r.Name, r.SocksProxy.Bind, r.SocksProxy.Port)
	}
	if cfg.PAC.Enabled {
		pacURL := fmt.Sprintf("http://%s:%d%s", cfg.PAC.Bind, cfg.PAC.Port, cfg.PAC.Path)
		fmt.Fprintf(out, "\nPAC URL: %s\n\n", pacURL)
		fmt.Fprintln(out, "GNOME:   Settings > Network > Network Proxy > Method: Automatic")
		fmt.Fprintf(out, "         Configuration URL: %s\n\n", pacURL)
		fmt.Fprintln(out, `Firefox (if not using "Use system proxy settings"):`)
		fmt.Fprintln(out, "         about:preferences > Network Settings > Settings...")
		fmt.Fprintf(out, "         Automatic proxy configuration URL: %s\n", pacURL)
		fmt.Fprintln(out, `         Click "Reload" after saving.`)
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Note: browsers/OS proxy layers vary in how promptly they re-fetch a")
		fmt.Fprintln(out, "changed PAC file — some only reload on restart or a manual click.")
	}
}
