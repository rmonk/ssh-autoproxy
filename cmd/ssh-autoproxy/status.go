package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"ssh-autoproxy/internal/netstate"
	"ssh-autoproxy/internal/state"
)

type routeStatus struct {
	Name           string `json:"name"`
	JumpHost       string `json:"jump_host"`
	MatchedProfile string `json:"matched_profile"`
	ProxyNeeded    bool   `json:"proxy_needed"`
	SocksAddr      string `json:"socks_addr,omitempty"`
	TunnelUp       bool   `json:"tunnel_up"`
}

func newStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the current network match and per-route tunnel/PAC health",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(cmd)
			if err != nil {
				return err
			}
			ns, err := netstate.Query()
			if err != nil {
				return err
			}

			// The daemon records the SOCKS addresses it actually bound;
			// this process's own config load may have picked different
			// random ports. A missing/unreadable state file just falls
			// back to the config's addresses.
			var recorded map[string]string
			if st, err := state.Load(""); err == nil {
				recorded = st.SocksAddrs
			}

			routes := make([]routeStatus, 0, len(cfg.Routes))
			for _, r := range cfg.Routes {
				profile, proxyNeeded := netstate.Evaluate(r.DirectProfiles, ns)
				tunnelUp := false
				addr := ""
				if r.SocksProxy != nil {
					addr = recorded[r.Name]
					if addr == "" {
						addr = net.JoinHostPort(r.SocksProxy.Bind, strconv.Itoa(r.SocksProxy.Port))
					}
					if conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
						tunnelUp = true
						_ = conn.Close()
					}
				}
				routes = append(routes, routeStatus{
					Name:           r.Name,
					JumpHost:       r.JumpHost,
					MatchedProfile: profile,
					ProxyNeeded:    proxyNeeded,
					SocksAddr:      addr,
					TunnelUp:       tunnelUp,
				})
			}

			pacURL := ""
			pacUp := false
			if cfg.PAC.Enabled {
				pacURL = fmt.Sprintf("http://%s:%d%s", cfg.PAC.Bind, cfg.PAC.Port, cfg.PAC.Path)
				client := http.Client{Timeout: 500 * time.Millisecond}
				if resp, err := client.Get(pacURL); err == nil {
					pacUp = resp.StatusCode == http.StatusOK
					_ = resp.Body.Close()
				}
			}

			if asJSON {
				result := struct {
					SSID    string        `json:"ssid"`
					Gateway string        `json:"gateway"`
					Address string        `json:"address"`
					Routes  []routeStatus `json:"routes"`
					PACURL  string        `json:"pac_url,omitempty"`
					PACUp   bool          `json:"pac_up"`
				}{ns.SSID, ns.Gateway, ns.Address, routes, pacURL, pacUp}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "SSID:     %s\n", orNone(ns.SSID))
			fmt.Fprintf(out, "Gateway:  %s\n", orNone(ns.Gateway))
			fmt.Fprintf(out, "Address:  %s\n\n", orNone(ns.Address))

			if len(routes) == 0 {
				fmt.Fprintln(out, "No routes configured.")
			}
			for _, rs := range routes {
				fmt.Fprintf(out, "Route %q (%s)\n", rs.Name, rs.JumpHost)
				if rs.MatchedProfile != "" {
					fmt.Fprintf(out, "  Matched profile:  %s (direct)\n", rs.MatchedProfile)
				} else {
					fmt.Fprintln(out, "  Matched profile:  none (away)")
				}
				fmt.Fprintf(out, "  Proxy needed:     %v\n", rs.ProxyNeeded)
				if rs.SocksAddr != "" {
					fmt.Fprintf(out, "  SOCKS tunnel up:  %v (%s)\n", rs.TunnelUp, rs.SocksAddr)
				} else {
					fmt.Fprintf(out, "  SOCKS tunnel up:  %v\n", rs.TunnelUp)
				}
			}

			if cfg.PAC.Enabled {
				fmt.Fprintf(out, "\nPAC server up:    %v (%s)\n", pacUp, pacURL)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
