// Command ssh-autoproxy makes SSH ProxyJump (and a browser SOCKS5/PAC
// proxy) conditional on the current network, keeping the jump-host
// connection alive in the background and reconnecting it on failure.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"ssh-autoproxy/internal/config"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "ssh-autoproxy",
		Short:         "Network-aware SSH ProxyJump and browser SOCKS5/PAC proxy",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config.yaml (default: $XDG_CONFIG_HOME/ssh-autoproxy/config.yaml)")

	root.AddCommand(newDaemonCmd())
	root.AddCommand(newCheckSSHCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newUpdateCmd())

	return root
}

func loadConfig(cmd *cobra.Command) (*config.Config, error) {
	path, err := cmd.Flags().GetString("config")
	if err != nil {
		return nil, err
	}
	return config.Load(expandHome(path))
}

func expandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[1:])
}
