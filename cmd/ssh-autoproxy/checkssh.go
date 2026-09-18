package main

import (
	"os"

	"github.com/spf13/cobra"

	"ssh-autoproxy/internal/netstate"
)

// newCheckSSHCmd is the fast, self-contained helper meant to be invoked
// from an OpenSSH `Match exec "ssh-autoproxy check-ssh <route>" host
// <route's patterns>` block — one such block per route, since each route
// has its own jump host and its own "direct network" profiles. ssh_config's
// own `host` criterion already does the pattern dispatch, so this only
// needs to know which route to evaluate, not the destination hostname.
//
// It never depends on the daemon being alive — it queries nmcli directly —
// so it still works (just colder, without a warm ControlMaster) if the
// daemon has crashed. Any internal error, or an unknown route name, fails
// safe toward "proxy needed" (exit 0): an unnecessary jump-host hop is a
// strictly smaller problem than a "direct" connection that's actually
// unreachable.
func newCheckSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "check-ssh <route>",
		Short:         "Exit 0 if <route> needs its jump host given the current network, non-zero if direct",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				os.Exit(0)
			}

			for _, r := range cfg.Routes {
				if r.Name != args[0] {
					continue
				}
				ns, err := netstate.Query()
				if err != nil {
					os.Exit(0)
				}
				_, proxyNeeded := netstate.Evaluate(r.DirectProfiles, ns)
				if proxyNeeded {
					os.Exit(0)
				}
				os.Exit(1)
			}
			// Unknown route name (e.g. config edited without updating the
			// pasted ssh_config snippet) — fail safe toward proxy needed.
			os.Exit(0)
			return nil
		},
	}
}
