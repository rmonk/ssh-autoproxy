# ssh-autoproxy

Makes SSH `ProxyJump` (and a browser SOCKS5/PAC proxy) conditional on the
current network. Configure one or more named **routes** — each with its
own jump host, its own destinations (`host_patterns`/`host_subnets`), and
its own "direct network" profiles (WiFi SSID, gateway, subnet): a route's
destinations are reached directly while on its matching network, and
routed through its jump host otherwise. A background daemon keeps every
route's jump-host connection alive and reconnects it on failure, and pops
a (rate-limited) desktop notification if there's an SSH key/auth problem.

## Install

```
go build -o ~/.local/bin/ssh-autoproxy ./cmd/ssh-autoproxy
cp config.example.yaml ~/.config/ssh-autoproxy/config.yaml
# edit ~/.config/ssh-autoproxy/config.yaml for your own network/jump host
ssh-autoproxy install
```

`install` installs and enables the `ssh-autoproxy` systemd `--user` service,
then prints the `~/.ssh/config` snippet and browser/PAC setup instructions
for you to apply by hand — this tool never edits `~/.ssh/config` or your
browser/OS settings for you. Re-run `ssh-autoproxy install --print` any
time to reprint those instructions without touching systemd.

## Usage

```
ssh-autoproxy status           # current network match, per-route tunnel/PAC health
ssh-autoproxy check-ssh <route> # exit 0 = proxy needed, non-zero = direct, for that route
ssh-autoproxy daemon           # run the background loop in the foreground
ssh-autoproxy daemon -v        # ...and also log each SOCKS5 request being proxied
ssh-autoproxy update           # update to the latest release (checksum-verified)
ssh-autoproxy update --check   # only report whether a newer release exists
```

`check-ssh <route>` is meant to be called from an OpenSSH `Match exec`
block, one per route (see `ssh-autoproxy install --print` for the exact
snippet) — ssh_config's own `host <pattern>` criterion already dispatches
to the right route, so `check-ssh` only needs the route name, not the
destination host. It's self-contained — it queries the network directly
and works even if the daemon isn't running.

## Notes

- Each route's `socks_proxy` is optional: omit it entirely and only SSH
  ProxyJump/`check-ssh` is managed for that route; set `{ active: true }`
  to enable SOCKS5 on a random free port on `127.0.0.1` (see
  `ssh-autoproxy status` for the port picked); or give an explicit
  `bind`/`port` to use exactly those.
- The SOCKS5 proxy and PAC server always bind to loopback only
  (`127.0.0.1`) — this is a personal, single-machine proxy, not an open
  relay.
- Notifications for SSH key/auth failures are deduplicated per failure
  category with a cooldown (default 20 minutes, see `notify.cooldown` in
  the config) so a flapping connection doesn't spam you.
